package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"

	"ocicore/internal/store"
)

// loopbackDispatcher 放行 127.0.0.1，供需要本地 httptest 服务的测试使用。
func loopbackDispatcher() *Dispatcher {
	return NewDispatcher(nil, WithAllowedNets([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}))
}

func TestIsPublic(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8":          true,
		"2606:4700::1111":  true,
		"127.0.0.1":        false,
		"::1":              false,
		"10.1.2.3":         false,
		"172.16.0.1":       false,
		"192.168.1.1":      false,
		"169.254.169.254":  false, // 云元数据
		"fe80::1":          false,
		"fc00::1":          false,
		"0.0.0.0":          false,
		"100.100.100.200":  false, // 运营商级 NAT，阿里云元数据也在这里
		"::ffff:127.0.0.1": false,
		"::ffff:10.0.0.1":  false,
		"224.0.0.1":        false,
	}
	for s, want := range cases {
		if got := isPublic(netip.MustParseAddr(s)); got != want {
			t.Errorf("isPublic(%s) = %v，期望 %v", s, got, want)
		}
	}
}

// 默认禁止：请求不能到达回环服务，错误可识别。
func TestWebhookToLoopbackBlockedByDefault(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()

	d := NewDispatcher(nil)
	ch := &store.Channel{Kind: KindWebhook, Config: map[string]string{"url": srv.URL}}
	err := d.Send(context.Background(), ch, Message{Title: "x"})
	if !errors.Is(err, ErrEgressBlocked) {
		t.Fatalf("应因出网边界失败，实际 %v", err)
	}
	if hits.Load() != 0 {
		t.Error("请求不应到达回环服务")
	}

	// 主机名同样按解析结果判断。
	ch.Config["url"] = "http://localhost:1/"
	if err := d.Send(context.Background(), ch, Message{Title: "x"}); !errors.Is(err, ErrEgressBlocked) {
		t.Errorf("localhost 应被拒绝，实际 %v", err)
	}
}

// 每次跳转都要重新校验：放行的目标跳到未放行的内网地址必须被拦下。
func TestRedirectToPrivateAddressBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()

	d := loopbackDispatcher()
	ch := &store.Channel{Kind: KindWebhook, Config: map[string]string{"url": srv.URL}}
	if err := d.Send(context.Background(), ch, Message{Title: "x"}); !errors.Is(err, ErrEgressBlocked) {
		t.Fatalf("跳转到链路本地地址应被拦下，实际 %v", err)
	}
}

// 拨号阶段按解析后的真实 IP 校验，这是防 DNS 重绑定的那一层。
func TestDialControlChecksResolvedIP(t *testing.T) {
	g := &egressGuard{}
	for _, addr := range []string{"127.0.0.1:80", "[::1]:443", "10.0.0.5:8080", "169.254.169.254:80"} {
		if err := g.control("tcp", addr, nil); !errors.Is(err, ErrEgressBlocked) {
			t.Errorf("拨号 %s 应被拒绝，实际 %v", addr, err)
		}
	}
	if err := g.control("tcp", "203.0.113.10:443", nil); err != nil {
		t.Errorf("公网地址应放行: %v", err)
	}

	g.allowed = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	if err := g.control("tcp", "10.0.0.5:8080", nil); err != nil {
		t.Errorf("显式放行的网段应可访问: %v", err)
	}
	if err := g.control("tcp", "192.168.0.5:8080", nil); err == nil {
		t.Error("放行一个网段不应连带放行其他内网")
	}
}

// R3：通知客户端不走环境代理。经代理时目标由代理端解析，拨号阶段的 IP 校验看不到它。
func TestNotifyClientIgnoresEnvironmentProxy(t *testing.T) {
	tr, ok := newHTTPClient(nil).Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport 类型异常")
	}
	if tr.Proxy != nil {
		t.Error("通知客户端不应使用环境代理")
	}
}
