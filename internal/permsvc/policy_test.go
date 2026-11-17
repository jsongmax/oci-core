package permsvc

import (
	"testing"
)

func TestParseStatement(t *testing.T) {
	cases := []struct {
		raw      string
		relevant bool
		ok       bool
		check    func(t *testing.T, st statement)
	}{
		{"Allow group 'OCI Core' to manage virtual-network-family in tenancy", true, true, func(t *testing.T, st statement) {
			if len(st.Groups) != 1 || st.Groups[0].Name != "OCI Core" || st.Verb != VerbManage ||
				st.ResourceType != "virtual-network-family" || st.Location != locTenancy {
				t.Errorf("解析结果不对: %+v", st)
			}
		}},
		{"allow  GROUP Admins   TO use instance-family IN compartment dev", true, true, func(t *testing.T, st statement) {
			if st.Groups[0].Name != "Admins" || st.Verb != VerbUse || st.Location != locCompartmentName || st.LocationRef != "dev" {
				t.Errorf("大小写与多余空格应被容忍: %+v", st)
			}
		}},
		{"Allow group 'Default'/'Ops to go' to read metrics in tenancy", true, true, func(t *testing.T, st statement) {
			if st.Groups[0].Domain != "Default" || st.Groups[0].Name != "Ops to go" {
				t.Errorf("引号里的 to 不应被当成关键字: %+v", st.Groups)
			}
		}},
		{"Allow group A, group id ocid1.group.oc1..x to inspect limits in tenancy", true, true, func(t *testing.T, st statement) {
			if len(st.Groups) != 2 || st.Groups[0].Name != "A" || st.Groups[1].ID != "ocid1.group.oc1..x" {
				t.Errorf("多个组: %+v", st.Groups)
			}
		}},
		{"Allow any-user to {SECURITY_LIST_UPDATE, VCN_READ} in compartment id ocid1.compartment.oc1..c", true, true, func(t *testing.T, st statement) {
			if st.Subject != subjectAnyUser || len(st.Perms) != 2 || st.Perms[0] != "SECURITY_LIST_UPDATE" ||
				st.Location != locCompartmentID {
				t.Errorf("权限列表写法: %+v", st)
			}
		}},
		{"Allow group X to manage instances in tenancy where request.permission = 'INSTANCE_CREATE'", true, true, func(t *testing.T, st statement) {
			if !st.Conditional || st.ResourceType != "instances" {
				t.Errorf("where 条件应被识别: %+v", st)
			}
		}},
		{"Deny group X to manage security-lists in tenancy", true, true, func(t *testing.T, st statement) {
			if !st.Deny {
				t.Error("应识别 Deny")
			}
		}},
		{"Allow dynamic-group fn to read buckets in tenancy", true, true, func(t *testing.T, st statement) {
			if st.Subject != subjectOther {
				t.Error("dynamic-group 与 API 用户无关")
			}
		}},
		{"Define tenancy Acceptor as ocid1.tenancy.oc1..x", false, false, nil},
		{"Endorse group X to manage all-resources in tenancy Acceptor", false, false, nil},
		{"Allow group X to frobnicate things in tenancy", true, false, nil},
		{"Allow group X manage things", true, false, nil},
	}
	for _, c := range cases {
		st, relevant, ok := parseStatement(c.raw)
		if relevant != c.relevant || ok != c.ok {
			t.Errorf("%q: relevant=%v ok=%v，期望 %v %v", c.raw, relevant, ok, c.relevant, c.ok)
			continue
		}
		if c.check != nil {
			c.check(t, st)
		}
	}
}

func view(t *testing.T, stmts ...string) *policyView {
	t.Helper()
	pv := &policyView{
		tenancyOCID: "ocid1.tenancy.oc1..t", compartmentOCID: "ocid1.tenancy.oc1..t",
		compartmentComplete: true, groupsKnown: true,
		groups: []groupInfo{{ID: "ocid1.group.oc1..g", Name: "OCI Core"}},
	}
	for _, raw := range stmts {
		st, relevant, ok := parseStatement(raw)
		if !relevant {
			continue
		}
		if !ok {
			pv.unparsed++
			continue
		}
		pv.statements = append(pv.statements, st)
	}
	return pv
}

func TestEvaluateUseVsManage(t *testing.T) {
	// issue #10 的场景：写成 use virtual-network-family，安全规则改不了。
	pv := view(t, "Allow group 'OCI Core' to use virtual-network-family in tenancy")
	if got := pv.evaluate(pSecListUpdate).Status; got != StatusMissing {
		t.Errorf("use 不包含 SECURITY_LIST_UPDATE，应为缺失，实际 %s", got)
	}
	if got := pv.evaluate(pPrivateIPAssignPub).Status; got != StatusOK {
		t.Errorf("use 包含 PRIVATE_IP_ASSIGN_PUBLIC_IP，应为可用，实际 %s", got)
	}
	if got := pv.evaluate(pIpv6Create).Status; got != StatusMissing {
		t.Errorf("IPV6_CREATE 只在 manage 里，应为缺失，实际 %s", got)
	}
}

func TestEvaluateFamiliesAndAllResources(t *testing.T) {
	pv := view(t, "Allow group 'OCI Core' to manage instance-family in tenancy")
	if pv.evaluate(pInstanceCreate).Status != StatusOK {
		t.Error("manage instance-family 应覆盖 INSTANCE_CREATE")
	}
	if pv.evaluate(pCapacityReport).Status != StatusMissing {
		t.Error("compute-capacity-reports 不属于 instance-family")
	}

	admin := view(t, "Allow group 'OCI Core' to manage all-resources in tenancy")
	for _, n := range []Need{pCapacityReport, pSecListUpdate, pUsageReportRead, pVolumeDelete} {
		if admin.evaluate(n).Status != StatusOK {
			t.Errorf("manage all-resources 应覆盖 %s", n.Perm)
		}
	}
}

func TestEvaluateSubjects(t *testing.T) {
	if view(t, "Allow group Others to manage all-resources in tenancy").evaluate(pVcnRead).Status != StatusMissing {
		t.Error("别的组的授权不应算到这个用户头上")
	}
	if view(t, "Allow any-user to inspect vcns in tenancy").evaluate(pVcnRead).Status != StatusOK {
		t.Error("any-user 对所有用户生效")
	}
	if view(t, "Allow group id ocid1.group.oc1..g to inspect vcns in tenancy").evaluate(pVcnRead).Status != StatusOK {
		t.Error("按组 OCID 引用应能匹配")
	}
	if view(t, "Allow group 'OtherDomain'/'OCI Core' to inspect vcns in tenancy").evaluate(pVcnRead).Status != StatusMissing {
		t.Error("其他身份域的同名组不是这个组")
	}
	if view(t, "Allow group 'Default'/'oci core' to inspect vcns in tenancy").evaluate(pVcnRead).Status != StatusOK {
		t.Error("默认域前缀与大小写差异应能匹配")
	}

	pv := view(t, "Allow group Others to manage all-resources in tenancy")
	pv.groupsKnown = false
	if pv.evaluate(pVcnRead).Status != StatusUnknown {
		t.Error("读不到组时不能断定缺失")
	}
}

func TestEvaluateDowngrades(t *testing.T) {
	if view(t, "Allow group 'OCI Core' to manage instances in tenancy where request.region = 'phx'").
		evaluate(pInstanceCreate).Status != StatusUnknown {
		t.Error("带 where 的授权应为无法判断")
	}
	if view(t,
		"Allow group 'OCI Core' to manage virtual-network-family in tenancy",
		"Deny group 'OCI Core' to manage security-lists in tenancy",
	).evaluate(pSecListUpdate).Status != StatusUnknown {
		t.Error("有 Deny 时应为无法判断")
	}
	if view(t, "Allow group 'OCI Core' to frobnicate things in tenancy").evaluate(pVcnRead).Status != StatusUnknown {
		t.Error("有解析失败的语句时，未授权只能算无法判断")
	}
}

func TestEvaluateCompartments(t *testing.T) {
	// 根区间账号：区间级语句覆盖不到根。
	pv := view(t, "Allow group 'OCI Core' to manage virtual-network-family in compartment dev")
	if pv.evaluate(pVcnRead).Status != StatusMissing {
		t.Error("compartment dev 的授权不覆盖根区间")
	}

	// 子区间账号。
	sub := view(t, "Allow group 'OCI Core' to manage virtual-network-family in compartment dev")
	sub.compartmentOCID, sub.compartmentName = "ocid1.compartment.oc1..dev", "dev"
	if sub.evaluate(pSecListUpdate).Status != StatusOK {
		t.Error("按名称写的子区间授权应覆盖该区间")
	}
	if sub.evaluate(pCapacityReport).Status != StatusMissing {
		t.Error("容量报告只能在租户级授予")
	}

	other := view(t, "Allow group 'OCI Core' to manage virtual-network-family in compartment parent")
	other.compartmentOCID, other.compartmentName = "ocid1.compartment.oc1..dev", "dev"
	if other.evaluate(pSecListUpdate).Status != StatusUnknown {
		t.Error("其他区间（可能是祖先）的授权应为无法判断")
	}
}

func TestFixStatements(t *testing.T) {
	got := fixStatements(map[string]Verb{"virtual-network-family": VerbManage, "compute-capacity-reports": VerbManage}, []string{"OCI Core"})
	want := []string{
		"Allow group 'OCI Core' to manage compute-capacity-reports in tenancy",
		"Allow group 'OCI Core' to manage virtual-network-family in tenancy",
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("修复建议 %q，期望 %q", got, want)
	}
	if g := fixStatements(map[string]Verb{"vcns": VerbInspect}, nil); g[0] != "Allow group <group-name> to inspect vcns in tenancy" {
		t.Errorf("不知道组名时应留占位符: %q", g)
	}
}

func TestEvaluateCapabilityProbeWins(t *testing.T) {
	pv := view(t) // 没有任何授权
	probes := map[string]probeResult{probeCapacity: {status: StatusOK}}
	var capb Capability
	for _, c := range capabilities {
		if c.Key == "capacity.report" {
			capb = c
		}
	}
	if item := evaluateCapability(capb, pv, true, probes, nil); item.Status != StatusOK {
		t.Errorf("探测成功应覆盖策略结论，实际 %s", item.Status)
	}
	probes[probeCapacity] = probeResult{status: StatusMissing}
	item := evaluateCapability(capb, view(t, "Allow group 'OCI Core' to manage all-resources in tenancy"), true, probes, nil)
	if item.Status != StatusMissing || len(item.Fix) != 1 {
		t.Errorf("探测被拒应判为缺失并给出修复建议: %+v", item)
	}
}
