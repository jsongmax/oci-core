package ociclient

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ocicore/internal/i18n"
)

const (
	// AuditRetentionDays 是 OCI 官方审计日志的最长保留天数。
	// 实测传入超过 365 天的 startTime 会收到 OCI 400 InvalidParameter:
	// "startTime can not be older than 365 days"。
	AuditRetentionDays = 365
	// AuditMaxSpanDays 是单次查询允许的最大时间跨度，避免过宽区间导致响应超时。
	AuditMaxSpanDays = 31

	AuditRetentionWindow = AuditRetentionDays * 24 * time.Hour
	AuditMaxSpan         = AuditMaxSpanDays * 24 * time.Hour

	maxAuditPageTokenLen = 2048
)

// ListAuditEventsOptions 描述单页 OCI 云端审计日志查询参数。
type ListAuditEventsOptions struct {
	// Region 覆盖客户端默认区域。留空则用默认区域。
	Region string
	// CompartmentID 目标区间 OCID。留空则默认租户根区间（TenancyOCID）。
	CompartmentID string
	// StartTime 与 EndTime 必填，且 EndTime 必须晚于 StartTime。
	// OCI 按 eventTime 升序返回 [StartTime, EndTime] 内的事件。
	StartTime time.Time
	EndTime   time.Time
	// Page 是上一页响应头 opc-next-page 返回的游标；留空查询首页。
	Page string
}

// AuditEventsPage 是 ListAuditEvents 的单页返回结果。
type AuditEventsPage struct {
	Events       []AuditEvent
	NextPage     string
	OpcRequestID string
}

// AuditEvent 对应 OCI Audit API (/20190901/auditEvents) 返回的单条 CloudEvents 审计事件。
// 字段名与嵌套层级严格对齐真实接口返回。
type AuditEvent struct {
	EventType          string         `json:"eventType"`
	CloudEventsVersion string         `json:"cloudEventsVersion"`
	EventTypeVersion   string         `json:"eventTypeVersion"`
	Source             string         `json:"source"`
	EventID            string         `json:"eventId"`
	EventTime          time.Time      `json:"eventTime"`
	ContentType        string         `json:"contentType"`
	Data               AuditEventData `json:"data"`
}

// AuditEventData 是事件主数据体。
type AuditEventData struct {
	EventGroupingID    string                    `json:"eventGroupingId"`
	EventName          string                    `json:"eventName"`
	CompartmentID      string                    `json:"compartmentId"`
	CompartmentName    string                    `json:"compartmentName"`
	ResourceName       string                    `json:"resourceName"`
	ResourceID         *string                   `json:"resourceId"`
	AvailabilityDomain string                    `json:"availabilityDomain"`
	FreeformTags       map[string]string         `json:"freeformTags"`
	DefinedTags        map[string]map[string]any `json:"definedTags"`
	Identity           AuditIdentity             `json:"identity"`
	Request            AuditRequest              `json:"request"`
	Response           AuditResponse             `json:"response"`
	StateChange        *AuditStateChange         `json:"stateChange"`
	AdditionalDetails  map[string]any            `json:"additionalDetails"`
}

// AuditIdentity 描述发起该 API 调用的身份上下文。
type AuditIdentity struct {
	PrincipalName    *string `json:"principalName"`
	PrincipalID      string  `json:"principalId"`
	AuthType         string  `json:"authType"`
	CallerName       *string `json:"callerName"`
	CallerID         *string `json:"callerId"`
	TenantID         string  `json:"tenantId"`
	IPAddress        string  `json:"ipAddress"`
	Credentials      string  `json:"credentials"`
	UserAgent        string  `json:"userAgent"`
	ConsoleSessionID *string `json:"consoleSessionId"`
}

// AuditRequest 描述被审计的原始 HTTP 请求摘要。
type AuditRequest struct {
	ID         string              `json:"id"`
	Path       string              `json:"path"`
	Action     string              `json:"action"`
	Parameters map[string][]string `json:"parameters"`
	Headers    map[string][]string `json:"headers"`
}

// AuditResponse 描述 OCI 服务端的响应摘要。
type AuditResponse struct {
	Status       string              `json:"status"`
	ResponseTime string              `json:"responseTime"`
	Headers      map[string][]string `json:"headers"`
	Payload      map[string]any      `json:"payload"`
	Message      string              `json:"message"`
}

// AuditStateChange 描述资源变更前后的状态快照（只读操作通常为 null 或空对象）。
type AuditStateChange struct {
	Previous map[string]any `json:"previous"`
	Current  map[string]any `json:"current"`
}

// ListAuditEvents 执行一次单页 OCI Audit ListEvents 调用（GET /20190901/auditEvents）。
//
// 需要 IAM 权限：read audit-events（AUDIT_EVENT_READ）。
// 该接口不支持 limit 参数，OCI 固定每页最多返回 100 条并按 eventTime 升序排列；
// 若存在更多结果，响应头 opc-next-page 会带回下一页游标。
func (c *Client) ListAuditEvents(ctx context.Context, opts ListAuditEventsOptions) (*AuditEventsPage, error) {
	return c.listAuditEventsAt(ctx, opts, time.Now().UTC())
}

func (c *Client) listAuditEventsAt(ctx context.Context, opts ListAuditEventsOptions, now time.Time) (*AuditEventsPage, error) {
	if opts.StartTime.IsZero() || opts.EndTime.IsZero() {
		return nil, i18n.Err("oci.audit_time_required")
	}
	start := opts.StartTime.UTC()
	end := opts.EndTime.UTC()
	if !end.After(start) {
		return nil, i18n.Err("oci.audit_range_invalid",
			start.Format(time.RFC3339), end.Format(time.RFC3339))
	}
	if end.Sub(start) > AuditMaxSpan {
		return nil, i18n.Err("oci.audit_span_too_large", AuditMaxSpanDays)
	}
	// 留 1 分钟时钟余量，避免边界时刻因毫秒差误拒。
	oldest := now.UTC().Add(-AuditRetentionWindow - time.Minute)
	if start.Before(oldest) {
		return nil, i18n.Err("oci.audit_beyond_retention", AuditRetentionDays)
	}

	pageToken := strings.TrimSpace(opts.Page)
	if len(pageToken) > maxAuditPageTokenLen {
		return nil, i18n.Err("oci.audit_page_invalid")
	}

	region := strings.TrimSpace(opts.Region)
	if region != "" {
		normalized := NormalizeRegion(region)
		if normalized == "" {
			return nil, i18n.Err("oci.region_invalid", opts.Region)
		}
		region = normalized
	}

	compartmentID := strings.TrimSpace(opts.CompartmentID)
	if compartmentID == "" {
		compartmentID = c.creds.TenancyOCID
	}

	query := url.Values{}
	query.Set("compartmentId", compartmentID)
	query.Set("startTime", start.Format(time.RFC3339))
	query.Set("endTime", end.Format(time.RFC3339))
	if pageToken != "" {
		query.Set("page", pageToken)
	}

	var events []AuditEvent
	resp, err := c.Do(ctx, Request{
		Method:  http.MethodGet,
		Service: ServiceAudit,
		Path:    "/auditEvents",
		Region:  region,
		Query:   query,
	}, &events)
	if err != nil {
		return nil, err
	}
	if events == nil {
		events = make([]AuditEvent, 0)
	}
	return &AuditEventsPage{
		Events:       events,
		NextPage:     resp.NextPage,
		OpcRequestID: resp.OpcRequestID,
	}, nil
}
