package ociclient

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// 用量聚合的时间粒度。
const (
	GranularityDaily   = "DAILY"
	GranularityMonthly = "MONTHLY"
	GranularityTotal   = "TOTAL"
)

// 查询类型。COST 出金额，USAGE 出用量数字。
//
// 免费额度内的资源两者都有意义：USAGE 会如实返回"用了 4 个 OCPU 小时"，
// 而 COST 返回 0。所以"金额是 0"不代表"没查到数据"——
// 这两种情况在界面上必须分得开。
const (
	QueryTypeCost  = "COST"
	QueryTypeUsage = "USAGE"
)

// 分组维度。OCI 支持的远不止这几个，只登记本工具用得到的。
const (
	GroupByService = "service"
	GroupByRegion  = "region"
	GroupBySKU     = "skuName"
)

// UsageQuery 是一次用量查询的参数。
type UsageQuery struct {
	// TenantID 必须是租户根 OCID。Usage API 不支持子分区级调用。
	TenantID string
	// Start 与 End 必须对齐到 UTC 零点，否则 OCI 直接拒绝（见 AlignDay）。
	// 区间是左闭右开：要包含今天就得把 End 设成明天零点。
	Start time.Time
	End   time.Time
	// Granularity 取 GranularityDaily / Monthly / Total。
	Granularity string
	// QueryType 留空时按 COST 处理。
	QueryType string
	// GroupBy 为空表示只要总数。
	GroupBy []string
	// Region 覆盖默认区域。Usage API 的数据是租户全局的，
	// 但请求仍须发往一个**已订阅**的区域，一般用 home region。
	Region string
}

// UsageItem 是聚合结果里的一条。
//
// 金额与数量都用指针：OCI 对"这个维度没有数据"返回 null 而不是 0。
// 解成 0 会让"没查到"和"确实是零"变成同一个东西——
// 免费账号满屏都是零，这个区分丢了就没法排障。
type UsageItem struct {
	TimeUsageStarted time.Time `json:"timeUsageStarted"`
	TimeUsageEnded   time.Time `json:"timeUsageEnded"`
	ComputedAmount   *float64  `json:"computedAmount"`
	ComputedQuantity *float64  `json:"computedQuantity"`
	Currency         string    `json:"currency"`
	Service          string    `json:"service"`
	Region           string    `json:"region"`
	SkuName          string    `json:"skuName"`
	SkuPartNumber    string    `json:"skuPartNumber"`
	Unit             string    `json:"unit"`
	// Shape 只在按资源分组时才有值，这里留着是为了完整解析响应。
	Shape string `json:"shape"`
}

// Amount 返回金额，null 时给 0。
func (i UsageItem) Amount() float64 {
	if i.ComputedAmount == nil {
		return 0
	}
	return *i.ComputedAmount
}

// Quantity 返回用量，null 时给 0。
func (i UsageItem) Quantity() float64 {
	if i.ComputedQuantity == nil {
		return 0
	}
	return *i.ComputedQuantity
}

// usageResponse 是 RequestSummarizedUsages 的响应体。
type usageResponse struct {
	Items   []UsageItem `json:"items"`
	GroupBy []string    `json:"groupBy"`
}

// AlignDay 把时刻截断到 UTC 零点。
//
// 这不是可选的整洁：DAILY 粒度下 timeUsageStarted 不在零点会被 OCI 拒掉，
// 报错信息还相当含糊。所有进 UsageQuery 的时间都先过这里。
func AlignDay(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// AlignMonth 把时刻截断到 UTC 当月一号零点。MONTHLY 粒度要求对齐到月初。
func AlignMonth(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// SummarizeUsage 调用 RequestSummarizedUsages 取回聚合用量。
//
// 需要额外的 IAM 权限：`read usage-report in tenancy`。
// 照抄本工具「权限自检」里那份策略的用户已经有了（其中的
// `read all-resources in tenancy` 覆盖 usage-report），
// 但只授了 compute / network / volume 三项的用户会在这里拿到
// NotAuthorizedOrNotFound——那是正常的缺权限，不是账号坏了。
//
// 另外两件事值得记住：
//   - 数据有延迟，通常几小时到一天。做不出"实时花了多少"。
//   - 这是只读接口，不产生费用、不消耗配额。
func (c *Client) SummarizeUsage(ctx context.Context, q UsageQuery) ([]UsageItem, error) {
	if q.TenantID == "" {
		q.TenantID = c.creds.TenancyOCID
	}
	if q.Granularity == "" {
		q.Granularity = GranularityDaily
	}
	if q.QueryType == "" {
		q.QueryType = QueryTypeCost
	}
	if !q.End.After(q.Start) {
		return nil, fmt.Errorf("ociclient: 用量查询的时间区间无效 (%s ~ %s)",
			q.Start.Format(time.RFC3339), q.End.Format(time.RFC3339))
	}

	body := map[string]any{
		"tenantId":          q.TenantID,
		"timeUsageStarted":  q.Start.UTC().Format(time.RFC3339),
		"timeUsageEnded":    q.End.UTC().Format(time.RFC3339),
		"granularity":       q.Granularity,
		"queryType":         q.QueryType,
		"isAggregateByTime": false,
	}
	if len(q.GroupBy) > 0 {
		body["groupBy"] = q.GroupBy
	}

	var out usageResponse
	_, err := c.Do(ctx, Request{
		Method:  http.MethodPost,
		Service: ServiceUsage,
		Path:    "/usage",
		Region:  q.Region,
		Body:    body,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Items, nil
}
