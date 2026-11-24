package netsvc

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ocicore/internal/ociclient"
)

type rewriteTransport struct{ target *url.URL }

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = rt.target.Scheme
	cloned.URL.Host = rt.target.Host
	return http.DefaultTransport.RoundTrip(cloned)
}

func newMockOCIClient(t *testing.T, handler http.HandlerFunc) *ociclient.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := ociclient.New(&ociclient.Credentials{
		TenancyOCID: "ocid1.tenancy.oc1..t",
		UserOCID:    "ocid1.user.oc1..u",
		Fingerprint: ociclient.FingerprintOf(&key.PublicKey),
		Region:      "ap-chuncheon-1",
		PrivateKey:  key,
	}, ociclient.WithHTTPClient(&http.Client{Transport: rewriteTransport{target}}), ociclient.WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestChangePublicIPSecondaryReserved(t *testing.T) {
	prevInterval := changeIPRetryInterval
	changeIPRetryInterval = 5 * time.Millisecond
	t.Cleanup(func() { changeIPRetryInterval = prevInterval })
	prevPoll := publicIPPollInterval
	publicIPPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { publicIPPollInterval = prevPoll })

	var deletedID string
	var createBody map[string]any
	var bindAttempts int
	var oldGetPolls int
	var order []string

	client := newMockOCIClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/privateIps"):
			_, _ = w.Write([]byte(`[
				{"id":"priv-1","vnicId":"vnic-1","ipAddress":"10.0.0.10","isPrimary":true},
				{"id":"priv-2","vnicId":"vnic-1","ipAddress":"10.0.0.12","displayName":"proxy-sec","isPrimary":false}
			]`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/publicIps/actions/getByPrivateIpId"):
			_, _ = w.Write([]byte(`{"id":"pub-old","ipAddress":"152.67.1.2","lifetime":"RESERVED","displayName":"proxy1-res","privateIpId":"priv-2","lifecycleState":"ASSIGNED"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/publicIps"):
			order = append(order, "create-new")
			_ = json.NewDecoder(r.Body).Decode(&createBody)
			_, _ = w.Write([]byte(`{"id":"pub-new","ipAddress":"152.67.9.9","lifetime":"RESERVED","displayName":"proxy1-res","lifecycleState":"AVAILABLE"}`))
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/publicIps/pub-old"):
			order = append(order, "unbind-old")
			_, _ = w.Write([]byte(`{"id":"pub-old","ipAddress":"152.67.1.2","lifetime":"RESERVED","privateIpId":"priv-2","lifecycleState":"UNASSIGNING"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/publicIps/pub-old"):
			oldGetPolls++
			if oldGetPolls == 1 {
				_, _ = w.Write([]byte(`{"id":"pub-old","ipAddress":"152.67.1.2","lifetime":"RESERVED","privateIpId":"priv-2","lifecycleState":"UNASSIGNING"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"pub-old","ipAddress":"152.67.1.2","lifetime":"RESERVED","privateIpId":"","lifecycleState":"AVAILABLE"}`))
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/publicIps/pub-new"):
			bindAttempts++
			if bindAttempts == 1 {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"code":"Conflict","message":"still unassigning"}`))
				return
			}
			order = append(order, "bind-new")
			_, _ = w.Write([]byte(`{"id":"pub-new","ipAddress":"152.67.9.9","lifetime":"RESERVED","privateIpId":"priv-2","lifecycleState":"ASSIGNED"}`))
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/publicIps/pub-old"):
			order = append(order, "delete-old")
			deletedID = "pub-old"
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	// P0: 预期无公网 IP 但 OCI 实际有旧预留 IP，必须返回 ErrStateChanged 且不得删除旧 IP。
	empty := ""
	if _, err := ChangePublicIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", "comp-1", ChangePublicIPOptions{
		PrivateIPID:      "priv-2",
		AllowReserved:    true,
		ConfirmIP:        "152.67.1.2",
		ExpectPublicIPID: &empty,
	}); !errors.Is(err, ErrStateChanged) {
		t.Fatalf("乐观锁不匹配时应返回 ErrStateChanged，实际为 %v", err)
	}

	if _, err := ChangePublicIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", "comp-1", ChangePublicIPOptions{
		PrivateIPID:   "priv-2",
		AllowReserved: false,
	}); !errors.Is(err, ErrReservedIP) {
		t.Fatalf("未显式允许时应返回 ErrReservedIP，实际为 %v", err)
	}

	// P1-1: 允许更换预留 IP 但未回传正确旧 IP 地址必须返回 ErrConfirmReservedIP。
	if _, err := ChangePublicIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", "comp-1", ChangePublicIPOptions{
		PrivateIPID:   "priv-2",
		AllowReserved: true,
		ConfirmIP:     "true",
	}); !errors.Is(err, ErrConfirmReservedIP) {
		t.Fatalf("未回传匹配旧预留 IP 时应返回 ErrConfirmReservedIP，实际为 %v", err)
	}

	expectOld := "pub-old"
	res, err := ChangePublicIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", "comp-1", ChangePublicIPOptions{
		PrivateIPID:      "priv-2",
		AllowReserved:    true,
		ConfirmIP:        "152.67.1.2",
		ExpectPublicIPID: &expectOld,
	})
	if err != nil {
		t.Fatalf("更换预留 IP 失败: %v", err)
	}
	wantOrder := "create-new,unbind-old,bind-new,delete-old"
	if strings.Join(order, ",") != wantOrder {
		t.Errorf("执行顺序 = %q，期望 %q", strings.Join(order, ","), wantOrder)
	}
	if deletedID != "pub-old" || bindAttempts != 2 || oldGetPolls != 2 {
		t.Errorf("deletedID=%q bindAttempts=%d oldGetPolls=%d", deletedID, bindAttempts, oldGetPolls)
	}
	if createBody["lifetime"] != ociclient.PublicIPReserved || createBody["privateIpId"] != nil || createBody["displayName"] != "proxy1-res" {
		t.Errorf("先建新未绑定预留 IP 参数不符: %+v", createBody)
	}
	if res.OldIP != "152.67.1.2" || res.NewIP != "152.67.9.9" || res.Lifetime != ociclient.PublicIPReserved || res.IsPrimary || res.PrivateIP != "10.0.0.12" {
		t.Errorf("结果不符: %+v", res)
	}
}

func TestChangePublicIPReservedRollbackAndNoRetryOnBadRequest(t *testing.T) {
	prevInterval := changeIPRetryInterval
	changeIPRetryInterval = 5 * time.Millisecond
	t.Cleanup(func() { changeIPRetryInterval = prevInterval })

	var reboundOld bool
	var deletedNew bool
	var bindCalls int

	client := newMockOCIClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/privateIps"):
			_, _ = w.Write([]byte(`[{"id":"priv-2","vnicId":"vnic-1","ipAddress":"10.0.0.12","isPrimary":false}]`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/publicIps/actions/getByPrivateIpId"):
			_, _ = w.Write([]byte(`{"id":"pub-old","ipAddress":"152.67.1.2","lifetime":"RESERVED","privateIpId":"priv-2","lifecycleState":"ASSIGNED"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/publicIps"):
			_, _ = w.Write([]byte(`{"id":"pub-new","ipAddress":"152.67.9.9","lifetime":"RESERVED","lifecycleState":"AVAILABLE"}`))
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/publicIps/pub-old"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["privateIpId"] == "priv-2" {
				reboundOld = true
				_, _ = w.Write([]byte(`{"id":"pub-old","ipAddress":"152.67.1.2","lifetime":"RESERVED","privateIpId":"priv-2","lifecycleState":"ASSIGNED"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"pub-old","ipAddress":"152.67.1.2","lifetime":"RESERVED","privateIpId":"","lifecycleState":"AVAILABLE"}`))
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/publicIps/pub-new"):
			bindCalls++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"InvalidParameter","message":"bad request"}`))
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/publicIps/pub-new"):
			deletedNew = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	_, err := ChangePublicIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", "comp-1", ChangePublicIPOptions{
		PrivateIPID:   "priv-2",
		AllowReserved: true,
		ConfirmIP:     "152.67.1.2",
	})
	if err == nil {
		t.Fatal("绑定新预留 IP 返回 400 应当失败")
	}
	if bindCalls != 1 {
		t.Errorf("400 不应重试，实际调用 %d 次", bindCalls)
	}
	if !reboundOld || !deletedNew {
		t.Errorf("失败后应回滚重绑旧预留 IP 并清理未用新 IP: reboundOld=%v deletedNew=%v", reboundOld, deletedNew)
	}
}

func TestChangePublicIPSecondaryAssignAndForeignCheck(t *testing.T) {
	var createdBody map[string]any
	client := newMockOCIClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/privateIps"):
			_, _ = w.Write([]byte(`[
				{"id":"priv-1","vnicId":"vnic-1","ipAddress":"10.0.0.10","isPrimary":true},
				{"id":"priv-2","vnicId":"vnic-1","ipAddress":"10.0.0.12","isPrimary":false}
			]`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/publicIps/actions/getByPrivateIpId"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NotAuthorizedOrNotFound","message":"not found"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/publicIps"):
			_ = json.NewDecoder(r.Body).Decode(&createdBody)
			_, _ = w.Write([]byte(`{"id":"pub-new","ipAddress":"168.107.8.8","lifetime":"RESERVED","privateIpId":"priv-2"}`))
		default:
			http.NotFound(w, r)
		}
	})

	if _, err := ChangePublicIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", "comp-1", ChangePublicIPOptions{
		PrivateIPID:   "priv-other-vnic",
		AllowReserved: true,
	}); err == nil {
		t.Fatal("不属于该网卡的 privateIpId 应当报错")
	}

	res, err := ChangePublicIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", "comp-1", ChangePublicIPOptions{
		PrivateIPID:   "priv-2",
		AllowReserved: true,
	})
	if err != nil {
		t.Fatalf("分配辅助 IP 保留公网地址失败: %v", err)
	}
	if createdBody["lifetime"] != ociclient.PublicIPReserved || res.OldIP != "" || res.NewIP != "168.107.8.8" {
		t.Errorf("分配结果不符: body=%+v res=%+v", createdBody, res)
	}
}

func TestBindReservedIPReplacesEphemeralAndUnassignsSafely(t *testing.T) {
	origInterval := changeIPRetryInterval
	changeIPRetryInterval = time.Millisecond
	t.Cleanup(func() { changeIPRetryInterval = origInterval })

	var deletedID string
	var putCalls []string
	var putBodies []string
	bindAttempts := 0

	client := newMockOCIClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/privateIps"):
			_, _ = w.Write([]byte(`[
				{"id":"priv-1","vnicId":"vnic-1","ipAddress":"10.0.0.10","isPrimary":true},
				{"id":"priv-2","vnicId":"vnic-1","ipAddress":"10.0.0.12","isPrimary":false}
			]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/publicIps/pub-free"):
			_, _ = w.Write([]byte(`{"id":"pub-free","ipAddress":"152.67.5.5","lifetime":"RESERVED","lifecycleState":"AVAILABLE"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/publicIps/pub-busy"):
			_, _ = w.Write([]byte(`{"id":"pub-busy","ipAddress":"152.67.6.6","lifetime":"RESERVED","privateIpId":"priv-other","lifecycleState":"ASSIGNED"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/publicIps/actions/getByPrivateIpId"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["privateIpId"] == "priv-1" {
				_, _ = w.Write([]byte(`{"id":"pub-eph","ipAddress":"168.107.1.1","lifetime":"EPHEMERAL","privateIpId":"priv-1"}`))
			} else {
				_, _ = w.Write([]byte(`{"id":"pub-res-2","ipAddress":"158.180.2.2","lifetime":"RESERVED","privateIpId":"priv-2"}`))
			}
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/publicIps/pub-eph"):
			deletedID = "pub-eph"
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/publicIps/"):
			raw := new(strings.Builder)
			buf := make([]byte, 256)
			n, _ := r.Body.Read(buf)
			raw.Write(buf[:n])
			putCalls = append(putCalls, r.URL.Path)
			putBodies = append(putBodies, raw.String())
			if strings.HasSuffix(r.URL.Path, "/publicIps/pub-free") {
				bindAttempts++
				if bindAttempts == 1 {
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"code":"Conflict","message":"ephemeral still releasing"}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"pub-free","ipAddress":"152.67.5.5","lifetime":"RESERVED","privateIpId":"priv-1","lifecycleState":"ASSIGNED"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"pub-res-2","ipAddress":"158.180.2.2","lifetime":"RESERVED","privateIpId":"","lifecycleState":"AVAILABLE"}`))
		default:
			http.NotFound(w, r)
		}
	})

	if _, err := BindReservedIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", BindReservedIPOptions{
		ReservedIPID: "pub-busy",
	}); err == nil {
		t.Fatal("已绑定其他私网的预留 IP 应当拒绝直接绑定")
	}

	res, err := BindReservedIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", BindReservedIPOptions{
		ReservedIPID: "pub-free",
	})
	if err != nil {
		t.Fatalf("主私网槽从临时 IP 绑定预留 IP 失败: %v", err)
	}
	if deletedID != "pub-eph" || res.OldIP != "168.107.1.1" || res.NewIP != "152.67.5.5" || bindAttempts != 2 {
		t.Fatalf("绑定结果不符: deleted=%s attempts=%d res=%+v", deletedID, bindAttempts, res)
	}

	unRes, err := UnassignPublicIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", UnassignPublicIPOptions{PrivateIPID: "priv-2"})
	if err != nil {
		t.Fatalf("解绑预留 IP 失败: %v", err)
	}
	if unRes.OldIP != "158.180.2.2" || unRes.Lifetime != ociclient.PublicIPReserved {
		t.Fatalf("解绑预留 IP 结果不符: %+v", unRes)
	}
	lastBody := putBodies[len(putBodies)-1]
	if !strings.Contains(lastBody, `"privateIpId":""`) {
		t.Fatalf("解绑预留 IP 必须显式发送空字符串 privateIpId，实际: %s", lastBody)
	}

	deletedID = ""
	unEph, err := UnassignPublicIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", UnassignPublicIPOptions{})
	if err != nil {
		t.Fatalf("取消绑定临时 IP 失败: %v", err)
	}
	if deletedID != "pub-eph" || unEph.OldIP != "168.107.1.1" || unEph.Lifetime != ociclient.PublicIPEphemeral {
		t.Fatalf("取消绑定临时 IP 结果不符: deleted=%s res=%+v", deletedID, unEph)
	}
}

func TestSwitchToEphemeralIPUnbindsReservedAndRejectsSecondary(t *testing.T) {
	origInterval := changeIPRetryInterval
	changeIPRetryInterval = time.Millisecond
	t.Cleanup(func() { changeIPRetryInterval = origInterval })

	var deletedAny bool
	var putBody string
	var createdBody map[string]any
	createAttempts := 0

	client := newMockOCIClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/privateIps"):
			_, _ = w.Write([]byte(`[
				{"id":"priv-1","vnicId":"vnic-1","ipAddress":"10.0.0.10","isPrimary":true,"compartmentId":"comp-priv-1"},
				{"id":"priv-2","vnicId":"vnic-1","ipAddress":"10.0.0.12","isPrimary":false,"compartmentId":"comp-priv-1"}
			]`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/publicIps/actions/getByPrivateIpId"):
			_, _ = w.Write([]byte(`{"id":"pub-res-1","ipAddress":"152.67.5.5","lifetime":"RESERVED","privateIpId":"priv-1"}`))
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/publicIps/"):
			deletedAny = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/publicIps/pub-res-1"):
			buf := make([]byte, 256)
			n, _ := r.Body.Read(buf)
			putBody = string(buf[:n])
			_, _ = w.Write([]byte(`{"id":"pub-res-1","ipAddress":"152.67.5.5","lifetime":"RESERVED","privateIpId":"","lifecycleState":"AVAILABLE"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/publicIps"):
			createAttempts++
			if createAttempts == 1 {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"code":"Conflict","message":"reserved IP still unbinding"}`))
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&createdBody)
			_, _ = w.Write([]byte(`{"id":"pub-eph-new","ipAddress":"168.107.99.9","lifetime":"EPHEMERAL","privateIpId":"priv-1"}`))
		default:
			http.NotFound(w, r)
		}
	})

	if _, err := SwitchToEphemeralIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", "comp-1", SwitchToEphemeralIPOptions{PrivateIPID: "priv-2"}); err == nil {
		t.Fatal("辅助私网 IP 切回临时公网 IP 必须被拒绝")
	}

	res, err := SwitchToEphemeralIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", "comp-1", SwitchToEphemeralIPOptions{})
	if err != nil {
		t.Fatalf("主私网 IP 切回临时 IP 失败: %v", err)
	}
	if deletedAny {
		t.Fatal("切回临时 IP 不得删除原预留 IP，只能无损解绑")
	}
	if !strings.Contains(putBody, `"privateIpId":""`) {
		t.Fatalf("解绑原预留 IP 必须发送空 privateIpId，实际: %s", putBody)
	}
	if createAttempts != 2 || createdBody["lifetime"] != ociclient.PublicIPEphemeral || createdBody["privateIpId"] != "priv-1" || createdBody["compartmentId"] != "comp-priv-1" {
		t.Fatalf("申请临时 IP 参数不符: attempts=%d body=%+v", createAttempts, createdBody)
	}
	if res.OldIP != "152.67.5.5" || res.OldLifetime != ociclient.PublicIPReserved || res.NewIP != "168.107.99.9" || res.Lifetime != ociclient.PublicIPEphemeral {
		t.Fatalf("切回结果不符: %+v", res)
	}
}

func TestSecondaryPrivateIPCreateAndDelete(t *testing.T) {
	origInterval := changeIPRetryInterval
	changeIPRetryInterval = time.Millisecond
	t.Cleanup(func() { changeIPRetryInterval = origInterval })

	var createReq map[string]any
	var putBody string
	var deletedPublic bool
	var deleteAttempts int

	client := newMockOCIClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/privateIps"):
			_ = json.NewDecoder(r.Body).Decode(&createReq)
			ip := "10.0.0.42"
			if v, ok := createReq["ipAddress"].(string); ok && v != "" {
				ip = v
			}
			name, _ := createReq["displayName"].(string)
			_, _ = w.Write([]byte(`{"id":"priv-new","vnicId":"vnic-1","ipAddress":"` + ip + `","displayName":"` + name + `","isPrimary":false}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/privateIps"):
			_, _ = w.Write([]byte(`[
				{"id":"priv-1","vnicId":"vnic-1","ipAddress":"10.0.0.2","isPrimary":true},
				{"id":"priv-2","vnicId":"vnic-1","ipAddress":"10.0.0.9","isPrimary":false}
			]`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/publicIps/actions/getByPrivateIpId"):
			_, _ = w.Write([]byte(`{"id":"pub-res-2","ipAddress":"152.67.9.9","lifetime":"RESERVED","privateIpId":"priv-2"}`))
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/publicIps/"):
			deletedPublic = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/publicIps/pub-res-2"):
			buf := make([]byte, 256)
			n, _ := r.Body.Read(buf)
			putBody = string(buf[:n])
			_, _ = w.Write([]byte(`{"id":"pub-res-2","ipAddress":"152.67.9.9","lifetime":"RESERVED","privateIpId":"","lifecycleState":"AVAILABLE"}`))
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/privateIps/priv-2"):
			deleteAttempts++
			if deleteAttempts == 1 {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"code":"Conflict","message":"public IP still unassigning"}`))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	if _, err := CreateSecondaryPrivateIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", "bad", "not-an-ip"); !errors.Is(err, ErrInvalidPrivateIP) {
		t.Fatalf("非法 IPv4 应返回 ErrInvalidPrivateIP，实际: %v", err)
	}

	created, err := CreateSecondaryPrivateIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", "extra-1", "10.0.0.55")
	if err != nil {
		t.Fatalf("创建辅助私网 IP 失败: %v", err)
	}
	if created.PrivateIPID != "priv-new" || created.PrivateIP != "10.0.0.55" || created.DisplayName != "extra-1" {
		t.Fatalf("创建结果不符: %+v", created)
	}

	if _, err := DeleteSecondaryPrivateIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", "priv-1"); !errors.Is(err, ErrPrimaryPrivateIP) {
		t.Fatalf("删除主私网 IP 应返回 ErrPrimaryPrivateIP，实际: %v", err)
	}
	if _, err := DeleteSecondaryPrivateIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", "priv-foreign"); !errors.Is(err, ErrPrivateIPNotFound) {
		t.Fatalf("删除不属于当前网卡的私网 IP 应返回 ErrPrivateIPNotFound，实际: %v", err)
	}

	deleted, err := DeleteSecondaryPrivateIP(t.Context(), client, "ap-chuncheon-1", "vnic-1", "priv-2")
	if err != nil {
		t.Fatalf("删除辅助私网 IP 失败: %v", err)
	}
	if deletedPublic {
		t.Fatal("释放辅助私网 IP 时不得删除其绑定的预留公网 IP")
	}
	if !strings.Contains(putBody, `"privateIpId":""`) {
		t.Fatalf("应先发送空 privateIpId 无损解绑预留 IP，实际: %s", putBody)
	}
	if deleteAttempts != 2 || deleted.PrivateIP != "10.0.0.9" || deleted.UnboundPublicIP != "152.67.9.9" {
		t.Fatalf("删除结果不符: attempts=%d res=%+v", deleteAttempts, deleted)
	}
}
