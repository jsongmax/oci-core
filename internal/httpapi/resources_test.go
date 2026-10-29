package httpapi

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"ocicore/internal/ociclient"
)

type rewriteTransport struct{ target *url.URL }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = rt.target.Scheme, rt.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

// TestCollectRegionClassifiesIdleResources 锁住资源页「待处理」依赖的几条判断。
//
//   - 挂载关系只认 ATTACHED / ATTACHING。OCI 会把分离过的历史关系一并列出来，
//     算进去的话一块早已闲置的卷会显示成「挂着」，正好漏掉最该清理的那块。
//   - 已终止的卷与 IP 不列出，也不计入存储用量。
//   - 公网 IP 按地址对照实例缓存，认得出是哪台机器的。
//   - 没有互联网网关的 VCN 要能看出来（InternetGateway 为 nil）。
func TestCollectRegionClassifiesIdleResources(t *testing.T) {
	const region = "ap-tokyo-1"
	routes := map[string]string{
		"/availabilityDomains": `[{"name":"AD-1"}]`,
		"/bootVolumes": `[
			{"id":"bv-used","displayName":"used","sizeInGBs":50,"lifecycleState":"AVAILABLE"},
			{"id":"bv-idle","displayName":"idle","sizeInGBs":47,"lifecycleState":"AVAILABLE"},
			{"id":"bv-gone","displayName":"gone","sizeInGBs":47,"lifecycleState":"TERMINATED"}]`,
		"/bootVolumeAttachments": `[
			{"bootVolumeId":"bv-used","instanceId":"i-1","lifecycleState":"ATTACHED"},
			{"bootVolumeId":"bv-idle","instanceId":"i-old","lifecycleState":"DETACHED"}]`,
		"/volumes":           `[{"id":"blk-1","displayName":"data","sizeInGBs":100,"lifecycleState":"AVAILABLE"}]`,
		"/volumeAttachments": `[]`,
		"/publicIps": `[
			{"id":"ip-eph","ipAddress":"1.1.1.1","lifetime":"EPHEMERAL","privateIpId":"p-1","lifecycleState":"ASSIGNED"},
			{"id":"ip-res","ipAddress":"2.2.2.2","lifetime":"RESERVED","lifecycleState":"AVAILABLE"}]`,
		"/instanceConsoleConnections": `[{"id":"cc-1","instanceId":"i-1","lifecycleState":"ACTIVE"}]`,
		"/vcns": `[
			{"id":"vcn-ok","displayName":"ok","cidrBlocks":["10.0.0.0/16"],"lifecycleState":"AVAILABLE"},
			{"id":"vcn-nogw","displayName":"nogw","cidrBlock":"10.1.0.0/16","lifecycleState":"AVAILABLE"}]`,
		"/subnets":          `[{"id":"sn-1","vcnId":"vcn-ok"}]`,
		"/internetGateways": `[{"id":"igw-1","vcnId":"vcn-ok","isEnabled":true,"lifecycleState":"AVAILABLE"}]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 路径形如 /20160918/bootVolumes；去掉版本段再查表。
		p := r.URL.Path[strings.Index(r.URL.Path[1:], "/")+1:]
		body, ok := routes[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(srv.URL)
	client, err := ociclient.New(&ociclient.Credentials{
		TenancyOCID: "ocid1.tenancy.oc1..t", UserOCID: "ocid1.user.oc1..u",
		Fingerprint: ociclient.FingerprintOf(&key.PublicKey), Region: region, PrivateKey: key,
	}, ociclient.WithHTTPClient(&http.Client{Transport: rewriteTransport{target}}))
	if err != nil {
		t.Fatal(err)
	}

	names := map[string]string{"i-1": "web-01"}
	ref := func(id string) ResourceRef { return ResourceRef{InstanceID: id, Name: names[id]} }
	byIP := map[string]ResourceRef{"1.1.1.1": ref("i-1")}
	var (
		mu   sync.Mutex
		errs []string
	)
	fail := func(region, part string, err error) {
		mu.Lock()
		errs = append(errs, part+": "+err.Error())
		mu.Unlock()
	}

	rr := collectRegion(t.Context(), client, "", region, ref, byIP, fail)
	if len(errs) > 0 {
		t.Fatalf("不该有查询失败：%v", errs)
	}

	vols := map[string]ResourceVolume{}
	for _, v := range rr.volumes {
		vols[v.ID] = v
	}
	if _, ok := vols["bv-gone"]; ok {
		t.Error("已终止的引导卷不该列出")
	}
	if got := vols["bv-used"].AttachedTo; len(got) != 1 || got[0].Name != "web-01" {
		t.Errorf("bv-used 应挂在 web-01 上，实际 %+v", got)
	}
	if got := vols["bv-idle"].AttachedTo; len(got) != 0 {
		t.Errorf("只有 DETACHED 历史关系的 bv-idle 应算未挂载，实际 %+v", got)
	}
	if got := vols["blk-1"]; got.Kind != "block" || len(got.AttachedTo) != 0 {
		t.Errorf("blk-1 应为未挂载的块存储，实际 %+v", got)
	}

	ips := map[string]ResourcePublicIP{}
	for _, ip := range rr.ips {
		ips[ip.ID] = ip
	}
	if got := ips["ip-eph"]; !got.Assigned || got.Instance == nil || got.Instance.Name != "web-01" {
		t.Errorf("临时 IP 应已绑定并对照到 web-01，实际 %+v", got)
	}
	if got := ips["ip-res"]; got.Assigned || got.Lifetime != "RESERVED" {
		t.Errorf("保留 IP 应为未绑定，实际 %+v", got)
	}

	if len(rr.consoles) != 1 || rr.consoles[0].Instance.Name != "web-01" {
		t.Errorf("控制台连接应对照到 web-01，实际 %+v", rr.consoles)
	}

	vcns := map[string]ResourceVcn{}
	for _, v := range rr.vcns {
		vcns[v.ID] = v
	}
	if got := vcns["vcn-ok"]; got.InternetGateway == nil || !got.InternetGateway.IsEnabled || got.Subnets != 1 {
		t.Errorf("vcn-ok 应有启用的网关和 1 个子网，实际 %+v", got)
	}
	if got := vcns["vcn-nogw"]; got.InternetGateway != nil || len(got.CidrBlocks) != 1 {
		t.Errorf("vcn-nogw 应没有网关、CIDR 退回 cidrBlock，实际 %+v", got)
	}
}
