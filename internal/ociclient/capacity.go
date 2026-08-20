package ociclient

import (
	"context"
	"net/http"
	"time"
)

// 容量报告里的可用状态。
const (
	// CapacityAvailable 表示当前有容量。
	//
	// 注意这只是"值得一试"，不是"一定能开"：报告反映的是宿主机池的整体状态，
	// 真正的分配还要看那一瞬间的争抢。社区里有明确记录报告说 AVAILABLE 而
	// LaunchInstance 仍然失败的情况（oracle/oci-cli#748）。
	// 所以它只能当过滤器，不能当判据。
	CapacityAvailable = "AVAILABLE"
	// CapacityOutOfHost 表示该可用域此刻没有这种规格的宿主机容量。
	CapacityOutOfHost = "OUT_OF_HOST_CAPACITY"
	// CapacityHardwareNotSupported 表示这个区域压根没部署这种硬件。
	//
	// 和"暂时没货"是两回事：这个状态不会因为等待而改变，
	// 对它做轮询监控是纯粹的浪费，UI 上必须和 OUT_OF_HOST_CAPACITY 区分开。
	CapacityHardwareNotSupported = "HARDWARE_NOT_SUPPORTED"
)

// CapacityShapeRequest 是要查询的一个规格。
type CapacityShapeRequest struct {
	InstanceShape string `json:"instanceShape"`
	// InstanceShapeConfig 只有弹性规格需要。固定规格（如 E2.1.Micro）带上会被拒。
	InstanceShapeConfig *ShapeConfig `json:"instanceShapeConfig,omitempty"`
	FaultDomain         string       `json:"faultDomain,omitempty"`
}

// CapacityShapeAvailability 是报告里一个规格的结果。
type CapacityShapeAvailability struct {
	InstanceShape       string       `json:"instanceShape"`
	InstanceShapeConfig *ShapeConfig `json:"instanceShapeConfig,omitempty"`
	FaultDomain         string       `json:"faultDomain,omitempty"`
	AvailabilityStatus  string       `json:"availabilityStatus"`
	// AvailableCount 不是所有规格都返回，缺省为 0。0 不代表"没有"，
	// 判断有没有容量一律看 AvailabilityStatus。
	AvailableCount int64 `json:"availableCount,omitempty"`
}

// CapacityReport 是一次容量查询的结果。
type CapacityReport struct {
	CompartmentID       string                      `json:"compartmentId"`
	AvailabilityDomain  string                      `json:"availabilityDomain"`
	ShapeAvailabilities []CapacityShapeAvailability `json:"shapeAvailabilities"`
	TimeCreated         time.Time                   `json:"timeCreated"`
}

// CreateCapacityReport 查询某个可用域里指定规格的容量。
//
// 这是个**只读**操作：不创建任何资源、不消耗配额、不占用额度，
// 和反复调 LaunchInstance 完全不是一个风险量级。用它先过滤一遍，
// 能把真正的创建请求量砍掉一个数量级——而风控盯的正是创建请求。
//
// 两个容易踩的点：
//   - compartmentID 必须是**租户根分区**（tenancy OCID），子分区会被拒。
//   - 一次只能查一个可用域。要覆盖一个区域的三个 AD 就是三次调用。
//
// 只能查已订阅的区域：请求签名后发往 iaas.{region}.oraclecloud.com，
// 未订阅的区域租户在那儿没有存在，会被直接拒掉。永久免费账号更严格，
// 实例只能开在 home region，通常也订阅不了第二个区域。
func (c *Client) CreateCapacityReport(ctx context.Context, region, compartmentID,
	availabilityDomain string, shapes []CapacityShapeRequest) (*CapacityReport, error) {

	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}

	body := map[string]any{
		"compartmentId":       compartmentID,
		"availabilityDomain":  availabilityDomain,
		"shapeAvailabilities": shapes,
	}

	var out CapacityReport
	_, err := c.Do(ctx, Request{
		Method:  http.MethodPost,
		Service: ServiceCore,
		Path:    "/computeCapacityReports",
		Region:  region,
		Body:    body,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// HasCapacity 在报告里查某个规格是否有容量。
//
// 找不到该规格时返回 false —— 报告没提到就当没有，宁可少试一次，
// 也不要因为解析不到而退回"直接开干"。
func (r *CapacityReport) HasCapacity(shape string) bool {
	if r == nil {
		return false
	}
	for _, a := range r.ShapeAvailabilities {
		if a.InstanceShape == shape {
			return a.AvailabilityStatus == CapacityAvailable
		}
	}
	return false
}

// StatusOf 返回某个规格的可用状态，找不到时返回空串。
func (r *CapacityReport) StatusOf(shape string) string {
	if r == nil {
		return ""
	}
	for _, a := range r.ShapeAvailabilities {
		if a.InstanceShape == shape {
			return a.AvailabilityStatus
		}
	}
	return ""
}

// CapacityStatusText 把状态码翻成给人看的短语。
func CapacityStatusText(status string) string {
	switch status {
	case CapacityAvailable:
		return "有容量"
	case CapacityOutOfHost:
		return "暂时没有容量"
	case CapacityHardwareNotSupported:
		return "该区域未部署此硬件"
	case "":
		return "未知"
	default:
		return status
	}
}
