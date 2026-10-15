package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// F06：兼容字段 proxyUrl 里的代理密码不能明文落库，也不能出现在任何账号响应里。
func TestLegacyProxyURLNeverStoredOrReturned(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	const secret = "legacy-proxy-secret-pw"
	body := testAccountBody(t, "兼容", "LEG", "legacy")
	body["proxyUrl"] = "http://user:" + secret + "@203.0.113.7:3128"
	body["skipCheck"] = true
	resp := h.post("/api/accounts", body)
	expectStatus(t, resp, http.StatusCreated)
	created := decode[map[string]any](t, resp)
	id := created["account"].(map[string]any)["id"].(string)

	for _, path := range []string{"/api/accounts", "/api/accounts/" + id} {
		resp := h.get(path)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(raw), secret) {
			t.Errorf("%s 的响应里出现了代理密码: %s", path, raw)
		}
		if strings.Contains(string(raw), `"proxyUrl"`) {
			t.Errorf("%s 不应再返回 proxyUrl 字段", path)
		}
	}

	var legacy string
	if err := h.store.DB().QueryRow(`SELECT proxy_url FROM accounts WHERE id = ?`, id).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy != "" {
		t.Errorf("accounts.proxy_url 不应保存原始地址，实际 %q", legacy)
	}

	ctx := context.Background()
	pid, err := h.store.AccountProxyID(ctx, id)
	if err != nil || pid == "" {
		t.Fatalf("proxyUrl 应转成代理池实体并绑定，proxy_id=%q err=%v", pid, err)
	}
	u, err := h.store.ProxyURL(ctx, pid)
	if err != nil || !strings.Contains(u, secret) {
		t.Errorf("代理池里的地址应能解密还原，实际 %q err=%v", u, err)
	}
}

// F08：通知 webhook 默认不能访问回环地址，测试按钮应返回失败且请求不能到达目标。
func TestWebhookChannelCannotReachLoopback(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer target.Close()

	h := newHarness(t)
	h.setupAndLogin()
	resp := h.post("/api/notifications/channels", map[string]any{
		"kind": "webhook", "name": "loopback", "config": map[string]string{"url": target.URL},
	})
	expectStatus(t, resp, http.StatusCreated)
	ch := decode[map[string]any](t, resp)

	resp = h.post("/api/notifications/channels/"+ch["id"].(string)+"/test", nil)
	expectStatus(t, resp, http.StatusOK)
	result := decode[map[string]any](t, resp)
	if result["ok"] != false {
		t.Errorf("向回环地址发送应失败，实际 %v", result)
	}
	if hits.Load() != 0 {
		t.Error("请求不应到达回环服务")
	}
}

func TestWebhookChannelRejectsNonHTTPURL(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()
	resp := h.post("/api/notifications/channels", map[string]any{
		"kind": "webhook", "name": "file", "config": map[string]string{"url": "file:///etc/passwd"},
	})
	expectStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()
}
