package netsvc

import (
	"context"
	"encoding/binary"
	"net/netip"
	"slices"
	"strings"
	"time"

	"ocicore/internal/i18n"
	"ocicore/internal/ociclient"
)

// pollInterval 是等待网络资源状态变化的轮询间隔。测试里会调小。
var pollInterval = 2 * time.Second

// IPv6Prep 描述为子网开通 IPv6 的过程。
type IPv6Prep struct {
	Steps []string
	// Warnings 是没能自动完成、需要让用户知道的事。出现这些情况时实例
	// 照样能拿到 IPv6 地址，但 IPv6 流量可能出不了公网。
	Warnings []string
}

// PrepareIPv6 保证子网能给网卡分配 IPv6 地址，并尽量让 IPv6 流量出得去。
//
// 必须完成的两件事：VCN 有 IPv6 前缀，子网有一段 /64。做不到就返回错误。
// 带着 assignIpv6Ip 去创建实例，只会换来 Oracle 一句
// "Subnet must support ipv6 when ipv6AutoAssign is true"——
// 用户看到的是创建失败，却不知道缺的是子网前缀。
//
// 尽力而为的两件事：::/0 路由与 ::/0 出站规则。只照搬用户已有的意图：
// IPv4 默认路由本来就走互联网网关，才给 IPv6 补同样的路由；本来就放行
// 全部 IPv4 出站，才给 IPv6 补同样的规则。收紧过的配置不去动，只在
// Warnings 里说明。入站规则一律不加——开放端口是用户自己的决定。
//
// 注意：VCN 一旦启用 IPv6 就无法关闭（Oracle 的限制）。
// 可以重复调用：已经就绪的部分只读不写。
func PrepareIPv6(ctx context.Context, client *ociclient.Client, region, subnetID string) (*IPv6Prep, error) {
	prep := &IPv6Prep{}

	subnet, err := client.GetSubnet(ctx, region, subnetID)
	if err != nil {
		return nil, i18n.Wrap(err, "net.read_subnet_failed")
	}

	if !subnetHasIPv6(subnet) {
		if err := addSubnetIPv6(ctx, client, region, subnet, prep); err != nil {
			return nil, err
		}
	}

	ensureIPv6Route(ctx, client, region, subnet, prep)
	ensureIPv6Egress(ctx, client, region, subnet, prep)
	return prep, nil
}

func subnetHasIPv6(s *ociclient.Subnet) bool {
	return s.Ipv6CidrBlock != "" || len(s.Ipv6CidrBlocks) > 0
}

// addSubnetIPv6 给子网分配一段 /64，必要时先给 VCN 申请前缀。
func addSubnetIPv6(ctx context.Context, client *ociclient.Client, region string,
	subnet *ociclient.Subnet, prep *IPv6Prep) error {

	vcn, err := client.GetVcn(ctx, region, subnet.VcnID)
	if err != nil {
		return i18n.Wrap(err, "net.read_vcn_failed")
	}

	// 每个 VCN 只能有一个 Oracle 分配的 /56。已经有了还去申请，Oracle 会拒绝，
	// 所以必须先看再申请。新建 VCN 时带了 isIpv6Enabled 的，这里会直接跳过。
	if len(vcn.Ipv6CidrBlocks) == 0 {
		if err := client.AddVcnIpv6Cidr(ctx, region, vcn.ID); err != nil {
			return i18n.Wrap(err, "net.vcn_v6_failed")
		}
		// 申请是异步的：接口先返回，VCN 进入 UPDATING，前缀稍后才出现。
		if err := waitFor(ctx, i18n.M("net.wait.vcn_v6"), func(ctx context.Context) (bool, error) {
			v, err := client.GetVcn(ctx, region, vcn.ID)
			if err != nil {
				return false, err
			}
			vcn = v
			return v.LifecycleState == "AVAILABLE" && len(v.Ipv6CidrBlocks) > 0, nil
		}); err != nil {
			return err
		}
		prep.Steps = append(prep.Steps, i18n.M("net.step.vcn_v6", vcn.DisplayName, vcn.Ipv6CidrBlocks[0]))
	}

	used, err := usedIPv6Prefixes(ctx, client, region, vcn, subnet)
	if err != nil {
		return err
	}
	block, err := freeIPv6Subnet(vcn.Ipv6CidrBlocks, used)
	if err != nil {
		return err
	}

	// 子网的前缀必须显式指定：Oracle 要求从 VCN 的前缀里切一段 /64。
	// 以前这里传的是空请求体，这一步一定失败。
	if err := client.AddSubnetIpv6Cidr(ctx, region, subnet.ID, block); err != nil {
		return i18n.Wrap(err, "net.subnet_v6_failed", block)
	}
	// 同样是异步的。子网还在 UPDATING 时去创建实例，照样会被拒。
	if err := waitFor(ctx, i18n.M("net.wait.subnet_v6"), func(ctx context.Context) (bool, error) {
		s, err := client.GetSubnet(ctx, region, subnet.ID)
		if err != nil {
			return false, err
		}
		return s.LifecycleState == "AVAILABLE" && subnetHasIPv6(s), nil
	}); err != nil {
		return err
	}
	prep.Steps = append(prep.Steps, i18n.M("net.step.subnet_v6", subnet.DisplayName, block))
	return nil
}

// usedIPv6Prefixes 收集同一 VCN 下其他子网已经占用的 IPv6 前缀。
//
// 子网可以和 VCN 不在同一个分区，所以两个分区都查。更远的分区查不到，
// 撞上了 Oracle 会拒绝分配，错误会原样报出来，不会静默出错。
func usedIPv6Prefixes(ctx context.Context, client *ociclient.Client, region string,
	vcn *ociclient.Vcn, subnet *ociclient.Subnet) ([]netip.Prefix, error) {

	compartments := []string{vcn.CompartmentID}
	if subnet.CompartmentID != "" && subnet.CompartmentID != vcn.CompartmentID {
		compartments = append(compartments, subnet.CompartmentID)
	}

	var used []netip.Prefix
	for _, compartment := range compartments {
		siblings, err := client.ListSubnets(ctx, region, compartment, vcn.ID)
		if err != nil {
			return nil, i18n.Wrap(err, "net.list_subnets_failed")
		}
		for _, s := range siblings {
			blocks := append([]string{s.Ipv6CidrBlock}, s.Ipv6CidrBlocks...)
			for _, b := range blocks {
				if p, err := netip.ParsePrefix(b); err == nil {
					used = append(used, p.Masked())
				}
			}
		}
	}
	return used, nil
}

// freeIPv6Subnet 从 VCN 的前缀里挑第一段没被占用的 /64。
//
// VCN 拿到的是 /56，能切出 256 段 /64。更大的前缀（比如自带的 /48）
// 只看前 256 段——一个 VCN 的子网数远到不了这个量。
func freeIPv6Subnet(vcnPrefixes []string, used []netip.Prefix) (string, error) {
	const maxCandidates = 256
	for _, raw := range vcnPrefixes {
		p, err := netip.ParsePrefix(raw)
		if err != nil || !p.Addr().Is6() || p.Bits() > 64 {
			continue
		}
		p = p.Masked()
		base := p.Addr().As16()
		hi := binary.BigEndian.Uint64(base[:8])

		count := uint64(maxCandidates)
		if free := 64 - p.Bits(); free < 8 {
			count = 1 << free
		}
		for i := uint64(0); i < count; i++ {
			var b [16]byte
			binary.BigEndian.PutUint64(b[:8], hi+i)
			candidate := netip.PrefixFrom(netip.AddrFrom16(b), 64)
			if !slices.ContainsFunc(used, candidate.Overlaps) {
				return candidate.String(), nil
			}
		}
	}
	if len(vcnPrefixes) == 0 {
		return "", i18n.Err("net.vcn_no_v6")
	}
	return "", i18n.Err("net.vcn_v6_full")
}

// ensureIPv6Route 在子网的路由表里补一条 ::/0，指向 IPv4 默认路由用的同一个互联网网关。
func ensureIPv6Route(ctx context.Context, client *ociclient.Client, region string,
	subnet *ociclient.Subnet, prep *IPv6Prep) {

	if subnet.RouteTableID == "" {
		prep.Warnings = append(prep.Warnings, i18n.M("net.warn.no_route_table"))
		return
	}
	rt, err := client.GetRouteTable(ctx, region, subnet.RouteTableID)
	if err != nil {
		prep.Warnings = append(prep.Warnings, i18n.M("net.warn.read_route_failed"))
		return
	}

	gateway := ""
	for _, r := range rt.RouteRules {
		if r.Destination == "::/0" {
			return // 已经有了，不管指向哪里都尊重现状。
		}
		if r.Destination == "0.0.0.0/0" && strings.HasPrefix(r.NetworkEntityID, "ocid1.internetgateway.") {
			gateway = r.NetworkEntityID
		}
	}
	if gateway == "" {
		prep.Warnings = append(prep.Warnings, i18n.M("net.warn.no_igw_route"))
		return
	}

	// 路由表是整体替换。LOCAL 规则由 Oracle 自己维护，回写它们的后果
	// 没有文档保证，宁可不动，让用户在控制台手动加。
	rules := make([]ociclient.RouteRule, 0, len(rt.RouteRules)+1)
	for _, r := range rt.RouteRules {
		if strings.EqualFold(r.RouteType, "LOCAL") {
			prep.Warnings = append(prep.Warnings, i18n.M("net.warn.managed_rules"))
			return
		}
		r.RouteType = "" // 与以前的写法保持一致：只回写目的地与目标。
		rules = append(rules, r)
	}
	rules = append(rules, ociclient.RouteRule{
		Destination:     "::/0",
		DestinationType: "CIDR_BLOCK",
		NetworkEntityID: gateway,
		Description:     i18n.T(i18n.FromContext(ctx), "net.desc.v6_route"),
	})
	if _, err := client.UpdateRouteTable(ctx, region, rt.ID, rules); err != nil {
		prep.Warnings = append(prep.Warnings, i18n.M("net.warn.add_route_failed"))
		return
	}
	prep.Steps = append(prep.Steps, i18n.M("net.step.v6_route"))
}

// ensureIPv6Egress 在放行全部 IPv4 出站的那个安全列表里，补一条 ::/0 全放行。
func ensureIPv6Egress(ctx context.Context, client *ociclient.Client, region string,
	subnet *ociclient.Subnet, prep *IPv6Prep) {

	if len(subnet.SecurityListIDs) == 0 {
		prep.Warnings = append(prep.Warnings, i18n.M("net.warn.no_seclist"))
		return
	}

	var target *ociclient.SecurityList
	for _, id := range subnet.SecurityListIDs {
		list, err := client.GetSecurityList(ctx, region, id)
		if err != nil {
			prep.Warnings = append(prep.Warnings, i18n.M("net.warn.read_seclist"))
			return
		}
		for _, rule := range list.EgressSecurityRules {
			if rule.Destination == "::/0" {
				return // 已经配过 IPv6 出站，不管放行了什么都尊重现状。
			}
			if target == nil && rule.Destination == "0.0.0.0/0" && strings.EqualFold(rule.Protocol, "all") {
				target = list
			}
		}
	}
	if target == nil {
		prep.Warnings = append(prep.Warnings, i18n.M("net.warn.egress_tightened"))
		return
	}

	egress := append(slices.Clone(target.EgressSecurityRules), ociclient.EgressSecurityRule{
		Protocol:        "all",
		Destination:     "::/0",
		DestinationType: "CIDR_BLOCK",
		Description:     i18n.T(i18n.FromContext(ctx), "net.desc.v6_egress"),
	})
	if _, err := client.UpdateSecurityList(ctx, region, target.ID, target.IngressSecurityRules, egress); err != nil {
		prep.Warnings = append(prep.Warnings, i18n.M("net.warn.add_egress_failed"))
		return
	}
	prep.Steps = append(prep.Steps, i18n.M("net.step.v6_egress", target.DisplayName))
}

// waitFor 轮询 check 直到它返回 true，上限 resourceWaitTimeout。
func waitFor(ctx context.Context, what string, check func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, resourceWaitTimeout)
	defer cancel()
	for {
		ok, err := check(ctx)
		if err == nil && ok {
			return nil
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return i18n.Wrap(err, "net.wait_timeout", what)
			}
			return i18n.Err("net.wait_timeout", what)
		case <-time.After(pollInterval):
		}
	}
}
