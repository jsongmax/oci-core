package ociclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// redirectTransport 把所有请求改发到测试服务器，路径与请求体原样保留。
type redirectTransport struct{ target *url.URL }

func (rt redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = rt.target.Scheme, rt.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

// TestCapacityReportAlwaysUsesTenancy 锁住容量报告只发租户 OCID。
//
// 这个接口拒绝子分区，而账号配置里的分区字段很可能填的就是子分区。
// 先前由调用方传那个字段，手动查询、容量监控、守候预检三处同时 404，
// 报错又只说"无权限或资源不存在"，很难联想到是分区传错了。
func TestCapacityReportAlwaysUsesTenancy(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"shapeAvailabilities":[]}`))
	}))
	defer srv.Close()

	target, _ := url.Parse(srv.URL)
	creds := testCreds(t, testKey(t))
	c, err := New(creds, WithHTTPClient(&http.Client{Transport: redirectTransport{target}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateCapacityReport(context.Background(), "ap-tokyo-1", "xxxx:AP-TOKYO-1-AD-1",
		[]CapacityShapeRequest{{InstanceShape: "VM.Standard.A1.Flex"}}); err != nil {
		t.Fatal(err)
	}
	if got["compartmentId"] != creds.TenancyOCID {
		t.Errorf("compartmentId 应为租户 OCID %q，实际 %v", creds.TenancyOCID, got["compartmentId"])
	}
}

// TestHasCapacityOnlyOnAvailable 锁住"只有 AVAILABLE 才算有货"。
//
// 这个判断决定抢机要不要发出真正的创建请求。放宽了（比如把未知状态也当有货）
// 就等于预检形同虚设；收紧错了（把 AVAILABLE 判成没货）则任务永远不动手，
// 而两种失效在界面上都不会报错。
func TestHasCapacityOnlyOnAvailable(t *testing.T) {
	const shape = "VM.Standard.A1.Flex"

	cases := []struct {
		status string
		want   bool
	}{
		{CapacityAvailable, true},
		{CapacityOutOfHost, false},
		{CapacityHardwareNotSupported, false},
		{"SOMETHING_NEW_FROM_ORACLE", false},
		{"", false},
	}
	for _, c := range cases {
		r := &CapacityReport{ShapeAvailabilities: []CapacityShapeAvailability{
			{InstanceShape: shape, AvailabilityStatus: c.status},
		}}
		if got := r.HasCapacity(shape); got != c.want {
			t.Errorf("状态 %q 时 HasCapacity = %v，期望 %v", c.status, got, c.want)
		}
	}
}

// TestHasCapacityMissingShapeIsFalse 报告里没提到这个规格时判定为"没有"。
//
// 宁可少试一次，也不要因为解析不到就退回"直接开干"——那正好是预检要防的事。
func TestHasCapacityMissingShapeIsFalse(t *testing.T) {
	r := &CapacityReport{ShapeAvailabilities: []CapacityShapeAvailability{
		{InstanceShape: "VM.Standard.E2.1.Micro", AvailabilityStatus: CapacityAvailable},
	}}
	if r.HasCapacity("VM.Standard.A1.Flex") {
		t.Error("报告里没有这个规格，不该判定为有容量")
	}
	if got := r.StatusOf("VM.Standard.A1.Flex"); got != "" {
		t.Errorf("StatusOf 应返回空串，实际 %q", got)
	}
}

// TestNilReportIsSafe nil 报告不能 panic。
//
// 查询失败时调用方拿到的就是 nil，而失败路径本身是要放行的，
// 这里 panic 会把一个可恢复的错误变成整个任务崩掉。
func TestNilReportIsSafe(t *testing.T) {
	var r *CapacityReport
	if r.HasCapacity("x") {
		t.Error("nil 报告不该判定为有容量")
	}
	if r.StatusOf("x") != "" {
		t.Error("nil 报告的状态应为空串")
	}
}

// TestCapacityStatusTextCoversKnownStates 三种已知状态都要有中文说法。
//
// HARDWARE_NOT_SUPPORTED 和 OUT_OF_HOST_CAPACITY 必须区分：前者不会因为
// 等待而改变，把两者显示成同一句话会让用户盯着一个永远不可能成功的监控项干等。
func TestCapacityStatusTextCoversKnownStates(t *testing.T) {
	seen := map[string]bool{}
	for _, st := range []string{CapacityAvailable, CapacityOutOfHost, CapacityHardwareNotSupported} {
		txt := CapacityStatusText(st)
		if txt == st || txt == "" {
			t.Errorf("状态 %q 没有对应的中文说法，得到 %q", st, txt)
		}
		if seen[txt] {
			t.Errorf("状态 %q 的说法与另一个状态重复：%q", st, txt)
		}
		seen[txt] = true
	}
	// 未知状态原样透出，不要吞掉——Oracle 加了新状态时得能看见。
	if got := CapacityStatusText("BRAND_NEW"); got != "BRAND_NEW" {
		t.Errorf("未知状态应原样返回，实际 %q", got)
	}
}

// 无权限时必须点明缺的是哪条策略：Oracle 只说"无权限或资源不存在"。
func TestCapacityErrorTextNamesMissingPolicy(t *testing.T) {
	err := &APIError{StatusCode: 404, Code: "NotAuthorizedOrNotFound",
		Message: "Authorization failed or requested resource not found.", Class: ClassNotAuthorized}
	if got := CapacityErrorText(err); !strings.Contains(got, "compute-capacity-reports") {
		t.Errorf("无权限的提示里应包含所需策略，实际 %q", got)
	}
}
