// Package permsvc 推算一个 API 用户能用本工具的哪些功能。
//
// OCI 没有"查询某用户实际权限"的官方接口，这里的结论来自两条路：
//
//  1. 解析策略：读出租户的策略语句与用户所在的组，按官方策略参考推算。
//     能区分 use 与 manage，但读策略本身需要 inspect policies / groups，
//     精简权限的用户多半读不到。
//  2. 只读探测：真去调几个只读接口。结果可靠，但只能证明"能读"，
//     分不出 use 与 manage。
//
// 因此每项结论都有三态：可用 / 缺失 / 无法判断。只有确定缺失时才报缺失——
// 把"读不到策略"误报成"缺权限"会让用户去改本来没问题的策略。
//
// 权限名、所属资源类型与最低动词全部摘自 Oracle 官方文档：
// https://docs.oracle.com/en-us/iaas/Content/Identity/Reference/corepolicyreference.htm
package permsvc

// Verb 是策略动词，按权限从小到大排列。高动词包含低动词的全部权限。
type Verb int

const (
	VerbInspect Verb = iota + 1
	VerbRead
	VerbUse
	VerbManage
)

var verbNames = map[Verb]string{
	VerbInspect: "inspect", VerbRead: "read", VerbUse: "use", VerbManage: "manage",
}

func (v Verb) String() string { return verbNames[v] }

func parseVerb(s string) (Verb, bool) {
	for v, name := range verbNames {
		if name == s {
			return v, true
		}
	}
	return 0, false
}

// Need 是一项权限要求：某个单项资源类型上的最低动词。
// Perm 是官方文档里的权限名，用于匹配 `{PERM_A, PERM_B}` 形式的语句，也用于展示。
type Need struct {
	Perm string `json:"perm"`
	Type string `json:"type"`
	Verb Verb   `json:"-"`
	// TenancyOnly 表示这项权限只能授予在租户级（策略写 in tenancy）。
	// 容量报告与用量查询都以租户根 OCID 为区间发请求。
	TenancyOnly bool `json:"-"`
}

// perm 按官方"Details for Verb + Resource-Type Combinations"表登记：
// 权限名 → 单项资源类型 + 包含它的最低动词。
func perm(name, typ string, v Verb) Need { return Need{Perm: name, Type: typ, Verb: v} }

var (
	pInstanceInspect   = perm("INSTANCE_INSPECT", "instances", VerbInspect)
	pInstanceRead      = perm("INSTANCE_READ", "instances", VerbRead)
	pInstancePower     = perm("INSTANCE_POWER_ACTIONS", "instances", VerbUse)
	pInstanceUpdate    = perm("INSTANCE_UPDATE", "instances", VerbUse)
	pInstanceCreate    = perm("INSTANCE_CREATE", "instances", VerbManage)
	pInstanceDelete    = perm("INSTANCE_DELETE", "instances", VerbManage)
	pInstanceAttachVN  = perm("INSTANCE_ATTACH_SECONDARY_VNIC", "instances", VerbManage)
	pInstanceDetachVN  = perm("INSTANCE_DETACH_SECONDARY_VNIC", "instances", VerbManage)
	pInstanceAttachVol = perm("INSTANCE_ATTACH_VOLUME", "instances", VerbUse)
	pInstanceDetachVol = perm("INSTANCE_DETACH_VOLUME", "instances", VerbUse)
	pImageInspect      = perm("INSTANCE_IMAGE_INSPECT", "instance-images", VerbInspect)
	pImageRead         = perm("INSTANCE_IMAGE_READ", "instance-images", VerbRead)
	pConsoleCreate     = perm("INSTANCE_CONSOLE_CONNECTION_CREATE", "instance-console-connection", VerbManage)
	pConsoleDelete     = perm("INSTANCE_CONSOLE_CONNECTION_DELETE", "instance-console-connection", VerbManage)

	pVnicAttachmentRead = perm("VNIC_ATTACHMENT_READ", "vnic-attachments", VerbInspect)
	pVnicRead           = perm("VNIC_READ", "vnics", VerbInspect)
	pVnicCreate         = perm("VNIC_CREATE", "vnics", VerbUse)
	pVnicDelete         = perm("VNIC_DELETE", "vnics", VerbUse)
	pVnicAttach         = perm("VNIC_ATTACH", "vnics", VerbUse)
	pVnicDetach         = perm("VNIC_DETACH", "vnics", VerbUse)
	pSubnetRead         = perm("SUBNET_READ", "subnets", VerbInspect)
	pSubnetAttach       = perm("SUBNET_ATTACH", "subnets", VerbUse)
	pSubnetDetach       = perm("SUBNET_DETACH", "subnets", VerbUse)
	pVcnRead            = perm("VCN_READ", "vcns", VerbInspect)
	pIGWRead            = perm("INTERNET_GATEWAY_READ", "internet-gateways", VerbInspect)
	pRouteTableRead     = perm("ROUTE_TABLE_READ", "route-tables", VerbInspect)
	pSecListRead        = perm("SECURITY_LIST_READ", "security-lists", VerbInspect)
	pSecListUpdate      = perm("SECURITY_LIST_UPDATE", "security-lists", VerbManage)
	pPrivateIPRead      = perm("PRIVATE_IP_READ", "private-ips", VerbInspect)
	pPrivateIPAssignPub = perm("PRIVATE_IP_ASSIGN_PUBLIC_IP", "private-ips", VerbUse)
	pPrivateIPUnassign  = perm("PRIVATE_IP_UNASSIGN_PUBLIC_IP", "private-ips", VerbUse)
	pIpv6Read           = perm("IPV6_READ", "ipv6s", VerbRead)
	pIpv6Create         = perm("IPV6_CREATE", "ipv6s", VerbManage)
	pIpv6Delete         = perm("IPV6_DELETE", "ipv6s", VerbManage)

	pVolumeInspect   = perm("VOLUME_INSPECT", "volumes", VerbInspect)
	pVolumeUpdate    = perm("VOLUME_UPDATE", "volumes", VerbUse)
	pVolumeWrite     = perm("VOLUME_WRITE", "volumes", VerbUse)
	pVolumeDelete    = perm("VOLUME_DELETE", "volumes", VerbManage)
	pVolAttachCreate = perm("VOLUME_ATTACHMENT_CREATE", "volume-attachments", VerbManage)
	pVolAttachDelete = perm("VOLUME_ATTACHMENT_DELETE", "volume-attachments", VerbManage)

	pCapacityReport = Need{Perm: "COMPUTE_CAPACITY_REPORT_CREATE", Type: "compute-capacity-reports", Verb: VerbManage, TenancyOnly: true}

	// 以下几项在官方文档里只以"动词 + 资源类型"给出，没有逐项权限表，Perm 留作展示用。
	// 配额：ListLimitValues 需要 inspect limits；GetResourceAvailability 需要 read resource-availability。
	pLimitsInspect   = Need{Perm: "inspect limits", Type: "limits", Verb: VerbInspect, TenancyOnly: true}
	pAvailRead       = Need{Perm: "read resource-availability", Type: "resource-availability", Verb: VerbRead}
	pMetricRead      = perm("METRIC_READ", "metrics", VerbRead)
	pUsageReportRead = Need{Perm: "read usage-report", Type: "usage-report", Verb: VerbRead, TenancyOnly: true}
)

// families 是聚合资源类型包含的单项资源类型（官方文档"Resource-Types"一节）。
// all-resources 覆盖全部类型，单独处理。
var families = map[string][]string{
	"instance-family": {
		"app-catalog-listing", "console-histories", "instances", "instance-console-connection",
		"instance-images", "volume-attachments",
	},
	"virtual-network-family": {
		"byoiprange", "capture-filters", "cpes", "cross-connect-groups", "cross-connects",
		"dhcp-options", "drg-attachments", "drg-object", "drg-route-distributions",
		"drg-route-tables", "internet-gateways", "ipsec-connections", "ipv6s", "ipam",
		"local-peering-gateways", "nat-gateways", "network-security-groups", "private-ips",
		"publicippool", "public-ips", "remote-peering-connections", "route-tables",
		"security-lists", "service-gateways", "subnets", "vcns", "virtual-circuits", "vlans",
		"vnic-attachments", "vnics", "vtaps",
	},
	"volume-family": {
		"volumes", "volume-attachments", "volume-backups", "boot-volume-backups",
		"backup-policies", "backup-policy-assignments", "volume-groups", "volume-group-backups",
	},
}

// suggestFamily 决定修复建议里写哪个资源类型：能写聚合类型就写聚合类型，
// 与"权限自检"页的策略示例保持一致；独立资源类型原样写。
var suggestFamily = map[string]string{
	"instances": "instance-family", "instance-images": "instance-family",
	"instance-console-connection": "instance-family",
	"volumes":                     "volume-family", "volume-attachments": "volume-family",
}

func init() {
	for _, t := range families["virtual-network-family"] {
		suggestFamily[t] = "virtual-network-family"
	}
}

// covers 判断策略里写的资源类型 written 是否覆盖单项类型 typ。
func covers(written, typ string) bool {
	if written == typ || written == "all-resources" {
		return true
	}
	for _, member := range families[written] {
		if member == typ {
			return true
		}
	}
	return false
}

// Capability 是本工具的一项功能及其所需权限。
// Needs 摘自官方文档"Permissions Required for Each API Operation"表，
// 列的是该功能调用的每个 API 的无条件权限。
type Capability struct {
	Key      string
	Category string // compute / network / storage / account
	Needs    []Need
	// Probe 非空时，表示有一个只读探测能直接证明（或证伪）其中某些权限。
	Probe string
}

var capabilities = []Capability{
	// ListInstances / GetInstance：INSTANCE_READ；ListVnicAttachments：VNIC_ATTACHMENT_READ + INSTANCE_INSPECT；GetVnic：VNIC_READ
	{Key: "instance.view", Category: "compute", Probe: probeInstances,
		Needs: []Need{pInstanceRead, pInstanceInspect, pVnicAttachmentRead, pVnicRead}},
	// InstanceAction：INSTANCE_POWER_ACTIONS
	{Key: "instance.power", Category: "compute", Needs: []Need{pInstancePower}},
	// UpdateInstance：INSTANCE_UPDATE
	{Key: "instance.update", Category: "compute", Needs: []Need{pInstanceUpdate}},
	// LaunchInstance：INSTANCE_CREATE + INSTANCE_IMAGE_READ + VNIC_CREATE + VNIC_ATTACH + SUBNET_ATTACH；
	// ListShapes：INSTANCE_INSPECT；ListImages：INSTANCE_IMAGE_INSPECT
	{Key: "instance.launch", Category: "compute",
		Needs: []Need{pInstanceCreate, pImageRead, pImageInspect, pVnicCreate, pVnicAttach, pSubnetAttach, pInstanceInspect}},
	// TerminateInstance：INSTANCE_DELETE + VNIC_DELETE + SUBNET_DETACH
	{Key: "instance.terminate", Category: "compute", Needs: []Need{pInstanceDelete, pVnicDelete, pSubnetDetach}},
	// CreateInstanceConsoleConnection：INSTANCE_CONSOLE_CONNECTION_CREATE + INSTANCE_READ；Delete：…_DELETE
	{Key: "instance.console", Category: "compute", Needs: []Need{pConsoleCreate, pConsoleDelete, pInstanceRead}},
	// CreateComputeCapacityReport：COMPUTE_CAPACITY_REPORT_CREATE
	{Key: "capacity.report", Category: "compute", Probe: probeCapacity, Needs: []Need{pCapacityReport}},

	// ListVcns：VCN_READ；ListSubnets：SUBNET_READ；ListSecurityLists：SECURITY_LIST_READ；
	// ListInternetGateways：INTERNET_GATEWAY_READ；GetRouteTable：ROUTE_TABLE_READ
	{Key: "network.view", Category: "network", Probe: probeVcns,
		Needs: []Need{pVcnRead, pSubnetRead, pSecListRead, pIGWRead, pRouteTableRead}},
	// UpdateSecurityList：SECURITY_LIST_UPDATE（仅 manage）
	{Key: "network.rules", Category: "network", Needs: []Need{pSecListUpdate}},
	// 临时公网 IP：CreatePublicIp / DeletePublicIp 需要 PRIVATE_IP_ASSIGN_PUBLIC_IP / PRIVATE_IP_UNASSIGN_PUBLIC_IP；
	// ListPrivateIps / GetPublicIpByPrivateIpId：PRIVATE_IP_READ
	{Key: "network.publicip", Category: "network", Needs: []Need{pPrivateIPRead, pPrivateIPAssignPub, pPrivateIPUnassign}},
	// CreateIpv6：IPV6_CREATE + SUBNET_ATTACH + VNIC_ASSIGN；DeleteIpv6：IPV6_DELETE + SUBNET_DETACH + VNIC_UNASSIGN；
	// ListIpv6s：IPV6_READ。VNIC_ASSIGN / VNIC_UNASSIGN 在官方动词表里没有归属，无法据此判断，不列入。
	{Key: "network.ipv6", Category: "network",
		Needs: []Need{pIpv6Read, pIpv6Create, pIpv6Delete, pSubnetAttach, pSubnetDetach}},
	// AttachVnic：INSTANCE_ATTACH_SECONDARY_VNIC + VNIC_ATTACH + VNIC_CREATE + SUBNET_ATTACH；DetachVnic 对应 DETACH/DELETE
	{Key: "network.vnic", Category: "network",
		Needs: []Need{pInstanceAttachVN, pInstanceDetachVN, pVnicCreate, pVnicDelete, pVnicAttach, pVnicDetach, pSubnetAttach, pSubnetDetach}},

	// ListBootVolumes / ListVolumes / GetBootVolume：VOLUME_INSPECT
	{Key: "storage.view", Category: "storage", Probe: probeVolumes, Needs: []Need{pVolumeInspect}},
	// UpdateBootVolume / UpdateVolume：VOLUME_UPDATE
	{Key: "storage.update", Category: "storage", Needs: []Need{pVolumeUpdate}},
	// AttachVolume：VOLUME_ATTACHMENT_CREATE + VOLUME_WRITE + INSTANCE_ATTACH_VOLUME；DetachVolume 对应 DELETE / DETACH
	{Key: "storage.attach", Category: "storage",
		Needs: []Need{pVolAttachCreate, pVolAttachDelete, pVolumeWrite, pInstanceAttachVol, pInstanceDetachVol}},
	// DeleteBootVolume / DeleteVolume：VOLUME_DELETE
	{Key: "storage.delete", Category: "storage", Needs: []Need{pVolumeDelete}},

	{Key: "account.quota", Category: "account", Probe: probeLimits, Needs: []Need{pLimitsInspect, pAvailRead}},
	// SummarizeMetricsData：METRIC_INSPECT + METRIC_READ（read metrics）
	{Key: "account.metrics", Category: "account", Needs: []Need{pMetricRead}},
	// Usage API：read usage-report in tenancy
	{Key: "account.billing", Category: "account", Probe: probeUsage, Needs: []Need{pUsageReportRead}},
}
