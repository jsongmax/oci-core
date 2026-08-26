package ociclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// Identity Domains 是 OCI 在 2022 年后引入的身份层。
//
// 它和经典 IAM 是**两套东西**，这一点常被混淆：
//
//   - 经典 IAM 的 AuthenticationPolicy 只管密码长度与字符类型，
//     **没有有效期**这个概念。
//   - 密码有效期（默认 120 天）在身份域自己的 PasswordPolicy 里，
//     走 SCIM 风格的接口，端点是每个域独立的 URL。
//
// 好在身份域的 REST API 支持 OCI 请求签名，所以这里能复用同一个签名器，
// 不必再搭一套 OAuth。

// Domain 是一个身份域。
type Domain struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	// URL 是区域无关的域端点，形如 https://idcs-xxxx.identity.oraclecloud.com。
	// 所有 /admin/v1/ 接口都挂在它下面。
	URL string `json:"url"`
	// HomeRegionURL 是绑定到 home region 的地址。两者通常都能用，
	// 优先用 URL——它不受区域影响。
	HomeRegionURL  string `json:"homeRegionUrl"`
	HomeRegion     string `json:"homeRegion"`
	Type           string `json:"type"`
	LicenseType    string `json:"licenseType"`
	LifecycleState string `json:"lifecycleState"`
}

// Endpoint 返回该域可用的基础地址，优先区域无关的那个。
func (d Domain) Endpoint() string {
	if d.URL != "" {
		return d.URL
	}
	return d.HomeRegionURL
}

// IsDefault 判断是否为默认域。
//
// 一个租户可以有多个身份域，但绝大多数情况只有 Default 那一个，
// 而密码策略要改的就是它。
func (d Domain) IsDefault() bool { return d.Type == "DEFAULT" }

// ListDomains 列出租户下的身份域。
//
// compartmentID 必须是租户根 OCID——身份域挂在租户上，不在子分区里。
// 经典（未迁移到身份域）的租户会返回空列表，那不是错误：那种租户
// 压根没有密码有效期这回事。
func (c *Client) ListDomains(ctx context.Context, region, compartmentID string) ([]Domain, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentID)
	query.Set("lifecycleState", "ACTIVE")
	query.Set("limit", "100")

	out := make([]Domain, 0)
	_, err := c.Do(ctx, Request{
		Method:  http.MethodGet,
		Service: ServiceIdentity,
		Path:    "/domains",
		Region:  region,
		Query:   query,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PasswordPolicy 是身份域的密码策略。
//
// 字段远不止这些（官方有六十多个），这里只解析本工具会展示或修改的部分。
// 未列出的字段在 PATCH 时不受影响——SCIM 的 PATCH 只动指名的属性。
//
// 数值字段一律用指针：0 和"未设置"在这里是两回事，而 passwordExpiresAfter
// 恰恰可能用 0 表示不过期。解成 0 就把两者揉平了。
type PasswordPolicy struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// PasswordExpiresAfter 是密码多少天后自动过期。
	//
	// 新租户的默认值是 120。Oracle **没有文档说明**哪个值代表"永不过期"，
	// 所以本工具的做法是：改完之后回读一次，把服务端实际存下的值显示出来，
	// 而不是假定写进去的就是生效的。
	PasswordExpiresAfter  *int `json:"passwordExpiresAfter,omitempty"`
	PasswordExpireWarning *int `json:"passwordExpireWarning,omitempty"`
	MinLength             *int `json:"minLength,omitempty"`
	MaxLength             *int `json:"maxLength,omitempty"`
	MinUpperCase          *int `json:"minUpperCase,omitempty"`
	MinLowerCase          *int `json:"minLowerCase,omitempty"`
	MinNumerals           *int `json:"minNumerals,omitempty"`
	MinSpecialChars       *int `json:"minSpecialChars,omitempty"`
	NumPasswordsInHistory *int `json:"numPasswordsInHistory,omitempty"`
	MaxIncorrectAttempts  *int `json:"maxIncorrectAttempts,omitempty"`
	LockoutDuration       *int `json:"lockoutDuration,omitempty"`
	// Priority 决定多条策略的优先级，数字小的优先。
	Priority *int `json:"priority,omitempty"`
}

// scimList 是 SCIM 列表响应的外壳。
type scimList[T any] struct {
	Schemas      []string `json:"schemas"`
	TotalResults int      `json:"totalResults"`
	Resources    []T      `json:"Resources"`
}

const (
	// scimPatchSchema 是 SCIM PatchOp 的 schema 标识，PATCH 请求体必须带。
	scimPatchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	// adminV1 是身份域管理接口的路径前缀。
	adminV1 = "/admin/v1"
)

// ListPasswordPolicies 取回该域下的全部密码策略。
//
// 通常只有一条（名为 Default）。多条时按 Priority 排，数字小的先生效。
func (c *Client) ListPasswordPolicies(ctx context.Context, domainURL string) ([]PasswordPolicy, error) {
	var out scimList[PasswordPolicy]
	_, err := c.Do(ctx, Request{
		Method:  http.MethodGet,
		BaseURL: domainURL,
		Path:    adminV1 + "/PasswordPolicies",
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Resources == nil {
		out.Resources = []PasswordPolicy{}
	}
	return out.Resources, nil
}

// GetPasswordPolicy 按 id 取一条策略。改完之后用它回读确认。
func (c *Client) GetPasswordPolicy(ctx context.Context, domainURL, id string) (*PasswordPolicy, error) {
	var out PasswordPolicy
	_, err := c.Do(ctx, Request{
		Method:  http.MethodGet,
		BaseURL: domainURL,
		Path:    adminV1 + "/PasswordPolicies/" + url.PathEscape(id),
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// scimOp 是一次 SCIM PATCH 操作。
type scimOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// SetPasswordExpiry 修改密码有效期，返回**回读**到的策略。
//
// 用 PATCH 而不是 PUT：PUT 要回传整个资源，而这个资源有六十多个字段，
// 少传一个就可能被清掉。PATCH 只动指名的那一个属性，其余原样不动。
//
// days 为 nil 表示尝试移除该属性（SCIM 的 remove），意图是"不再过期"。
// **Oracle 没有文档说明哪种做法代表永不过期**，所以这里不做任何承诺：
// 改完立刻回读，由调用方把服务端的真实状态展示给用户。写进去什么
// 和最终生效什么，是两件事。
func (c *Client) SetPasswordExpiry(ctx context.Context, domainURL, id string, days *int) (*PasswordPolicy, error) {
	op := scimOp{Op: "replace", Path: "passwordExpiresAfter"}
	if days == nil {
		op = scimOp{Op: "remove", Path: "passwordExpiresAfter"}
	} else {
		if *days < 0 {
			return nil, fmt.Errorf("ociclient: 密码有效期不能为负: %d", *days)
		}
		op.Value = *days
	}

	body := map[string]any{
		"schemas":    []string{scimPatchSchema},
		"Operations": []scimOp{op},
	}

	_, err := c.Do(ctx, Request{
		Method:  http.MethodPatch,
		BaseURL: domainURL,
		Path:    adminV1 + "/PasswordPolicies/" + url.PathEscape(id),
		Body:    body,
	}, nil)
	if err != nil {
		return nil, err
	}

	// 回读。PATCH 的响应体各实现返回得不一致，而且我们要确认的是
	// "服务端最终存成了什么"，不是"我们发过去了什么"。
	return c.GetPasswordPolicy(ctx, domainURL, id)
}
