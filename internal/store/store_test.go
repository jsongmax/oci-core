package store

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ocicore/internal/cryptobox"
	"ocicore/internal/ociclient"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()

	key := make([]byte, cryptobox.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := cryptobox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(filepath.Join(t.TempDir(), "test.db"), box)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// testAccount 生成一份自洽的账号输入：指纹由私钥真实计算得出。
func testAccount(t *testing.T, alias, code, tenancySuffix string) NewAccount {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return NewAccount{
		Alias:         alias,
		Code:          code,
		TenancyOCID:   "ocid1.tenancy.oc1..aaaa" + tenancySuffix,
		UserOCID:      "ocid1.user.oc1..aaaauser",
		Fingerprint:   ociclient.FingerprintOf(&key.PublicKey),
		PrivateKeyPEM: string(keyPEM),
		DefaultRegion: "ap-tokyo-1",
	}
}

func TestCreateAndGetAccount(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	in := testAccount(t, "东京主号", "TYO", "tokyo")
	acc, err := st.CreateAccount(ctx, in)
	if err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}

	if acc.Alias != "东京主号" || acc.Code != "TYO" {
		t.Errorf("账号字段不正确: %+v", acc)
	}
	if acc.Status != StatusUnchecked {
		t.Errorf("新账号状态 = %q，期望 %q", acc.Status, StatusUnchecked)
	}
	if !acc.Enabled {
		t.Error("新账号应默认启用")
	}
	if acc.ColorIndex < 1 || acc.ColorIndex > ColorCount {
		t.Errorf("自动分配的身份色序号越界: %d", acc.ColorIndex)
	}

	got, err := st.GetAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("读取账号失败: %v", err)
	}
	if got.ID != acc.ID {
		t.Error("读回的账号 ID 不一致")
	}
}

// 这是规格书里"私钥永不回显"那条约束的代码级保证：
// Account 会被直接序列化成 API 响应，其中绝不能出现任何密钥material。
func TestAccountJSONNeverContainsPrivateKey(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	in := testAccount(t, "东京主号", "TYO", "tokyo")
	acc, err := st.CreateAccount(ctx, in)
	if err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(acc)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, forbidden := range []string{
		"BEGIN RSA PRIVATE KEY",
		"privateKey",
		"keyCiphertext",
		"keyNonce",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("账号 JSON 中出现了敏感字段 %q: %s", forbidden, body)
		}
	}

	// 列表接口同样不能泄露。
	list, err := st.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(list)
	if strings.Contains(string(data), "PRIVATE KEY") {
		t.Error("账号列表 JSON 中出现了私钥内容")
	}
}

func TestCredentialsRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	in := testAccount(t, "东京主号", "TYO", "tokyo")
	acc, err := st.CreateAccount(ctx, in)
	if err != nil {
		t.Fatal(err)
	}

	creds, err := st.Credentials(ctx, acc.ID)
	if err != nil {
		t.Fatalf("解密凭据失败: %v", err)
	}
	if creds.TenancyOCID != in.TenancyOCID {
		t.Errorf("租户 OCID = %q", creds.TenancyOCID)
	}
	// 解密出的私钥必须与原指纹一致，否则签名会直接失败。
	if got := ociclient.FingerprintOf(&creds.PrivateKey.PublicKey); got != in.Fingerprint {
		t.Errorf("解密后的私钥指纹 = %q，期望 %q", got, in.Fingerprint)
	}
	if err := creds.Validate(); err != nil {
		t.Errorf("解密出的凭据未通过校验: %v", err)
	}
}

func TestCreateAccountRejectsFingerprintMismatch(t *testing.T) {
	st := newTestStore(t)
	in := testAccount(t, "东京主号", "TYO", "tokyo")
	in.Fingerprint = "00:11:22:33:44:55:66:77:88:99:aa:bb:cc:dd:ee:ff"

	_, err := st.CreateAccount(context.Background(), in)
	if err == nil {
		t.Fatal("指纹与私钥不匹配时应拒绝创建")
	}
	if !strings.Contains(err.Error(), "指纹") {
		t.Errorf("错误信息应说明是指纹问题: %v", err)
	}
}

func TestCreateAccountRejectsDuplicateTenancy(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateAccount(ctx, testAccount(t, "东京主号", "TYO", "same")); err != nil {
		t.Fatal(err)
	}
	dup := testAccount(t, "另一个", "OSA", "same")
	_, err := st.CreateAccount(ctx, dup)
	if !errors.Is(err, ErrConflict) {
		t.Errorf("重复租户应返回 ErrConflict，实际: %v", err)
	}
}

func TestCreateAccountRejectsDuplicateCode(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateAccount(ctx, testAccount(t, "东京主号", "TYO", "a")); err != nil {
		t.Fatal(err)
	}
	_, err := st.CreateAccount(ctx, testAccount(t, "另一个", "TYO", "b"))
	if !errors.Is(err, ErrConflict) {
		t.Errorf("重复代号应返回 ErrConflict，实际: %v", err)
	}
}

// 身份色要尽量均匀分布，否则前 8 个账号里就会出现重色，归属识别失效。
func TestColorIndexDistribution(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	seen := make(map[int]bool)
	for i := 0; i < ColorCount; i++ {
		code := string(rune('A'+i)) + "CT"
		acc, err := st.CreateAccount(ctx, testAccount(t, "账号", code, string(rune('a'+i))))
		if err != nil {
			t.Fatalf("第 %d 个账号创建失败: %v", i, err)
		}
		if seen[acc.ColorIndex] {
			t.Errorf("前 %d 个账号内出现了重复身份色 %d", ColorCount, acc.ColorIndex)
		}
		seen[acc.ColorIndex] = true
	}
	if len(seen) != ColorCount {
		t.Errorf("应用满 %d 种身份色，实际用了 %d 种", ColorCount, len(seen))
	}
}

func TestUpdateAccountRotateKey(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	acc, err := st.CreateAccount(ctx, testAccount(t, "东京主号", "TYO", "tokyo"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountStatus(ctx, acc.ID, StatusOK, ""); err != nil {
		t.Fatal(err)
	}

	newIn := testAccount(t, "x", "XXX", "y")
	updated, err := st.UpdateAccount(ctx, acc.ID, AccountUpdate{
		PrivateKeyPEM: &newIn.PrivateKeyPEM,
		Fingerprint:   &newIn.Fingerprint,
	})
	if err != nil {
		t.Fatalf("轮换私钥失败: %v", err)
	}
	if updated.Fingerprint != newIn.Fingerprint {
		t.Errorf("指纹未更新: %q", updated.Fingerprint)
	}
	// 密钥变了，之前的校验结论必须作废。
	if updated.Status != StatusUnchecked {
		t.Errorf("轮换密钥后状态应重置为 unchecked，实际 %q", updated.Status)
	}

	creds, err := st.Credentials(ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := ociclient.FingerprintOf(&creds.PrivateKey.PublicKey); got != newIn.Fingerprint {
		t.Error("解密出的仍是旧私钥")
	}
}

func TestUpdateAccountRejectsMismatchedRotation(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	acc, err := st.CreateAccount(ctx, testAccount(t, "东京主号", "TYO", "tokyo"))
	if err != nil {
		t.Fatal(err)
	}

	other := testAccount(t, "x", "XXX", "y")
	// 只换私钥不换指纹 —— 新私钥与旧指纹必然不匹配，应被拒绝。
	_, err = st.UpdateAccount(ctx, acc.ID, AccountUpdate{PrivateKeyPEM: &other.PrivateKeyPEM})
	if err == nil {
		t.Fatal("新私钥与现有指纹不匹配时应拒绝更新")
	}

	// 只改指纹不给私钥同样应被拒绝，否则会存下一份永远签不出名的凭据。
	_, err = st.UpdateAccount(ctx, acc.ID, AccountUpdate{Fingerprint: &other.Fingerprint})
	if err == nil {
		t.Fatal("只改指纹不提供私钥时应拒绝更新")
	}
}

func TestDeleteAccount(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	acc, err := st.CreateAccount(ctx, testAccount(t, "东京主号", "TYO", "tokyo"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteAccount(ctx, acc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetAccount(ctx, acc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("删除后查询应返回 ErrNotFound，实际: %v", err)
	}
	if err := st.DeleteAccount(ctx, acc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("重复删除应返回 ErrNotFound，实际: %v", err)
	}
}

func TestNormalizeCode(t *testing.T) {
	if got, err := NormalizeCode(" tyo "); err != nil || got != "TYO" {
		t.Errorf("NormalizeCode(\" tyo \") = %q, %v", got, err)
	}
	for _, bad := range []string{"", "T", "TOOLONG", "T-O", "东京"} {
		if _, err := NormalizeCode(bad); err == nil {
			t.Errorf("非法代号 %q 应被拒绝", bad)
		}
	}
}

func TestSuggestCode(t *testing.T) {
	cases := map[string]string{
		"ap-tokyo-1":     "TOK",
		"nrt":            "TOK",
		"eu-frankfurt-1": "FRA",
		"":               "ACC",
	}
	for in, want := range cases {
		if got := SuggestCode(in); got != want {
			t.Errorf("SuggestCode(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// ---- 用户与会话 ----

func TestUserLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if n, _ := st.CountUsers(ctx); n != 0 {
		t.Fatal("初始应当没有用户")
	}

	user, err := st.CreateUser(ctx, "admin", "$argon2id$fake")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountUsers(ctx); n != 1 {
		t.Error("创建后用户数应为 1")
	}

	if _, err := st.CreateUser(ctx, "admin", "$argon2id$fake"); !errors.Is(err, ErrConflict) {
		t.Errorf("重复用户名应返回 ErrConflict，实际: %v", err)
	}

	got, err := st.GetUserByUsername(ctx, "admin")
	if err != nil || got.ID != user.ID {
		t.Errorf("按用户名查询失败: %v", err)
	}
	if _, err := st.GetUserByUsername(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在的用户应返回 ErrNotFound，实际: %v", err)
	}
}

// TOTP 码在 30 秒窗口内可以重放，只有"这个窗口已用过"的记录能挡住它。
func TestConsumeTOTPCounterPreventsReplay(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	user, err := st.CreateUser(ctx, "admin", "$argon2id$fake")
	if err != nil {
		t.Fatal(err)
	}

	ok, err := st.ConsumeTOTPCounter(ctx, user.ID, 1000)
	if err != nil || !ok {
		t.Fatalf("首次使用应通过: ok=%v err=%v", ok, err)
	}
	ok, err = st.ConsumeTOTPCounter(ctx, user.ID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("同一时间窗被重复使用，重放防护失效")
	}
	ok, err = st.ConsumeTOTPCounter(ctx, user.ID, 999)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("更早的时间窗不应被接受")
	}
	ok, err = st.ConsumeTOTPCounter(ctx, user.ID, 1001)
	if err != nil || !ok {
		t.Errorf("下一个时间窗应被接受: ok=%v err=%v", ok, err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	user, err := st.CreateUser(ctx, "admin", "$argon2id$fake")
	if err != nil {
		t.Fatal(err)
	}

	const token = "test-session-token"
	if err := st.CreateSession(ctx, user.ID, token, "127.0.0.1", "go-test", time.Hour, false); err != nil {
		t.Fatal(err)
	}

	sess, err := st.GetSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if sess.TOTPVerified {
		t.Error("新建的半登录会话不应标记为已验证")
	}

	if err := st.MarkTOTPVerified(ctx, token); err != nil {
		t.Fatal(err)
	}
	if sess, _ = st.GetSession(ctx, token); !sess.TOTPVerified {
		t.Error("提升后会话应标记为已验证")
	}

	if err := st.DeleteSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSession(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Errorf("删除后应返回 ErrNotFound，实际: %v", err)
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	user, err := st.CreateUser(ctx, "admin", "$argon2id$fake")
	if err != nil {
		t.Fatal(err)
	}
	// 负 TTL 直接造出一个已过期的会话。
	if err := st.CreateSession(ctx, user.ID, "expired", "", "", -time.Minute, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSession(ctx, "expired"); !errors.Is(err, ErrNotFound) {
		t.Errorf("过期会话应按不存在处理，实际: %v", err)
	}
}

// 改密后所有旧会话必须立即失效。
func TestSetPasswordInvalidatesSessions(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	user, err := st.CreateUser(ctx, "admin", "$argon2id$old")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, user.ID, "tok", "", "", time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPassword(ctx, user.ID, "$argon2id$new"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSession(ctx, "tok"); !errors.Is(err, ErrNotFound) {
		t.Errorf("改密后旧会话应失效，实际: %v", err)
	}
}

func TestAuditLog(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := st.Audit(ctx, AuditEntry{
			UserID: "u1", Action: "account_create", AccountID: "a1", Target: "东京主号", IP: "127.0.0.1",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Audit(ctx, AuditEntry{
		UserID: "u1", Action: "login", Result: ResultFail,
	}); err != nil {
		t.Fatal(err)
	}

	all, hasMore, err := st.ListAudit(ctx, AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("应有 4 条记录，实际 %d", len(all))
	}
	if hasMore {
		t.Error("只有 4 条却报还有下一页")
	}
	// 倒序排列，最新的在前。
	if all[0].Action != "login" {
		t.Errorf("最新一条应为 login，实际 %q", all[0].Action)
	}

	filtered, _, err := st.ListAudit(ctx, AuditFilter{AccountID: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 3 {
		t.Errorf("按账号过滤应得 3 条，实际 %d", len(filtered))
	}
}

// TestAuditPaginationCoversEveryRow 锁住游标翻页不漏不重。
//
// 用 OFFSET 分页在持续写入的表上会漏记录：翻到第二页时新记录已经插到
// 最前面，原本第 limit 条被挤到了下一页的位置，于是被跳过。审计日志正是
// 这种表。这个测试模拟「翻页途中又写入了新记录」，要求已翻过的部分不受影响。
func TestAuditPaginationCoversEveryRow(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	const total = 25
	for i := 0; i < total; i++ {
		if err := st.Audit(ctx, AuditEntry{Action: fmt.Sprintf("act-%02d", i)}); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]int{}
	var before int64
	pages := 0
	for {
		page, hasMore, err := st.ListAudit(ctx, AuditFilter{Limit: 10, BeforeID: before})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page {
			seen[e.Action]++
		}
		pages++
		if !hasMore {
			break
		}
		before = page[len(page)-1].ID

		// 翻页途中插入新记录。游标是「id 小于某值」，新记录 id 更大，
		// 不会挤进后续页面——这正是不用 OFFSET 的理由。
		if err := st.Audit(ctx, AuditEntry{Action: "noise"}); err != nil {
			t.Fatal(err)
		}
		if pages > 10 {
			t.Fatal("翻页没有终止")
		}
	}

	for i := 0; i < total; i++ {
		k := fmt.Sprintf("act-%02d", i)
		if seen[k] != 1 {
			t.Errorf("%s 出现 %d 次，期望恰好 1 次", k, seen[k])
		}
	}
}

// TestPruneAuditKeepsRecent 确认清理只删过期的那部分。
func TestPruneAuditKeepsRecent(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.Audit(ctx, AuditEntry{Action: "recent"}); err != nil {
		t.Fatal(err)
	}
	// 直接改写 created_at 造一条“很旧”的记录：Audit() 用的是当前时间。
	if err := st.Audit(ctx, AuditEntry{Action: "ancient"}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-90 * 24 * time.Hour).Unix()
	if _, err := st.db.ExecContext(ctx,
		`UPDATE audit_logs SET created_at = ? WHERE action = 'ancient'`, old); err != nil {
		t.Fatal(err)
	}

	n, err := st.PruneAudit(ctx, time.Now().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应删掉 1 条，实际 %d", n)
	}

	left, _, err := st.ListAudit(ctx, AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].Action != "recent" {
		t.Fatalf("应只剩 recent，实际 %+v", left)
	}
}

// TestRunningSinceOnlyTracksObservedTransitions 锁住 running_since 的语义。
//
// 它记录的是「面板观测到实例进入 RUNNING 的时刻」，不是「实例开机的时刻」。
// 两者在首次同步时就已经在跑的实例上会分岔——那种情况必须留空，
// 让前端退回显示"创建至今"并标明是近似值。猜一个开机时间会让
// 运行时长这一列看起来精确、实际上是编的。
func TestRunningSinceOnlyTracksObservedTransitions(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	acc, err := st.CreateAccount(ctx, testAccount(t, "运行时长", "RUN", "run1"))
	if err != nil {
		t.Fatal(err)
	}

	inst := Instance{
		ID: "ocid1.instance.oc1..running", AccountID: acc.ID, Region: "ap-osaka-1",
		DisplayName: "vm", LifecycleState: "RUNNING", TimeCreated: time.Now().Add(-72 * time.Hour),
	}

	// 首次见到就已经在跑：不知道何时开的机。
	if err := st.UpsertInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunningSince != nil {
		t.Errorf("首次见到就是 RUNNING，不该编造开机时间，得到 %v", got.RunningSince)
	}

	// 关机：仍然不知道。
	if err := st.SetInstanceState(ctx, inst.ID, "STOPPED"); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.GetInstance(ctx, inst.ID); got.RunningSince != nil {
		t.Errorf("STOPPED 时不该有开机时间，得到 %v", got.RunningSince)
	}

	// 观测到重新开机：这一次我们知道。
	before := time.Now().Add(-time.Second)
	if err := st.SetInstanceState(ctx, inst.ID, "RUNNING"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetInstance(ctx, inst.ID)
	if got.RunningSince == nil {
		t.Fatal("观测到进入 RUNNING，应当记下时刻")
	}
	if got.RunningSince.Before(before) {
		t.Errorf("开机时刻 %v 早于本次操作，说明取的是旧值", got.RunningSince)
	}

	// 后续同步仍是 RUNNING：不能把时刻刷新成"现在"，否则运行时长永远是 0。
	first := *got.RunningSince
	inst.LifecycleState = "RUNNING"
	if err := st.UpsertInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetInstance(ctx, inst.ID)
	if got.RunningSince == nil || !got.RunningSince.Equal(first) {
		t.Errorf("状态没变时开机时刻不该被刷新: %v → %v", first, got.RunningSince)
	}
}

// TestSyncDoesNotClobberNote 锁住「备注不被同步覆盖」这条约束。
//
// 备注是本工具里唯一不来自 Oracle 的实例字段。UpsertInstance 每 5 分钟被
// 同步调用一次，一旦有人把 note 加进那条 SQL 的更新列表，用户写的东西就会
// 每 5 分钟被抹一次——而且抹得很安静：写的时候好好的，过几分钟自己没了。
func TestSyncDoesNotClobberNote(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	acc, err := st.CreateAccount(ctx, testAccount(t, "备注", "NOT", "note1"))
	if err != nil {
		t.Fatal(err)
	}

	inst := Instance{
		ID: "ocid1.instance.oc1..note", AccountID: acc.ID, Region: "ap-osaka-1",
		DisplayName: "vm", LifecycleState: "RUNNING", TimeCreated: time.Now(),
	}
	if err := st.UpsertInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if err := st.SetInstanceNote(ctx, inst.ID, "生产环境 · 勿动"); err != nil {
		t.Fatal(err)
	}

	// 模拟一轮同步：Oracle 的数据再写一遍，备注必须原样还在。
	inst.DisplayName = "vm-renamed-in-oracle"
	if err := st.UpsertInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetInstance(ctx, inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Note != "生产环境 · 勿动" {
		t.Errorf("同步之后备注变成了 %q —— 它被 UpsertInstance 覆盖了", got.Note)
	}
	if got.DisplayName != "vm-renamed-in-oracle" {
		t.Errorf("来自 Oracle 的字段应当被同步更新，得到 %q", got.DisplayName)
	}

	// 空串表示清除。
	if err := st.SetInstanceNote(ctx, inst.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.GetInstance(ctx, inst.ID); got.Note != "" {
		t.Errorf("清除后应为空串，得到 %q", got.Note)
	}

	// 不存在的实例要报 ErrNotFound，而不是静默成功。
	if err := st.SetInstanceNote(ctx, "ocid1.instance.oc1..missing", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("给不存在的实例写备注应返回 ErrNotFound，得到 %v", err)
	}
}
