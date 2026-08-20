package ociclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newLimitedClient 造一个指向 srv 的客户端，凭据用测试密钥。
func newLimitedClient(t *testing.T, srv *httptest.Server, l *Limiter, perTenancy int) *Client {
	t.Helper()
	creds := testCreds(t, testKey(t))
	c, err := New(creds, WithLimiter(l), WithTenancyConcurrency(perTenancy),
		WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// peakTracker 记录并发峰值。
type peakTracker struct {
	cur, peak atomic.Int64
}

func (p *peakTracker) enter() {
	n := p.cur.Add(1)
	for {
		old := p.peak.Load()
		if n <= old || p.peak.CompareAndSwap(old, n) {
			return
		}
	}
}

func (p *peakTracker) leave() { p.cur.Add(-1) }

// TestLimiterCapsConcurrency 验证并发上限真的生效。
//
// 没有这个测试，限流器写错方向（比如信号量满了就直接放行）也不会有任何
// 症状——请求照常成功，只是保护没了。而这种失效恰恰要等到多账号高并发
// 时才会以 429 的形式暴露出来，那时候已经很难联想到是限流器的问题。
func TestLimiterCapsConcurrency(t *testing.T) {
	var peak peakTracker
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peak.enter()
		defer peak.leave()
		// 停一会儿，让并发有机会真正叠起来。
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	const (
		globalCap  = 4
		tenancyCap = 3
		tenancies  = 5
		perTenancy = 10
	)

	limiter := NewLimiter(globalCap)
	var wg sync.WaitGroup
	for i := 0; i < tenancies; i++ {
		c := newLimitedClient(t, srv, limiter, tenancyCap)
		for j := 0; j < perTenancy; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = c.attempt(context.Background(), http.MethodGet, srv.URL, nil, nil)
			}()
		}
	}
	wg.Wait()

	if got := peak.peak.Load(); got > globalCap {
		t.Errorf("并发峰值 %d 超过全局上限 %d —— 限流器没有生效", got, globalCap)
	}
	if got := peak.peak.Load(); got == 0 {
		t.Error("一个请求都没打出去，测试本身有问题")
	}
}

// TestTenancyCapIsPerClient 验证租户级上限是按客户端算的，不是全局共用一个。
func TestTenancyCapIsPerClient(t *testing.T) {
	var peak peakTracker
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peak.enter()
		defer peak.leave()
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// 全局放得很宽，只看租户级这一层。两个租户各限 2，峰值应当能到 4——
	// 如果租户级信号量被写成共享的，峰值会卡在 2。
	limiter := NewLimiter(100)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		c := newLimitedClient(t, srv, limiter, 2)
		for j := 0; j < 8; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = c.attempt(context.Background(), http.MethodGet, srv.URL, nil, nil)
			}()
		}
	}
	wg.Wait()

	if got := peak.peak.Load(); got > 4 {
		t.Errorf("并发峰值 %d 超过两个租户各 2 的上限", got)
	}
}

// TestLimiterRespectsContext 验证排队时 context 取消能立刻返回。
//
// 用户关掉页面后请求应当尽快松手，而不是在信号量上一直排到超时——
// 那会让已经没人要的请求继续占着名额，把后面真正要发的请求堵在外面。
func TestLimiterRespectsContext(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	defer close(block)

	limiter := NewLimiter(1)
	c := newLimitedClient(t, srv, limiter, 1)

	// 先占住唯一的名额。
	go func() { _, _ = c.attempt(context.Background(), http.MethodGet, srv.URL, nil, nil) }()
	time.Sleep(30 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.attempt(ctx, http.MethodGet, srv.URL, nil, nil)
		done <- err
	}()

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("context 已取消，排队中的请求不该成功返回")
		}
	case <-time.After(2 * time.Second):
		t.Error("context 取消后仍卡在信号量上")
	}
}
