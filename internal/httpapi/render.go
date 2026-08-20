package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"ocicore/internal/store"
)

// maxRequestBytes 限制请求体大小。表单里最大的字段是 PEM 私钥，几 KB 足够。
const maxRequestBytes = 256 << 10 // 256 KiB

// errorBody 是统一的错误响应格式。
//
// Code 是稳定的机器可读标识，Message 是给用户看的中文说明。
// 涉及 OCI 调用失败时额外带上 OciCode —— 设计稿要求原始错误码必须
// 原样可见、可复制，用户需要拿它去搜索。
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	OciCode string `json:"ociCode,omitempty"`
	Advice  string `json:"advice,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("写入响应失败", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Code: code, Message: message})
}

// writeStoreError 把持久层错误映射成合适的 HTTP 状态码。
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "记录不存在")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	default:
		slog.Error("数据库操作失败", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
	}
}

// decodeJSON 解析请求体。拒绝未知字段，让拼错的字段名立刻暴露而不是被静默忽略。
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		// 空 body 单独说清楚。io.EOF 的字面意思是"读到结尾"，套进
		// "请求内容格式有误: EOF" 之后完全看不出是"根本没发内容"——
		// 排查时会往 JSON 语法上想，而实际是前端漏传了参数。
		if errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "empty_body",
				"请求没有携带任何内容。这通常是前端漏传了参数，不是格式问题。")
			return false
		}
		writeError(w, http.StatusBadRequest, "bad_request", "请求内容格式有误: "+err.Error())
		return false
	}
	return true
}

// clientIP 提取客户端 IP。
//
// 只有在明确配置了 TrustProxyHeaders 时才采信 X-Forwarded-For ——
// 否则任何人都能伪造该头来绕过登录失败限流。
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxyHeaders {
		// 取最后一段，不是第一段。
		//
		// X-Forwarded-For 是「客户端声称的值, 代理逐跳追加的值」。第一段完全
		// 由客户端控制：攻击者发 `X-Forwarded-For: 1.2.3.4`，nginx 会把真实
		// IP 追加在后面变成 `1.2.3.4, <真实IP>`。取第一段就等于让攻击者自选
		// 限流桶——每次换一个伪造 IP，登录爆破的次数限制就完全失效了，
		// 审计日志记下的也是伪造 IP。
		//
		// 最后一段是紧邻本服务的那一跳写的，也就是我们信任的那个代理填的值。
		// 这在「恰好一层可信代理」下正确；多层代理需要按跳数回溯，
		// 那属于更复杂的部署，本工具不做支持。
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if idx := strings.LastIndexByte(xff, ','); idx >= 0 {
				return strings.TrimSpace(xff[idx+1:])
			}
			return strings.TrimSpace(xff)
		}
	}
	host := r.RemoteAddr
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}
	return strings.Trim(host, "[]")
}

// isSecureRequest 判断连接是否是 HTTPS，用于决定 Cookie 的 Secure 标志。
func (s *Server) isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.cfg.TrustProxyHeaders {
		return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	}
	return false
}
