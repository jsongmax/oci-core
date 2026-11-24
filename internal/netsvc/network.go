// Package netsvc 封装网络相关的多步编排：自动建网、更换公网 IP、启用 IPv6。
//
// 这些操作单看都是几次 API 调用，但顺序、等待与回滚的处理很啰嗦，
// 放在这里统一实现，HTTP 层与创建实例流程共用。
package netsvc

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"ocicore/internal/i18n"
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
	// EnableIPv6 为 true 时，复用优先挑已经有 IPv6 的子网，新建的 VCN 直接
	// 带上 IPv6 前缀。
	//
	// 它**不负责**给子网分配前缀、补路由——那是 PrepareIPv6 的事，调用方
	// 拿到子网之后必须自己调一次。分开是因为守候任务每一轮都会调
	// EnsureNetwork，而 PrepareIPv6 只需要成功一次。
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
		return nil, i18n.Wrap(err, "net.list_subnets_failed")
	}
	// ProhibitPublicIpOnVnic 为 true 的是私有子网，实例起来也没有公网 IP。
	var pick *ociclient.Subnet
	for i := range subnets {
		sub := &subnets[i]
		if sub.LifecycleState != "AVAILABLE" || sub.ProhibitPublicIPOnVnic {
			continue
		}
		// 要 IPv6 时优先挑已经开通的，少改一处用户的网络。
		if opts.EnableIPv6 && subnetHasIPv6(sub) {
			pick = sub
			break
		}
		if pick == nil {
			pick = sub
		}
	}
	if pick != nil {
		result.VcnID = pick.VcnID
		result.SubnetID = pick.ID
		result.SubnetName = pick.DisplayName
		result.Steps = append(result.Steps, i18n.M("net.step.reuse_subnet", pick.DisplayName))
		return result, nil
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
		return nil, i18n.Wrap(err, "net.create_vcn_failed")
	}
	result.VcnID = vcn.ID
	result.VcnName = vcn.DisplayName
	result.Steps = append(result.Steps, i18n.M("net.step.vcn_created", vcn.DisplayName, defaultVcnCidr))

	// VCN 刚创建时处于 PROVISIONING，此时挂网关会失败。
	if err := waitVcnAvailable(ctx, client, opts.Region, vcn.ID); err != nil {
		return nil, err
	}

	gateway, err := client.CreateInternetGateway(ctx, opts.Region, opts.CompartmentID, vcn.ID, autoGatewayName)
	if err != nil {
		return nil, i18n.Wrap(err, "net.create_igw_failed")
	}
	result.Steps = append(result.Steps, i18n.M("net.step.igw_created"))

	// 没有这条默认路由，实例即使有公网 IP 也出不去。
	if vcn.DefaultRouteTableID != "" {
		_, err := client.UpdateRouteTable(ctx, opts.Region, vcn.DefaultRouteTableID, []ociclient.RouteRule{{
			Destination:     "0.0.0.0/0",
			DestinationType: "CIDR_BLOCK",
			NetworkEntityID: gateway.ID,
			Description:     i18n.T(i18n.FromContext(ctx), "net.desc.default_route"),
		}})
		if err != nil {
			return nil, i18n.Wrap(err, "net.route_failed")
		}
		result.Steps = append(result.Steps, i18n.M("net.step.route"))
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
		return nil, i18n.Wrap(err, "net.create_subnet_failed")
	}
	result.SubnetID = subnet.ID
	result.SubnetName = subnet.DisplayName
	result.Steps = append(result.Steps, i18n.M("net.step.subnet_created", subnet.DisplayName, defaultSubnetCidr))

	// IPv6 不在这里处理：VCN 创建时已经带上前缀，子网前缀与路由由调用方
	// 接着调 PrepareIPv6 完成。以前这里还会再给 VCN 申请一次前缀——每个
	// VCN 只能有一个 Oracle 分配的前缀，那一步必然失败——失败又被吞掉，
	// 最后创建实例时才以 "Subnet must support ipv6" 的形式暴露。
	return result, nil
}

// waitVcnAvailable 等待 VCN 进入 AVAILABLE。
func waitVcnAvailable(ctx context.Context, client *ociclient.Client, region, vcnID string) error {
	ctx, cancel := context.WithTimeout(ctx, resourceWaitTimeout)
	defer cancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		vcn, err := client.GetVcn(ctx, region, vcnID)
		if err == nil && vcn.LifecycleState == "AVAILABLE" {
			return nil
		}
		select {
		case <-ctx.Done():
			return i18n.Err("net.vcn_wait_timeout")
		case <-ticker.C:
		}
	}
}

// ErrReservedIP 表示目标公网 IP 是预留 IP，不能用"删了重建"的方式更换。
var ErrReservedIP = i18n.Err("net.reserved_ip")

// ErrConfirmReservedIP 表示删除/更换预留公网 IP 时未在 confirm 中回传匹配的 IP 地址。
var ErrConfirmReservedIP = i18n.Err("net.confirm_reserved_ip")

// ErrStateChanged 表示目标私网槽位当前的公网 IP 状态与调用方预期不一致（乐观并发校验失败）。
var ErrStateChanged = i18n.Err("net.state_changed")

// 辅助私网 IP 操作的业务校验错误，供 HTTP 层映射为 400/404。
var (
	ErrInvalidPrivateIP  = i18n.Err("net.invalid_private_ipv4")
	ErrPrimaryPrivateIP  = i18n.Err("net.cannot_delete_primary_ip")
	ErrPrivateIPNotFound = i18n.Err("net.private_ip_not_found")
)

var (
	changeIPRetryInterval = 2 * time.Second
	publicIPPollInterval  = 1500 * time.Millisecond
	publicIPPollTimeout   = 30 * time.Second
)

// ChangePublicIPOptions 控制更换公网 IP 的目标与策略。
type ChangePublicIPOptions struct {
	// PrivateIPID 指定网卡上的私网 IP（可用于辅助私网 IP）；留空时默认取主私网 IP。
	PrivateIPID string
	// AllowReserved 为 true 时允许释放现有预留公网 IP 并重新申请新的预留公网 IP。
	AllowReserved bool
	// ConfirmIP 在更换现有预留公网 IP 时必须等于旧预留 IP 的地址（L3 确认）。
	ConfirmIP string
	// ExpectPublicIPID 与 ExpectPublicIP 为可选的乐观并发校验条件；
	// 传入非 nil 空字符串表示预期该槽位当前未绑定任何公网 IP。
	ExpectPublicIPID *string
	ExpectPublicIP   *string
}

// ChangePublicIPResult 描述一次换 IP 的结果。
type ChangePublicIPResult struct {
	OldIP         string `json:"oldIp"`
	NewIP         string `json:"newIp"`
	Lifetime      string `json:"lifetime,omitempty"`
	PrivateIP     string `json:"privateIp,omitempty"`
	IsPrimary     bool   `json:"isPrimary,omitempty"`
	IsPrimaryVnic bool   `json:"isPrimaryVnic,omitempty"`
}

func isConflictErr(err error) bool {
	apiErr, ok := ociclient.AsAPIError(err)
	return ok && (apiErr.Class == ociclient.ClassConflict || apiErr.StatusCode == http.StatusConflict)
}

// retryOnConflict 仅在 OCI 返回 409 Conflict 时按短间隔重试，避免对 400/404/QuotaExceeded 等必然失败的错误白等。
func retryOnConflict(ctx context.Context, timeout time.Duration, fn func() error) error {
	deadline := time.Now().Add(timeout)
	for {
		err := fn()
		if err == nil {
			return nil
		}
		if !isConflictErr(err) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(changeIPRetryInterval):
		}
	}
}

func checkExpectedPublicIP(existing *ociclient.PublicIP, expectID, expectIP *string) error {
	if expectID != nil {
		wantID := strings.TrimSpace(*expectID)
		actualID := ""
		if existing != nil {
			actualID = existing.ID
		}
		if actualID != wantID {
			return ErrStateChanged
		}
	}
	if expectIP != nil {
		wantIP := strings.TrimSpace(*expectIP)
		actualIP := ""
		if existing != nil {
			actualIP = existing.IPAddress
		}
		if actualIP != wantIP {
			return ErrStateChanged
		}
	}
	return nil
}

func ephemeralCompartmentID(target *ociclient.PrivateIP, fallback string) string {
	if target != nil && strings.TrimSpace(target.CompartmentID) != "" {
		return target.CompartmentID
	}
	return fallback
}

func publicIPStateReady(pub *ociclient.PublicIP, wantAssigned bool, wantPrivateIPID string) bool {
	if pub == nil {
		return false
	}
	bound := publicIPBoundPrivateID(pub)
	if pub.LifecycleState == "" {
		if wantAssigned {
			return wantPrivateIPID == "" || bound == wantPrivateIPID
		}
		return bound == ""
	}
	if wantAssigned {
		return pub.LifecycleState == "ASSIGNED" && (wantPrivateIPID == "" || bound == wantPrivateIPID)
	}
	return pub.LifecycleState == "AVAILABLE" && bound == ""
}

// waitPublicIPState 轮询预留公网 IP 的异步绑定/解绑状态，直到落定为 ASSIGNED 或 AVAILABLE。
func waitPublicIPState(ctx context.Context, client *ociclient.Client, region string, current *ociclient.PublicIP, wantAssigned bool, wantPrivateIPID string) (*ociclient.PublicIP, error) {
	if current == nil || current.ID == "" {
		return current, nil
	}
	if publicIPStateReady(current, wantAssigned, wantPrivateIPID) {
		return current, nil
	}
	if current.LifecycleState == "TERMINATED" || current.LifecycleState == "TERMINATING" {
		return nil, i18n.Err("net.public_ip_terminated")
	}
	deadline := time.Now().Add(publicIPPollTimeout)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(publicIPPollInterval):
		}
		pub, err := client.GetPublicIP(ctx, region, current.ID)
		if err != nil {
			if time.Now().After(deadline) {
				return nil, err
			}
			continue
		}
		if publicIPStateReady(pub, wantAssigned, wantPrivateIPID) {
			return pub, nil
		}
		if pub.LifecycleState == "TERMINATED" || pub.LifecycleState == "TERMINATING" {
			return nil, i18n.Err("net.public_ip_terminated")
		}
		if time.Now().After(deadline) {
			return nil, i18n.Err("net.public_ip_wait_timeout")
		}
	}
}

// rollbackRebindReserved 在解绑旧预留 IP 后后续步骤失败时，尽力将旧预留 IP 绑回原私网槽位。
func rollbackRebindReserved(ctx context.Context, client *ociclient.Client, region, publicIPID, privateIPID string) {
	if publicIPID == "" || privateIPID == "" {
		return
	}
	rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	privID := privateIPID
	var updated *ociclient.PublicIP
	err := retryOnConflict(rbCtx, 15*time.Second, func() error {
		var callErr error
		updated, callErr = client.UpdatePublicIP(rbCtx, region, publicIPID, ociclient.UpdatePublicIPRequest{PrivateIPID: &privID})
		return callErr
	})
	if err == nil && updated != nil {
		_, _ = waitPublicIPState(rbCtx, client, region, updated, true, privateIPID)
	}
}

// UnbindReservedIP 将一个预留公网 IP 从当前私网 IP 解绑，并轮询直到其状态回到 AVAILABLE。
func UnbindReservedIP(ctx context.Context, client *ociclient.Client, region, publicIPID string) (*ociclient.PublicIP, error) {
	empty := ""
	updated, err := client.UpdatePublicIP(ctx, region, publicIPID, ociclient.UpdatePublicIPRequest{PrivateIPID: &empty})
	if err != nil {
		return nil, i18n.Wrap(err, "net.unbind_ip_failed")
	}
	settled, err := waitPublicIPState(ctx, client, region, updated, false, "")
	if err != nil {
		return nil, i18n.Wrap(err, "net.unbind_ip_failed")
	}
	return settled, nil
}

// ChangePublicIP 更换或分配实例网卡上某个私网 IP 的公网 IP。
//
// 更换预留公网 IP 时优先走「先建新 → 解旧 → 绑新 → 删旧」的安全顺序，并在中途失败时回滚；
// 仅在建新撞上配额上限（QuotaExceeded）时才回退为先删后建。
// 辅助私网 IP 按 OCI 规则只能绑定预留公网 IP（RESERVED）。
func ChangePublicIP(ctx context.Context, client *ociclient.Client, region, vnicID, compartmentID string, opts ChangePublicIPOptions) (*ChangePublicIPResult, error) {
	target, err := resolveTargetPrivateIP(ctx, client, region, vnicID, opts.PrivateIPID)
	if err != nil {
		return nil, err
	}

	result := &ChangePublicIPResult{
		PrivateIP: target.IPAddress,
		IsPrimary: target.IsPrimary,
	}

	existing, err := client.GetPublicIPByPrivateIP(ctx, region, target.ID)
	if err != nil {
		return nil, i18n.Wrap(err, "net.read_public_ip_failed")
	}
	if err := checkExpectedPublicIP(existing, opts.ExpectPublicIPID, opts.ExpectPublicIP); err != nil {
		return nil, err
	}

	lifetime := ociclient.PublicIPEphemeral
	if !target.IsPrimary {
		lifetime = ociclient.PublicIPReserved
	}
	displayName := ""

	if existing != nil && existing.Lifetime == ociclient.PublicIPReserved {
		// 预留 IP 是独立配额资源，删掉就再也拿不回来了，绝不能顺手删。
		// 必须显式允许且在 confirm 中回传匹配的旧 IP 地址（L3 确认）。
		if !opts.AllowReserved {
			return nil, ErrReservedIP
		}
		if strings.TrimSpace(opts.ConfirmIP) != existing.IPAddress {
			return nil, ErrConfirmReservedIP
		}
		result.OldIP = existing.IPAddress
		lifetime = ociclient.PublicIPReserved
		displayName = existing.DisplayName

		// 优先无损顺序：先申请一个新的未绑定预留 IP，避免解绑/删除旧 IP 后才发现建新失败。
		newPub, createErr := client.CreatePublicIP(ctx, region, compartmentID, ociclient.PublicIPReserved, "", displayName)
		if createErr == nil {
			if _, err := UnbindReservedIP(ctx, client, region, existing.ID); err != nil {
				cleanCtx, cleanCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
				_ = client.DeletePublicIP(cleanCtx, region, newPub.ID)
				cleanCancel()
				return nil, err
			}
			privID := target.ID
			var bound *ociclient.PublicIP
			bindErr := retryOnConflict(ctx, 45*time.Second, func() error {
				var callErr error
				bound, callErr = client.UpdatePublicIP(ctx, region, newPub.ID, ociclient.UpdatePublicIPRequest{PrivateIPID: &privID})
				return callErr
			})
			if bindErr == nil {
				bound, bindErr = waitPublicIPState(ctx, client, region, bound, true, target.ID)
			}
			if bindErr != nil {
				rollbackRebindReserved(ctx, client, region, existing.ID, target.ID)
				cleanCtx, cleanCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
				_ = client.DeletePublicIP(cleanCtx, region, newPub.ID)
				cleanCancel()
				return nil, i18n.Wrap(bindErr, "net.bind_ip_failed")
			}
			if delErr := retryOnConflict(ctx, 30*time.Second, func() error {
				return client.DeletePublicIP(ctx, region, existing.ID)
			}); delErr != nil {
				return nil, i18n.Wrap(delErr, "net.release_ip_failed")
			}
			newIP := newPub.IPAddress
			if bound != nil && bound.IPAddress != "" {
				newIP = bound.IPAddress
			}
			result.NewIP = newIP
			result.Lifetime = ociclient.PublicIPReserved
			return result, nil
		}

		// 仅在预留 IP 配额刚好打满（QuotaExceeded）时回退到先删旧再建新；
		// 其余错误立刻中止，不触碰原有预留 IP。
		apiErr, isAPI := ociclient.AsAPIError(createErr)
		if !isAPI || apiErr.Class != ociclient.ClassQuotaExceeded {
			return nil, i18n.Wrap(createErr, "net.new_ip_failed")
		}
		if err := client.DeletePublicIP(ctx, region, existing.ID); err != nil {
			return nil, i18n.Wrap(err, "net.release_ip_failed")
		}
	} else if existing != nil {
		result.OldIP = existing.IPAddress
		if err := client.DeletePublicIP(ctx, region, existing.ID); err != nil {
			return nil, i18n.Wrap(err, "net.release_ip_failed")
		}
	}

	createCompID := compartmentID
	if lifetime == ociclient.PublicIPEphemeral {
		createCompID = ephemeralCompartmentID(target, compartmentID)
	}

	var created *ociclient.PublicIP
	createFn := func() error {
		var callErr error
		created, callErr = client.CreatePublicIP(ctx, region, createCompID, lifetime, target.ID, displayName)
		return callErr
	}
	if existing != nil {
		// 删除是异步的，紧接着申请新 IP 有几率撞上 409 Conflict，仅对 409 重试。
		if err := retryOnConflict(ctx, 45*time.Second, createFn); err != nil {
			return nil, i18n.Wrap(err, "net.new_ip_failed")
		}
	} else {
		if err := createFn(); err != nil {
			return nil, i18n.Wrap(err, "net.new_ip_failed")
		}
	}

	result.NewIP = created.IPAddress
	result.Lifetime = lifetime
	return result, nil
}

func resolveTargetPrivateIP(ctx context.Context, client *ociclient.Client, region, vnicID, privateIPID string) (*ociclient.PrivateIP, error) {
	privateIPs, err := client.ListPrivateIPs(ctx, region, vnicID)
	if err != nil {
		return nil, i18n.Wrap(err, "net.read_private_ip_failed")
	}
	if privateIPID != "" {
		for i := range privateIPs {
			if privateIPs[i].ID == privateIPID {
				return &privateIPs[i], nil
			}
		}
		return nil, ErrPrivateIPNotFound
	}
	for i := range privateIPs {
		if privateIPs[i].IsPrimary {
			return &privateIPs[i], nil
		}
	}
	return nil, i18n.Err("net.no_primary_ip")
}

func publicIPBoundPrivateID(pub *ociclient.PublicIP) string {
	if pub == nil {
		return ""
	}
	if pub.PrivateIPID != "" {
		return pub.PrivateIPID
	}
	if strings.HasPrefix(pub.AssignedEntityID, "ocid1.privateip.") {
		return pub.AssignedEntityID
	}
	return ""
}

// BindReservedIPOptions 指定要绑定预留公网 IP 的私网 IP 槽位与目标预留 IP。
type BindReservedIPOptions struct {
	PrivateIPID      string
	ReservedIPID     string
	ExpectPublicIPID *string
	ExpectPublicIP   *string
}

// BindReservedIPResult 描述绑定预留 IP 的结果。
type BindReservedIPResult struct {
	OldIP       string `json:"oldIp,omitempty"`
	OldLifetime string `json:"oldLifetime,omitempty"`
	NewIP       string `json:"newIp"`
	PublicIPID  string `json:"publicIpId"`
	PrivateIP   string `json:"privateIp,omitempty"`
	IsPrimary   bool   `json:"isPrimary,omitempty"`
}

// BindReservedIP 将一个空闲的预留公网 IP 绑定到实例网卡的指定私网 IP 槽位。
// 若该槽位当前已绑定临时公网 IP，会先删除该临时 IP 再绑定预留 IP；
// 若当前已绑定另一个预留公网 IP，会先无损解绑旧预留 IP（不删除）再绑定新的。
func BindReservedIP(ctx context.Context, client *ociclient.Client, region, vnicID string, opts BindReservedIPOptions) (*BindReservedIPResult, error) {
	target, err := resolveTargetPrivateIP(ctx, client, region, vnicID, opts.PrivateIPID)
	if err != nil {
		return nil, err
	}

	reserved, err := client.GetPublicIP(ctx, region, opts.ReservedIPID)
	if err != nil {
		return nil, i18n.Wrap(err, "net.read_public_ip_failed")
	}
	if reserved.Lifetime != ociclient.PublicIPReserved {
		return nil, i18n.Err("net.not_reserved_ip")
	}
	if boundPriv := publicIPBoundPrivateID(reserved); boundPriv != "" && boundPriv != target.ID {
		return nil, i18n.Err("net.reserved_ip_in_use")
	}
	if publicIPBoundPrivateID(reserved) != target.ID && reserved.LifecycleState != "" && reserved.LifecycleState != "AVAILABLE" {
		return nil, i18n.Err("net.reserved_ip_not_available")
	}

	result := &BindReservedIPResult{
		PublicIPID: reserved.ID,
		PrivateIP:  target.IPAddress,
		IsPrimary:  target.IsPrimary,
	}
	if publicIPBoundPrivateID(reserved) == target.ID {
		result.NewIP = reserved.IPAddress
		return result, nil
	}

	existing, err := client.GetPublicIPByPrivateIP(ctx, region, target.ID)
	if err != nil {
		return nil, i18n.Wrap(err, "net.read_public_ip_failed")
	}
	if err := checkExpectedPublicIP(existing, opts.ExpectPublicIPID, opts.ExpectPublicIP); err != nil {
		return nil, err
	}
	unboundOldReserved := false
	if existing != nil && existing.ID != reserved.ID {
		result.OldIP = existing.IPAddress
		result.OldLifetime = existing.Lifetime
		if existing.Lifetime == ociclient.PublicIPReserved {
			if _, err := UnbindReservedIP(ctx, client, region, existing.ID); err != nil {
				return nil, err
			}
			unboundOldReserved = true
		} else {
			if err := client.DeletePublicIP(ctx, region, existing.ID); err != nil {
				return nil, i18n.Wrap(err, "net.release_ip_failed")
			}
		}
	}

	var updated *ociclient.PublicIP
	privID := target.ID
	bindFn := func() error {
		var callErr error
		updated, callErr = client.UpdatePublicIP(ctx, region, reserved.ID, ociclient.UpdatePublicIPRequest{PrivateIPID: &privID})
		return callErr
	}
	if existing != nil {
		err = retryOnConflict(ctx, 45*time.Second, bindFn)
	} else {
		err = bindFn()
	}
	if err == nil {
		updated, err = waitPublicIPState(ctx, client, region, updated, true, target.ID)
	}
	if err != nil {
		if unboundOldReserved {
			rollbackRebindReserved(ctx, client, region, existing.ID, target.ID)
		}
		return nil, i18n.Wrap(err, "net.bind_ip_failed")
	}

	result.NewIP = updated.IPAddress
	if result.NewIP == "" {
		result.NewIP = reserved.IPAddress
	}
	return result, nil
}

// UnassignPublicIPOptions 指定取消绑定公网 IP 的目标槽位与乐观锁条件。
type UnassignPublicIPOptions struct {
	PrivateIPID      string
	ExpectPublicIPID *string
	ExpectPublicIP   *string
}

// UnassignPublicIPResult 描述从私网 IP 取消绑定公网 IP 的结果。
type UnassignPublicIPResult struct {
	OldIP      string `json:"oldIp"`
	Lifetime   string `json:"lifetime"`
	PublicIPID string `json:"publicIpId"`
	PrivateIP  string `json:"privateIp,omitempty"`
	IsPrimary  bool   `json:"isPrimary,omitempty"`
}

// UnassignPublicIP 取消绑定实例网卡上某个私网 IP 的公网 IP。
// 临时 IP 会被释放删除；预留 IP 仅解绑回到空闲池，不会被删除。
func UnassignPublicIP(ctx context.Context, client *ociclient.Client, region, vnicID string, opts UnassignPublicIPOptions) (*UnassignPublicIPResult, error) {
	target, err := resolveTargetPrivateIP(ctx, client, region, vnicID, opts.PrivateIPID)
	if err != nil {
		return nil, err
	}
	existing, err := client.GetPublicIPByPrivateIP(ctx, region, target.ID)
	if err != nil {
		return nil, i18n.Wrap(err, "net.read_public_ip_failed")
	}
	if err := checkExpectedPublicIP(existing, opts.ExpectPublicIPID, opts.ExpectPublicIP); err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, i18n.Err("net.no_public_ip_bound")
	}

	if existing.Lifetime == ociclient.PublicIPReserved {
		if _, err := UnbindReservedIP(ctx, client, region, existing.ID); err != nil {
			return nil, err
		}
	} else {
		if err := client.DeletePublicIP(ctx, region, existing.ID); err != nil {
			return nil, i18n.Wrap(err, "net.release_ip_failed")
		}
	}

	return &UnassignPublicIPResult{
		OldIP:      existing.IPAddress,
		Lifetime:   existing.Lifetime,
		PublicIPID: existing.ID,
		PrivateIP:  target.IPAddress,
		IsPrimary:  target.IsPrimary,
	}, nil
}

// SwitchToEphemeralIPOptions 指定切换为临时公网 IP 的目标槽位与乐观锁条件。
type SwitchToEphemeralIPOptions struct {
	PrivateIPID      string
	ExpectPublicIPID *string
	ExpectPublicIP   *string
}

// SwitchToEphemeralIPResult 描述将主私网 IP 槽切换为临时公网 IP 的结果。
type SwitchToEphemeralIPResult struct {
	OldIP       string `json:"oldIp,omitempty"`
	OldLifetime string `json:"oldLifetime,omitempty"`
	NewIP       string `json:"newIp"`
	Lifetime    string `json:"lifetime"`
	PrivateIP   string `json:"privateIp,omitempty"`
	IsPrimary   bool   `json:"isPrimary,omitempty"`
}

// SwitchToEphemeralIP 将网卡的主私网 IP 槽切换为临时公网 IP。
// 若当前绑定了预留公网 IP，会先无损解绑回空闲池（不删除），再申请一个新的临时公网 IP；
// 辅助私网 IP 按 OCI 规则不支持临时公网 IP，会直接返回错误。
func SwitchToEphemeralIP(ctx context.Context, client *ociclient.Client, region, vnicID, compartmentID string, opts SwitchToEphemeralIPOptions) (*SwitchToEphemeralIPResult, error) {
	target, err := resolveTargetPrivateIP(ctx, client, region, vnicID, opts.PrivateIPID)
	if err != nil {
		return nil, err
	}
	if !target.IsPrimary {
		return nil, i18n.Err("net.ephemeral_only_primary")
	}

	result := &SwitchToEphemeralIPResult{
		PrivateIP: target.IPAddress,
		IsPrimary: target.IsPrimary,
		Lifetime:  ociclient.PublicIPEphemeral,
	}

	existing, err := client.GetPublicIPByPrivateIP(ctx, region, target.ID)
	if err != nil {
		return nil, i18n.Wrap(err, "net.read_public_ip_failed")
	}
	if err := checkExpectedPublicIP(existing, opts.ExpectPublicIPID, opts.ExpectPublicIP); err != nil {
		return nil, err
	}
	unboundOldReserved := false
	if existing != nil {
		if existing.Lifetime == ociclient.PublicIPEphemeral {
			result.NewIP = existing.IPAddress
			return result, nil
		}
		result.OldIP = existing.IPAddress
		result.OldLifetime = existing.Lifetime
		if _, err := UnbindReservedIP(ctx, client, region, existing.ID); err != nil {
			return nil, err
		}
		unboundOldReserved = true
	}

	ephCompID := ephemeralCompartmentID(target, compartmentID)
	var created *ociclient.PublicIP
	createFn := func() error {
		var callErr error
		created, callErr = client.CreatePublicIP(ctx, region, ephCompID, ociclient.PublicIPEphemeral, target.ID, "")
		return callErr
	}
	if existing != nil {
		err = retryOnConflict(ctx, 45*time.Second, createFn)
	} else {
		err = createFn()
	}
	if err != nil {
		if unboundOldReserved {
			rollbackRebindReserved(ctx, client, region, existing.ID, target.ID)
		}
		return nil, i18n.Wrap(err, "net.new_ip_failed")
	}

	result.NewIP = created.IPAddress
	return result, nil
}

// CreateSecondaryPrivateIPResult 描述在网卡上创建辅助私网 IP 的结果。
type CreateSecondaryPrivateIPResult struct {
	PrivateIPID string `json:"privateIpId"`
	PrivateIP   string `json:"privateIp"`
	VnicID      string `json:"vnicId"`
	DisplayName string `json:"displayName,omitempty"`
}

// CreateSecondaryPrivateIP 在指定 VNIC 上创建一个辅助私网 IPv4 槽位。
// ipAddress 为空时由 OCI 从所在子网自动分配。
func CreateSecondaryPrivateIP(ctx context.Context, client *ociclient.Client, region, vnicID, displayName, ipAddress string) (*CreateSecondaryPrivateIPResult, error) {
	displayName = strings.TrimSpace(displayName)
	ipAddress = strings.TrimSpace(ipAddress)
	if ipAddress != "" {
		parsed := net.ParseIP(ipAddress)
		if parsed == nil || parsed.To4() == nil {
			return nil, ErrInvalidPrivateIP
		}
		ipAddress = parsed.To4().String()
	}

	created, err := client.CreatePrivateIP(ctx, region, ociclient.CreatePrivateIPRequest{
		VnicID:      vnicID,
		DisplayName: displayName,
		IPAddress:   ipAddress,
	})
	if err != nil {
		return nil, i18n.Wrap(err, "net.create_private_ip_failed")
	}
	return &CreateSecondaryPrivateIPResult{
		PrivateIPID: created.ID,
		PrivateIP:   created.IPAddress,
		VnicID:      created.VnicID,
		DisplayName: created.DisplayName,
	}, nil
}

// DeleteSecondaryPrivateIPResult 描述释放（删除）辅助私网 IP 的结果。
type DeleteSecondaryPrivateIPResult struct {
	PrivateIPID      string `json:"privateIpId"`
	PrivateIP        string `json:"privateIp"`
	UnboundPublicIP  string `json:"unboundPublicIp,omitempty"`
	PublicIPLifetime string `json:"publicIpLifetime,omitempty"`
}

// DeleteSecondaryPrivateIP 删除指定 VNIC 上的某个辅助私网 IP。
// 会先校验该私网 IP 属于该网卡且不是主私网 IP；若其上绑定了预留公网 IP，
// 会先无损解绑回空闲池（不删除），再删除辅助私网 IP。
func DeleteSecondaryPrivateIP(ctx context.Context, client *ociclient.Client, region, vnicID, privateIPID string) (*DeleteSecondaryPrivateIPResult, error) {
	if strings.TrimSpace(privateIPID) == "" {
		return nil, ErrPrivateIPNotFound
	}
	target, err := resolveTargetPrivateIP(ctx, client, region, vnicID, privateIPID)
	if err != nil {
		return nil, err
	}
	if target.IsPrimary {
		return nil, ErrPrimaryPrivateIP
	}

	result := &DeleteSecondaryPrivateIPResult{
		PrivateIPID: target.ID,
		PrivateIP:   target.IPAddress,
	}

	existing, err := client.GetPublicIPByPrivateIP(ctx, region, target.ID)
	if err != nil {
		return nil, i18n.Wrap(err, "net.read_public_ip_failed")
	}
	unboundOldReserved := false
	if existing != nil {
		result.UnboundPublicIP = existing.IPAddress
		result.PublicIPLifetime = existing.Lifetime
		if existing.Lifetime == ociclient.PublicIPReserved {
			if _, err := UnbindReservedIP(ctx, client, region, existing.ID); err != nil {
				return nil, err
			}
			unboundOldReserved = true
		} else {
			if err := client.DeletePublicIP(ctx, region, existing.ID); err != nil {
				return nil, i18n.Wrap(err, "net.release_ip_failed")
			}
		}
	}

	// 刚解绑公网 IP 时 OCI 侧可能存在短暂异步冲突（409），仅在刚解绑且遇到 409 时重试。
	deleteFn := func() error {
		return client.DeletePrivateIP(ctx, region, target.ID)
	}
	if existing != nil {
		err = retryOnConflict(ctx, 20*time.Second, deleteFn)
	} else {
		err = deleteFn()
	}
	if err != nil {
		if unboundOldReserved {
			rollbackRebindReserved(ctx, client, region, existing.ID, target.ID)
		}
		return nil, i18n.Wrap(err, "net.delete_private_ip_failed")
	}
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

	prep, err := PrepareIPv6(ctx, client, region, subnetID)
	if err != nil {
		return nil, err
	}
	result.Steps = append(result.Steps, prep.Steps...)
	for _, w := range prep.Warnings {
		result.Steps = append(result.Steps, "⚠ "+w)
	}

	// 已经有地址就直接返回，重复调用不该产生第二个地址。
	if existing, err := client.ListIpv6s(ctx, region, vnicID); err == nil && len(existing) > 0 {
		result.Address = existing[0].IPAddress
		result.Steps = append(result.Steps, i18n.M("net.step.has_v6"))
		return result, nil
	}

	ipv6, err := client.CreateIpv6(ctx, region, vnicID)
	if err != nil {
		return nil, i18n.Wrap(err, "net.assign_v6_failed")
	}
	result.Address = ipv6.IPAddress
	result.Steps = append(result.Steps, i18n.M("net.step.v6_assigned", ipv6.IPAddress))
	return result, nil
}

// AddIPv6 给网卡再分配一个 IPv6 地址，不管它已经有几个。
//
// 与 EnableIPv6 的区别只在这里：那个是「确保有」，重复调用不产生新地址；
// 这个是用户明确要多一个。前缀没开通时同样会先补齐。
func AddIPv6(ctx context.Context, client *ociclient.Client, region, vnicID, subnetID string) (*EnableIPv6Result, error) {
	result := &EnableIPv6Result{}

	prep, err := PrepareIPv6(ctx, client, region, subnetID)
	if err != nil {
		return nil, err
	}
	result.Steps = append(result.Steps, prep.Steps...)
	for _, w := range prep.Warnings {
		result.Steps = append(result.Steps, "⚠ "+w)
	}

	ipv6, err := client.CreateIpv6(ctx, region, vnicID)
	if err != nil {
		return nil, i18n.Wrap(err, "net.assign_v6_failed")
	}
	result.Address = ipv6.IPAddress
	result.Steps = append(result.Steps, i18n.M("net.step.v6_assigned", ipv6.IPAddress))
	return result, nil
}

// 规则方向。
const (
	DirIngress = "ingress"
	DirEgress  = "egress"
)

// RuleTemplate 是常用端口的安全规则模板。
type RuleTemplate struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Direction 取 DirIngress / DirEgress。空值按入站处理，兼容旧调用方。
	Direction   string `json:"direction"`
	Protocol    string `json:"protocol"`
	Port        int    `json:"port"`
	Description string `json:"description"`
	// Dangerous 标记会大幅扩大攻击面的规则，UI 上需要显著警示。
	Dangerous bool `json:"dangerous"`
}

// IsEgress 报告该模板是否为出站规则。
func (t RuleTemplate) IsEgress() bool { return t.Direction == DirEgress }

// RuleTemplates 返回安全规则的快捷模板。
//
// 出站那条不是可有可无的补充：OCI 的安全列表是白名单，**没有任何出站规则
// 就等于掐断该子网的全部对外流量**——apt、DNS、下载全断，而实例本身看起来
// 一切正常。面板此前只能删出站不能加，用户删掉之后就卡在这个状态里出不来。
func RuleTemplates() []RuleTemplate {
	return []RuleTemplate{
		{Key: "ssh", Label: "SSH", Direction: DirIngress, Protocol: "6", Port: 22, Description: i18n.M("net.tpl.ssh")},
		{Key: "http", Label: "HTTP", Direction: DirIngress, Protocol: "6", Port: 80, Description: i18n.M("net.tpl.http")},
		{Key: "https", Label: "HTTPS", Direction: DirIngress, Protocol: "6", Port: 443, Description: i18n.M("net.tpl.https")},
		{Key: "icmp", Label: "ICMP", Direction: DirIngress, Protocol: "1", Port: 0, Description: i18n.M("net.tpl.icmp")},
		{Key: "all", Label: i18n.M("net.tpl.all"), Direction: DirIngress, Protocol: "all", Port: 0,
			Description: i18n.M("net.tpl.all.desc"), Dangerous: true},

		// 出站全放行是 OCI 新建安全列表的默认值，也是绝大多数场景该有的样子。
		// 它不标 Dangerous：限制出站属于进阶收紧，而缺了它才是故障。
		{Key: "egress-all", Label: i18n.M("net.tpl.egress_all"), Direction: DirEgress, Protocol: "all", Port: 0,
			Description: i18n.M("net.tpl.egress_all.desc")},
	}
}

// BuildIngressRule 按模板构造一条入站规则。source 留空默认为 0.0.0.0/0。
//
// Port 为 0 表示不限端口，此时不写端口区间：退化成 0-0 会生成一条只对
// 端口 0 生效的规则，看着像加上了、实际什么都没放行。
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
		if tpl.Port > 0 {
			rule.TCPOptions = &ociclient.TCPOptions{
				DestinationPortRange: &ociclient.PortRange{Min: tpl.Port, Max: tpl.Port},
			}
		}
	case "17": // UDP
		if tpl.Port > 0 {
			rule.UDPOptions = &ociclient.UDPOptions{
				DestinationPortRange: &ociclient.PortRange{Min: tpl.Port, Max: tpl.Port},
			}
		}
	case "1": // ICMP
		rule.ICMPOptions = &ociclient.ICMPOptions{Type: 3, Code: intPtr(4)}
	}
	return rule
}

// BuildEgressRule 按模板构造一条出站规则。destination 留空默认为 0.0.0.0/0。
func BuildEgressRule(tpl RuleTemplate, destination, description string) ociclient.EgressSecurityRule {
	if destination == "" {
		destination = "0.0.0.0/0"
	}
	if description == "" {
		description = tpl.Description
	}

	rule := ociclient.EgressSecurityRule{
		Protocol:        tpl.Protocol,
		Destination:     destination,
		DestinationType: "CIDR_BLOCK",
		Description:     description,
	}
	switch tpl.Protocol {
	case "6":
		if tpl.Port > 0 {
			rule.TCPOptions = &ociclient.TCPOptions{
				DestinationPortRange: &ociclient.PortRange{Min: tpl.Port, Max: tpl.Port},
			}
		}
	case "17":
		if tpl.Port > 0 {
			rule.UDPOptions = &ociclient.UDPOptions{
				DestinationPortRange: &ociclient.PortRange{Min: tpl.Port, Max: tpl.Port},
			}
		}
	case "1":
		rule.ICMPOptions = &ociclient.ICMPOptions{Type: 3, Code: intPtr(4)}
	}
	return rule
}

// HasEgress 报告安全列表有没有出站规则。
//
// 没有出站规则的安全列表会掐断该子网的全部对外流量，而实例本身看起来
// 一切正常——这种故障最难排查，界面必须显著提示。
func HasEgress(list ociclient.SecurityList) bool {
	return len(list.EgressSecurityRules) > 0
}

// IsAllowAllRule 判断一条入站规则是否把全部端口开给了整个公网，
// 用于在 UI 上打警示标记、在审计日志里计数。
//
// 只看协议为 all 是不够的：TCP 或 UDP 不限端口（没有端口范围，或范围
// 覆盖 1–65535）对公网的暴露面和全放行几乎一样，而这类规则既能从
// 自定义规则表单建出来，也常见于在 Oracle 控制台里手工建的规则。
func IsAllowAllRule(rule ociclient.IngressSecurityRule) bool {
	if rule.Source != "0.0.0.0/0" && rule.Source != "::/0" {
		return false
	}
	switch strings.ToLower(rule.Protocol) {
	case "all":
		return true
	case "6":
		return rule.TCPOptions == nil || coversAllPorts(rule.TCPOptions.DestinationPortRange)
	case "17":
		return rule.UDPOptions == nil || coversAllPorts(rule.UDPOptions.DestinationPortRange)
	}
	return false
}

// coversAllPorts 报告目的端口范围是否等于不限端口。
//
// 没有范围就是不限；只写了源端口范围时目的端口同样不限，所以这里
// 只看目的端口。
func coversAllPorts(r *ociclient.PortRange) bool {
	return r == nil || (r.Min <= 1 && r.Max >= 65535)
}

func intPtr(v int) *int { return &v }
