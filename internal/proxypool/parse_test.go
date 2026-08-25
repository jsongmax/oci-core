package proxypool

import "testing"

// TestParseLineFormats 锁住代理商实际会给的那几种写法。
//
// 这个解析器的价值全在"宽容"上：认不出来，用户就得手工把二十行重排一遍。
func TestParseLineFormats(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Parsed
	}{
		{"裸 host:port", "1.2.3.4:8080",
			Parsed{Scheme: "http", Host: "1.2.3.4", Port: 8080}},
		{"代理商最常给的四段式", "1.2.3.4:8080:alice:s3cret",
			Parsed{Scheme: "http", Host: "1.2.3.4", Port: 8080, Username: "alice", Password: "s3cret"}},
		{"标准 URL 认证", "alice:s3cret@1.2.3.4:8080",
			Parsed{Scheme: "http", Host: "1.2.3.4", Port: 8080, Username: "alice", Password: "s3cret"}},
		{"带协议", "socks5://alice:s3cret@1.2.3.4:1080",
			Parsed{Scheme: "socks5", Host: "1.2.3.4", Port: 1080, Username: "alice", Password: "s3cret"}},
		{"带备注", "http://1.2.3.4:8080  # 香港节点",
			Parsed{Scheme: "http", Host: "1.2.3.4", Port: 8080, Label: "香港节点"}},
		{"域名而非 IP", "proxy.example.com:3128",
			Parsed{Scheme: "http", Host: "proxy.example.com", Port: 3128}},
		{"只有用户名没有密码", "alice@1.2.3.4:8080",
			Parsed{Scheme: "http", Host: "1.2.3.4", Port: 8080, Username: "alice"}},
		{"IPv6 字面量", "[2001:db8::1]:8080",
			Parsed{Scheme: "http", Host: "2001:db8::1", Port: 8080}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseLine(c.in)
			if err != nil {
				t.Fatalf("解析 %q 失败: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("解析 %q\n得到 %+v\n期望 %+v", c.in, got, c.want)
			}
		})
	}
}

// TestParseLinePasswordWithAt 密码里含 @ 时要按最后一个 @ 切。
//
// 用 Index 而不是 LastIndex 会把密码从中间截断，得到一个能存进去、
// 但永远认证失败的代理——而且看不出哪里错了。
func TestParseLinePasswordWithAt(t *testing.T) {
	got, err := ParseLine("alice:p@ss@1.2.3.4:8080")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got.Username != "alice" || got.Password != "p@ss" {
		t.Errorf("认证信息解析错误: user=%q pass=%q", got.Username, got.Password)
	}
	if got.Host != "1.2.3.4" || got.Port != 8080 {
		t.Errorf("地址解析错误: %s", got.Addr())
	}
}

// TestParseLineRejects 锁住必须拒绝的输入。
func TestParseLineRejects(t *testing.T) {
	cases := []struct{ name, in string }{
		{"空行", "   "},
		{"只有注释", "# 这一段是说明"},
		{"缺端口", "1.2.3.4"},
		{"端口非数字", "1.2.3.4:abcd"},
		{"端口越界", "1.2.3.4:70000"},
		{"端口为零", "1.2.3.4:0"},
		// socks5h 要显式拒绝：Go 的 net/http 不支持它，
		// 放行只会让它在真正发请求时才神秘失败。
		{"socks5h", "socks5h://1.2.3.4:1080"},
		{"未知协议", "ftp://1.2.3.4:21"},
		{"三段式无法判定", "1.2.3.4:8080:alice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseLine(c.in); err == nil {
				t.Errorf("%q 应该被拒绝，却解析成功了", c.in)
			}
		})
	}
}

// TestMaskedHidesPassword 脱敏后不能残留密码。
//
// 这个字符串会进 API 响应和界面。代理密码是凭据，和 OCI 私钥同级——
// 那边的规矩是"界面上没有任何导出或回显入口"。
func TestMaskedHidesPassword(t *testing.T) {
	p, err := ParseLine("socks5://alice:s3cret@1.2.3.4:1080")
	if err != nil {
		t.Fatal(err)
	}
	masked := p.Masked()
	if contains(masked, "s3cret") {
		t.Errorf("脱敏结果里仍能看到密码: %s", masked)
	}
	if !contains(masked, "alice") || !contains(masked, "1.2.3.4:1080") {
		t.Errorf("脱敏过头了，用户名和地址应当保留: %s", masked)
	}
}

// TestURLRoundTrip URL() 生成的地址要能被标准库解析回去。
//
// 它会被直接交给 http.Transport，特殊字符没转义的话会在那里炸。
func TestURLRoundTrip(t *testing.T) {
	p, err := ParseLine("http://alice:p@ss word@1.2.3.4:8080")
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseLine(p.URL())
	if err != nil {
		t.Fatalf("自己生成的 URL 解析不回来: %s (%v)", p.URL(), err)
	}
	if back.Host != p.Host || back.Port != p.Port {
		t.Errorf("往返后地址变了: %s -> %s", p.Addr(), back.Addr())
	}
}

// TestParseBulkSkipsBlanksKeepsErrors 空行跳过，错误行必须留下且带行号。
//
// 用户粘二十行进来，得知道是第几行有问题。只报"成功 19 条"等于让他自己找。
func TestParseBulkSkipsBlanksKeepsErrors(t *testing.T) {
	text := "1.2.3.4:8080\n\n   \n# 纯注释\n坏行\n5.6.7.8:1080:bob:pw\n"
	got := ParseBulk(text)

	if len(got) != 3 {
		t.Fatalf("应有 3 条结果（2 成功 1 失败），实际 %d 条: %+v", len(got), got)
	}
	if got[0].Error != "" || got[0].Line != 1 {
		t.Errorf("第一条应成功且行号为 1: %+v", got[0])
	}
	if got[1].Error == "" {
		t.Errorf("坏行应被标为失败: %+v", got[1])
	}
	if got[1].Line != 5 {
		t.Errorf("失败行的行号应是原文行号 5，实际 %d", got[1].Line)
	}
	if got[2].Error != "" || got[2].Proxy.Username != "bob" {
		t.Errorf("第三条解析有误: %+v", got[2])
	}
}

// TestParseBulkDoesNotEchoPassword 批量结果里不能回显密码。
//
// 成功行的 Raw 会显示在导入预览里。原样回显等于把整张代理表的密码
// 铺在界面上。
func TestParseBulkDoesNotEchoPassword(t *testing.T) {
	for _, r := range ParseBulk("1.2.3.4:8080:alice:s3cret\n") {
		if contains(r.Raw, "s3cret") || contains(r.Masked, "s3cret") {
			t.Errorf("批量结果里残留了密码: %+v", r)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
