package ociclient

import "testing"

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
