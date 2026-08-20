package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 规定 TOTP 使用 HMAC-SHA1
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// TOTP 参数固定为 RFC 6238 的默认值，也是所有主流验证器 App 的默认设置。
const (
	totpPeriod  = 30 // 秒
	totpDigits  = 6
	totpSkew    = 1  // 允许前后各 1 个时间窗，容忍约 ±30 秒的时钟偏移
	secretBytes = 20 // RFC 4226 建议的 HMAC-SHA1 密钥长度
)

// base32NoPad 是验证器 App 通用的密钥编码：大写 base32、无填充。
var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret 生成一个新的 TOTP 密钥，返回可供用户手动输入的 base32 文本。
func GenerateTOTPSecret() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("auth: 生成 TOTP 密钥失败: %w", err)
	}
	return base32NoPad.EncodeToString(buf), nil
}

// TOTPProvisioningURI 构造 otpauth:// URI，前端据此渲染二维码供验证器扫描。
func TOTPProvisioningURI(secret, accountName, issuer string) string {
	label := issuer + ":" + accountName
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(totpPeriod))

	return "otpauth://totp/" + url.PathEscape(label) + "?" + q.Encode()
}

// TOTPCode 计算指定时刻的验证码。导出它是为了让测试和"绑定时验证一次"的流程可用。
func TOTPCode(secret string, t time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return codeAtCounter(key, uint64(t.Unix()/totpPeriod)), nil
}

// VerifyTOTP 校验用户输入的验证码，允许 ±1 个时间窗的偏移。
func VerifyTOTP(secret, code string, now time.Time) bool {
	_, ok := VerifyTOTPWithCounter(secret, code, now)
	return ok
}

// VerifyTOTPWithCounter 校验验证码并返回它对应的时间窗序号。
//
// 调用方必须把这个序号记下来，并拒绝序号不大于已记录值的后续请求——
// TOTP 码在 30 秒窗口内是可以重放的，算法本身挡不住，
// 只有"这个窗口已经用过了"这条记录才能让它真正一次性。
func VerifyTOTPWithCounter(secret, code string, now time.Time) (int64, bool) {
	key, err := decodeSecret(secret)
	if err != nil {
		return 0, false
	}
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, false
	}

	counter := now.Unix() / totpPeriod
	for offset := int64(-totpSkew); offset <= totpSkew; offset++ {
		candidate := codeAtCounter(key, uint64(counter+offset))
		// 常数时间比较，避免通过响应时间侧信道逐位试探。
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			return counter + offset, true
		}
	}
	return 0, false
}

// codeAtCounter 是 RFC 4226 的 HOTP 算法：HMAC-SHA1 后做动态截断。
func codeAtCounter(key []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)

	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)

	// 动态截断：用最后一个字节的低 4 位当作偏移量，从该处取 4 字节。
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff

	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, value%mod)
}

// decodeSecret 解析 base32 密钥。容忍用户输入里的空格、连字符和小写。
func decodeSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(secret)))
	key, err := base32NoPad.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("auth: TOTP 密钥不是合法的 base32: %w", err)
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("auth: TOTP 密钥为空")
	}
	return key, nil
}
