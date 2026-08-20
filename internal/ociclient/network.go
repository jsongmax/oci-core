package ociclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// 公网 IP 的生命周期类型。
//
// EPHEMERAL 随 VNIC 存在，删掉就会重新分配一个新的——「更换 IP」正是利用这一点。
// RESERVED 是独立资源，可以在实例之间搬移，删掉才真正释放。
const (
	PublicIPEphemeral = "EPHEMERAL"
	PublicIPReserved  = "RESERVED"
)

// Vcn 是虚拟云网络。
type Vcn struct {
	ID                    string    `json:"id"`
	CompartmentID         string    `json:"compartmentId"`
	DisplayName           string    `json:"displayName"`
	CidrBlock             string    `json:"cidrBlock"`
	CidrBlocks            []string  `json:"cidrBlocks"`
	Ipv6CidrBlocks        []string  `json:"ipv6CidrBlocks"`
	DefaultRouteTableID   string    `json:"defaultRouteTableId"`
	DefaultSecurityListID string    `json:"defaultSecurityListId"`
	DefaultDhcpOptionsID  string    `json:"defaultDhcpOptionsId"`
	DnsLabel              string    `json:"dnsLabel"`
	LifecycleState        string    `json:"lifecycleState"`
	TimeCreated           time.Time `json:"timeCreated"`
	Region                string    `json:"-"`
}

// Subnet 是子网。
type Subnet struct {
	ID                     string    `json:"id"`
	CompartmentID          string    `json:"compartmentId"`
	VcnID                  string    `json:"vcnId"`
	DisplayName            string    `json:"displayName"`
	CidrBlock              string    `json:"cidrBlock"`
	Ipv6CidrBlock          string    `json:"ipv6CidrBlock"`
	AvailabilityDomain     string    `json:"availabilityDomain"`
	RouteTableID           string    `json:"routeTableId"`
	SecurityListIDs        []string  `json:"securityListIds"`
	ProhibitPublicIPOnVnic bool      `json:"prohibitPublicIpOnVnic"`
	DnsLabel               string    `json:"dnsLabel"`
	LifecycleState         string    `json:"lifecycleState"`
	TimeCreated            time.Time `json:"timeCreated"`
}

// InternetGateway 是互联网网关。没有它子网就出不了公网。
type InternetGateway struct {
	ID             string `json:"id"`
	CompartmentID  string `json:"compartmentId"`
	VcnID          string `json:"vcnId"`
	DisplayName    string `json:"displayName"`
	IsEnabled      bool   `json:"isEnabled"`
	LifecycleState string `json:"lifecycleState"`
}

// RouteRule 是一条路由规则。
type RouteRule struct {
	Destination     string `json:"destination"`
	DestinationType string `json:"destinationType,omitempty"`
	NetworkEntityID string `json:"networkEntityId"`
	Description     string `json:"description,omitempty"`
}

// RouteTable 是路由表。
type RouteTable struct {
	ID             string      `json:"id"`
	CompartmentID  string      `json:"compartmentId"`
	VcnID          string      `json:"vcnId"`
	DisplayName    string      `json:"displayName"`
	RouteRules     []RouteRule `json:"routeRules"`
	LifecycleState string      `json:"lifecycleState"`
}

// PortRange 是端口区间。
type PortRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// TCPOptions / UDPOptions 描述协议端口范围。
type TCPOptions struct {
	DestinationPortRange *PortRange `json:"destinationPortRange,omitempty"`
	SourcePortRange      *PortRange `json:"sourcePortRange,omitempty"`
}

type UDPOptions struct {
	DestinationPortRange *PortRange `json:"destinationPortRange,omitempty"`
	SourcePortRange      *PortRange `json:"sourcePortRange,omitempty"`
}

// ICMPOptions 描述 ICMP 类型与代码。
type ICMPOptions struct {
	Type int  `json:"type"`
	Code *int `json:"code,omitempty"`
}

// IngressSecurityRule 是入站规则。
type IngressSecurityRule struct {
	Protocol    string       `json:"protocol"` // "6"=TCP "17"=UDP "1"=ICMP "all"
	Source      string       `json:"source"`
	SourceType  string       `json:"sourceType,omitempty"`
	IsStateless *bool        `json:"isStateless,omitempty"`
	TCPOptions  *TCPOptions  `json:"tcpOptions,omitempty"`
	UDPOptions  *UDPOptions  `json:"udpOptions,omitempty"`
	ICMPOptions *ICMPOptions `json:"icmpOptions,omitempty"`
	Description string       `json:"description,omitempty"`
}

// EgressSecurityRule 是出站规则。
type EgressSecurityRule struct {
	Protocol        string       `json:"protocol"`
	Destination     string       `json:"destination"`
	DestinationType string       `json:"destinationType,omitempty"`
	IsStateless     *bool        `json:"isStateless,omitempty"`
	TCPOptions      *TCPOptions  `json:"tcpOptions,omitempty"`
	UDPOptions      *UDPOptions  `json:"udpOptions,omitempty"`
	ICMPOptions     *ICMPOptions `json:"icmpOptions,omitempty"`
	Description     string       `json:"description,omitempty"`
}

// SecurityList 是子网级的安全规则集合。
type SecurityList struct {
	ID                   string                `json:"id"`
	CompartmentID        string                `json:"compartmentId"`
	VcnID                string                `json:"vcnId"`
	DisplayName          string                `json:"displayName"`
	IngressSecurityRules []IngressSecurityRule `json:"ingressSecurityRules"`
	EgressSecurityRules  []EgressSecurityRule  `json:"egressSecurityRules"`
	LifecycleState       string                `json:"lifecycleState"`
}

// Vnic 是虚拟网卡，公网 IP 与私网 IP 都挂在它上面。
type Vnic struct {
	ID             string   `json:"id"`
	CompartmentID  string   `json:"compartmentId"`
	SubnetID       string   `json:"subnetId"`
	DisplayName    string   `json:"displayName"`
	PrivateIP      string   `json:"privateIp"`
	PublicIP       string   `json:"publicIp"`
	HostnameLabel  string   `json:"hostnameLabel"`
	IsPrimary      bool     `json:"isPrimary"`
	MacAddress     string   `json:"macAddress"`
	LifecycleState string   `json:"lifecycleState"`
	NsgIDs         []string `json:"nsgIds"`
}

// PrivateIP 是 VNIC 上的私网 IP 对象。更换公网 IP 时需要先定位到它。
type PrivateIP struct {
	ID            string `json:"id"`
	VnicID        string `json:"vnicId"`
	SubnetID      string `json:"subnetId"`
	IPAddress     string `json:"ipAddress"`
	IsPrimary     bool   `json:"isPrimary"`
	CompartmentID string `json:"compartmentId"`
}

// PublicIP 是公网 IP 对象。
type PublicIP struct {
	ID               string `json:"id"`
	CompartmentID    string `json:"compartmentId"`
	DisplayName      string `json:"displayName"`
	IPAddress        string `json:"ipAddress"`
	Lifetime         string `json:"lifetime"`
	Scope            string `json:"scope"`
	PrivateIPID      string `json:"privateIpId"`
	LifecycleState   string `json:"lifecycleState"`
	AssignedEntityID string `json:"assignedEntityId"`
}

// Ipv6 是 VNIC 上的 IPv6 地址。
type Ipv6 struct {
	ID             string `json:"id"`
	VnicID         string `json:"vnicId"`
	SubnetID       string `json:"subnetId"`
	IPAddress      string `json:"ipAddress"`
	CompartmentID  string `json:"compartmentId"`
	LifecycleState string `json:"lifecycleState"`
}

// ---- VCN ----

// ListVcns 列出虚拟云网络。
func (c *Client) ListVcns(ctx context.Context, region, compartmentID string) ([]Vcn, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentID)
	query.Set("limit", "100")

	effectiveRegion := orString(region, c.creds.Region)
	// 用空切片而不是 nil：nil 会被序列化成 JSON null，前端拿到 null
	// 再去 .forEach 就直接抛异常。列表接口永远返回列表。
	all := make([]Vcn, 0)
	err := listPages(ctx, c, Request{
		Method: http.MethodGet, Service: ServiceCore, Path: "/vcns",
		Region: region, Query: query,
	}, 20, func(page []byte) error {
		var batch []Vcn
		if err := json.Unmarshal(page, &batch); err != nil {
			return err
		}
		for i := range batch {
			batch[i].Region = effectiveRegion
		}
		all = append(all, batch...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return all, nil
}

// CreateVcnRequest 是创建 VCN 的请求体。
type CreateVcnRequest struct {
	CompartmentID string `json:"compartmentId"`
	CidrBlock     string `json:"cidrBlock"`
	DisplayName   string `json:"displayName,omitempty"`
	DnsLabel      string `json:"dnsLabel,omitempty"`
	IsIpv6Enabled bool   `json:"isIpv6Enabled,omitempty"`
}

// CreateVcn 创建虚拟云网络。
func (c *Client) CreateVcn(ctx context.Context, region string, req CreateVcnRequest) (*Vcn, error) {
	if req.CompartmentID == "" {
		req.CompartmentID = c.creds.TenancyOCID
	}
	var out Vcn
	_, err := c.Do(ctx, Request{
		Method: http.MethodPost, Service: ServiceCore, Path: "/vcns",
		Region: region, Body: req,
	}, &out)
	if err != nil {
		return nil, err
	}
	out.Region = orString(region, c.creds.Region)
	return &out, nil
}

// GetVcn 取回单个 VCN。
func (c *Client) GetVcn(ctx context.Context, region, vcnID string) (*Vcn, error) {
	var out Vcn
	_, err := c.Do(ctx, Request{
		Method: http.MethodGet, Service: ServiceCore,
		Path: "/vcns/" + url.PathEscape(vcnID), Region: region,
	}, &out)
	if err != nil {
		return nil, err
	}
	out.Region = orString(region, c.creds.Region)
	return &out, nil
}

// AddVcnIpv6Cidr 为 VCN 分配一段 Oracle 提供的 IPv6 前缀。
// 这是启用 IPv6 的第一步，之后还需要给子网分配前缀、再给 VNIC 分配地址。
func (c *Client) AddVcnIpv6Cidr(ctx context.Context, region, vcnID string) error {
	_, err := c.Do(ctx, Request{
		Method: http.MethodPost, Service: ServiceCore,
		Path:   "/vcns/" + url.PathEscape(vcnID) + "/actions/addIpv6Cidr",
		Region: region,
		Body:   map[string]any{"isOracleGuaAllocationEnabled": true},
	}, nil)
	return err
}

// ---- 子网 ----

// ListSubnets 列出子网。vcnID 可留空表示不限。
func (c *Client) ListSubnets(ctx context.Context, region, compartmentID, vcnID string) ([]Subnet, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentID)
	query.Set("limit", "100")
	if vcnID != "" {
		query.Set("vcnId", vcnID)
	}

	// 用空切片而不是 nil：nil 会被序列化成 JSON null，前端拿到 null
	// 再去 .forEach 就直接抛异常。列表接口永远返回列表。
	all := make([]Subnet, 0)
	err := listPages(ctx, c, Request{
		Method: http.MethodGet, Service: ServiceCore, Path: "/subnets",
		Region: region, Query: query,
	}, 20, func(page []byte) error {
		var batch []Subnet
		if err := json.Unmarshal(page, &batch); err != nil {
			return err
		}
		all = append(all, batch...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return all, nil
}

// CreateSubnetRequest 是创建子网的请求体。
type CreateSubnetRequest struct {
	CompartmentID          string   `json:"compartmentId"`
	VcnID                  string   `json:"vcnId"`
	CidrBlock              string   `json:"cidrBlock"`
	DisplayName            string   `json:"displayName,omitempty"`
	DnsLabel               string   `json:"dnsLabel,omitempty"`
	AvailabilityDomain     string   `json:"availabilityDomain,omitempty"`
	RouteTableID           string   `json:"routeTableId,omitempty"`
	SecurityListIDs        []string `json:"securityListIds,omitempty"`
	ProhibitPublicIPOnVnic bool     `json:"prohibitPublicIpOnVnic,omitempty"`
}

// CreateSubnet 创建子网。留空 AvailabilityDomain 即为区域级子网（推荐）。
func (c *Client) CreateSubnet(ctx context.Context, region string, req CreateSubnetRequest) (*Subnet, error) {
	if req.CompartmentID == "" {
		req.CompartmentID = c.creds.TenancyOCID
	}
	var out Subnet
	_, err := c.Do(ctx, Request{
		Method: http.MethodPost, Service: ServiceCore, Path: "/subnets",
		Region: region, Body: req,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// AddSubnetIpv6Cidr 为子网分配一段 IPv6 前缀。
// 必须先给 VCN 分配过 IPv6 前缀，否则会失败。
func (c *Client) AddSubnetIpv6Cidr(ctx context.Context, region, subnetID, ipv6CidrBlock string) error {
	body := map[string]any{}
	if ipv6CidrBlock != "" {
		body["ipv6CidrBlock"] = ipv6CidrBlock
	}
	_, err := c.Do(ctx, Request{
		Method: http.MethodPost, Service: ServiceCore,
		Path:   "/subnets/" + url.PathEscape(subnetID) + "/actions/addIpv6Cidr",
		Region: region, Body: body,
	}, nil)
	return err
}

// GetSubnet 取回单个子网。
func (c *Client) GetSubnet(ctx context.Context, region, subnetID string) (*Subnet, error) {
	var out Subnet
	_, err := c.Do(ctx, Request{
		Method: http.MethodGet, Service: ServiceCore,
		Path: "/subnets/" + url.PathEscape(subnetID), Region: region,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- 网关与路由 ----

// ListInternetGateways 列出互联网网关。
func (c *Client) ListInternetGateways(ctx context.Context, region, compartmentID, vcnID string) ([]InternetGateway, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentID)
	query.Set("limit", "100")
	if vcnID != "" {
		query.Set("vcnId", vcnID)
	}

	var out []InternetGateway
	_, err := c.Do(ctx, Request{
		Method: http.MethodGet, Service: ServiceCore, Path: "/internetGateways",
		Region: region, Query: query,
	}, &out)
	return out, err
}

// CreateInternetGateway 创建互联网网关。
func (c *Client) CreateInternetGateway(ctx context.Context, region, compartmentID, vcnID, displayName string) (*InternetGateway, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	var out InternetGateway
	_, err := c.Do(ctx, Request{
		Method: http.MethodPost, Service: ServiceCore, Path: "/internetGateways",
		Region: region,
		Body: map[string]any{
			"compartmentId": compartmentID,
			"vcnId":         vcnID,
			"isEnabled":     true,
			"displayName":   displayName,
		},
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRouteTable 取回路由表。
func (c *Client) GetRouteTable(ctx context.Context, region, routeTableID string) (*RouteTable, error) {
	var out RouteTable
	_, err := c.Do(ctx, Request{
		Method: http.MethodGet, Service: ServiceCore,
		Path: "/routeTables/" + url.PathEscape(routeTableID), Region: region,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateRouteTable 覆盖写路由规则。OCI 的语义是整体替换，不是增量追加。
func (c *Client) UpdateRouteTable(ctx context.Context, region, routeTableID string, rules []RouteRule) (*RouteTable, error) {
	var out RouteTable
	_, err := c.Do(ctx, Request{
		Method: http.MethodPut, Service: ServiceCore,
		Path: "/routeTables/" + url.PathEscape(routeTableID), Region: region,
		Body: map[string]any{"routeRules": rules},
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- 安全规则 ----

// ListSecurityLists 列出安全列表。
func (c *Client) ListSecurityLists(ctx context.Context, region, compartmentID, vcnID string) ([]SecurityList, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentID)
	query.Set("limit", "100")
	if vcnID != "" {
		query.Set("vcnId", vcnID)
	}

	// 用空切片而不是 nil：nil 会被序列化成 JSON null，前端拿到 null
	// 再去 .forEach 就直接抛异常。列表接口永远返回列表。
	all := make([]SecurityList, 0)
	err := listPages(ctx, c, Request{
		Method: http.MethodGet, Service: ServiceCore, Path: "/securityLists",
		Region: region, Query: query,
	}, 20, func(page []byte) error {
		var batch []SecurityList
		if err := json.Unmarshal(page, &batch); err != nil {
			return err
		}
		all = append(all, batch...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return all, nil
}

// GetSecurityList 取回单个安全列表。
func (c *Client) GetSecurityList(ctx context.Context, region, securityListID string) (*SecurityList, error) {
	var out SecurityList
	_, err := c.Do(ctx, Request{
		Method: http.MethodGet, Service: ServiceCore,
		Path: "/securityLists/" + url.PathEscape(securityListID), Region: region,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateSecurityList 覆盖写安全规则。
//
// 与路由表一样是整体替换语义：调用方必须先 Get 拿到完整规则集，
// 在其基础上修改后再整体提交，否则会静默丢掉未提交的规则。
func (c *Client) UpdateSecurityList(ctx context.Context, region, securityListID string,
	ingress []IngressSecurityRule, egress []EgressSecurityRule) (*SecurityList, error) {

	// 显式传空切片而非 nil，确保"删光所有规则"能真正生效
	// （nil 会被 omitempty 吃掉，变成不修改）。
	if ingress == nil {
		ingress = []IngressSecurityRule{}
	}
	if egress == nil {
		egress = []EgressSecurityRule{}
	}

	var out SecurityList
	_, err := c.Do(ctx, Request{
		Method: http.MethodPut, Service: ServiceCore,
		Path: "/securityLists/" + url.PathEscape(securityListID), Region: region,
		Body: map[string]any{
			"ingressSecurityRules": ingress,
			"egressSecurityRules":  egress,
		},
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- VNIC 与 IP ----

// GetVnic 取回虚拟网卡。
func (c *Client) GetVnic(ctx context.Context, region, vnicID string) (*Vnic, error) {
	var out Vnic
	_, err := c.Do(ctx, Request{
		Method: http.MethodGet, Service: ServiceCore,
		Path: "/vnics/" + url.PathEscape(vnicID), Region: region,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListPrivateIPs 列出 VNIC 上的私网 IP。
func (c *Client) ListPrivateIPs(ctx context.Context, region, vnicID string) ([]PrivateIP, error) {
	query := url.Values{}
	query.Set("vnicId", vnicID)
	query.Set("limit", "100")

	var out []PrivateIP
	_, err := c.Do(ctx, Request{
		Method: http.MethodGet, Service: ServiceCore, Path: "/privateIps",
		Region: region, Query: query,
	}, &out)
	return out, err
}

// GetPublicIPByPrivateIP 查询某个私网 IP 当前绑定的公网 IP。
// 未绑定时 OCI 返回 404，这里转成 (nil, nil) 让调用方按"没有公网 IP"处理。
func (c *Client) GetPublicIPByPrivateIP(ctx context.Context, region, privateIPID string) (*PublicIP, error) {
	var out PublicIP
	_, err := c.Do(ctx, Request{
		Method: http.MethodPost, Service: ServiceCore,
		Path: "/publicIps/actions/getByPrivateIpId", Region: region,
		Body: map[string]any{"privateIpId": privateIPID},
	}, &out)
	if err != nil {
		if apiErr, ok := AsAPIError(err); ok && apiErr.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// ListPublicIPs 列出公网 IP。scope 为 REGION 时列出保留 IP，
// AVAILABILITY_DOMAIN 时列出临时 IP（需同时给出 availabilityDomain）。
func (c *Client) ListPublicIPs(ctx context.Context, region, compartmentID, scope, availabilityDomain string) ([]PublicIP, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentID)
	query.Set("scope", scope)
	query.Set("limit", "100")
	if availabilityDomain != "" {
		query.Set("availabilityDomain", availabilityDomain)
	}

	// 用空切片而不是 nil：nil 会被序列化成 JSON null，前端拿到 null
	// 再去 .forEach 就直接抛异常。列表接口永远返回列表。
	all := make([]PublicIP, 0)
	err := listPages(ctx, c, Request{
		Method: http.MethodGet, Service: ServiceCore, Path: "/publicIps",
		Region: region, Query: query,
	}, 20, func(page []byte) error {
		var batch []PublicIP
		if err := json.Unmarshal(page, &batch); err != nil {
			return err
		}
		all = append(all, batch...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return all, nil
}

// CreatePublicIP 分配公网 IP 并绑定到私网 IP。
// lifetime 传 EPHEMERAL 时 privateIPID 必填。
func (c *Client) CreatePublicIP(ctx context.Context, region, compartmentID, lifetime, privateIPID, displayName string) (*PublicIP, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	body := map[string]any{
		"compartmentId": compartmentID,
		"lifetime":      lifetime,
	}
	if privateIPID != "" {
		body["privateIpId"] = privateIPID
	}
	if displayName != "" {
		body["displayName"] = displayName
	}

	var out PublicIP
	_, err := c.Do(ctx, Request{
		Method: http.MethodPost, Service: ServiceCore, Path: "/publicIps",
		Region: region, Body: body,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeletePublicIP 删除公网 IP。
//
// 对 EPHEMERAL 类型来说这就是「更换 IP」的实现：删掉之后 OCI 会自动分配一个新的。
// 原 IP 不可找回，SSH 连接会中断——UI 必须在操作前明确提示。
func (c *Client) DeletePublicIP(ctx context.Context, region, publicIPID string) error {
	_, err := c.Do(ctx, Request{
		Method: http.MethodDelete, Service: ServiceCore,
		Path: "/publicIps/" + url.PathEscape(publicIPID), Region: region,
	}, nil)
	return err
}

// ListIpv6s 列出 VNIC 上的 IPv6 地址。
func (c *Client) ListIpv6s(ctx context.Context, region, vnicID string) ([]Ipv6, error) {
	query := url.Values{}
	query.Set("vnicId", vnicID)
	query.Set("limit", "100")

	var out []Ipv6
	_, err := c.Do(ctx, Request{
		Method: http.MethodGet, Service: ServiceCore, Path: "/ipv6",
		Region: region, Query: query,
	}, &out)
	return out, err
}

// CreateIpv6 为 VNIC 分配一个 IPv6 地址。
func (c *Client) CreateIpv6(ctx context.Context, region, vnicID string) (*Ipv6, error) {
	var out Ipv6
	_, err := c.Do(ctx, Request{
		Method: http.MethodPost, Service: ServiceCore, Path: "/ipv6",
		Region: region, Body: map[string]any{"vnicId": vnicID},
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteIpv6 删除 IPv6 地址。
func (c *Client) DeleteIpv6(ctx context.Context, region, ipv6ID string) error {
	_, err := c.Do(ctx, Request{
		Method: http.MethodDelete, Service: ServiceCore,
		Path: "/ipv6/" + url.PathEscape(ipv6ID), Region: region,
	}, nil)
	return err
}
