package ociclient

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成测试密钥失败: %v", err)
	}
	return key
}

func testCreds(t *testing.T, key *rsa.PrivateKey) *Credentials {
	t.Helper()
	return &Credentials{
		TenancyOCID: "ocid1.tenancy.oc1..aaaatenancy",
		UserOCID:    "ocid1.user.oc1..aaaauser",
		Fingerprint: FingerprintOf(&key.PublicKey),
		Region:      "ap-tokyo-1",
		PrivateKey:  key,
	}
}

// 固定时间，让 Date 头可预测。
var fixedTime = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

func TestSignGETBuildsExpectedSigningString(t *testing.T) {
	key := testKey(t)
	creds := testCreds(t, key)
	signer := NewSigner(creds)
	signer.now = func() time.Time { return fixedTime }

	req, err := http.NewRequest(http.MethodGet,
		"https://identity.ap-tokyo-1.oraclecloud.com/20160918/users/ocid1.user.oc1..aaaauser", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.Sign(req, nil); err != nil {
		t.Fatalf("签名失败: %v", err)
	}

	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, `Signature version="1",keyId="`) {
		t.Fatalf("Authorization 头前缀不正确: %s", auth)
	}
	// GET 不带 body，只签三个头。
	if !strings.Contains(auth, `headers="date (request-target) host"`) {
		t.Errorf("GET 请求签名头列表不正确: %s", auth)
	}
	if !strings.Contains(auth, `keyId="`+creds.KeyID()+`"`) {
		t.Errorf("keyId 不正确: %s", auth)
	}
	if req.Header.Get("Date") != fixedTime.Format(http.TimeFormat) {
		t.Errorf("Date 头 = %q，期望 %q", req.Header.Get("Date"), fixedTime.Format(http.TimeFormat))
	}

	want := strings.Join([]string{
		"date: " + fixedTime.Format(http.TimeFormat),
		"(request-target): get /20160918/users/ocid1.user.oc1..aaaauser",
		"host: identity.ap-tokyo-1.oraclecloud.com",
	}, "\n")
	assertSignatureOver(t, auth, key, want)
}

func TestSignPOSTIncludesBodyHeaders(t *testing.T) {
	key := testKey(t)
	signer := NewSigner(testCreds(t, key))
	signer.now = func() time.Time { return fixedTime }

	body := []byte(`{"displayName":"arm-tokyo-01"}`)
	req, err := http.NewRequest(http.MethodPost,
		"https://iaas.ap-tokyo-1.oraclecloud.com/20160918/instances", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.Sign(req, body); err != nil {
		t.Fatalf("签名失败: %v", err)
	}

	auth := req.Header.Get("Authorization")
	if !strings.Contains(auth, `headers="date (request-target) host content-length content-type x-content-sha256"`) {
		t.Fatalf("POST 请求签名头列表不正确: %s", auth)
	}

	sum := sha256.Sum256(body)
	wantDigest := base64.StdEncoding.EncodeToString(sum[:])
	if got := req.Header.Get("X-Content-Sha256"); got != wantDigest {
		t.Errorf("x-content-sha256 = %q，期望 %q", got, wantDigest)
	}
	if got := req.Header.Get("Content-Length"); got != "30" {
		t.Errorf("content-length = %q，期望 30", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type = %q，期望 application/json", got)
	}
}

// 空 body 的 POST 依然要带上三个 body 相关头，否则 OCI 会拒签。
func TestSignPOSTWithEmptyBody(t *testing.T) {
	key := testKey(t)
	signer := NewSigner(testCreds(t, key))
	signer.now = func() time.Time { return fixedTime }

	req, err := http.NewRequest(http.MethodPost,
		"https://iaas.ap-tokyo-1.oraclecloud.com/20160918/instances/x/actions/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.Sign(req, nil); err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	if got := req.Header.Get("Content-Length"); got != "0" {
		t.Errorf("空 body 的 content-length = %q，期望 0", got)
	}
	emptySum := sha256.Sum256(nil)
	if got := req.Header.Get("X-Content-Sha256"); got != base64.StdEncoding.EncodeToString(emptySum[:]) {
		t.Errorf("空 body 的摘要不正确: %s", got)
	}
}

// query 必须参与 (request-target) 的签名，否则改动分页参数就能重放签名。
func TestSignIncludesQueryInRequestTarget(t *testing.T) {
	key := testKey(t)
	signer := NewSigner(testCreds(t, key))
	signer.now = func() time.Time { return fixedTime }

	req, err := http.NewRequest(http.MethodGet,
		"https://identity.ap-tokyo-1.oraclecloud.com/20160918/compartments?compartmentId=abc&limit=100", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.Sign(req, nil); err != nil {
		t.Fatal(err)
	}

	want := strings.Join([]string{
		"date: " + fixedTime.Format(http.TimeFormat),
		"(request-target): get /20160918/compartments?compartmentId=abc&limit=100",
		"host: identity.ap-tokyo-1.oraclecloud.com",
	}, "\n")
	assertSignatureOver(t, req.Header.Get("Authorization"), key, want)
}

// assertSignatureOver 校验 Authorization 里的签名确实是对 want 这段文本做的。
func assertSignatureOver(t *testing.T, authHeader string, key *rsa.PrivateKey, want string) {
	t.Helper()
	const marker = `signature="`
	idx := strings.Index(authHeader, marker)
	if idx < 0 {
		t.Fatalf("Authorization 头缺少 signature: %s", authHeader)
	}
	encoded := authHeader[idx+len(marker):]
	encoded = strings.TrimSuffix(encoded, `"`)

	sig, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("签名不是合法 base64: %v", err)
	}
	digest := sha256.Sum256([]byte(want))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Errorf("签名与预期的待签名文本不匹配:\n%s\n错误: %v", want, err)
	}
}

func TestValidateRejectsFingerprintMismatch(t *testing.T) {
	key := testKey(t)
	creds := testCreds(t, key)
	creds.Fingerprint = "00:11:22:33:44:55:66:77:88:99:aa:bb:cc:dd:ee:ff"

	err := creds.Validate()
	if err == nil {
		t.Fatal("指纹不匹配时应当报错")
	}
	// 这是 401 的头号成因，报错必须点明是指纹问题。
	if !strings.Contains(err.Error(), "指纹") {
		t.Errorf("错误信息应说明是指纹问题，实际为: %v", err)
	}
}

func TestValidateRejectsMalformedOCID(t *testing.T) {
	key := testKey(t)
	for _, tc := range []struct {
		name string
		mut  func(*Credentials)
	}{
		{"tenancy", func(c *Credentials) { c.TenancyOCID = "ocid1.user.oc1..wrong" }},
		{"user", func(c *Credentials) { c.UserOCID = "not-an-ocid" }},
		{"region", func(c *Credentials) { c.Region = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creds := testCreds(t, key)
			tc.mut(creds)
			if err := creds.Validate(); err == nil {
				t.Errorf("%s 非法时应当报错", tc.name)
			}
		})
	}
}

func TestParsePrivateKeyPKCS1AndPKCS8(t *testing.T) {
	key := testKey(t)

	pkcs1 := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	got, err := ParsePrivateKey(pkcs1)
	if err != nil {
		t.Fatalf("PKCS#1 解析失败: %v", err)
	}
	if !got.Equal(key) {
		t.Error("PKCS#1 解析出的密钥与原始密钥不同")
	}

	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	got, err = ParsePrivateKey(pkcs8)
	if err != nil {
		t.Fatalf("PKCS#8 解析失败: %v", err)
	}
	if !got.Equal(key) {
		t.Error("PKCS#8 解析出的密钥与原始密钥不同")
	}
}

func TestParsePrivateKeyRejectsEncrypted(t *testing.T) {
	// 带口令的私钥必须给出可操作的提示，而不是一句笼统的解析失败。
	encrypted := []byte("-----BEGIN ENCRYPTED PRIVATE KEY-----\nAAAA\n-----END ENCRYPTED PRIVATE KEY-----\n")
	_, err := ParsePrivateKey(encrypted)
	if err == nil {
		t.Fatal("带口令的私钥应当被拒绝")
	}
	if !strings.Contains(err.Error(), "openssl") {
		t.Errorf("错误信息应给出 openssl 解密命令，实际为: %v", err)
	}
}

func TestParsePrivateKeyRejectsGarbage(t *testing.T) {
	if _, err := ParsePrivateKey([]byte("这不是 PEM")); err == nil {
		t.Fatal("非 PEM 内容应当被拒绝")
	}
}

func TestFingerprintFormat(t *testing.T) {
	key := testKey(t)
	fp := FingerprintOf(&key.PublicKey)

	parts := strings.Split(fp, ":")
	if len(parts) != 16 {
		t.Fatalf("指纹应为 16 段，实际 %d 段: %s", len(parts), fp)
	}
	for _, p := range parts {
		if len(p) != 2 {
			t.Fatalf("指纹每段应为 2 个十六进制字符: %s", fp)
		}
	}
	if fp != strings.ToLower(fp) {
		t.Errorf("指纹应为小写: %s", fp)
	}
}
