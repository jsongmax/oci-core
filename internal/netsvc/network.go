// Package netsvc 封装网络相关的多步编排：自动建网、更换公网 IP、启用 IPv6。
//
// 这些操作单看都是几次 API 调用，但顺序、等待与回滚的处理很啰嗦，
// 放在这里统一实现，HTTP 层与创建实例流程共用。
package netsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"ocicore/internal/ociclient"
)

// 自动建网使用的默认网段。
//
// 10.0.0.0/16 是 Oracle 控制台"快速创建 VCN"的默认值，
// 与用户在官方文档里看到的保持一致，减少困惑。
const (
	defaultVcnCidr    = "10.0.0.0/16"
	defaultSubnetCidr = "10.0.0.0/24"
	// 这三个名字只用于新建资源。复用已有网络走的是属性匹配
	// （AVAILABLE 且允许公网 IP），不按名字找——所以改名不会让面板
	// 认不出改名前建的那套 VCN。
	autoVcnName     = "ocicore-vcn"
	autoSubnetName  = "ocicore-subnet"
	autoGatewayName = "ocicore-igw"
)

// resourceWaitTimeout 是等待网络资源变为可用的上限。
const resourceWaitTimeout = 90 * time.Second

// EnsureNetworkOptions 是自动建网的参数。
type EnsureNetworkOptions struct {
	Region        string
	CompartmentID string
	// EnableIPv6 为 true 时同时给 VCN 与子网分配 IPv6 前缀。
	EnableIPv6 bool
}

// EnsureNetworkResult 描述自动建网的结果。
type EnsureNetworkResult struct {
	VcnID      string   `json:"vcnId"`
	VcnName    string   `json:"vcnName"`
	SubnetID   string   `json:"subnetId"`
	SubnetName string   `json:"subnetName"`
	Created    bool     `json:"created"`
	Steps      []string `json:"steps"`
}

// EnsureNetwork 保证目标区域存在一个可用的公网子网，没有就建一个。
//
// 复用优先：先找已有的公网子网。用户多半已经在 Oracle 控制台建过网络，
// 再建一套只会把配额和心智负担都撑爆。
func EnsureNetwork(ctx context.Context, client *ociclient.Client, opts EnsureNetworkOptions) (*EnsureNetworkResult, error) {
	result := &EnsureNetworkResult{}

	subnets, err := client.ListSubnets(ctx, opts.Region, opts.CompartmentID, "")
	if err != nil {
		return nil, fmt.Errorf("读取子网列表失败: %w", err)
	}
	for _, sub := range subnets {
		// ProhibitPublicIpOnVnic 为 true 的是私有子网，实例起来也没有公网 IP。
		if sub.LifecycleState == "AVAILABLE" && !sub.ProhibitPublicIPOnVnic {
			result.VcnID = sub.VcnID
			result.SubnetID = sub.ID
			result.SubnetName = sub.DisplayName
			result.Steps = append(result.Steps, "复用已有公网子网 "+sub.DisplayName)
			return result, nil
		}
	}

	return createNetwork(ctx, client, opts, result)
}

// createNetwork 从零建一套 VCN + 网关 + 路由 + 子网。
func createNetwork(ctx context.Context, client *ociclient.Client,
	opts EnsureNetworkOptions, result *EnsureNetworkResult) (*EnsureNetworkResult, error) {

	result.Created = true

	vcn, err := client.CreateVcn(ctx, opts.Region, ociclient.CreateVcnRequest{
		CompartmentID: opts.CompartmentID,
		CidrBlock:     defaultVcnCidr,
		DisplayName:   autoVcnName,
		DnsLabel:      "ocicore",
		IsIpv6Enabled: opts.EnableIPv6,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 VCN 失败: %w", err)
	}
	result.VcnID = vcn.ID
	result.VcnName = vcn.DisplayName
	result.Steps = append(result.Steps, "已创建 VCN "+vcn.DisplayName+" ("+defaultVcnCidr+")")

	// VCN 刚创建时处于 PROVISIONING，此时挂网关会失败。
	if err := waitVcnAvailable(ctx, client, opts.Region, vcn.ID); err != nil {
		return nil, err
	}

	gateway, err := client.CreateInternetGateway(ctx, opts.Region, opts.CompartmentID, vcn.ID, autoGatewayName)
	if err != nil {
		return nil, fmt.Errorf("创建互联网网关失败: %w", err)
	}
	result.Steps = append(result.Steps, "已创建互联网网关")

	// 没有这条默认路由，实例即使有公网 IP 也出不去。
	if vcn.DefaultRouteTableID != "" {
		_, err := client.UpdateRouteTable(ctx, opts.Region, vcn.DefaultRouteTableID, []ociclient.RouteRule{{
			Destination:     "0.0.0.0/0",
			DestinationType: "CIDR_BLOCK",
			NetworkEntityID: gateway.ID,
			Description:     "OCI Core 自动创建的默认路由",
		}})
		if err != nil {
			return nil, fmt.Errorf("配置默认路由失败: %w", err)
		}
		result.Steps = append(result.Steps, "已配置默认路由 0.0.0.0/0")
	}

	if opts.EnableIPv6 {
		if err := client.AddVcnIpv6Cidr(ctx, opts.Region, vcn.ID); err != nil {
			// IPv6 是加分项，失败不该让整个建网流程回滚。
			slog.Warn("为 VCN 分配 IPv6 前缀失败", "vcn", vcn.ID, "err", err)
			result.Steps = append(result.Steps, "IPv6 前缀分配失败，已跳过")
		} else {
			result.Steps = append(result.Steps, "已为 VCN 分配 IPv6 前缀")
		}
	}

	// 区域级子网（不指定可用域）比 AD 级子网更灵活：
	// 实例可以落在该区域任意可用域，抢容量时多一分余地。
	subnet, err := client.CreateSubnet(ctx, opts.Region, ociclient.CreateSubnetRequest{
		CompartmentID: opts.CompartmentID,
		VcnID:         vcn.ID,
		CidrBlock:     defaultSubnetCidr,
		DisplayName:   autoSubnetName,
		DnsLabel:      "sub",
	})
	if err != nil {
		return nil, fmt.Errorf("创建子网失败: %w", err)
	}
	result.SubnetID = subnet.ID
	result.SubnetName = subnet.DisplayName
	result.Steps = append(result.Steps, "已创建子网 "+subnet.DisplayName+" ("+defaultSubnetCidr+")")

	if opts.EnableIPv6 {
		if err := client.AddSubnetIpv6Cidr(ctx, opts.Region, subnet.ID, ""); err != nil {
			slog.Warn("为子网分配 IPv6 前缀失败", "subnet", subnet.ID, "err", err)
		} else {
			result.Steps = append(result.Steps, "已为子网分配 IPv6 前缀")
		}
	}

	return result, nil
}

// waitVcnAvailable 等待 VCN 进入 AVAILABLE。
func waitVcnAvailable(ctx context.Context, client *ociclient.Client, region, vcnID string) error {
	ctx, cancel := context.WithTimeout(ctx, resourceWaitTimeout)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		vcn, err := client.GetVcn(ctx, region, vcnID)
		if err == nil && vcn.LifecycleState == "AVAILABLE" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待 VCN 就绪超时，请稍后在网络页面确认状态")
		case <-ticker.C:
		}
	}
}

// ErrReservedIP 表示目标公网 IP 是保留 IP，不能用"删了重建"的方式更换。
var ErrReservedIP = errors.New("该实例使用的是保留公网 IP，删除后将永久释放，请在网络页面手动处理")

// ChangePublicIPResult 描述一次换 IP 的结果。
type ChangePublicIPResult struct {
	OldIP string `json:"oldIp"`
	NewIP string `json:"newIp"`
}

// ChangePublicIP 更换实例主网卡上的临时公网 IP。
//
// 实现方式是删掉现有的临时公网 IP 再申请一个新的。原 IP 不可找回，
// 且 SSH 连接会立刻中断——HTTP 层必须在调用前完成 L2 确认。
func ChangePublicIP(ctx context.Context, client *ociclient.Client, region, vnicID, compartmentID string) (*ChangePublicIPResult, error) {
	privateIPs, err := client.ListPrivateIPs(ctx, region, vnicID)
	if err != nil {
		return nil, fmt.Errorf("读取私网 IP 失败: %w", err)
	}

	var primary *ociclient.PrivateIP
	for i := range privateIPs {
		if privateIPs[i].IsPrimary {
			primary = &privateIPs[i]
			break
		}
	}
	if primary == nil {
		return nil, errors.New("未找到主私网 IP，无法更换公网 IP")
	}

	result := &ChangePublicIPResult{}

	existing, err := client.GetPublicIPByPrivateIP(ctx, region, primary.ID)
	if err != nil {
		return nil, fmt.Errorf("读取当前公网 IP 失败: %w", err)
	}
	if existing != nil {
		// 保留 IP 是独立计费资源，删掉就再也拿不回来了，绝不能顺手删。
		if existing.Lifetime == ociclient.PublicIPReserved {
			return nil, ErrReservedIP
		}
		result.OldIP = existing.IPAddress
		if err := client.DeletePublicIP(ctx, region, existing.ID); err != nil {
			return nil, fmt.Errorf("释放原公网 IP 失败: %w", err)
		}
	}

	// 删除是异步的，紧接着申请新 IP 有几率撞上"该私网 IP 已有公网 IP"的冲突，
	// 因此这里带重试。
	var created *ociclient.PublicIP
	deadline := time.Now().Add(30 * time.Second)
	for {
		created, err = client.CreatePublicIP(ctx, region, compartmentID,
			ociclient.PublicIPEphemeral, primary.ID, "")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("申请新公网 IP 失败: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	result.NewIP = created.IPAddress
	return result, nil
}

// EnableIPv6Result 描述启用 IPv6 的结果。
type EnableIPv6Result struct {
	Address string   `json:"address"`
	Steps   []string `json:"steps"`
}

// EnableIPv6 为实例的网卡启用 IPv6。
//
// 完整链路是三段：VCN 拿到前缀 → 子网拿到前缀 → 网卡分配地址。
// 前两段可能早就做过，因此都按"已存在即跳过"处理。
func EnableIPv6(ctx context.Context, client *ociclient.Client, region, vnicID, subnetID string) (*EnableIPv6Result, error) {
	result := &EnableIPv6Result{}

	subnet, err := client.GetSubnet(ctx, region, subnetID)
	if err != nil {
		return nil, fmt.Errorf("读取子网失败: %w", err)
	}

	if subnet.Ipv6CidrBlock == "" {
		vcn, err := client.GetVcn(ctx, region, subnet.VcnID)
		if err != nil {
			return nil, fmt.Errorf("读取 VCN 失败: %w", err)
		}
		if len(vcn.Ipv6CidrBlocks) == 0 {
			if err := client.AddVcnIpv6Cidr(ctx, region, vcn.ID); err != nil {
				return nil, fmt.Errorf("为 VCN 分配 IPv6 前缀失败: %w", err)
			}
			result.Steps = append(result.Steps, "已为 VCN 分配 IPv6 前缀")
		}
		if err := client.AddSubnetIpv6Cidr(ctx, region, subnetID, ""); err != nil {
			return nil, fmt.Errorf("为子网分配 IPv6 前缀失败: %w", err)
		}
		result.Steps = append(result.Steps, "已为子网分配 IPv6 前缀")
	}

	// 已经有地址就直接返回，重复调用不该产生第二个地址。
	if existing, err := client.ListIpv6s(ctx, region, vnicID); err == nil && len(existing) > 0 {
		result.Address = existing[0].IPAddress
		result.Steps = append(result.Steps, "该网卡已有 IPv6 地址")
		return result, nil
	}

	ipv6, err := client.CreateIpv6(ctx, region, vnicID)
	if err != nil {
		return nil, fmt.Errorf("分配 IPv6 地址失败: %w", err)
	}
	result.Address = ipv6.IPAddress
	result.Steps = append(result.Steps, "已分配 IPv6 地址 "+ipv6.IPAddress)
	return result, nil
}

// RuleTemplate 是常用端口的安全规则模板。
type RuleTemplate struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Protocol    string `json:"protocol"`
	Port        int    `json:"port"`
	Description string `json:"description"`
	// Dangerous 标记会大幅扩大攻击面的规则，UI 上需要显著警示。
	Dangerous bool `json:"dangerous"`
}

// RuleTemplates 返回安全规则的快捷模板。
func RuleTemplates() []RuleTemplate {
	return []RuleTemplate{
		{Key: "ssh", Label: "SSH", Protocol: "6", Port: 22, Description: "远程登录"},
		{Key: "http", Label: "HTTP", Protocol: "6", Port: 80, Description: "网站服务"},
		{Key: "https", Label: "HTTPS", Protocol: "6", Port: 443, Description: "加密网站服务"},
		{Key: "icmp", Label: "ICMP", Protocol: "1", Port: 0, Description: "允许 ping"},
		{Key: "all", Label: "全部放行", Protocol: "all", Port: 0,
			Description: "放行所有入站流量。除非你清楚自己在做什么，否则不要开启。", Dangerous: true},
	}
}

// BuildIngressRule 按模板构造一条入站规则。source 留空默认为 0.0.0.0/0。
func BuildIngressRule(tpl RuleTemplate, source, description string) ociclient.IngressSecurityRule {
	if source == "" {
		source = "0.0.0.0/0"
	}
	if description == "" {
		description = tpl.Description
	}

	rule := ociclient.IngressSecurityRule{
		Protocol:    tpl.Protocol,
		Source:      source,
		SourceType:  "CIDR_BLOCK",
		Description: description,
	}
	switch tpl.Protocol {
	case "6": // TCP
		rule.TCPOptions = &ociclient.TCPOptions{
			DestinationPortRange: &ociclient.PortRange{Min: tpl.Port, Max: tpl.Port},
		}
	case "17": // UDP
		rule.UDPOptions = &ociclient.UDPOptions{
			DestinationPortRange: &ociclient.PortRange{Min: tpl.Port, Max: tpl.Port},
		}
	case "1": // ICMP
		rule.ICMPOptions = &ociclient.ICMPOptions{Type: 3, Code: intPtr(4)}
	}
	return rule
}

// IsAllowAllRule 判断一条入站规则是否等同于全放行，用于在 UI 上打警示标记。
func IsAllowAllRule(rule ociclient.IngressSecurityRule) bool {
	if !strings.EqualFold(rule.Protocol, "all") {
		return false
	}
	return rule.Source == "0.0.0.0/0" || rule.Source == "::/0"
}

func intPtr(v int) *int { return &v }
