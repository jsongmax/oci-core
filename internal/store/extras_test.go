package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---- 设置 ----

func TestSettingsDefaults(t *testing.T) {
	st := newTestStore(t)

	settings, err := st.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultSettings()
	if settings != want {
		t.Errorf("默认设置 = %+v，期望 %+v", settings, want)
	}
}

func TestUpdateSettingsPartial(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	no := false
	settings, err := st.UpdateSettings(ctx, SettingsUpdate{AllowTerminate: &no})
	if err != nil {
		t.Fatal(err)
	}
	if settings.AllowTerminate {
		t.Error("AllowTerminate 应当已关闭")
	}
	// 未提交的字段必须保持默认值，不能被顺手清零。
	if !settings.AllowBulkActions {
		t.Error("未提交的 AllowBulkActions 不应被改动")
	}
	if settings.SyncIntervalMinutes != DefaultSettings().SyncIntervalMinutes {
		t.Error("未提交的同步间隔不应被改动")
	}

	// 重新读取应当持久化。
	reloaded, err := st.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.AllowTerminate {
		t.Error("设置未持久化")
	}
}

func TestUpdateSettingsRejectsBadInterval(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for _, bad := range []int{0, -5, 5000} {
		n := bad
		if _, err := st.UpdateSettings(ctx, SettingsUpdate{SyncIntervalMinutes: &n}); err == nil {
			t.Errorf("同步间隔 %d 应当被拒绝", bad)
		}
	}
}

// ---- 通知渠道 ----

func TestChannelCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	ch, err := st.CreateChannel(ctx, NewChannel{
		Kind:   "telegram",
		Name:   "我的 TG",
		Config: map[string]string{"token": "secret-token", "chatId": "123"},
		Events: []string{"instance.created"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ch.Enabled {
		t.Error("新建渠道应默认启用")
	}
	if ch.Config["token"] != "secret-token" {
		t.Errorf("配置未正确保存: %+v", ch.Config)
	}
	if len(ch.Events) != 1 || ch.Events[0] != "instance.created" {
		t.Errorf("事件订阅未正确保存: %+v", ch.Events)
	}

	name := "改名后"
	off := false
	updated, err := st.UpdateChannel(ctx, ch.ID, ChannelUpdate{Name: &name, Enabled: &off})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != name || updated.Enabled {
		t.Errorf("更新未生效: %+v", updated)
	}
	// 未提交的配置必须保留。
	if updated.Config["token"] != "secret-token" {
		t.Error("未提交的配置被清空了")
	}

	list, err := st.ListChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("渠道数量 = %d，期望 1", len(list))
	}

	if err := st.DeleteChannel(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetChannel(ctx, ch.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("删除后应返回 ErrNotFound，实际: %v", err)
	}
}

func TestCreateChannelValidatesInput(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateChannel(ctx, NewChannel{Kind: "telegram"}); err == nil {
		t.Error("缺少名称应当报错")
	}
	if _, err := st.CreateChannel(ctx, NewChannel{Name: "x"}); err == nil {
		t.Error("缺少类型应当报错")
	}
}

func TestRecordChannelSend(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	ch, err := st.CreateChannel(ctx, NewChannel{Kind: "webhook", Name: "hook"})
	if err != nil {
		t.Fatal(err)
	}
	if ch.LastSentAt != nil {
		t.Error("新建渠道不应有发送记录")
	}

	if err := st.RecordChannelSend(ctx, ch.ID, "连接超时"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetChannel(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastError != "连接超时" {
		t.Errorf("错误信息 = %q", got.LastError)
	}
	if got.LastSentAt == nil {
		t.Error("应当记录发送时间")
	}

	// 成功发送要把上次的错误清掉，否则界面会一直挂着一条过期的红字。
	if err := st.RecordChannelSend(ctx, ch.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.GetChannel(ctx, ch.ID); got.LastError != "" {
		t.Errorf("成功发送后应清除错误，实际 %q", got.LastError)
	}
}

// ---- 实例缓存 ----

func seedInstance(t *testing.T, st *Store, accountID, id, name, region, state string) {
	t.Helper()
	err := st.UpsertInstance(context.Background(), Instance{
		ID: id, AccountID: accountID, Region: region, DisplayName: name,
		LifecycleState: state, Shape: "VM.Standard.A1.Flex",
		Ocpus: 4, MemoryGB: 24, PublicIP: "1.2.3.4",
		TimeCreated: time.Now().Add(-24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInstanceUpsertAndList(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	acc, err := st.CreateAccount(ctx, testAccount(t, "东京主号", "TYO", "tokyo"))
	if err != nil {
		t.Fatal(err)
	}

	seedInstance(t, st, acc.ID, "ocid1.instance..a", "arm-01", "ap-tokyo-1", "RUNNING")
	seedInstance(t, st, acc.ID, "ocid1.instance..b", "arm-02", "ap-osaka-1", "STOPPED")

	all, err := st.ListInstances(ctx, InstanceFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("实例数量 = %d，期望 2", len(all))
	}
	// 列表要联表带出账号身份，前端才能直接渲染色条与代号。
	if all[0].AccountCode != "TYO" || all[0].AccountColorIndex == 0 {
		t.Errorf("未带出账号身份信息: %+v", all[0])
	}

	// upsert 同一 ID 应当是更新而非插入。
	seedInstance(t, st, acc.ID, "ocid1.instance..a", "arm-01-renamed", "ap-tokyo-1", "RUNNING")
	all, _ = st.ListInstances(ctx, InstanceFilter{})
	if len(all) != 2 {
		t.Fatalf("重复 upsert 后数量 = %d，期望 2", len(all))
	}
}

func TestInstanceFilters(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	acc, err := st.CreateAccount(ctx, testAccount(t, "东京主号", "TYO", "tokyo"))
	if err != nil {
		t.Fatal(err)
	}
	seedInstance(t, st, acc.ID, "i-run", "web-01", "ap-tokyo-1", "RUNNING")
	seedInstance(t, st, acc.ID, "i-stop", "db-01", "ap-osaka-1", "STOPPED")
	seedInstance(t, st, acc.ID, "i-dead", "old-01", "ap-tokyo-1", "TERMINATED")

	// 已终止实例默认隐藏，否则用一段时间后列表会被墓碑塞满。
	got, err := st.ListInstances(ctx, InstanceFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("默认应隐藏已终止实例，实际返回 %d 条", len(got))
	}

	got, _ = st.ListInstances(ctx, InstanceFilter{IncludeTerminated: true})
	if len(got) != 3 {
		t.Errorf("包含已终止时应返回 3 条，实际 %d", len(got))
	}

	got, _ = st.ListInstances(ctx, InstanceFilter{States: []string{"RUNNING"}})
	if len(got) != 1 || got[0].ID != "i-run" {
		t.Errorf("按状态过滤失败: %+v", got)
	}

	got, _ = st.ListInstances(ctx, InstanceFilter{Regions: []string{"ap-osaka-1"}})
	if len(got) != 1 || got[0].ID != "i-stop" {
		t.Errorf("按区域过滤失败: %+v", got)
	}

	got, _ = st.ListInstances(ctx, InstanceFilter{Search: "web"})
	if len(got) != 1 || got[0].ID != "i-run" {
		t.Errorf("按名称搜索失败: %+v", got)
	}
}

// 同步靠时间戳识别"实例已在 Oracle 控制台被删掉"。
func TestPruneStaleInstances(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	acc, err := st.CreateAccount(ctx, testAccount(t, "东京主号", "TYO", "tokyo"))
	if err != nil {
		t.Fatal(err)
	}
	seedInstance(t, st, acc.ID, "i-old", "old", "ap-tokyo-1", "RUNNING")

	// 用一个未来的时间点做基准，模拟"本轮同步没有见到这台机器"。
	pruned, err := st.PruneStaleInstances(ctx, acc.ID, "ap-tokyo-1", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Errorf("应清理 1 条过期缓存，实际 %d", pruned)
	}
	if got, _ := st.ListInstances(ctx, InstanceFilter{}); len(got) != 0 {
		t.Errorf("清理后仍有 %d 条", len(got))
	}
}

// 同步流程不该抹掉一条尚未被用户看到的操作失败提示。
func TestInstanceErrorSurvivesSync(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	acc, err := st.CreateAccount(ctx, testAccount(t, "东京主号", "TYO", "tokyo"))
	if err != nil {
		t.Fatal(err)
	}
	seedInstance(t, st, acc.ID, "i-1", "web", "ap-tokyo-1", "RUNNING")

	if err := st.SetInstanceError(ctx, "i-1", "IncorrectState · 状态冲突"); err != nil {
		t.Fatal(err)
	}
	// 再同步一次（upsert 覆盖），错误标记必须还在。
	seedInstance(t, st, acc.ID, "i-1", "web", "ap-tokyo-1", "RUNNING")

	got, err := st.GetInstance(ctx, "i-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastError == "" {
		t.Error("同步不应清除未被确认的错误提示")
	}

	if err := st.ClearInstanceError(ctx, "i-1"); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.GetInstance(ctx, "i-1"); got.LastError != "" {
		t.Error("显式清除后错误应消失")
	}
}

func TestInstanceStatsForOverview(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	acc, err := st.CreateAccount(ctx, testAccount(t, "东京主号", "TYO", "tokyo"))
	if err != nil {
		t.Fatal(err)
	}
	seedInstance(t, st, acc.ID, "i-1", "a", "ap-tokyo-1", "RUNNING")
	seedInstance(t, st, acc.ID, "i-2", "b", "ap-tokyo-1", "RUNNING")
	seedInstance(t, st, acc.ID, "i-3", "c", "ap-osaka-1", "STOPPED")

	byState, err := st.CountInstancesByState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if byState["RUNNING"] != 2 || byState["STOPPED"] != 1 {
		t.Errorf("状态统计不正确: %+v", byState)
	}

	dist, err := st.InstanceDistribution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dist) != 2 {
		t.Fatalf("分布矩阵应有 2 格，实际 %d", len(dist))
	}
}

// 删除账号时其实例缓存必须级联清掉，否则会留下永远同步不到的孤儿行。
func TestDeleteAccountCascadesInstances(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	acc, err := st.CreateAccount(ctx, testAccount(t, "东京主号", "TYO", "tokyo"))
	if err != nil {
		t.Fatal(err)
	}
	seedInstance(t, st, acc.ID, "i-1", "web", "ap-tokyo-1", "RUNNING")

	if err := st.DeleteAccount(ctx, acc.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ListInstances(ctx, InstanceFilter{}); len(got) != 0 {
		t.Errorf("账号删除后仍有 %d 条实例缓存", len(got))
	}
}

func TestSetAccountIdentity(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	acc, err := st.CreateAccount(ctx, testAccount(t, "东京主号", "TYO", "tokyo"))
	if err != nil {
		t.Fatal(err)
	}
	if len(acc.SubscribedRegions) != 0 {
		t.Error("新账号不应有订阅区域")
	}
	// 没有订阅列表时应退回默认区域，保证同步功能仍然可用。
	if regions := acc.EffectiveRegions(); len(regions) != 1 || regions[0] != "ap-tokyo-1" {
		t.Errorf("EffectiveRegions 应退回默认区域，实际 %v", regions)
	}

	err = st.SetAccountIdentity(ctx, acc.ID, AccountIdentity{
		Regions:     []string{"ap-tokyo-1", "ap-osaka-1"},
		HomeRegion:  "ap-tokyo-1",
		Email:       "me@example.com",
		TenancyName: "my-tenancy",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := st.GetAccount(ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SubscribedRegions) != 2 {
		t.Errorf("订阅区域 = %v", got.SubscribedRegions)
	}
	if got.Email != "me@example.com" || got.TenancyName != "my-tenancy" {
		t.Errorf("身份信息未保存: %+v", got)
	}

	// 空字段不该覆盖已有值：某次校验权限不足没取到邮箱，
	// 不能把上次成功取到的抹掉。
	if err := st.SetAccountIdentity(ctx, acc.ID, AccountIdentity{HomeRegion: "ap-osaka-1"}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetAccount(ctx, acc.ID)
	if got.Email != "me@example.com" {
		t.Error("空字段不应覆盖已有的邮箱")
	}
	if got.HomeRegion != "ap-osaka-1" {
		t.Error("非空字段应当更新")
	}
}
