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
	"sync/atomic"

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

// ErrBusy 表示同时进行的口令计算已达上限，本次直接拒绝。
var ErrBusy = errors.New("auth: 口令校验繁忙，请稍后再试")

// 每次 Argon2 占 64 MiB。不设上限时，登录接口来多少并发就同时算多少，
// 1C1G 的小机器几十个请求就会被内存压垮——限流按 IP 计数，挡不住这个。
// 所以全局只允许 maxActiveHashes 个同时计算，另外最多 maxQueuedHashes 个排队，
// 再多的请求立即拿到 ErrBusy，不再占内存也不再堆积。
const (
	maxActiveHashes = 2
	maxQueuedHashes = 16
)

var (
	hashSlots    = make(chan struct{}, maxActiveHashes)
	hashInFlight atomic.Int32
)

// idKey 是带全局并发闸门的 argon2.IDKey。
func idKey(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) ([]byte, error) {
	if hashInFlight.Add(1) > maxActiveHashes+maxQueuedHashes {
		hashInFlight.Add(-1)
		return nil, ErrBusy
	}
	defer hashInFlight.Add(-1)

	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	return argon2.IDKey(password, salt, time, memory, threads, keyLen), nil
}

// HashPassword 用 argon2id 散列口令，返回自描述的编码串，
// 格式与 PHC 规范一致，便于日后调参而不破坏旧数据。
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("auth: 生成盐失败: %w", err)
	}
	hash, err := idKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword 校验口令。返回的 error 表示散列串本身有问题，或计算繁忙
// （ErrBusy）；口令不匹配通过 false 返回，调用方不应把两者区别对待地反馈给用户。
func VerifyPassword(password, encoded string) (bool, error) {
	params, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	got, err := idKey([]byte(password), salt, params.time, params.memory, params.threads, uint32(len(want)))
	if err != nil {
		return false, err
	}
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
