package httpapi

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

// 列表与保存接口共用这份标注。保存接口以前回的是原始对象，前端拿它覆盖
// 本地数据后警示全部消失——刚加上的危险规则反而不显示红色。
func TestAnnotateSecurityList(t *testing.T) {
	list := ociclient.SecurityList{
		ID: "ocid1.securitylist.oc1..x",
		IngressSecurityRules: []ociclient.IngressSecurityRule{
			{Protocol: "6", Source: "0.0.0.0/0",
				TCPOptions: &ociclient.TCPOptions{DestinationPortRange: &ociclient.PortRange{Min: 22, Max: 22}}},
			{Protocol: "6", Source: "0.0.0.0/0",
				TCPOptions: &ociclient.TCPOptions{DestinationPortRange: &ociclient.PortRange{Min: 1, Max: 65535}}},
			{Protocol: "all", Source: "10.0.0.0/8"},
			{Protocol: "17", Source: "::/0"},
		},
	}

	a := annotateSecurityList(list)
	if len(a.AllowAllRules) != 2 || a.AllowAllRules[0] != 1 || a.AllowAllRules[1] != 3 {
		t.Errorf("AllowAllRules = %v，期望 [1 3]", a.AllowAllRules)
	}
	if !a.NoEgress {
		t.Error("没有出站规则时 NoEgress 应为 true")
	}

	// 前端按这几个字段名读，嵌入的 SecurityList 字段也必须平铺在同一层。
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "ingressSecurityRules", "allowAllRules", "noEgress"} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON 缺少字段 %q：%s", key, raw)
		}
	}

	list.EgressSecurityRules = []ociclient.EgressSecurityRule{{Protocol: "all", Destination: "0.0.0.0/0"}}
	if annotateSecurityList(list).NoEgress {
		t.Error("有出站规则时 NoEgress 应为 false")
	}
}

// 网卡 OCID 来自客户端：只认挂在这台实例上、且还是 ATTACHED 的网卡，
// 否则凭一个 OCID 就能去改同账号里别的机器的公网 IP 或 IPv6。
func TestFindInstanceVnicChecksOwnership(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/vnicAttachments"):
			if r.URL.Query().Get("instanceId") != "i-1" {
				t.Errorf("应按实例过滤网卡，查询参数 instanceId = %q", r.URL.Query().Get("instanceId"))
			}
			_, _ = w.Write([]byte(`[
				{"vnicId":"vnic-mine","instanceId":"i-1","lifecycleState":"ATTACHED"},
				{"vnicId":"vnic-gone","instanceId":"i-1","lifecycleState":"DETACHED"}]`))
		case strings.HasSuffix(r.URL.Path, "/vnics/vnic-mine"):
			_, _ = w.Write([]byte(`{"id":"vnic-mine","subnetId":"sn-1","isPrimary":false,"displayName":"eth1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(srv.URL)
	client, err := ociclient.New(&ociclient.Credentials{
		TenancyOCID: "ocid1.tenancy.oc1..t", UserOCID: "ocid1.user.oc1..u",
		Fingerprint: ociclient.FingerprintOf(&key.PublicKey), Region: "ap-tokyo-1", PrivateKey: key,
	}, ociclient.WithHTTPClient(&http.Client{Transport: rewriteTransport{target}}))
	if err != nil {
		t.Fatal(err)
	}
	inst := &store.Instance{ID: "i-1", Region: "ap-tokyo-1"}

	vnic, err := findInstanceVnic(t.Context(), client, inst, "c", "vnic-mine")
	if err != nil || vnic == nil || vnic.SubnetID != "sn-1" {
		t.Fatalf("自己的网卡应能取到，得到 %+v, %v", vnic, err)
	}
	if got := vnicLabel(vnic); got != "附属网卡 eth1" {
		t.Errorf("vnicLabel = %q，期望 附属网卡 eth1", got)
	}
	for _, id := range []string{"vnic-gone", "vnic-of-another-instance"} {
		if _, err := findInstanceVnic(t.Context(), client, inst, "c", id); err == nil {
			t.Errorf("%s 不该被当成这台实例的网卡", id)
		}
	}
}

// 开通 IPv6 失败时给用户看的文字：保留是哪一步出的错，OCI 错误压成「代码 · 说明」。
func TestIPv6ErrText(t *testing.T) {
	apiErr := &ociclient.APIError{StatusCode: 400, Code: "InvalidParameter",
		Message: "ipv6CidrBlock overlaps", Class: ociclient.ClassBadRequest}
	err := fmt.Errorf("为子网分配 IPv6 前缀 2603:c020::/64 失败: %w", apiErr)

	got := ipv6ErrText(err)
	want := "为子网分配 IPv6 前缀 2603:c020::/64 失败: InvalidParameter · ipv6CidrBlock overlaps"
	if got != want {
		t.Errorf("得到 %q\n期望 %q", got, want)
	}
	if plain := ipv6ErrText(errors.New("等待子网的 IPv6 前缀生效超时")); plain != "等待子网的 IPv6 前缀生效超时" {
		t.Errorf("非 OCI 错误应原样返回，得到 %q", plain)
	}
}
