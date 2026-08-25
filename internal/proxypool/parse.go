// Package proxypool 负责代理的解析、存活检测与规范化。
//
// 存在的意义是网络隔离：给每个 Oracle 账号配一条独立出口，避免所有账号的
// API 调用从同一个 IP 出去。有两件事必须一开始就说清楚，否则这个功能会变成
// 一种虚假的安全感：
//
//   - **代理只换 IP，不换身份。** 每个 OCI 请求都带该账号的私钥签名，
//     Oracle 百分百知道是哪个租户在调。隔离的只有"源 IP"这一个维度，
//     而它是所有关联信号里最弱的一个。
//   - **共用出口比不用代理更糟。** 两个账号配同一条代理，等于主动把它们
//     绑在一个 IP 上，凭空制造一个本来不存在的关联信号。所以重复绑定
//     是硬性拒绝，不是提示。
package proxypool

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// 支持的代理协议。
//
// Go 的 net/http 原生支持这三种；socks5h 不支持，明确拒绝而不是
// 让它一路走到运行时才神秘失败。
const (
	SchemeHTTP   = "http"
	SchemeHTTPS  = "https"
	SchemeSOCKS5 = "socks5"
)

// Parsed 是一条解析后的代理。
type Parsed struct {
	Scheme   string
	Host     string
	Port     int
	Username string
	Password string
	// Label 来自行尾的 # 注释，没有则为空。
	Label string
}

// Addr 返回 host:port。
func (p Parsed) Addr() string { return net.JoinHostPort(p.Host, strconv.Itoa(p.Port)) }

// URL 拼出可直接交给 http.Transport 的代理地址。
func (p Parsed) URL() string {
	var auth string
	if p.Username != "" {
		auth = url.UserPassword(p.Username, p.Password).String() + "@"
	}
	return fmt.Sprintf("%s://%s%s", p.Scheme, auth, p.Addr())
}

// Masked 返回可以安全显示与返回给前端的形式，密码打码。
//
// 代理密码是凭据。这个面板对 OCI 私钥的态度是"界面上没有任何导出或回显
// 入口"，付费代理的账密不该比它低一档。
func (p Parsed) Masked() string {
	var auth string
	if p.Username != "" {
		auth = p.Username + ":****@"
	}
	return fmt.Sprintf("%s://%s%s", p.Scheme, auth, p.Addr())
}

// ParseLine 解析一行代理。
//
// 代理商给的格式五花八门，这里尽量都认下来：
//
//	1.2.3.4:8080
//	1.2.3.4:8080:user:pass          ← 最常见
//	user:pass@1.2.3.4:8080
//	socks5://user:pass@1.2.3.4:1080
//	http://1.2.3.4:8080  # 香港节点
//
// 不写协议时默认 http —— 绝大多数 HTTP 代理商都这么给。
func ParseLine(line string) (Parsed, error) {
	raw := strings.TrimSpace(line)
	if raw == "" {
		return Parsed{}, fmt.Errorf("空行")
	}

	// 行尾注释当作备注名。放在最前面剥离，免得 # 后面的内容干扰后续解析。
	var label string
	if i := strings.Index(raw, "#"); i >= 0 {
		label = strings.TrimSpace(raw[i+1:])
		raw = strings.TrimSpace(raw[:i])
	}
	if raw == "" {
		return Parsed{}, fmt.Errorf("只有注释没有地址")
	}

	scheme := SchemeHTTP
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme = strings.ToLower(raw[:i])
		raw = raw[i+3:]
	}
	switch scheme {
	case SchemeHTTP, SchemeHTTPS, SchemeSOCKS5:
	case "socks5h":
		return Parsed{}, fmt.Errorf("不支持 socks5h（Go 的 net/http 只支持 socks5）")
	default:
		return Parsed{}, fmt.Errorf("不支持的协议 %q", scheme)
	}

	user, pass, hostPort, err := splitAuth(raw)
	if err != nil {
		return Parsed{}, err
	}

	host, portStr, err := net.SplitHostPort(hostPort)
	if err != nil {
		return Parsed{}, fmt.Errorf("地址格式无法识别: %s", hostPort)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return Parsed{}, fmt.Errorf("端口无效: %s", portStr)
	}
	if strings.TrimSpace(host) == "" {
		return Parsed{}, fmt.Errorf("缺少主机名")
	}

	return Parsed{
		Scheme: scheme, Host: host, Port: port,
		Username: user, Password: pass, Label: label,
	}, nil
}

// splitAuth 从 "user:pass@host:port" 或 "host:port:user:pass" 里拆出认证信息。
//
// 两种写法都很常见，必须都认。区分方法是看有没有 @：
// 有 @ 就是标准 URL 写法，没有 @ 而恰好four段就是 host:port:user:pass。
func splitAuth(raw string) (user, pass, hostPort string, err error) {
	// 标准写法：最后一个 @ 之前是认证信息。
	// 用 LastIndex 而不是 Index —— 密码里可能含 @。
	if i := strings.LastIndex(raw, "@"); i >= 0 {
		cred := raw[:i]
		hostPort = raw[i+1:]
		if j := strings.Index(cred, ":"); j >= 0 {
			user, pass = cred[:j], cred[j+1:]
		} else {
			user = cred
		}
		return user, pass, hostPort, nil
	}

	parts := strings.Split(raw, ":")
	switch len(parts) {
	case 2:
		// host:port
		return "", "", raw, nil
	case 4:
		// host:port:user:pass —— 代理商最常给的格式
		return parts[2], parts[3], parts[0] + ":" + parts[1], nil
	default:
		// IPv6 字面量形如 [::1]:8080，冒号很多但带方括号，交给 SplitHostPort。
		if strings.HasPrefix(raw, "[") {
			return "", "", raw, nil
		}
		return "", "", "", fmt.Errorf("无法识别的格式（认得 host:port、host:port:user:pass、user:pass@host:port）")
	}
}

// ParseResult 是批量导入里的一行结果。
//
// 解析失败的行不能直接丢掉：用户粘了 20 行进来，得知道是哪一行有问题、
// 问题是什么，而不是只看到"成功 19 条"。
type ParseResult struct {
	// Line 是原始行号，从 1 开始。
	Line int `json:"line"`
	// Raw 是原文，失败时回显给用户对照。成功时做过脱敏。
	Raw    string `json:"raw"`
	Proxy  Parsed `json:"-"`
	Error  string `json:"error,omitempty"`
	Masked string `json:"masked,omitempty"`
}

// ParseBulk 逐行解析，返回与输入行一一对应的结果。
//
// 空行和纯注释行直接跳过，不计入结果——用户从表格里复制过来经常带空行，
// 把它们报成错误只会淹没真正的问题行。
func ParseBulk(text string) []ParseResult {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	out := make([]ParseResult, 0, len(lines))

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		res := ParseResult{Line: i + 1, Raw: trimmed}
		p, err := ParseLine(line)
		if err != nil {
			res.Error = err.Error()
		} else {
			res.Proxy = p
			res.Masked = p.Masked()
			// 成功的行不回显原文，里面有密码。
			res.Raw = p.Masked()
		}
		out = append(out, res)
	}
	return out
}
