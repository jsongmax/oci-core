package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"ocicore/internal/auth"
)

// 危险操作的 TOTP 复核。
//
// 设置里的「危险操作需重新验证 TOTP」以前只存了个开关，没有任何接口读它，
// 窃取了会话的人照样能终止实例、改安全规则。这里把它落到服务端：开关打开时，
// 危险接口要求请求头带一个新鲜的验证码，校验通过后该会话在 dangerElevationTTL
// 内免再次输入——救援模式要连着分离、挂载好几次，每次都等下一个 30 秒窗口
// 不现实。前端的确认框只是体验，这里才是防线。

// dangerCodeHeader 是危险操作附带验证码的请求头。
const dangerCodeHeader = "X-OCI-TOTP-Code"

// dangerElevationTTL 是一次复核的有效期。
const dangerElevationTTL = 5 * time.Minute

// dangerElevations 记录已通过复核的会话。只存在内存里：重启后需要重新输入，
// 这正是想要的；键是令牌指纹，不留原始令牌。
type dangerElevations struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func newDangerElevations() *dangerElevations {
	return &dangerElevations{until: make(map[string]time.Time)}
}

func elevationKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (d *dangerElevations) active(token string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := elevationKey(token)
	exp, ok := d.until[key]
	if ok && time.Now().After(exp) {
		delete(d.until, key)
		return false
	}
	return ok
}

func (d *dangerElevations) grant(token string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	// 顺手清掉过期条目，防止 map 随会话数无限增长。
	for k, exp := range d.until {
		if now.After(exp) {
			delete(d.until, k)
		}
	}
	d.until[elevationKey(token)] = now.Add(dangerElevationTTL)
}

// requireDangerTOTP 在开关打开时校验危险操作的验证码。
//
// 返回 false 时已经写好了响应。全部拒绝都用 403 而不是 401：401 会让前端
// 以为会话失效、跳回登录页，而这里会话本身是好的，只是缺一次复核。
func (s *Server) requireDangerTOTP(w http.ResponseWriter, r *http.Request) bool {
	policy, err := s.st.Settings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return false
	}
	if !policy.RequireTOTPForDanger {
		return true
	}

	user := userFrom(r.Context())
	token := tokenFrom(r.Context())
	if !user.TOTPEnabled {
		writeError(w, http.StatusForbidden, "danger_totp_not_enrolled",
			"已开启「危险操作需重新验证 TOTP」，但当前账户没有绑定两步验证。"+
				"请先绑定，或在设置中关闭该策略。")
		return false
	}
	if s.elevations.active(token) {
		return true
	}

	code := strings.TrimSpace(r.Header.Get(dangerCodeHeader))
	if code == "" {
		writeError(w, http.StatusForbidden, "danger_totp_required", "该操作需要输入两步验证码")
		return false
	}

	// 与登录第二步共用限流：复核同样是在猜 6 位数字。
	ip := s.clientIP(r)
	if allowed, _ := s.logins.Reserve("totp:" + ip); !allowed {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "尝试次数过多，请稍后再试")
		return false
	}
	counter, ok := auth.VerifyTOTPWithCounter(user.TOTPSecret, code, time.Now())
	if !ok {
		writeError(w, http.StatusForbidden, "danger_totp_invalid", "验证码不正确")
		return false
	}
	fresh, err := s.st.ConsumeTOTPCounter(r.Context(), user.ID, counter)
	if err != nil {
		writeStoreError(w, err)
		return false
	}
	if !fresh {
		writeError(w, http.StatusForbidden, "danger_totp_reused",
			"该验证码已被使用，请等待下一个验证码")
		return false
	}

	s.logins.Reset("totp:" + ip)
	s.elevations.grant(token)
	return true
}

// danger 包装一律需要复核的接口。条件性危险的接口（比如只有强制关机才算）
// 在处理器里自己调 requireDangerTOTP。
func (s *Server) danger(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireDangerTOTP(w, r) {
			return
		}
		next(w, r)
	}
}
