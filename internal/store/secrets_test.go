package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ocicore/internal/cryptobox"
)

// reopenableStore 返回一个可以用同一把主密钥重新打开的库，用来模拟升级后的首次启动。
func reopenableStore(t *testing.T) (*Store, func() *Store) {
	t.Helper()
	key := make([]byte, cryptobox.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := cryptobox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "test.db")
	open := func() *Store {
		st, err := Open(path, box)
		if err != nil {
			t.Fatalf("打开数据库失败: %v", err)
		}
		t.Cleanup(func() { st.Close() })
		return st
	}
	return open(), open
}

const plainTOTP = "JBSWY3DPEHPK3PXP"

// F07：TOTP 密钥以密文落库，读出来是明文。
func TestTOTPSecretEncryptedAtRest(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetTOTPSecret(ctx, u.ID, plainTOTP); err != nil {
		t.Fatal(err)
	}

	var raw string
	if err := st.DB().QueryRow(`SELECT totp_secret FROM users WHERE id = ?`, u.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, plainTOTP) || !isSealed(raw) {
		t.Errorf("库里的 TOTP 密钥应为密文，实际 %q", raw)
	}
	got, err := st.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TOTPSecret != plainTOTP {
		t.Errorf("读出的密钥 = %q，期望 %q", got.TOTPSecret, plainTOTP)
	}

	// 密文绑定用户 ID：搬到别的用户行上必须解不开。
	other, err := st.CreateUser(ctx, "other", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE users SET totp_secret = ? WHERE id = ?`, raw, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetUser(ctx, other.ID); err == nil {
		t.Error("搬到其他用户的 TOTP 密文不应能解密")
	}
}

// F07：通知渠道配置（含 token、密码）以密文落库。
func TestChannelConfigEncryptedAtRest(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	const token = "123456:telegram-bot-token"
	ch, err := st.CreateChannel(ctx, NewChannel{
		Kind: "telegram", Name: "tg", Config: map[string]string{"token": token, "chatId": "42"},
	})
	if err != nil {
		t.Fatal(err)
	}
	const newPass = "smtp-password-xyz"
	if _, err := st.UpdateChannel(ctx, ch.ID, ChannelUpdate{
		Config: map[string]string{"token": token, "chatId": "42", "password": newPass},
	}); err != nil {
		t.Fatal(err)
	}

	var raw string
	if err := st.DB().QueryRow(`SELECT config FROM notification_channels WHERE id = ?`, ch.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, token) || strings.Contains(raw, newPass) || !isSealed(raw) {
		t.Errorf("库里的渠道配置应为密文，实际 %q", raw)
	}
	got, err := st.GetChannel(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config["token"] != token || got.Config["password"] != newPass {
		t.Errorf("读出的配置不对: %+v", got.Config)
	}
}

// F07：升级前写入的明文 TOTP 密钥与渠道配置，在启动时被改写成密文且仍可读。
func TestPlaintextSecretsMigratedOnOpen(t *testing.T) {
	st, reopen := reopenableStore(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE users SET totp_secret = ?, totp_enabled = 1 WHERE id = ?`, plainTOTP, u.ID); err != nil {
		t.Fatal(err)
	}
	const legacyConfig = `{"token":"legacy-token","chatId":"1"}`
	if _, err := st.DB().Exec(`
		INSERT INTO notification_channels (id, kind, name, config, events, enabled, created_at, updated_at)
		VALUES ('ch1', 'telegram', 'old', ?, '[]', 1, 0, 0)`, legacyConfig); err != nil {
		t.Fatal(err)
	}
	// 迁移前也能按明文读出。
	if got, err := st.GetUser(ctx, u.ID); err != nil || got.TOTPSecret != plainTOTP {
		t.Fatalf("迁移前读取明文失败: %v %+v", err, got)
	}
	st.Close()

	st = reopen()
	var rawSecret, rawConfig string
	if err := st.DB().QueryRow(`SELECT totp_secret FROM users WHERE id = ?`, u.ID).Scan(&rawSecret); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRow(`SELECT config FROM notification_channels WHERE id = 'ch1'`).Scan(&rawConfig); err != nil {
		t.Fatal(err)
	}
	if !isSealed(rawSecret) || strings.Contains(rawSecret, plainTOTP) {
		t.Errorf("TOTP 密钥未被迁移成密文: %q", rawSecret)
	}
	if !isSealed(rawConfig) || strings.Contains(rawConfig, "legacy-token") {
		t.Errorf("渠道配置未被迁移成密文: %q", rawConfig)
	}

	got, err := st.GetUser(ctx, u.ID)
	if err != nil || got.TOTPSecret != plainTOTP || !got.TOTPEnabled {
		t.Errorf("迁移后读取 TOTP 失败: %v %+v", err, got)
	}
	ch, err := st.GetChannel(ctx, "ch1")
	if err != nil || ch.Config["token"] != "legacy-token" {
		t.Errorf("迁移后读取渠道配置失败: %v %+v", err, ch)
	}
}

// F04/F05：启用、关闭 TOTP 在同一事务里撤销全部会话；关闭时已用过的时间窗不能再用。
func TestTOTPStateChangesRevokeSessions(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	countSessions := func() int {
		var n int
		_ = st.DB().QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, u.ID).Scan(&n)
		return n
	}
	mustSession := func(tok string, verified bool) {
		if err := st.CreateSession(ctx, u.ID, tok, "", "", time.Hour, verified); err != nil {
			t.Fatal(err)
		}
	}

	mustSession("a", true)
	if err := st.SetTOTPSecret(ctx, u.ID, plainTOTP); err != nil {
		t.Fatal(err)
	}
	if err := st.EnableTOTP(ctx, u.ID, 100); err != nil {
		t.Fatal(err)
	}
	if n := countSessions(); n != 0 {
		t.Errorf("启用后应撤销全部会话，剩余 %d", n)
	}
	if err := st.SetTOTPSecret(ctx, u.ID, "OTHERSECRET234567"); err == nil {
		t.Error("已启用时不应能直接覆盖密钥")
	}

	mustSession("b", false)
	fresh, err := st.DisableTOTP(ctx, u.ID, 100)
	if err != nil || fresh {
		t.Fatalf("已用过的时间窗应被拒绝: fresh=%v err=%v", fresh, err)
	}
	if got, _ := st.GetUser(ctx, u.ID); !got.TOTPEnabled {
		t.Error("被拒绝的关闭不应改变状态")
	}
	if n := countSessions(); n != 1 {
		t.Errorf("被拒绝的关闭不应撤销会话，剩余 %d", n)
	}

	fresh, err = st.DisableTOTP(ctx, u.ID, 101)
	if err != nil || !fresh {
		t.Fatalf("新的时间窗应允许关闭: fresh=%v err=%v", fresh, err)
	}
	if got, _ := st.GetUser(ctx, u.ID); got.TOTPEnabled || got.TOTPSecret != "" {
		t.Errorf("关闭后应清除密钥: %+v", got)
	}
	if n := countSessions(); n != 0 {
		t.Errorf("关闭后应撤销全部会话（含半登录会话），剩余 %d", n)
	}
}

// F06：旧版 accounts.proxy_url 在启动迁移后被清空；已绑定代理池的账号也一并清掉遗留字段。
func TestLegacyProxyURLClearedAfterMigration(t *testing.T) {
	st, reopen := reopenableStore(t)
	ctx := context.Background()

	a1, err := st.CreateAccount(ctx, testAccount(t, "东京", "TYO", "tyo"))
	if err != nil {
		t.Fatal(err)
	}
	a2, err := st.CreateAccount(ctx, testAccount(t, "大阪", "OSA", "osa"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProxy(ctx, testProxy("198.51.100.2"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindProxy(ctx, a2.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	// 模拟旧版本写下的明文字段。
	if _, err := st.DB().Exec(`UPDATE accounts SET proxy_url = ? WHERE id = ?`,
		"http://bob:legacy-pw@198.51.100.1:8080", a1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE accounts SET proxy_url = ? WHERE id = ?`,
		"http://carol:stale-pw@198.51.100.3:8080", a2.ID); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st = reopen()
	rows, err := st.DB().Query(`SELECT proxy_url FROM accounts`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		if v != "" {
			t.Errorf("迁移后 proxy_url 应被清空，实际 %q", v)
		}
	}
	rows.Close()

	pid, err := st.AccountProxyID(ctx, a1.ID)
	if err != nil || pid == "" {
		t.Fatalf("旧地址应迁成代理池实体并绑定: %q %v", pid, err)
	}
	if u, _ := st.ProxyURL(ctx, pid); !strings.Contains(u, "legacy-pw") {
		t.Errorf("迁移后的代理应保留密码，实际 %q", u)
	}
	if pid2, _ := st.AccountProxyID(ctx, a2.ID); pid2 != p.ID {
		t.Errorf("已有绑定不应被旧字段覆盖: %q", pid2)
	}
}

// F06：更新账号时提交的 proxyUrl 同样转成代理池实体，不落原文。
func TestUpdateAccountProxyURLAdoptsIntoPool(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	acc, err := st.CreateAccount(ctx, testAccount(t, "东京", "TYO", "tyo"))
	if err != nil {
		t.Fatal(err)
	}
	raw := "socks5://dave:upd-pw@198.51.100.9:1080"
	if _, err := st.UpdateAccount(ctx, acc.ID, AccountUpdate{ProxyURL: &raw}); err != nil {
		t.Fatal(err)
	}
	if legacy, _ := st.LegacyProxyURL(ctx, acc.ID); legacy != "" {
		t.Errorf("账号行不应保存原始地址: %q", legacy)
	}
	pid, _ := st.AccountProxyID(ctx, acc.ID)
	if u, _ := st.ProxyURL(ctx, pid); u != raw {
		t.Errorf("代理池地址 = %q，期望 %q", u, raw)
	}

	// 同一出口再提交一次（密码变了）：复用原实体并更新密码，而不是报重复。
	raw2 := "socks5://dave:new-pw@198.51.100.9:1080"
	if _, err := st.UpdateAccount(ctx, acc.ID, AccountUpdate{ProxyURL: &raw2}); err != nil {
		t.Fatalf("同一出口重复提交应复用: %v", err)
	}
	if pid2, _ := st.AccountProxyID(ctx, acc.ID); pid2 != pid {
		t.Errorf("应复用同一条代理: %q != %q", pid2, pid)
	}
	if u, _ := st.ProxyURL(ctx, pid); u != raw2 {
		t.Errorf("密码应更新为本次提交的值，实际 %q", u)
	}

	bad := "not a proxy at all"
	if _, err := st.UpdateAccount(ctx, acc.ID, AccountUpdate{ProxyURL: &bad}); err == nil || strings.Contains(err.Error(), bad) {
		t.Errorf("无效地址应报错且不回显原文: %v", err)
	}
}

// R1：登录按口令校验前读到的用户快照签发会话。校验期间若启用了 TOTP 或改了口令，
// 签发必须失败，而不是插入一个绕过新状态的完整会话。
func TestLoginSessionRejectedWhenAuthStateChanged(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "admin", "hash-v1")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := st.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}

	// 快照未变：正常签发完整会话。
	if err := st.CreateLoginSession(ctx, stale, "ok", "", "", time.Hour); err != nil {
		t.Fatalf("状态未变时应能签发: %v", err)
	}
	if sess, err := st.GetSession(ctx, "ok"); err != nil || !sess.TOTPVerified {
		t.Fatalf("未启用 TOTP 的登录应得到完整会话: %+v %v", sess, err)
	}

	// 登录进行中，用户启用了 TOTP。
	if err := st.SetTOTPSecret(ctx, u.ID, plainTOTP); err != nil {
		t.Fatal(err)
	}
	if err := st.EnableTOTP(ctx, u.ID, 100); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateLoginSession(ctx, stale, "late", "", "", time.Hour); !errors.Is(err, ErrAuthStateChanged) {
		t.Errorf("TOTP 已启用时按旧快照签发应失败，实际 %v", err)
	}
	// 即使绕过登录专用入口，也不能给已启用 TOTP 的用户直接签完整会话。
	if err := st.CreateSession(ctx, u.ID, "late2", "", "", time.Hour, true); !errors.Is(err, ErrAuthStateChanged) {
		t.Errorf("已启用 TOTP 的用户不应拿到 totp_verified=1 的普通会话，实际 %v", err)
	}
	if _, err := st.GetSession(ctx, "late"); !errors.Is(err, ErrNotFound) {
		t.Error("失败的签发不应留下会话")
	}

	// 启用后的新快照：签出的是半登录会话。
	fresh, _ := st.GetUser(ctx, u.ID)
	if err := st.CreateLoginSession(ctx, fresh, "pending", "", "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if sess, _ := st.GetSession(ctx, "pending"); sess.TOTPVerified {
		t.Error("已启用 TOTP 的登录只应得到半登录会话")
	}

	// 登录进行中改了口令，同样拒绝。
	if err := st.SetPassword(ctx, u.ID, "hash-v2"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateLoginSession(ctx, fresh, "old-pw", "", "", time.Hour); !errors.Is(err, ErrAuthStateChanged) {
		t.Errorf("口令已改时按旧快照签发应失败，实际 %v", err)
	}
}

// R2：迁不进代理池的旧地址（出口冲突、无法解析）不留明文，加密后仍能解密兜底；
// 迁移收尾做 checkpoint，库仍打开时主库与 WAL 文件里都找不到旧密码。
func TestUnmigratableLegacyProxySealed(t *testing.T) {
	st, reopen := reopenableStore(t)
	ctx := context.Background()
	a, err := st.CreateAccount(ctx, testAccount(t, "一号", "ONE", "one"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateAccount(ctx, testAccount(t, "二号", "TWO", "two"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateAccount(ctx, testAccount(t, "三号", "THR", "thr"))
	if err != nil {
		t.Fatal(err)
	}
	const shared = "http://u:shared-legacy-pw@198.51.100.77:3128"
	const garbage = "garbage-legacy-pw only"
	for id, v := range map[string]string{a.ID: shared, b.ID: shared, c.ID: garbage} {
		if _, err := st.DB().Exec(`UPDATE accounts SET proxy_url = ? WHERE id = ?`, v, id); err != nil {
			t.Fatal(err)
		}
	}
	var path string
	if err := st.DB().QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st = reopen()
	var plain int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM accounts WHERE proxy_url != '' AND proxy_url NOT LIKE 'enc:v1:%'`).Scan(&plain); err != nil {
		t.Fatal(err)
	}
	if plain != 0 {
		t.Errorf("迁移后仍有 %d 行旧代理地址以明文保存", plain)
	}

	// 每个账号仍有代理可用：要么已绑定代理池，要么能解密出原地址，不会静默直连。
	bound := 0
	for id, want := range map[string]string{a.ID: shared, b.ID: shared, c.ID: garbage} {
		if pid, _ := st.AccountProxyID(ctx, id); pid != "" {
			bound++
			continue
		}
		got, err := st.LegacyProxyURL(ctx, id)
		if err != nil || got != want {
			t.Errorf("账号 %s 的旧代理地址应能解密还原，实际 %q %v", id, got, err)
		}
	}
	if bound != 1 {
		t.Errorf("共用同一出口的两个账号应恰好一个迁入代理池，实际 %d", bound)
	}

	for _, f := range []string{path, path + "-wal"} {
		data, err := os.ReadFile(f)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, secret := range []string{"shared-legacy-pw", "garbage-legacy-pw"} {
			if bytes.Contains(data, []byte(secret)) {
				t.Errorf("%s 里仍能找到旧明文 %q", filepath.Base(f), secret)
			}
		}
	}
}
