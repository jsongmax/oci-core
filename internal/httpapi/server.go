// Package httpapi 是面板的 HTTP 接口层。
//
// 所有接口都在 /api 下，前端 SPA 构建产物由 cmd/server 挂在根路径。
// 认证使用 HttpOnly Cookie 会话，配合自定义请求头做 CSRF 防护。
package httpapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"ocicore/internal/capacitysvc"
	"ocicore/internal/config"
	"ocicore/internal/huntsvc"
	"ocicore/internal/instancesvc"
	"ocicore/internal/notify"
	"ocicore/internal/ociconn"
	"ocicore/internal/store"
)

const (
	sessionCookie = "oci_session"

	// csrfHeader 是所有写操作必须携带的自定义头。
	//
	// 浏览器不允许跨源请求携带自定义头（除非目标站点显式开启 CORS 预检，
	// 而本服务从不开启），因此"存在该头"就等价于"请求来自本站脚本"。
	// 这比双提交 Cookie 更简单，且没有令牌同步问题。
	csrfHeader = "X-OCI-Tools"
	csrfValue  = "1"
)

// Deps 是构造 Server 所需的依赖。
//
// 用结构体而非位置参数：这一层会持续接入新的 service，
// 每加一个就改一次函数签名会让所有调用点跟着抖动。
type Deps struct {
	Store     *store.Store
	Config    config.Config
	Conns     *ociconn.Factory
	Instances *instancesvc.Service
}

// Server 持有依赖并注册路由。
type Server struct {
	st        *store.Store
	cfg       config.Config
	conns     *ociconn.Factory
	instances *instancesvc.Service
	mux       *http.ServeMux
	logins    *attemptLimiter
	quotas    *quotaCache
	notifier  *notify.Dispatcher
	hunter    *huntsvc.Service
	capacity  *capacitysvc.Service
	started   time.Time
}

// New 构造服务并注册全部路由。
func New(deps Deps) *Server {
	s := &Server{
		st:        deps.Store,
		cfg:       deps.Config,
		conns:     deps.Conns,
		instances: deps.Instances,
		mux:       http.NewServeMux(),
		logins:    newAttemptLimiter(5, 15*time.Minute),
		quotas:    newQuotaCache(),
		notifier:  notify.NewDispatcher(deps.Store),
		started:   time.Now(),
	}
	if s.conns == nil {
		s.conns = ociconn.New(deps.Store)
	}
	// 让后台的生命周期轮询也能推送通知（新实例就绪、账号凭据失效）。
	if s.instances != nil {
		s.instances.SetNotifier(s.notifier)
	}
	// 容量守候的调度器建在这里而不是 main：它要用到 notifier 与实例缓存，
	// 两者都只在 Server 里组装完整。
	s.capacity = capacitysvc.New(capacitysvc.Deps{
		Store: s.st, Conns: s.conns, OnChange: s.onCapacityChange,
	})
	s.hunter = huntsvc.New(huntsvc.Deps{
		Store:    s.st,
		Conns:    s.conns,
		OnLaunch: s.onHuntLaunched,
		OnEvent:  s.onHuntEvent,
	})
	s.routes()
	return s
}

func (s *Server) routes() {
	// 无需认证：首次设置与登录。
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("POST /api/setup", s.guard(s.handleSetup))
	s.mux.HandleFunc("POST /api/auth/login", s.guard(s.handleLogin))

	// 半登录即可访问：仅用于完成第二因子。
	s.mux.HandleFunc("POST /api/auth/totp/verify", s.guard(s.requireSession(s.handleTOTPVerify)))
	s.mux.HandleFunc("POST /api/auth/logout", s.guard(s.requireSession(s.handleLogout)))

	// 需要完整会话。
	s.mux.HandleFunc("GET /api/auth/me", s.requireAuth(s.handleMe))
	s.mux.HandleFunc("POST /api/auth/totp/setup", s.guard(s.requireAuth(s.handleTOTPSetup)))
	s.mux.HandleFunc("POST /api/auth/totp/enable", s.guard(s.requireAuth(s.handleTOTPEnable)))
	s.mux.HandleFunc("POST /api/auth/totp/disable", s.guard(s.requireAuth(s.handleTOTPDisable)))
	s.mux.HandleFunc("POST /api/auth/password", s.guard(s.requireAuth(s.handleChangePassword)))

	s.mux.HandleFunc("GET /api/accounts", s.requireAuth(s.handleListAccounts))
	s.mux.HandleFunc("POST /api/accounts", s.guard(s.requireAuth(s.handleCreateAccount)))
	s.mux.HandleFunc("GET /api/accounts/{id}", s.requireAuth(s.handleGetAccount))
	s.mux.HandleFunc("PATCH /api/accounts/{id}", s.guard(s.requireAuth(s.handleUpdateAccount)))
	s.mux.HandleFunc("DELETE /api/accounts/{id}", s.guard(s.requireAuth(s.handleDeleteAccount)))
	s.mux.HandleFunc("POST /api/accounts/{id}/check", s.guard(s.requireAuth(s.handleCheckAccount)))
	s.mux.HandleFunc("GET /api/accounts/{id}/regions", s.requireAuth(s.handleAccountRegions))

	// 添加账号抽屉的两个辅助接口：粘贴解析与保存前测试。
	s.mux.HandleFunc("POST /api/accounts/parse-config", s.guard(s.requireAuth(s.handleParseConfig)))
	s.mux.HandleFunc("POST /api/accounts/check-draft", s.guard(s.requireAuth(s.handleCheckDraft)))

	s.mux.HandleFunc("GET /api/audit", s.requireAuth(s.handleListAudit))
	s.mux.HandleFunc("GET /api/audit/export", s.requireAuth(s.handleExportAudit))
	s.mux.HandleFunc("GET /api/regions", s.requireAuth(s.handleListRegions))

	// 实例管理。
	s.mux.HandleFunc("GET /api/instances", s.requireAuth(s.handleListInstances))
	s.mux.HandleFunc("GET /api/instances/{id}", s.requireAuth(s.handleGetInstance))
	s.mux.HandleFunc("POST /api/instances/sync", s.guard(s.requireAuth(s.handleSyncInstances)))
	s.mux.HandleFunc("POST /api/instances/{id}/actions/{action}", s.guard(s.requireAuth(s.handleInstanceAction)))
	s.mux.HandleFunc("PATCH /api/instances/{id}", s.guard(s.requireAuth(s.handleRenameInstance)))
	s.mux.HandleFunc("PATCH /api/instances/{id}/note", s.guard(s.requireAuth(s.handleSetInstanceNote)))
	s.mux.HandleFunc("POST /api/instances/{id}/reshape", s.guard(s.requireAuth(s.handleReshapeInstance)))
	s.mux.HandleFunc("DELETE /api/instances/{id}", s.guard(s.requireAuth(s.handleTerminateInstance)))
	s.mux.HandleFunc("POST /api/instances/{id}/dismiss-error", s.guard(s.requireAuth(s.handleDismissInstanceError)))

	s.mux.HandleFunc("POST /api/instances/{id}/console", s.guard(s.requireAuth(s.handleCreateConsole)))
	s.mux.HandleFunc("POST /api/instances/bulk", s.guard(s.requireAuth(s.handleBulkAction)))
	s.mux.HandleFunc("POST /api/auth/sessions/revoke-all", s.guard(s.requireAuth(s.handleRevokeSessions)))

	// 实时事件流。SSE 单向推送足够，不需要 WebSocket 的双向能力。
	s.mux.HandleFunc("GET /api/events", s.requireAuth(s.handleEvents))

	// 网络。
	s.mux.HandleFunc("GET /api/network/vcns", s.requireAuth(s.handleListVcns))
	s.mux.HandleFunc("GET /api/network/subnets", s.requireAuth(s.handleListSubnets))
	s.mux.HandleFunc("GET /api/network/security-lists", s.requireAuth(s.handleListSecurityLists))
	s.mux.HandleFunc("PUT /api/network/security-lists/{id}", s.guard(s.requireAuth(s.handleUpdateSecurityList)))
	s.mux.HandleFunc("GET /api/network/rule-templates", s.requireAuth(s.handleRuleTemplates))
	s.mux.HandleFunc("GET /api/network/public-ips", s.requireAuth(s.handleListPublicIPs))
	s.mux.HandleFunc("POST /api/network/ensure", s.guard(s.requireAuth(s.handleEnsureNetwork)))
	s.mux.HandleFunc("POST /api/instances/{id}/change-ip", s.guard(s.requireAuth(s.handleChangePublicIP)))
	s.mux.HandleFunc("POST /api/instances/{id}/enable-ipv6", s.guard(s.requireAuth(s.handleEnableIPv6)))

	// 存储。
	s.mux.HandleFunc("GET /api/storage/boot-volumes", s.requireAuth(s.handleListBootVolumes))
	s.mux.HandleFunc("PATCH /api/storage/boot-volumes/{id}", s.guard(s.requireAuth(s.handleUpdateBootVolume)))
	s.mux.HandleFunc("GET /api/storage/volumes", s.requireAuth(s.handleListVolumes))
	s.mux.HandleFunc("PATCH /api/storage/volumes/{id}", s.guard(s.requireAuth(s.handleUpdateVolume)))
	s.mux.HandleFunc("POST /api/storage/boot-volume-attachments/detach", s.guard(s.requireAuth(s.handleDetachBootVolume)))
	s.mux.HandleFunc("POST /api/storage/boot-volume-attachments/attach", s.guard(s.requireAuth(s.handleAttachBootVolume)))
	// 数据盘挂载。救援模式要靠它把坏机器的引导卷挂到好机器上当普通盘改文件——
	// 上面那对 boot-volume-attachments 只能让卷当引导卷，改不了里面的东西。
	s.mux.HandleFunc("POST /api/storage/volume-attachments/attach", s.guard(s.requireAuth(s.handleAttachVolume)))
	s.mux.HandleFunc("POST /api/storage/volume-attachments/detach", s.guard(s.requireAuth(s.handleDetachVolume)))

	// 创建实例。
	s.mux.HandleFunc("GET /api/launch/shapes", s.requireAuth(s.handleListShapes))
	s.mux.HandleFunc("GET /api/launch/images", s.requireAuth(s.handleListImages))
	s.mux.HandleFunc("GET /api/launch/availability-domains", s.requireAuth(s.handleListADs))
	s.mux.HandleFunc("GET /api/launch/presets", s.requireAuth(s.handleLaunchPresets))
	s.mux.HandleFunc("POST /api/launch", s.guard(s.requireAuth(s.handleLaunchInstance)))

	// 容量守候（抢机）。创建走 guard：它会持续调用 Oracle，
	// 属于需要二次确认的操作，和普通只读接口不是一个量级。
	s.mux.HandleFunc("GET /api/hunt", s.requireAuth(s.handleListHuntTasks))
	s.mux.HandleFunc("POST /api/hunt", s.guard(s.requireAuth(s.handleCreateHuntTask)))
	s.mux.HandleFunc("POST /api/hunt/{id}/{action}", s.guard(s.requireAuth(s.handleSetHuntState)))
	s.mux.HandleFunc("DELETE /api/hunt/{id}", s.guard(s.requireAuth(s.handleDeleteHuntTask)))

	// 容量监控。查询本身是只读的（Oracle 的容量报告接口，不创建任何资源），
	// 所以 probe 不走 guard；增删监控项会影响后台轮询，走 guard。
	s.mux.HandleFunc("GET /api/capacity", s.requireAuth(s.handleListCapacityWatches))
	s.mux.HandleFunc("POST /api/capacity/probe", s.requireAuth(s.handleProbeCapacity))
	s.mux.HandleFunc("POST /api/capacity", s.guard(s.requireAuth(s.handleCreateCapacityWatch)))
	s.mux.HandleFunc("POST /api/capacity/{id}/{action}", s.guard(s.requireAuth(s.handleSetCapacityWatchEnabled)))
	s.mux.HandleFunc("DELETE /api/capacity/{id}", s.guard(s.requireAuth(s.handleDeleteCapacityWatch)))

	// 配额与监控。
	s.mux.HandleFunc("GET /api/quota", s.requireAuth(s.handleQuota))
	s.mux.HandleFunc("GET /api/instances/{id}/metrics", s.requireAuth(s.handleInstanceMetrics))

	// 通知与设置。
	s.mux.HandleFunc("GET /api/notifications/channels", s.requireAuth(s.handleListChannels))
	s.mux.HandleFunc("POST /api/notifications/channels", s.guard(s.requireAuth(s.handleCreateChannel)))
	s.mux.HandleFunc("PATCH /api/notifications/channels/{id}", s.guard(s.requireAuth(s.handleUpdateChannel)))
	s.mux.HandleFunc("DELETE /api/notifications/channels/{id}", s.guard(s.requireAuth(s.handleDeleteChannel)))
	s.mux.HandleFunc("POST /api/notifications/channels/{id}/test", s.guard(s.requireAuth(s.handleTestChannel)))
	s.mux.HandleFunc("GET /api/notifications/events", s.requireAuth(s.handleNotificationEvents))
	s.mux.HandleFunc("GET /api/settings", s.requireAuth(s.handleGetSettings))
	s.mux.HandleFunc("PATCH /api/settings", s.guard(s.requireAuth(s.handleUpdateSettings)))

	// 总览页数据。
	s.mux.HandleFunc("GET /api/overview", s.requireAuth(s.handleOverview))
}

// Handler 返回带全局中间件的处理器。
func (s *Server) Handler() http.Handler {
	return securityHeaders(s.mux)
}

// ServeHTTP 让 Server 本身可以作为 http.Handler 使用。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Handler().ServeHTTP(w, r)
}

// securityHeaders 设置基础安全响应头。
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// 面板是纯本地资源的 SPA，不需要加载任何外部资源。
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// guard 是 CSRF 防护：所有写操作必须带上自定义头。
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(csrfHeader) != csrfValue {
			writeError(w, http.StatusForbidden, "csrf",
				"缺少 "+csrfHeader+" 请求头。跨站请求已被拒绝。")
			return
		}
		next(w, r)
	}
}

// ---- 会话上下文 ----

type ctxKey int

const (
	ctxKeySession ctxKey = iota
	ctxKeyUser
	ctxKeyToken
)

func sessionFrom(ctx context.Context) *store.Session {
	v, _ := ctx.Value(ctxKeySession).(*store.Session)
	return v
}

func userFrom(ctx context.Context) *store.User {
	v, _ := ctx.Value(ctxKeyUser).(*store.User)
	return v
}

func tokenFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyToken).(string)
	return v
}

// loadSession 从 Cookie 还原会话与用户。
func (s *Server) loadSession(r *http.Request) (*store.Session, *store.User, string, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return nil, nil, "", false
	}
	sess, err := s.st.GetSession(r.Context(), cookie.Value)
	if err != nil {
		return nil, nil, "", false
	}
	user, err := s.st.GetUser(r.Context(), sess.UserID)
	if err != nil {
		return nil, nil, "", false
	}
	return sess, user, cookie.Value, true
}

// requireSession 只要求会话存在，不要求已完成第二因子。
// 仅用于 TOTP 校验与登出这两个必须在"半登录"状态下可用的接口。
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, user, token, ok := s.loadSession(r)
		if !ok {
			s.clearSessionCookie(w, r)
			writeError(w, http.StatusUnauthorized, "unauthenticated", "请先登录")
			return
		}
		next(w, r.WithContext(withSession(r.Context(), sess, user, token)))
	}
}

// requireAuth 要求完整会话：已通过口令，且在启用了 TOTP 时也已通过第二因子。
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, user, token, ok := s.loadSession(r)
		if !ok {
			s.clearSessionCookie(w, r)
			writeError(w, http.StatusUnauthorized, "unauthenticated", "请先登录")
			return
		}
		if user.TOTPEnabled && !sess.TOTPVerified {
			writeError(w, http.StatusUnauthorized, "totp_required", "请完成两步验证")
			return
		}

		// 滑动续期：活跃用户不会在操作中途被踢下线。
		if err := s.st.TouchSession(r.Context(), token, s.cfg.SessionTTL); err != nil {
			// 续期失败不影响本次请求，交给下一次重试。
			_ = err
		}
		next(w, r.WithContext(withSession(r.Context(), sess, user, token)))
	}
}

func withSession(ctx context.Context, sess *store.Session, user *store.User, token string) context.Context {
	ctx = context.WithValue(ctx, ctxKeySession, sess)
	ctx = context.WithValue(ctx, ctxKeyUser, user)
	return context.WithValue(ctx, ctxKeyToken, token)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// ---- 登录失败限流 ----

// attemptLimiter 是按来源 IP 的固定窗口计数器。
//
// 单机部署、单用户场景下，固定窗口足够挡住暴力破解，
// 不值得为此引入更复杂的滑动窗口或外部存储。
type attemptLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	buckets map[string]*bucket
}

type bucket struct {
	count     int
	resetAt   time.Time
	lastTouch time.Time
}

func newAttemptLimiter(max int, window time.Duration) *attemptLimiter {
	return &attemptLimiter{max: max, window: window, buckets: make(map[string]*bucket)}
}

// Allow 报告该 key 是否还能再尝试一次，并返回剩余次数。
func (l *attemptLimiter) Allow(key string) (allowed bool, remaining int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	l.pruneLocked(now)

	b, ok := l.buckets[key]
	if !ok || now.After(b.resetAt) {
		b = &bucket{resetAt: now.Add(l.window)}
		l.buckets[key] = b
	}
	b.lastTouch = now

	if b.count >= l.max {
		return false, 0
	}
	return true, l.max - b.count
}

// Fail 记录一次失败尝试。
func (l *attemptLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[key]
	if !ok || now.After(b.resetAt) {
		b = &bucket{resetAt: now.Add(l.window)}
		l.buckets[key] = b
	}
	b.count++
	b.lastTouch = now
}

// Reset 在登录成功后清除计数。
func (l *attemptLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

// pruneLocked 清理长期不活跃的条目，防止 map 无限增长。
func (l *attemptLimiter) pruneLocked(now time.Time) {
	if len(l.buckets) < 128 {
		return
	}
	for key, b := range l.buckets {
		if now.Sub(b.lastTouch) > 2*l.window {
			delete(l.buckets, key)
		}
	}
}
