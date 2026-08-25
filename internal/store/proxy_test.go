package store

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"ocicore/internal/proxypool"
)

func testProxy(host string) proxypool.Parsed {
	return proxypool.Parsed{
		Scheme: "http", Host: host, Port: 8080,
		Username: "alice", Password: "s3cret", Label: host,
	}
}

// TestProxyPasswordNeverStoredPlaintext 密码不能以明文落盘。
//
// 这条和账号私钥是同一条规矩。代理密码是付费凭据，库文件泄露时
// 不该能直接读出来。
func TestProxyPasswordNeverStoredPlaintext(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateProxy(ctx, testProxy("1.2.3.4")); err != nil {
		t.Fatalf("创建代理失败: %v", err)
	}

	var cipher []byte
	err := st.DB().QueryRow(`SELECT pass_ciphertext FROM proxies LIMIT 1`).Scan(&cipher)
	if err != nil {
		t.Fatal(err)
	}
	if len(cipher) == 0 {
		t.Fatal("密文为空，密码根本没存进去")
	}
	if bytes.Contains(cipher, []byte("s3cret")) {
		t.Error("密文里能直接搜到明文密码")
	}
}

// TestProxyStructNeverCarriesPassword 返回给上层的结构体不含密码。
//
// store.Proxy 会被直接序列化成 API 响应——账号那边就是这么干的
// （writeJSON 直接把 store.Account 吐出去）。这里但凡多一个字段，
// 整张代理表的密码就会随列表接口流到前端。
func TestProxyStructNeverCarriesPassword(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	p, err := st.CreateProxy(ctx, testProxy("1.2.3.4"))
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasPassword {
		t.Error("HasPassword 应为 true")
	}
	if got := p.Display(); contains(got, "s3cret") {
		t.Errorf("Display() 泄露了密码: %s", got)
	}

	list, err := st.ListProxies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Display() == "" {
		t.Fatalf("列表结果异常: %+v", list)
	}
}

// TestProxyURLRoundTrip 解密后能拼回可用的地址。
func TestProxyURLRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	p, err := st.CreateProxy(ctx, testProxy("1.2.3.4"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.ProxyURL(ctx, p.ID)
	if err != nil {
		t.Fatalf("取代理地址失败: %v", err)
	}
	back, err := proxypool.ParseLine(got)
	if err != nil {
		t.Fatalf("生成的地址解析不回来: %s (%v)", got, err)
	}
	if back.Host != "1.2.3.4" || back.Port != 8080 || back.Username != "alice" {
		t.Errorf("往返后信息不一致: %+v", back)
	}
}

// TestCreateProxyRejectsDuplicate 同一个出口不该重复录入。
func TestCreateProxyRejectsDuplicate(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateProxy(ctx, testProxy("1.2.3.4")); err != nil {
		t.Fatal(err)
	}
	_, err := st.CreateProxy(ctx, testProxy("1.2.3.4"))
	if !errors.Is(err, ErrProxyExists) {
		t.Errorf("重复录入应返回 ErrProxyExists，实际 %v", err)
	}
}

// TestBindProxyRejectsSharing 一条代理不能绑给两个账号。
//
// 这是整个功能最核心的一条约束。共用出口比不用代理更糟——它把两个
// 本来从不同网络访问的账号绑在同一个 IP 上，凭空制造一个关联信号，
// 与"网络隔离"的目的正好相反。所以必须是硬拒绝，不是警告。
func TestBindProxyRejectsSharing(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	a1, err := st.CreateAccount(ctx, testAccount(t, "大阪", "OSA", "osa"))
	if err != nil {
		t.Fatal(err)
	}
	a2, err := st.CreateAccount(ctx, testAccount(t, "首尔", "SEO", "seo"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProxy(ctx, testProxy("1.2.3.4"))
	if err != nil {
		t.Fatal(err)
	}

	if err := st.BindProxy(ctx, a1.ID, p.ID); err != nil {
		t.Fatalf("首次绑定应成功: %v", err)
	}
	err = st.BindProxy(ctx, a2.ID, p.ID)
	if !errors.Is(err, proxypool.ErrDuplicateBinding) {
		t.Errorf("重复绑定应被拒绝，实际 %v", err)
	}

	// 重新绑给同一个账号不算冲突——那只是幂等重放。
	if err := st.BindProxy(ctx, a1.ID, p.ID); err != nil {
		t.Errorf("绑给原账号应当幂等成功，实际 %v", err)
	}
}

// TestUnbindReturnsToDirect 解绑后回到本机直连。
func TestUnbindReturnsToDirect(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	acc, err := st.CreateAccount(ctx, testAccount(t, "大阪", "OSA", "osa"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProxy(ctx, testProxy("1.2.3.4"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindProxy(ctx, acc.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.BindProxy(ctx, acc.ID, ""); err != nil {
		t.Fatalf("解绑失败: %v", err)
	}

	id, err := st.AccountProxyID(ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Errorf("解绑后应为空，实际 %q", id)
	}
	if b, _ := st.ProxyBindings(ctx); len(b) != 0 {
		t.Errorf("解绑后不该还有绑定: %+v", b)
	}
}

// TestDeleteProxyBlockedWhileBound 仍被绑定时不能删。
//
// 静默删除会让那个账号在用户不知情的情况下回落本机直连——
// 而用代理的全部目的就是不要那样。
func TestDeleteProxyBlockedWhileBound(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	acc, err := st.CreateAccount(ctx, testAccount(t, "大阪", "OSA", "osa"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProxy(ctx, testProxy("1.2.3.4"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindProxy(ctx, acc.ID, p.ID); err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteProxy(ctx, p.ID); err == nil {
		t.Error("仍被绑定时删除应被拒绝")
	}
	if err := st.BindProxy(ctx, acc.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteProxy(ctx, p.ID); err != nil {
		t.Errorf("解绑后删除应成功: %v", err)
	}
}

// TestUpdateProxyInvalidatesBoundAccounts 改代理要让账号的连接缓存失效。
//
// ociconn 按账号的 updated_at 判断缓存是否过期。改了代理密码却不动账号行，
// 那些账号会继续用旧密码建好的连接——表现是"密码明明改对了还是 407"，
// 而且要等到账号本身被改动才会自愈。
func TestUpdateProxyInvalidatesBoundAccounts(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	acc, err := st.CreateAccount(ctx, testAccount(t, "大阪", "OSA", "osa"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProxy(ctx, testProxy("1.2.3.4"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindProxy(ctx, acc.ID, p.ID); err != nil {
		t.Fatal(err)
	}

	before, err := st.GetAccount(ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 直接把账号的 updated_at 推回过去，好观察它有没有被推进。
	if _, err := st.DB().Exec(
		`UPDATE accounts SET updated_at = ? WHERE id = ?`, 1, acc.ID); err != nil {
		t.Fatal(err)
	}

	newPass := "rotated"
	if _, err := st.UpdateProxy(ctx, p.ID, ProxyUpdate{Password: &newPass}); err != nil {
		t.Fatalf("更新代理失败: %v", err)
	}

	after, err := st.GetAccount(ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpdatedAt.After(unixToTime(1)) {
		t.Error("改代理后账号的 updated_at 没被推进，连接缓存不会失效")
	}
	_ = before

	// 密码确实换了。
	url, err := st.ProxyURL(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(url, "rotated") {
		t.Errorf("密码没更新: %s", url)
	}
}

// TestRecordProxyCheckKeepsLastOK 失败不该抹掉"最近一次成功"。
//
// 界面要能区分"刚才失败了但十分钟前还好好的"和"从来就没通过"，
// 那是换代理和查配置两种完全不同的处置。
func TestRecordProxyCheckKeepsLastOK(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	p, err := st.CreateProxy(ctx, testProxy("1.2.3.4"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordProxyCheck(ctx, p.ID, proxypool.CheckResult{
		Status: proxypool.StatusOK, LatencyMs: 120, Region: "ap-tokyo-1",
	}); err != nil {
		t.Fatal(err)
	}
	okAt := mustGet(t, st, p.ID).LastOKAt

	if err := st.RecordProxyCheck(ctx, p.ID, proxypool.CheckResult{
		Status: proxypool.StatusFail, Error: "超时", Region: "ap-tokyo-1",
	}); err != nil {
		t.Fatal(err)
	}
	after := mustGet(t, st, p.ID)

	if after.LastStatus != proxypool.StatusFail {
		t.Errorf("状态应为 fail，实际 %q", after.LastStatus)
	}
	if !after.LastOKAt.Equal(okAt) {
		t.Errorf("失败不该改动 last_ok_at：%v -> %v", okAt, after.LastOKAt)
	}
	if after.LastError == "" {
		t.Error("失败原因不该被丢掉")
	}
}

func mustGet(t *testing.T, st *Store, id string) *Proxy {
	t.Helper()
	p, err := st.GetProxy(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func contains(s, sub string) bool {
	return bytes.Contains([]byte(s), []byte(sub))
}
