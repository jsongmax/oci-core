// Package billingsvc 汇总各账号的用量与费用。
//
// 数据来自 OCI Usage API（RequestSummarizedUsages），只读，不产生费用。
// 它需要一项本工具其余功能都用不到的权限：read usage-report in tenancy。
// 缺这项权限是**正常情况**而不是账号故障——很多人为本工具单独建的 IAM 用户
// 只授了 compute / network / volume 三项。因此这里把「没权限」做成一等状态，
// 有独立的文案和处理建议，绝不混进泛泛的错误里。
//
// 还有一件事贯穿整个包：这个面板的用户大多是免费额度账号，金额恒为 0。
// 一屏的 0.00 看起来和「功能坏了」一模一样，所以查得到数据但金额为零时，
// 状态是 StatusFree，界面转而展示**用量**（用了多少 OCPU 小时、多少 GB），
// 那才是免费号用户真正关心的数字。
package billingsvc

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ocicore/internal/ociclient"
	"ocicore/internal/ociconn"
	"ocicore/internal/store"
)

// Status 是一个账号账单数据的取回结果。
type Status string

const (
	// StatusOK 查到了费用，且金额大于零。
	StatusOK Status = "ok"
	// StatusFree 查到了数据但金额为零——免费额度内，不是错误。
	StatusFree Status = "free"
	// StatusNoPermission 缺 read usage-report 权限。
	//
	// OCI 对「无权限」和「不存在」返回同一个错误码，无法区分；
	// 但这个接口的资源是租户自己，不可能不存在，所以一律当作缺权限。
	StatusNoPermission Status = "no_permission"
	// StatusDisabled 账号在本面板里被停用了。
	StatusDisabled Status = "disabled"
	// StatusError 其余失败。
	StatusError Status = "error"
)

// cacheTTL 是账单结果的缓存时长。
//
// 取 30 分钟：Usage API 的数据本身就有几小时到一天的延迟，
// 刷得再勤也变不出新数字，只是白白多打 Oracle 的接口。
const cacheTTL = 30 * time.Minute

// queryTimeout 是单个账号一次取数的上限。
const queryTimeout = 25 * time.Second

// Deps 是构造 Service 所需的依赖。
type Deps struct {
	Store *store.Store
	Conns *ociconn.Factory
}

// Service 提供账单查询，带进程内缓存。
type Service struct {
	st    *store.Store
	conns *ociconn.Factory

	mu      sync.Mutex
	sums    map[string]*AccountSummary
	details map[string]*Detail
}

// New 构造服务。
func New(d Deps) *Service {
	return &Service{
		st:      d.Store,
		conns:   d.Conns,
		sums:    make(map[string]*AccountSummary),
		details: make(map[string]*Detail),
	}
}

// AccountSummary 是一个账号的账单概况：本月与上月各花了多少。
type AccountSummary struct {
	AccountID string `json:"accountId"`
	Status    Status `json:"status"`
	// Currency 是 Oracle 返回的币种。不同账号可能不同币种，
	// 跨账号求和前必须按它分组——把 USD 和 CNY 加在一起是纯粹的错误。
	Currency  string  `json:"currency"`
	ThisMonth float64 `json:"thisMonth"`
	LastMonth float64 `json:"lastMonth"`
	// Region 是实际发出请求的区域，排障时要看。
	Region string `json:"region"`
	// Error 保留原始错误文本。StatusNoPermission 时为空——那不是错误。
	Error     string    `json:"error,omitempty"`
	FetchedAt time.Time `json:"fetchedAt"`
}

// Bucket 是按某个维度（服务 / 区域）分组后的一项。
type Bucket struct {
	Key    string  `json:"key"`
	Amount float64 `json:"amount"`
	// Quantity 与 Unit 来自 USAGE 查询。免费额度账号金额恒为零，
	// 这两个字段才是有信息量的那一半。
	Quantity float64 `json:"quantity"`
	Unit     string  `json:"unit"`
}

// DayPoint 是日趋势里的一天。
type DayPoint struct {
	// Date 是 UTC 日期，形如 2026-08-20。
	Date   string  `json:"date"`
	Amount float64 `json:"amount"`
}

// Detail 是单个账号的账单明细。
type Detail struct {
	AccountID string     `json:"accountId"`
	Status    Status     `json:"status"`
	Currency  string     `json:"currency"`
	Region    string     `json:"region"`
	Start     time.Time  `json:"start"`
	End       time.Time  `json:"end"`
	Days      int        `json:"days"`
	Total     float64    `json:"total"`
	Series    []DayPoint `json:"series"`
	Services  []Bucket   `json:"services"`
	Regions   []Bucket   `json:"regions"`
	// Usage 是同一区间按服务分组的**用量**，与 Services 一一对应但看的是数量。
	// 免费号唯一有内容的就是这块。
	Usage     []Bucket  `json:"usage"`
	Error     string    `json:"error,omitempty"`
	FetchedAt time.Time `json:"fetchedAt"`
}

/* ---------- 概况 ---------- */

// Summaries 并发取回全部账号的账单概况。
func (s *Service) Summaries(ctx context.Context, accounts []store.Account, refresh bool) []AccountSummary {
	out := make([]AccountSummary, len(accounts))
	var wg sync.WaitGroup
	// 限并发到 4：和配额查询同一个量级，别让「打开账单页」变成一次对 Oracle 的突刺。
	sem := make(chan struct{}, 4)

	for i := range accounts {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[idx] = s.Summary(ctx, &accounts[idx], refresh)
		}(i)
	}
	wg.Wait()
	return out
}

// Summary 取回单个账号的账单概况，优先走缓存。
func (s *Service) Summary(ctx context.Context, acc *store.Account, refresh bool) AccountSummary {
	region := billingRegion(acc)
	res := AccountSummary{
		AccountID: acc.ID,
		Region:    region,
		FetchedAt: time.Now(),
	}

	if !acc.Enabled {
		res.Status = StatusDisabled
		return res
	}
	if !refresh {
		s.mu.Lock()
		cached, ok := s.sums[acc.ID]
		s.mu.Unlock()
		if ok && time.Since(cached.FetchedAt) < cacheTTL {
			return *cached
		}
	}

	client, err := s.conns.For(ctx, acc)
	if err != nil {
		res.Status = StatusError
		res.Error = err.Error()
		return res
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	// 一次查两个月：MONTHLY 粒度从上月一号到下月一号，返回两条。
	// 分两次查是白白多打一次接口。
	now := time.Now().UTC()
	start := ociclient.AlignMonth(now).AddDate(0, -1, 0)
	end := ociclient.AlignMonth(now).AddDate(0, 1, 0)

	items, err := client.SummarizeUsage(ctx, ociclient.UsageQuery{
		Start:       start,
		End:         end,
		Granularity: ociclient.GranularityMonthly,
		QueryType:   ociclient.QueryTypeCost,
		Region:      region,
	})
	if err != nil {
		res.Status, res.Error = classify(err)
		return res
	}

	thisMonth := ociclient.AlignMonth(now)
	for _, it := range items {
		if res.Currency == "" {
			res.Currency = it.Currency
		}
		// 按条目自身的起始时刻归月，不靠返回顺序——顺序没有承诺。
		if !it.TimeUsageStarted.UTC().Before(thisMonth) {
			res.ThisMonth += it.Amount()
		} else {
			res.LastMonth += it.Amount()
		}
	}

	res.Status = StatusOK
	if res.ThisMonth == 0 && res.LastMonth == 0 {
		res.Status = StatusFree
	}

	s.mu.Lock()
	s.sums[acc.ID] = &res
	s.mu.Unlock()
	return res
}

/* ---------- 明细 ---------- */

// DetailFor 取回单个账号最近 days 天的账单明细。
func (s *Service) DetailFor(ctx context.Context, acc *store.Account, days int, refresh bool) Detail {
	if days <= 0 || days > 180 {
		days = 30
	}
	region := billingRegion(acc)
	now := time.Now().UTC()
	// 左闭右开，End 取明天零点才能把今天算进去。
	end := ociclient.AlignDay(now).AddDate(0, 0, 1)
	start := end.AddDate(0, 0, -days)

	res := Detail{
		AccountID: acc.ID,
		Region:    region,
		Start:     start,
		End:       end,
		Days:      days,
		FetchedAt: time.Now(),
	}

	if !acc.Enabled {
		res.Status = StatusDisabled
		return res
	}

	cacheKey := acc.ID + "|" + strconv.Itoa(days)
	if !refresh {
		s.mu.Lock()
		cached, ok := s.details[cacheKey]
		s.mu.Unlock()
		if ok && time.Since(cached.FetchedAt) < cacheTTL {
			return *cached
		}
	}

	client, err := s.conns.For(ctx, acc)
	if err != nil {
		res.Status = StatusError
		res.Error = err.Error()
		return res
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	// 三次查询彼此独立，并发发出。
	//
	// 按服务分组的那次一举两得：既能按天求和得到趋势，又能按服务求和得到构成。
	// 单独再查一次「不分组的每日总额」是多余的一趟。
	var (
		costItems, regionItems, usageItems []ociclient.UsageItem
		costErr, regionErr, usageErr       error
		wg                                 sync.WaitGroup
	)

	wg.Add(3)
	go func() {
		defer wg.Done()
		costItems, costErr = client.SummarizeUsage(ctx, ociclient.UsageQuery{
			Start: start, End: end,
			Granularity: ociclient.GranularityDaily,
			QueryType:   ociclient.QueryTypeCost,
			GroupBy:     []string{ociclient.GroupByService},
			Region:      region,
		})
	}()
	go func() {
		defer wg.Done()
		regionItems, regionErr = client.SummarizeUsage(ctx, ociclient.UsageQuery{
			Start: start, End: end,
			Granularity: ociclient.GranularityTotal,
			QueryType:   ociclient.QueryTypeCost,
			GroupBy:     []string{ociclient.GroupByRegion},
			Region:      region,
		})
	}()
	go func() {
		defer wg.Done()
		usageItems, usageErr = client.SummarizeUsage(ctx, ociclient.UsageQuery{
			Start: start, End: end,
			Granularity: ociclient.GranularityTotal,
			QueryType:   ociclient.QueryTypeUsage,
			GroupBy:     []string{ociclient.GroupByService},
			Region:      region,
		})
	}()
	wg.Wait()

	// 费用那次是主查询，它失败才算整体失败；另外两次只是补充维度。
	if costErr != nil {
		res.Status, res.Error = classify(costErr)
		return res
	}

	res.Series, res.Services, res.Total, res.Currency = foldCost(costItems, start, end)
	if regionErr == nil {
		res.Regions = foldBuckets(regionItems, false)
	}
	if usageErr == nil {
		res.Usage = foldBuckets(usageItems, true)
	}

	res.Status = StatusOK
	if res.Total == 0 {
		res.Status = StatusFree
	}

	s.mu.Lock()
	s.details[cacheKey] = &res
	s.mu.Unlock()
	return res
}

/* ---------- 聚合 ---------- */

// foldCost 把按天 × 按服务的条目折成日趋势与服务构成。
//
// 日趋势要补齐没有数据的那些天：Oracle 只返回有用量的日子，
// 直接画出来会把 8 月 3 日和 8 月 9 日排成相邻两根柱子，
// 让一条断断续续的曲线看起来是连续的。
func foldCost(items []ociclient.UsageItem, start, end time.Time) ([]DayPoint, []Bucket, float64, string) {
	byDay := make(map[string]float64)
	byService := make(map[string]float64)
	var total float64
	var currency string

	for _, it := range items {
		if currency == "" {
			currency = it.Currency
		}
		amount := it.Amount()
		total += amount
		byDay[it.TimeUsageStarted.UTC().Format("2006-01-02")] += amount
		byService[serviceName(it.Service)] += amount
	}

	series := make([]DayPoint, 0, 32)
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		series = append(series, DayPoint{Date: key, Amount: byDay[key]})
	}

	buckets := make([]Bucket, 0, len(byService))
	for k, v := range byService {
		buckets = append(buckets, Bucket{Key: k, Amount: v})
	}
	sortBuckets(buckets, false)

	return series, buckets, total, currency
}

// foldBuckets 把条目按维度折叠。byQuantity 为真时按用量排序并保留单位。
func foldBuckets(items []ociclient.UsageItem, byQuantity bool) []Bucket {
	type acc struct {
		amount   float64
		quantity float64
		unit     string
	}
	m := make(map[string]*acc)

	for _, it := range items {
		key := it.Region
		if byQuantity || key == "" {
			key = serviceName(it.Service)
		}
		if key == "" {
			key = "未分类"
		}
		e := m[key]
		if e == nil {
			e = &acc{}
			m[key] = e
		}
		e.amount += it.Amount()
		e.quantity += it.Quantity()
		if e.unit == "" {
			e.unit = it.Unit
		}
	}

	out := make([]Bucket, 0, len(m))
	for k, v := range m {
		// 用量为零的服务不值得占一行——免费号里这种条目能有几十个。
		if byQuantity && v.quantity == 0 {
			continue
		}
		out = append(out, Bucket{Key: k, Amount: v.amount, Quantity: v.quantity, Unit: v.unit})
	}
	sortBuckets(out, byQuantity)
	return out
}

// sortBuckets 按金额（或用量）降序，同值时按名字排，保证顺序稳定。
func sortBuckets(b []Bucket, byQuantity bool) {
	sort.Slice(b, func(i, j int) bool {
		x, y := b[i].Amount, b[j].Amount
		if byQuantity {
			x, y = b[i].Quantity, b[j].Quantity
		}
		if x != y {
			return x > y
		}
		return b[i].Key < b[j].Key
	})
}

// serviceName 归一化服务名。
//
// Oracle 在 Usage API 里返回的是 "Compute" / "Block Storage" 这种标题式写法，
// 而不是文档里常见的 COMPUTE / BLOCK_STORAGE。查表前必须先归一化大小写与
// 分隔符，否则一条都匹配不上——界面上不会报错，只是永远显示英文原名，
// 看起来像"就是没做翻译"。
func serviceName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "未分类"
	}
	key := strings.ToUpper(s)
	key = strings.ReplaceAll(key, " ", "_")
	key = strings.ReplaceAll(key, "-", "_")
	if label, ok := serviceLabels[key]; ok {
		return label
	}
	// 表里没有就原样显示 Oracle 的名字，不要退回"未分类"——
	// 那会把几个不同的服务糊成一行。
	return s
}

// serviceLabels 把常见的服务标识翻成中文。表里没有的原样显示——
// Oracle 有上百个服务，这里只覆盖这个工具的用户实际会碰到的那几个。
//
// 键一律写成全大写下划线形式，由 serviceName 负责把 Oracle 的实际写法
// （"Block Storage"）归一化过来。
var serviceLabels = map[string]string{
	"COMPUTE":              "计算",
	"BLOCK_STORAGE":        "块存储",
	"OBJECT_STORAGE":       "对象存储",
	"VCN":                  "网络",
	"NETWORK":              "网络",
	"LOAD_BALANCER":        "负载均衡",
	"DATABASE":             "数据库",
	"AUTONOMOUS_DATABASE":  "自治数据库",
	"MONITORING":           "监控",
	"NOTIFICATION_SERVICE": "通知",
	"VAULT":                "密钥管理",
	"FILE_STORAGE":         "文件存储",
}

// classify 把 OCI 错误映射成账单状态。
//
// NotAuthorized 单独拎出来：这个接口的资源就是租户自己，不可能「不存在」，
// 所以只可能是缺 read usage-report 权限。它不是账号故障，
// 界面要给的是一段可照抄的策略，而不是一句「出错了」。
func classify(err error) (Status, string) {
	if apiErr, ok := ociclient.AsAPIError(err); ok {
		if apiErr.Class == ociclient.ClassNotAuthorized {
			return StatusNoPermission, ""
		}
		if apiErr.Code != "" {
			return StatusError, apiErr.Code + " · " + apiErr.Message
		}
		return StatusError, apiErr.Message
	}
	return StatusError, err.Error()
}

// billingRegion 选发请求的区域：优先 home region。
//
// Usage API 的数据是租户全局的，发去哪个已订阅的区域结果都一样；
// 但发去**未订阅**的区域会被拒，而 home region 是唯一保证订阅了的。
func billingRegion(acc *store.Account) string {
	if acc.HomeRegion != "" {
		return acc.HomeRegion
	}
	return acc.DefaultRegion
}
