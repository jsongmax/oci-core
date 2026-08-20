package httpapi

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ocicore/internal/instancesvc"
)

// TestEventStreamSurvivesWriteTimeout 锁住 SSE 不受 http.Server 写超时影响。
//
// WriteTimeout 是从请求被接受起算的**绝对**截止时间，不是空闲计时器。SSE 是
// 一个永不结束的响应，配了 WriteTimeout 之后，截止时间一过，下一次写就会失败、
// 连接被服务端自己掐掉——浏览器控制台里表现为 ERR_HTTP2_PROTOCOL_ERROR 200：
// 响应头早发出去了所以是 200，流被中途重置所以是 protocol error。
//
// 这个失效没有功能性症状：EventSource 会自动重连，界面看起来一切正常，
// 只是每隔一分钟丢几秒的实时推送。正因为"不修也能用"，很容易在后续改动里
// 被无意还原，所以拿测试钉住。
//
// 测试必须在越过截止时间**之后触发一次写**。WriteTimeout 只在发生写操作时
// 才生效，光 sleep 不推事件的话，把修复删掉测试照样通过，等于什么都没测——
// 第一版就是这么写的。
func TestEventStreamSurvivesWriteTimeout(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	// 用一个远小于测试时长的写超时重建服务器。生产上是 60 秒，
	// 这里 300 毫秒——只要机制对，两者没有区别。
	const writeTimeout = 300 * time.Millisecond
	srv := httptest.NewUnstartedServer(h.srv.Config.Handler)
	srv.Config.WriteTimeout = writeTimeout
	srv.Start()
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-OCI-Tools", "1")
	// 会话 cookie 是发给 harness 那台服务器的，这里要手动搬过来。
	for _, c := range h.client.Jar.Cookies(mustParseURL(t, h.srv.URL)) {
		req.AddCookie(c)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("建立 SSE 连接失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 %d，期望 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q，期望 text/event-stream", ct)
	}

	reader := bufio.NewReader(resp.Body)

	// 先读掉开头的 retry:，确认流确实建起来了。
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("读取首行失败: %v", err)
	}
	if !strings.HasPrefix(line, "retry:") {
		t.Fatalf("首行是 %q，期望以 retry: 开头", strings.TrimSpace(line))
	}

	// 越过写超时，然后推一条事件——那次写才会撞上过期的 deadline。
	time.Sleep(writeTimeout * 2)

	type readResult struct {
		text string
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		var sb strings.Builder
		for {
			l, err := reader.ReadString('\n')
			sb.WriteString(l)
			if err != nil {
				done <- readResult{sb.String(), err}
				return
			}
			if strings.Contains(sb.String(), "test.ping") {
				done <- readResult{sb.String(), nil}
				return
			}
		}
	}()

	// 给读侧一点时间进入阻塞再推。
	time.Sleep(50 * time.Millisecond)
	h.bus.Publish(instancesvc.Event{Type: "test.ping", Message: "hello"})

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("越过 WriteTimeout 之后推事件失败：%v —— "+
				"handleEvents 里解除写超时的那段是不是被去掉了？", r.err)
		}
		if !strings.Contains(r.text, "test.ping") {
			t.Fatalf("没收到推送的事件，收到的是 %q", r.text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("越过 WriteTimeout 之后再也收不到事件，推流已经死了")
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
