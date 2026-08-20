package ociclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// 实例生命周期状态。与前端 StateBadge 的六态一一对应。
//
// 关键区分：稳定态（RUNNING / STOPPED）与过渡态。UI 上只有过渡态带动画，
// 因此后端也必须严格按这个划分回报状态，不能把过渡态直接报成终态。
const (
	LifecycleProvisioning = "PROVISIONING"
	LifecycleRunning      = "RUNNING"
	LifecycleStarting     = "STARTING"
	LifecycleStopping     = "STOPPING"
	LifecycleStopped      = "STOPPED"
	LifecycleCreatingImg  = "CREATING_IMAGE"
	LifecycleTerminating  = "TERMINATING"
	LifecycleTerminated   = "TERMINATED"
)

// IsTransitionalState 报告状态是否处于转换过程中。
func IsTransitionalState(state string) bool {
	switch state {
	case LifecycleProvisioning, LifecycleStarting, LifecycleStopping,
		LifecycleTerminating, LifecycleCreatingImg:
		return true
	}
	return false
}

// 实例操作。SOFT 系列会通知客户机正常关闭，非 SOFT 系列相当于直接拔电源。
const (
	ActionStart     = "START"
	ActionStop      = "STOP"      // 强制关机
	ActionSoftStop  = "SOFTSTOP"  // 正常关机
	ActionReset     = "RESET"     // 强制重启
	ActionSoftReset = "SOFTRESET" // 正常重启
)

// ShapeConfig 是弹性规格的 OCPU 与内存配置。
//
// A1.Flex 每 OCPU 最多配 6 GB 内存。永久免费额度见 freetier.go。
// 这个约束在 ValidateShapeConfig 里强制执行。
type ShapeConfig struct {
	Ocpus                     float32 `json:"ocpus,omitempty"`
	MemoryInGBs               float32 `json:"memoryInGBs,omitempty"`
	BaselineOcpuUtilization   string  `json:"baselineOcpuUtilization,omitempty"`
	ProcessorDescription      string  `json:"processorDescription,omitempty"`
	NetworkingBandwidthInGbps float32 `json:"networkingBandwidthInGbps,omitempty"`
	MaxVnicAttachments        int     `json:"maxVnicAttachments,omitempty"`
}

// Instance 是一台计算实例。
type Instance struct {
	ID                 string            `json:"id"`
	CompartmentID      string            `json:"compartmentId"`
	DisplayName        string            `json:"displayName"`
	AvailabilityDomain string            `json:"availabilityDomain"`
	FaultDomain        string            `json:"faultDomain"`
	Shape              string            `json:"shape"`
	ShapeConfig        *ShapeConfig      `json:"shapeConfig"`
	LifecycleState     string            `json:"lifecycleState"`
	Region             string            `json:"region"`
	ImageID            string            `json:"imageId"`
	TimeCreated        time.Time         `json:"timeCreated"`
	Metadata           map[string]string `json:"metadata"`
	FreeformTags       map[string]string `json:"freeformTags"`
	SourceDetails      *SourceDetails    `json:"sourceDetails"`
}

// SourceDetails 描述实例的启动源（镜像或已有引导卷）。
type SourceDetails struct {
	SourceType          string `json:"sourceType"`
	ImageID             string `json:"imageId,omitempty"`
	BootVolumeID        string `json:"bootVolumeId,omitempty"`
	BootVolumeSizeInGBs int64  `json:"bootVolumeSizeInGBs,omitempty"`
}

// Shape 是可用的实例规格。
type Shape struct {
	Shape                     string   `json:"shape"`
	ProcessorDescription      string   `json:"processorDescription"`
	Ocpus                     float32  `json:"ocpus"`
	MemoryInGBs               float32  `json:"memoryInGBs"`
	NetworkingBandwidthInGbps float32  `json:"networkingBandwidthInGbps"`
	MaxVnicAttachments        int      `json:"maxVnicAttachments"`
	IsFlexible                bool     `json:"isFlexible"`
	OcpuOptions               *MinMax  `json:"ocpuOptions"`
	MemoryOptions             *MemOpts `json:"memoryOptions"`
	BillingType               string   `json:"billingType"`
}

// MinMax 是弹性规格的 OCPU 取值范围。
type MinMax struct {
	Min float32 `json:"min"`
	Max float32 `json:"max"`
}

// MemOpts 是弹性规格的内存取值范围，含每 OCPU 的内存上下限。
type MemOpts struct {
	MinInGBs            float32 `json:"minInGBs"`
	MaxInGBs            float32 `json:"maxInGBs"`
	MinPerOcpuInGBs     float32 `json:"minPerOcpuInGBs"`
	MaxPerOcpuInGBs     float32 `json:"maxPerOcpuInGBs"`
	DefaultPerOcpuInGBs float32 `json:"defaultPerOcpuInGBs"`
}

// Image 是操作系统镜像。
type Image struct {
	ID                     string    `json:"id"`
	CompartmentID          string    `json:"compartmentId"`
	DisplayName            string    `json:"displayName"`
	OperatingSystem        string    `json:"operatingSystem"`
	OperatingSystemVersion string    `json:"operatingSystemVersion"`
	LifecycleState         string    `json:"lifecycleState"`
	TimeCreated            time.Time `json:"timeCreated"`
	SizeInMBs              int64     `json:"sizeInMBs"`
}

// VnicAttachment 把 VNIC 关联到实例。取实例的公网 IP 必须先经过它。
type VnicAttachment struct {
	ID                 string `json:"id"`
	InstanceID         string `json:"instanceId"`
	VnicID             string `json:"vnicId"`
	SubnetID           string `json:"subnetId"`
	AvailabilityDomain string `json:"availabilityDomain"`
	LifecycleState     string `json:"lifecycleState"`
	NicIndex           int    `json:"nicIndex"`
	DisplayName        string `json:"displayName"`
}

// ListInstancesOptions 是实例列表的过滤条件。
type ListInstancesOptions struct {
	CompartmentID      string
	Region             string
	AvailabilityDomain string
	LifecycleState     string
	// Limit 是单页条数，留空用 100。
	Limit int
}

// ListInstances 列出分区下的实例，自动翻页取全量。
func (c *Client) ListInstances(ctx context.Context, opts ListInstancesOptions) ([]Instance, error) {
	compartment := opts.CompartmentID
	if compartment == "" {
		compartment = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartment)
	query.Set("limit", strconv.Itoa(orDefault(opts.Limit, 100)))
	if opts.AvailabilityDomain != "" {
		query.Set("availabilityDomain", opts.AvailabilityDomain)
	}
	if opts.LifecycleState != "" {
		query.Set("lifecycleState", opts.LifecycleState)
	}

	region := opts.Region
	if region == "" {
		region = c.creds.Region
	}

	// 用空切片而不是 nil：nil 会被序列化成 JSON null，前端拿到 null
	// 再去 .forEach 就直接抛异常。列表接口永远返回列表。
	all := make([]Instance, 0)
	err := listPages(ctx, c, Request{
		Method:  http.MethodGet,
		Service: ServiceCore,
		Path:    "/instances",
		Region:  region,
		Query:   query,
	}, 50, func(page []byte) error {
		var batch []Instance
		if err := json.Unmarshal(page, &batch); err != nil {
			return err
		}
		// 响应体里没有 region 字段，但上层做跨账号聚合时必须知道它。
		for i := range batch {
			batch[i].Region = region
		}
		all = append(all, batch...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return all, nil
}

// GetInstance 取回单台实例。生命周期轮询靠的就是它。
func (c *Client) GetInstance(ctx context.Context, region, instanceID string) (*Instance, error) {
	var out Instance
	_, err := c.Do(ctx, Request{
		Method:  http.MethodGet,
		Service: ServiceCore,
		Path:    "/instances/" + url.PathEscape(instanceID),
		Region:  region,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Region == "" {
		out.Region = orString(region, c.creds.Region)
	}
	return &out, nil
}

// InstanceAction 对实例执行开关机等操作，返回操作受理后的实例状态。
//
// 注意返回的一定是过渡态（STARTING / STOPPING），不是终态——
// 真正落定需要调用方继续轮询 GetInstance。
func (c *Client) InstanceAction(ctx context.Context, region, instanceID, action string) (*Instance, error) {
	if err := ValidateAction(action); err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("action", action)

	var out Instance
	_, err := c.Do(ctx, Request{
		Method:  http.MethodPost,
		Service: ServiceCore,
		Path:    "/instances/" + url.PathEscape(instanceID),
		Region:  region,
		Query:   query,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Region == "" {
		out.Region = orString(region, c.creds.Region)
	}
	return &out, nil
}

// ValidateAction 校验操作名。
func ValidateAction(action string) error {
	switch action {
	case ActionStart, ActionStop, ActionSoftStop, ActionReset, ActionSoftReset:
		return nil
	default:
		return fmt.Errorf("ociclient: 不支持的实例操作 %q", action)
	}
}

// UpdateInstanceRequest 是修改实例的请求体。
type UpdateInstanceRequest struct {
	DisplayName  string            `json:"displayName,omitempty"`
	ShapeConfig  *ShapeConfig      `json:"shapeConfig,omitempty"`
	Shape        string            `json:"shape,omitempty"`
	FreeformTags map[string]string `json:"freeformTags,omitempty"`
}

// UpdateInstance 修改实例配置。改 OCPU/内存要求实例处于 STOPPED，
// 否则 OCI 会返回 409 IncorrectState——调用方应提前拦下并给出提示。
func (c *Client) UpdateInstance(ctx context.Context, region, instanceID string, req UpdateInstanceRequest) (*Instance, error) {
	var out Instance
	_, err := c.Do(ctx, Request{
		Method:  http.MethodPut,
		Service: ServiceCore,
		Path:    "/instances/" + url.PathEscape(instanceID),
		Region:  region,
		Body:    req,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Region == "" {
		out.Region = orString(region, c.creds.Region)
	}
	return &out, nil
}

// TerminateInstance 终止实例。preserveBootVolume 为 false 时引导卷一并删除。
//
// 这是不可逆操作。HTTP 层必须已经过 L3 输名确认才允许调到这里。
func (c *Client) TerminateInstance(ctx context.Context, region, instanceID string, preserveBootVolume bool) error {
	query := url.Values{}
	query.Set("preserveBootVolume", strconv.FormatBool(preserveBootVolume))

	_, err := c.Do(ctx, Request{
		Method:  http.MethodDelete,
		Service: ServiceCore,
		Path:    "/instances/" + url.PathEscape(instanceID),
		Region:  region,
		Query:   query,
	}, nil)
	return err
}

// LaunchInstanceRequest 是创建实例的请求体。
type LaunchInstanceRequest struct {
	CompartmentID      string             `json:"compartmentId"`
	AvailabilityDomain string             `json:"availabilityDomain"`
	DisplayName        string             `json:"displayName,omitempty"`
	Shape              string             `json:"shape"`
	ShapeConfig        *ShapeConfig       `json:"shapeConfig,omitempty"`
	SourceDetails      *SourceDetails     `json:"sourceDetails,omitempty"`
	CreateVnicDetails  *CreateVnicDetails `json:"createVnicDetails,omitempty"`
	// Metadata 承载 cloud-init：ssh_authorized_keys 与 base64 编码的 user_data。
	Metadata     map[string]string `json:"metadata,omitempty"`
	FreeformTags map[string]string `json:"freeformTags,omitempty"`
	FaultDomain  string            `json:"faultDomain,omitempty"`
}

// CreateVnicDetails 是创建实例时的网卡配置。
type CreateVnicDetails struct {
	SubnetID       string `json:"subnetId"`
	AssignPublicIP *bool  `json:"assignPublicIp,omitempty"`
	AssignIpv6IP   *bool  `json:"assignIpv6Ip,omitempty"`
	DisplayName    string `json:"displayName,omitempty"`
	HostnameLabel  string `json:"hostnameLabel,omitempty"`
	PrivateIP      string `json:"privateIp,omitempty"`
}

// LaunchInstance 创建实例。返回的实例处于 PROVISIONING 状态。
func (c *Client) LaunchInstance(ctx context.Context, region string, req LaunchInstanceRequest) (*Instance, error) {
	if req.CompartmentID == "" {
		req.CompartmentID = c.creds.TenancyOCID
	}
	var out Instance
	_, err := c.Do(ctx, Request{
		Method:  http.MethodPost,
		Service: ServiceCore,
		Path:    "/instances",
		Region:  region,
		Body:    req,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Region == "" {
		out.Region = orString(region, c.creds.Region)
	}
	return &out, nil
}

// ListShapes 列出某可用域下可用的实例规格。
func (c *Client) ListShapes(ctx context.Context, region, compartmentID, availabilityDomain string) ([]Shape, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentID)
	query.Set("limit", "100")
	if availabilityDomain != "" {
		query.Set("availabilityDomain", availabilityDomain)
	}

	// 用空切片而不是 nil：nil 会被序列化成 JSON null，前端拿到 null
	// 再去 .forEach 就直接抛异常。列表接口永远返回列表。
	all := make([]Shape, 0)
	err := listPages(ctx, c, Request{
		Method:  http.MethodGet,
		Service: ServiceCore,
		Path:    "/shapes",
		Region:  region,
		Query:   query,
	}, 20, func(page []byte) error {
		var batch []Shape
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

// ListImagesOptions 是镜像列表的过滤条件。
type ListImagesOptions struct {
	CompartmentID          string
	Region                 string
	OperatingSystem        string
	OperatingSystemVersion string
	// Shape 用于只返回该规格能用的镜像。ARM 与 x86 的镜像不通用，
	// 不加这个过滤会让用户选到一个根本起不来的镜像。
	Shape string
	Limit int
}

// ListImages 列出可用镜像，按发布时间倒序（OCI 默认排序）。
func (c *Client) ListImages(ctx context.Context, opts ListImagesOptions) ([]Image, error) {
	compartment := opts.CompartmentID
	if compartment == "" {
		compartment = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartment)
	query.Set("limit", strconv.Itoa(orDefault(opts.Limit, 100)))
	query.Set("lifecycleState", "AVAILABLE")
	query.Set("sortBy", "TIMECREATED")
	query.Set("sortOrder", "DESC")
	if opts.OperatingSystem != "" {
		query.Set("operatingSystem", opts.OperatingSystem)
	}
	if opts.OperatingSystemVersion != "" {
		query.Set("operatingSystemVersion", opts.OperatingSystemVersion)
	}
	if opts.Shape != "" {
		query.Set("shape", opts.Shape)
	}

	// 用空切片而不是 nil：nil 会被序列化成 JSON null，前端拿到 null
	// 再去 .forEach 就直接抛异常。列表接口永远返回列表。
	all := make([]Image, 0)
	err := listPages(ctx, c, Request{
		Method:  http.MethodGet,
		Service: ServiceCore,
		Path:    "/images",
		Region:  opts.Region,
		Query:   query,
	}, 20, func(page []byte) error {
		var batch []Image
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

// ListVnicAttachments 列出 VNIC 关联。传入 instanceID 可只查单台实例的。
func (c *Client) ListVnicAttachments(ctx context.Context, region, compartmentID, instanceID string) ([]VnicAttachment, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentID)
	query.Set("limit", "100")
	if instanceID != "" {
		query.Set("instanceId", instanceID)
	}

	// 用空切片而不是 nil：nil 会被序列化成 JSON null，前端拿到 null
	// 再去 .forEach 就直接抛异常。列表接口永远返回列表。
	all := make([]VnicAttachment, 0)
	err := listPages(ctx, c, Request{
		Method:  http.MethodGet,
		Service: ServiceCore,
		Path:    "/vnicAttachments",
		Region:  region,
		Query:   query,
	}, 20, func(page []byte) error {
		var batch []VnicAttachment
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

// AttachVnic 为实例添加附属网卡。
func (c *Client) AttachVnic(ctx context.Context, region, instanceID string, details CreateVnicDetails, displayName string) (*VnicAttachment, error) {
	body := map[string]any{
		"instanceId":        instanceID,
		"createVnicDetails": details,
	}
	if displayName != "" {
		body["displayName"] = displayName
	}
	var out VnicAttachment
	_, err := c.Do(ctx, Request{
		Method:  http.MethodPost,
		Service: ServiceCore,
		Path:    "/vnicAttachments",
		Region:  region,
		Body:    body,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DetachVnic 移除附属网卡。主网卡（nicIndex 0）无法移除。
func (c *Client) DetachVnic(ctx context.Context, region, attachmentID string) error {
	_, err := c.Do(ctx, Request{
		Method:  http.MethodDelete,
		Service: ServiceCore,
		Path:    "/vnicAttachments/" + url.PathEscape(attachmentID),
		Region:  region,
	}, nil)
	return err
}

// InstanceConsoleConnection 是实例的控制台连接。
//
// 建立后 Oracle 会给出两条 SSH 命令：一条连串行控制台（排查启动失败、
// 修 fstab 这类"机器起不来"的场景），一条建 VNC 隧道。
type InstanceConsoleConnection struct {
	ID                   string            `json:"id"`
	InstanceID           string            `json:"instanceId"`
	CompartmentID        string            `json:"compartmentId"`
	LifecycleState       string            `json:"lifecycleState"`
	ConnectionString     string            `json:"connectionString"`
	VncConnectionString  string            `json:"vncConnectionString"`
	ServiceHostKeyFinger string            `json:"serviceHostKeyFingerprint"`
	Fingerprint          string            `json:"fingerprint"`
	DefinedTags          map[string]any    `json:"definedTags,omitempty"`
	FreeformTags         map[string]string `json:"freeformTags,omitempty"`
}

// ListConsoleConnections 列出实例已有的控制台连接。
func (c *Client) ListConsoleConnections(ctx context.Context, region, compartmentID, instanceID string) ([]InstanceConsoleConnection, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentID)
	if instanceID != "" {
		query.Set("instanceId", instanceID)
	}

	var out []InstanceConsoleConnection
	_, err := c.Do(ctx, Request{
		Method: http.MethodGet, Service: ServiceCore,
		Path: "/instanceConsoleConnections", Region: region, Query: query,
	}, &out)
	return out, err
}

// CreateConsoleConnection 为实例建立控制台连接。
//
// publicKey 是用户的 SSH 公钥——Oracle 用它鉴权，只有持有对应私钥的人
// 才能连上控制台。这也意味着本工具不需要、也拿不到任何额外凭据。
func (c *Client) CreateConsoleConnection(ctx context.Context, region, compartmentID, instanceID, publicKey string) (*InstanceConsoleConnection, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	var out InstanceConsoleConnection
	_, err := c.Do(ctx, Request{
		Method: http.MethodPost, Service: ServiceCore,
		Path: "/instanceConsoleConnections", Region: region,
		Body: map[string]any{
			"instanceId":    instanceID,
			"compartmentId": compartmentID,
			"publicKey":     publicKey,
		},
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteConsoleConnection 删除控制台连接。
func (c *Client) DeleteConsoleConnection(ctx context.Context, region, connectionID string) error {
	_, err := c.Do(ctx, Request{
		Method: http.MethodDelete, Service: ServiceCore,
		Path: "/instanceConsoleConnections/" + url.PathEscape(connectionID), Region: region,
	}, nil)
	return err
}

// ValidateShapeConfig 校验弹性规格的 OCPU 与内存搭配。
//
// 校验一律以 shape 自带的元数据为准，不掺任何写死的免费额度数字——
// Oracle 改过一次免费额度，写死的数字迟早会把合法配置拦下来。
// 提前拦下非法组合，比让用户等 OCI 返回 400 体验好得多。
func ValidateShapeConfig(shape *Shape, cfg ShapeConfig) error {
	if shape == nil {
		return nil
	}
	if !shape.IsFlexible {
		if cfg.Ocpus > 0 || cfg.MemoryInGBs > 0 {
			return fmt.Errorf("规格 %s 不是弹性规格，无法自定义 OCPU 与内存", shape.Shape)
		}
		return nil
	}
	if cfg.Ocpus <= 0 {
		return fmt.Errorf("弹性规格必须指定 OCPU 数量")
	}
	if shape.OcpuOptions != nil {
		if cfg.Ocpus < shape.OcpuOptions.Min || cfg.Ocpus > shape.OcpuOptions.Max {
			return fmt.Errorf("OCPU 必须在 %g–%g 之间，当前 %g",
				shape.OcpuOptions.Min, shape.OcpuOptions.Max, cfg.Ocpus)
		}
	}
	if cfg.MemoryInGBs <= 0 {
		return fmt.Errorf("弹性规格必须指定内存大小")
	}
	if shape.MemoryOptions != nil {
		mo := shape.MemoryOptions
		if cfg.MemoryInGBs < mo.MinInGBs || cfg.MemoryInGBs > mo.MaxInGBs {
			return fmt.Errorf("内存必须在 %g–%g GB 之间，当前 %g GB",
				mo.MinInGBs, mo.MaxInGBs, cfg.MemoryInGBs)
		}
		perOcpu := cfg.MemoryInGBs / cfg.Ocpus
		if mo.MaxPerOcpuInGBs > 0 && perOcpu > mo.MaxPerOcpuInGBs {
			return fmt.Errorf("每 OCPU 最多配 %g GB 内存，当前 %g OCPU 配了 %g GB",
				mo.MaxPerOcpuInGBs, cfg.Ocpus, cfg.MemoryInGBs)
		}
		if mo.MinPerOcpuInGBs > 0 && perOcpu < mo.MinPerOcpuInGBs {
			return fmt.Errorf("每 OCPU 至少需要 %g GB 内存，当前 %g OCPU 只配了 %g GB",
				mo.MinPerOcpuInGBs, cfg.Ocpus, cfg.MemoryInGBs)
		}
	}
	return nil
}

func orDefault(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

func orString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
