package auth

import (
	"strings"
	"testing"
	"time"
)

func TestHashAndVerifyPassword(t *testing.T) {
	const password = "correct horse battery staple"

	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatalf("散列失败: %v", err)
	}
	if strings.Contains(encoded, password) {
		t.Fatal("散列串中出现了明文口令")
	}
	if !strings.HasPrefix(encoded, "$argon2id$") {
		t.Errorf("散列串格式不正确: %s", encoded)
	}

	ok, err := VerifyPassword(password, encoded)
	if err != nil {
		t.Fatalf("校验出错: %v", err)
	}
	if !ok {
		t.Error("正确口令未通过校验")
	}

	ok, err = VerifyPassword("wrong password", encoded)
	if err != nil {
		t.Fatalf("校验出错: %v", err)
	}
	if ok {
		t.Error("错误口令通过了校验")
	}
}

// 同一口令每次散列都必须不同（盐是随机的），否则散列可以被彩虹表批量还原。
func TestHashPasswordUsesRandomSalt(t *testing.T) {
	a, err := HashPassword("same password")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword("same password")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("两次散列结果相同，盐没有随机化")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	for _, bad := range []string{
		"",
		"plaintext",
		"$argon2id$v=19$m=65536",
		"$bcrypt$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
	} {
		if _, err := VerifyPassword("x", bad); err == nil {
			t.Errorf("非法散列串 %q 应当报错", bad)
		}
	}
}

func TestNewTokenIsUniqueAndURLSafe(t *testing.T) {
	seen := make(map[string]bool, 64)
	for i := 0; i < 64; i++ {
		tok, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if seen[tok] {
			t.Fatal("生成了重复的令牌")
		}
		seen[tok] = true

		if strings.ContainsAny(tok, "+/=") {
			t.Errorf("令牌含有非 URL 安全字符: %s", tok)
		}
		if len(tok) < 40 {
			t.Errorf("令牌熵不足: %s", tok)
		}
	}
}

func TestTOTPRoundTrip(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}

	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	code, err := TOTPCode(secret, now)
	if err != nil {
		t.Fatalf("计算验证码失败: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("验证码应为 6 位，实际 %q", code)
	}
	if !VerifyTOTP(secret, code, now) {
		t.Error("当前时间窗的验证码未通过")
	}
}

// 允许 ±1 个时间窗，容忍手机与服务器约 30 秒的时钟偏移。
func TestTOTPAcceptsAdjacentWindows(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	prev, _ := TOTPCode(secret, now.Add(-30*time.Second))
	next, _ := TOTPCode(secret, now.Add(30*time.Second))

	if !VerifyTOTP(secret, prev, now) {
		t.Error("上一个时间窗的验证码应被接受")
	}
	if !VerifyTOTP(secret, next, now) {
		t.Error("下一个时间窗的验证码应被接受")
	}

	// 超出容忍范围就必须拒绝，否则窗口形同虚设。
	tooOld, _ := TOTPCode(secret, now.Add(-120*time.Second))
	if VerifyTOTP(secret, tooOld, now) {
		t.Error("过期太久的验证码不应被接受")
	}
}

// 返回的计数器用于防重放，必须与验证码实际所属的时间窗一致。
func TestVerifyTOTPWithCounterReportsWindow(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	wantCounter := now.Unix() / 30

	code, _ := TOTPCode(secret, now)
	got, ok := VerifyTOTPWithCounter(secret, code, now)
	if !ok {
		t.Fatal("验证码应通过")
	}
	if got != wantCounter {
		t.Errorf("计数器 = %d，期望 %d", got, wantCounter)
	}

	// 上一窗口的码要报告它自己的窗口号，而不是当前窗口号。
	prevCode, _ := TOTPCode(secret, now.Add(-30*time.Second))
	got, ok = VerifyTOTPWithCounter(secret, prevCode, now)
	if !ok {
		t.Fatal("上一窗口的验证码应通过")
	}
	if got != wantCounter-1 {
		t.Errorf("上一窗口计数器 = %d，期望 %d", got, wantCounter-1)
	}
}

func TestVerifyTOTPRejectsBadInput(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	now := time.Now()

	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if VerifyTOTP(secret, bad, now) {
			t.Errorf("非法验证码 %q 不应通过", bad)
		}
	}
	if VerifyTOTP("不是base32", "123456", now) {
		t.Error("非法密钥不应通过校验")
	}
}

// 用户手抄密钥时常带空格或连字符，应当容忍。
func TestDecodeSecretTolerantOfFormatting(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	now := time.Now()
	code, _ := TOTPCode(secret, now)

	spaced := ""
	for i, r := range secret {
		if i > 0 && i%4 == 0 {
			spaced += " "
		}
		spaced += string(r)
	}
	if !VerifyTOTP(spaced, code, now) {
		t.Error("带空格的密钥应能正常校验")
	}
	if !VerifyTOTP(strings.ToLower(secret), code, now) {
		t.Error("小写密钥应能正常校验")
	}
}

func TestTOTPProvisioningURI(t *testing.T) {
	uri := TOTPProvisioningURI("JBSWY3DPEHPK3PXP", "admin", "OCI Core")

	for _, want := range []string{
		"otpauth://totp/",
		"secret=JBSWY3DPEHPK3PXP",
		"issuer=OCI+Core",
		"digits=6",
		"period=30",
		"algorithm=SHA1",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI 缺少 %q: %s", want, uri)
		}
	}
}
