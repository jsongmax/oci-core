package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

// quotaTTL 是配额结果的缓存时长。
//
// 配额变化很慢（只有创建/删除资源时才变），但每次查询要发 3–4 个 API 调用。
// 缓存 5 分钟能让配额面板秒开，代价是刚创建完实例时可能看到旧数字——
// 这点延迟远比每次刷新都等两秒可接受。
const quotaTTL = 5 * time.Minute

// QuotaItem 是一项配额的用量。
type QuotaItem struct {
	// Key 是稳定的语义标识（ocpu/memory/block/micro），前端按它取值。
	//
	// 前端一度直接匹配 Name 里的 OCI 限额名，结果限额名一改就整片显示成
	// 0/0——而且不报错，看起来像"配额真的是零"。Key 把这层耦合切断。
	Key   string `json:"key"`
	Name  string `json:"name"`
	Label string `json:"label"`
	Used  int64  `json:"used"`
	Limit int64  `json:"limit"`
	// Known 为 false 表示这项配额没查到（权限不足或该区域不支持）。
	// 前端应显示"未知"而不是"0"——把未知画成 0 会让用户以为还有额度。
	Known bool `json:"known"`
	// Error 记录查不到的原因。静默吞掉会让"没权限"和"配额真的是 0"
	// 长得一模一样，排障时无从下手。
	Error string `json:"error,omitempty"`
	// Unlimited 表示这项没有实际上限。
	//
	// 升级号（PAYG）的 ARM 限额，Oracle 返回的是 100000000——一亿核。
	// 那不是真实上限，是"不限"的哨兵值。照着画进度条永远是 0%，
	// 还会把"2 / 100000000 OCPU"这么长一串塞进卡片，把整行挤散。
	Unlimited bool `json:"unlimited"`
}

// unlimitedThreshold 是判定"哨兵值"的门槛。
//
// 取一百万：真实的服务限额都在几十到几千的量级，没有哪个租户会有
// 一百万个 OCPU 或一百万 GB 内存。用门槛而不是精确匹配 100000000，
// 是因为 Oracle 未必在每个服务上都用同一个数。
const unlimitedThreshold int64 = 1_000_000

// AccountQuota 是一个账号的配额汇总。
type AccountQuota struct {
	AccountID string      `json:"accountId"`
	Region    string      `json:"region"`
	Items     []QuotaItem `json:"items"`
	Error     string      `json:"error,omitempty"`
	FetchedAt time.Time   `json:"fetchedAt"`
}

// quotaCache 是进程内的配额缓存。
type quotaCache struct {
	mu      sync.Mutex
	entries map[string]*AccountQuota
}

func newQuotaCache() *quotaCache {
	return &quotaCache{entries: make(map[string]*AccountQuota)}
}

func (c *quotaCache) get(key string) (*AccountQuota, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Since(entry.FetchedAt) > quotaTTL {
		return nil, false
	}
	return entry, true
}

func (c *quotaCache) put(key string, q *AccountQuota) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = q
}

// quotaSpecs 是要查询的配额项。
//
// 只查免费额度用户真正关心的这几项：全量拉限额定义会返回上百条，
// 绝大多数与这个工具的使用场景无关。
var quotaSpecs = []struct {
	key     string
	service string
	limit   string
	label   string
	scope   ociclient.LimitScope
}{
	{"ocpu", ociclient.LimitServiceCompute, ociclient.LimitARMCores, "ARM OCPU", ociclient.ScopeRegion},
	{"memory", ociclient.LimitServiceCompute, ociclient.LimitARMMemory, "ARM 内存 (GB)", ociclient.ScopeRegion},
	{"micro", ociclient.LimitServiceCompute, ociclient.LimitE2MicroVMs, "AMD 微型实例", ociclient.ScopeAD},
	{"block", ociclient.LimitServiceBlockStorage, ociclient.LimitStorageGB, "块存储 · 免费额度 (GB)", ociclient.ScopeRegion},
}

func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	refresh := r.URL.Query().Get("refresh") == "true"

	var accounts []store.Account
	if id := r.URL.Query().Get("accountId"); id != "" {
		acc, err := s.st.GetAccount(ctx, id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		accounts = []store.Account{*acc}
	} else {
		all, err := s.st.ListAccounts(ctx)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		accounts = all
	}

	results := make([]AccountQuota, len(accounts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)

	for i := range accounts {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx] = s.accountQuota(ctx, &accounts[idx], refresh)
		}(i)
	}
	wg.Wait()

	writeJSON(w, http.StatusOK, map[string]any{"quotas": results})
}

// accountQuota 取回单个账号的配额，优先走缓存。
func (s *Server) accountQuota(ctx context.Context, acc *store.Account, refresh bool) AccountQuota {
	region := acc.HomeRegion
	if region == "" {
		region = acc.DefaultRegion
	}
	key := acc.ID + "|" + region

	if !refresh {
		if cached, ok := s.quotas.get(key); ok {
			return *cached
		}
	}

	result := AccountQuota{AccountID: acc.ID, Region: region, FetchedAt: time.Now()}
	if !acc.Enabled {
		result.Error = "账号已禁用"
		return result
	}

	client, err := s.conns.For(ctx, acc)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 各项配额之间没有依赖，并发查完再汇总。
	items := make([]QuotaItem, len(quotaSpecs))
	var wg sync.WaitGroup
	for i, spec := range quotaSpecs {
		wg.Add(1)
		go func(idx int, key, service, limit, label string, scope ociclient.LimitScope) {
			defer wg.Done()
			item := fetchQuotaItem(ctx, client, region, acc.CompartmentOCID,
				service, limit, label, scope)
			item.Key = key
			items[idx] = item
		}(i, spec.key, spec.service, spec.limit, spec.label, spec.scope)
	}
	wg.Wait()

	result.Items = items
	s.quotas.put(key, &result)
	return result
}

// handleInstanceMetrics 返回实例的监控数据。
func (s *Server) handleInstanceMetrics(w http.ResponseWriter, r *http.Request) {
	inst, err := s.st.GetInstance(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	client, acc, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}

	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 || hours > 24*30 {
		hours = 6
	}
	span := time.Duration(hours) * time.Hour
	end := time.Now()
	start := end.Add(-span)
	resolution := ociclient.ResolutionFor(span)

	metrics := splitCSV(r.URL.Query().Get("metrics"))
	if len(metrics) == 0 {
		metrics = []string{
			ociclient.MetricCPUUtilization,
			ociclient.MetricMemoryUtilization,
			ociclient.MetricNetworkBytesIn,
			ociclient.MetricNetworkBytesOut,
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	type seriesOut struct {
		Metric      string                `json:"metric"`
		Aggregation string                `json:"aggregation"`
		Datapoints  []ociclient.Datapoint `json:"datapoints"`
		Error       string                `json:"error,omitempty"`
	}

	out := make([]seriesOut, len(metrics))
	var wg sync.WaitGroup
	for i, metric := range metrics {
		wg.Add(1)
		go func(idx int, metric string) {
			defer wg.Done()
			agg := ociclient.DefaultAggregationFor(metric)
			entry := seriesOut{Metric: metric, Aggregation: agg}

			series, err := client.SummarizeMetrics(ctx, ociclient.MetricQuery{
				CompartmentID: orText(acc.CompartmentOCID, acc.TenancyOCID),
				Region:        inst.Region,
				Namespace:     ociclient.NamespaceComputeAgent,
				Query:         ociclient.InstanceMetricQuery(metric, inst.ID, resolution, agg),
				StartTime:     start,
				EndTime:       end,
				Resolution:    resolution,
			})
			if err != nil {
				entry.Error = shortOCIError(err)
			} else if len(series) > 0 {
				entry.Datapoints = series[0].AggregatedDatapoints
			}
			out[idx] = entry
		}(i, metric)
	}
	wg.Wait()

	writeJSON(w, http.StatusOK, map[string]any{
		"instanceId": inst.ID,
		"start":      start,
		"end":        end,
		"resolution": resolution,
		"series":     out,
		// 没装 Oracle Cloud Agent 的实例查不到任何数据，接口会正常返回空序列。
		// 前端要能区分"没数据"与"调用失败"，因此这里给一句说明。
		"notice": "监控数据依赖实例内运行的 Oracle Cloud Agent。若所有序列均为空，请确认该服务已启用。",
	})
}

// fetchQuotaItem 查一项配额的上限与已用量。
//
// 先用 ListLimitValues 拿上限：它对两种作用域都有效，且顺带给出 AD 列表。
// 再用 GetResourceAvailability 拿用量——这一步 AD 作用域的限额必须逐个 AD
// 查再求和，不带 AD 会被拒；REGION 作用域反过来，带了 AD 才会被拒。
func fetchQuotaItem(ctx context.Context, client *ociclient.Client,
	region, compartmentID, service, limit, label string, scope ociclient.LimitScope) QuotaItem {

	item := QuotaItem{Name: limit, Label: label}

	values, err := client.ListLimitValues(ctx, region, compartmentID, service, limit)
	if err != nil {
		item.Error = shortOCIError(err)
		return item
	}
	if len(values) == 0 {
		item.Error = "该区域没有这项限额"
		return item
	}

	// AD 作用域的限额是每个 AD 各一份配额，总量要相加；
	// REGION 作用域只会返回一条。
	domains := make([]string, 0, len(values))
	for _, v := range values {
		item.Limit += v.Value
		if v.AvailabilityDomain != "" {
			domains = append(domains, v.AvailabilityDomain)
		}
	}
	// 拿到上限就算"已知"：用量查不到时显示 "? / 上限" 也远好过整项空白。
	item.Known = true
	if item.Limit >= unlimitedThreshold {
		item.Unlimited = true
	}

	if scope != ociclient.ScopeAD {
		domains = []string{""}
	} else if len(domains) == 0 {
		item.Error = "限额未返回可用区，无法查询用量"
		return item
	}

	var (
		mu       sync.Mutex
		usedSum  int64
		usedOK   bool
		firstErr string
		wg       sync.WaitGroup
	)
	for _, ad := range domains {
		wg.Add(1)
		go func(ad string) {
			defer wg.Done()
			avail, err := client.GetResourceAvailability(ctx, region, compartmentID, service, limit, ad)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == "" {
					firstErr = shortOCIError(err)
				}
				return
			}
			if avail != nil && avail.Used != nil {
				usedSum += *avail.Used
				usedOK = true
			}
		}(ad)
	}
	wg.Wait()

	if usedOK {
		item.Used = usedSum
	} else if firstErr != "" {
		// 上限是可信的，用量不是——把原因留下，但不要因此把整项标成未知。
		item.Error = "用量查询失败: " + firstErr
	}
	return item
}

func shortOCIError(err error) string {
	if apiErr, ok := ociclient.AsAPIError(err); ok {
		if apiErr.Code != "" {
			return apiErr.Code + " · " + apiErr.Message
		}
		return apiErr.Message
	}
	return err.Error()
}
