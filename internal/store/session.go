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

// ErrAuthStateChanged 表示签发会话时，用户的认证状态（口令或 TOTP）已与调用方读到的不同。
// 调用方应拒绝本次签发，让用户重新走认证流程。
var ErrAuthStateChanged = errors.New("store: 认证状态已变化")

// CreateSession 记录一个新会话。原始令牌只在这一刻存在于内存中，此后无法从库中还原。
//
// totpVerified 为 true 的会话只能签给当前**未启用** TOTP 的用户：判断与插入是同一条
// INSERT ... SELECT，SQLite 单条语句原子，不存在"先读用户再插入"的窗口。
// 已启用 TOTP 的用户只能拿到半登录会话，通过 MarkTOTPVerified 提升，
// 或在刚校验过验证码之后用 CreateTOTPVerifiedSession。
// 条件不满足（含用户不存在）时返回 ErrAuthStateChanged。
func (s *Store) CreateSession(ctx context.Context, userID, token, ip, userAgent string, ttl time.Duration, totpVerified bool) error {
	cond := `id = ?`
	args := []any{userID}
	if totpVerified {
		cond += ` AND totp_enabled = 0`
	}
	return s.insertSession(ctx, token, ip, userAgent, ttl, totpVerified, cond, args)
}

// CreateLoginSession 为一次口令登录签发会话，并要求用户状态仍与 snapshot 一致。
//
// 登录在 Argon2 校验之前读出用户，校验要花上百毫秒。这期间若用户启用了 TOTP 或改了口令，
// 按旧快照签发的会话就会绕过新状态（例如 totp_verified=1 却面对已启用 TOTP 的用户）。
// 这里把快照里的 totp_enabled 与 password_hash 一并写进插入条件，状态已变则返回 ErrAuthStateChanged。
func (s *Store) CreateLoginSession(ctx context.Context, snapshot *User, token, ip, userAgent string, ttl time.Duration) error {
	return s.insertSession(ctx, token, ip, userAgent, ttl, !snapshot.TOTPEnabled,
		`id = ? AND totp_enabled = ? AND password_hash = ?`,
		[]any{snapshot.ID, boolToInt(snapshot.TOTPEnabled), snapshot.PasswordHash})
}

// CreateTOTPVerifiedSession 为已启用 TOTP、且本次请求刚校验过一次新鲜验证码的用户签发完整会话。
// 用户此刻未启用 TOTP 时返回 ErrAuthStateChanged。
func (s *Store) CreateTOTPVerifiedSession(ctx context.Context, userID, token, ip, userAgent string, ttl time.Duration) error {
	return s.insertSession(ctx, token, ip, userAgent, ttl, true, `id = ? AND totp_enabled = 1`, []any{userID})
}

// insertSession 仅当 users 中存在满足 cond 的行时插入会话。
func (s *Store) insertSession(ctx context.Context, token, ip, userAgent string, ttl time.Duration, totpVerified bool, cond string, condArgs []any) error {
	now := time.Now()
	args := append([]any{hashToken(token), boolToInt(totpVerified), ip, userAgent, now.Unix(), now.Add(ttl).Unix()}, condArgs...)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, user_id, totp_verified, ip, user_agent, created_at, expires_at)
		SELECT ?, id, ?, ?, ?, ?, ? FROM users WHERE `+cond, args...)
	if err != nil {
		return fmt.Errorf("store: 创建会话失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAuthStateChanged
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
