package httpapi

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ocicore/internal/config"
	"ocicore/internal/cryptobox"
	"ocicore/internal/instancesvc"
	"ocicore/internal/ociclient"
	"ocicore/internal/ociconn"
	"ocicore/internal/store"
)

// harness 把服务、测试客户端和常用断言打包在一起。
type harness struct {
	t      *testing.T
	srv    *httptest.Server
	client *http.Client
	store  *store.Store
	// bus 留给 SSE 测试：要验证推流是活的，就得能从外面推一条事件进去。
	bus *instancesvc.Bus
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	key := make([]byte, cryptobox.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := cryptobox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	conns := ociconn.New(st)
	bus := instancesvc.NewBus()
	api := New(Deps{
		Store:     st,
		Config:    config.Config{SessionTTL: time.Hour},
		Conns:     conns,
		Instances: instancesvc.New(st, conns, bus),
	})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, srv: srv, store: st, client: &http.Client{Jar: jar}, bus: bus}
}

// do 发起请求并自动带上 CSRF 头。withCSRF 为 false 时用于测试防护本身。
func (h *harness) do(method, path string, body any, withCSRF bool) *http.Response {
	h.t.Helper()

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if withCSRF {
		req.Header.Set(csrfHeader, csrfValue)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func (h *harness) post(path string, body any) *http.Response { return h.do("POST", path, body, true) }
func (h *harness) get(path string) *http.Response            { return h.do("GET", path, nil, true) }

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close()
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	return out
}

func expectStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("状态码 = %d，期望 %d；响应体: %s", resp.StatusCode, want, body)
	}
}

// setupAndLogin 走完首次设置与登录，返回可用的会话。
func (h *harness) setupAndLogin() {
	h.t.Helper()

	resp := h.post("/api/setup", map[string]string{
		"username": "admin", "password": "a-very-long-password",
	})
	expectStatus(h.t, resp, http.StatusCreated)
	resp.Body.Close()

	resp = h.post("/api/auth/login", map[string]string{
		"username": "admin", "password": "a-very-long-password",
	})
	expectStatus(h.t, resp, http.StatusOK)
	resp.Body.Close()
}

func testAccountBody(t *testing.T, alias, code, suffix string) map[string]any {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return map[string]any{
		"alias":         alias,
		"code":          code,
		"tenancyOcid":   "ocid1.tenancy.oc1..aaaa" + suffix,
		"userOcid":      "ocid1.user.oc1..aaaauser",
		"fingerprint":   ociclient.FingerprintOf(&key.PublicKey),
		"privateKeyPem": string(keyPEM),
		"defaultRegion": "ap-tokyo-1",
		// 测试环境不联网，跳过创建后的连通性校验。
		"skipCheck": true,
	}
}

func TestStatusReportsSetupRequired(t *testing.T) {
	h := newHarness(t)

	resp := h.get("/api/status")
	expectStatus(t, resp, http.StatusOK)
	st := decode[statusResponse](t, resp)

	if !st.SetupRequired {
		t.Error("尚无用户时应提示需要初始化")
	}
	if st.Authenticated {
		t.Error("未登录时不应报告已认证")
	}
}

func TestSetupOnlyAllowedOnce(t *testing.T) {
	h := newHarness(t)

	resp := h.post("/api/setup", map[string]string{
		"username": "admin", "password": "a-very-long-password",
	})
	expectStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	// 这是唯一的无鉴权写接口，必须严守"零用户"前提，否则任何人都能加管理员。
	resp = h.post("/api/setup", map[string]string{
		"username": "attacker", "password": "another-long-password",
	})
	expectStatus(t, resp, http.StatusConflict)
	resp.Body.Close()
}

func TestSetupRejectsWeakPassword(t *testing.T) {
	h := newHarness(t)
	resp := h.post("/api/setup", map[string]string{"username": "admin", "password": "short"})
	expectStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()
}

// 用户名不存在与口令错误必须给出完全相同的响应，否则接口就成了用户名枚举器。
func TestLoginDoesNotLeakUsernameExistence(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	respWrongUser := h.post("/api/auth/login", map[string]string{
		"username": "nobody", "password": "a-very-long-password",
	})
	bodyA := decode[errorBody](t, respWrongUser)

	respWrongPass := h.post("/api/auth/login", map[string]string{
		"username": "admin", "password": "wrong-password-here",
	})
	bodyB := decode[errorBody](t, respWrongPass)

	if respWrongUser.StatusCode != respWrongPass.StatusCode {
		t.Errorf("两种失败的状态码不同: %d vs %d", respWrongUser.StatusCode, respWrongPass.StatusCode)
	}
	if bodyA.Code != bodyB.Code {
		t.Errorf("两种失败的错误码不同: %q vs %q", bodyA.Code, bodyB.Code)
	}
}

func TestLoginRateLimited(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	var lastStatus int
	// 限流阈值是 5 次，多试几次确保触发。
	for i := 0; i < 8; i++ {
		resp := h.post("/api/auth/login", map[string]string{
			"username": "admin", "password": "definitely-wrong",
		})
		lastStatus = resp.StatusCode
		resp.Body.Close()
	}
	if lastStatus != http.StatusTooManyRequests {
		t.Errorf("连续失败后应被限流，最后状态码 = %d", lastStatus)
	}
}

func TestUnauthenticatedAccessRejected(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"/api/accounts", "/api/auth/me", "/api/audit"} {
		resp := h.get(path)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s 未登录时状态码 = %d，期望 401", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// 缺少自定义头的写请求必须被拒绝——这是 CSRF 防护的全部依据。
func TestCSRFGuardRejectsMissingHeader(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	resp := h.do("POST", "/api/accounts", testAccountBody(t, "东京", "TYO", "a"), false)
	expectStatus(t, resp, http.StatusForbidden)
	body := decode[errorBody](t, resp)
	if body.Code != "csrf" {
		t.Errorf("错误码 = %q，期望 csrf", body.Code)
	}

	// 读请求不受影响。
	resp = h.do("GET", "/api/accounts", nil, false)
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()
}

func TestAccountCRUD(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	resp := h.post("/api/accounts", testAccountBody(t, "东京主号", "TYO", "tokyo"))
	expectStatus(t, resp, http.StatusCreated)
	created := decode[struct {
		Account store.Account `json:"account"`
	}](t, resp)

	if created.Account.Alias != "东京主号" {
		t.Errorf("别名 = %q", created.Account.Alias)
	}
	id := created.Account.ID

	resp = h.get("/api/accounts")
	expectStatus(t, resp, http.StatusOK)
	listed := decode[struct {
		Accounts []store.Account `json:"accounts"`
	}](t, resp)
	if len(listed.Accounts) != 1 {
		t.Fatalf("列表应有 1 个账号，实际 %d", len(listed.Accounts))
	}

	newAlias := "东京备用"
	resp = h.do("PATCH", "/api/accounts/"+id, map[string]any{"alias": newAlias}, true)
	expectStatus(t, resp, http.StatusOK)
	updated := decode[store.Account](t, resp)
	if updated.Alias != newAlias {
		t.Errorf("更新后别名 = %q", updated.Alias)
	}

	resp = h.get("/api/accounts/does-not-exist")
	expectStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
}

// 删除是 L3 级危险操作。前端的输名确认框可以被绕过，服务端校验不能。
func TestDeleteAccountRequiresConfirmation(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	resp := h.post("/api/accounts", testAccountBody(t, "东京主号", "TYO", "tokyo"))
	expectStatus(t, resp, http.StatusCreated)
	created := decode[struct {
		Account store.Account `json:"account"`
	}](t, resp)
	id := created.Account.ID

	resp = h.do("DELETE", "/api/accounts/"+id, nil, true)
	expectStatus(t, resp, http.StatusBadRequest)
	if body := decode[errorBody](t, resp); body.Code != "confirm_required" {
		t.Errorf("错误码 = %q，期望 confirm_required", body.Code)
	}

	resp = h.do("DELETE", "/api/accounts/"+id+"?confirm="+url.QueryEscape("错误的名字"), nil, true)
	expectStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()

	resp = h.do("DELETE", "/api/accounts/"+id+"?confirm="+url.QueryEscape("东京主号"), nil, true)
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = h.get("/api/accounts/" + id)
	expectStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
}

func TestCreateAccountRejectsBadKey(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	body := testAccountBody(t, "东京主号", "TYO", "tokyo")
	body["privateKeyPem"] = "这不是私钥"

	resp := h.post("/api/accounts", body)
	expectStatus(t, resp, http.StatusBadRequest)
	if e := decode[errorBody](t, resp); e.Code != "invalid_input" {
		t.Errorf("错误码 = %q，期望 invalid_input", e.Code)
	}
}

func TestCreateAccountRejectsFingerprintMismatch(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	body := testAccountBody(t, "东京主号", "TYO", "tokyo")
	body["fingerprint"] = "00:11:22:33:44:55:66:77:88:99:aa:bb:cc:dd:ee:ff"

	resp := h.post("/api/accounts", body)
	expectStatus(t, resp, http.StatusBadRequest)
	e := decode[errorBody](t, resp)
	if !strings.Contains(e.Message, "指纹") {
		t.Errorf("错误信息应说明是指纹问题: %q", e.Message)
	}
}

// 私钥绝不能通过任何接口回流到客户端。
func TestAccountResponsesNeverContainPrivateKey(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	body := testAccountBody(t, "东京主号", "TYO", "tokyo")
	resp := h.post("/api/accounts", body)
	expectStatus(t, resp, http.StatusCreated)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if bytes.Contains(raw, []byte("PRIVATE KEY")) {
		t.Error("创建响应中出现了私钥内容")
	}

	for _, path := range []string{"/api/accounts", "/api/audit"} {
		resp := h.get(path)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if bytes.Contains(raw, []byte("PRIVATE KEY")) {
			t.Errorf("%s 的响应中出现了私钥内容", path)
		}
	}
}

func TestParseConfigEndpoint(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	resp := h.post("/api/accounts/parse-config", map[string]string{
		"text": "[DEFAULT]\nuser=ocid1.user.oc1..x\nfingerprint=AA:BB\n" +
			"tenancy=ocid1.tenancy.oc1..y\nregion=nrt\n",
	})
	expectStatus(t, resp, http.StatusOK)

	out := decode[struct {
		Profiles []struct {
			UserOcid      string `json:"userOcid"`
			Region        string `json:"region"`
			Fingerprint   string `json:"fingerprint"`
			Complete      bool   `json:"complete"`
			SuggestedCode string `json:"suggestedCode"`
		} `json:"profiles"`
	}](t, resp)

	if len(out.Profiles) != 1 {
		t.Fatalf("应解析出 1 个 profile，实际 %d", len(out.Profiles))
	}
	p := out.Profiles[0]
	if p.Region != "ap-tokyo-1" {
		t.Errorf("区域代号未展开: %q", p.Region)
	}
	if p.Fingerprint != "aa:bb" {
		t.Errorf("指纹未转小写: %q", p.Fingerprint)
	}
	if !p.Complete {
		t.Error("该 profile 应判为完整")
	}
	if p.SuggestedCode != "TOK" {
		t.Errorf("建议代号 = %q，期望 TOK", p.SuggestedCode)
	}
}

func TestParseConfigRejectsGarbage(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	resp := h.post("/api/accounts/parse-config", map[string]string{"text": "完全无关的一段话"})
	expectStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()
}

func TestChangePasswordInvalidatesSession(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	resp := h.post("/api/auth/password", map[string]string{
		"current": "a-very-long-password", "new": "a-brand-new-long-password",
	})
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// 改密后旧会话必须立刻失效。
	resp = h.get("/api/auth/me")
	expectStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()
}

func TestSecurityHeadersPresent(t *testing.T) {
	h := newHarness(t)

	resp := h.get("/api/status")
	defer resp.Body.Close()

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for header, value := range want {
		if got := resp.Header.Get(header); got != value {
			t.Errorf("%s = %q，期望 %q", header, got, value)
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP 缺少 frame-ancestors: %q", csp)
	}
}

func TestSessionCookieIsHardened(t *testing.T) {
	h := newHarness(t)

	resp := h.post("/api/setup", map[string]string{
		"username": "admin", "password": "a-very-long-password",
	})
	resp.Body.Close()

	resp = h.post("/api/auth/login", map[string]string{
		"username": "admin", "password": "a-very-long-password",
	})
	defer resp.Body.Close()

	var found *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			found = c
		}
	}
	if found == nil {
		t.Fatal("登录未下发会话 Cookie")
	}
	if !found.HttpOnly {
		t.Error("会话 Cookie 必须是 HttpOnly，否则 XSS 可直接窃取")
	}
	if found.SameSite != http.SameSiteStrictMode {
		t.Error("会话 Cookie 必须是 SameSite=Strict")
	}
}
