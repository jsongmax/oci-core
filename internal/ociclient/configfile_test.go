package ociclient

import "testing"

// 用户从 Oracle 控制台复制的原样文本。
const consoleSample = `[DEFAULT]
user=ocid1.user.oc1..aaaauser
fingerprint=20:3B:97:13:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55
tenancy=ocid1.tenancy.oc1..aaaatenancy
region=ap-tokyo-1
key_file=<path to your private keyfile> # TODO
`

func TestParseConfigFileConsoleSample(t *testing.T) {
	profiles := ParseConfigFile(consoleSample)
	if len(profiles) != 1 {
		t.Fatalf("应解析出 1 个 profile，实际 %d", len(profiles))
	}
	p := profiles[0]

	if p.Name != "DEFAULT" {
		t.Errorf("profile 名 = %q", p.Name)
	}
	if p.User != "ocid1.user.oc1..aaaauser" {
		t.Errorf("user = %q", p.User)
	}
	if p.Tenancy != "ocid1.tenancy.oc1..aaaatenancy" {
		t.Errorf("tenancy = %q", p.Tenancy)
	}
	// 指纹统一转小写，避免同一份凭据因大小写不同被判为不匹配。
	if p.Fingerprint != "20:3b:97:13:aa:bb:cc:dd:ee:ff:00:11:22:33:44:55" {
		t.Errorf("fingerprint 应转为小写，实际 %q", p.Fingerprint)
	}
	if p.Region != "ap-tokyo-1" {
		t.Errorf("region = %q", p.Region)
	}
	if !p.Complete() {
		t.Errorf("该 profile 应当是完整的，缺失: %v", p.MissingFields())
	}
}

// OCID 与指纹里都含有冒号，绝不能在第一个冒号之外的位置拆分。
func TestParseConfigFileHandlesColonsInValues(t *testing.T) {
	profiles := ParseConfigFile("fingerprint: aa:bb:cc:dd\nuser: ocid1.user.oc1..x\n")
	if len(profiles) != 1 {
		t.Fatalf("应解析出 1 个 profile，实际 %d", len(profiles))
	}
	if profiles[0].Fingerprint != "aa:bb:cc:dd" {
		t.Errorf("指纹被错误截断: %q", profiles[0].Fingerprint)
	}
}

// 用户经常只粘贴几行，甚至漏掉 [DEFAULT] 头。这时也要尽力解析，
// 并如实报告缺了哪些字段，而不是整段丢弃。
func TestParseConfigFilePartialWithoutHeader(t *testing.T) {
	profiles := ParseConfigFile("user=ocid1.user.oc1..x\ntenancy=ocid1.tenancy.oc1..y\n")
	if len(profiles) != 1 {
		t.Fatalf("应解析出 1 个 profile，实际 %d", len(profiles))
	}
	p := profiles[0]
	if p.Complete() {
		t.Error("缺少 fingerprint 与 region，不应判为完整")
	}

	missing := map[string]bool{}
	for _, f := range p.MissingFields() {
		missing[f] = true
	}
	if !missing["fingerprint"] || !missing["region"] {
		t.Errorf("缺失字段应包含 fingerprint 与 region，实际 %v", p.MissingFields())
	}
}

func TestParseConfigFileMultipleProfiles(t *testing.T) {
	text := `[DEFAULT]
user=ocid1.user.oc1..a
tenancy=ocid1.tenancy.oc1..a
fingerprint=aa:bb
region=ap-tokyo-1

[BACKUP]
user=ocid1.user.oc1..b
tenancy=ocid1.tenancy.oc1..b
fingerprint=cc:dd
region=eu-frankfurt-1
`
	profiles := ParseConfigFile(text)
	if len(profiles) != 2 {
		t.Fatalf("应解析出 2 个 profile，实际 %d", len(profiles))
	}
	if profiles[0].Name != "DEFAULT" || profiles[1].Name != "BACKUP" {
		t.Errorf("profile 名不正确: %q, %q", profiles[0].Name, profiles[1].Name)
	}
	if profiles[1].Region != "eu-frankfurt-1" {
		t.Errorf("第二个 profile 的 region = %q", profiles[1].Region)
	}
}

// 带 BOM 是"明明粘贴对了却解析不出来"的常见成因，必须容忍。
func TestParseConfigFileTolerantOfBOMAndComments(t *testing.T) {
	text := utf8BOM + "# 这是注释\n; 这也是\n\nuser = ocid1.user.oc1..x\nregion = nrt\n"
	profiles := ParseConfigFile(text)
	if len(profiles) != 1 {
		t.Fatalf("应解析出 1 个 profile，实际 %d", len(profiles))
	}
	if profiles[0].User != "ocid1.user.oc1..x" {
		t.Errorf("BOM 未被去除，user = %q", profiles[0].User)
	}
	// 三字母代号应被展开成区域全名。
	if profiles[0].Region != "ap-tokyo-1" {
		t.Errorf("区域代号未展开，region = %q", profiles[0].Region)
	}
}

func TestParseConfigFileDetectsPassPhrase(t *testing.T) {
	profiles := ParseConfigFile("user=ocid1.user.oc1..x\npass_phrase=secret\n")
	if len(profiles) != 1 {
		t.Fatalf("应解析出 1 个 profile，实际 %d", len(profiles))
	}
	if !profiles[0].HasPassPhrase {
		t.Error("应识别出配置声明了私钥口令")
	}
}

func TestParseConfigFileEmpty(t *testing.T) {
	if got := ParseConfigFile(""); len(got) != 0 {
		t.Errorf("空文本不应解析出 profile，实际 %d 个", len(got))
	}
	if got := ParseConfigFile("完全无关的一段话"); len(got) != 0 {
		t.Errorf("无关文本不应解析出 profile，实际 %d 个", len(got))
	}
}

func TestNormalizeRegion(t *testing.T) {
	cases := map[string]string{
		"nrt":            "ap-tokyo-1",
		"NRT":            "ap-tokyo-1",
		"  fra  ":        "eu-frankfurt-1",
		"ap-tokyo-1":     "ap-tokyo-1",
		"AP-TOKYO-1":     "ap-tokyo-1",
		"ap-newregion-1": "ap-newregion-1", // 未登记的新区域按原样通过
	}
	for in, want := range cases {
		if got := NormalizeRegion(in); got != want {
			t.Errorf("NormalizeRegion(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestEndpoint(t *testing.T) {
	got := Endpoint(ServiceIdentity, "nrt")
	want := "https://identity.ap-tokyo-1.oraclecloud.com"
	if got != want {
		t.Errorf("Endpoint = %q，期望 %q", got, want)
	}
	if got := Endpoint(ServiceCore, "uk-gov-london-1"); got != "https://iaas.uk-gov-london-1.oraclegovcloud.uk" {
		t.Errorf("政府云 realm 解析错误: %q", got)
	}

	// limits 的域名多一段 .oci。写漏了不会报错——泛解析让 DNS 照样有结果，
	// 但 TCP 连不上，请求会一直挂到超时。这条断言就是为了别再踩一次。
	if got := Endpoint(ServiceLimits, "kix"); got != "https://limits.ap-osaka-1.oci.oraclecloud.com" {
		t.Errorf("limits 端点缺少 .oci 段: %q", got)
	}
	// 版本段同样是踩过的坑：20181025 是配额（quotas）服务的版本，
	// 限额（limits）服务是 20190729，用错了整条链路返回 404。
	if ServiceLimits.Version != "20190729" {
		t.Errorf("limits API 版本 = %q，期望 20190729", ServiceLimits.Version)
	}
}

// TestNormalizeRegionRejectsHostInjection 锁住区域名的格式校验。
//
// 区域名会被拼进 Endpoint 的主机名部分。含 "/" 的输入能把主机名提前截断，
// 让请求连同 OCI 签名一起发到攻击者的服务器上——而这个失效完全静默：
// 请求照常发出，只是发错了地方。
func TestNormalizeRegionRejectsHostInjection(t *testing.T) {
	// 合法输入必须原样通过，否则会误伤 Oracle 将来新开的区域。
	for _, ok := range []string{"ap-osaka-1", "us-ashburn-1", "eu-frankfurt-1", "mx-queretaro-1"} {
		if got := NormalizeRegion(ok); got != ok {
			t.Errorf("合法区域 %q 被改成了 %q", ok, got)
		}
	}
	// 三字母代号仍要能展开。
	if got := NormalizeRegion("KIX"); got != "ap-osaka-1" {
		t.Errorf("短代号展开失败: %q", got)
	}

	for _, bad := range []string{
		"evil.com/",           // 斜杠截断主机名
		"evil.com/x",          //
		"a/../b",              //
		"host:8080",           // 端口
		"a@evil.com",          // userinfo
		"a b",                 // 空格
		"a?x=1",               // 查询串
		"a#frag",              // 片段
		"ap-osaka-1.evil.com", // 点号
	} {
		if got := NormalizeRegion(bad); got != "" {
			t.Errorf("非法区域 %q 应当被拒，却返回了 %q", bad, got)
		}
	}
}
