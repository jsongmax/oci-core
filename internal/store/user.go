package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// User 是面板的登录用户。P0 只支持单用户；多用户与 RBAC 在路线图后段。
//
// PasswordHash 与 TOTPSecret 会出现在这个结构里，因此它**不能**被直接序列化成
// API 响应。HTTP 层有独立的对外视图，见 httpapi 包。
type User struct {
	ID              string
	Username        string
	PasswordHash    string
	TOTPSecret      string
	TOTPEnabled     bool
	TOTPLastCounter int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

const userColumns = `id, username, password_hash, totp_secret, totp_enabled, totp_last_counter, created_at, updated_at`

// CountUsers 返回用户总数。为 0 时说明还没初始化，应引导用户走首次设置流程。
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: 统计用户失败: %w", err)
	}
	return n, nil
}

// CreateUser 创建用户。passwordHash 必须是 auth.HashPassword 的输出。
func (s *Store) CreateUser(ctx context.Context, username, passwordHash string) (*User, error) {
	name := strings.TrimSpace(username)
	if name == "" {
		return nil, errors.New("用户名不能为空")
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := nowUnix()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, created_at, updated_at)
		VALUES (?,?,?,?,?)`, id, name, passwordHash, now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: 用户名已存在", ErrConflict)
		}
		return nil, fmt.Errorf("store: 创建用户失败: %w", err)
	}
	return s.GetUser(ctx, id)
}

// GetUser 按 ID 取回用户。
func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	return scanUser(row)
}

// GetUserByUsername 按用户名取回用户。
func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, strings.TrimSpace(username))
	return scanUser(row)
}

// SetPassword 更新口令散列，并使该用户的所有会话失效——改密后旧会话必须下线。
func (s *Store) SetPassword(ctx context.Context, userID, passwordHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: 开启事务失败: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // 提交成功后回滚是无操作

	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, nowUnix(), userID); err != nil {
		return fmt.Errorf("store: 更新口令失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("store: 清理会话失败: %w", err)
	}
	return tx.Commit()
}

// SetTOTPSecret 写入尚未启用的 TOTP 密钥。用户扫码并输入一次正确验证码后
// 才调用 EnableTOTP —— 避免密钥写进去了但用户其实没扫上，把自己锁在门外。
func (s *Store) SetTOTPSecret(ctx context.Context, userID, secret string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = ?, totp_enabled = 0, updated_at = ? WHERE id = ?`,
		secret, nowUnix(), userID)
	if err != nil {
		return fmt.Errorf("store: 写入 TOTP 密钥失败: %w", err)
	}
	return nil
}

// EnableTOTP 启用双因子，并记录首个已使用的时间窗。
func (s *Store) EnableTOTP(ctx context.Context, userID string, counter int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_enabled = 1, totp_last_counter = ?, updated_at = ? WHERE id = ?`,
		counter, nowUnix(), userID)
	if err != nil {
		return fmt.Errorf("store: 启用 TOTP 失败: %w", err)
	}
	return nil
}

// DisableTOTP 关闭双因子并清除密钥。
func (s *Store) DisableTOTP(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = '', totp_enabled = 0, totp_last_counter = 0, updated_at = ? WHERE id = ?`,
		nowUnix(), userID)
	if err != nil {
		return fmt.Errorf("store: 关闭 TOTP 失败: %w", err)
	}
	return nil
}

// ConsumeTOTPCounter 原子地推进已使用的时间窗，用于阻止验证码重放。
//
// TOTP 码在 30 秒窗口内可以重复使用，仅靠算法校验挡不住重放；
// 只有记录"这个窗口已经用过了"才能真正一次性。
// 返回 false 表示该窗口（或更早的窗口）已被使用过，应拒绝本次登录。
func (s *Store) ConsumeTOTPCounter(ctx context.Context, userID string, counter int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_last_counter = ?, updated_at = ? WHERE id = ? AND totp_last_counter < ?`,
		counter, nowUnix(), userID, counter)
	if err != nil {
		return false, fmt.Errorf("store: 更新 TOTP 计数器失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func scanUser(sc rowScanner) (*User, error) {
	var (
		u       User
		enabled int
		created int64
		updated int64
	)
	err := sc.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.TOTPSecret, &enabled, &u.TOTPLastCounter, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 读取用户失败: %w", err)
	}
	u.TOTPEnabled = enabled != 0
	u.CreatedAt = unixToTime(created)
	u.UpdatedAt = unixToTime(updated)
	return &u, nil
}
