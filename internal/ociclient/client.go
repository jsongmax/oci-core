package ociclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxResponseBytes 限制单次响应的读取上限，避免异常响应耗尽内存。
const maxResponseBytes = 8 << 20 // 8 MiB

// UserAgent 使用真实的产品标识。不伪装成官方 SDK——伪造 UA 既无益于兼容性，
// 也会让异常流量更难被 Oracle 正确归因。
const UserAgent = "OCICore/0.1 (+https://github.com/ocicore)"

// Client 是一个绑定到单个 OCI 账号（租户 + 用户 + 密钥）的 API 客户端。
// 可安全地被多 goroutine 共用。
type Client struct {
	creds  *Credentials
	signer *Signer
	http   *http.Client

	// maxRetries 是瞬时错误的自动重试次数。认证、权限、配额类错误从不重试。
	maxRetries int

	// limiter 是跨账号共享的全局并发闸；tenancySlots 是本租户自己的闸。
	// 两者都可为 nil，表示不限——测试里用得上。
	limiter      *Limiter
	tenancySlots chan struct{}
}

// defaultTransport 是带短拨号超时的传输层。
//
// 默认的 30 秒总超时挡不住"域名能解析但 TCP 连不上"这种情况——OCI 的
// 泛解析会让写错的服务域名照样解析出 IP，请求随后一路挂到总超时。
// 把拨号阶段单独限到 8 秒，配错域名会很快报错而不是让界面卡半分钟。
func defaultTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{
		Timeout:   8 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	t.TLSHandshakeTimeout = 8 * time.Second
	return t
}

// Option 用于定制 Client。
type Option func(*Client)

// WithHTTPClient 替换底层的 http.Client，用于设置代理或自定义超时。
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.http = hc }
}

// WithMaxRetries 设置瞬时错误的重试次数。
func WithMaxRetries(n int) Option {
	return func(c *Client) { c.maxRetries = n }
}

// New 创建一个客户端。凭据会先经过离线校验，尽早暴露配置错误。
func New(creds *Credentials, opts ...Option) (*Client, error) {
	if err := creds.Validate(); err != nil {
		return nil, err
	}
	c := &Client{
		creds:      creds,
		signer:     NewSigner(creds),
		http:       &http.Client{Timeout: 30 * time.Second, Transport: defaultTransport()},
		maxRetries: 2,
		// 默认就带上租户级限流。忘了配全局限流器最多是少一层保护，
		// 而默认不限流会让"同步 + 配额 + 批量同时触发"直接打到 Oracle。
		tenancySlots: make(chan struct{}, DefaultTenancyConcurrency),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Region 返回该客户端默认使用的区域。
func (c *Client) Region() string { return c.creds.Region }

// TenancyOCID 返回租户 OCID。
func (c *Client) TenancyOCID() string { return c.creds.TenancyOCID }

// UserOCID 返回 IAM 用户 OCID。
func (c *Client) UserOCID() string { return c.creds.UserOCID }

// Request 描述一次 OCI API 调用。
type Request struct {
	Method  string
	Service Service
	// Path 是版本段之后的路径，必须以 / 开头，例如 "/users/ocid1.user..."。
	Path string
	// Region 覆盖客户端默认区域。留空则用默认值。
	Region string
	Query  url.Values
	// Body 会被序列化为 JSON。为 nil 表示无请求体。
	Body any
}

// Response 携带调用方偶尔需要的响应元数据（分页游标、请求 ID）。
type Response struct {
	StatusCode   int
	OpcRequestID string
	// NextPage 是分页游标，为空表示已到最后一页。
	NextPage string
}

// Do 执行一次 API 调用，并把 JSON 响应体解码到 out（out 为 nil 时丢弃响应体）。
func (c *Client) Do(ctx context.Context, req Request, out any) (*Response, error) {
	var payload []byte
	if req.Body != nil {
		var err error
		payload, err = json.Marshal(req.Body)
		if err != nil {
			return nil, fmt.Errorf("ociclient: 序列化请求体失败: %w", err)
		}
	}

	region := req.Region
	if region == "" {
		region = c.creds.Region
	}

	target := Endpoint(req.Service, region) + "/" + req.Service.Version + req.Path
	if len(req.Query) > 0 {
		target += "?" + req.Query.Encode()
	}

	for attempt := 0; ; attempt++ {
		resp, err := c.attempt(ctx, req.Method, target, payload, out)
		if err == nil {
			return resp, nil
		}
		// 认证、权限、配额类错误重试多少次都是一样的结果，立刻返回。
		if attempt >= c.maxRetries || !IsRetryable(err) {
			return nil, err
		}
		// 退避期间必须尊重 context：用户关掉页面时应立刻停止。
		wait := backoffFor(err, attempt)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// attempt 执行单次请求。每次尝试都重新构造并签名——Date 头必须是新鲜的，
// 陈旧超过 5 分钟的签名会被 OCI 直接拒绝。
func (c *Client) attempt(ctx context.Context, method, target string, payload []byte, out any) (*Response, error) {
	var bodyReader io.Reader
	if payload != nil {
		bodyReader = bytes.NewReader(payload)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, target, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("ociclient: 构造请求失败: %w", err)
	}
	httpReq.Header.Set("User-Agent", UserAgent)
	httpReq.Header.Set("Accept", "application/json")

	if err := c.signer.Sign(httpReq, payload); err != nil {
		return nil, err
	}

	release, err := c.acquireSlots(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		// 网络层失败没有 HTTP 状态码，归为瞬时错误交给重试逻辑。
		return nil, &APIError{
			StatusCode: 0,
			Code:       "NetworkError",
			Message:    err.Error(),
			Class:      ClassTransient,
			Method:     method,
			URL:        target,
		}
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes))
	if err != nil {
		return nil, &APIError{
			StatusCode: httpResp.StatusCode,
			Code:       "ReadBodyFailed",
			Message:    err.Error(),
			Class:      ClassTransient,
			Method:     method,
			URL:        target,
		}
	}

	resp := &Response{
		StatusCode:   httpResp.StatusCode,
		OpcRequestID: httpResp.Header.Get("opc-request-id"),
		NextPage:     httpResp.Header.Get("opc-next-page"),
	}

	if httpResp.StatusCode >= 400 {
		return nil, newAPIError(httpResp, body, method, target)
	}

	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return nil, fmt.Errorf("ociclient: 解析响应失败 (%s): %w", target, err)
		}
	}
	return resp, nil
}

// newAPIError 把错误响应解析成 APIError。OCI 的错误体格式是 {"code":…,"message":…}，
// 但网关层的错误未必遵守，因此解析失败时保留原始文本。
func newAPIError(httpResp *http.Response, body []byte, method, target string) *APIError {
	var parsed struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &parsed)

	message := parsed.Message
	if message == "" {
		message = strings.TrimSpace(string(body))
		if message == "" {
			message = httpResp.Status
		}
	}

	return &APIError{
		StatusCode:   httpResp.StatusCode,
		Code:         parsed.Code,
		Message:      message,
		OpcRequestID: httpResp.Header.Get("opc-request-id"),
		Class:        Classify(httpResp.StatusCode, parsed.Code, message),
		RetryAfter:   parseRetryAfter(httpResp.Header.Get("Retry-After")),
		Method:       method,
		URL:          target,
	}
}

// parseRetryAfter 解析 Retry-After 头，支持秒数与 HTTP 日期两种格式。
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// backoffFor 计算第 attempt 次重试前的等待时长：以分类的最小退避为基线，逐次翻倍。
//
// 抖动留到 P3 的任务引擎里做——那里有多账号并发，需要打散唤醒时刻；
// P0 的调用都是用户点击触发的单次请求，不存在惊群。
func backoffFor(err error, attempt int) time.Duration {
	base := 2 * time.Second
	if apiErr, ok := AsAPIError(err); ok {
		if b := apiErr.Backoff(); b > 0 {
			base = b
		}
	}
	wait := base << attempt
	if max := 2 * time.Minute; wait > max {
		wait = max
	}
	return wait
}

// listPages 反复调用同一个接口直到分页结束，把每页结果追加进 collect。
//
// limit 是安全上限：OCI 的游标出错时可能永远返回 next page，不设上限会无限循环。
func listPages(ctx context.Context, c *Client, req Request, limit int, collect func(page []byte) error) error {
	if req.Query == nil {
		req.Query = url.Values{}
	}
	for i := 0; i < limit; i++ {
		var raw json.RawMessage
		resp, err := c.Do(ctx, req, &raw)
		if err != nil {
			return err
		}
		if err := collect(raw); err != nil {
			return err
		}
		if resp.NextPage == "" {
			return nil
		}
		req.Query.Set("page", resp.NextPage)
	}
	return fmt.Errorf("ociclient: 分页超过 %d 页上限，已中止 (%s)", limit, req.Path)
}
