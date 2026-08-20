package httpapi

import (
	"context"
	"encoding/csv"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ocicore/internal/accountsvc"
	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.st.ListAccounts(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": accounts})
}

func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	acc, err := s.st.GetAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, acc)
}

// createAccountRequest 是添加账号表单。PrivateKeyPem 只在这一次请求中存在，
// 落库时立即加密，此后没有任何接口能把它读回来。
type createAccountRequest struct {
	Alias           string `json:"alias"`
	Code            string `json:"code"`
	ColorIndex      int    `json:"colorIndex"`
	TenancyOcid     string `json:"tenancyOcid"`
	UserOcid        string `json:"userOcid"`
	Fingerprint     string `json:"fingerprint"`
	PrivateKeyPem   string `json:"privateKeyPem"`
	DefaultRegion   string `json:"defaultRegion"`
	CompartmentOcid string `json:"compartmentOcid"`
	ProxyUrl        string `json:"proxyUrl"`
	// SkipCheck 为 true 时跳过保存后的连通性校验（离线环境下有用）。
	SkipCheck bool `json:"skipCheck"`
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	var req createAccountRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Code == "" {
		req.Code = store.SuggestCode(req.DefaultRegion)
	}

	acc, err := s.st.CreateAccount(r.Context(), store.NewAccount{
		Alias:           req.Alias,
		Code:            req.Code,
		ColorIndex:      req.ColorIndex,
		TenancyOCID:     req.TenancyOcid,
		UserOCID:        req.UserOcid,
		Fingerprint:     req.Fingerprint,
		PrivateKeyPEM:   req.PrivateKeyPem,
		DefaultRegion:   req.DefaultRegion,
		CompartmentOCID: req.CompartmentOcid,
		ProxyURL:        req.ProxyUrl,
	})
	if err != nil {
		// 创建失败绝大多数是表单填写问题（PEM 无法解析、指纹不匹配、OCID 格式错），
		// 这些都应该原样告诉用户，而不是笼统地报 500。
		s.writeAccountInputError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "account_create", AccountID: acc.ID,
		Target: acc.Alias, IP: s.clientIP(r),
	})

	resp := map[string]any{"account": acc}
	if !req.SkipCheck {
		if result := s.checkAccount(r.Context(), acc); result != nil {
			resp["check"] = result
			// 重新读一次拿到刚写入的状态。
			if refreshed, err := s.st.GetAccount(r.Context(), acc.ID); err == nil {
				resp["account"] = refreshed
			}
		}
	}
	writeJSON(w, http.StatusCreated, resp)
}

type updateAccountRequest struct {
	Alias           *string `json:"alias"`
	Code            *string `json:"code"`
	ColorIndex      *int    `json:"colorIndex"`
	DefaultRegion   *string `json:"defaultRegion"`
	CompartmentOcid *string `json:"compartmentOcid"`
	ProxyUrl        *string `json:"proxyUrl"`
	Enabled         *bool   `json:"enabled"`
	UserOcid        *string `json:"userOcid"`
	Fingerprint     *string `json:"fingerprint"`
	PrivateKeyPem   *string `json:"privateKeyPem"`
}

func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	var req updateAccountRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// 空字符串的私钥是误传，不是"清空私钥"——没有私钥的账号毫无意义。
	if req.PrivateKeyPem != nil && strings.TrimSpace(*req.PrivateKeyPem) == "" {
		writeError(w, http.StatusBadRequest, "empty_key",
			"私钥不能为空。若不想修改私钥，请不要提交该字段。")
		return
	}

	acc, err := s.st.UpdateAccount(r.Context(), r.PathValue("id"), store.AccountUpdate{
		Alias:           req.Alias,
		Code:            req.Code,
		ColorIndex:      req.ColorIndex,
		DefaultRegion:   req.DefaultRegion,
		CompartmentOCID: req.CompartmentOcid,
		ProxyURL:        req.ProxyUrl,
		Enabled:         req.Enabled,
		UserOCID:        req.UserOcid,
		Fingerprint:     req.Fingerprint,
		PrivateKeyPEM:   req.PrivateKeyPem,
	})
	if err != nil {
		s.writeAccountInputError(w, err)
		return
	}

	user := userFrom(r.Context())
	action := "account_update"
	if req.PrivateKeyPem != nil {
		action = "account_rotate_key"
	}
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: action, AccountID: acc.ID,
		Target: acc.Alias, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, acc)
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acc, err := s.st.GetAccount(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// 删除账号是 L3 级危险操作。服务端要求客户端回传账号别名，
	// 与前端的输名确认框对应——确认框可以被绕过，服务端校验不能。
	confirm := strings.TrimSpace(r.URL.Query().Get("confirm"))
	if confirm != acc.Alias {
		writeError(w, http.StatusBadRequest, "confirm_required",
			"删除账号需要在 confirm 参数中回传账号别名 "+strconv.Quote(acc.Alias))
		return
	}

	if err := s.st.DeleteAccount(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	// 丢掉缓存的客户端，避免已删除账号的私钥继续留在内存里。
	s.conns.Invalidate(id)

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "account_delete", AccountID: id,
		Target: acc.Alias, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleCheckAccount 对已保存的账号做一次连通性复检。
func (s *Server) handleCheckAccount(w http.ResponseWriter, r *http.Request) {
	acc, err := s.st.GetAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	result := s.checkAccount(r.Context(), acc)
	if result == nil {
		writeError(w, http.StatusInternalServerError, "internal", "校验失败")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// checkAccount 执行校验并把结论写回账号状态。返回 nil 表示解密或建连本身就失败了。
func (s *Server) checkAccount(ctx context.Context, acc *store.Account) *accountsvc.Result {
	client, err := s.conns.For(ctx, acc)
	if err != nil {
		msg := err.Error()
		_ = s.st.SetAccountStatus(ctx, acc.ID, store.StatusError, msg)
		return &accountsvc.Result{
			OK:           false,
			ErrorText:    msg,
			AccountFatal: true,
			Steps: []accountsvc.Step{
				{Key: "credentials", Label: "读取凭据", OK: false, Detail: msg},
			},
		}
	}

	result := accountsvc.CheckClient(ctx, client)

	status, message := store.StatusOK, ""
	if !result.OK {
		status = store.StatusError
		message = strings.TrimSpace(result.ErrorCode + " " + result.ErrorText)
	}
	_ = s.st.SetAccountStatus(ctx, acc.ID, status, message)

	// 顺手把探测到的身份信息存下来。订阅区域尤其关键：跨账号同步靠它
	// 决定要去哪些区域拉实例——没有这份清单就只能盲扫三十多个区域，
	// 绝大多数都是空跑。
	if err := s.st.SetAccountIdentity(ctx, acc.ID, store.AccountIdentity{
		Regions:              result.Regions,
		HomeRegion:           result.HomeRegion,
		Email:                result.UserEmail,
		TenancyName:          result.TenancyName,
		PaymentModel:         result.PaymentModel,
		SubscriptionState:    result.SubscriptionState,
		SubscriptionStartsAt: result.SubscriptionStartsAt,
		SubscriptionEndsAt:   result.SubscriptionEndsAt,
	}); err != nil {
		slog.Warn("保存账号身份信息失败", "account", acc.ID, "err", err)
	}
	return &result
}

// handleAccountRegions 返回该账号已订阅的区域，供创建实例时的区域下拉使用。
func (s *Server) handleAccountRegions(w http.ResponseWriter, r *http.Request) {
	acc, err := s.st.GetAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	client, err := s.conns.For(r.Context(), acc)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	subs, err := client.ListRegionSubscriptions(ctx)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"regions": subs})
}

// ---- 添加账号抽屉的两个辅助接口 ----

type parseConfigRequest struct {
	Text string `json:"text"`
}

// handleParseConfig 解析用户整段粘贴的 OCI 配置文件。
//
// 这是添加账号流程里最有价值的一个交互：用户从 Oracle 控制台复制的配置
// 直接粘进来就能自动拆成各个字段，消除手抄导致的绝大多数配置错误。
func (s *Server) handleParseConfig(w http.ResponseWriter, r *http.Request) {
	var req parseConfigRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	profiles := ociclient.ParseConfigFile(req.Text)
	if len(profiles) == 0 {
		writeError(w, http.StatusBadRequest, "parse_failed",
			"没有识别出任何配置项。请粘贴 Oracle 控制台生成的完整配置片段。")
		return
	}

	type profileOut struct {
		Name          string   `json:"name"`
		UserOcid      string   `json:"userOcid"`
		Fingerprint   string   `json:"fingerprint"`
		TenancyOcid   string   `json:"tenancyOcid"`
		Region        string   `json:"region"`
		KeyFile       string   `json:"keyFile"`
		HasPassPhrase bool     `json:"hasPassPhrase"`
		Complete      bool     `json:"complete"`
		Missing       []string `json:"missing"`
		SuggestedCode string   `json:"suggestedCode"`
	}

	out := make([]profileOut, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, profileOut{
			Name:          p.Name,
			UserOcid:      p.User,
			Fingerprint:   p.Fingerprint,
			TenancyOcid:   p.Tenancy,
			Region:        p.Region,
			KeyFile:       p.KeyFile,
			HasPassPhrase: p.HasPassPhrase,
			Complete:      p.Complete(),
			Missing:       p.MissingFields(),
			SuggestedCode: store.SuggestCode(p.Region),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": out})
}

type checkDraftRequest struct {
	TenancyOcid   string `json:"tenancyOcid"`
	UserOcid      string `json:"userOcid"`
	Fingerprint   string `json:"fingerprint"`
	PrivateKeyPem string `json:"privateKeyPem"`
	Region        string `json:"region"`
	ProxyUrl      string `json:"proxyUrl"`
}

// handleCheckDraft 在保存之前测试一份凭据是否可用。
func (s *Server) handleCheckDraft(w http.ResponseWriter, r *http.Request) {
	var req checkDraftRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result := accountsvc.CheckDraft(r.Context(), accountsvc.Draft{
		TenancyOCID:   req.TenancyOcid,
		UserOCID:      req.UserOcid,
		Fingerprint:   req.Fingerprint,
		PrivateKeyPEM: req.PrivateKeyPem,
		Region:        req.Region,
		ProxyURL:      req.ProxyUrl,
	})
	writeJSON(w, http.StatusOK, result)
}

// ---- 审计与区域 ----

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	beforeID, _ := strconv.ParseInt(q.Get("beforeId"), 10, 64)

	filter := store.AuditFilter{
		AccountID: q.Get("accountId"),
		Action:    q.Get("action"),
		Limit:     limit,
		BeforeID:  beforeID,
	}
	entries, hasMore, err := s.st.ListAudit(r.Context(), filter)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// total 只在第一页给：翻页时再数一遍纯属浪费，总数也不会因为翻页而变。
	resp := map[string]any{"entries": entries, "hasMore": hasMore}
	if beforeID == 0 {
		if total, err := s.st.CountAudit(r.Context(), filter); err == nil {
			resp["total"] = total
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleExportAudit 导出全部匹配的审计记录为 CSV。
//
// 走后端而不是让前端把已加载的那一页拼成文件：前端手里只有当前分页的
// 内容，导出的东西会悄悄少掉绝大部分记录，而按钮上写的是「导出 CSV」。
func (s *Server) handleExportAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.AuditFilter{
		AccountID: q.Get("accountId"),
		Action:    q.Get("action"),
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`attachment; filename="audit-`+time.Now().Format("2006-01-02")+`.csv"`)

	cw := csv.NewWriter(w)
	// UTF-8 BOM：Excel 不认无 BOM 的 UTF-8，中文会显示成乱码。
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	_ = cw.Write([]string{"时间", "动作", "账号", "目标资源", "详情", "来源 IP", "结果"})

	err := s.st.EachAudit(r.Context(), filter, func(e store.AuditEntry) error {
		return cw.Write([]string{
			e.CreatedAt.Format("2006-01-02 15:04:05"),
			e.Action, e.AccountID, e.Target, e.Detail, e.IP, e.Result,
		})
	})
	cw.Flush()
	if err != nil {
		// 响应头和部分内容已经发出去了，改不了状态码。记日志，让客户端
		// 拿到一个内容不完整的文件——总比装作成功要好。
		slog.Error("导出审计日志中断", "err", err)
		return
	}
	if err := cw.Error(); err != nil {
		slog.Error("写出审计 CSV 失败", "err", err)
	}
}

func (s *Server) handleListRegions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"regions": ociclient.KnownRegions()})
}

// ---- 共用助手 ----

// writeAccountInputError 把账号写入失败归类为"用户填错了"还是"系统问题"。
func (s *Server) writeAccountInputError(w http.ResponseWriter, err error) {
	switch {
	case isValidationError(err):
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
	default:
		writeStoreError(w, err)
	}
}

// isValidationError 判断错误是否来自输入校验。
// store 层的校验错误都是 errors.New / fmt.Errorf 构造的普通错误，
// 与 ErrNotFound / ErrConflict 这两个哨兵值区分开即可。
func isValidationError(err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), "store: ") {
		return false
	}
	return true
}

// writeOCIError 把 OCI 调用失败转成响应，保留原始错误码与处理建议。
func writeOCIError(w http.ResponseWriter, err error) {
	apiErr, ok := ociclient.AsAPIError(err)
	if !ok {
		writeError(w, http.StatusBadGateway, "oci_error", err.Error())
		return
	}
	status := http.StatusBadGateway
	switch apiErr.Class {
	case ociclient.ClassAuthFailed, ociclient.ClassNotAuthorized:
		status = http.StatusForbidden
	case ociclient.ClassThrottled:
		status = http.StatusTooManyRequests
	case ociclient.ClassBadRequest, ociclient.ClassQuotaExceeded:
		status = http.StatusBadRequest
	}
	writeJSON(w, status, errorBody{
		Code:    "oci_error",
		Message: apiErr.Message,
		OciCode: apiErr.Code,
		Advice:  apiErr.Advice(),
	})
}
