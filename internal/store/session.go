package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Session 是一次登录会话。
//
// TOTPVerified 为 false 表示只通过了口令这一因子，此时会话仅允许调用
// TOTP 校验接口，其余全部拒绝。这样"半登录"状态是显式的，不会漏判。
type Session struct {
	UserID       string
	TOTPVerified bool
	IP           string
	UserAgent    string
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

// hashToken 计算令牌指纹。库里只存指纹，即使数据库泄露也无法拼出可用的 Cookie。
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateSession 记录一个新会话，返回值即写入 Cookie 的原始令牌。
// 原始令牌只在这一刻存在于内存中，此后无法从库中还原。
func (s *Store) CreateSession(ctx context.Context, userID, token, ip, userAgent string, ttl time.Duration, totpVerified bool) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, user_id, totp_verified, ip, user_agent, created_at, expires_at)
		VALUES (?,?,?,?,?,?,?)`,
		hashToken(token), userID, boolToInt(totpVerified), ip, userAgent,
		now.Unix(), now.Add(ttl).Unix())
	if err != nil {
		return fmt.Errorf("store: 创建会话失败: %w", err)
	}
	return nil
}

// GetSession 按令牌取回会话。已过期的会话按不存在处理，并顺手清除。
func (s *Store) GetSession(ctx context.Context, token string) (*Session, error) {
	hash := hashToken(token)
	row := s.db.QueryRowContext(ctx, `
		SELECT user_id, totp_verified, ip, user_agent, created_at, expires_at
		FROM sessions WHERE token_hash = ?`, hash)

	var (
		sess     Session
		verified int
		created  int64
		expires  int64
	)
	err := row.Scan(&sess.UserID, &verified, &sess.IP, &sess.UserAgent, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 读取会话失败: %w", err)
	}

	if time.Now().Unix() >= expires {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hash)
		return nil, ErrNotFound
	}

	sess.TOTPVerified = verified != 0
	sess.CreatedAt = unixToTime(created)
	sess.ExpiresAt = unixToTime(expires)
	return &sess, nil
}

// MarkTOTPVerified 把半登录会话提升为完整会话。
func (s *Store) MarkTOTPVerified(ctx context.Context, token string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET totp_verified = 1 WHERE token_hash = ?`, hashToken(token))
	if err != nil {
		return fmt.Errorf("store: 更新会话失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchSession 延长会话有效期，实现滑动过期。
func (s *Store) TouchSession(ctx context.Context, token string, ttl time.Duration) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET expires_at = ? WHERE token_hash = ?`,
		time.Now().Add(ttl).Unix(), hashToken(token))
	if err != nil {
		return fmt.Errorf("store: 续期会话失败: %w", err)
	}
	return nil
}

// DeleteSession 注销单个会话。
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	if err != nil {
		return fmt.Errorf("store: 删除会话失败: %w", err)
	}
	return nil
}

// DeleteUserSessions 强制某用户全部下线。
func (s *Store) DeleteUserSessions(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("store: 清理用户会话失败: %w", err)
	}
	return nil
}
