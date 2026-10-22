package instancesvc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ocicore/internal/ociclient"
)

type redirectTransport struct{ target *url.URL }

func (rt redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = rt.target.Scheme, rt.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

// TestSplitVnicsUsesIsPrimary 锁住主网卡按 isPrimary 判定，而不是 nicIndex。
//
// VM 规格上所有 VNIC 的 nicIndex 都是 0。先前按 nicIndex 取最小值，
// 实际拿到的是 API 返回顺序里的第一张——附属网卡排在前面时，
// 列表就把附属网卡的 IP 当成主 IP 显示，换 IP 也会换错网卡。
func TestSplitVnicsUsesIsPrimary(t *testing.T) {
	vnics := map[string]ociclient.Vnic{
		"vnic-sec":  {ID: "vnic-sec", PublicIP: "2.2.2.2", Ipv6Addresses: []string{"2603::2"}},
		"vnic-pri":  {ID: "vnic-pri", PublicIP: "1.1.1.1", IsPrimary: true, Ipv6Addresses: []string{"2603::1"}},
		"vnic-gone": {ID: "vnic-gone", PublicIP: "3.3.3.3"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, ok := vnics[strings.TrimPrefix(r.URL.Path, "/20160918/vnics/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
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
	}, ociclient.WithHTTPClient(&http.Client{Transport: redirectTransport{target}}))
	if err != nil {
		t.Fatal(err)
	}

	// 附属网卡排在前面，nicIndex 全是 0；已分离的那张不该被读。
	primary, secondary := splitVnics(context.Background(), client, "ap-tokyo-1", []ociclient.VnicAttachment{
		{VnicID: "vnic-sec", LifecycleState: "ATTACHED"},
		{VnicID: "vnic-gone", LifecycleState: "DETACHED"},
		{VnicID: "vnic-pri", LifecycleState: "ATTACHED"},
	})
	if primary == nil || primary.ID != "vnic-pri" {
		t.Fatalf("主网卡应为 vnic-pri，实际 %+v", primary)
	}
	if len(secondary) != 1 || secondary[0].ID != "vnic-sec" {
		t.Fatalf("附属网卡应只有 vnic-sec，实际 %+v", secondary)
	}
}
