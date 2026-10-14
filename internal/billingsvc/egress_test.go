package billingsvc

import (
	"math"
	"testing"

	"ocicore/internal/ociclient"
)

// 只挑出站流量那几个计费项，各 Zone 相加，其余计费项不能混进来。
func TestFoldEgressPicksOutboundSKUsOnly(t *testing.T) {
	start, end := day(2026, 10, 1), day(2026, 10, 4)
	items := []ociclient.UsageItem{
		{TimeUsageStarted: day(2026, 10, 1), SkuName: "Outbound Data Transfer Zone 1",
			ComputedQuantity: amount(100), Unit: "GB"},
		{TimeUsageStarted: day(2026, 10, 1), SkuName: "Outbound Data Transfer Zone 2",
			ComputedQuantity: amount(20), Unit: "GB"},
		{TimeUsageStarted: day(2026, 10, 3), SkuName: "Outbound Data Transfer Zone 1",
			ComputedQuantity: amount(5), Unit: "GB"},
		{TimeUsageStarted: day(2026, 10, 2), SkuName: "Compute - Standard - A1 - OCPU",
			ComputedQuantity: amount(999), Unit: "OCPU Hours"},
	}

	gb, _, unit, _, series := foldEgress(items, start, end)

	if gb != 125 {
		t.Errorf("合计 = %v, 期望 125", gb)
	}
	if unit != "GB" {
		t.Errorf("单位 = %q", unit)
	}
	if len(series) != 3 {
		t.Fatalf("应补齐 3 天，得到 %d 天", len(series))
	}
	want := []float64{120, 0, 5}
	for i, w := range want {
		if series[i].GB != w {
			t.Errorf("第 %d 天 = %v, 期望 %v", i+1, series[i].GB, w)
		}
	}
}

func TestToGB(t *testing.T) {
	cases := []struct {
		qty  float64
		unit string
		want float64
	}{
		{5, "GB", 5},
		{2, "TB", 2048},
		{512, "MB", 0.5},
		{1 << 30, "BYTES", 1},
		{3, "Gigabytes", 3},
		{7, "", 7},
	}
	for _, c := range cases {
		if got := toGB(c.qty, c.unit); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("toGB(%v, %q) = %v, 期望 %v", c.qty, c.unit, got, c.want)
		}
	}
}
