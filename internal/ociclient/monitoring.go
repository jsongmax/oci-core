package ociclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// 计算实例的监控命名空间与常用指标名。
//
// oci_computeagent 需要实例内安装并运行 Oracle Cloud Agent 才有数据；
// 未安装时接口会正常返回但数据点为空——UI 要能区分"没数据"与"调用失败"。
const (
	NamespaceComputeAgent = "oci_computeagent"

	MetricCPUUtilization    = "CpuUtilization"
	MetricMemoryUtilization = "MemoryUtilization"
	MetricNetworkBytesIn    = "NetworksBytesIn"
	MetricNetworkBytesOut   = "NetworksBytesOut"
	MetricDiskBytesRead     = "DiskBytesRead"
	MetricDiskBytesWritten  = "DiskBytesWritten"
)

// VCN 网卡的监控命名空间与流量指标。
//
// 与 oci_computeagent 不同，这组指标由 VCN 在宿主侧统计，不需要实例里装任何东西，
// 实例停机时也照样留着历史数据。维度 resourceId 是 **VNIC** 的 OCID，不是实例的。
//
// 它统计的是网卡收发的全部字节，包括 VCN 内网、同区域 OCI 服务之间这些不计费的流量，
// 所以只能当"这台机器跑了多少流量"看，计费口径以 Usage API 为准。
const (
	NamespaceVCN = "oci_vcn"

	// MetricVnicToNetworkBytes 是网卡发往网络的字节数，即出站。
	MetricVnicToNetworkBytes = "VnicToNetworkBytes"
	// MetricVnicFromNetworkBytes 是网络发到网卡的字节数，即入站。
	MetricVnicFromNetworkBytes = "VnicFromNetworkBytes"
)

// Datapoint 是一个时间序列数据点。
type Datapoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
	Count     int       `json:"count,omitempty"`
}

// MetricSeries 是一条指标序列。
type MetricSeries struct {
	Namespace            string            `json:"namespace"`
	Name                 string            `json:"name"`
	CompartmentID        string            `json:"compartmentId"`
	Dimensions           map[string]string `json:"dimensions"`
	Resolution           string            `json:"resolution"`
	AggregatedDatapoints []Datapoint       `json:"aggregatedDatapoints"`
}

// MetricQuery 描述一次监控数据查询。
type MetricQuery struct {
	CompartmentID string
	Region        string
	Namespace     string
	// Query 是 MQL 表达式，例如：
	//   CpuUtilization[1m]{resourceId = "ocid1.instance..."}.mean()
	Query      string
	StartTime  time.Time
	EndTime    time.Time
	Resolution string // 1m / 5m / 1h，留空由服务端决定
}

// SummarizeMetrics 查询监控数据。
func (c *Client) SummarizeMetrics(ctx context.Context, q MetricQuery) ([]MetricSeries, error) {
	compartment := q.CompartmentID
	if compartment == "" {
		compartment = c.creds.TenancyOCID
	}
	query := url.Values{}
	query.Set("compartmentId", compartment)

	body := map[string]any{
		"namespace": q.Namespace,
		"query":     q.Query,
		"startTime": q.StartTime.UTC().Format(time.RFC3339),
		"endTime":   q.EndTime.UTC().Format(time.RFC3339),
	}
	if q.Resolution != "" {
		body["resolution"] = q.Resolution
	}

	var out []MetricSeries
	_, err := c.Do(ctx, Request{
		Method:  http.MethodPost,
		Service: ServiceMonitoring,
		Path:    "/metrics/actions/summarizeMetricsData",
		Region:  q.Region,
		Query:   query,
		Body:    body,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// InstanceMetricQuery 为单台实例拼出 MQL 查询串。
//
// interval 决定采样粒度（1m/5m/1h）；aggregation 是聚合函数（mean/max/rate）。
// 流量类指标用 rate() 才能得到"每秒字节数"，用 mean() 得到的是原始计数器均值。
func InstanceMetricQuery(metricName, instanceOCID, interval, aggregation string) string {
	return fmt.Sprintf(`%s[%s]{resourceId = "%s"}.%s()`,
		metricName, interval, instanceOCID, aggregation)
}

// VnicMetricQuery 为单块网卡拼出 MQL 查询串，按 interval 求和得到每个窗口的字节数。
func VnicMetricQuery(metricName, vnicOCID, interval string) string {
	return fmt.Sprintf(`%s[%s]{resourceId = "%s"}.sum()`, metricName, interval, vnicOCID)
}

// DefaultAggregationFor 返回某指标的推荐聚合方式。
func DefaultAggregationFor(metricName string) string {
	switch metricName {
	case MetricNetworkBytesIn, MetricNetworkBytesOut,
		MetricDiskBytesRead, MetricDiskBytesWritten:
		// 这些是累积计数器，取速率才有意义。
		return "rate"
	default:
		return "mean"
	}
}

// ResolutionFor 根据时间跨度挑一个合适的采样粒度。
//
// 目的是把返回的数据点数控制在图表能画得清楚的范围内（约 100–300 个），
// 跨度越大粒度越粗，避免一次拉回上万个点。
func ResolutionFor(span time.Duration) string {
	switch {
	case span <= 2*time.Hour:
		return "1m"
	case span <= 12*time.Hour:
		return "5m"
	case span <= 3*24*time.Hour:
		return "1h"
	default:
		return "6h"
	}
}
