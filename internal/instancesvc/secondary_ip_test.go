package instancesvc

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

type rewriteTransport struct{ target *url.URL }

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = rt.target.Scheme
	cloned.URL.Host = rt.target.Host
	return http.DefaultTransport.RoundTrip(cloned)
}

func newMockClient(t *testing.T, handler http.HandlerFunc) *ociclient.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := ociclient.New(&ociclient.Credentials{
		TenancyOCID: "ocid1.tenancy.oc1..t",
		UserOCID:    "ocid1.user.oc1..u",
		Fingerprint: ociclient.FingerprintOf(&key.PublicKey),
		Region:      "ap-chuncheon-1",
		PrivateKey:  key,
	}, ociclient.WithHTTPClient(&http.Client{Transport: rewriteTransport{target}}), ociclient.WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestCollectVnicDetailAndNetworkInfoSecondaryIPs(t *testing.T) {
	var listPrivateIPCalls int
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/vnicAttachments"):
			_, _ = w.Write([]byte(`[{"id":"att-1","instanceId":"inst-1","vnicId":"vnic-1","nicIndex":0,"lifecycleState":"ATTACHED"}]`))
		case strings.HasSuffix(r.URL.Path, "/vnics/vnic-1"):
			_, _ = w.Write([]byte(`{"id":"vnic-1","displayName":"proxy1","isPrimary":true,"privateIp":"10.0.0.10","publicIp":"168.107.1.1","subnetId":"sn-1"}`))
		case strings.HasSuffix(r.URL.Path, "/privateIps/ocid1.privateip.oc1..p2"):
			_, _ = w.Write([]byte(`{"id":"ocid1.privateip.oc1..p2","vnicId":"vnic-1","ipAddress":"10.0.0.11","displayName":"sec-1","isPrimary":false}`))
		case strings.HasSuffix(r.URL.Path, "/privateIps"):
			listPrivateIPCalls++
			_, _ = w.Write([]byte(`[
				{"id":"ocid1.privateip.oc1..p1","vnicId":"vnic-1","ipAddress":"10.0.0.10","isPrimary":true},
				{"id":"ocid1.privateip.oc1..p2","vnicId":"vnic-1","ipAddress":"10.0.0.11","displayName":"sec-1","isPrimary":false},
				{"id":"ocid1.privateip.oc1..p3","vnicId":"vnic-1","ipAddress":"10.0.0.2","displayName":"sec-2","isPrimary":false}
			]`))
		case strings.HasSuffix(r.URL.Path, "/publicIps/actions/getByPrivateIpId"):
			var body struct {
				PrivateIPID string `json:"privateIpId"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch body.PrivateIPID {
			case "ocid1.privateip.oc1..p1":
				_, _ = w.Write([]byte(`{"id":"pub-1","ipAddress":"168.107.1.1","lifetime":"EPHEMERAL","privateIpId":"ocid1.privateip.oc1..p1"}`))
			case "ocid1.privateip.oc1..p2":
				_, _ = w.Write([]byte(`{"id":"pub-2","ipAddress":"152.67.2.2","lifetime":"RESERVED","privateIpId":"ocid1.privateip.oc1..p2"}`))
			default:
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":"NotAuthorizedOrNotFound","message":"not found"}`))
			}
		case strings.HasSuffix(r.URL.Path, "/publicIps"):
			_, _ = w.Write([]byte(`[
				{"id":"pub-2","ipAddress":"152.67.2.2","lifetime":"RESERVED","assignedEntityId":"ocid1.privateip.oc1..p2"}
			]`))
		case strings.HasSuffix(r.URL.Path, "/ipv6"):
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/subnets"):
			_, _ = w.Write([]byte(`[{"id":"sn-1","displayName":"sub-1","vcnId":"vcn-1"}]`))
		case strings.HasSuffix(r.URL.Path, "/vcns"):
			_, _ = w.Write([]byte(`[{"id":"vcn-1","displayName":"vcn-1"}]`))
		default:
			http.NotFound(w, r)
		}
	})

	svc := &Service{}
	acc := &store.Account{ID: "acc-1", Alias: "chu", CompartmentOCID: "comp-1"}
	inst := &store.Instance{ID: "inst-1", Region: "ap-chuncheon-1", CompartmentID: "comp-1"}

	vnics, err := svc.collectVnicDetail(t.Context(), client, acc, inst)
	if err != nil {
		t.Fatalf("collectVnicDetail 失败: %v", err)
	}
	if len(vnics) != 1 {
		t.Fatalf("len(vnics) = %d，期望 1", len(vnics))
	}
	v := vnics[0]
	if v.PublicIPType != "EPHEMERAL" || len(v.SecondaryIPs) != 2 {
		t.Fatalf("主网卡信息不符: %+v", v)
	}
	// P2: 按数值排序，10.0.0.2 应排在 10.0.0.11 之前。
	if v.SecondaryIPs[0].PrivateIP != "10.0.0.2" || v.SecondaryIPs[0].PublicIP != "" {
		t.Errorf("第一个辅助 IP 不符（应按数值排序）: %+v", v.SecondaryIPs[0])
	}
	if v.SecondaryIPs[1].PrivateIP != "10.0.0.11" || v.SecondaryIPs[1].PublicIP != "152.67.2.2" || v.SecondaryIPs[1].PublicIPType != "RESERVED" || v.SecondaryIPs[1].DisplayName != "sec-1" {
		t.Errorf("第二个辅助 IP 不符: %+v", v.SecondaryIPs[1])
	}

	listPrivateIPCalls = 0
	netMap := svc.collectNetworkInfo(t.Context(), client, acc, "ap-chuncheon-1", []ociclient.Instance{{ID: "inst-1"}})
	nd, ok := netMap["inst-1"]
	if !ok {
		t.Fatal("collectNetworkInfo 未返回 inst-1")
	}
	if nd.publicIP != "168.107.1.1" || nd.secondaryIPv4 != "152.67.2.2" {
		t.Errorf("collectNetworkInfo 结果不符: publicIP=%q secondaryIPv4=%q", nd.publicIP, nd.secondaryIPv4)
	}
	if listPrivateIPCalls != 0 {
		t.Errorf("collectNetworkInfo 不应全量调用 ListPrivateIPs，实际调用 %d 次", listPrivateIPCalls)
	}
}

func TestCollectVnicDetailMarksPublicIPUnknownOnError(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/vnicAttachments"):
			_, _ = w.Write([]byte(`[{"id":"att-1","instanceId":"inst-1","vnicId":"vnic-1","nicIndex":0,"lifecycleState":"ATTACHED"}]`))
		case strings.HasSuffix(r.URL.Path, "/vnics/vnic-1"):
			_, _ = w.Write([]byte(`{"id":"vnic-1","displayName":"proxy1","isPrimary":true,"privateIp":"10.0.0.10","publicIp":"168.107.1.1","subnetId":"sn-1"}`))
		case strings.HasSuffix(r.URL.Path, "/privateIps"):
			_, _ = w.Write([]byte(`[
				{"id":"ocid1.privateip.oc1..p1","vnicId":"vnic-1","ipAddress":"10.0.0.10","isPrimary":true},
				{"id":"ocid1.privateip.oc1..p2","vnicId":"vnic-1","ipAddress":"10.0.0.11","isPrimary":false}
			]`))
		case strings.HasSuffix(r.URL.Path, "/publicIps/actions/getByPrivateIpId"):
			var body struct {
				PrivateIPID string `json:"privateIpId"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.PrivateIPID == "ocid1.privateip.oc1..p2" {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"code":"TooManyRequests","message":"rate limited"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"pub-1","ipAddress":"168.107.1.1","lifetime":"EPHEMERAL","privateIpId":"ocid1.privateip.oc1..p1"}`))
		case strings.HasSuffix(r.URL.Path, "/ipv6"):
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/subnets"):
			_, _ = w.Write([]byte(`[{"id":"sn-1","displayName":"sub-1","vcnId":"vcn-1"}]`))
		case strings.HasSuffix(r.URL.Path, "/vcns"):
			_, _ = w.Write([]byte(`[{"id":"vcn-1","displayName":"vcn-1"}]`))
		default:
			http.NotFound(w, r)
		}
	})

	svc := &Service{}
	acc := &store.Account{ID: "acc-1", Alias: "chu", CompartmentOCID: "comp-1"}
	inst := &store.Instance{ID: "inst-1", Region: "ap-chuncheon-1", CompartmentID: "comp-1"}

	vnics, err := svc.collectVnicDetail(t.Context(), client, acc, inst)
	if err != nil {
		t.Fatalf("collectVnicDetail 失败: %v", err)
	}
	if vnics[0].PublicIPUnknown {
		t.Errorf("主槽查询成功不应标记 PublicIPUnknown")
	}
	if len(vnics[0].SecondaryIPs) != 1 || !vnics[0].SecondaryIPs[0].PublicIPUnknown {
		t.Fatalf("辅助槽 429 查询失败应标记 PublicIPUnknown=true，实际: %+v", vnics[0].SecondaryIPs)
	}
}
