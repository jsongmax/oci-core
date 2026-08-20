package billingsvc

import (
	"errors"
	"testing"
	"time"

	"ocicore/internal/ociclient"
)

func amount(v float64) *float64 { return &v }

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// TestFoldCostFillsGapDays 没有用量的日子必须补零占位。
//
// Oracle 只返回有用量的天。不补齐就会把 8 月 1 日和 8 月 5 日排成相邻两根
// 柱子——一条断断续续的曲线看起来是连续的，读图的人会得出完全错误的结论。
func TestFoldCostFillsGapDays(t *testing.T) {
	start, end := day(2026, 8, 1), day(2026, 8, 6)
	items := []ociclient.UsageItem{
		{TimeUsageStarted: day(2026, 8, 1), ComputedAmount: amount(1), Service: "COMPUTE", Currency: "USD"},
		{TimeUsageStarted: day(2026, 8, 5), ComputedAmount: amount(2), Service: "COMPUTE", Currency: "USD"},
	}

	series, _, total, currency := foldCost(items, start, end)

	if len(series) != 5 {
		t.Fatalf("应有 5 天，实际 %d 天", len(series))
	}
	want := []struct {
		date   string
		amount float64
	}{
		{"2026-08-01", 1},
		{"2026-08-02", 0},
		{"2026-08-03", 0},
		{"2026-08-04", 0},
		{"2026-08-05", 2},
	}
	for i, w := range want {
		if series[i].Date != w.date || series[i].Amount != w.amount {
			t.Errorf("第 %d 天 = %s/%v，期望 %s/%v",
				i, series[i].Date, series[i].Amount, w.date, w.amount)
		}
	}
	if total != 3 {
		t.Errorf("合计 = %v，期望 3", total)
	}
	if currency != "USD" {
		t.Errorf("币种 = %q，期望 USD", currency)
	}
}

// TestFoldCostSumsSameDayAcrossServices 同一天的多个服务要合并进一根柱子。
//
// 按服务分组的那次查询一举两得：日趋势和服务构成都从它算。
// 日合计漏掉求和的话，趋势图只会显示每天的第一个服务。
func TestFoldCostSumsSameDayAcrossServices(t *testing.T) {
	items := []ociclient.UsageItem{
		{TimeUsageStarted: day(2026, 8, 1), ComputedAmount: amount(1.5), Service: "COMPUTE"},
		{TimeUsageStarted: day(2026, 8, 1), ComputedAmount: amount(0.5), Service: "BLOCK_STORAGE"},
	}

	series, services, total, _ := foldCost(items, day(2026, 8, 1), day(2026, 8, 2))

	if len(series) != 1 || series[0].Amount != 2 {
		t.Errorf("当天合计应为 2，实际 %+v", series)
	}
	if total != 2 {
		t.Errorf("总计 = %v，期望 2", total)
	}
	// 金额大的排前面。
	if len(services) != 2 || services[0].Key != "计算" || services[1].Key != "块存储" {
		t.Errorf("服务构成排序不对: %+v", services)
	}
}

// TestFoldCostHandlesNullAmounts null 金额当零处理，不能 panic。
//
// 免费账号的响应里 computedAmount 大片都是 null。
func TestFoldCostHandlesNullAmounts(t *testing.T) {
	items := []ociclient.UsageItem{
		{TimeUsageStarted: day(2026, 8, 1), ComputedAmount: nil, Service: "COMPUTE"},
	}
	series, _, total, _ := foldCost(items, day(2026, 8, 1), day(2026, 8, 2))
	if total != 0 || len(series) != 1 || series[0].Amount != 0 {
		t.Errorf("null 金额应折成 0，实际 total=%v series=%+v", total, series)
	}
}

// TestFoldBucketsDropsZeroUsage 用量为零的服务不占一行。
//
// 免费号的 USAGE 响应里能有几十个零用量条目，全列出来会把真正在用的
// 那两三个服务淹掉。金额维度则相反——零金额的服务本来就不该出现在费用表里，
// 但它若出现，说明确实有过费用记录，不该删。
func TestFoldBucketsDropsZeroUsage(t *testing.T) {
	items := []ociclient.UsageItem{
		{Service: "COMPUTE", ComputedQuantity: amount(720), Unit: "OCPU Hours"},
		{Service: "VAULT", ComputedQuantity: amount(0), Unit: "Keys"},
		{Service: "BLOCK_STORAGE", ComputedQuantity: amount(50), Unit: "GB Months"},
	}

	got := foldBuckets(items, true)

	if len(got) != 2 {
		t.Fatalf("零用量的条目应被丢掉，实际留下 %d 条: %+v", len(got), got)
	}
	if got[0].Key != "计算" || got[0].Quantity != 720 {
		t.Errorf("第一条应是计算 720，实际 %+v", got[0])
	}
	if got[0].Unit != "OCPU Hours" {
		t.Errorf("单位丢了: %+v", got[0])
	}
}

// TestFoldBucketsGroupsByRegion 金额维度按区域分组，不按服务。
func TestFoldBucketsGroupsByRegion(t *testing.T) {
	items := []ociclient.UsageItem{
		{Region: "ap-tokyo-1", ComputedAmount: amount(1)},
		{Region: "ap-tokyo-1", ComputedAmount: amount(2)},
		{Region: "us-ashburn-1", ComputedAmount: amount(5)},
	}

	got := foldBuckets(items, false)

	if len(got) != 2 {
		t.Fatalf("应分成 2 个区域，实际 %d 个", len(got))
	}
	if got[0].Key != "us-ashburn-1" || got[0].Amount != 5 {
		t.Errorf("金额最大的区域应排第一，实际 %+v", got[0])
	}
	if got[1].Key != "ap-tokyo-1" || got[1].Amount != 3 {
		t.Errorf("同区域金额应累加，实际 %+v", got[1])
	}
}

// TestFoldBucketsSortIsStable 同金额时按名字排，保证刷新前后顺序一致。
//
// map 遍历顺序随机，不兜底排序的话每次刷新几行的次序都在跳。
func TestFoldBucketsSortIsStable(t *testing.T) {
	items := []ociclient.UsageItem{
		{Region: "b-region", ComputedAmount: amount(1)},
		{Region: "a-region", ComputedAmount: amount(1)},
		{Region: "c-region", ComputedAmount: amount(1)},
	}
	for i := 0; i < 20; i++ {
		got := foldBuckets(items, false)
		if got[0].Key != "a-region" || got[1].Key != "b-region" || got[2].Key != "c-region" {
			t.Fatalf("第 %d 次的顺序不稳定: %+v", i, got)
		}
	}
}

// TestClassifyNotAuthorizedIsNotAnError 缺权限是一等状态，不是错误。
//
// 这是整个包最关键的一条判断。为本工具单独建的 IAM 用户多半没授
// read usage-report，把它显示成"查询失败"会让人以为账号坏了，
// 跑去重新录凭据——而真正要做的只是补一条策略。
// 同时 Error 必须留空：那里的文本会被当成故障原文显示。
func TestClassifyNotAuthorizedIsNotAnError(t *testing.T) {
	err := &ociclient.APIError{
		StatusCode: 404,
		Code:       "NotAuthorizedOrNotFound",
		Message:    "Authorization failed or requested resource not found",
		Class:      ociclient.ClassNotAuthorized,
	}

	status, text := classify(err)

	if status != StatusNoPermission {
		t.Errorf("状态 = %q，期望 %q", status, StatusNoPermission)
	}
	if text != "" {
		t.Errorf("缺权限时不该带错误文本，实际 %q", text)
	}
}

// TestClassifyKeepsOracleErrorCode 其余错误要保留 Oracle 的原始错误码。
//
// 用户排障时要拿这个码去搜索，吞掉它等于让人对着"查询失败"干瞪眼。
func TestClassifyKeepsOracleErrorCode(t *testing.T) {
	err := &ociclient.APIError{
		StatusCode: 429,
		Code:       "TooManyRequests",
		Message:    "rate limited",
		Class:      ociclient.ClassThrottled,
	}

	status, text := classify(err)

	if status != StatusError {
		t.Errorf("状态 = %q，期望 %q", status, StatusError)
	}
	if text != "TooManyRequests · rate limited" {
		t.Errorf("错误文本 = %q，期望带上原始错误码", text)
	}
}

// TestClassifyPlainError 非 OCI 错误原样带出。
func TestClassifyPlainError(t *testing.T) {
	status, text := classify(errors.New("私钥解密失败"))
	if status != StatusError || text != "私钥解密失败" {
		t.Errorf("classify = %q/%q", status, text)
	}
}

// TestServiceNameFallsBackToRawIdentifier 表里没有的服务原样显示。
//
// Oracle 有上百个服务，翻译表必然滞后。滞后时显示 OCI 的原始标识是对的——
// 显示"未分类"会把几个不同的服务糊成一行。
func TestServiceNameFallsBackToRawIdentifier(t *testing.T) {
	if got := serviceName("COMPUTE"); got != "计算" {
		t.Errorf("已知服务应翻译，实际 %q", got)
	}
	// Oracle 实际返回的是标题式写法，不是文档里那种全大写。
	// 这条锁住的是真实响应里观察到的格式——第一版就是在这里漏了，
	// 界面上服务名全是英文原文。
	if got := serviceName("Compute"); got != "计算" {
		t.Errorf("Oracle 的实际写法应能匹配，实际 %q", got)
	}
	if got := serviceName("Block Storage"); got != "块存储" {
		t.Errorf("带空格的服务名应能匹配，实际 %q", got)
	}
	// 这两个是真实账单里实际出现过、而第一版表里漏掉的。
	// 表写的是 VCN，Oracle 返回的却是 Virtual Cloud Network。
	if got := serviceName("Virtual Cloud Network"); got != "网络" {
		t.Errorf("Virtual Cloud Network 应翻成网络，实际 %q", got)
	}
	if got := serviceName("Telemetry"); got != "监控" {
		t.Errorf("Telemetry 应翻成监控，实际 %q", got)
	}
	if got := serviceName("SOME_NEW_ORACLE_SERVICE"); got != "SOME_NEW_ORACLE_SERVICE" {
		t.Errorf("未知服务应原样显示，实际 %q", got)
	}
	if got := serviceName("  "); got != "未分类" {
		t.Errorf("空服务名应归入未分类，实际 %q", got)
	}
}
