package ociclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// OCI 服务名，用于查询限额。
const (
	LimitServiceCompute      = "compute"
	LimitServiceBlockStorage = "block-storage"
	LimitServiceVCN          = "vcn"
)

// 免费额度用户最关心的几个限额名。
//
// 永久免费额度见 freetier.go——那些数字 Oracle 改过，别在这里再抄一份。
// 这几个常量让配额面板能直接对上用户心里的那笔账。
//
// 名字必须逐字对上 ListLimitDefinitions 的返回值，写错会得到
// 400 InvalidParameter 而不是空结果。带 -regional- 的是 REGION 作用域，
// 不带的是 AD 作用域——见 LimitScope。
const (
	LimitARMCores   = "standard-a1-core-regional-count"
	LimitARMMemory  = "standard-a1-memory-regional-count"
	LimitE2MicroVMs = "vm-standard-e2-1-micro-count"
	LimitStorageGB  = "total-free-storage-gb-regional"
	LimitVcnCount   = "vcn-count"
)

// LimitScope 是限额的作用域。
//
// AD 作用域的限额查用量时必须带 availabilityDomain，否则 OCI 返回
// 400 InvalidParameter；REGION 作用域的限额带了反而会报错。
type LimitScope string

const (
	ScopeRegion LimitScope = "REGION"
	ScopeAD     LimitScope = "AD"
)

// LimitValue 是某个限额在某个作用域下的配置值。
type LimitValue struct {
	Name               string `json:"name"`
	ScopeType          string `json:"scopeType"` // GLOBAL / REGION / AD
	AvailabilityDomain string `json:"availabilityDomain"`
	Value              int64  `json:"value"`
}

// ResourceAvailability 是某个限额的实际用量。
//
// Used 与 Available 都是指针：OCI 对部分限额不返回这两个字段，
// 用零值会把"未知"错误地显示成"用了 0 个"。
type ResourceAvailability struct {
	Used      *int64 `json:"used"`
	Available *int64 `json:"available"`
	// 这三个字段在响应里是 JSON 数字，不是字符串。写成字符串会让整个响应
	// 解码失败——而且失败得很隐蔽：报的是解析错误，看不出是哪个字段。
	FractionalUsage        *float64 `json:"fractionalUsage"`
	FractionalAvailability *float64 `json:"fractionalAvailability"`
	EffectiveQuotaValue    *float64 `json:"effectiveQuotaValue"`
}

// LimitDefinition 描述一个限额项的元信息。
type LimitDefinition struct {
	Name                            string `json:"name"`
	ServiceName                     string `json:"serviceName"`
	Description                     string `json:"description"`
	ScopeType                       string `json:"scopeType"`
	AreQuotasSupported              bool   `json:"areQuotasSupported"`
	IsResourceAvailabilitySupported bool   `json:"isResourceAvailabilitySupported"`
}

// ListLimitValues 列出某服务的限额配置。limitName 留空则返回全部。
func (c *Client) ListLimitValues(ctx context.Context, region, compartmentID, serviceName, limitName string) ([]LimitValue, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentID)
	query.Set("serviceName", serviceName)
	query.Set("limit", "100")
	if limitName != "" {
		query.Set("name", limitName)
	}

	// 用空切片而不是 nil：nil 会被序列化成 JSON null，前端拿到 null
	// 再去 .forEach 就直接抛异常。列表接口永远返回列表。
	all := make([]LimitValue, 0)
	err := listPages(ctx, c, Request{
		Method: http.MethodGet, Service: ServiceLimits, Path: "/limitValues",
		Region: region, Query: query,
	}, 20, func(page []byte) error {
		var batch []LimitValue
		if err := json.Unmarshal(page, &batch); err != nil {
			return err
		}
		all = append(all, batch...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return all, nil
}

// ListLimitDefinitions 列出某服务下所有限额项的定义。
func (c *Client) ListLimitDefinitions(ctx context.Context, region, compartmentID, serviceName string) ([]LimitDefinition, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentID)
	query.Set("serviceName", serviceName)
	query.Set("limit", "100")

	// 用空切片而不是 nil：nil 会被序列化成 JSON null，前端拿到 null
	// 再去 .forEach 就直接抛异常。列表接口永远返回列表。
	all := make([]LimitDefinition, 0)
	err := listPages(ctx, c, Request{
		Method: http.MethodGet, Service: ServiceLimits, Path: "/limitDefinitions",
		Region: region, Query: query,
	}, 20, func(page []byte) error {
		var batch []LimitDefinition
		if err := json.Unmarshal(page, &batch); err != nil {
			return err
		}
		all = append(all, batch...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return all, nil
}

// GetResourceAvailability 查询某个限额的已用量与可用量。
//
// availabilityDomain 只在 AD 级限额上需要；对 REGION 级限额传了反而会报错。
func (c *Client) GetResourceAvailability(ctx context.Context, region, compartmentID, serviceName, limitName, availabilityDomain string) (*ResourceAvailability, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentID)
	if availabilityDomain != "" {
		query.Set("availabilityDomain", availabilityDomain)
	}

	path := fmt.Sprintf("/services/%s/limits/%s/resourceAvailability",
		url.PathEscape(serviceName), url.PathEscape(limitName))

	var out ResourceAvailability
	_, err := c.Do(ctx, Request{
		Method: http.MethodGet, Service: ServiceLimits, Path: path,
		Region: region, Query: query,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
