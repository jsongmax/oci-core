package notify

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ocicore/internal/store"
)

func TestSubscribes(t *testing.T) {
	events := []string{EventInstanceCreated, EventAccountAuthFail}

	if !subscribes(events, EventInstanceCreated) {
		t.Error("已订阅的事件应当匹配")
	}
	if subscribes(events, EventQuotaNearLimit) {
		t.Error("未订阅的事件不应匹配")
	}
	if subscribes(nil, EventInstanceCreated) {
		t.Error("空订阅列表不应匹配任何事件")
	}
}

func TestPlainTextIncludesAllParts(t *testing.T) {
	msg := Message{
		Title:  "实例已就绪",
		Body:   "ssh ubuntu@1.2.3.4",
		Fields: map[string]string{"区域": "ap-tokyo-1"},
	}
	got := plainText(msg)

	for _, want := range []string{"实例已就绪", "ssh ubuntu@1.2.3.4", "区域", "ap-tokyo-1"} {
		if !strings.Contains(got, want) {
			t.Errorf("渲染结果缺少 %q:\n%s", want, got)
		}
	}
}

func TestEventDefsAndKindDefsAreComplete(t *testing.T) {
	events := EventDefs()
	if len(events) == 0 {
		t.Fatal("应当提供可订阅事件清单")
	}
	for _, e := range events {
		if e.Key == "" || e.Label == "" || e.Description == "" {
			t.Errorf("事件定义不完整: %+v", e)
		}
	}

	kinds := KindDefs()
	if len(kinds) < 5 {
		t.Fatalf("应当支持至少 5 种渠道，实际 %d", len(kinds))
	}
	for _, k := range kinds {
		if k.Kind == "" || k.Label == "" || len(k.Fields) == 0 {
			t.Errorf("渠道定义不完整: %+v", k)
		}
		for _, f := range k.Fields {
			if f.Key == "" || f.Label == "" {
				t.Errorf("渠道 %s 的字段定义不完整: %+v", k.Kind, f)
			}
		}
	}
}

// Telegram 的 token 与各类 webhook 都必须标记为 secret，
// HTTP 层据此决定哪些字段需要打码后才能下发给前端。
func TestSecretFieldsAreMarked(t *testing.T) {
	mustBeSecret := map[string][]string{
		KindTelegram: {"token"},
		KindWeCom:    {"webhook"},
		KindDingTalk: {"webhook", "secret"},
		KindEmail:    {"password"},
	}

	for _, k := range KindDefs() {
		want, ok := mustBeSecret[k.Kind]
		if !ok {
			continue
		}
		marked := map[string]bool{}
		for _, f := range k.Fields {
			if f.Secret {
				marked[f.Key] = true
			}
		}
		for _, key := range want {
			if !marked[key] {
				t.Errorf("渠道 %s 的字段 %q 必须标记为 secret", k.Kind, key)
			}
		}
	}
}

func TestSendWebhookPostsPayload(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := loopbackDispatcher()
	ch := &store.Channel{Kind: KindWebhook, Config: map[string]string{"url": srv.URL}}

	err := d.Send(context.Background(), ch, Message{
		Event: EventInstanceCreated, Title: "实例已就绪",
	})
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if received["title"] != "实例已就绪" {
		t.Errorf("收到的 payload 不正确: %+v", received)
	}
	if received["source"] != "ocicore" {
		t.Errorf("payload 应带 source 标识: %+v", received)
	}
}

// 企业微信和钉钉在参数错误时依然返回 HTTP 200，只在 body 里写 errcode。
// 只看状态码会把失败当成功，用户到真出事时才发现一直没收到通知。
func TestSendDetectsBusinessErrorInBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":93000,"errmsg":"invalid webhook url"}`))
	}))
	defer srv.Close()

	d := loopbackDispatcher()
	ch := &store.Channel{Kind: KindWeCom, Config: map[string]string{"webhook": srv.URL}}

	err := d.Send(context.Background(), ch, Message{Title: "测试"})
	if err == nil {
		t.Fatal("响应体里的 errcode 应当被识别为失败")
	}
	if !strings.Contains(err.Error(), "93000") {
		t.Errorf("错误信息应包含业务错误码: %v", err)
	}
}

func TestSendReportsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	defer srv.Close()

	d := loopbackDispatcher()
	ch := &store.Channel{Kind: KindWebhook, Config: map[string]string{"url": srv.URL}}

	err := d.Send(context.Background(), ch, Message{Title: "测试"})
	if err == nil {
		t.Fatal("HTTP 401 应当返回错误")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("错误信息应包含状态码: %v", err)
	}
}

func TestSendRejectsIncompleteConfig(t *testing.T) {
	d := NewDispatcher(nil)
	cases := []*store.Channel{
		{Kind: KindTelegram, Config: map[string]string{"token": "x"}}, // 缺 chatId
		{Kind: KindWeCom, Config: map[string]string{}},
		{Kind: KindWebhook, Config: map[string]string{}},
		{Kind: "unknown", Config: map[string]string{}},
	}
	for _, ch := range cases {
		if err := d.Send(context.Background(), ch, Message{Title: "x"}); err == nil {
			t.Errorf("渠道 %s 配置不全时应当报错", ch.Kind)
		}
	}
}

// 465 端口是隐式 TLS：连上后客户端必须先发 TLS ClientHello，
// 而不是像 587 那样等服务器的明文问候。
func TestSendMailImplicitTLSStartsHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	first := make(chan byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1)
		if _, err := conn.Read(buf); err == nil {
			first <- buf[0]
		}
	}()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = sendMail(ctx, host, port, SecuritySSL, nil, "a@x", []string{"b@x"}, []byte("x"))

	select {
	case b := <-first:
		if b != 0x16 { // TLS handshake record
			t.Errorf("首字节 = %#x, 期望 TLS 握手记录 0x16", b)
		}
	default:
		t.Error("客户端没有主动发起 TLS 握手")
	}
}

// 服务器连上后一言不发（例如对 465 端口用了明文），发送必须按 ctx 超时返回，
// 而不是永远挂着让前端停在"发送中"。
func TestSendMailHonorsContextWhenServerSilent(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn)
	}()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = sendMail(ctx, host, port, SecurityAuto, nil, "a@x", []string{"b@x"}, []byte("x"))
	if err == nil {
		t.Fatal("服务器不响应时应当报错")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("超时后没有及时返回，耗时 %v", elapsed)
	}
}

// 选了 STARTTLS 而服务器不支持时，必须报错，不能退回明文发送。
func TestSendMailRequiredStartTLSRejectsPlainServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 512)
		_, _ = conn.Write([]byte("220 fake ESMTP\r\n"))
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			line := string(buf[:n])
			switch {
			case strings.HasPrefix(line, "EHLO"):
				_, _ = conn.Write([]byte("250-fake\r\n250 AUTH PLAIN\r\n"))
			case strings.HasPrefix(line, "QUIT"):
				_, _ = conn.Write([]byte("221 bye\r\n"))
				return
			default:
				_, _ = conn.Write([]byte("250 ok\r\n"))
			}
		}
	}()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = sendMail(ctx, host, port, SecurityStartTLS, nil, "a@x", []string{"b@x"}, []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("err = %v, 期望提示不支持 STARTTLS", err)
	}
}
