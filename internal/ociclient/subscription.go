package ociclient

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// 付费模式。Oracle 返回的原始字符串,不做归一化——枚举值可能新增,
// 把没见过的值原样透出去也好过悄悄归到"未知"里。
const (
	PaymentFreeTrial = "FREE_TRIAL"
	PaymentPAYG      = "PAY_AS_YOU_GO"
)

// Subscription 是租户的订阅信息。
//
// 这是区分"试用号"与"升级号"的唯一权威来源。配额值不能用来判断:
// 试用期内的账号会拿到远高于永久免费额度的限额(实测 ARM 16 OCPU / 96 GB,
// 而 2026-06 起永久免费只有 2 / 12),看起来和升级号一模一样——直到试用到期被打回原形。
type Subscription struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	ServiceName           string `json:"serviceName"`
	ClassicSubscriptionID string `json:"classicSubscriptionId"`
	// PaymentModel 是 FREE_TRIAL / PAY_AS_YOU_GO 之类。
	PaymentModel string `json:"paymentModel"`
	// LifecycleState 常见值 ACTIVE / CANCELED。
	LifecycleState string `json:"lifecycleState"`
	// TimeCreated 是订阅建立的时刻，也就是这个甲骨文账号开户的时刻。
	//
	// 比 StartDate 精确：StartDate 是计费起始日，被抹到当天零点。
	TimeCreated *time.Time `json:"timeCreated"`
	StartDate   *time.Time `json:"startDate"`
	// EndDate 在试用订阅上就是试用到期日。
	EndDate *time.Time `json:"endDate"`
}

// IsFreeTrial 报告这是不是仍在试用期的账号。
func (s *Subscription) IsFreeTrial() bool {
	return s != nil && s.PaymentModel == PaymentFreeTrial
}

// ListSubscriptions 列出租户的订阅。
//
// 需要 organizations 服务的读权限。精简权限的 IAM 用户会拿到 401/404,
// 调用方应把失败当作"这项信息不可用",而不是账号有问题。
func (c *Client) ListSubscriptions(ctx context.Context, region string) ([]Subscription, error) {
	query := url.Values{}
	query.Set("compartmentId", c.creds.TenancyOCID)

	var out struct {
		Items []Subscription `json:"items"`
	}
	_, err := c.Do(ctx, Request{
		Method: http.MethodGet, Service: ServiceOrganizations,
		Path: "/subscriptions", Region: region, Query: query,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Items, nil
}

// PrimarySubscription 返回最能代表账号性质的那条订阅。
//
// 绝大多数租户只有一条。有多条时优先取 ACTIVE 的——已取消的订阅留在列表里
// 会把账号误判成过期。
func (c *Client) PrimarySubscription(ctx context.Context, region string) (*Subscription, error) {
	subs, err := c.ListSubscriptions(ctx, region)
	if err != nil {
		return nil, err
	}
	for i := range subs {
		if subs[i].LifecycleState == "ACTIVE" {
			return &subs[i], nil
		}
	}
	if len(subs) > 0 {
		return &subs[0], nil
	}
	return nil, nil
}
