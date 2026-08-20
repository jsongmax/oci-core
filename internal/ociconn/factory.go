// Package ociconn 按账号构建 OCI 客户端。
//
// 独立成包是为了让 httpapi 与各个 service 共用同一条建连路径——
// 私钥解密只发生在这里一处，便于审计。
package ociconn

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

// Factory 按账号 ID 产出 OCI 客户端，并缓存已建好的客户端。
//
// 缓存的意义不在于省内存，而在于避免每次请求都做一次 RSA 私钥解析——
// 那是毫秒级开销，跨账号聚合时会被放大几十倍。
type Factory struct {
	st *store.Store

	mu      sync.RWMutex
	cache   map[string]*cacheEntry
	timeout time.Duration

	// limiter 由所有账号的客户端共享，是唯一能拦住跨账号并发总量的地方。
	// 各操作内部的信号量只管自己那次操作的扇出，管不住彼此叠加。
	limiter *ociclient.Limiter
}

type cacheEntry struct {
	client *ociclient.Client
	// updatedAt 取自账号行，账号被改过（例如轮换密钥）就作废重建。
	updatedAt time.Time
}

// New 创建工厂。
func New(st *store.Store) *Factory {
	return &Factory{
		st:      st,
		cache:   make(map[string]*cacheEntry),
		timeout: 30 * time.Second,
		limiter: ociclient.NewLimiter(ociclient.DefaultGlobalConcurrency),
	}
}

// For 返回某账号的客户端。账号自上次建连后被修改过就会重建。
func (f *Factory) For(ctx context.Context, acc *store.Account) (*ociclient.Client, error) {
	f.mu.RLock()
	entry, ok := f.cache[acc.ID]
	f.mu.RUnlock()
	if ok && entry.updatedAt.Equal(acc.UpdatedAt) {
		return entry.client, nil
	}

	client, err := f.build(ctx, acc)
	if err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.cache[acc.ID] = &cacheEntry{client: client, updatedAt: acc.UpdatedAt}
	f.mu.Unlock()
	return client, nil
}

// ForID 按账号 ID 取客户端。
func (f *Factory) ForID(ctx context.Context, accountID string) (*ociclient.Client, *store.Account, error) {
	acc, err := f.st.GetAccount(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	client, err := f.For(ctx, acc)
	if err != nil {
		return nil, acc, err
	}
	return client, acc, nil
}

// Invalidate 丢弃某账号的缓存客户端。账号被删除或密钥轮换后应调用。
func (f *Factory) Invalidate(accountID string) {
	f.mu.Lock()
	delete(f.cache, accountID)
	f.mu.Unlock()
}

func (f *Factory) build(ctx context.Context, acc *store.Account) (*ociclient.Client, error) {
	// 这是全项目唯一的私钥解密入口。返回的凭据只喂给 ociclient，
	// 绝不进入日志、错误信息或 HTTP 响应。
	creds, err := f.st.Credentials(ctx, acc.ID)
	if err != nil {
		return nil, err
	}

	opts := []ociclient.Option{ociclient.WithLimiter(f.limiter)}
	if acc.ProxyURL != "" {
		u, err := url.Parse(acc.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("账号 %s 的代理地址无效: %w", acc.Alias, err)
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = http.ProxyURL(u)
		opts = append(opts, ociclient.WithHTTPClient(&http.Client{
			Transport: transport,
			Timeout:   f.timeout,
		}))
	}
	return ociclient.New(creds, opts...)
}
