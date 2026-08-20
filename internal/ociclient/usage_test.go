package ociclient

import (
	"testing"
	"time"
)

// TestAlignDayTruncatesToUTCMidnight 锁住日对齐。
//
// DAILY 粒度下 timeUsageStarted 不在 UTC 零点会被 OCI 直接拒绝，
// 报错还相当含糊。这个函数错了，整页账单都查不出来。
func TestAlignDayTruncatesToUTCMidnight(t *testing.T) {
	// 特意用东八区的时刻：本地日期比 UTC 日期早一天，
	// 只有真的转成 UTC 再截断才能得到 08-19。
	shanghai := time.FixedZone("CST", 8*3600)
	in := time.Date(2026, 8, 20, 7, 30, 0, 0, shanghai)

	got := AlignDay(in)
	want := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("AlignDay = %s，期望 %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if got.Location() != time.UTC {
		t.Errorf("对齐结果必须在 UTC，实际 %s", got.Location())
	}
}

// TestAlignMonthTruncatesToFirstOfMonth MONTHLY 粒度要求对齐到月初零点。
func TestAlignMonthTruncatesToFirstOfMonth(t *testing.T) {
	in := time.Date(2026, 8, 20, 23, 59, 59, 0, time.UTC)
	got := AlignMonth(in)
	want := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("AlignMonth = %s，期望 %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestAlignIsIdempotent 已经对齐过的时刻再对齐一次不能变。
//
// 调用链上可能对齐两次（服务层算区间、客户端再兜一次），
// 第二次把日期往前推一天就会静默少查一天。
func TestAlignIsIdempotent(t *testing.T) {
	day := AlignDay(time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC))
	if !AlignDay(day).Equal(day) {
		t.Error("AlignDay 不是幂等的")
	}
	month := AlignMonth(time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC))
	if !AlignMonth(month).Equal(month) {
		t.Error("AlignMonth 不是幂等的")
	}
}

// TestUsageItemNullAmountIsZero null 金额不能 panic，取值为 0。
//
// OCI 对"这个维度没有数据"返回 null 而不是 0。指针解引用前必须判空——
// 免费账号的响应里大片都是 null。
func TestUsageItemNullAmountIsZero(t *testing.T) {
	var empty UsageItem
	if got := empty.Amount(); got != 0 {
		t.Errorf("null 金额应为 0，实际 %v", got)
	}
	if got := empty.Quantity(); got != 0 {
		t.Errorf("null 用量应为 0，实际 %v", got)
	}

	amount := 1.25
	quantity := 720.0
	item := UsageItem{ComputedAmount: &amount, ComputedQuantity: &quantity}
	if got := item.Amount(); got != amount {
		t.Errorf("Amount = %v，期望 %v", got, amount)
	}
	if got := item.Quantity(); got != quantity {
		t.Errorf("Quantity = %v，期望 %v", got, quantity)
	}
}

// TestUsageEndpointHasOCIInfix 锁住 usageapi 的域名形态。
//
// 少了 .oci 那一段的域名会被 Oracle 的泛解析命中：DNS 查得到、TCP 连不上，
// 请求一路挂到拨号超时。表现是账单页卡住而不是干脆报错，最难排查的那一类。
func TestUsageEndpointHasOCIInfix(t *testing.T) {
	got := Endpoint(ServiceUsage, "ap-tokyo-1")
	want := "https://usageapi.ap-tokyo-1.oci.oraclecloud.com"
	if got != want {
		t.Errorf("Endpoint = %q，期望 %q", got, want)
	}
}

// TestSummarizeUsageRejectsInvalidRange 起止时刻颠倒或相等时本地就该报错。
//
// 让它发出去只会换回一个含糊的 400，还白白消耗一次调用。
func TestSummarizeUsageRejectsInvalidRange(t *testing.T) {
	c, err := New(testCreds(t, testKey(t)))
	if err != nil {
		t.Fatalf("构造客户端失败: %v", err)
	}
	day := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		start, end time.Time
	}{
		{"起止相等", day, day},
		{"起晚于止", day.AddDate(0, 0, 1), day},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, callErr := c.SummarizeUsage(t.Context(), UsageQuery{
				TenantID: "ocid1.tenancy.oc1..aaaa",
				Start:    tc.start,
				End:      tc.end,
			})
			if callErr == nil {
				t.Fatal("期望报错，实际返回 nil")
			}
		})
	}
}
