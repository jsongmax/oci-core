package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

// 密码有效期。
//
// 新租户的身份域默认 120 天到期，到期必须重置。管着几个只是"留着别被回收"
// 的账号时，这意味着每隔几个月就要挨个登进去改一次密码——而且一旦忘了，
// 下次想进去还得先走找回流程。
//
// 关掉它并不是在降低安全性：NIST SP 800-63B 早就不推荐强制定期轮换了，
// 理由是它逼着人用可预测的变体。真正管用的是强密码加多因子，那两样这个
// 面板都在别处强调过。

// policyTimeout 是单次身份域调用的上限。
const policyTimeout = 25 * time.Second

// PasswordPolicyView 是返回给前端的密码策略。
type PasswordPolicyView struct {
	AccountID string `json:"accountId"`
	// Supported 为 false 表示这个租户没有身份域（老的经典 IAM 租户），
	// 压根没有"密码有效期"这个概念。此时不是错误，界面该说清楚而不是报错。
	Supported bool `json:"supported"`
	// DomainName / PolicyID 排障时要用：一个租户可能有多个域、多条策略。
	DomainName string `json:"domainName,omitempty"`
	DomainURL  string `json:"domainUrl,omitempty"`
	PolicyID   string `json:"policyId,omitempty"`
	PolicyName string `json:"policyName,omitempty"`

	// ExpiresAfterDays 为 nil 表示该属性不存在——按 Oracle 的语义那应当
	// 就是"不过期"，但**官方没有明文**，所以这里只如实转述，由界面把
	// 这层不确定说给用户听。
	ExpiresAfterDays *int `json:"expiresAfterDays"`
	WarnBeforeDays   *int `json:"warnBeforeDays,omitempty"`
	MinLength        *int `json:"minLength,omitempty"`

	Error string `json:"error,omitempty"`
}

// resolvePolicy 找到该账号的身份域与密码策略。
//
// 两步：先 ListDomains 拿到默认域的地址，再在那个域下列出密码策略。
// 域列表为空说明这是个没迁移到身份域的老租户，直接返回 Supported=false。
func (s *Server) resolvePolicy(ctx context.Context, acc *store.Account) (
	client *ociclient.Client, domain ociclient.Domain, policy *ociclient.PasswordPolicy, err error) {

	client, err = s.conns.For(ctx, acc)
	if err != nil {
		return nil, domain, nil, err
	}

	region := acc.HomeRegion
	if region == "" {
		region = acc.DefaultRegion
	}

	domains, err := client.ListDomains(ctx, region, acc.TenancyOCID)
	if err != nil {
		return nil, domain, nil, err
	}
	if len(domains) == 0 {
		return client, domain, nil, nil
	}

	// 优先默认域——密码策略要改的就是它。一个租户可以有多个域，
	// 但其余的通常是为特定应用建的，不是控制台登录用的那个。
	domain = domains[0]
	for _, d := range domains {
		if d.IsDefault() {
			domain = d
			break
		}
	}
	if domain.Endpoint() == "" {
		return client, domain, nil, errors.New("身份域没有返回可用的端点地址")
	}

	policies, err := client.ListPasswordPolicies(ctx, domain.Endpoint())
	if err != nil {
		return client, domain, nil, err
	}
	if len(policies) == 0 {
		return client, domain, nil, nil
	}

	// 多条策略时按 Priority 取最优先的一条，数字小的先生效。
	best := policies[0]
	for _, p := range policies[1:] {
		if p.Priority != nil && (best.Priority == nil || *p.Priority < *best.Priority) {
			best = p
		}
	}
	return client, domain, &best, nil
}

func (s *Server) handleGetPasswordPolicy(w http.ResponseWriter, r *http.Request) {
	acc, err := s.st.GetAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), policyTimeout)
	defer cancel()

	view := PasswordPolicyView{AccountID: acc.ID}
	_, domain, policy, err := s.resolvePolicy(ctx, acc)
	if err != nil {
		view.Error = shortOCIError(err)
		writeJSON(w, http.StatusOK, map[string]any{"policy": view, "notice": policyNotice})
		return
	}

	if policy != nil {
		view.Supported = true
		view.DomainName = domain.DisplayName
		view.DomainURL = domain.Endpoint()
		view.PolicyID = policy.ID
		view.PolicyName = policy.Name
		view.ExpiresAfterDays = policy.PasswordExpiresAfter
		view.WarnBeforeDays = policy.PasswordExpireWarning
		view.MinLength = policy.MinLength
	}

	writeJSON(w, http.StatusOK, map[string]any{"policy": view, "notice": policyNotice})
}

// policyNotice 是随每次响应返回的说明。
//
// 放在接口里而不是只写在前端：这是 Oracle 的事实约束，任何调用方都该看到。
const policyNotice = "密码有效期属于身份域策略。Oracle 未公开说明哪个值代表「永不过期」，" +
	"因此修改后本工具会立即回读一次，显示的始终是服务端实际存下的值，而不是提交的值。"

type passwordPolicyRequest struct {
	// Days 为 nil 且 Disable 为 false 表示不改动。
	Days *int `json:"days"`
	// Disable 为真时尝试移除该属性，意图是"不再过期"。
	Disable bool `json:"disable"`
}

func (s *Server) handleUpdatePasswordPolicy(w http.ResponseWriter, r *http.Request) {
	acc, err := s.st.GetAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}

	var req passwordPolicyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Days == nil && !req.Disable {
		writeError(w, http.StatusBadRequest, "invalid_input", "未指定要设置的有效期")
		return
	}
	if req.Days != nil && (*req.Days < 0 || *req.Days > 3650) {
		writeError(w, http.StatusBadRequest, "invalid_input", "有效期应在 0 到 3650 天之间")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), policyTimeout)
	defer cancel()

	client, domain, policy, err := s.resolvePolicy(ctx, acc)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	if policy == nil {
		writeError(w, http.StatusConflict, "no_identity_domain",
			"该租户没有身份域密码策略，无法修改有效期。这类租户本来也没有密码过期一说。")
		return
	}

	days := req.Days
	if req.Disable {
		days = nil
	}

	updated, err := client.SetPasswordExpiry(ctx, domain.Endpoint(), policy.ID, days)
	if err != nil {
		writeOCIError(w, err)
		return
	}

	view := PasswordPolicyView{
		AccountID:        acc.ID,
		Supported:        true,
		DomainName:       domain.DisplayName,
		DomainURL:        domain.Endpoint(),
		PolicyID:         updated.ID,
		PolicyName:       updated.Name,
		ExpiresAfterDays: updated.PasswordExpiresAfter,
		WarnBeforeDays:   updated.PasswordExpireWarning,
		MinLength:        updated.MinLength,
	}

	// 回读的结果跟提交的不一致时明说，别让用户以为改成功了。
	notice := "已修改并回读确认。"
	switch {
	case req.Disable && updated.PasswordExpiresAfter != nil:
		notice = "已提交「取消过期」，但服务端回读仍有有效期——" +
			"说明该身份域不接受移除这个属性。请改为设置一个较大的天数。"
	case req.Days != nil && (updated.PasswordExpiresAfter == nil || *updated.PasswordExpiresAfter != *req.Days):
		notice = "已提交，但服务端存下的值与提交值不一致，请以上方显示的为准。"
	}

	// 改身份域策略影响的是整个租户的登录行为，必须留痕。
	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "password_policy_update",
		AccountID: acc.ID, Target: acc.Alias,
		Detail: expiryDetail(updated.PasswordExpiresAfter), IP: s.clientIP(r),
	})

	writeJSON(w, http.StatusOK, map[string]any{"policy": view, "notice": notice})
}

// expiryDetail 把有效期写成审计日志里看得懂的一句话。
func expiryDetail(days *int) string {
	if days == nil {
		return "有效期已移除（不再过期）"
	}
	return "有效期设为 " + strconv.Itoa(*days) + " 天"
}
