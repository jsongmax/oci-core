package billingsvc

import (
	"context"
	"strings"
	"time"

	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

// FreeEgressGB 是永久免费额度里每月的出站流量：10 TB。
const FreeEgressGB = 10 * 1024

// EgressDay 是本月出站流量按天拆开的一天。
type EgressDay struct {
	// Date 是 UTC 日期，形如 2026-10-01。
	Date string  `json:"date"`
	GB   float64 `json:"gb"`
}

// EgressReport 是一个账号本自然月（UTC）的出站流量。
//
// 与 Detail.Egress 的区别：那个跟着明细的「最近 N 天」走，跨月；
// 免费额度却是按自然月算的，要和 10 TB 比就只能按月取。
type EgressReport struct {
	AccountID string `json:"accountId"`
	Status    Status `json:"status"`
	Region    string `json:"region"`
	// Month 形如 2026-10。
	Month string `json:"month"`
	// UsedGB 是本月至今的出站流量，已按 Oracle 返回的单位折成 GB。
	UsedGB float64 `json:"usedGb"`
	FreeGB float64 `json:"freeGb"`
	// Unit 是 Oracle 原始返回的单位，换算有疑问时可对照。
	Unit   string      `json:"unit"`
	Series []EgressDay `json:"series"`
	// Amount 是出站流量产生的费用，免费额度内为 0。
	Amount    float64   `json:"amount"`
	Currency  string    `json:"currency"`
	Error     string    `json:"error,omitempty"`
	FetchedAt time.Time `json:"fetchedAt"`
}

// EgressFor 取回单个账号本月的出站流量，优先走缓存。
func (s *Service) EgressFor(ctx context.Context, acc *store.Account, refresh bool) EgressReport {
	region := billingRegion(acc)
	now := time.Now().UTC()
	start := ociclient.AlignMonth(now)
	// 左闭右开，End 取明天零点才能把今天算进去。
	end := ociclient.AlignDay(now).AddDate(0, 0, 1)

	res := EgressReport{
		AccountID: acc.ID,
		Region:    region,
		Month:     start.Format("2006-01"),
		FreeGB:    FreeEgressGB,
		FetchedAt: time.Now(),
	}

	if !acc.Enabled {
		res.Status = StatusDisabled
		return res
	}

	if !refresh {
		s.mu.Lock()
		cached, ok := s.egress[acc.ID]
		s.mu.Unlock()
		// 跨月后旧缓存是上个月的数，不能再用。
		if ok && cached.Month == res.Month && time.Since(cached.FetchedAt) < cacheTTL {
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

	// 要的是数量，所以走 USAGE；金额顺带读出来，口径与 foldSKUs 一致。
	items, err := client.SummarizeUsage(ctx, ociclient.UsageQuery{
		Start:       start,
		End:         end,
		Granularity: ociclient.GranularityDaily,
		QueryType:   ociclient.QueryTypeUsage,
		GroupBy:     []string{ociclient.GroupBySKU},
		Region:      region,
	})
	if err != nil {
		res.Status, res.Error = classify(err)
		return res
	}

	res.UsedGB, res.Amount, res.Unit, res.Currency, res.Series = foldEgress(items, start, end)
	res.Status = StatusOK

	s.mu.Lock()
	s.egress[acc.ID] = &res
	s.mu.Unlock()
	return res
}

// foldEgress 从按天 × 按计费项的条目里挑出出站流量，折成按天序列与合计。
//
// 日序列补齐没有数据的日子，理由同 foldCost。
func foldEgress(items []ociclient.UsageItem, start, end time.Time) (gb, amount float64, unit, currency string, series []EgressDay) {
	byDay := make(map[string]float64)
	for _, it := range items {
		if !isEgressSKU(it) {
			continue
		}
		v := toGB(it.Quantity(), it.Unit)
		gb += v
		amount += it.Amount()
		byDay[it.TimeUsageStarted.UTC().Format("2006-01-02")] += v
		if unit == "" {
			unit = it.Unit
		}
		if currency == "" {
			currency = it.Currency
		}
	}

	series = make([]EgressDay, 0, 31)
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		series = append(series, EgressDay{Date: key, GB: byDay[key]})
	}
	return gb, amount, unit, currency, series
}

// isEgressSKU 判断一条用量是否是出站流量。
//
// Oracle 把它按区域分成 Outbound Data Transfer Zone 1/2… 等几个计费项，
// 名字前缀一致，单位一致，可以直接相加。
func isEgressSKU(it ociclient.UsageItem) bool {
	return strings.Contains(strings.ToLower(it.SkuName), "outbound data transfer")
}

// toGB 把 Oracle 返回的流量数量折成 GB（按 1024 进制，与 10 TB = 10240 GB 的额度口径一致）。
//
// Oracle 对出站流量一般直接返回 GB；其余单位按名字换算，认不出的原样当作 GB——
// 原始单位同时回传给前端，真有出入时看得出来。
func toGB(qty float64, unit string) float64 {
	u := strings.ToUpper(strings.ReplaceAll(unit, " ", ""))
	switch {
	case strings.HasPrefix(u, "TB"), strings.Contains(u, "TERABYTE"):
		return qty * 1024
	case strings.HasPrefix(u, "MB"), strings.Contains(u, "MEGABYTE"):
		return qty / 1024
	case strings.HasPrefix(u, "KB"), strings.Contains(u, "KILOBYTE"):
		return qty / (1024 * 1024)
	case u == "B", strings.HasPrefix(u, "BYTE"):
		return qty / (1024 * 1024 * 1024)
	default:
		return qty
	}
}
