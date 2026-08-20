package ociclient

import (
	"strings"
	"testing"
	"time"
)

// 稳定态与过渡态的划分是整个 UI 动效语义的基础：
// 只有过渡态带脉冲动画，判错了动效就失去意义。
func TestIsTransitionalState(t *testing.T) {
	transitional := []string{
		LifecycleProvisioning, LifecycleStarting, LifecycleStopping,
		LifecycleTerminating, LifecycleCreatingImg,
	}
	for _, state := range transitional {
		if !IsTransitionalState(state) {
			t.Errorf("%s 应被判为过渡态", state)
		}
	}

	stable := []string{LifecycleRunning, LifecycleStopped, LifecycleTerminated, ""}
	for _, state := range stable {
		if IsTransitionalState(state) {
			t.Errorf("%s 不应被判为过渡态", state)
		}
	}
}

func TestValidateAction(t *testing.T) {
	for _, action := range []string{ActionStart, ActionStop, ActionSoftStop, ActionReset, ActionSoftReset} {
		if err := ValidateAction(action); err != nil {
			t.Errorf("%s 应当是合法操作: %v", action, err)
		}
	}
	for _, bad := range []string{"", "start", "DESTROY", "REBOOT"} {
		if err := ValidateAction(bad); err == nil {
			t.Errorf("%q 应当被拒绝", bad)
		}
	}
}

// a1Flex 模拟 A1.Flex 的规格元数据：每 OCPU 最多 6 GB，免费额度上限 4 OCPU / 24 GB。
func a1Flex() *Shape {
	return &Shape{
		Shape:       "VM.Standard.A1.Flex",
		IsFlexible:  true,
		OcpuOptions: &MinMax{Min: 1, Max: 4},
		MemoryOptions: &MemOpts{
			MinInGBs: 1, MaxInGBs: 24,
			MinPerOcpuInGBs: 1, MaxPerOcpuInGBs: 6,
		},
	}
}

func TestValidateShapeConfigAcceptsFreeTierMax(t *testing.T) {
	if err := ValidateShapeConfig(a1Flex(), ShapeConfig{Ocpus: 4, MemoryInGBs: 24}); err != nil {
		t.Errorf("免费额度满配 4 OCPU / 24 GB 应当通过: %v", err)
	}
	if err := ValidateShapeConfig(a1Flex(), ShapeConfig{Ocpus: 1, MemoryInGBs: 6}); err != nil {
		t.Errorf("1 OCPU / 6 GB 应当通过: %v", err)
	}
}

func TestValidateShapeConfigRejectsOverLimits(t *testing.T) {
	cases := []struct {
		name string
		cfg  ShapeConfig
		want string
	}{
		{"OCPU 超上限", ShapeConfig{Ocpus: 8, MemoryInGBs: 24}, "OCPU"},
		{"内存超上限", ShapeConfig{Ocpus: 4, MemoryInGBs: 48}, "内存"},
		// 每 OCPU 6 GB 是硬约束：2 OCPU 配 24 GB 会被 OCI 拒绝，
		// 提前拦下能省掉一次失败的创建请求。
		{"每 OCPU 内存超限", ShapeConfig{Ocpus: 2, MemoryInGBs: 24}, "每 OCPU"},
		{"缺少 OCPU", ShapeConfig{MemoryInGBs: 24}, "OCPU"},
		{"缺少内存", ShapeConfig{Ocpus: 4}, "内存"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateShapeConfig(a1Flex(), tc.cfg)
			if err == nil {
				t.Fatalf("配置 %+v 应当被拒绝", tc.cfg)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误信息应提到 %q，实际为: %v", tc.want, err)
			}
		})
	}
}

func TestValidateShapeConfigFixedShape(t *testing.T) {
	fixed := &Shape{Shape: "VM.Standard.E2.1.Micro", IsFlexible: false}

	if err := ValidateShapeConfig(fixed, ShapeConfig{}); err != nil {
		t.Errorf("固定规格不带配置应当通过: %v", err)
	}
	if err := ValidateShapeConfig(fixed, ShapeConfig{Ocpus: 2}); err == nil {
		t.Error("固定规格指定 OCPU 应当被拒绝")
	}
	// 规格元数据取不到时不该阻断创建，交给 OCI 自己判断。
	if err := ValidateShapeConfig(nil, ShapeConfig{Ocpus: 99}); err != nil {
		t.Errorf("规格未知时应放行: %v", err)
	}
}

// 云盘只能扩不能缩，OCI 的拒绝信息晦涩，必须提前拦下并说清楚。
func TestValidateVolumeResize(t *testing.T) {
	if err := ValidateVolumeResize(50, 100); err != nil {
		t.Errorf("扩容应当通过: %v", err)
	}

	err := ValidateVolumeResize(100, 50)
	if err == nil {
		t.Fatal("缩容应当被拒绝")
	}
	if !strings.Contains(err.Error(), "缩容") {
		t.Errorf("错误信息应说明不支持缩容: %v", err)
	}

	if err := ValidateVolumeResize(50, 50); err == nil {
		t.Error("容量未变化应当被拒绝")
	}
	if err := ValidateVolumeResize(50, 40); err == nil {
		t.Error("小于最小值应当被拒绝")
	}
	if err := ValidateVolumeResize(50, 99999); err == nil {
		t.Error("超过单卷上限应当被拒绝")
	}
}

func TestValidateVpus(t *testing.T) {
	for _, ok := range []int64{0, 10, 20, 30, 120} {
		if err := ValidateVpus(ok); err != nil {
			t.Errorf("VPU %d 应当合法: %v", ok, err)
		}
	}
	for _, bad := range []int64{5, 15, 130, -10} {
		if err := ValidateVpus(bad); err == nil {
			t.Errorf("VPU %d 应当被拒绝", bad)
		}
	}
}

// 流量类指标是累积计数器，必须用 rate 才有意义；用 mean 得到的是计数器均值。
func TestDefaultAggregationFor(t *testing.T) {
	rateMetrics := []string{
		MetricNetworkBytesIn, MetricNetworkBytesOut,
		MetricDiskBytesRead, MetricDiskBytesWritten,
	}
	for _, m := range rateMetrics {
		if got := DefaultAggregationFor(m); got != "rate" {
			t.Errorf("%s 的聚合方式 = %q，期望 rate", m, got)
		}
	}
	if got := DefaultAggregationFor(MetricCPUUtilization); got != "mean" {
		t.Errorf("CPU 使用率的聚合方式 = %q，期望 mean", got)
	}
}

func TestInstanceMetricQuery(t *testing.T) {
	got := InstanceMetricQuery(MetricCPUUtilization, "ocid1.instance.oc1..x", "1m", "mean")
	want := `CpuUtilization[1m]{resourceId = "ocid1.instance.oc1..x"}.mean()`
	if got != want {
		t.Errorf("MQL = %q，期望 %q", got, want)
	}
}

// 采样粒度要随时间跨度变粗，否则长跨度会拉回上万个点把图表压垮。
func TestResolutionForScalesWithSpan(t *testing.T) {
	cases := []struct {
		hours int
		want  string
	}{
		{1, "1m"}, {2, "1m"}, {6, "5m"}, {12, "5m"}, {48, "1h"}, {24 * 7, "6h"},
	}
	for _, tc := range cases {
		got := ResolutionFor(time.Duration(tc.hours) * time.Hour)
		if got != tc.want {
			t.Errorf("跨度 %d 小时的粒度 = %q，期望 %q", tc.hours, got, tc.want)
		}
	}
}
