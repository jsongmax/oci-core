package ociclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// 卷的生命周期状态。
const (
	VolumeProvisioning = "PROVISIONING"
	VolumeRestoring    = "RESTORING"
	VolumeAvailable    = "AVAILABLE"
	VolumeTerminating  = "TERMINATING"
	VolumeTerminated   = "TERMINATED"
	VolumeFaulty       = "FAULTY"
)

// VPU（每 GB 的性能单位）的常用档位。免费额度账号默认是 10（Balanced）。
const (
	VpuLowerCost   = 0  // Lower Cost，无 SLA
	VpuBalanced    = 10 // Balanced，默认
	VpuHigh        = 20 // Higher Performance
	VpuUltraHigh30 = 30 // Ultra High Performance 起步档
)

// BootVolume 是实例的系统盘。
type BootVolume struct {
	ID                 string    `json:"id"`
	CompartmentID      string    `json:"compartmentId"`
	DisplayName        string    `json:"displayName"`
	AvailabilityDomain string    `json:"availabilityDomain"`
	SizeInGBs          int64     `json:"sizeInGBs"`
	VpusPerGB          int64     `json:"vpusPerGB"`
	LifecycleState     string    `json:"lifecycleState"`
	ImageID            string    `json:"imageId"`
	IsHydrated         bool      `json:"isHydrated"`
	TimeCreated        time.Time `json:"timeCreated"`
	Region             string    `json:"-"`
}

// BootVolumeAttachment 把引导卷挂到实例上。
//
// 「救援模式」的实现方式就是把故障机的引导卷 detach 下来，
// 再 attach 到另一台正常实例上当数据盘挂载修复。
type BootVolumeAttachment struct {
	ID                 string    `json:"id"`
	InstanceID         string    `json:"instanceId"`
	BootVolumeID       string    `json:"bootVolumeId"`
	AvailabilityDomain string    `json:"availabilityDomain"`
	CompartmentID      string    `json:"compartmentId"`
	DisplayName        string    `json:"displayName"`
	LifecycleState     string    `json:"lifecycleState"`
	TimeCreated        time.Time `json:"timeCreated"`
}

// Volume 是块存储卷。
type Volume struct {
	ID                 string    `json:"id"`
	CompartmentID      string    `json:"compartmentId"`
	DisplayName        string    `json:"displayName"`
	AvailabilityDomain string    `json:"availabilityDomain"`
	SizeInGBs          int64     `json:"sizeInGBs"`
	VpusPerGB          int64     `json:"vpusPerGB"`
	LifecycleState     string    `json:"lifecycleState"`
	TimeCreated        time.Time `json:"timeCreated"`
	Region             string    `json:"-"`
}

// VolumeAttachment 把块存储卷挂到实例上。
type VolumeAttachment struct {
	ID                 string    `json:"id"`
	InstanceID         string    `json:"instanceId"`
	VolumeID           string    `json:"volumeId"`
	AvailabilityDomain string    `json:"availabilityDomain"`
	CompartmentID      string    `json:"compartmentId"`
	DisplayName        string    `json:"displayName"`
	AttachmentType     string    `json:"attachmentType"`
	LifecycleState     string    `json:"lifecycleState"`
	Device             string    `json:"device"`
	IsReadOnly         bool      `json:"isReadOnly"`
	Iqn                string    `json:"iqn,omitempty"`
	IPv4               string    `json:"ipv4,omitempty"`
	Port               int       `json:"port,omitempty"`
	TimeCreated        time.Time `json:"timeCreated"`
}

// ListBootVolumes 列出分区下的引导卷。
func (c *Client) ListBootVolumes(ctx context.Context, region, compartmentID, availabilityDomain string) ([]BootVolume, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentID)
	query.Set("limit", "100")
	if availabilityDomain != "" {
		query.Set("availabilityDomain", availabilityDomain)
	}

	effectiveRegion := orString(region, c.creds.Region)
	// 用空切片而不是 nil：nil 会被序列化成 JSON null，前端拿到 null
	// 再去 .forEach 就直接抛异常。列表接口永远返回列表。
	all := make([]BootVolume, 0)
	err := listPages(ctx, c, Request{
		Method:  http.MethodGet,
		Service: ServiceCore,
		Path:    "/bootVolumes",
		Region:  region,
		Query:   query,
	}, 20, func(page []byte) error {
		var batch []BootVolume
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

// GetBootVolume 取回单个引导卷。
func (c *Client) GetBootVolume(ctx context.Context, region, bootVolumeID string) (*BootVolume, error) {
	var out BootVolume
	_, err := c.Do(ctx, Request{
		Method:  http.MethodGet,
		Service: ServiceCore,
		Path:    "/bootVolumes/" + url.PathEscape(bootVolumeID),
		Region:  region,
	}, &out)
	if err != nil {
		return nil, err
	}
	out.Region = orString(region, c.creds.Region)
	return &out, nil
}

// UpdateBootVolumeRequest 是修改引导卷的请求体。
type UpdateBootVolumeRequest struct {
	DisplayName string `json:"displayName,omitempty"`
	SizeInGBs   int64  `json:"sizeInGBs,omitempty"`
	VpusPerGB   *int64 `json:"vpusPerGB,omitempty"`
}

// UpdateBootVolume 改名、扩容或调整 VPU 性能档。
//
// 扩容只能增不能减，且扩容后还需要在实例内部扩展分区与文件系统——
// 这一点必须在 UI 上讲清楚，否则用户会以为扩容没生效。
func (c *Client) UpdateBootVolume(ctx context.Context, region, bootVolumeID string, req UpdateBootVolumeRequest) (*BootVolume, error) {
	var out BootVolume
	_, err := c.Do(ctx, Request{
		Method:  http.MethodPut,
		Service: ServiceCore,
		Path:    "/bootVolumes/" + url.PathEscape(bootVolumeID),
		Region:  region,
		Body:    req,
	}, &out)
	if err != nil {
		return nil, err
	}
	out.Region = orString(region, c.creds.Region)
	return &out, nil
}

// ListBootVolumeAttachments 列出引导卷挂载关系。
func (c *Client) ListBootVolumeAttachments(ctx context.Context, region, compartmentID, availabilityDomain, instanceID string) ([]BootVolumeAttachment, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentID)
	query.Set("limit", "100")
	if availabilityDomain != "" {
		query.Set("availabilityDomain", availabilityDomain)
	}
	if instanceID != "" {
		query.Set("instanceId", instanceID)
	}

	// 用空切片而不是 nil：nil 会被序列化成 JSON null，前端拿到 null
	// 再去 .forEach 就直接抛异常。列表接口永远返回列表。
	all := make([]BootVolumeAttachment, 0)
	err := listPages(ctx, c, Request{
		Method:  http.MethodGet,
		Service: ServiceCore,
		Path:    "/bootVolumeAttachments",
		Region:  region,
		Query:   query,
	}, 20, func(page []byte) error {
		var batch []BootVolumeAttachment
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

// ListVolumes 列出块存储卷。
func (c *Client) ListVolumes(ctx context.Context, region, compartmentID string) ([]Volume, error) {
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartmentID)
	query.Set("limit", "100")

	effectiveRegion := orString(region, c.creds.Region)
	// 用空切片而不是 nil：nil 会被序列化成 JSON null，前端拿到 null
	// 再去 .forEach 就直接抛异常。列表接口永远返回列表。
	all := make([]Volume, 0)
	err := listPages(ctx, c, Request{
		Method:  http.MethodGet,
		Service: ServiceCore,
		Path:    "/volumes",
		Region:  region,
		Query:   query,
	}, 20, func(page []byte) error {
		var batch []Volume
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

// UpdateVolumeRequest 是修改块存储卷的请求体。
type UpdateVolumeRequest struct {
	DisplayName string `json:"displayName,omitempty"`
	SizeInGBs   int64  `json:"sizeInGBs,omitempty"`
	VpusPerGB   *int64 `json:"vpusPerGB,omitempty"`
}

// UpdateVolume 改名、扩容或调整 VPU。
func (c *Client) UpdateVolume(ctx context.Context, region, volumeID string, req UpdateVolumeRequest) (*Volume, error) {
	var out Volume
	_, err := c.Do(ctx, Request{
		Method:  http.MethodPut,
		Service: ServiceCore,
		Path:    "/volumes/" + url.PathEscape(volumeID),
		Region:  region,
		Body:    req,
	}, &out)
	if err != nil {
		return nil, err
	}
	out.Region = orString(region, c.creds.Region)
	return &out, nil
}

// ListVolumeAttachments 列出块存储挂载关系。
func (c *Client) ListVolumeAttachments(ctx context.Context, region, compartmentID, instanceID string) ([]VolumeAttachment, error) {
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
	all := make([]VolumeAttachment, 0)
	err := listPages(ctx, c, Request{
		Method:  http.MethodGet,
		Service: ServiceCore,
		Path:    "/volumeAttachments",
		Region:  region,
		Query:   query,
	}, 20, func(page []byte) error {
		var batch []VolumeAttachment
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

// DetachBootVolume 分离引导卷。
//
// 这是「救援模式」的第一步：把起不来的机器的系统盘卸下来，
// 挂到另一台正常实例上当数据盘修复。实例必须先关机。
func (c *Client) DetachBootVolume(ctx context.Context, region, attachmentID string) error {
	_, err := c.Do(ctx, Request{
		Method:  http.MethodDelete,
		Service: ServiceCore,
		Path:    "/bootVolumeAttachments/" + url.PathEscape(attachmentID),
		Region:  region,
	}, nil)
	return err
}

// AttachBootVolume 把引导卷挂回某台实例。
func (c *Client) AttachBootVolume(ctx context.Context, region, instanceID, bootVolumeID, displayName string) (*BootVolumeAttachment, error) {
	body := map[string]any{
		"instanceId":   instanceID,
		"bootVolumeId": bootVolumeID,
	}
	if displayName != "" {
		body["displayName"] = displayName
	}

	var out BootVolumeAttachment
	_, err := c.Do(ctx, Request{
		Method:  http.MethodPost,
		Service: ServiceCore,
		Path:    "/bootVolumeAttachments",
		Region:  region,
		Body:    body,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// 挂载类型。救援模式一律用半虚拟化：iSCSI 挂上去之后还要在客机里跑一串
// iscsiadm 命令才能看到设备，而救援场景下人往往正手忙脚乱，多一步就多一次失败。
// 半虚拟化挂上就是 /dev/sdb，lsblk 直接可见。
const (
	AttachmentTypeParavirtualized = "paravirtualized"
	AttachmentTypeISCSI           = "iscsi"
)

// AttachVolume 把一块卷挂到实例上当**数据盘**。
//
// 和 AttachBootVolume 的区别是救援流程的关键：AttachBootVolume 是让目标机器
// 拿这块卷当自己的系统盘启动（一台机器只能有一块，且必须关机）；这里是把卷
// 当普通数据盘挂在一台正在运行的机器上，好 mount 起来改里面的文件。
//
// volumeID 可以是引导卷的 OCID——Oracle 允许把引导卷当数据盘挂到别的实例上，
// 这正是官方文档里修复无法启动的机器的办法。前提是两台机器在同一个可用域。
func (c *Client) AttachVolume(ctx context.Context, region, instanceID, volumeID, displayName, attachmentType string) (*VolumeAttachment, error) {
	if attachmentType == "" {
		attachmentType = AttachmentTypeParavirtualized
	}
	body := map[string]any{
		"type":       attachmentType,
		"instanceId": instanceID,
		"volumeId":   volumeID,
	}
	if displayName != "" {
		body["displayName"] = displayName
	}

	var out VolumeAttachment
	_, err := c.Do(ctx, Request{
		Method:  http.MethodPost,
		Service: ServiceCore,
		Path:    "/volumeAttachments",
		Region:  region,
		Body:    body,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DetachVolume 分离数据盘挂载。
//
// 救援结束后必须先做这一步，卷才能挂回原来的机器当引导卷——
// 一块卷同一时刻只能挂在一处。
func (c *Client) DetachVolume(ctx context.Context, region, attachmentID string) error {
	_, err := c.Do(ctx, Request{
		Method:  http.MethodDelete,
		Service: ServiceCore,
		Path:    "/volumeAttachments/" + url.PathEscape(attachmentID),
		Region:  region,
	}, nil)
	return err
}

// ValidateVolumeResize 校验扩容请求。
//
// 云盘只能扩不能缩，OCI 侧会拒绝缩容请求，但错误信息比较晦涩，
// 这里提前拦下并说清楚。
func ValidateVolumeResize(currentGBs, targetGBs int64) error {
	if targetGBs < currentGBs {
		return fmt.Errorf("云盘不支持缩容：当前 %d GB，无法调整为 %d GB", currentGBs, targetGBs)
	}
	if targetGBs == currentGBs {
		return fmt.Errorf("目标容量与当前容量相同（%d GB），无需调整", currentGBs)
	}
	if targetGBs < 50 {
		return fmt.Errorf("引导卷最小 50 GB，当前请求 %d GB", targetGBs)
	}
	if targetGBs > 32768 {
		return fmt.Errorf("单卷最大 32768 GB，当前请求 %d GB", targetGBs)
	}
	return nil
}

// ValidateVpus 校验 VPU 档位。OCI 只接受 0 与 10 起步、以 10 为步长的值。
func ValidateVpus(vpus int64) error {
	if vpus == 0 {
		return nil
	}
	if vpus < 10 || vpus > 120 || vpus%10 != 0 {
		return fmt.Errorf("VPU 只能是 0 或 10–120 之间 10 的倍数，当前 %d", vpus)
	}
	return nil
}
