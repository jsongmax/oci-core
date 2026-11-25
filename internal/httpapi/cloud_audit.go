package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ocicore/internal/i18n"
	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

const (
	defaultCloudAuditHours   = 24
	maxCloudAuditPageLen     = 2048
	cloudAuditQueryTimeout   = 30 * time.Second
	compartmentsQueryTimeout = 20 * time.Second
)

// CloudAuditEvent 是返回给前端的 OCI 云端审计事件视图。
// 常用列拍平在顶层、可空字段规整为空串；原始嵌套详情放在 Detail 中并已剥离鉴权头。
type CloudAuditEvent struct {
	EventID            string           `json:"eventId"`
	EventTime          time.Time        `json:"eventTime"`
	EventType          string           `json:"eventType"`
	EventName          string           `json:"eventName"`
	Source             string           `json:"source"`
	CompartmentID      string           `json:"compartmentId"`
	CompartmentName    string           `json:"compartmentName"`
	ResourceID         string           `json:"resourceId"`
	ResourceName       string           `json:"resourceName"`
	AvailabilityDomain string           `json:"availabilityDomain"`
	EventGroupingID    string           `json:"eventGroupingId"`
	Action             string           `json:"action"`
	Path               string           `json:"path"`
	RequestID          string           `json:"requestId"`
	IsWrite            bool             `json:"isWrite"`
	Status             string           `json:"status"`
	StatusCode         int              `json:"statusCode"`
	IsError            bool             `json:"isError"`
	Message            string           `json:"message"`
	ResponseTime       string           `json:"responseTime"`
	PrincipalID        string           `json:"principalId"`
	PrincipalName      string           `json:"principalName"`
	CallerID           string           `json:"callerId"`
	CallerName         string           `json:"callerName"`
	AuthType           string           `json:"authType"`
	IPAddress          string           `json:"ipAddress"`
	UserAgent          string           `json:"userAgent"`
	Detail             CloudAuditDetail `json:"detail"`
}

// CloudAuditDetail 是展开单条事件时查看的结构化详情。
type CloudAuditDetail struct {
	CloudEventsVersion string                      `json:"cloudEventsVersion"`
	EventTypeVersion   string                      `json:"eventTypeVersion"`
	ContentType        string                      `json:"contentType"`
	RequestParameters  map[string][]string         `json:"requestParameters,omitempty"`
	RequestHeaders     map[string][]string         `json:"requestHeaders,omitempty"`
	ResponseHeaders    map[string][]string         `json:"responseHeaders,omitempty"`
	ResponsePayload    map[string]any              `json:"responsePayload,omitempty"`
	StateChange        *ociclient.AuditStateChange `json:"stateChange,omitempty"`
	AdditionalDetails  map[string]any              `json:"additionalDetails,omitempty"`
}

// CloudAuditResponse 是 GET /api/accounts/{id}/cloud-audit 的完整响应。
type CloudAuditResponse struct {
	AccountID     string            `json:"accountId"`
	Region        string            `json:"region"`
	HomeRegion    string            `json:"homeRegion"`
	IsHomeRegion  bool              `json:"isHomeRegion"`
	CompartmentID string            `json:"compartmentId"`
	StartTime     time.Time         `json:"startTime"`
	EndTime       time.Time         `json:"endTime"`
	RetentionDays int               `json:"retentionDays"`
	MaxSpanDays   int               `json:"maxSpanDays"`
	Order         string            `json:"order"`
	NextPage      string            `json:"nextPage"`
	HasMore       bool              `json:"hasMore"`
	Notice        string            `json:"notice"`
	Events        []CloudAuditEvent `json:"events"`
}

// AccountCompartmentItem 是 GET /api/accounts/{id}/compartments 返回的区间项。
type AccountCompartmentItem struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Path           string `json:"path,omitempty"`
	IsRoot         bool   `json:"isRoot"`
	LifecycleState string `json:"lifecycleState"`
}

func (s *Server) accountClient(ctx context.Context, acc *store.Account) (*ociclient.Client, error) {
	if s.testClientFor != nil {
		return s.testClientFor(ctx, acc)
	}
	return s.conns.For(ctx, acc)
}

// handleAccountCloudAudit 查询指定账号在 OCI 官方 Audit 服务中的云端审计日志（单页只读）。
func (s *Server) handleAccountCloudAudit(w http.ResponseWriter, r *http.Request) {
	acc, err := s.st.GetAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}

	q := r.URL.Query()

	// 1. 区域解析与防注入校验
	homeRegion := ociclient.NormalizeRegion(acc.HomeRegion)
	region := ""
	if rawRegion := strings.TrimSpace(q.Get("region")); rawRegion != "" {
		region = ociclient.NormalizeRegion(rawRegion)
		if region == "" {
			writeError(w, http.StatusBadRequest, "invalid_region",
				i18n.M("api.cloud_audit.invalid_region", rawRegion))
			return
		}
	} else if homeRegion != "" {
		region = homeRegion
	} else {
		region = ociclient.NormalizeRegion(acc.DefaultRegion)
	}

	// 2. 区间 OCID 解析与格式校验
	compartmentID := strings.TrimSpace(q.Get("compartmentId"))
	if compartmentID == "" {
		if acc.CompartmentOCID != "" {
			compartmentID = acc.CompartmentOCID
		} else {
			compartmentID = acc.TenancyOCID
		}
	} else if !isValidCompartmentOCID(compartmentID) {
		writeError(w, http.StatusBadRequest, "invalid_compartment",
			i18n.M("api.cloud_audit.invalid_compartment"))
		return
	}

	// 3. 时间窗解析与边界校验
	now := time.Now().UTC().Truncate(time.Second)
	startTime, endTime, code, msg, ok := parseCloudAuditRange(q.Get("startTime"), q.Get("endTime"), q.Get("hours"), now)
	if !ok {
		writeError(w, http.StatusBadRequest, code, msg)
		return
	}

	// 4. 分页游标校验
	page := strings.TrimSpace(q.Get("page"))
	if len(page) > maxCloudAuditPageLen {
		writeError(w, http.StatusBadRequest, "invalid_page", i18n.M("api.cloud_audit.invalid_page"))
		return
	}

	client, err := s.accountClient(r.Context(), acc)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), cloudAuditQueryTimeout)
	defer cancel()

	res, err := client.ListAuditEvents(ctx, ociclient.ListAuditEventsOptions{
		Region:        region,
		CompartmentID: compartmentID,
		StartTime:     startTime,
		EndTime:       endTime,
		Page:          page,
	})
	if err != nil {
		writeOCIErrorWithPermHint(w, err, i18n.M("oci.audit_perm_hint"))
		return
	}

	events := make([]CloudAuditEvent, 0, len(res.Events))
	for _, ev := range res.Events {
		events = append(events, toCloudAuditEvent(ev))
	}

	writeJSON(w, http.StatusOK, CloudAuditResponse{
		AccountID:     acc.ID,
		Region:        region,
		HomeRegion:    homeRegion,
		IsHomeRegion:  homeRegion != "" && homeRegion == region,
		CompartmentID: compartmentID,
		StartTime:     startTime,
		EndTime:       endTime,
		RetentionDays: ociclient.AuditRetentionDays,
		MaxSpanDays:   ociclient.AuditMaxSpanDays,
		Order:         "asc",
		NextPage:      res.NextPage,
		HasMore:       res.NextPage != "",
		Notice:        i18n.M("api.cloud_audit.notice"),
		Events:        events,
	})
}

// handleAccountCompartments 返回该账号可选择的区间列表（根区间 + 活跃子区间）。
// 若账号缺少 inspect compartments 权限，优雅降级只返回根区间（及账号配置区间）并置 partial=true。
func (s *Server) handleAccountCompartments(w http.ResponseWriter, r *http.Request) {
	acc, err := s.st.GetAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}

	rootName := strings.TrimSpace(acc.TenancyName)
	if rootName == "" {
		rootName = "root"
	}
	items := []AccountCompartmentItem{
		{
			ID:             acc.TenancyOCID,
			Name:           rootName,
			Path:           rootName,
			IsRoot:         true,
			LifecycleState: "ACTIVE",
		},
	}
	seen := map[string]bool{acc.TenancyOCID: true}

	client, err := s.accountClient(r.Context(), acc)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), compartmentsQueryTimeout)
	defer cancel()

	partial := false
	subs, err := client.ListAllCompartments(ctx, acc.TenancyOCID)
	if err != nil {
		if apiErr, ok := ociclient.AsAPIError(err); ok && apiErr.Class == ociclient.ClassNotAuthorized {
			partial = true
		} else {
			writeOCIError(w, err)
			return
		}
	} else {
		byID := make(map[string]ociclient.Compartment, len(subs))
		for _, cp := range subs {
			if cp.ID != "" {
				byID[cp.ID] = cp
			}
		}
		pathCache := make(map[string]string, len(subs))
		subItems := make([]AccountCompartmentItem, 0, len(subs))
		for _, cp := range subs {
			if cp.ID == "" || seen[cp.ID] {
				continue
			}
			seen[cp.ID] = true
			subItems = append(subItems, AccountCompartmentItem{
				ID:             cp.ID,
				Name:           cp.Name,
				Path:           resolveCompartmentPath(cp.ID, acc.TenancyOCID, byID, pathCache),
				IsRoot:         false,
				LifecycleState: cp.LifecycleState,
			})
		}
		sort.Slice(subItems, func(i, j int) bool {
			pi := strings.ToLower(subItems[i].Path)
			pj := strings.ToLower(subItems[j].Path)
			if pi != pj {
				return pi < pj
			}
			return subItems[i].ID < subItems[j].ID
		})
		items = append(items, subItems...)
	}

	// 若账号单独配置了子区间且不在结果中（例如无权限列出子区间），补入可选列表。
	if acc.CompartmentOCID != "" && !seen[acc.CompartmentOCID] {
		items = append(items, AccountCompartmentItem{
			ID:             acc.CompartmentOCID,
			Name:           acc.CompartmentOCID,
			Path:           acc.CompartmentOCID,
			IsRoot:         false,
			LifecycleState: "ACTIVE",
		})
	}

	defaultCompartmentID := acc.CompartmentOCID
	if defaultCompartmentID == "" {
		defaultCompartmentID = acc.TenancyOCID
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"compartments":         items,
		"defaultCompartmentId": defaultCompartmentID,
		"partial":              partial,
	})
}

func isValidCompartmentOCID(id string) bool {
	if strings.ContainsAny(id, " \t\r\n/?#&") {
		return false
	}
	return strings.HasPrefix(id, "ocid1.tenancy.") || strings.HasPrefix(id, "ocid1.compartment.")
}

func parseCloudAuditRange(rawStart, rawEnd, rawHours string, now time.Time) (start, end time.Time, errCode, errMsg string, ok bool) {
	rawStart = strings.TrimSpace(rawStart)
	rawEnd = strings.TrimSpace(rawEnd)
	rawHours = strings.TrimSpace(rawHours)

	if rawEnd != "" {
		t, err := time.Parse(time.RFC3339Nano, rawEnd)
		if err != nil {
			return time.Time{}, time.Time{}, "invalid_time_range", i18n.M("api.cloud_audit.invalid_time", "endTime"), false
		}
		end = t.UTC()
	} else {
		end = now
	}

	if rawStart != "" {
		t, err := time.Parse(time.RFC3339Nano, rawStart)
		if err != nil {
			return time.Time{}, time.Time{}, "invalid_time_range", i18n.M("api.cloud_audit.invalid_time", "startTime"), false
		}
		start = t.UTC()
	} else {
		hours := defaultCloudAuditHours
		maxHours := ociclient.AuditMaxSpanDays * 24
		if rawHours != "" {
			h, err := strconv.Atoi(rawHours)
			if err != nil || h <= 0 || h > maxHours {
				return time.Time{}, time.Time{}, "invalid_time_range", i18n.M("api.cloud_audit.invalid_hours", maxHours), false
			}
			hours = h
		}
		start = end.Add(-time.Duration(hours) * time.Hour)
	}

	if !end.After(start) {
		return time.Time{}, time.Time{}, "invalid_time_range", i18n.M("api.cloud_audit.range_order"), false
	}
	if end.Sub(start) > ociclient.AuditMaxSpan {
		return time.Time{}, time.Time{}, "time_range_too_large", i18n.M("api.cloud_audit.span_limit", ociclient.AuditMaxSpanDays), false
	}
	oldest := now.Add(-ociclient.AuditRetentionWindow - time.Minute)
	if start.Before(oldest) {
		return time.Time{}, time.Time{}, "beyond_retention", i18n.M("api.cloud_audit.beyond_retention", ociclient.AuditRetentionDays, ociclient.AuditRetentionDays), false
	}
	return start, end, "", "", true
}

func toCloudAuditEvent(ev ociclient.AuditEvent) CloudAuditEvent {
	action := strings.ToUpper(strings.TrimSpace(ev.Data.Request.Action))
	statusStr := strings.TrimSpace(ev.Data.Response.Status)
	statusCode, _ := strconv.Atoi(statusStr)

	return CloudAuditEvent{
		EventID:            ev.EventID,
		EventTime:          ev.EventTime,
		EventType:          ev.EventType,
		EventName:          ev.Data.EventName,
		Source:             ev.Source,
		CompartmentID:      ev.Data.CompartmentID,
		CompartmentName:    ev.Data.CompartmentName,
		ResourceID:         derefString(ev.Data.ResourceID),
		ResourceName:       ev.Data.ResourceName,
		AvailabilityDomain: ev.Data.AvailabilityDomain,
		EventGroupingID:    ev.Data.EventGroupingID,
		Action:             action,
		Path:               ev.Data.Request.Path,
		RequestID:          ev.Data.Request.ID,
		IsWrite:            isWriteAction(action),
		Status:             statusStr,
		StatusCode:         statusCode,
		IsError:            statusCode >= 400,
		Message:            ev.Data.Response.Message,
		ResponseTime:       ev.Data.Response.ResponseTime,
		PrincipalID:        ev.Data.Identity.PrincipalID,
		PrincipalName:      derefString(ev.Data.Identity.PrincipalName),
		CallerID:           derefString(ev.Data.Identity.CallerID),
		CallerName:         derefString(ev.Data.Identity.CallerName),
		AuthType:           ev.Data.Identity.AuthType,
		IPAddress:          ev.Data.Identity.IPAddress,
		UserAgent:          ev.Data.Identity.UserAgent,
		Detail: CloudAuditDetail{
			CloudEventsVersion: ev.CloudEventsVersion,
			EventTypeVersion:   ev.EventTypeVersion,
			ContentType:        ev.ContentType,
			RequestParameters:  ev.Data.Request.Parameters,
			RequestHeaders:     sanitizeAuditHeaders(ev.Data.Request.Headers),
			ResponseHeaders:    sanitizeAuditHeaders(ev.Data.Response.Headers),
			ResponsePayload:    ev.Data.Response.Payload,
			StateChange:        ev.Data.StateChange,
			AdditionalDetails:  ev.Data.AdditionalDetails,
		},
	}
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func isWriteAction(action string) bool {
	switch action {
	case "", http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func sanitizeAuditHeaders(headers map[string][]string) map[string][]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string][]string, len(headers))
	for k, v := range headers {
		if isSensitiveAuditHeader(k) {
			out[k] = []string{"[REDACTED]"}
		} else {
			cp := make([]string, len(v))
			copy(cp, v)
			out[k] = cp
		}
	}
	return out
}

func resolveCompartmentPath(id, rootID string, byID map[string]ociclient.Compartment, cache map[string]string) string {
	if p, ok := cache[id]; ok {
		return p
	}
	visited := make(map[string]bool)
	var parts []string
	cur := id
	for cur != "" && cur != rootID && !visited[cur] {
		cp, ok := byID[cur]
		if !ok {
			break
		}
		visited[cur] = true
		name := strings.TrimSpace(cp.Name)
		if name == "" {
			name = cp.ID
		}
		parts = append(parts, name)
		cur = cp.CompartmentID
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	path := strings.Join(parts, " / ")
	cache[id] = path
	return path
}

func isSensitiveAuditHeader(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch lower {
	// opc-retry-token 是 OCI 幂等重试标识，不属于认证凭据。
	case "opc-retry-token":
		return false
	case "authorization", "proxy-authorization", "cookie", "set-cookie",
		"x-api-key", "opc-obo-token", "x-auth-token":
		return true
	}
	for _, needle := range []string{"token", "secret", "cookie", "authorization", "api-key", "apikey"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}
