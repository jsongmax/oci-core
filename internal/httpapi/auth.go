package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"ocicore/internal/auth"
	"ocicore/internal/store"
)

// minPasswordLen 是口令长度下限。这个面板持有所有租户的控制权，
// 不接受短口令；上限交给 argon2 处理，无需额外限制。
const minPasswordLen = 10

// issuer 出现在验证器 App 的条目名里。
const issuer = "OCI Core"

// statusResponse 供前端在启动时判断该渲染哪个界面。
type statusResponse struct {
	SetupRequired bool   `json:"setupRequired"`
	Authenticated bool   `json:"authenticated"`
	TOTPRequired  bool   `json:"totpRequired"`
	TOTPEnabled   bool   `json:"totpEnabled"`
	Username      string `json:"username,omitempty"`
	Version       string `json:"version"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	count, err := s.st.CountUsers(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	resp := statusResponse{SetupRequired: count == 0, Version: Version}

	if sess, user, _, ok := s.loadSession(r); ok {
		resp.Username = user.Username
		resp.TOTPEnabled = user.TOTPEnabled
		resp.TOTPRequired = user.TOTPEnabled && !sess.TOTPVerified
		resp.Authenticated = !resp.TOTPRequired
	}
	writeJSON(w, http.StatusOK, resp)
}

// Version 是构建版本，由 cmd/server 在启动时覆盖。
var Version = "dev"

// ---- 首次设置 ----

type setupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleSetup 创建第一个用户。仅在系统中还没有任何用户时可用——
// 这是唯一的无鉴权写接口，因此必须严格守住"零用户"这个前提。
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	count, err := s.st.CountUsers(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if count > 0 {
		writeError(w, http.StatusConflict, "already_initialized", "系统已完成初始化")
		return
	}

	var req setupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, "invalid_username", "用户名不能为空")
		return
	}
	if len([]rune(req.Password)) < minPasswordLen {
		writeError(w, http.StatusBadRequest, "weak_password",
			"口令至少需要 10 个字符")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeHashError(w, "散列口令失败", err)
		return
	}
	// 上面的 count 只是提前拒绝，真正守住"零用户"的是这条原子插入。
	user, err := s.st.CreateFirstUser(r.Context(), req.Username, hash)
	if errors.Is(err, store.ErrAlreadyInitialized) {
		writeError(w, http.StatusConflict, "already_initialized", "系统已完成初始化")
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}

	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "setup", Target: user.Username, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"username": user.Username,
		// 提醒前端立刻引导用户绑定 TOTP。设计上双因子是必须的，
		// 但强行在首次设置里塞进扫码流程容易把用户卡在门外，
		// 因此拆成"创建账户 → 引导绑定"两步。
		"nextStep": "enroll_totp",
	})
}

// writeHashError 把口令计算的失败转成响应。
//
// ErrBusy 是负载保护在起作用，回 503 让客户端稍后重试；其余是散列串
// 损坏之类的内部问题。
func writeHashError(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, auth.ErrBusy) {
		writeError(w, http.StatusServiceUnavailable, "busy", "请求过多，请稍后再试")
		return
	}
	slog.Error(what, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
}

// ---- 登录 ----

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	TOTPRequired bool `json:"totpRequired"`
	TOTPEnabled  bool `json:"totpEnabled"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	allowed, remaining := s.logins.Reserve(ip)
	if !allowed {
		writeError(w, http.StatusTooManyRequests, "rate_limited",
			"登录失败次数过多，请 15 分钟后再试")
		return
	}

	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	// 用户名不存在与口令错误必须给出完全相同的响应，
	// 否则接口就变成了用户名枚举器。
	fail := func() {
		_ = s.st.Audit(r.Context(), store.AuditEntry{
			Action: "login", Target: req.Username, IP: ip, Result: store.ResultFail,
		})
		writeJSON(w, http.StatusUnauthorized, errorBody{
			Code:    "invalid_credentials",
			Message: "用户名或口令错误，还可尝试 " + itoa(remaining) + " 次",
		})
	}

	user, err := s.st.GetUserByUsername(r.Context(), req.Username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// 即使用户不存在也要走一次散列计算，抹平响应时间差异。
			// 繁忙时与已存在用户走同一个 503，否则负载高时能借此区分用户名。
			if _, err := auth.HashPassword(req.Password); errors.Is(err, auth.ErrBusy) {
				writeHashError(w, "散列口令失败", err)
				return
			}
			fail()
			return
		}
		writeStoreError(w, err)
		return
	}

	ok, err := auth.VerifyPassword(req.Password, user.PasswordHash)
	if err != nil {
		writeHashError(w, "校验口令失败", err)
		return
	}
	if !ok {
		fail()
		return
	}

	token, err := auth.NewToken()
	if err != nil {
		slog.Error("生成会话令牌失败", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
		return
	}

	// 启用了 TOTP 的用户先拿到"半登录"会话，只能调用 TOTP 校验接口。
	// 会话按口令校验前读到的用户快照签发；校验期间若启用了 TOTP 或改了口令，
	// 快照已过时，拒绝这次登录而不是按旧状态放行。
	if err := s.st.CreateLoginSession(r.Context(), user, token, ip, r.UserAgent(), s.cfg.SessionTTL); err != nil {
		if errors.Is(err, store.ErrAuthStateChanged) {
			writeError(w, http.StatusConflict, "auth_state_changed", "账户的认证设置刚刚发生变化，请重新登录")
			return
		}
		writeStoreError(w, err)
		return
	}

	s.logins.Reset(ip)
	s.setSessionCookie(w, r, token)
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "login", Target: user.Username, IP: ip,
	})

	writeJSON(w, http.StatusOK, loginResponse{
		TOTPRequired: user.TOTPEnabled,
		TOTPEnabled:  user.TOTPEnabled,
	})
}

type totpRequest struct {
	Code string `json:"code"`
}

func (s *Server) handleTOTPVerify(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	token := tokenFrom(r.Context())
	ip := s.clientIP(r)

	if !user.TOTPEnabled {
		writeError(w, http.StatusBadRequest, "totp_not_enabled", "该账户未启用两步验证")
		return
	}

	allowed, _ := s.logins.Reserve("totp:" + ip)
	if !allowed {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "尝试次数过多，请稍后再试")
		return
	}

	var req totpRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	counter, ok := auth.VerifyTOTPWithCounter(user.TOTPSecret, req.Code, time.Now())
	if !ok {
		_ = s.st.Audit(r.Context(), store.AuditEntry{
			UserID: user.ID, Action: "totp_verify", IP: ip, Result: store.ResultFail,
		})
		writeError(w, http.StatusUnauthorized, "invalid_code", "验证码不正确")
		return
	}

	// 阻止重放：同一个时间窗只能用一次。
	fresh, err := s.st.ConsumeTOTPCounter(r.Context(), user.ID, counter)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !fresh {
		writeError(w, http.StatusUnauthorized, "code_reused",
			"该验证码已被使用，请等待下一个验证码")
		return
	}

	if err := s.st.MarkTOTPVerified(r.Context(), token); err != nil {
		writeStoreError(w, err)
		return
	}
	s.logins.Reset("totp:" + ip)
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "totp_verify", IP: ip,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := tokenFrom(r.Context())
	user := userFrom(r.Context())
	if err := s.st.DeleteSession(r.Context(), token); err != nil {
		writeStoreError(w, err)
		return
	}
	s.clearSessionCookie(w, r)
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "logout", IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// meResponse 是用户信息的对外视图。
// 刻意与 store.User 分开：后者含有口令散列与 TOTP 密钥，绝不能直接序列化出去。
type meResponse struct {
	Username    string    `json:"username"`
	TOTPEnabled bool      `json:"totpEnabled"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	writeJSON(w, http.StatusOK, meResponse{
		Username:    user.Username,
		TOTPEnabled: user.TOTPEnabled,
		CreatedAt:   user.CreatedAt,
	})
}

// ---- TOTP 绑定 ----

type totpSetupResponse struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
}

// handleTOTPSetup 生成新密钥但不启用。用户必须先用验证器扫码并提交一次
// 正确的验证码，才会真正启用——否则会出现"密钥存了但没扫上"把自己锁死的情况。
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if user.TOTPEnabled {
		writeError(w, http.StatusConflict, "already_enabled",
			"两步验证已启用。如需重新绑定，请先关闭。")
		return
	}

	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		slog.Error("生成 TOTP 密钥失败", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
		return
	}
	if err := s.st.SetTOTPSecret(r.Context(), user.ID, secret); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, totpSetupResponse{
		Secret: secret,
		URI:    auth.TOTPProvisioningURI(secret, user.Username, issuer),
	})
}

func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if user.TOTPEnabled {
		writeError(w, http.StatusConflict, "already_enabled", "两步验证已启用")
		return
	}
	if user.TOTPSecret == "" {
		writeError(w, http.StatusBadRequest, "no_secret", "请先获取绑定密钥")
		return
	}

	var req totpRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	counter, ok := auth.VerifyTOTPWithCounter(user.TOTPSecret, req.Code, time.Now())
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_code",
			"验证码不正确。请确认手机时间准确，并使用当前显示的验证码。")
		return
	}
	// EnableTOTP 会撤销该用户的全部会话：启用前的会话只通过了口令，
	// 不能在加上第二因子之后继续通行。当前请求刚提交了正确验证码，换发一个新会话。
	if err := s.st.EnableTOTP(r.Context(), user.ID, counter); err != nil {
		writeStoreError(w, err)
		return
	}
	if !s.reissueSession(w, r, user.ID, true) {
		return
	}
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "totp_enable", IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// reissueSession 为当前请求签发一个全新的完整会话并写入 Cookie。
//
// 用于 TOTP 启用、关闭这类会撤销全部会话的认证状态变更之后：
// 旧令牌已随撤销失效，当前用户刚完成了足够强的复核，不该被一并踢下线。
// totpEnabled 是变更之后的 TOTP 状态；签发时会核对它，状态又被并发改掉则拒绝。
// 失败时已写好错误响应，返回 false。
func (s *Server) reissueSession(w http.ResponseWriter, r *http.Request, userID string, totpEnabled bool) bool {
	token, err := auth.NewToken()
	if err != nil {
		slog.Error("生成会话令牌失败", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
		return false
	}
	ip, ua := s.clientIP(r), r.UserAgent()
	if totpEnabled {
		err = s.st.CreateTOTPVerifiedSession(r.Context(), userID, token, ip, ua, s.cfg.SessionTTL)
	} else {
		err = s.st.CreateSession(r.Context(), userID, token, ip, ua, s.cfg.SessionTTL, true)
	}
	if err != nil {
		if errors.Is(err, store.ErrAuthStateChanged) {
			s.clearSessionCookie(w, r)
			writeError(w, http.StatusConflict, "auth_state_changed", "账户的认证设置刚刚发生变化，请重新登录")
			return false
		}
		writeStoreError(w, err)
		return false
	}
	s.setSessionCookie(w, r, token)
	return true
}

// ---- 关闭两步验证 ----

type disableTOTPRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

// handleTOTPDisable 关闭两步验证。
//
// 同时要求口令与一次有效验证码——只有会话是不够的。
// 摘掉一道防线这件事本身，就该由两道防线共同确认：
// 会话可能是别人捡到的浏览器，光凭它就能把 2FA 卸了等于没有 2FA。
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if !user.TOTPEnabled {
		writeError(w, http.StatusBadRequest, "totp_not_enabled", "该账户未启用两步验证")
		return
	}

	ip := s.clientIP(r)
	allowed, _ := s.logins.Reserve("totp:" + ip)
	if !allowed {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "尝试次数过多，请稍后再试")
		return
	}

	var req disableTOTPRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	ok, err := auth.VerifyPassword(req.Password, user.PasswordHash)
	if err != nil {
		writeHashError(w, "校验口令失败", err)
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "口令不正确")
		return
	}

	counter, valid := auth.VerifyTOTPWithCounter(user.TOTPSecret, req.Code, time.Now())
	if !valid {
		writeError(w, http.StatusUnauthorized, "invalid_code", "验证码不正确")
		return
	}
	// 消费时间窗、关闭 TOTP、撤销全部会话在同一事务里完成。
	// 已用过的验证码必须拒绝：一次性是 TOTP 防重放的全部意义，
	// 关闭第二因子这一步更不能放松。撤销会话则是为了不让还停在第二步的
	// 半登录会话在 TOTP 关闭后直接变成完整会话。
	fresh, err := s.st.DisableTOTP(r.Context(), user.ID, counter)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !fresh {
		writeError(w, http.StatusUnauthorized, "code_reused",
			"该验证码已被使用，请等待下一个验证码")
		return
	}
	// DisableTOTP 已在同一事务里关掉了危险操作复核开关。
	if !s.reissueSession(w, r, user.ID, false) {
		return
	}

	s.logins.Reset("totp:" + ip)
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "totp_disable", IP: ip,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "两步验证已关闭。这个面板持有全部 Oracle 租户的控制权，建议尽快重新绑定。",
	})
}

// ---- 改密 ----

type changePasswordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	var req changePasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ok, err := auth.VerifyPassword(req.Current, user.PasswordHash)
	if err != nil {
		writeHashError(w, "校验口令失败", err)
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "当前口令不正确")
		return
	}
	if len([]rune(req.New)) < minPasswordLen {
		writeError(w, http.StatusBadRequest, "weak_password", "新口令至少需要 10 个字符")
		return
	}

	hash, err := auth.HashPassword(req.New)
	if err != nil {
		writeHashError(w, "散列口令失败", err)
		return
	}
	// SetPassword 会顺带清空该用户的全部会话——改密后所有旧会话必须下线。
	if err := s.st.SetPassword(r.Context(), user.ID, hash); err != nil {
		writeStoreError(w, err)
		return
	}
	s.clearSessionCookie(w, r)
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "change_password", IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "口令已更新，所有会话已下线，请重新登录",
	})
}

func itoa(n int) string {
	if n < 0 {
		n = 0
	}
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
