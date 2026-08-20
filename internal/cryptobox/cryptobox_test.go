package cryptobox

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func newTestBox(t *testing.T) (*Box, []byte) {
	t.Helper()
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box, key
}

func TestSealOpenRoundTrip(t *testing.T) {
	box, _ := newTestBox(t)
	plaintext := []byte("-----BEGIN RSA PRIVATE KEY-----\nMIIE...\n-----END RSA PRIVATE KEY-----")

	ciphertext, nonce, err := box.Seal(plaintext, "acct-123")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if bytes.Contains(ciphertext, []byte("BEGIN RSA")) {
		t.Fatal("密文中出现了明文片段")
	}

	got, err := box.Open(ciphertext, nonce, "acct-123")
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Error("解密结果与原文不一致")
	}
}

// AAD 绑定账号 ID：即使攻击者能写库，也无法把 A 账号的密文搬到 B 账号行上冒用。
func TestOpenRejectsMismatchedAAD(t *testing.T) {
	box, _ := newTestBox(t)
	ciphertext, nonce, err := box.Seal([]byte("私钥内容"), "acct-A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.Open(ciphertext, nonce, "acct-B"); err == nil {
		t.Fatal("换用其他账号 ID 作为 AAD 时必须解密失败")
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	box, _ := newTestBox(t)
	ciphertext, nonce, err := box.Seal([]byte("私钥内容"), "acct-A")
	if err != nil {
		t.Fatal(err)
	}
	ciphertext[0] ^= 0xff
	if _, err := box.Open(ciphertext, nonce, "acct-A"); err == nil {
		t.Fatal("密文被篡改后必须解密失败")
	}
}

func TestOpenRejectsWrongMasterKey(t *testing.T) {
	box1, _ := newTestBox(t)
	box2, _ := newTestBox(t)

	ciphertext, nonce, err := box1.Seal([]byte("私钥内容"), "acct-A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box2.Open(ciphertext, nonce, "acct-A"); err == nil {
		t.Fatal("换主密钥后必须解密失败")
	}
}

// 每次加密都必须用新的 nonce——GCM 下 nonce 复用会直接泄露明文异或值。
func TestSealUsesFreshNonce(t *testing.T) {
	box, _ := newTestBox(t)
	plaintext := []byte("同样的内容")

	c1, n1, err := box.Seal(plaintext, "acct-A")
	if err != nil {
		t.Fatal(err)
	}
	c2, n2, err := box.Seal(plaintext, "acct-A")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(n1, n2) {
		t.Fatal("两次加密使用了相同的 nonce")
	}
	if bytes.Equal(c1, c2) {
		t.Fatal("相同明文产生了相同密文")
	}
}

func TestNewRejectsBadKeySize(t *testing.T) {
	for _, size := range []int{0, 16, 31, 33} {
		if _, err := New(make([]byte, size)); err == nil {
			t.Errorf("长度 %d 的主密钥应被拒绝", size)
		}
	}
}

func TestLoadOrCreateMasterKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "master.key")

	key1, err := LoadOrCreateMasterKey(path)
	if err != nil {
		t.Fatalf("首次创建失败: %v", err)
	}
	if len(key1) != KeySize {
		t.Fatalf("主密钥长度 = %d，期望 %d", len(key1), KeySize)
	}

	// 再次调用必须读到同一把密钥，否则重启后所有已存私钥都会解不开。
	key2, err := LoadOrCreateMasterKey(path)
	if err != nil {
		t.Fatalf("二次读取失败: %v", err)
	}
	if !bytes.Equal(key1, key2) {
		t.Fatal("二次读取得到了不同的主密钥")
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("主密钥文件未落盘: %v", err)
	}
}

func TestLoadMasterKeyRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	if err := os.WriteFile(path, []byte("不是十六进制"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateMasterKey(path); err == nil {
		t.Fatal("损坏的主密钥文件应当报错，而不是静默生成新密钥")
	}
}

func TestDecodeKey(t *testing.T) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	hexKey := ""
	for _, b := range key {
		const digits = "0123456789abcdef"
		hexKey += string(digits[b>>4]) + string(digits[b&0x0f])
	}

	got, err := DecodeKey("  " + hexKey + "\n")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !bytes.Equal(got, key) {
		t.Error("解析结果与原始密钥不一致")
	}

	if _, err := DecodeKey("abcd"); err == nil {
		t.Error("长度不足的密钥应被拒绝")
	}
}
