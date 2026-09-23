package netsvc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ocicore/internal/ociclient"
)

// 这里的测试跑在一个有状态的假 Oracle 上。它刻意照搬了几条真实的限制，
// 正是这几条让「开了 IPv6 就创建失败」：
//
//   - 给子网加 IPv6 前缀必须指定一段 /64，且要在 VCN 的前缀之内、不能和兄弟子网重叠；
//   - 每个 VCN 只能有一个 Oracle 分配的前缀，已有还申请会被拒；
//   - 两种申请都是异步的，刚返回时资源还在 UPDATING，前缀还看不到。

const (
	testRegion      = "ap-osaka-1"
	testCompartment = "ocid1.compartment.oc1..c1"
	testIGW         = "ocid1.internetgateway.oc1..igw1"
	testVcnPrefix   = "2603:c020:4015:8e00::/56"
)

func TestMain(m *testing.M) {
	pollInterval = time.Millisecond
	os.Exit(m.Run())
}

var (
	keyOnce sync.Once
	testKey *rsa.PrivateKey
)

type fakeOCI struct {
	mu            sync.Mutex
	vcns          map[string]*ociclient.Vcn
	subnets       map[string]*ociclient.Subnet
	routeTables   map[string]*ociclient.RouteTable
	lists         map[string]*ociclient.SecurityList
	pendingVcn    map[string]int // 还要被读几次才生效
	pendingSubnet map[string]int
	rejectSubnet  bool
	writes        []string
	routePuts     []json.RawMessage
	seq           int
}

func newFake() *fakeOCI {
	return &fakeOCI{
		vcns:          map[string]*ociclient.Vcn{},
		subnets:       map[string]*ociclient.Subnet{},
		routeTables:   map[string]*ociclient.RouteTable{},
		lists:         map[string]*ociclient.SecurityList{},
		pendingVcn:    map[string]int{},
		pendingSubnet: map[string]int{},
	}
}

func (f *fakeOCI) client(t *testing.T) *ociclient.Client {
	t.Helper()
	keyOnce.Do(func() {
		var err error
		if testKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	creds := &ociclient.Credentials{
		TenancyOCID: "ocid1.tenancy.oc1..t1",
		UserOCID:    "ocid1.user.oc1..u1",
		Fingerprint: ociclient.FingerprintOf(&testKey.PublicKey),
		Region:      testRegion,
		PrivateKey:  testKey,
	}
	c, err := ociclient.New(creds, ociclient.WithHTTPClient(&http.Client{Transport: f}))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// RoundTrip 让客户端的所有请求都落到这个假服务上，不走网络。
func (f *fakeOCI) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	f.serve(rec, r)
	return rec.Result(), nil
}

func (f *fakeOCI) id(kind string) string {
	f.seq++
	return fmt.Sprintf("ocid1.%s.oc1..%d", kind, f.seq)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func reject(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"code": "InvalidParameter", "message": msg})
}

func (f *fakeOCI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	path := strings.TrimPrefix(r.URL.Path, "/20160918")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if r.Method != http.MethodGet {
		f.writes = append(f.writes, r.Method+" "+path)
	}
	var body map[string]json.RawMessage
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	switch {
	case r.Method == http.MethodPost && path == "/vcns":
		var req ociclient.CreateVcnRequest
		raw, _ := json.Marshal(body)
		_ = json.Unmarshal(raw, &req)
		f.addVcn(req.DisplayName, req.IsIpv6Enabled)
		for _, v := range f.vcns {
			if v.DisplayName == req.DisplayName {
				writeJSON(w, 200, v)
			}
		}

	case len(parts) == 2 && parts[0] == "vcns" && r.Method == http.MethodGet:
		v := f.vcns[parts[1]]
		out := *v
		if f.pendingVcn[v.ID] > 0 {
			f.pendingVcn[v.ID]--
			out.LifecycleState, out.Ipv6CidrBlocks = "UPDATING", nil
		}
		writeJSON(w, 200, out)

	case len(parts) == 4 && parts[0] == "vcns" && parts[3] == "addIpv6Cidr":
		v := f.vcns[parts[1]]
		if len(v.Ipv6CidrBlocks) > 0 {
			reject(w, "VCN already has an Oracle allocated IPv6 prefix")
			return
		}
		v.Ipv6CidrBlocks = []string{testVcnPrefix}
		f.pendingVcn[v.ID] = 1
		w.WriteHeader(http.StatusAccepted)

	case parts[0] == "subnets" && len(parts) == 1 && r.Method == http.MethodGet:
		out := []ociclient.Subnet{}
		for _, s := range f.sortedSubnets() {
			if s.CompartmentID == r.URL.Query().Get("compartmentId") &&
				(r.URL.Query().Get("vcnId") == "" || s.VcnID == r.URL.Query().Get("vcnId")) {
				out = append(out, *s)
			}
		}
		writeJSON(w, 200, out)

	case parts[0] == "subnets" && len(parts) == 1 && r.Method == http.MethodPost:
		var req ociclient.CreateSubnetRequest
		raw, _ := json.Marshal(body)
		_ = json.Unmarshal(raw, &req)
		s := f.addSubnet(req.VcnID, req.DisplayName, "")
		writeJSON(w, 200, s)

	case len(parts) == 2 && parts[0] == "subnets" && r.Method == http.MethodGet:
		s := f.subnets[parts[1]]
		out := *s
		if f.pendingSubnet[s.ID] > 0 {
			f.pendingSubnet[s.ID]--
			out.LifecycleState, out.Ipv6CidrBlock = "UPDATING", ""
		}
		writeJSON(w, 200, out)

	case len(parts) == 4 && parts[0] == "subnets" && parts[3] == "addIpv6Cidr":
		f.addSubnetPrefix(w, parts[1], body)

	case parts[0] == "internetGateways" && r.Method == http.MethodPost:
		writeJSON(w, 200, map[string]any{"id": testIGW, "isEnabled": true, "lifecycleState": "AVAILABLE"})

	case len(parts) == 2 && parts[0] == "routeTables":
		rt := f.routeTables[parts[1]]
		if r.Method == http.MethodPut {
			f.routePuts = append(f.routePuts, body["routeRules"])
			var rules []ociclient.RouteRule
			_ = json.Unmarshal(body["routeRules"], &rules)
			rt.RouteRules = rules
		}
		writeJSON(w, 200, rt)

	case len(parts) == 2 && parts[0] == "securityLists":
		l := f.lists[parts[1]]
		if r.Method == http.MethodPut {
			_ = json.Unmarshal(body["ingressSecurityRules"], &l.IngressSecurityRules)
			_ = json.Unmarshal(body["egressSecurityRules"], &l.EgressSecurityRules)
		}
		writeJSON(w, 200, l)

	case parts[0] == "ipv6" && r.Method == http.MethodGet:
		writeJSON(w, 200, []any{})

	case parts[0] == "ipv6" && r.Method == http.MethodPost:
		writeJSON(w, 200, map[string]string{"id": "ocid1.ipv6.oc1..1", "ipAddress": "2603:c020:4015:8e00::100"})

	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"code": "NotAuthorizedOrNotFound", "message": r.Method + " " + path})
	}
}

// addSubnetPrefix 照 Oracle 的规矩校验子网前缀。
func (f *fakeOCI) addSubnetPrefix(w http.ResponseWriter, id string, body map[string]json.RawMessage) {
	if f.rejectSubnet {
		reject(w, "IPv6 is not supported for this subnet")
		return
	}
	s := f.subnets[id]
	var block string
	_ = json.Unmarshal(body["ipv6CidrBlock"], &block)
	p, err := netip.ParsePrefix(block)
	if err != nil || p.Bits() != 64 {
		reject(w, "ipv6CidrBlock must be a /64 prefix")
		return
	}
	vcn := f.vcns[s.VcnID]
	if len(vcn.Ipv6CidrBlocks) == 0 || !netip.MustParsePrefix(vcn.Ipv6CidrBlocks[0]).Contains(p.Addr()) {
		reject(w, "ipv6CidrBlock must be within the VCN's IPv6 prefix")
		return
	}
	for _, other := range f.subnets {
		if other.VcnID == s.VcnID && other.Ipv6CidrBlock != "" &&
			netip.MustParsePrefix(other.Ipv6CidrBlock).Overlaps(p) {
			reject(w, "ipv6CidrBlock overlaps with another subnet")
			return
		}
	}
	s.Ipv6CidrBlock = p.String()
	f.pendingSubnet[id] = 1
	w.WriteHeader(http.StatusAccepted)
}

// addVcn 建一个 VCN，带上 Oracle 默认会有的路由表和安全列表。
func (f *fakeOCI) addVcn(name string, ipv6 bool) *ociclient.Vcn {
	v := &ociclient.Vcn{ID: f.id("vcn"), CompartmentID: testCompartment, DisplayName: name, LifecycleState: "AVAILABLE"}
	if ipv6 {
		v.Ipv6CidrBlocks = []string{testVcnPrefix}
	}
	rt := &ociclient.RouteTable{ID: f.id("routetable"), VcnID: v.ID}
	sl := &ociclient.SecurityList{ID: f.id("securitylist"), VcnID: v.ID, DisplayName: "Default Security List",
		IngressSecurityRules: []ociclient.IngressSecurityRule{{Protocol: "6", Source: "0.0.0.0/0",
			TCPOptions: &ociclient.TCPOptions{DestinationPortRange: &ociclient.PortRange{Min: 22, Max: 22}}}},
		EgressSecurityRules: []ociclient.EgressSecurityRule{{Protocol: "all", Destination: "0.0.0.0/0"}}}
	v.DefaultRouteTableID, v.DefaultSecurityListID = rt.ID, sl.ID
	f.vcns[v.ID], f.routeTables[rt.ID], f.lists[sl.ID] = v, rt, sl
	return v
}

func (f *fakeOCI) addSubnet(vcnID, name, ipv6 string) *ociclient.Subnet {
	v := f.vcns[vcnID]
	s := &ociclient.Subnet{ID: f.id("subnet"), CompartmentID: testCompartment, VcnID: vcnID, DisplayName: name,
		Ipv6CidrBlock: ipv6, RouteTableID: v.DefaultRouteTableID,
		SecurityListIDs: []string{v.DefaultSecurityListID}, LifecycleState: "AVAILABLE"}
	f.subnets[s.ID] = s
	return s
}

func (f *fakeOCI) sortedSubnets() []*ociclient.Subnet {
	out := make([]*ociclient.Subnet, 0, len(f.subnets))
	for _, s := range f.subnets {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b *ociclient.Subnet) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// publicNetwork 搭一个典型的已有公网网络：IPv4 默认路由走互联网网关，放行全部出站。
func (f *fakeOCI) publicNetwork(ipv6 bool) (*ociclient.Vcn, *ociclient.RouteTable, *ociclient.SecurityList) {
	v := f.addVcn("vcn-existing", ipv6)
	rt := f.routeTables[v.DefaultRouteTableID]
	rt.RouteRules = []ociclient.RouteRule{{Destination: "0.0.0.0/0", DestinationType: "CIDR_BLOCK",
		NetworkEntityID: testIGW, Description: "用户自己加的", RouteType: "STATIC"}}
	return v, rt, f.lists[v.DefaultSecurityListID]
}

func (f *fakeOCI) wrote(op string) bool {
	return slices.ContainsFunc(f.writes, func(w string) bool { return strings.Contains(w, op) })
}

// ---- 纯计算 ----

func TestFreeIPv6Subnet(t *testing.T) {
	used := func(ps ...string) []netip.Prefix {
		var out []netip.Prefix
		for _, p := range ps {
			out = append(out, netip.MustParsePrefix(p))
		}
		return out
	}
	cases := []struct {
		name   string
		vcn    []string
		used   []netip.Prefix
		want   string
		errHas string
	}{
		{"空 VCN 取第一段", []string{testVcnPrefix}, nil, "2603:c020:4015:8e00::/64", ""},
		{"跳过已占用", []string{testVcnPrefix},
			used("2603:c020:4015:8e00::/64", "2603:c020:4015:8e01::/64"), "2603:c020:4015:8e02::/64", ""},
		{"中间有空位", []string{testVcnPrefix},
			used("2603:c020:4015:8e00::/64", "2603:c020:4015:8e02::/64"), "2603:c020:4015:8e01::/64", ""},
		{"前缀没对齐也能算", []string{"2603:c020:4015:8e12::/56"}, nil, "2603:c020:4015:8e00::/64", ""},
		{"跳过非 IPv6 前缀", []string{"10.0.0.0/16", testVcnPrefix}, nil, "2603:c020:4015:8e00::/64", ""},
		{"占满报错", []string{"2603:c020:4015:8e00::/63"},
			used("2603:c020:4015:8e00::/64", "2603:c020:4015:8e01::/64"), "", "占满"},
		{"没有前缀报错", nil, nil, "", "没有 IPv6 前缀"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := freeIPv6Subnet(tc.vcn, tc.used)
			if tc.errHas != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("期望错误含 %q，得到 %v（%q）", tc.errHas, err, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("得到 %q, %v；期望 %q", got, err, tc.want)
			}
		})
	}
}

// ---- 编排 ----

// 用户报告的场景：复用一个从没开过 IPv6 的已有公网子网。
func TestPrepareIPv6OnExistingNetwork(t *testing.T) {
	f := newFake()
	v, rt, sl := f.publicNetwork(false)
	s := f.addSubnet(v.ID, "subnet-existing", "")

	prep, err := PrepareIPv6(context.Background(), f.client(t), testRegion, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(prep.Warnings) > 0 {
		t.Errorf("不该有警告：%v", prep.Warnings)
	}

	if s.Ipv6CidrBlock != "2603:c020:4015:8e00::/64" {
		t.Errorf("子网前缀 = %q", s.Ipv6CidrBlock)
	}
	if len(rt.RouteRules) != 2 || rt.RouteRules[0].Description != "用户自己加的" ||
		rt.RouteRules[1].Destination != "::/0" || rt.RouteRules[1].NetworkEntityID != testIGW {
		t.Errorf("路由表不对：%+v", rt.RouteRules)
	}
	// 回写时不带 routeType，与以前的写法一致。
	if strings.Contains(string(f.routePuts[0]), "routeType") {
		t.Errorf("回写的路由规则不该带 routeType：%s", f.routePuts[0])
	}
	if len(sl.IngressSecurityRules) != 1 {
		t.Errorf("入站规则不该被改动：%+v", sl.IngressSecurityRules)
	}
	last := sl.EgressSecurityRules[len(sl.EgressSecurityRules)-1]
	if len(sl.EgressSecurityRules) != 2 || last.Destination != "::/0" || last.Protocol != "all" {
		t.Errorf("出站规则不对：%+v", sl.EgressSecurityRules)
	}
	if len(prep.Steps) != 4 {
		t.Errorf("步骤应有 4 条（VCN 前缀、子网前缀、路由、出站），实际：%v", prep.Steps)
	}
}

func TestPrepareIPv6PicksFreeSlotAndKeepsVcnPrefix(t *testing.T) {
	f := newFake()
	v, _, _ := f.publicNetwork(true)
	f.addSubnet(v.ID, "sibling", "2603:c020:4015:8e00::/64")
	s := f.addSubnet(v.ID, "target", "")

	if _, err := PrepareIPv6(context.Background(), f.client(t), testRegion, s.ID); err != nil {
		t.Fatal(err)
	}
	if s.Ipv6CidrBlock != "2603:c020:4015:8e01::/64" {
		t.Errorf("应避开兄弟子网占用的 /64，实际 %q", s.Ipv6CidrBlock)
	}
	if f.wrote("/vcns/") {
		t.Errorf("VCN 已有前缀时不该再申请：%v", f.writes)
	}
}

func TestPrepareIPv6IsNoopWhenReady(t *testing.T) {
	f := newFake()
	v, rt, sl := f.publicNetwork(true)
	rt.RouteRules = append(rt.RouteRules, ociclient.RouteRule{Destination: "::/0", NetworkEntityID: testIGW})
	sl.EgressSecurityRules = append(sl.EgressSecurityRules, ociclient.EgressSecurityRule{Protocol: "all", Destination: "::/0"})
	s := f.addSubnet(v.ID, "ready", "2603:c020:4015:8e00::/64")

	prep, err := PrepareIPv6(context.Background(), f.client(t), testRegion, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.writes) > 0 || len(prep.Steps) > 0 || len(prep.Warnings) > 0 {
		t.Errorf("已就绪时应只读不写：writes=%v steps=%v warnings=%v", f.writes, prep.Steps, prep.Warnings)
	}
}

// 用户收紧过的配置：默认路由走 NAT、出站只放 443。只开通前缀，不替用户放开。
func TestPrepareIPv6RespectsTightenedNetwork(t *testing.T) {
	f := newFake()
	v, rt, sl := f.publicNetwork(false)
	rt.RouteRules = []ociclient.RouteRule{{Destination: "0.0.0.0/0", NetworkEntityID: "ocid1.natgateway.oc1..nat"}}
	sl.EgressSecurityRules = []ociclient.EgressSecurityRule{{Protocol: "6", Destination: "0.0.0.0/0",
		TCPOptions: &ociclient.TCPOptions{DestinationPortRange: &ociclient.PortRange{Min: 443, Max: 443}}}}
	s := f.addSubnet(v.ID, "private-ish", "")

	prep, err := PrepareIPv6(context.Background(), f.client(t), testRegion, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Ipv6CidrBlock == "" {
		t.Error("子网前缀照样要开通")
	}
	if f.wrote("/routeTables/") || f.wrote("/securityLists/") {
		t.Errorf("收紧过的路由和安全列表不该被改：%v", f.writes)
	}
	if len(prep.Warnings) != 2 {
		t.Errorf("应对路由和出站各给一条警告：%v", prep.Warnings)
	}
}

func TestPrepareIPv6LeavesLocalRouteRulesAlone(t *testing.T) {
	f := newFake()
	v, rt, _ := f.publicNetwork(true)
	rt.RouteRules = append(rt.RouteRules, ociclient.RouteRule{Destination: "10.1.0.0/16",
		NetworkEntityID: "ocid1.privateip.oc1..x", RouteType: "LOCAL"})
	s := f.addSubnet(v.ID, "s", "")

	prep, err := PrepareIPv6(context.Background(), f.client(t), testRegion, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.wrote("/routeTables/") {
		t.Errorf("有 LOCAL 规则时不该回写路由表：%v", f.writes)
	}
	if !slices.ContainsFunc(prep.Warnings, func(w string) bool { return strings.Contains(w, "手动添加") }) {
		t.Errorf("应提示用户手动添加：%v", prep.Warnings)
	}
}

// 子网开通不了就必须报错，不能让调用方带着 assignIpv6Ip 去创建实例。
func TestPrepareIPv6FailsWhenSubnetRejects(t *testing.T) {
	f := newFake()
	v, _, _ := f.publicNetwork(true)
	s := f.addSubnet(v.ID, "s", "")
	f.rejectSubnet = true

	_, err := PrepareIPv6(context.Background(), f.client(t), testRegion, s.ID)
	if err == nil || !strings.Contains(err.Error(), "为子网分配 IPv6 前缀") {
		t.Fatalf("期望子网前缀错误，得到 %v", err)
	}
	if f.wrote("/routeTables/") || f.wrote("/securityLists/") {
		t.Errorf("前缀失败后不该继续改路由和安全列表：%v", f.writes)
	}
}

// 新建网络的完整流程。以前这里会给已带前缀的 VCN 再申请一次，失败被吞掉，
// 子网前缀又因为请求体为空而失败——最后创建实例时才报错。
func TestEnsureNetworkThenPrepareOnNewNetwork(t *testing.T) {
	f := newFake()
	c := f.client(t)

	net, err := EnsureNetwork(context.Background(), c, EnsureNetworkOptions{
		Region: testRegion, CompartmentID: testCompartment, EnableIPv6: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !net.Created {
		t.Fatal("没有可复用的子网时应新建")
	}
	prep, err := PrepareIPv6(context.Background(), c, testRegion, net.SubnetID)
	if err != nil {
		t.Fatal(err)
	}
	if f.wrote("addIpv6Cidr") && slices.ContainsFunc(f.writes, func(w string) bool {
		return strings.HasPrefix(w, "POST /vcns/") && strings.HasSuffix(w, "addIpv6Cidr")
	}) {
		t.Errorf("新建时已带 isIpv6Enabled，不该再给 VCN 申请前缀：%v", f.writes)
	}
	s := f.subnets[net.SubnetID]
	if s.Ipv6CidrBlock != "2603:c020:4015:8e00::/64" {
		t.Errorf("子网前缀 = %q", s.Ipv6CidrBlock)
	}
	rt := f.routeTables[s.RouteTableID]
	if !slices.ContainsFunc(rt.RouteRules, func(r ociclient.RouteRule) bool {
		return r.Destination == "::/0" && r.NetworkEntityID == testIGW
	}) {
		t.Errorf("应补上 ::/0 路由：%+v", rt.RouteRules)
	}
	if len(prep.Warnings) > 0 {
		t.Errorf("新建网络不该有警告：%v", prep.Warnings)
	}
}

func TestEnsureNetworkPrefersIPv6SubnetWhenAsked(t *testing.T) {
	f := newFake()
	v, _, _ := f.publicNetwork(true)
	first := f.addSubnet(v.ID, "v4-only", "")
	second := f.addSubnet(v.ID, "dual-stack", "2603:c020:4015:8e00::/64")
	c := f.client(t)

	got, err := EnsureNetwork(context.Background(), c, EnsureNetworkOptions{
		Region: testRegion, CompartmentID: testCompartment, EnableIPv6: true})
	if err != nil || got.SubnetID != second.ID {
		t.Errorf("要 IPv6 时应优先已开通的子网：got %v, %v", got, err)
	}
	got, err = EnsureNetwork(context.Background(), c, EnsureNetworkOptions{
		Region: testRegion, CompartmentID: testCompartment})
	if err != nil || got.SubnetID != first.ID {
		t.Errorf("不要 IPv6 时行为不变，仍取第一个：got %v, %v", got, err)
	}
	if len(f.writes) > 0 {
		t.Errorf("复用不该有任何写操作：%v", f.writes)
	}
}

// 实例抽屉里的「启用 IPv6」走同一条开通逻辑。
func TestEnableIPv6OnExistingInstance(t *testing.T) {
	f := newFake()
	v, _, _ := f.publicNetwork(false)
	s := f.addSubnet(v.ID, "s", "")

	res, err := EnableIPv6(context.Background(), f.client(t), testRegion, "ocid1.vnic.oc1..v", s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Ipv6CidrBlock == "" || res.Address == "" {
		t.Errorf("应先开通子网再分配地址：prefix=%q addr=%q", s.Ipv6CidrBlock, res.Address)
	}
}
