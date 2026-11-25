package ociclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// sanitizedRealAuditEventsJSON 来自真实 OCI Audit API (GET /20190901/auditEvents) 实测响应，
// 100% 保留原始 JSON 键名与嵌套层级，仅对 OCID、邮箱、IP、请求头鉴权字段等敏感值脱敏。
const sanitizedRealAuditEventsJSON = `[
  {
    "cloudEventsVersion": "0.1",
    "contentType": "application/json",
    "data": {
      "additionalDetails": {
        "X-Real-Port": 56010
      },
      "availabilityDomain": "AD2",
      "compartmentId": "ocid1.tenancy.oc1..exampleuniqueid",
      "compartmentName": "demo-tenancy",
      "definedTags": null,
      "eventGroupingId": "oci-demo-group-01/req-a/sub-1",
      "eventName": "ListInstances",
      "freeformTags": null,
      "identity": {
        "authType": "natv",
        "callerId": null,
        "callerName": null,
        "consoleSessionId": null,
        "credentials": "########",
        "ipAddress": "203.0.113.42",
        "principalId": "ocid1.user.oc1..exampleuniqueid",
        "principalName": "admin@example.com",
        "tenantId": "ocid1.tenancy.oc1..exampleuniqueid",
        "userAgent": "OCICore/0.1 (+https://github.com/ocicore)"
      },
      "request": {
        "action": "GET",
        "headers": {
          "Accept": ["application/json"],
          "Date": ["Wed, 25 Nov 2026 08:16:04 GMT"]
        },
        "id": "oci-demo-req-01/req-a/sub-1",
        "parameters": {
          "compartmentId": ["ocid1.tenancy.oc1..exampleuniqueid"],
          "limit": ["100"]
        },
        "path": "/20160918/instances"
      },
      "resourceId": null,
      "resourceName": "",
      "response": {
        "headers": {
          "Content-Encoding": ["gzip"],
          "Content-Length": ["1849"],
          "Content-Type": ["application/json"],
          "opc-request-id": ["oci-demo-req-01/req-a/sub-1"]
        },
        "message": "ListInstances succeeded",
        "payload": {},
        "responseTime": "2026-11-25T08:16:04.670Z",
        "status": "200"
      },
      "stateChange": {
        "current": null,
        "previous": null
      }
    },
    "eventId": "89452f46-04dc-4ccb-8259-e386a7f5c051",
    "eventTime": "2026-11-25T08:16:04.670Z",
    "eventType": "com.oraclecloud.computeApi.ListInstances",
    "eventTypeVersion": "2.0",
    "source": "computeApi"
  },
  {
    "cloudEventsVersion": "0.1",
    "contentType": "application/json",
    "data": {
      "additionalDetails": {
        "X-Real-Port": 51912
      },
      "availabilityDomain": "AD3",
      "compartmentId": "ocid1.tenancy.oc1..exampleuniqueid",
      "compartmentName": "demo-tenancy",
      "definedTags": null,
      "eventGroupingId": "oci-demo-group-02/req-b/sub-2",
      "eventName": "SummarizeMetricsData",
      "freeformTags": null,
      "identity": {
        "authType": "resource",
        "callerId": null,
        "callerName": null,
        "consoleSessionId": null,
        "credentials": "########",
        "ipAddress": "198.51.100.10",
        "principalId": "ocid1.optimizervrp.oc1.phx.exampleuniqueid",
        "principalName": null,
        "tenantId": "ocid1.tenancy.oc1..exampleuniqueid",
        "userAgent": "Oracle-JavaSDK/2.92.0"
      },
      "request": {
        "action": "POST",
        "headers": {
          "Accept": ["application/json"],
          "Content-Type": ["application/json"]
        },
        "id": "oci-demo-req-02/req-b/sub-2",
        "parameters": {
          "compartmentId": ["ocid1.tenancy.oc1..exampleuniqueid"],
          "compartmentIdInSubtree": ["true"]
        },
        "path": "/20180401/metrics/actions/summarizeMetricsData"
      },
      "resourceId": "actions",
      "resourceName": "",
      "response": {
        "headers": {
          "Content-Length": ["3"],
          "Content-Type": ["application/json"]
        },
        "message": "SummarizeMetricsData succeeded",
        "payload": {},
        "responseTime": "2026-11-25T08:20:08.488Z",
        "status": "200"
      },
      "stateChange": {
        "current": {},
        "previous": {}
      }
    },
    "eventId": "2e64af71-6b60-457b-b96d-40bf3021c243",
    "eventTime": "2026-11-25T08:20:08.488Z",
    "eventType": "com.oraclecloud.PublicApiTelemetry.SummarizeMetricsData",
    "eventTypeVersion": "2.0",
    "source": "PublicApiTelemetry"
  }
]`

func TestAuditEndpointAndInjectionGuard(t *testing.T) {
	if got := Endpoint(ServiceAudit, "us-phoenix-1"); got != "https://audit.us-phoenix-1.oraclecloud.com" {
		t.Errorf("Endpoint(ServiceAudit, us-phoenix-1) = %q", got)
	}
	if got := Endpoint(ServiceAudit, "phx"); got != "https://audit.us-phoenix-1.oraclecloud.com" {
		t.Errorf("Endpoint(ServiceAudit, phx) = %q", got)
	}

	c, err := New(testCreds(t, testKey(t)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := c.ListAuditEvents(context.Background(), ListAuditEventsOptions{
		Region:    "evil.com/",
		StartTime: now.Add(-time.Hour),
		EndTime:   now,
	}); err == nil {
		t.Fatal("非法区域名注入应被拒绝")
	}
}

func TestListAuditEventsParsesRealStructureAndPagination(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("opc-next-page", "opaque-cursor-page-2")
		w.Header().Set("opc-request-id", "req-audit-001")
		_, _ = w.Write([]byte(sanitizedRealAuditEventsJSON))
	}))
	defer srv.Close()

	target, _ := url.Parse(srv.URL)
	creds := testCreds(t, testKey(t))
	c, err := New(creds, WithHTTPClient(&http.Client{Transport: redirectTransport{target}}), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	start := now.Add(-2 * time.Hour)
	page, err := c.listAuditEventsAt(context.Background(), ListAuditEventsOptions{
		Region:    "phx",
		StartTime: start,
		EndTime:   now,
		Page:      "opaque-cursor-page-1",
	}, now)
	if err != nil {
		t.Fatalf("ListAuditEvents 失败: %v", err)
	}

	if gotPath != "/20190901/auditEvents" {
		t.Errorf("请求路径 = %q，期望 /20190901/auditEvents", gotPath)
	}
	if gotQuery.Get("compartmentId") != creds.TenancyOCID {
		t.Errorf("compartmentId = %q，期望 %q", gotQuery.Get("compartmentId"), creds.TenancyOCID)
	}
	if gotQuery.Get("startTime") != start.Format(time.RFC3339) {
		t.Errorf("startTime = %q", gotQuery.Get("startTime"))
	}
	if gotQuery.Get("endTime") != now.Format(time.RFC3339) {
		t.Errorf("endTime = %q", gotQuery.Get("endTime"))
	}
	if gotQuery.Get("page") != "opaque-cursor-page-1" {
		t.Errorf("page = %q", gotQuery.Get("page"))
	}
	if page.NextPage != "opaque-cursor-page-2" || page.OpcRequestID != "req-audit-001" {
		t.Errorf("分页元信息不对: %+v", page)
	}
	if len(page.Events) != 2 {
		t.Fatalf("事件数 = %d，期望 2", len(page.Events))
	}

	ev0 := page.Events[0]
	if ev0.EventID != "89452f46-04dc-4ccb-8259-e386a7f5c051" || ev0.Source != "computeApi" {
		t.Errorf("事件 0 基础字段不对: %+v", ev0)
	}
	if ev0.Data.EventName != "ListInstances" || ev0.Data.Request.Action != "GET" || ev0.Data.Response.Status != "200" {
		t.Errorf("事件 0 data 不对: %+v", ev0.Data)
	}
	if ev0.Data.Identity.PrincipalName == nil || *ev0.Data.Identity.PrincipalName != "admin@example.com" {
		t.Errorf("事件 0 principalName 不对: %+v", ev0.Data.Identity.PrincipalName)
	}
	if ev0.Data.ResourceID != nil {
		t.Errorf("事件 0 resourceId 应为 nil，实际 %v", *ev0.Data.ResourceID)
	}
	if ev0.Data.Identity.IPAddress != "203.0.113.42" {
		t.Errorf("事件 0 ipAddress = %q", ev0.Data.Identity.IPAddress)
	}

	ev1 := page.Events[1]
	if ev1.Data.Request.Action != "POST" || ev1.Data.Identity.PrincipalName != nil {
		t.Errorf("事件 1 可空字段与动作不对: %+v", ev1.Data)
	}
	if ev1.Data.ResourceID == nil || *ev1.Data.ResourceID != "actions" {
		t.Errorf("事件 1 resourceId 应为 actions，实际 %v", ev1.Data.ResourceID)
	}
}

func TestListAuditEventsValidation(t *testing.T) {
	c, err := New(testCreds(t, testKey(t)), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		opts ListAuditEventsOptions
	}{
		{
			name: "缺少时间",
			opts: ListAuditEventsOptions{},
		},
		{
			name: "起止相等",
			opts: ListAuditEventsOptions{StartTime: now, EndTime: now},
		},
		{
			name: "开始晚于结束",
			opts: ListAuditEventsOptions{StartTime: now, EndTime: now.Add(-time.Hour)},
		},
		{
			name: "跨度超过31天",
			opts: ListAuditEventsOptions{StartTime: now.Add(-32 * 24 * time.Hour), EndTime: now},
		},
		{
			name: "早于365天保留期",
			opts: ListAuditEventsOptions{StartTime: now.Add(-366 * 24 * time.Hour), EndTime: now.Add(-360 * 24 * time.Hour)},
		},
		{
			name: "分页游标过长",
			opts: ListAuditEventsOptions{StartTime: now.Add(-time.Hour), EndTime: now, Page: strings.Repeat("a", maxAuditPageTokenLen+1)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.listAuditEventsAt(context.Background(), tc.opts, now); err == nil {
				t.Fatalf("%s 应当返回错误", tc.name)
			}
		})
	}
}
