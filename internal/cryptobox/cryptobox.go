// Package cryptobox 负责 OCI 私钥的落盘加密。
//
// 威胁模型：攻击者拿到了 SQLite 文件（备份泄露、误传仓库、宿主机被翻），
// 但没有拿到主密钥文件。此时数据库里的私钥必须是不可用的密文。
//
// 主密钥与数据库分开存放，用 AES-256-GCM 做信封加密。每条私钥用独立的
// nonce，并把账号 ID 作为附加认证数据（AAD）绑定——这样即使攻击者能写库，
// 也无法把 A 账号的密文搬到 B 账号行上冒用。
package cryptobox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// KeySize 是主密钥长度，对应 AES-256。
const KeySize = 32

// Box 用一把主密钥加解密。可安全地被多 goroutine 共用。
type Box struct {
	aead cipher.AEAD
}

// New 用给定的主密钥创建 Box。
func New(masterKey []byte) (*Box, error) {
	if len(masterKey) != KeySize {
		return nil, fmt.Errorf("cryptobox: 主密钥长度必须是 %d 字节，实际 %d", KeySize, len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("cryptobox: 初始化分组密码失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cryptobox: 初始化 GCM 失败: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal 加密 plaintext。aad 应当是与该条密文绑定的稳定标识（这里用账号 ID）。
// 返回的 nonce 需要与密文一同存储。
func (b *Box) Seal(plaintext []byte, aad string) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("cryptobox: 生成 nonce 失败: %w", err)
	}
	ciphertext = b.aead.Seal(nil, nonce, plaintext, []byte(aad))
	return ciphertext, nonce, nil
}

// Open 解密。aad 必须与加密时完全一致，否则认证失败。
func (b *Box) Open(ciphertext, nonce []byte, aad string) ([]byte, error) {
	if len(nonce) != b.aead.NonceSize() {
		return nil, fmt.Errorf("cryptobox: nonce 长度错误，应为 %d 字节", b.aead.NonceSize())
	}
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		// 不透出底层细节：认证失败的原因可能是密钥换了、数据被改了或 AAD 不匹配，
		// 对调用方来说处理方式都一样。
		return nil, errors.New("cryptobox: 解密失败，主密钥可能已更换或数据已损坏")
	}
	return plaintext, nil
}

// LoadOrCreateMasterKey 从 path 读取主密钥；文件不存在时生成一把新的并写入。
//
// 文件内容是 64 个十六进制字符，方便用户备份和通过环境变量迁移。
// 权限设为 0600——在 Windows 上 ACL 语义不同，但设置它无害且在 Linux 部署时是必需的。
func LoadOrCreateMasterKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, err := decodeKey(string(data))
		if err != nil {
			return nil, fmt.Errorf("cryptobox: 主密钥文件 %s 内容无效: %w", path, err)
		}
		return key, nil

	case errors.Is(err, os.ErrNotExist):
		key := make([]byte, KeySize)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, fmt.Errorf("cryptobox: 生成主密钥失败: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("cryptobox: 创建主密钥目录失败: %w", err)
		}
		if err := os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
			return nil, fmt.Errorf("cryptobox: 写入主密钥失败: %w", err)
		}
		return key, nil

	default:
		return nil, fmt.Errorf("cryptobox: 读取主密钥失败: %w", err)
	}
}

// DecodeKey 解析十六进制表示的主密钥，用于从环境变量注入。
func DecodeKey(s string) ([]byte, error) { return decodeKey(s) }

func decodeKey(s string) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("不是合法的十六进制: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("解码后长度为 %d 字节，应为 %d", len(key), KeySize)
	}
	return key, nil
}
