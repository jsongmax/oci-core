// Package auth 提供面板自身的登录凭据处理：口令散列、TOTP 双因子、会话令牌。
//
// 注意区分两套"凭据"：本包处理的是用户登录本面板的凭据；
// OCI 的 API 密钥由 internal/cryptobox 和 internal/store 负责。
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id 参数。这些值针对"一台 1C1G 小鸡上偶尔登录一次"的场景选取：
// 64 MiB 内存 × 3 轮在这类机器上约 100ms，既挡得住离线爆破，也不会拖垮登录。
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

// ErrInvalidHash 表示存储的散列串格式无法解析。
var ErrInvalidHash = errors.New("auth: 口令散列格式无效")

// HashPassword 用 argon2id 散列口令，返回自描述的编码串，
// 格式与 PHC 规范一致，便于日后调参而不破坏旧数据。
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("auth: 生成盐失败: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword 校验口令。返回的 error 只表示散列串本身有问题；
// 口令不匹配通过 false 返回，调用方不应把两者区别对待地反馈给用户。
func VerifyPassword(password, encoded string) (bool, error) {
	params, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
}

func decodeHash(encoded string) (params argonParams, salt, hash []byte, err error) {
	parts := strings.Split(encoded, "$")
	// 前导 $ 会产生一个空段：["", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return params, nil, nil, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return params, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return params, nil, nil, fmt.Errorf("auth: 不支持的 argon2 版本 %d", version)
	}

	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.memory, &params.time, &params.threads); err != nil {
		return params, nil, nil, ErrInvalidHash
	}

	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return params, nil, nil, ErrInvalidHash
	}
	if hash, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return params, nil, nil, ErrInvalidHash
	}
	return params, salt, hash, nil
}

// NewToken 生成一个高熵的随机令牌，用于会话 ID 等场景。
// 返回 URL 安全的 base64 文本（32 字节熵）。
func NewToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("auth: 生成令牌失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
