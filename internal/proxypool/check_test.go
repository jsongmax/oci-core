package proxypool

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestDescribeErrMapsCommonFailures 常见失败要翻成能直接照着做的一句话。
//
// 原始错误长这样：
//
//	Head "https://iaas...": proxyconnect tcp: dial tcp 1.2.3.4:8080: i/o timeout
//
// 直接贴进界面没人看得懂，而这几种失败的处置完全不同——换代理、改密码、
// 还是改协议前缀。不区分开，用户只能挨个试。
func TestDescribeErrMapsCommonFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string // 期望出现在结果里的关键词
	}{
		{"连代理超时", errors.New(`proxyconnect tcp: dial tcp 1.2.3.4:8080: i/o timeout`), "连接代理超时"},
		{"到不了 OCI", errors.New(`Head "https://iaas...": context deadline exceeded (Client.Timeout)`), "到不了"},
		{"认证失败", errors.New(`Proxy Authentication Required`), "用户名或密码"},
		{"拒绝连接 unix", errors.New(`dial tcp 1.2.3.4:8080: connect: connection refused`), "拒绝连接"},
		// Windows 的措辞不一样，也得认——不少人在本地 Windows 上跑。
		{"拒绝连接 windows", errors.New(`connectex: No connection could be made because the target machine actively refused it.`), "拒绝连接"},
		{"域名解析不了", errors.New(`dial tcp: lookup bad.example: no such host`), "解析不了"},
		// 这条最容易被误判成"代理挂了"，实际上是协议前缀写反了。
		{"协议写错", errors.New(`Head "https://iaas...": unexpected EOF`), "协议写错"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := describeErr(c.err)
			if !strings.Contains(got, c.want) {
				t.Errorf("describeErr(%v)\n得到 %q\n期望包含 %q", c.err, got, c.want)
			}
		})
	}
}

// TestDescribeErrKeepsUnknownVerbatim 认不出来的错误要原样保留。
//
// 宁可长一点难看一点，也不要把排障线索吃掉换成一句"未知错误"。
func TestDescribeErrKeepsUnknownVerbatim(t *testing.T) {
	msg := "some brand new failure mode from the future"
	if got := describeErr(errors.New(msg)); got != msg {
		t.Errorf("未知错误应原样保留，实际 %q", got)
	}
}

// TestCheckRejectsBadInput 代理地址或区域非法时应当立刻失败，不发请求。
func TestCheckRejectsBadInput(t *testing.T) {
	c := NewChecker(0)

	if r := c.Check(t.Context(), "://not a url", "us-ashburn-1"); r.Status != StatusFail {
		t.Errorf("非法代理地址应判失败，实际 %+v", r)
	}
	// 区域会被拼进主机名。带斜杠的输入能把主机名提前截断，
	// 让请求连同代理凭据一起发到别处去。
	if r := c.Check(t.Context(), "http://1.2.3.4:8080", "evil.com/"); r.Status != StatusFail {
		t.Errorf("非法区域应判失败，实际 %+v", r)
	}
}

// TestDuplicateOf 找出与目标代理冲突的账号。
//
// 共用出口比不用代理更糟——它把两个本来从不同网络访问的账号绑在同一个
// IP 上。这个判断是"禁止共用"那条硬约束的依据。
func TestDuplicateOf(t *testing.T) {
	bindings := map[string]string{"accA": "p1", "accB": "p2"}

	if got := DuplicateOf(bindings, "p1", "accB"); len(got) != 1 || got[0] != "accA" {
		t.Errorf("应检出 accA 已占用 p1，实际 %v", got)
	}
	// 绑给原来那个账号不算冲突，那只是幂等重放。
	if got := DuplicateOf(bindings, "p1", "accA"); len(got) != 0 {
		t.Errorf("绑给原账号不该算冲突，实际 %v", got)
	}
	// 空代理 id 表示解绑，永远不冲突。
	if got := DuplicateOf(bindings, "", "accA"); len(got) != 0 {
		t.Errorf("解绑不该算冲突，实际 %v", got)
	}
}

var _ = context.Background
