package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"ocicore/internal/config"
)

// TestClientIPIgnoresForgedForwardedFor 锁住「取最后一跳」这条规则。
//
// X-Forwarded-For 的格式是「客户端声称的值, 各层代理逐跳追加的值」。
// 取第一段会让攻击者自选限流桶：每个请求换一个伪造 IP，登录爆破的次数
// 限制就形同虚设，审计日志里留下的也全是编的地址。
//
// 这个失效没有任何可见症状——限流器照常工作，只是永远不会命中。
func TestClientIPIgnoresForgedForwardedFor(t *testing.T) {
	trusting := &Server{cfg: config.Config{TrustProxyHeaders: true}}
	plain := &Server{cfg: config.Config{TrustProxyHeaders: false}}

	cases := []struct {
		name   string
		srv    *Server
		xff    string
		remote string
		want   string
	}{
		{
			// 攻击者伪造在前，nginx 把真实 IP 追加在后。必须取后者。
			name: "伪造值在前，代理追加的真实值在后",
			srv:  trusting, xff: "1.2.3.4, 203.0.113.9", remote: "10.0.0.1:5000",
			want: "203.0.113.9",
		},
		{
			name: "多层伪造仍然只认最后一跳",
			srv:  trusting, xff: "9.9.9.9, 8.8.8.8, 203.0.113.9", remote: "10.0.0.1:5000",
			want: "203.0.113.9",
		},
		{
			name: "只有一段时直接采信——那就是代理填的",
			srv:  trusting, xff: "203.0.113.9", remote: "10.0.0.1:5000",
			want: "203.0.113.9",
		},
		{
			// 没配代理时绝不能看这个头，否则任何人都能伪造。
			name: "未开启 TrustProxy 时完全忽略该头",
			srv:  plain, xff: "1.2.3.4", remote: "203.0.113.9:5000",
			want: "203.0.113.9",
		},
		{
			name: "IPv6 的 RemoteAddr 要去掉方括号与端口",
			srv:  plain, xff: "", remote: "[2001:db8::1]:5000",
			want: "2001:db8::1",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = c.remote
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if got := c.srv.clientIP(r); got != c.want {
				t.Errorf("clientIP = %q，期望 %q", got, c.want)
			}
		})
	}
}
