package ociclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// User 是一个 IAM 用户。用于连通性校验——能成功取回自己，就说明签名链路完全打通了。
type User struct {
	ID             string    `json:"id"`
	CompartmentID  string    `json:"compartmentId"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	Email          string    `json:"email"`
	LifecycleState string    `json:"lifecycleState"`
	TimeCreated    time.Time `json:"timeCreated"`
}

// Tenancy 是租户本身。name 就是用户在控制台看到的云账户名。
type Tenancy struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	HomeRegionKey   string `json:"homeRegionKey"`
	UPICloudAccount string `json:"upiIdcsCompatibilityLayerEndpoint"`
}

// RegionSubscription 是租户已订阅的区域。只有订阅过的区域才能创建资源。
type RegionSubscription struct {
	RegionKey    string `json:"regionKey"`
	RegionName   string `json:"regionName"`
	Status       string `json:"status"`
	IsHomeRegion bool   `json:"isHomeRegion"`
}

// Compartment 是分区。免费额度用户通常只用根分区（即租户本身）。
type Compartment struct {
	ID             string    `json:"id"`
	CompartmentID  string    `json:"compartmentId"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	LifecycleState string    `json:"lifecycleState"`
	TimeCreated    time.Time `json:"timeCreated"`
}

// AvailabilityDomain 是可用域。创建实例时必须指定，且容量按 AD 独立计算。
type AvailabilityDomain struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	CompartmentID string `json:"compartmentId"`
}

// GetUser 取回指定 IAM 用户。
func (c *Client) GetUser(ctx context.Context, userOCID string) (*User, error) {
	var out User
	_, err := c.Do(ctx, Request{
		Method:  http.MethodGet,
		Service: ServiceIdentity,
		Path:    "/users/" + url.PathEscape(userOCID),
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetCurrentUser 取回本客户端凭据所属的用户。这是连通性测试的主要调用：
// 它同时验证了签名算法、密钥有效性、时钟同步和网络可达性。
func (c *Client) GetCurrentUser(ctx context.Context) (*User, error) {
	return c.GetUser(ctx, c.creds.UserOCID)
}

// GetTenancy 取回租户信息。
func (c *Client) GetTenancy(ctx context.Context) (*Tenancy, error) {
	var out Tenancy
	_, err := c.Do(ctx, Request{
		Method:  http.MethodGet,
		Service: ServiceIdentity,
		Path:    "/tenancies/" + url.PathEscape(c.creds.TenancyOCID),
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListRegionSubscriptions 列出租户已订阅的所有区域。该接口不分页。
func (c *Client) ListRegionSubscriptions(ctx context.Context) ([]RegionSubscription, error) {
	var out []RegionSubscription
	_, err := c.Do(ctx, Request{
		Method:  http.MethodGet,
		Service: ServiceIdentity,
		Path:    "/tenancies/" + url.PathEscape(c.creds.TenancyOCID) + "/regionSubscriptions",
	}, &out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListCompartments 列出指定分区下的子分区。compartmentOCID 留空则从租户根分区开始。
func (c *Client) ListCompartments(ctx context.Context, compartmentOCID string) ([]Compartment, error) {
	if compartmentOCID == "" {
		compartmentOCID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentOCID)
	query.Set("limit", "100")
	query.Set("lifecycleState", "ACTIVE")

	// 用空切片而不是 nil：nil 会被序列化成 JSON null，前端拿到 null
	// 再去 .forEach 就直接抛异常。列表接口永远返回列表。
	all := make([]Compartment, 0)
	err := listPages(ctx, c, Request{
		Method:  http.MethodGet,
		Service: ServiceIdentity,
		Path:    "/compartments",
		Query:   query,
	}, 50, func(page []byte) error {
		var batch []Compartment
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

// ListAvailabilityDomains 列出某区域下的可用域。region 留空则用客户端默认区域。
func (c *Client) ListAvailabilityDomains(ctx context.Context, region, compartmentOCID string) ([]AvailabilityDomain, error) {
	if compartmentOCID == "" {
		compartmentOCID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentOCID)

	var out []AvailabilityDomain
	_, err := c.Do(ctx, Request{
		Method:  http.MethodGet,
		Service: ServiceIdentity,
		Path:    "/availabilityDomains",
		Region:  region,
		Query:   query,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out, nil
}
