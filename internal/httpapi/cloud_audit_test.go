package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ocicore/internal/config"
	"ocicore/internal/cryptobox"
	"ocicore/internal/instancesvc"
	"ocicore/internal/ociclient"
	"ocicore/internal/ociconn"
	"ocicore/internal/store"
)

// sanitizedRealCloudAuditPage 来自真实 OCI Audit ListEvents 响应脱敏后的完整 JSON。
const sanitizedRealCloudAuditPage = `[
  {
    "cloudEventsVersion": "0.1",
    "contentType": "application/json",
    "data": {
      "additionalDetails": { "X-Real-Port": 56010 },
      "availabilityDomain": "AD2",
      "compartmentId": "ocid1.tenancy.oc1..exampleuniqueid",
      "compartmentName": "demo-tenancy",
      "definedTags": null,
      "eventGroupingId": "oci-demo-group/req-001",
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
          "Authorization": ["Signature version=\"1\",keyId=\"demo\",signature=\"secret\""],
          "Date": ["Wed, 25 Nov 2026 08:16:04 GMT"]
        },
        "id": "oci-demo-req/req-001",
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
          "Content-Type": ["application/json"],
          "opc-request-id": ["oci-demo-req/req-001"]
        },
        "message": "ListInstances succeeded",
        "payload": {},
        "responseTime": "2026-11-25T08:16:04.670Z",
        "status": "200"
      },
      "stateChange": { "current": null, "previous": null }
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
      "additionalDetails": { "X-Real-Port": 51912 },
      "availabilityDomain": "AD3",
      "compartmentId": "ocid1.tenancy.oc1..exampleuniqueid",
      "compartmentName": "demo-tenancy",
      "definedTags": null,
      "eventGroupingId": "oci-demo-group/req-002",
      "eventName": "LaunchInstance",
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
        "action": "POST",
        "headers": { "Content-Type": ["application/json"] },
        "id": "oci-demo-req/req-002",
        "parameters": {},
        "path": "/20160918/instances"
      },
      "resourceId": "ocid1.instance.oc1.phx.exampleuniqueid",
      "resourceName": "arm-phx-01",
      "response": {
        "headers": { "Content-Type": ["application/json"] },
        "message": "Out of host capacity.",
        "payload": { "code": "InternalError" },
        "responseTime": "2026-11-25T08:18:10.120Z",
        "status": "500"
      },
      "stateChange": { "current": null, "previous": null }
    },
    "eventId": "2e64af71-6b60-457b-b96d-40bf3021c243",
    "eventTime": "2026-11-25T08:18:10.120Z",
    "eventType": "com.oraclecloud.computeApi.LaunchInstance",
    "eventTypeVersion": "2.0",
    "source": "computeApi"
  }
]`

func newCloudAuditHarness(t *testing.T, ociHandler http.HandlerFunc) (*harness, *store.Account) {
	t.Helper()

	ociSrv := httptest.NewServer(ociHandler)
	t.Cleanup(ociSrv.Close)
	target, _ := url.Parse(ociSrv.URL)

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fp := ociclient.FingerprintOf(&rsaKey.PublicKey)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey),
	}))

	boxKey := make([]byte, cryptobox.KeySize)
	if _, err := rand.Read(boxKey); err != nil {
		t.Fatal(err)
	}
	box, err := cryptobox.New(boxKey)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "audit.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	conns := ociconn.New(st)
	bus := instancesvc.NewBus()
	api := New(Deps{
		Store:     st,
		Config:    config.Config{SessionTTL: time.Hour},
		Conns:     conns,
		Instances: instancesvc.New(st, conns, bus),
	})
	t.Cleanup(api.jobs.Wait)

	api.testClientFor = func(_ context.Context, acc *store.Account) (*ociclient.Client, error) {
		return ociclient.New(&ociclient.Credentials{
			TenancyOCID: acc.TenancyOCID,
			UserOCID:    acc.UserOCID,
			Fingerprint: fp,
			Region:      acc.DefaultRegion,
			PrivateKey:  rsaKey,
		}, ociclient.WithHTTPClient(&http.Client{Transport: rewriteTransport{target}}), ociclient.WithMaxRetries(0))
	}

	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, srv: srv, store: st, client: &http.Client{Jar: jar}, bus: bus}
	h.setupAndLogin()

	acc, err := st.CreateAccount(t.Context(), store.NewAccount{
		Alias:         "凤凰城测试",
		Code:          "PHX",
		TenancyOCID:   "ocid1.tenancy.oc1..exampleuniqueid",
		UserOCID:      "ocid1.user.oc1..exampleuniqueid",
		Fingerprint:   fp,
		PrivateKeyPEM: keyPEM,
		DefaultRegion: "us-phoenix-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountIdentity(t.Context(), acc.ID, store.AccountIdentity{
		Regions:     []string{"us-phoenix-1", "us-ashburn-1"},
		HomeRegion:  "us-phoenix-1",
		TenancyName: "demo-tenancy",
	}); err != nil {
		t.Fatal(err)
	}
	acc, err = st.GetAccount(t.Context(), acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	return h, acc
}

func TestCloudAuditSuccessAndPagination(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	h, acc := newCloudAuditHarness(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("opc-next-page", "cursor-page-2")
		_, _ = w.Write([]byte(sanitizedRealCloudAuditPage))
	})

	resp := h.get("/api/accounts/" + acc.ID + "/cloud-audit?hours=6&page=cursor-page-1")
	expectStatus(t, resp, http.StatusOK)
	out := decode[CloudAuditResponse](t, resp)

	if gotPath != "/20190901/auditEvents" {
		t.Errorf("OCI 路径 = %q", gotPath)
	}
	if gotQuery.Get("page") != "cursor-page-1" {
		t.Errorf("OCI page = %q", gotQuery.Get("page"))
	}
	if gotQuery.Get("compartmentId") != acc.TenancyOCID {
		t.Errorf("OCI compartmentId = %q", gotQuery.Get("compartmentId"))
	}
	if out.Region != "us-phoenix-1" || !out.IsHomeRegion || out.RetentionDays != 365 || out.MaxSpanDays != 31 || out.Order != "asc" {
		t.Errorf("响应元信息不对: %+v", out)
	}
	if !out.HasMore || out.NextPage != "cursor-page-2" {
		t.Errorf("分页字段不对: hasMore=%v nextPage=%q", out.HasMore, out.NextPage)
	}
	if len(out.Events) != 2 {
		t.Fatalf("事件数 = %d，期望 2", len(out.Events))
	}

	ev0 := out.Events[0]
	if ev0.EventName != "ListInstances" || ev0.Action != "GET" || ev0.IsWrite || ev0.IsError || ev0.StatusCode != 200 {
		t.Errorf("事件 0 摘要不对: %+v", ev0)
	}
	if auth := ev0.Detail.RequestHeaders["Authorization"]; len(auth) != 1 || auth[0] != "[REDACTED]" {
		t.Errorf("Authorization 请求头必须脱敏，实际 %v", auth)
	}

	ev1 := out.Events[1]
	if ev1.EventName != "LaunchInstance" || !ev1.IsWrite || !ev1.IsError || ev1.StatusCode != 500 || ev1.ResourceName != "arm-phx-01" {
		t.Errorf("事件 1 写操作/失败标记不对: %+v", ev1)
	}
}

func TestCloudAuditValidationErrors(t *testing.T) {
	called := false
	h, acc := newCloudAuditHarness(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	})

	now := time.Now().UTC().Truncate(time.Second)
	cases := []struct {
		name     string
		query    string
		wantCode string
	}{
		{"区域注入拦截", "region=evil.com/", "invalid_region"},
		{"非法区间OCID", "compartmentId=ocid1.instance.oc1..bad", "invalid_compartment"},
		{"非法startTime", "startTime=not-a-time", "invalid_time_range"},
		{"非法hours", "hours=0", "invalid_time_range"},
		{"hours超限", "hours=800", "invalid_time_range"},
		{"结束早于开始", "startTime=" + url.QueryEscape(now.Format(time.RFC3339)) + "&endTime=" + url.QueryEscape(now.Add(-time.Hour).Format(time.RFC3339)), "invalid_time_range"},
		{"跨度超过31天", "startTime=" + url.QueryEscape(now.Add(-32*24*time.Hour).Format(time.RFC3339)) + "&endTime=" + url.QueryEscape(now.Format(time.RFC3339)), "time_range_too_large"},
		{"超过365天保留期", "startTime=" + url.QueryEscape(now.Add(-366*24*time.Hour).Format(time.RFC3339)) + "&endTime=" + url.QueryEscape(now.Add(-360*24*time.Hour).Format(time.RFC3339)), "beyond_retention"},
		{"分页游标过长", "page=" + strings.Repeat("x", maxCloudAuditPageLen+1), "invalid_page"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.get("/api/accounts/" + acc.ID + "/cloud-audit?" + tc.query)
			expectStatus(t, resp, http.StatusBadRequest)
			body := decode[errorBody](t, resp)
			if body.Code != tc.wantCode {
				t.Errorf("code = %q，期望 %q (msg=%q)", body.Code, tc.wantCode, body.Message)
			}
		})
	}
	if called {
		t.Fatal("参数校验失败时绝不应发出 OCI 请求")
	}
}

func TestCloudAuditPermissionHint(t *testing.T) {
	h, acc := newCloudAuditHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"NotAuthorizedOrNotFound","message":"Authorization failed or requested resource not found."}`))
	})

	resp := h.get("/api/accounts/" + acc.ID + "/cloud-audit?hours=1")
	expectStatus(t, resp, http.StatusForbidden)
	body := decode[errorBody](t, resp)
	if body.Code != "oci_error" || body.OciCode != "NotAuthorizedOrNotFound" {
		t.Errorf("错误码不对: %+v", body)
	}
	if !strings.Contains(body.Advice, "read audit-events") {
		t.Errorf("缺权限提示应包含 read audit-events，实际: %q", body.Advice)
	}
}

func TestAccountCompartmentsNormalAndPartialFallback(t *testing.T) {
	t.Run("正常返回根区间与多层同名子区间路径", func(t *testing.T) {
		var gotQuery url.Values
		h, acc := newCloudAuditHarness(t, func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.Query()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"id":"ocid1.compartment.oc1..staging-app","compartmentId":"ocid1.compartment.oc1..staging","name":"app","lifecycleState":"ACTIVE"},
				{"id":"ocid1.compartment.oc1..prod","compartmentId":"ocid1.tenancy.oc1..exampleuniqueid","name":"prod","lifecycleState":"ACTIVE"},
				{"id":"ocid1.compartment.oc1..prod-app","compartmentId":"ocid1.compartment.oc1..prod","name":"app","lifecycleState":"ACTIVE"},
				{"id":"ocid1.compartment.oc1..staging","compartmentId":"ocid1.tenancy.oc1..exampleuniqueid","name":"staging","lifecycleState":"ACTIVE"}
			]`))
		})

		resp := h.get("/api/accounts/" + acc.ID + "/compartments")
		expectStatus(t, resp, http.StatusOK)
		out := decode[struct {
			Compartments         []AccountCompartmentItem `json:"compartments"`
			DefaultCompartmentID string                   `json:"defaultCompartmentId"`
			Partial              bool                     `json:"partial"`
		}](t, resp)
		if gotQuery.Get("compartmentIdInSubtree") != "true" || gotQuery.Get("accessLevel") != "ANY" {
			t.Errorf("OCI /compartments 必须带 compartmentIdInSubtree=true&accessLevel=ANY，实际 %v", gotQuery)
		}
		if out.Partial || len(out.Compartments) != 5 {
			t.Fatalf("区间返回不对: %+v", out)
		}
		if out.DefaultCompartmentID != acc.TenancyOCID {
			t.Errorf("defaultCompartmentId = %q，期望 %q", out.DefaultCompartmentID, acc.TenancyOCID)
		}
		if !out.Compartments[0].IsRoot || out.Compartments[0].ID != acc.TenancyOCID || out.Compartments[0].Path != "demo-tenancy" {
			t.Errorf("首项应为根区间: %+v", out.Compartments[0])
		}
		wantPaths := []string{"prod", "prod / app", "staging", "staging / app"}
		for i, want := range wantPaths {
			got := out.Compartments[i+1].Path
			if got != want {
				t.Errorf("子区间[%d] path = %q，期望 %q (items=%+v)", i+1, got, want, out.Compartments)
			}
		}
	})

	t.Run("账号配置子区间时defaultCompartmentId对齐该子区间", func(t *testing.T) {
		h, acc := newCloudAuditHarness(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"id":"ocid1.compartment.oc1..sub","compartmentId":"ocid1.tenancy.oc1..exampleuniqueid","name":"sub","lifecycleState":"ACTIVE"}
			]`))
		})
		subID := "ocid1.compartment.oc1..sub"
		if _, err := h.store.UpdateAccount(t.Context(), acc.ID, store.AccountUpdate{CompartmentOCID: &subID}); err != nil {
			t.Fatal(err)
		}

		resp := h.get("/api/accounts/" + acc.ID + "/compartments")
		expectStatus(t, resp, http.StatusOK)
		out := decode[struct {
			Compartments         []AccountCompartmentItem `json:"compartments"`
			DefaultCompartmentID string                   `json:"defaultCompartmentId"`
			Partial              bool                     `json:"partial"`
		}](t, resp)
		if out.DefaultCompartmentID != subID {
			t.Errorf("defaultCompartmentId = %q，期望 %q", out.DefaultCompartmentID, subID)
		}
	})

	t.Run("缺权限时降级返回根区间", func(t *testing.T) {
		h, acc := newCloudAuditHarness(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NotAuthorizedOrNotFound","message":"Authorization failed"}`))
		})

		resp := h.get("/api/accounts/" + acc.ID + "/compartments")
		expectStatus(t, resp, http.StatusOK)
		out := decode[struct {
			Compartments         []AccountCompartmentItem `json:"compartments"`
			DefaultCompartmentID string                   `json:"defaultCompartmentId"`
			Partial              bool                     `json:"partial"`
		}](t, resp)
		if !out.Partial || len(out.Compartments) != 1 || !out.Compartments[0].IsRoot || out.DefaultCompartmentID != acc.TenancyOCID {
			t.Fatalf("无权限时应优雅降级返回根区间并标 partial=true: %+v", out)
		}
	})
}

func TestSanitizeAuditHeaders(t *testing.T) {
	in := map[string][]string{
		"Accept":              {"application/json"},
		"Content-Type":        {"application/json"},
		"opc-request-id":      {"req-12345"},
		"opc-retry-token":     {"retry-idempotency-key-1"},
		"Authorization":       {"Signature secret"},
		"Proxy-Authorization": {"Basic secret"},
		"Cookie":              {"session=abc"},
		"Set-Cookie":          {"session=xyz"},
		"X-Api-Key":           {"k-123"},
		"X-Apikey":            {"k-456"},
		"opc-obo-token":       {"obo-secret"},
		"X-Auth-Token":        {"tok-secret"},
		"X-Custom-Secret":     {"s-789"},
	}
	got := sanitizeAuditHeaders(in)

	for _, keep := range []string{"Accept", "Content-Type", "opc-request-id", "opc-retry-token"} {
		if len(got[keep]) != 1 || got[keep][0] != in[keep][0] {
			t.Errorf("正常头 %s 不应被误脱敏: got=%v want=%v", keep, got[keep], in[keep])
		}
	}
	for _, redact := range []string{
		"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie",
		"X-Api-Key", "X-Apikey", "opc-obo-token", "X-Auth-Token", "X-Custom-Secret",
	} {
		if len(got[redact]) != 1 || got[redact][0] != "[REDACTED]" {
			t.Errorf("敏感头 %s 应脱敏为 [REDACTED]，实际 %v", redact, got[redact])
		}
	}
}

func TestResolveCompartmentPathCycleSafe(t *testing.T) {
	byID := map[string]ociclient.Compartment{
		"a": {ID: "a", CompartmentID: "b", Name: "comp-a"},
		"b": {ID: "b", CompartmentID: "a", Name: "comp-b"},
	}
	got := resolveCompartmentPath("a", "root", byID, map[string]string{})
	if got != "comp-b / comp-a" {
		t.Errorf("存在父子环时应安全中止并返回非空路径，实际 %q", got)
	}
}
