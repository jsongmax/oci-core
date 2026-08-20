package ociclient

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Service 描述一个 OCI 服务的端点主机前缀与 API 版本。
type Service struct {
	Host    string // 端点主机名的第一段，例如 "iaas"
	Version string // URL 里的版本路径段，例如 "20160918"
	// OCIInfix 表示该服务的域名要多一段 .oci，形如
	// limits.{region}.oci.oraclecloud.com。
	//
	// 这不是可选的风格差异：不带 .oci 的域名会被泛解析命中，DNS 查得到
	// 但 TCP 连不上，请求会一直挂到超时——表现为整个接口卡三十秒，
	// 而不是干脆利落地报错。
	OCIInfix bool
}

// P0 只需要 Identity；其余服务在 P1/P2 用到时才会被引用，先在此登记以固化版本号。
var (
	ServiceIdentity   = Service{Host: "identity", Version: "20160918"}
	ServiceCore       = Service{Host: "iaas", Version: "20160918"} // 计算 / 网络 / 块存储
	ServiceLimits     = Service{Host: "limits", Version: "20190729", OCIInfix: true}
	ServiceMonitoring = Service{Host: "telemetry", Version: "20180401"}
	// 订阅信息挂在 organizations 服务下,不是 identity。
	ServiceOrganizations = Service{Host: "organizations", Version: "20230401", OCIInfix: true}
)

// realmOf 返回区域所属 realm 的域名后缀。绝大多数商业区域属于 OC1。
func realmOf(region string) string {
	switch {
	case strings.HasPrefix(region, "us-gov-"), strings.HasPrefix(region, "us-lang-"):
		return "oraclegovcloud.com"
	case strings.HasPrefix(region, "uk-gov-"):
		return "oraclegovcloud.uk"
	default:
		return "oraclecloud.com"
	}
}

// shortCodes 是 Oracle 的三字母区域代号。控制台下载的配置文件里 region 一般是全名，
// 但用户手抄时经常写成代号，这里做一次归一化。
var shortCodes = map[string]string{
	"iad": "us-ashburn-1", "phx": "us-phoenix-1", "sjc": "us-sanjose-1", "ord": "us-chicago-1",
	"yyz": "ca-toronto-1", "yul": "ca-montreal-1",
	"gru": "sa-saopaulo-1", "scl": "sa-santiago-1", "vcp": "sa-vinhedo-1", "bog": "sa-bogota-1", "vap": "sa-valparaiso-1",
	"fra": "eu-frankfurt-1", "zrh": "eu-zurich-1", "ams": "eu-amsterdam-1", "mrs": "eu-marseille-1",
	"cdg": "eu-paris-1", "mad": "eu-madrid-1", "lin": "eu-milan-1", "arn": "eu-stockholm-1",
	"lhr": "uk-london-1", "cwl": "uk-cardiff-1",
	"nrt": "ap-tokyo-1", "kix": "ap-osaka-1", "icn": "ap-seoul-1", "syd": "ap-sydney-1",
	"mel": "ap-melbourne-1", "bom": "ap-mumbai-1", "hyd": "ap-hyderabad-1", "sin": "ap-singapore-1",
	"jed": "me-jeddah-1", "dxb": "me-dubai-1", "auh": "me-abudhabi-1", "ruh": "me-riyadh-1",
	"jnb": "af-johannesburg-1",
	"qro": "mx-queretaro-1", "mty": "mx-monterrey-1",
	"aga": "us-saltlake-2",
}

// knownRegions 是全名集合，由 shortCodes 反推得到，用于校验用户输入。
var knownRegions = func() map[string]string {
	m := make(map[string]string, len(shortCodes))
	for code, full := range shortCodes {
		m[full] = code
	}
	return m
}()

// regionPattern 是区域名允许的字符集。
//
// 不做白名单（Oracle 持续开新区域，本表滞后不该导致拒绝服务），但必须做
// 格式校验：区域名会被拼进 Endpoint 的主机名部分，含 "/" 的输入能把主机名
// 提前截断——region="evil.com/" 会让 https://iaas.{region}.oraclecloud.com
// 变成 https://iaas.evil.com/.oraclecloud.com，请求连同 OCI 签名一起发到
// 攻击者的服务器上。
//
// 真实的区域名一律形如 ap-osaka-1，这个字符集够用。
var regionPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// NormalizeRegion 把三字母代号展开为区域全名，并做大小写与空白归一化。
//
// 格式不合法的输入返回空串，由调用方回落到账号默认区域——
// 返回原样会让非法值一路流进 URL 拼接。
func NormalizeRegion(region string) string {
	r := strings.ToLower(strings.TrimSpace(region))
	if full, ok := shortCodes[r]; ok {
		return full
	}
	if r != "" && !regionPattern.MatchString(r) {
		return ""
	}
	return r
}

// IsKnownRegion 报告区域是否在本表内。仅用于给用户提示，不作为拒绝的依据。
func IsKnownRegion(region string) bool {
	_, ok := knownRegions[NormalizeRegion(region)]
	return ok
}

// KnownRegions 返回所有已登记的区域全名，按字典序排列。
func KnownRegions() []string {
	out := make([]string, 0, len(knownRegions))
	for full := range knownRegions {
		out = append(out, full)
	}
	sort.Strings(out)
	return out
}

// Endpoint 拼出某个服务在某个区域的基础 URL（不含版本段）。
func Endpoint(svc Service, region string) string {
	r := NormalizeRegion(region)
	if svc.OCIInfix {
		return fmt.Sprintf("https://%s.%s.oci.%s", svc.Host, r, realmOf(r))
	}
	return fmt.Sprintf("https://%s.%s.%s", svc.Host, r, realmOf(r))
}
