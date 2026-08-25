package proxypool

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ocicore/internal/ociclient"
)

// 检测结果状态。
const (
	StatusOK      = "ok"
	StatusFail    = "fail"
	StatusUnknown = "unknown"
)

// DefaultCheckTimeout 是单次检测的上限。
//
// 取 10 秒：代理本身就是最容易不通的一环，等太久没有意义——
// 一条要 10 秒才握上手的代理，拿来跑 OCI 调用也是灾难。
const DefaultCheckTimeout = 10 * time.Second

// CheckResult 是一次存活检测的结果。
type CheckResult struct {
	Status    string    `json:"status"`
	LatencyMs int64     `json:"latencyMs"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
	// Region 是这次检测实际打的区域，排障时要看——
	// 同一条代理连东京和连阿什本的延迟能差好几倍。
	Region string `json:"region"`
}

// Checker 执行存活检测。
type Checker struct {
	Timeout time.Duration
}

// NewChecker 构造检测器。
func NewChecker(timeout time.Duration) *Checker {
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	return &Checker{Timeout: timeout}
}

// Check 通过代理访问 OCI 的端点，确认这条路真的能走通。
//
// 为什么打 OCI 而不是 ipify 之类的回显服务：
//
//   - 测的是**真正要走的那条路**。有些代理能上网但到不了 OCI，
//     打第三方全绿、真用起来全废。
//   - 不把代理列表送给任何第三方。这个面板的原则是数据留在本地、
//     不向第三方上报，检测功能没有理由破这个例。
//   - 不需要凭据、不消耗配额、不产生费用——这是个未认证请求，
//     Oracle 返回 401/404 都算通，我们要的只是"HTTP 响应回来了"。
//
// region 决定打哪个端点，应当传该代理所绑账号的 home region：
// 一条美国代理连东京 OCI 和连阿什本 OCI 的延迟差好几倍，
// 固定测一个端点给出的数字是误导。
func (c *Checker) Check(ctx context.Context, proxyURL, region string) CheckResult {
	res := CheckResult{Status: StatusFail, CheckedAt: time.Now(), Region: region}

	u, err := url.Parse(proxyURL)
	if err != nil {
		res.Error = "代理地址无效: " + err.Error()
		return res
	}

	target := ociclient.Endpoint(ociclient.ServiceCore, region)
	if target == "" || strings.Contains(target, "..") {
		res.Error = "区域无效: " + region
		return res
	}

	transport := &http.Transport{
		Proxy: http.ProxyURL(u),
		DialContext: (&net.Dialer{
			Timeout:   c.Timeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: c.Timeout,
		// 检测是一次性的，别在连接池里留下东西。
		DisableKeepAlives: true,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   c.Timeout,
		// 不跟随跳转：我们只关心第一个响应回来没有。
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("User-Agent", ociclient.UserAgent)

	start := time.Now()
	resp, err := client.Do(req)
	res.LatencyMs = time.Since(start).Milliseconds()

	if err != nil {
		res.Error = describeErr(err)
		return res
	}
	defer resp.Body.Close()

	// 任何 HTTP 状态码都算通。这是个未认证请求，Oracle 回 401 或 404
	// 恰恰证明请求到达了它——那正是我们要确认的事。
	res.Status = StatusOK
	return res
}

// describeErr 把网络错误翻成能直接显示的一句话。
//
// 原始错误长这样：
//
//	Head "https://iaas.ap-tokyo-1.oraclecloud.com": proxyconnect tcp:
//	dial tcp 1.2.3.4:8080: i/o timeout
//
// 直接贴进界面没人看得懂，而这几种失败的处理方式完全不同：
// 代理本身连不上要换代理，认证失败要改密码，到不了 OCI 说明这条代理
// 出口被墙——不区分开，用户只能挨个试。
func describeErr(err error) string {
	msg := err.Error()
	// 小写化再比对：http.Client 的超时说的是 "Client.Timeout"（大写 T），
	// 而拨号超时说的是 "i/o timeout"（小写）。只认一种会漏掉另一种，
	// 于是那条错误一路掉进"原样保留"分支，用户看到一串英文。
	low := strings.ToLower(msg)

	switch {
	case errors.Is(err, context.DeadlineExceeded),
		strings.Contains(low, "timeout"),
		strings.Contains(low, "deadline exceeded"):
		if strings.Contains(low, "proxyconnect") {
			return "连接代理超时——代理地址或端口不对，或者代理已经挂了"
		}
		return "通过代理访问 OCI 超时——代理能连上，但到不了 Oracle"
	case strings.Contains(low, "proxy authentication required"), strings.Contains(msg, "407"):
		return "代理认证失败——用户名或密码不对"
	// "connection refused" 是 Unix 的措辞，Windows 说的是 "actively refused"。
	// 部署目标虽是 Linux，但不少人在 Windows 上本地跑，两种都认。
	case strings.Contains(low, "connection refused"),
		strings.Contains(low, "actively refused"):
		return "代理拒绝连接——端口不对，或者代理服务没在跑"
	case strings.Contains(low, "no such host"):
		return "代理主机名解析不了——地址拼错了，或者域名已失效"
	// 连上了却立刻断开，几乎总是协议对不上：最常见的是把 socks5 端口
	// 配成了 http://，或者反过来。这条如果不点破，用户会以为代理挂了，
	// 去找代理商而不是检查那一行开头写的是什么。
	case strings.Contains(low, "unexpected eof"),
		strings.Contains(low, "malformed http response"):
		return "连上了但握手失败——多半是协议写错了（把 socks5 端口配成 http://，或反过来）"
	case strings.Contains(low, "socks"):
		return "SOCKS 握手失败: " + msg
	}
	// 认不出来就保留原文。宁可长一点，也不要把排障线索吃掉。
	return msg
}

// DuplicateOf 在已有绑定里找出与 proxyID 冲突的账号。
//
// 共用出口比不用代理更糟：两个账号配同一条代理，等于主动把它们绑在
// 一个 IP 上，凭空制造一个本来不存在的关联信号——而这正是这个功能
// 想避免的事。所以重复绑定是硬性拒绝，不是提示。
//
// bindings 是 accountID -> proxyID 的现有映射。
func DuplicateOf(bindings map[string]string, proxyID, exceptAccountID string) []string {
	if proxyID == "" {
		return nil
	}
	var hit []string
	for accID, pid := range bindings {
		if pid == proxyID && accID != exceptAccountID {
			hit = append(hit, accID)
		}
	}
	return hit
}

// ErrDuplicateBinding 表示这条代理已经绑给别的账号了。
var ErrDuplicateBinding = fmt.Errorf("该代理已绑定其他账号")
