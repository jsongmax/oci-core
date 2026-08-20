package ociclient

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Class 是 OCI 错误的处理分类。
//
// 这是整个项目最重要的一张表：调用方不该去解析错误字符串，而是按 Class 决策。
// 分类决定三件事——能不能重试、要等多久、要不要把账号标成异常。
type Class int

const (
	ClassUnknown Class = iota
	// ClassOutOfCapacity 目标可用域暂时没有该规格的宿主机。
	// 这是创建实例时最常见的返回，属于"稍后再试"，不是配置错误。
	ClassOutOfCapacity
	// ClassThrottled 触发了 OCI 侧限流（429）。必须长退避，并尊重 Retry-After。
	ClassThrottled
	// ClassQuotaExceeded 配额/服务限额已满。重试没有任何意义，必须停下来让用户处理。
	ClassQuotaExceeded
	// ClassAuthFailed 签名未通过。绝大多数是指纹与私钥不匹配，或服务器时钟偏移超过 5 分钟。
	ClassAuthFailed
	// ClassNotAuthorized OCI 对"无权限"和"不存在"返回同一个错误码，无法区分。
	ClassNotAuthorized
	// ClassConflict 资源当前状态不允许该操作（例如对正在启动的实例再发起启动）。
	ClassConflict
	// ClassBadRequest 请求参数有误。重试无意义。
	ClassBadRequest
	// ClassTransient 网络抖动或 OCI 网关层 5xx。短退避后可重试。
	ClassTransient
)

// classInfo 描述一个分类的重试策略与面向用户的话术。
type classInfo struct {
	name string
	// retryable 表示同样的请求原样重发有可能成功。
	retryable bool
	// minBackoff 是重试前至少要等待的时长。
	minBackoff time.Duration
	// accountFatal 表示该错误说明账号凭据或权限有问题，应把账号标记为异常。
	accountFatal bool
	// advice 是给用户看的下一步建议。UI 上与原始错误码并列展示。
	advice string
}

var classTable = map[Class]classInfo{
	ClassUnknown: {
		name: "Unknown", retryable: false, minBackoff: 0,
		advice: "未识别的错误，请查看下方原始错误码。",
	},
	ClassOutOfCapacity: {
		name: "OutOfCapacity", retryable: true, minBackoff: 30 * time.Second,
		advice: "该可用域暂时没有此规格的容量，可稍后重试或换一个可用域。",
	},
	ClassThrottled: {
		name: "Throttled", retryable: true, minBackoff: 5 * time.Minute,
		advice: "请求过于频繁，已被 Oracle 限流。请降低操作频率后再试。",
	},
	ClassQuotaExceeded: {
		name: "QuotaExceeded", retryable: false, minBackoff: 0,
		advice: "该账号的配额已用尽。请在账号详情的配额页确认限额，或先释放已有资源。",
	},
	ClassAuthFailed: {
		name: "AuthFailed", retryable: false, minBackoff: 0, accountFatal: true,
		advice: "API 密钥校验失败。请检查指纹是否与私钥匹配，以及本机时间是否准确（偏移超过 5 分钟即会被拒绝）。",
	},
	ClassNotAuthorized: {
		name: "NotAuthorized", retryable: false, minBackoff: 0, accountFatal: true,
		advice: "无权限，或资源不存在。请确认该 IAM 用户已被授予对应策略，且 OCID 填写正确。",
	},
	ClassConflict: {
		name: "Conflict", retryable: true, minBackoff: 10 * time.Second,
		advice: "资源当前状态不允许此操作，请等待正在进行的操作完成后重试。",
	},
	ClassBadRequest: {
		name: "BadRequest", retryable: false, minBackoff: 0,
		advice: "请求参数有误，请检查填写的配置。",
	},
	ClassTransient: {
		name: "Transient", retryable: true, minBackoff: 5 * time.Second,
		advice: "网络或 Oracle 服务端临时故障，稍后会自动重试。",
	},
}

func (c Class) String() string            { return classTable[c].name }
func (c Class) Retryable() bool           { return classTable[c].retryable }
func (c Class) AccountFatal() bool        { return classTable[c].accountFatal }
func (c Class) Advice() string            { return classTable[c].advice }
func (c Class) MinBackoff() time.Duration { return classTable[c].minBackoff }

// APIError 是一次失败的 OCI API 调用。原始的 status/code/message 全部保留——
// 用户排障时需要拿它们去搜索，UI 上必须原样可见、可复制。
type APIError struct {
	StatusCode   int
	Code         string
	Message      string
	OpcRequestID string
	Class        Class
	// RetryAfter 来自响应头，仅在 429/503 时有值。为 0 表示服务端未指定。
	RetryAfter time.Duration

	Method string
	URL    string
}

func (e *APIError) Error() string {
	code := e.Code
	if code == "" {
		code = "HTTP" + fmt.Sprint(e.StatusCode)
	}
	return fmt.Sprintf("oci: %s %d %s: %s", code, e.StatusCode, e.Class, e.Message)
}

// Advice 返回面向用户的处理建议。
func (e *APIError) Advice() string { return e.Class.Advice() }

// Backoff 返回本次错误建议的等待时长：优先采用服务端给出的 Retry-After。
func (e *APIError) Backoff() time.Duration {
	if e.RetryAfter > 0 {
		return e.RetryAfter
	}
	return e.Class.MinBackoff()
}

// Classify 把 HTTP 状态码与 OCI 错误码归入一个处理分类。
//
// 判定顺序是刻意的：先看错误码（最精确），再看状态码兜底。
// 500 需要额外看 message，因为 Oracle 把"容量不足"塞进了通用的 InternalError 里。
func Classify(status int, code, message string) Class {
	switch code {
	case "TooManyRequests":
		return ClassThrottled
	case "LimitExceeded", "QuotaExceeded":
		return ClassQuotaExceeded
	case "NotAuthenticated", "InvalidAuthorization", "SignatureDoesNotMatch":
		return ClassAuthFailed
	case "NotAuthorizedOrNotFound", "NotAuthorized", "NotFound":
		return ClassNotAuthorized
	case "IncorrectState", "Conflict", "InvalidatedRetryToken":
		return ClassConflict
	case "InvalidParameter", "MissingParameter", "CannotParseRequest", "UnsupportedMediaType":
		return ClassBadRequest
	case "ServiceUnavailable":
		return ClassTransient
	case "OutOfCapacity", "OutOfHostCapacity":
		return ClassOutOfCapacity
	}

	switch status {
	case http.StatusTooManyRequests: // 429
		return ClassThrottled
	case http.StatusUnauthorized: // 401
		return ClassAuthFailed
	case http.StatusForbidden: // 403
		return ClassNotAuthorized
	case http.StatusNotFound: // 404
		return ClassNotAuthorized
	case http.StatusConflict: // 409
		return ClassConflict
	case http.StatusBadRequest, http.StatusUnprocessableEntity: // 400 422
		return ClassBadRequest
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout: // 502 503 504
		return ClassTransient
	case http.StatusInternalServerError: // 500
		// Oracle 用通用的 500 InternalError 表达"没机器了"，只能靠 message 区分。
		if mentionsCapacity(message) {
			return ClassOutOfCapacity
		}
		return ClassTransient
	}

	if status >= 500 {
		return ClassTransient
	}
	return ClassUnknown
}

// mentionsCapacity 识别容量不足的各种措辞。Oracle 在不同区域和时期用过不止一种。
func mentionsCapacity(message string) bool {
	m := strings.ToLower(message)
	for _, needle := range []string{
		"out of host capacity",
		"out of capacity",
		"insufficient capacity",
		"capacity is not available",
	} {
		if strings.Contains(m, needle) {
			return true
		}
	}
	return false
}

// AsAPIError 从错误链中取出 *APIError。
func AsAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}

// IsRetryable 判断一个错误是否值得原样重试。非 APIError（例如网络层错误）默认可重试。
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if apiErr, ok := AsAPIError(err); ok {
		return apiErr.Class.Retryable()
	}
	// 走到这里通常是 DNS / 连接超时 / TLS 握手失败，都属于瞬时故障。
	return true
}

// IsAccountFatal 判断错误是否说明账号凭据本身有问题，需要把账号标记为异常。
func IsAccountFatal(err error) bool {
	apiErr, ok := AsAPIError(err)
	return ok && apiErr.Class.AccountFatal()
}
