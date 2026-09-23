package netsvc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

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
		return nil, fmt.Errorf("读取子网失败: %w", err)
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
		return fmt.Errorf("读取 VCN 失败: %w", err)
	}

	// 每个 VCN 只能有一个 Oracle 分配的 /56。已经有了还去申请，Oracle 会拒绝，
	// 所以必须先看再申请。新建 VCN 时带了 isIpv6Enabled 的，这里会直接跳过。
	if len(vcn.Ipv6CidrBlocks) == 0 {
		if err := client.AddVcnIpv6Cidr(ctx, region, vcn.ID); err != nil {
			return fmt.Errorf("为 VCN 申请 IPv6 前缀失败: %w", err)
		}
		// 申请是异步的：接口先返回，VCN 进入 UPDATING，前缀稍后才出现。
		if err := waitFor(ctx, "VCN 的 IPv6 前缀生效", func(ctx context.Context) (bool, error) {
			v, err := client.GetVcn(ctx, region, vcn.ID)
			if err != nil {
				return false, err
			}
			vcn = v
			return v.LifecycleState == "AVAILABLE" && len(v.Ipv6CidrBlocks) > 0, nil
		}); err != nil {
			return err
		}
		prep.Steps = append(prep.Steps, "已为 VCN "+vcn.DisplayName+" 申请 IPv6 前缀 "+vcn.Ipv6CidrBlocks[0])
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
		return fmt.Errorf("为子网分配 IPv6 前缀 %s 失败: %w", block, err)
	}
	// 同样是异步的。子网还在 UPDATING 时去创建实例，照样会被拒。
	if err := waitFor(ctx, "子网的 IPv6 前缀生效", func(ctx context.Context) (bool, error) {
		s, err := client.GetSubnet(ctx, region, subnet.ID)
		if err != nil {
			return false, err
		}
		return s.LifecycleState == "AVAILABLE" && subnetHasIPv6(s), nil
	}); err != nil {
		return err
	}
	prep.Steps = append(prep.Steps, "已为子网 "+subnet.DisplayName+" 分配 IPv6 前缀 "+block)
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
			return nil, fmt.Errorf("读取子网列表失败: %w", err)
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
		return "", errors.New("VCN 没有 IPv6 前缀")
	}
	return "", errors.New("VCN 的 IPv6 前缀已被其他子网占满，无法再分出 /64")
}

// ensureIPv6Route 在子网的路由表里补一条 ::/0，指向 IPv4 默认路由用的同一个互联网网关。
func ensureIPv6Route(ctx context.Context, client *ociclient.Client, region string,
	subnet *ociclient.Subnet, prep *IPv6Prep) {

	const noRoute = "实例的 IPv6 暂时无法访问外网。"
	if subnet.RouteTableID == "" {
		prep.Warnings = append(prep.Warnings, "子网没有关联路由表，未添加 IPv6 路由，"+noRoute)
		return
	}
	rt, err := client.GetRouteTable(ctx, region, subnet.RouteTableID)
	if err != nil {
		prep.Warnings = append(prep.Warnings, "读取路由表失败，未添加 IPv6 路由，"+noRoute)
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
		prep.Warnings = append(prep.Warnings,
			"子网的 IPv4 默认路由不经过互联网网关，未自动添加 IPv6 路由，"+noRoute)
		return
	}

	// 路由表是整体替换。LOCAL 规则由 Oracle 自己维护，回写它们的后果
	// 没有文档保证，宁可不动，让用户在控制台手动加。
	rules := make([]ociclient.RouteRule, 0, len(rt.RouteRules)+1)
	for _, r := range rt.RouteRules {
		if strings.EqualFold(r.RouteType, "LOCAL") {
			prep.Warnings = append(prep.Warnings,
				"路由表里有 Oracle 自动维护的规则，为免改坏未自动添加 IPv6 路由，请在控制台手动添加 ::/0 指向互联网网关。")
			return
		}
		r.RouteType = "" // 与以前的写法保持一致：只回写目的地与目标。
		rules = append(rules, r)
	}
	rules = append(rules, ociclient.RouteRule{
		Destination:     "::/0",
		DestinationType: "CIDR_BLOCK",
		NetworkEntityID: gateway,
		Description:     "OCI Core 为 IPv6 添加的默认路由",
	})
	if _, err := client.UpdateRouteTable(ctx, region, rt.ID, rules); err != nil {
		prep.Warnings = append(prep.Warnings, "添加 IPv6 路由失败，"+noRoute)
		return
	}
	prep.Steps = append(prep.Steps, "已添加 IPv6 默认路由 ::/0")
}

// ensureIPv6Egress 在放行全部 IPv4 出站的那个安全列表里，补一条 ::/0 全放行。
func ensureIPv6Egress(ctx context.Context, client *ociclient.Client, region string,
	subnet *ociclient.Subnet, prep *IPv6Prep) {

	const noEgress = "实例暂时无法主动发起 IPv6 连接。"
	if len(subnet.SecurityListIDs) == 0 {
		prep.Warnings = append(prep.Warnings,
			"子网没有关联安全列表（可能用的是网络安全组），未添加 IPv6 出站规则，"+noEgress)
		return
	}

	var target *ociclient.SecurityList
	for _, id := range subnet.SecurityListIDs {
		list, err := client.GetSecurityList(ctx, region, id)
		if err != nil {
			prep.Warnings = append(prep.Warnings, "读取安全列表失败，未添加 IPv6 出站规则，"+noEgress)
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
		prep.Warnings = append(prep.Warnings,
			"安全列表没有放行全部 IPv4 出站（出站规则收紧过），未自动添加 IPv6 出站规则，"+noEgress)
		return
	}

	egress := append(slices.Clone(target.EgressSecurityRules), ociclient.EgressSecurityRule{
		Protocol:        "all",
		Destination:     "::/0",
		DestinationType: "CIDR_BLOCK",
		Description:     "允许所有 IPv6 出站流量（OCI 默认）",
	})
	if _, err := client.UpdateSecurityList(ctx, region, target.ID, target.IngressSecurityRules, egress); err != nil {
		prep.Warnings = append(prep.Warnings, "添加 IPv6 出站规则失败，"+noEgress)
		return
	}
	prep.Steps = append(prep.Steps, "已在安全列表 "+target.DisplayName+" 添加 IPv6 出站规则 ::/0")
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
				return fmt.Errorf("等待%s超时: %w", what, err)
			}
			return fmt.Errorf("等待%s超时", what)
		case <-time.After(pollInterval):
		}
	}
}
