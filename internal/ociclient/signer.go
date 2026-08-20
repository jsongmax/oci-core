// Package ociclient 实现 OCI REST API 的调用，包括请求签名、区域端点解析和错误归类。
//
// OCI 不使用 OAuth，而是 HTTP Signature（draft-cavage-http-signatures-08）：
// 用租户的 RSA 私钥对若干请求头做 RSA-SHA256 签名，放进 Authorization 头。
// 整个协议只有几十行，这里自己实现，避免引入体积庞大的官方 SDK，
// 同时让错误处理和重试策略完全可控（见 errors.go）。
package ociclient

import (
	"crypto"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 需要签名的请求头集合。OCI 对带 body 的方法要求额外签三个头。
var (
	headersNoBody   = []string{"date", "(request-target)", "host"}
	headersWithBody = []string{"date", "(request-target)", "host", "content-length", "content-type", "x-content-sha256"}
)

// Credentials 是一份 OCI API 密钥。对应用户在控制台
// "用户设置 → API 密钥" 下载的配置文件里的四项。
type Credentials struct {
	TenancyOCID string
	UserOCID    string
	Fingerprint string
	Region      string
	PrivateKey  *rsa.PrivateKey
}

// KeyID 是 Authorization 头里的 keyId，格式固定为 tenancy/user/fingerprint。
func (c *Credentials) KeyID() string {
	return c.TenancyOCID + "/" + c.UserOCID + "/" + c.Fingerprint
}

// Validate 做离线的格式检查，在真正发请求之前拦下最常见的配置错误。
func (c *Credentials) Validate() error {
	switch {
	case !strings.HasPrefix(c.TenancyOCID, "ocid1.tenancy."):
		return fmt.Errorf("tenancy OCID 格式不对，应以 ocid1.tenancy. 开头，实际为 %q", truncate(c.TenancyOCID, 32))
	case !strings.HasPrefix(c.UserOCID, "ocid1.user."):
		return fmt.Errorf("user OCID 格式不对，应以 ocid1.user. 开头，实际为 %q", truncate(c.UserOCID, 32))
	case c.PrivateKey == nil:
		return errors.New("私钥为空")
	case c.Region == "":
		return errors.New("region 为空")
	}
	// 指纹和私钥必须是同一对。这是 401 NotAuthenticated 的头号成因：
	// 用户换了密钥却忘了同步更新指纹。离线就能查出来，不用等服务端拒绝。
	actual := FingerprintOf(&c.PrivateKey.PublicKey)
	if !strings.EqualFold(normalizeFingerprint(c.Fingerprint), actual) {
		return fmt.Errorf("指纹与私钥不匹配：配置为 %s，私钥实际指纹为 %s", c.Fingerprint, actual)
	}
	return nil
}

// Signer 用一份凭据为 HTTP 请求签名。可安全地被多 goroutine 共用。
type Signer struct {
	creds *Credentials
	// now 可在测试中替换，用于生成确定性的 Date 头。
	now func() time.Time
}

func NewSigner(creds *Credentials) *Signer {
	return &Signer{creds: creds, now: time.Now}
}

// Sign 就地填充签名所需的请求头，并写入 Authorization。
// body 是即将发送的请求体（无 body 传 nil）；调用方需保证它与 req.Body 一致。
func (s *Signer) Sign(req *http.Request, body []byte) error {
	if s.creds == nil || s.creds.PrivateKey == nil {
		return errors.New("ociclient: 签名器缺少私钥")
	}

	if req.Header.Get("Date") == "" {
		// OCI 允许 5 分钟的时钟偏移，超出即 401。服务器时间不准是第二常见的认证失败原因。
		req.Header.Set("Date", s.now().UTC().Format(http.TimeFormat))
	}
	if req.Header.Get("Host") == "" {
		req.Header.Set("Host", req.URL.Host)
	}

	signing := headersNoBody
	if methodHasBody(req.Method) {
		// 即使 body 为空，OCI 也要求这三个头存在且参与签名。
		sum := sha256.Sum256(body)
		req.Header.Set("X-Content-Sha256", base64.StdEncoding.EncodeToString(sum[:]))
		req.Header.Set("Content-Length", strconv.Itoa(len(body)))
		if req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.ContentLength = int64(len(body))
		signing = headersWithBody
	}

	signingString, err := buildSigningString(req, signing)
	if err != nil {
		return err
	}

	digest := sha256.Sum256([]byte(signingString))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.creds.PrivateKey, crypto.SHA256, digest[:])
	if err != nil {
		return fmt.Errorf("ociclient: 签名失败: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf(
		`Signature version="1",keyId="%s",algorithm="rsa-sha256",headers="%s",signature="%s"`,
		s.creds.KeyID(),
		strings.Join(signing, " "),
		base64.StdEncoding.EncodeToString(sig),
	))
	return nil
}

// buildSigningString 拼出待签名文本：每行 "header: value"，用 \n 连接，结尾无换行。
func buildSigningString(req *http.Request, names []string) (string, error) {
	lines := make([]string, 0, len(names))
	for _, name := range names {
		var value string
		if name == "(request-target)" {
			// 小写方法 + 空格 + 带 query 的转义路径。
			value = strings.ToLower(req.Method) + " " + req.URL.RequestURI()
		} else {
			value = req.Header.Get(name)
			if name == "host" && value == "" {
				value = req.URL.Host
			}
			if value == "" {
				return "", fmt.Errorf("ociclient: 待签名头 %q 缺失", name)
			}
		}
		lines = append(lines, name+": "+value)
	}
	return strings.Join(lines, "\n"), nil
}

func methodHasBody(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	}
	return false
}

// ParsePrivateKey 解析 PEM 格式的 RSA 私钥，同时支持 PKCS#1 和 PKCS#8。
func ParsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("不是有效的 PEM 内容，请确认粘贴了完整的 -----BEGIN ... KEY----- 区块")
	}

	// 带口令的私钥：旧式 PEM 加密已被 Go 弃用，新式 PKCS#8 加密 Go 标准库不支持。
	// 与其半支持，不如给一条能直接照抄的解密命令。
	if x509.IsEncryptedPEMBlock(block) || strings.Contains(string(pemBytes), "ENCRYPTED") { //nolint:staticcheck // 仅用于检测，不解密
		return nil, errors.New("检测到带口令的私钥，请先解密后再导入：" +
			"openssl rsa -in oci_api_key.pem -out oci_api_key_plain.pem")
	}

	switch block.Type {
	case "RSA PRIVATE KEY": // PKCS#1
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY": // PKCS#8
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("私钥类型为 %T，OCI API 密钥必须是 RSA", key)
		}
		return rsaKey, nil
	default:
		return nil, fmt.Errorf("不支持的 PEM 类型 %q，应为 RSA PRIVATE KEY 或 PRIVATE KEY", block.Type)
	}
}

// FingerprintOf 按 OCI 的算法计算公钥指纹：DER 编码后取 MD5，转成冒号分隔的小写十六进制。
// 这里的 MD5 只是 Oracle 选定的标识符算法，不承担任何安全职责。
func FingerprintOf(pub *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return ""
	}
	sum := md5.Sum(der) //nolint:gosec // OCI 协议规定的指纹算法，非安全用途
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(parts, ":")
}

func normalizeFingerprint(fp string) string {
	return strings.ToLower(strings.TrimSpace(fp))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
