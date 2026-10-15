package notify

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// 出网边界。
//
// 通知 URL 由面板管理员填写，面板进程却往往能访问宿主机回环、Docker 网桥、
// 云元数据和内网服务。没有边界的话，一个被盗的会话就能借面板向这些地址发请求，
// 错误响应还会把对方返回的一部分内容带回界面。
//
// 默认拒绝回环、私有、链路本地等非公网目的地；确需推到内网 webhook 时，
// 由部署方用 OCICORE_NOTIFY_ALLOWED_NETS 显式放行目标网段。
//
// 校验落在真正建连的那一刻（net.Dialer.Control 拿到的是解析后的 IP），
// 而不是只在保存配置时看一眼主机名：后者挡不住 DNS 重绑定，也挡不住跳转。
// 每次跳转都会重新拨号，因此同样受约束。
//
// 通知客户端**不使用** HTTP_PROXY / HTTPS_PROXY 等环境代理。经代理时建连的是代理，
// 目标域名交给代理自己去解析，面板这边无论预检多少次，都拦不住代理端解析到内网
// （DNS 重绑定）。与其给出一个经代理时并不成立的保证，不如只走直连。

// maxRedirects 是允许跟随的跳转次数。正常的 webhook 不该跳转，留一点余量即可。
const maxRedirects = 3

// blockedPrefixes 是 netip 自带判断之外、同样不该被通知请求触达的网段。
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "本网络"，部分系统会当作本机
	netip.MustParsePrefix("100.64.0.0/10"),  // 运营商级 NAT，云厂商内网常用
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF 协议分配
	netip.MustParsePrefix("198.18.0.0/15"),  // 基准测试网段
	netip.MustParsePrefix("240.0.0.0/4"),    // 保留，含广播地址
	netip.MustParsePrefix("fec0::/10"),      // 已废弃的站点本地地址
	netip.MustParsePrefix("64:ff9b:1::/48"), // 本地使用的 NAT64，可映射到内网 IPv4
}

// ErrEgressBlocked 表示通知目标落在默认禁止的地址范围内。
var ErrEgressBlocked = errors.New("通知目标属于回环、内网或链路本地地址，默认禁止访问")

// egressGuard 判断一个目的 IP 能否被通知请求访问。
type egressGuard struct {
	allowed []netip.Prefix
}

// isPublic 报告 ip 是否是可以默认访问的公网地址。
func isPublic(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

func (g *egressGuard) checkIP(ip netip.Addr) error {
	ip = ip.Unmap()
	for _, p := range g.allowed {
		if p.Contains(ip) {
			return nil
		}
	}
	if !isPublic(ip) {
		return fmt.Errorf("%w（%s）；如确需推送到内网，请在 OCICORE_NOTIFY_ALLOWED_NETS 中放行该网段", ErrEgressBlocked, ip)
	}
	return nil
}

// control 在套接字 connect 之前执行，address 已是解析后的 IP:端口。
func (g *egressGuard) control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("拨号地址 %q 不是 IP", address)
	}
	return g.checkIP(ip)
}

// newHTTPClient 构造带出网边界的 HTTP 客户端。
func newHTTPClient(allowed []netip.Prefix) *http.Client {
	g := &egressGuard{allowed: allowed}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: g.control}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	// 不走环境代理，原因见文件开头。
	transport.Proxy = nil
	transport.DialContext = dialer.DialContext

	return &http.Client{
		Timeout:   sendTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("跳转次数超过 %d 次", maxRedirects)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("不允许跳转到 %s 协议", req.URL.Scheme)
			}
			return nil
		},
	}
}
