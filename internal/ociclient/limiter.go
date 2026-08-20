package ociclient

import "context"

// 并发上限的默认值。
//
// 分两级是因为它们防的是两件事：
//
//   - 每租户 6：Oracle 的限流是按租户算的。同一个租户上堆太多并发，
//     换来的是 429 和随之而来的退避，实际吞吐反而更低。
//   - 全局 16：多账号聚合时，每个租户各占 6 条，十个账号就是六十条
//     并发出站连接。这一级防的不是 Oracle，是本机的文件描述符与出口带宽。
//
// 各个操作内部原有的信号量（同步 6、配额 4、批量 3）解决的是另一个问题：
// 限制单次操作的扇出宽度。两者不冲突，也不互相替代——那些信号量拦不住
// 「同步 + 配额 + 批量同时触发」叠加出来的总量。
const (
	DefaultTenancyConcurrency = 6
	DefaultGlobalConcurrency  = 16
)

// Limiter 限制对 OCI 的出站并发。
//
// 多个 Client 共用同一个 Limiter 才有意义：每账号一个 Client，
// 而全局这一级恰恰要跨账号才拦得住。
type Limiter struct {
	global chan struct{}
}

// NewLimiter 创建一个全局并发上限为 n 的限流器。n <= 0 表示不限。
func NewLimiter(n int) *Limiter {
	if n <= 0 {
		return &Limiter{}
	}
	return &Limiter{global: make(chan struct{}, n)}
}

// acquire 获取一个全局名额。ctx 结束时立刻返回错误，不做无谓的等待。
func (l *Limiter) acquire(ctx context.Context) error {
	if l == nil || l.global == nil {
		return nil
	}
	select {
	case l.global <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *Limiter) release() {
	if l == nil || l.global == nil {
		return
	}
	<-l.global
}

// WithLimiter 让客户端受某个全局限流器约束。
func WithLimiter(l *Limiter) Option {
	return func(c *Client) { c.limiter = l }
}

// WithTenancyConcurrency 设置该客户端（即该租户）的并发上限。n <= 0 表示不限。
func WithTenancyConcurrency(n int) Option {
	return func(c *Client) {
		if n <= 0 {
			c.tenancySlots = nil
			return
		}
		c.tenancySlots = make(chan struct{}, n)
	}
}

// acquireSlots 依次取全局与租户两级名额。
//
// 顺序不能反：先全局后租户。反过来的话，一个租户的请求会先占住租户名额
// 再去排全局的队，而它占着的租户名额本可以让同租户的其他请求先走完。
// 更要紧的是两级顺序不一致时会形成环路等待。
func (c *Client) acquireSlots(ctx context.Context) (func(), error) {
	if err := c.limiter.acquire(ctx); err != nil {
		return nil, err
	}
	if c.tenancySlots == nil {
		return c.limiter.release, nil
	}
	select {
	case c.tenancySlots <- struct{}{}:
		return func() {
			<-c.tenancySlots
			c.limiter.release()
		}, nil
	case <-ctx.Done():
		c.limiter.release()
		return nil, ctx.Err()
	}
}
