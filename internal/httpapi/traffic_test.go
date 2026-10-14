package httpapi

import (
	"testing"
	"time"

	"ocicore/internal/ociclient"
)

func TestFoldTrafficByDayAndLast24h(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	at := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, time.UTC) }

	out := []ociclient.Datapoint{
		{Timestamp: at(1, 5), Value: 100},
		{Timestamp: at(2, 13), Value: 10}, // 最近 24 小时内
		{Timestamp: at(3, 1), Value: 1},   // 最近 24 小时内
	}
	in := []ociclient.Datapoint{
		{Timestamp: at(1, 6), Value: 50},
	}

	series, outSum, inSum, out24h, in24h := foldTraffic(out, in, start, now)

	if outSum != 111 || inSum != 50 {
		t.Errorf("合计 out=%v in=%v", outSum, inSum)
	}
	if out24h != 11 || in24h != 0 {
		t.Errorf("24 小时 out=%v in=%v", out24h, in24h)
	}
	if len(series) != 3 {
		t.Fatalf("应有 3 天，得到 %d", len(series))
	}
	if series[0].OutBytes != 100 || series[0].InBytes != 50 || series[1].OutBytes != 10 || series[2].OutBytes != 1 {
		t.Errorf("按天拆分不对: %+v", series)
	}
}

// 本月一个数据点都没有（实例一直停机）时，日序列仍要按天占位，全部为零。
func TestFoldTrafficEmptyStillFillsDays(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 2, 0, 30, 0, 0, time.UTC)
	series, outSum, _, _, _ := foldTraffic(nil, nil, start, now)
	if outSum != 0 || len(series) != 2 {
		t.Errorf("out=%v days=%d", outSum, len(series))
	}
}
