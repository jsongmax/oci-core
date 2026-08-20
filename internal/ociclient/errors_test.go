package ociclient

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		code    string
		message string
		want    Class
	}{
		// 容量不足被 Oracle 塞进了通用的 500 InternalError，只能靠 message 认出来。
		{"容量不足", 500, "InternalError", "Out of host capacity.", ClassOutOfCapacity},
		{"容量不足-变体", 500, "InternalError", "Out of capacity for shape VM.Standard.A1.Flex", ClassOutOfCapacity},
		{"容量不足-显式码", 500, "OutOfCapacity", "", ClassOutOfCapacity},
		{"普通 500", 500, "InternalError", "Internal error occurred", ClassTransient},

		{"限流", 429, "TooManyRequests", "", ClassThrottled},
		{"限流-仅状态码", 429, "", "", ClassThrottled},

		// 配额满了重试没有意义，必须与"稍后再试"区分开。
		{"配额已满", 400, "LimitExceeded", "", ClassQuotaExceeded},
		{"配额已满-别名", 400, "QuotaExceeded", "", ClassQuotaExceeded},

		{"认证失败", 401, "NotAuthenticated", "", ClassAuthFailed},
		{"签名不匹配", 401, "SignatureDoesNotMatch", "", ClassAuthFailed},
		{"无权限或不存在", 404, "NotAuthorizedOrNotFound", "", ClassNotAuthorized},
		{"禁止访问", 403, "", "", ClassNotAuthorized},

		{"状态冲突", 409, "IncorrectState", "", ClassConflict},
		{"参数错误", 400, "InvalidParameter", "", ClassBadRequest},
		{"网关错误", 502, "", "", ClassTransient},
		{"服务不可用", 503, "ServiceUnavailable", "", ClassTransient},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.status, tc.code, tc.message); got != tc.want {
				t.Errorf("Classify(%d, %q, %q) = %v，期望 %v",
					tc.status, tc.code, tc.message, got, tc.want)
			}
		})
	}
}

// 重试策略是整个项目的核心约定，用表格锁死，防止日后被无意改动。
func TestRetryPolicy(t *testing.T) {
	cases := []struct {
		class        Class
		retryable    bool
		accountFatal bool
	}{
		{ClassOutOfCapacity, true, false},
		{ClassThrottled, true, false},
		{ClassTransient, true, false},
		{ClassConflict, true, false},

		{ClassQuotaExceeded, false, false},
		{ClassBadRequest, false, false},
		{ClassAuthFailed, false, true},
		{ClassNotAuthorized, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.class.String(), func(t *testing.T) {
			if got := tc.class.Retryable(); got != tc.retryable {
				t.Errorf("Retryable() = %v，期望 %v", got, tc.retryable)
			}
			if got := tc.class.AccountFatal(); got != tc.accountFatal {
				t.Errorf("AccountFatal() = %v，期望 %v", got, tc.accountFatal)
			}
			if tc.class.Advice() == "" {
				t.Error("每个分类都必须有面向用户的处理建议")
			}
		})
	}
}

// 限流时必须优先采用服务端给出的 Retry-After，而不是自己的默认退避。
func TestBackoffPrefersRetryAfter(t *testing.T) {
	err := &APIError{Class: ClassThrottled, RetryAfter: 90 * time.Second}
	if got := err.Backoff(); got != 90*time.Second {
		t.Errorf("Backoff() = %v，期望采用 Retry-After 的 90s", got)
	}

	err = &APIError{Class: ClassThrottled}
	if got := err.Backoff(); got != ClassThrottled.MinBackoff() {
		t.Errorf("无 Retry-After 时应退回默认退避，实际 %v", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("120"); got != 120*time.Second {
		t.Errorf("秒数格式解析错误: %v", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("空值应返回 0，实际 %v", got)
	}
	if got := parseRetryAfter("garbage"); got != 0 {
		t.Errorf("非法值应返回 0，实际 %v", got)
	}
	future := time.Now().Add(60 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got <= 0 {
		t.Errorf("HTTP 日期格式应解析出正数时长，实际 %v", got)
	}
}

func TestIsRetryableAndAccountFatal(t *testing.T) {
	authErr := &APIError{Class: ClassAuthFailed}
	if IsRetryable(authErr) {
		t.Error("认证失败不应重试")
	}
	if !IsAccountFatal(authErr) {
		t.Error("认证失败应标记账号异常")
	}

	// 网络层错误没有 APIError 包装，默认按可重试处理。
	if !IsRetryable(errors.New("dial tcp: i/o timeout")) {
		t.Error("未分类的错误应默认可重试")
	}
	if IsRetryable(nil) {
		t.Error("nil 不应被判为可重试")
	}
	if IsAccountFatal(errors.New("boom")) {
		t.Error("普通错误不应标记账号异常")
	}
}

func TestAsAPIErrorUnwrapsChain(t *testing.T) {
	inner := &APIError{Class: ClassThrottled, Code: "TooManyRequests"}
	wrapped := errors.Join(errors.New("上下文"), inner)

	got, ok := AsAPIError(wrapped)
	if !ok {
		t.Fatal("应能从错误链中取出 APIError")
	}
	if got.Code != "TooManyRequests" {
		t.Errorf("取出的错误码 = %q", got.Code)
	}
}
