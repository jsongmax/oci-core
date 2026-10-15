package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"ocicore/internal/auth"
)

const testTOTPSecret = "JBSWY3DPEHPK3PXP"

// statusWithToken 用指定的会话令牌请求 path，返回状态码。
func (h *harness) statusWithToken(path, token string) int {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.srv.URL+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// useSession 让 harness 自己的客户端改用 token 这个会话。
func (h *harness) useSession(token string) {
	h.t.Helper()
	u, _ := url.Parse(h.srv.URL)
	h.client.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: token, Path: "/"}})
}

// enableTOTPDirect 在库里直接启用 TOTP（lastCounter 为已用过的时间窗），
// 并给 harness 换一个已通过两步验证的完整会话。
func (h *harness) enableTOTPDirect(lastCounter int64) {
	h.t.Helper()
	ctx := context.Background()
	u, err := h.store.GetUserByUsername(ctx, "admin")
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.SetTOTPSecret(ctx, u.ID, testTOTPSecret); err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.EnableTOTP(ctx, u.ID, lastCounter); err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.CreateTOTPVerifiedSession(ctx, u.ID, "full-session", "127.0.0.1", "test", time.Hour); err != nil {
		h.t.Fatal(err)
	}
	h.useSession("full-session")
}

// F04：启用 TOTP 前创建的纯口令会话，在启用后必须失效；发起启用的会话换发新令牌后继续可用。
func TestEnableTOTPRevokesExistingSessions(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()
	ctx := context.Background()
	u, err := h.store.GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	const stolen = "stolen-password-only-session"
	if err := h.store.CreateSession(ctx, u.ID, stolen, "10.0.0.9", "attacker", time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if got := h.statusWithToken("/api/accounts", stolen); got != http.StatusOK {
		t.Fatalf("启用前旧会话应可用，实际 %d", got)
	}

	setup := decode[totpSetupResponse](t, h.post("/api/auth/totp/setup", nil))
	code, err := auth.TOTPCode(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resp := h.post("/api/auth/totp/enable", map[string]string{"code": code})
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	if got := h.statusWithToken("/api/accounts", stolen); got != http.StatusUnauthorized {
		t.Errorf("启用 TOTP 后旧的纯口令会话应被撤销，实际 %d", got)
	}
	resp = h.get("/api/accounts")
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()
}

// F04：关闭 TOTP 时，还停在第二步的半登录会话不能因此变成完整会话。
func TestDisableTOTPRevokesPendingSessions(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()
	now := time.Now()
	h.enableTOTPDirect(now.Unix()/30 - 1)

	ctx := context.Background()
	u, _ := h.store.GetUserByUsername(ctx, "admin")
	const pending = "pending-session"
	if err := h.store.CreateSession(ctx, u.ID, pending, "10.0.0.9", "attacker", time.Hour, false); err != nil {
		t.Fatal(err)
	}

	code, _ := auth.TOTPCode(testTOTPSecret, now)
	resp := h.post("/api/auth/totp/disable", map[string]string{"password": "a-very-long-password", "code": code})
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	if got := h.statusWithToken("/api/accounts", pending); got != http.StatusUnauthorized {
		t.Errorf("关闭 TOTP 后半登录会话应被撤销，实际 %d", got)
	}
	if got := h.statusWithToken("/api/accounts", "full-session"); got != http.StatusUnauthorized {
		t.Errorf("关闭 TOTP 后旧令牌应失效（当前会话应换发新令牌），实际 %d", got)
	}
	resp = h.get("/api/accounts")
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()
}

// F05：关闭 TOTP 必须拒绝已经用过的验证码，且不改动任何状态。
func TestDisableTOTPRejectsReusedCode(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()
	now := time.Now()
	h.enableTOTPDirect(now.Unix() / 30) // 当前时间窗已被消费

	code, _ := auth.TOTPCode(testTOTPSecret, now)
	resp := h.post("/api/auth/totp/disable", map[string]string{"password": "a-very-long-password", "code": code})
	expectStatus(t, resp, http.StatusUnauthorized)
	body := decode[errorBody](t, resp)
	if body.Code != "code_reused" {
		t.Errorf("错误码应为 code_reused，实际 %q", body.Code)
	}

	u, err := h.store.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !u.TOTPEnabled || u.TOTPSecret != testTOTPSecret {
		t.Error("被拒绝的关闭请求不应改动 TOTP 状态")
	}
	if got := h.statusWithToken("/api/accounts", "full-session"); got != http.StatusOK {
		t.Errorf("被拒绝的关闭请求不应撤销会话，实际 %d", got)
	}
}
