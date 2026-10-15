package config

import "testing"

func TestParseAllowedNets(t *testing.T) {
	nets, err := parseAllowedNets(" 10.0.0.0/8, 192.168.1.5 ,fd00::/8,")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.0/8", "192.168.1.5/32", "fd00::/8"}
	if len(nets) != len(want) {
		t.Fatalf("解析结果 %v，期望 %v", nets, want)
	}
	for i, w := range want {
		if nets[i].String() != w {
			t.Errorf("第 %d 项 = %s，期望 %s", i, nets[i], w)
		}
	}
	for _, bad := range []string{"10.0.0.0/33", "not-an-ip"} {
		if _, err := parseAllowedNets(bad); err == nil {
			t.Errorf("%q 应报错", bad)
		}
	}
}
