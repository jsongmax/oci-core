package ociclient

import (
	"strings"
	"testing"
)

// TestOracleBaseURLRejectsHostile 锁住绝对端点地址的校验。
//
// 这个地址来自 ListDomains 的响应，而我们会**带着 OCI 签名**向它发请求。
// 不校验的话，一个被污染的响应就能把签名过的请求引到攻击者那里去——
// 跟 region.go 防的是同一类事，只是入口从用户输入换成了响应体。
func TestOracleBaseURLRejectsHostile(t *testing.T) {
	bad := []string{
		"http://idcs-x.identity.oraclecloud.com",            // 明文
		"https://evil.com",                                  // 非 Oracle 域
		"https://idcs-x.identity.oraclecloud.com.evil.com",  // 后缀伪装
		"https://user:pass@idcs-x.identity.oraclecloud.com", // userinfo
		"https://idcs-x.identity.oraclecloud.com?a=1",       // 查询串
		"https://idcs-x.identity.oraclecloud.com#frag",      // 片段
		"ftp://idcs-x.identity.oraclecloud.com",             // 非 https
		"",                                                  // 空
		"not a url at all ::::",                             // 垃圾
	}
	for _, in := range bad {
		if got, err := oracleBaseURL(in); err == nil {
			t.Errorf("%q 应当被拒，却通过并归一化成了 %q", in, got)
		}
	}
}

// TestOracleBaseURLAcceptsReal 真实的域端点必须放行。
//
// 收得太紧会让功能整个用不了，而且失效方式是"所有账号都报错"，
// 排查起来跟被攻击一样难受。
func TestOracleBaseURLAcceptsReal(t *testing.T) {
	cases := map[string]string{
		"https://idcs-abc123.identity.oraclecloud.com":  "https://idcs-abc123.identity.oraclecloud.com",
		"https://idcs-abc123.identity.oraclecloud.com/": "https://idcs-abc123.identity.oraclecloud.com",
		// 政府云 realm 也要认。
		"https://idcs-x.identity.oraclegovcloud.com": "https://idcs-x.identity.oraclegovcloud.com",
	}
	for in, want := range cases {
		got, err := oracleBaseURL(in)
		if err != nil {
			t.Errorf("合法端点 %q 被拒: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("归一化 %q -> %q，期望 %q", in, got, want)
		}
	}
}

// TestDomainEndpointPrefersRegionAgnostic URL 优先于 HomeRegionURL。
func TestDomainEndpointPrefersRegionAgnostic(t *testing.T) {
	d := Domain{URL: "https://a.identity.oraclecloud.com", HomeRegionURL: "https://b.identity.oraclecloud.com"}
	if got := d.Endpoint(); got != "https://a.identity.oraclecloud.com" {
		t.Errorf("应优先用区域无关地址，实际 %q", got)
	}
	// 只有 HomeRegionURL 时回落到它，而不是返回空串让调用方拿着空地址去发请求。
	d2 := Domain{HomeRegionURL: "https://b.identity.oraclecloud.com"}
	if got := d2.Endpoint(); got != "https://b.identity.oraclecloud.com" {
		t.Errorf("应回落到 HomeRegionURL，实际 %q", got)
	}
}

// TestSetPasswordExpiryRejectsNegative 负数天数在本地就该挡住。
func TestSetPasswordExpiryRejectsNegative(t *testing.T) {
	c, err := New(testCreds(t, testKey(t)))
	if err != nil {
		t.Fatal(err)
	}
	n := -1
	_, err = c.SetPasswordExpiry(t.Context(), "https://idcs-x.identity.oraclecloud.com", "pp1", &n)
	if err == nil {
		t.Fatal("负数应被拒")
	}
	if !strings.Contains(err.Error(), "不能为负") {
		t.Errorf("错误信息应点明原因，实际: %v", err)
	}
}

// TestBaseURLBypassesRegionValidation 走绝对地址时不该再校验区域。
//
// 身份域端点跟区域无关，若仍走区域校验，凭据里区域为空的客户端会被
// 无谓地拦下。
func TestBaseURLBypassesRegionValidation(t *testing.T) {
	c, err := New(testCreds(t, testKey(t)))
	if err != nil {
		t.Fatal(err)
	}
	// 地址非法 -> 应报端点相关的错，而不是"区域名不合法"。
	_, err = c.Do(t.Context(), Request{
		Method: "GET", BaseURL: "https://evil.com", Path: "/admin/v1/PasswordPolicies",
	}, nil)
	if err == nil {
		t.Fatal("非 Oracle 端点应被拒")
	}
	if strings.Contains(err.Error(), "区域名不合法") {
		t.Errorf("不该走到区域校验分支: %v", err)
	}
}
