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
