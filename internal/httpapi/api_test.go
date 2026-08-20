package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

// ---- 设置 ----

func TestSettingsRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	resp := h.get("/api/settings")
	expectStatus(t, resp, http.StatusOK)
	settings := decode[store.Settings](t, resp)
	if !settings.AllowTerminate {
		t.Error("默认应允许终止实例")
	}

	resp = h.do("PATCH", "/api/settings", map[string]any{"allowTerminate": false}, true)
	expectStatus(t, resp, http.StatusOK)
	out := decode[struct {
		Settings store.Settings `json:"settings"`
	}](t, resp)
	if out.Settings.AllowTerminate {
		t.Error("更新后应禁止终止实例")
	}
	if !out.Settings.AllowBulkActions {
		t.Error("未提交的字段不应被改动")
	}
}

func TestSettingsRejectsBadSyncInterval(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	resp := h.do("PATCH", "/api/settings", map[string]any{"syncIntervalMinutes": 0}, true)
	expectStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()
}

// ---- 通知渠道 ----

func TestChannelCRUDOverHTTP(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	resp := h.post("/api/notifications/channels", map[string]any{
		"kind":   "telegram",
		"name":   "我的 TG",
		"config": map[string]string{"token": "123456:ABCDEFGHIJKLMNOP", "chatId": "999"},
		"events": []string{"instance.created"},
	})
	expectStatus(t, resp, http.StatusCreated)
	created := decode[store.Channel](t, resp)
	if created.Name != "我的 TG" {
		t.Errorf("名称 = %q", created.Name)
	}

	resp = h.get("/api/notifications/channels")
	expectStatus(t, resp, http.StatusOK)
	list := decode[struct {
		Channels []store.Channel `json:"channels"`
		Kinds    []struct {
			Kind string `json:"kind"`
		} `json:"kinds"`
	}](t, resp)
	if len(list.Channels) != 1 {
		t.Fatalf("渠道数量 = %d，期望 1", len(list.Channels))
	}
	if len(list.Kinds) < 5 {
		t.Errorf("应返回渠道类型定义，实际 %d 种", len(list.Kinds))
	}

	resp = h.do("DELETE", "/api/notifications/channels/"+created.ID, nil, true)
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()
}

// token 与 webhook 一旦泄露就等于把机器人拱手让人，绝不能回流到客户端。
func TestChannelSecretsAreMasked(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	const token = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	resp := h.post("/api/notifications/channels", map[string]any{
		"kind":   "telegram",
		"name":   "我的 TG",
		"config": map[string]string{"token": token, "chatId": "999"},
	})
	expectStatus(t, resp, http.StatusCreated)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if bytes.Contains(raw, []byte(token)) {
		t.Error("创建响应中出现了完整的 token")
	}

	resp = h.get("/api/notifications/channels")
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if bytes.Contains(raw, []byte(token)) {
		t.Error("列表响应中出现了完整的 token")
	}
	// chatId 不是机密，应当原样返回，否则用户没法确认自己配对了没有。
	if !bytes.Contains(raw, []byte("999")) {
		t.Error("非机密字段不应被打码")
	}
}

// 前端拿到的是打码值，原样提交回来不能把真实配置覆盖成一串圆点。
func TestChannelUpdateIgnoresMaskedSecrets(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	const token = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	resp := h.post("/api/notifications/channels", map[string]any{
		"kind":   "telegram",
		"name":   "我的 TG",
		"config": map[string]string{"token": token, "chatId": "999"},
	})
	expectStatus(t, resp, http.StatusCreated)
	created := decode[store.Channel](t, resp)

	// 把打码后的值原样提交回去。
	resp = h.do("PATCH", "/api/notifications/channels/"+created.ID, map[string]any{
		"config": map[string]string{"token": created.Config["token"], "chatId": "888"},
	}, true)
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// 真实 token 必须还在库里。
	ch, err := h.store.GetChannel(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Config["token"] != token {
		t.Errorf("真实 token 被打码值覆盖了：%q", ch.Config["token"])
	}
	if ch.Config["chatId"] != "888" {
		t.Errorf("非机密字段应当被更新，实际 %q", ch.Config["chatId"])
	}
}

func TestChannelRejectsIncompleteConfig(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	resp := h.post("/api/notifications/channels", map[string]any{
		"kind":   "telegram",
		"name":   "缺 chatId",
		"config": map[string]string{"token": "x"},
	})
	expectStatus(t, resp, http.StatusBadRequest)
	body := decode[errorBody](t, resp)
	if !strings.Contains(body.Message, "Chat ID") {
		t.Errorf("错误信息应指出缺哪个字段: %q", body.Message)
	}

	resp = h.post("/api/notifications/channels", map[string]any{
		"kind": "carrier-pigeon", "name": "x",
	})
	expectStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()
}

func TestNotificationEventsListed(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	resp := h.get("/api/notifications/events")
	expectStatus(t, resp, http.StatusOK)
	out := decode[struct {
		Events []struct {
			Key   string `json:"key"`
			Label string `json:"label"`
		} `json:"events"`
	}](t, resp)
	if len(out.Events) < 5 {
		t.Errorf("可订阅事件应当至少 5 种，实际 %d", len(out.Events))
	}
}

// ---- 实例 ----

func TestListInstancesEmpty(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	resp := h.get("/api/instances")
	expectStatus(t, resp, http.StatusOK)
	out := decode[struct {
		Instances []store.Instance `json:"instances"`
		Sync      struct {
			Syncing bool `json:"syncing"`
		} `json:"sync"`
	}](t, resp)

	if len(out.Instances) != 0 {
		t.Errorf("应返回空列表，实际 %d 条", len(out.Instances))
	}
	if out.Sync.Syncing {
		t.Error("初始不应处于同步中")
	}
}

func TestInstanceEndpointsRequireAuth(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{
		"/api/instances", "/api/overview", "/api/quota",
		"/api/settings", "/api/notifications/channels",
		"/api/network/rule-templates", "/api/launch/presets",
	} {
		resp := h.get(path)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s 未登录时状态码 = %d，期望 401", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestInstanceNotFound(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	resp := h.get("/api/instances/ocid1.instance.oc1..nonexistent")
	expectStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
}

// 终止是 L3 操作，禁用开关必须在服务端生效——前端的按钮可以被绕过。
func TestTerminateBlockedByPolicy(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	acc, err := h.store.CreateAccount(t.Context(), storeTestAccount(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.UpsertInstance(t.Context(), store.Instance{
		ID: "i-1", AccountID: acc.ID, Region: "ap-tokyo-1",
		DisplayName: "web-01", LifecycleState: "RUNNING",
	}); err != nil {
		t.Fatal(err)
	}

	no := false
	if _, err := h.store.UpdateSettings(t.Context(), store.SettingsUpdate{AllowTerminate: &no}); err != nil {
		t.Fatal(err)
	}

	resp := h.do("DELETE", "/api/instances/i-1?confirm=web-01", nil, true)
	expectStatus(t, resp, http.StatusForbidden)
	if body := decode[errorBody](t, resp); body.Code != "terminate_disabled" {
		t.Errorf("错误码 = %q，期望 terminate_disabled", body.Code)
	}
}

// 允许终止时仍然要过输名确认这一关。
func TestTerminateRequiresInstanceName(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	acc, err := h.store.CreateAccount(t.Context(), storeTestAccount(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.UpsertInstance(t.Context(), store.Instance{
		ID: "i-1", AccountID: acc.ID, Region: "ap-tokyo-1",
		DisplayName: "web-01", LifecycleState: "RUNNING",
	}); err != nil {
		t.Fatal(err)
	}

	resp := h.do("DELETE", "/api/instances/i-1", nil, true)
	expectStatus(t, resp, http.StatusBadRequest)
	if body := decode[errorBody](t, resp); body.Code != "confirm_required" {
		t.Errorf("错误码 = %q，期望 confirm_required", body.Code)
	}

	resp = h.do("DELETE", "/api/instances/i-1?confirm=wrong-name", nil, true)
	expectStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()
}

// 强制关机/重启会直接拔电源，必须显式带 force=true。
func TestForcefulActionRequiresFlag(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	acc, err := h.store.CreateAccount(t.Context(), storeTestAccount(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.UpsertInstance(t.Context(), store.Instance{
		ID: "i-1", AccountID: acc.ID, Region: "ap-tokyo-1",
		DisplayName: "web-01", LifecycleState: "RUNNING",
	}); err != nil {
		t.Fatal(err)
	}

	for _, action := range []string{"STOP", "RESET"} {
		resp := h.post("/api/instances/i-1/actions/"+action, nil)
		expectStatus(t, resp, http.StatusBadRequest)
		if body := decode[errorBody](t, resp); body.Code != "confirm_required" {
			t.Errorf("%s 的错误码 = %q，期望 confirm_required", action, body.Code)
		}
	}
}

// ---- 只读参考数据 ----

func TestReferenceEndpoints(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	resp := h.get("/api/launch/presets")
	expectStatus(t, resp, http.StatusOK)
	presets := decode[struct {
		Presets []LaunchPreset `json:"presets"`
	}](t, resp)
	if len(presets.Presets) == 0 {
		t.Fatal("应提供创建实例的快捷预设")
	}
	// 免费额度满配是绝大多数用户的目标，必须有这一档。
	//
	// 断言绑常量而不是字面量：Oracle 改过一次免费额度（2026-06-15 从
	// 4C24G 砍到 2C12G），下次再改时改常量就够，不必回来改测试。
	found := false
	for _, p := range presets.Presets {
		if p.Shape == "VM.Standard.A1.Flex" && p.FreeTier &&
			p.Ocpus == ociclient.AlwaysFreeARMOcpus &&
			p.MemoryInGBs == ociclient.AlwaysFreeARMMemoryGB {
			found = true
		}
	}
	if !found {
		t.Errorf("缺少 ARM 永久免费满配预设（%d OCPU / %d GB）",
			ociclient.AlwaysFreeARMOcpus, ociclient.AlwaysFreeARMMemoryGB)
	}
	// 旧的 4C24G 档必须明确标为非免费额度，否则永久免费号照着开会被回收。
	for _, p := range presets.Presets {
		if p.Ocpus == ociclient.LegacyFreeARMOcpus && p.FreeTier {
			t.Error("4C24G 档不应标记为 freeTier —— 永久免费号照此创建会被 Oracle 回收")
		}
	}

	resp = h.get("/api/network/rule-templates")
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = h.get("/api/regions")
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()
}

// 需要账号上下文的接口在缺少 accountId 时要给出明确提示。
func TestNetworkEndpointsRequireAccount(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	for _, path := range []string{
		"/api/network/vcns", "/api/network/subnets",
		"/api/storage/boot-volumes", "/api/launch/shapes",
	} {
		resp := h.get(path)
		expectStatus(t, resp, http.StatusBadRequest)
		if body := decode[errorBody](t, resp); body.Code != "missing_account" {
			t.Errorf("%s 的错误码 = %q，期望 missing_account", path, body.Code)
		}
	}
}

// storeTestAccount 造一份可入库的账号输入。
func storeTestAccount(t *testing.T) store.NewAccount {
	t.Helper()
	body := testAccountBody(t, "东京主号", "TYO", "tokyo")
	return store.NewAccount{
		Alias:         body["alias"].(string),
		Code:          body["code"].(string),
		TenancyOCID:   body["tenancyOcid"].(string),
		UserOCID:      body["userOcid"].(string),
		Fingerprint:   body["fingerprint"].(string),
		PrivateKeyPEM: body["privateKeyPem"].(string),
		DefaultRegion: body["defaultRegion"].(string),
	}
}

// TestListEndpointsNeverReturnNull 锁住"列表接口永远返回列表"这条约定。
//
// Go 的 nil 切片序列化成 JSON null，前端 .forEach 直接抛
// "Cannot read properties of null"——而且异常会被 catch 吞成
// "这个区域查询失败"，排障时完全指错方向。存储页就是这么炸的。
func TestListEndpointsNeverReturnNull(t *testing.T) {
	for _, tc := range []struct {
		path  string
		field string
	}{
		{"/api/notifications/channels", "channels"},
		{"/api/notifications/events", "events"},
		{"/api/launch/presets", "presets"},
		{"/api/regions", "regions"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			h := newHarness(t)
			h.setupAndLogin()
			resp := h.get(tc.path)
			expectStatus(t, resp, http.StatusOK)
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			var parsed map[string]json.RawMessage
			if err := json.Unmarshal(body, &parsed); err != nil {
				t.Fatalf("响应不是 JSON 对象: %v", err)
			}
			raw, ok := parsed[tc.field]
			if !ok {
				t.Fatalf("响应缺少字段 %s: %s", tc.field, body)
			}
			if string(raw) == "null" {
				t.Errorf("%s 返回 null，应当返回 []", tc.field)
			}
		})
	}
}
