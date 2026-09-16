package netsvc

import (
	"testing"

	"ocicore/internal/ociclient"
)

func TestRuleTemplatesMarkAllowAllAsDangerous(t *testing.T) {
	templates := RuleTemplates()
	if len(templates) == 0 {
		t.Fatal("应当提供快捷规则模板")
	}

	var allowAll *RuleTemplate
	for i := range templates {
		if templates[i].Key == "all" {
			allowAll = &templates[i]
		}
		if templates[i].Label == "" || templates[i].Description == "" {
			t.Errorf("模板 %q 缺少标签或说明", templates[i].Key)
		}
	}
	if allowAll == nil {
		t.Fatal("缺少全放行模板")
	}
	// 全放行会把机器整个暴露到公网，必须带危险标记让 UI 显著警示。
	if !allowAll.Dangerous {
		t.Error("全放行模板必须标记为危险")
	}

	for _, tpl := range templates {
		if tpl.Key != "all" && tpl.Dangerous {
			t.Errorf("模板 %q 不应被标记为危险", tpl.Key)
		}
	}
}

func TestBuildIngressRuleTCP(t *testing.T) {
	tpl := RuleTemplate{Key: "ssh", Label: "SSH", Protocol: "6", Port: 22, Description: "远程登录"}
	rule := BuildIngressRule(tpl, "", "")

	if rule.Protocol != "6" {
		t.Errorf("协议 = %q，期望 6", rule.Protocol)
	}
	// 来源留空应当补成 0.0.0.0/0，否则规则建出来是无效的。
	if rule.Source != "0.0.0.0/0" {
		t.Errorf("来源 = %q，期望 0.0.0.0/0", rule.Source)
	}
	if rule.Description != "远程登录" {
		t.Errorf("说明 = %q", rule.Description)
	}
	if rule.TCPOptions == nil || rule.TCPOptions.DestinationPortRange == nil {
		t.Fatal("TCP 规则应当带端口范围")
	}
	if rule.TCPOptions.DestinationPortRange.Min != 22 || rule.TCPOptions.DestinationPortRange.Max != 22 {
		t.Errorf("端口范围不正确: %+v", rule.TCPOptions.DestinationPortRange)
	}
}

func TestBuildIngressRuleRespectsCustomSource(t *testing.T) {
	tpl := RuleTemplate{Key: "http", Protocol: "6", Port: 80}
	rule := BuildIngressRule(tpl, "203.0.113.0/24", "仅办公网")

	if rule.Source != "203.0.113.0/24" {
		t.Errorf("来源 = %q", rule.Source)
	}
	if rule.Description != "仅办公网" {
		t.Errorf("说明 = %q", rule.Description)
	}
}

func TestBuildIngressRuleICMP(t *testing.T) {
	tpl := RuleTemplate{Key: "icmp", Protocol: "1"}
	rule := BuildIngressRule(tpl, "", "")

	if rule.ICMPOptions == nil {
		t.Fatal("ICMP 规则应当带 icmpOptions")
	}
	if rule.TCPOptions != nil || rule.UDPOptions != nil {
		t.Error("ICMP 规则不应带 TCP/UDP 选项")
	}
}

func TestIsAllowAllRule(t *testing.T) {
	cases := []struct {
		name string
		rule ociclient.IngressSecurityRule
		want bool
	}{
		{"全放行 v4", ociclient.IngressSecurityRule{Protocol: "all", Source: "0.0.0.0/0"}, true},
		{"全放行 v6", ociclient.IngressSecurityRule{Protocol: "all", Source: "::/0"}, true},
		{"大小写不敏感", ociclient.IngressSecurityRule{Protocol: "ALL", Source: "0.0.0.0/0"}, true},
		// 限定来源网段的全协议规则不算全放行，不该误报警示。
		{"限定来源", ociclient.IngressSecurityRule{Protocol: "all", Source: "10.0.0.0/8"}, false},
		{"仅 TCP", ociclient.IngressSecurityRule{Protocol: "6", Source: "0.0.0.0/0"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAllowAllRule(tc.rule); got != tc.want {
				t.Errorf("IsAllowAllRule = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// 每个模板都必须写明方向。缺了方向的模板会被前端当成入站追加进去——
// 一条本该放到出站的 ALL 规则落在入站，等于把机器整个挂上公网。
func TestRuleTemplatesDeclareDirection(t *testing.T) {
	for _, tpl := range RuleTemplates() {
		switch tpl.Direction {
		case DirIngress, DirEgress:
		default:
			t.Errorf("模板 %q 的方向非法: %q", tpl.Key, tpl.Direction)
		}
	}
}

// 出站全放行是 OCI 新建安全列表的默认规则。面板允许删出站，就必须能加回来，
// 否则用户删掉之后子网彻底断网且无从恢复。
func TestRuleTemplatesIncludeEgressAllowAll(t *testing.T) {
	var found *RuleTemplate
	for _, tpl := range RuleTemplates() {
		if tpl.IsEgress() {
			found = &tpl
			break
		}
	}
	if found == nil {
		t.Fatal("缺少出站模板，删掉出站规则后无法恢复")
	}
	if found.Protocol != "all" {
		t.Errorf("出站默认模板应放行所有协议，实际为 %q", found.Protocol)
	}
	// 出站全放行是默认配置，不该当成危险操作吓唬用户——缺了它才是故障。
	if found.Dangerous {
		t.Error("出站全放行是 OCI 默认配置，不应标记为危险")
	}
}

func TestBuildEgressRuleDefaultsToAnywhere(t *testing.T) {
	tpl := RuleTemplate{Key: "egress-all", Direction: DirEgress, Protocol: "all", Description: "全部出站"}
	rule := BuildEgressRule(tpl, "", "")

	if rule.Destination != "0.0.0.0/0" {
		t.Errorf("目标应默认为 0.0.0.0/0，实际为 %q", rule.Destination)
	}
	if rule.DestinationType != "CIDR_BLOCK" {
		t.Errorf("目标类型应为 CIDR_BLOCK，实际为 %q", rule.DestinationType)
	}
	if rule.Description != "全部出站" {
		t.Errorf("描述应回落到模板说明，实际为 %q", rule.Description)
	}
	// 协议为 all 时不能带端口选项，OCI 会直接拒掉这种请求。
	if rule.TCPOptions != nil || rule.UDPOptions != nil || rule.ICMPOptions != nil {
		t.Error("全协议规则不应携带端口选项")
	}
}

func TestBuildEgressRuleTCP(t *testing.T) {
	tpl := RuleTemplate{Key: "smtp", Direction: DirEgress, Protocol: "6", Port: 587}
	rule := BuildEgressRule(tpl, "10.0.0.0/16", "只允许发到内网邮件网关")

	if rule.Destination != "10.0.0.0/16" {
		t.Errorf("目标应保留调用方传入的值，实际为 %q", rule.Destination)
	}
	if rule.TCPOptions == nil || rule.TCPOptions.DestinationPortRange == nil {
		t.Fatal("TCP 规则应带目的端口范围")
	}
	if got := rule.TCPOptions.DestinationPortRange; got.Min != 587 || got.Max != 587 {
		t.Errorf("端口范围应为 587-587，实际为 %d-%d", got.Min, got.Max)
	}
}

func TestHasEgress(t *testing.T) {
	empty := ociclient.SecurityList{}
	if HasEgress(empty) {
		t.Error("没有出站规则时应返回 false")
	}

	filled := ociclient.SecurityList{
		EgressSecurityRules: []ociclient.EgressSecurityRule{{Protocol: "all", Destination: "0.0.0.0/0"}},
	}
	if !HasEgress(filled) {
		t.Error("有出站规则时应返回 true")
	}
}
