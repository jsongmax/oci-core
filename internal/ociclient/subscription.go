package ociclient

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// 付费模式。Oracle 返回的原始字符串,不做归一化——枚举值可能新增,
// 把没见过的值原样透出去也好过悄悄归到"未知"里。
//
// 只有 FREE_TRIAL 需要特殊对待；其它非空值一律当升级号。升级号实测返回的是
// PAYG，文档里写的是 PAY_AS_YOU_GO，两种都会出现。
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
	// PaymentModel 是 FREE_TRIAL / PAYG / PAY_AS_YOU_GO 之类，IDCS 等订阅上为 null。
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
// 选择规则见 pickPrimarySubscription。
func (c *Client) PrimarySubscription(ctx context.Context, region string) (*Subscription, error) {
	subs, err := c.ListSubscriptions(ctx, region)
	if err != nil {
		return nil, err
	}
	return pickPrimarySubscription(subs), nil
}

// ServiceCloudCM 是承载 OCI 计费的那类订阅。
const ServiceCloudCM = "CLOUDCM"

// pickPrimarySubscription 从租户的订阅列表里挑出决定账号性质的那条。
//
// 一个租户常有多条订阅，并且不止一条是 ACTIVE：实测有租户同时返回
// IDCS（身份域，paymentModel 为 null）和 CLOUDCM（paymentModel 为 PAYG），
// 而 IDCS 排在前面。只取第一条 ACTIVE 会选中 IDCS，账号类型就变成"未知"
// （issue #11）。所以按下面的优先级挑：
//
//  1. ACTIVE 且带付费模式的 CLOUDCM——它就是 OCI 计费那条；
//  2. ACTIVE 且带付费模式的任意订阅；
//  3. 任意 ACTIVE 订阅——已取消的订阅留在列表里会把账号误判成过期；
//  4. 列表第一条。
//
// 不看 EndDate：升级号的 CLOUDCM 订阅上见过早已过去的 EndDate，
// 而 lifecycleState 仍是 ACTIVE，EndDate 只在试用订阅上表示到期日。
func pickPrimarySubscription(subs []Subscription) *Subscription {
	if len(subs) == 0 {
		return nil
	}
	rank := func(s *Subscription) int {
		if s.LifecycleState != "ACTIVE" {
			return 0
		}
		if s.PaymentModel == "" {
			return 1
		}
		if s.ServiceName == ServiceCloudCM {
			return 3
		}
		return 2
	}
	best := &subs[0]
	for i := 1; i < len(subs); i++ {
		if rank(&subs[i]) > rank(best) {
			best = &subs[i]
		}
	}
	return best
}
