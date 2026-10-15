package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ocicore/internal/auth"
	"ocicore/internal/config"
)

const dangerTestSecret = "JBSWY3DPEHPK3PXP"

// gatedBody 让请求体在第一次被读取时停住，用来把两个请求卡在同一个时间点。
type gatedBody struct {
	io.Reader
	entered chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (b *gatedBody) Read(p []byte) (int, error) {
	b.once.Do(func() { b.entered <- struct{}{}; <-b.release })
	return b.Reader.Read(p)
}

func (b *gatedBody) Close() error { return nil }

// 两个首次设置都通过了"零用户"的提前检查时，只能有一个真正建出用户。
func TestSetupConcurrentCreatesSingleUser(t *testing.T) {
	h := newHarness(t)
	s := New(Deps{Store: h.store, Config: config.Config{SessionTTL: time.Hour}})

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	codes := make(chan int, 2)
	for _, name := range []string{"owner", "intruder"} {
		go func(name string) {
			r := httptest.NewRequest("POST", "/api/setup", nil)
			r.Header.Set(csrfHeader, csrfValue)
			r.Body = &gatedBody{
				Reader:  strings.NewReader(`{"username":"` + name + `","password":"long-enough-password"}`),
				entered: entered, release: release,
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			codes <- w.Code
		}(name)
	}
	<-entered
	<-entered
	close(release)

	a, b := <-codes, <-codes
	if !(a == http.StatusCreated && b == http.StatusConflict) && !(a == http.StatusConflict && b == http.StatusCreated) {
		t.Fatalf("状态码 = %d, %d，期望一个 201 一个 409", a, b)
	}
	n, err := h.store.CountUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("用户数 = %d，期望 1", n)
	}
}

// 额度在校验前就预占：还没有任何一次失败落定时，同一 IP 也只能放进 max 个请求。
func TestAttemptLimiterReservesBeforeVerification(t *testing.T) {
	l := newAttemptLimiter(5, 15*time.Minute)
	accepted := 0
	for i := 0; i < 100; i++ {
		if ok, _ := l.Reserve("same-ip"); ok {
			accepted++
		}
	}
	if accepted != 5 {
		t.Fatalf("放行 %d 次，期望 5", accepted)
	}
	l.Reset("same-ip")
	if ok, remaining := l.Reserve("same-ip"); !ok || remaining != 4 {
		t.Fatalf("Reset 后 Reserve = %v, %d，期望 true, 4", ok, remaining)
	}
}

// avoidTOTPBoundary 在时间窗即将切换时等到下一个窗口开头，
// 免得测试里先后算出的两个码因跨窗而错位。
func avoidTOTPBoundary() {
	if left := 30 - time.Now().Unix()%30; left <= 3 {
		time.Sleep(time.Duration(left)*time.Second + 100*time.Millisecond)
	}
}

// enrollAndRelogin 给 admin 绑定 TOTP，并用上一个时间窗的码重新完成两步登录，
// 留下当前时间窗给危险操作复核使用。
func (h *harness) enrollAndRelogin() {
	h.t.Helper()
	ctx := context.Background()
	avoidTOTPBoundary()

	u, err := h.store.GetUserByUsername(ctx, "admin")
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.SetTOTPSecret(ctx, u.ID, dangerTestSecret); err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.EnableTOTP(ctx, u.ID, time.Now().Unix()/30-5); err != nil {
		h.t.Fatal(err)
	}

	resp := h.post("/api/auth/login", map[string]string{
		"username": "admin", "password": "a-very-long-password",
	})
	expectStatus(h.t, resp, http.StatusOK)
	resp.Body.Close()

	prev, err := auth.TOTPCode(dangerTestSecret, time.Now().Add(-30*time.Second))
	if err != nil {
		h.t.Fatal(err)
	}
	resp = h.post("/api/auth/totp/verify", map[string]string{"code": prev})
	expectStatus(h.t, resp, http.StatusOK)
	resp.Body.Close()
}

// doWithCode 发起写请求并附带危险操作验证码。
func (h *harness) doWithCode(method, path string, body string, code string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(csrfHeader, csrfValue)
	if code != "" {
		req.Header.Set(dangerCodeHeader, code)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func expectErrorCode(t *testing.T, resp *http.Response, status int, code string) {
	t.Helper()
	if resp.StatusCode != status {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("状态码 = %d，期望 %d；响应体: %s", resp.StatusCode, status, body)
	}
	got := decode[errorBody](t, resp)
	if got.Code != code {
		t.Fatalf("错误码 = %q，期望 %q", got.Code, code)
	}
}

const forcedBulkStop = `{"instanceIds":["nonexistent-instance"],"action":"STOP","force":true}`

func TestDangerPolicyCannotBeEnabledWithoutTOTP(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	resp := h.do("PATCH", "/api/settings", map[string]any{"requireTotpForDanger": true}, true)
	expectErrorCode(t, resp, http.StatusConflict, "totp_not_enabled")
}

func TestDangerPolicyRequiresFreshTOTP(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()
	h.enrollAndRelogin()

	resp := h.do("PATCH", "/api/settings", map[string]any{"requireTotpForDanger": true}, true)
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// 批量开机不算危险，不要求验证码。
	resp = h.doWithCode("POST", "/api/instances/bulk",
		`{"instanceIds":["nonexistent-instance"],"action":"START"}`, "")
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = h.doWithCode("POST", "/api/instances/bulk", forcedBulkStop, "")
	expectErrorCode(t, resp, http.StatusForbidden, "danger_totp_required")

	resp = h.doWithCode("POST", "/api/instances/bulk", forcedBulkStop, "000000")
	expectErrorCode(t, resp, http.StatusForbidden, "danger_totp_invalid")

	// 登录第二步用掉的上一个窗口不能再拿来复核。
	prev, err := auth.TOTPCode(dangerTestSecret, time.Now().Add(-30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	resp = h.doWithCode("POST", "/api/instances/bulk", forcedBulkStop, prev)
	expectErrorCode(t, resp, http.StatusForbidden, "danger_totp_reused")

	code, err := auth.TOTPCode(dangerTestSecret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resp = h.doWithCode("POST", "/api/instances/bulk", forcedBulkStop, code)
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// 复核通过后的有效期内，同一会话不再重复询问。
	resp = h.doWithCode("POST", "/api/instances/bulk", forcedBulkStop, "")
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()
}

// 关掉复核本身就是在拆门槛，必须先过复核。
func TestDisablingDangerPolicyRequiresTOTP(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()
	h.enrollAndRelogin()

	resp := h.do("PATCH", "/api/settings", map[string]any{"requireTotpForDanger": true}, true)
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = h.doWithCode("PATCH", "/api/settings", `{"requireTotpForDanger":false}`, "")
	expectErrorCode(t, resp, http.StatusForbidden, "danger_totp_required")

	resp = h.doWithCode("PATCH", "/api/settings", `{"auditRetentionDays":7}`, "")
	expectErrorCode(t, resp, http.StatusForbidden, "danger_totp_required")

	// 收紧方向的修改不拦。
	resp = h.doWithCode("PATCH", "/api/settings", `{"allowTerminate":false}`, "")
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	code, err := auth.TOTPCode(dangerTestSecret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resp = h.doWithCode("PATCH", "/api/settings", `{"requireTotpForDanger":false}`, code)
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()
}

// 关闭两步验证时复核开关随之关闭，否则所有危险操作都会被挡在门外。
func TestDisablingTOTPTurnsOffDangerPolicy(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()
	h.enrollAndRelogin()

	resp := h.do("PATCH", "/api/settings", map[string]any{"requireTotpForDanger": true}, true)
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	code, err := auth.TOTPCode(dangerTestSecret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resp = h.post("/api/auth/totp/disable", map[string]string{
		"password": "a-very-long-password", "code": code,
	})
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	settings, err := h.store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.RequireTOTPForDanger {
		t.Fatal("关闭两步验证后危险操作复核仍处于开启状态")
	}
}
