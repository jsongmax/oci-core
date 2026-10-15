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
// TOTPSecret 在库里是 AES-GCM 密文，这里是解密后的明文。
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

// ErrAlreadyInitialized 表示系统里已经有用户，首次设置不再可用。
var ErrAlreadyInitialized = errors.New("store: 系统已完成初始化")

// CreateFirstUser 仅在 users 表为空时创建用户，否则返回 ErrAlreadyInitialized。
//
// "检查为空"和"插入"必须是同一条语句：先 CountUsers 再 CreateUser 之间
// 隔着解析请求体和一次 Argon2，两个并发的首次设置都能通过检查，各自
// 插入一个管理员。SQLite 的单条写语句天然串行，多进程共用库文件也成立。
func (s *Store) CreateFirstUser(ctx context.Context, username, passwordHash string) (*User, error) {
	name := strings.TrimSpace(username)
	if name == "" {
		return nil, errors.New("用户名不能为空")
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := nowUnix()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, created_at, updated_at)
		SELECT ?,?,?,?,?
		WHERE NOT EXISTS (SELECT 1 FROM users)`, id, name, passwordHash, now, now)
	if err != nil {
		return nil, fmt.Errorf("store: 创建用户失败: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, fmt.Errorf("store: 创建用户失败: %w", err)
	} else if n == 0 {
		return nil, ErrAlreadyInitialized
	}
	return s.GetUser(ctx, id)
}

// GetUser 按 ID 取回用户。
func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	return s.scanUser(row)
}

// GetUserByUsername 按用户名取回用户。
func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, strings.TrimSpace(username))
	return s.scanUser(row)
}

// SetPassword 更新口令散列，并使该用户的所有会话失效——改密后旧会话必须下线。
func (s *Store) SetPassword(ctx context.Context, userID, passwordHash string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
			passwordHash, nowUnix(), userID); err != nil {
			return fmt.Errorf("store: 更新口令失败: %w", err)
		}
		return deleteUserSessionsTx(ctx, tx, userID)
	})
}

// inTx 在事务里执行 fn，fn 返回错误时回滚。
func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: 开启事务失败: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // 提交成功后回滚是无操作
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteUserSessionsTx(ctx context.Context, tx *sql.Tx, userID string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("store: 清理会话失败: %w", err)
	}
	return nil
}

// SetTOTPSecret 写入尚未启用的 TOTP 密钥。用户扫码并输入一次正确验证码后
// 才调用 EnableTOTP —— 避免密钥写进去了但用户其实没扫上，把自己锁在门外。
//
// 只对未启用 TOTP 的用户生效：已启用时直接覆盖密钥等于悄悄关掉了第二因子，
// 却没有走关闭流程该有的复核与会话撤销。重新绑定必须先 DisableTOTP。
// 密钥以密文落库，见 sealTOTPSecret。
func (s *Store) SetTOTPSecret(ctx context.Context, userID, secret string) error {
	sealed, err := s.sealTOTPSecret(userID, secret)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = ?, updated_at = ? WHERE id = ? AND totp_enabled = 0`,
		sealed, nowUnix(), userID)
	if err != nil {
		return fmt.Errorf("store: 写入 TOTP 密钥失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.GetUser(ctx, userID); err != nil {
			return err
		}
		return fmt.Errorf("%w: 两步验证已启用，请先关闭再重新绑定", ErrConflict)
	}
	return nil
}

// EnableTOTP 启用双因子，记录首个已使用的时间窗，并在同一事务里撤销该用户的全部会话。
//
// 启用前创建的会话只通过了口令（totp_verified 被直接置 1），若保留下来，
// 一个早已被盗的会话在用户加上第二因子之后依然畅通无阻。调用方需要为
// 当前请求重新签发会话。
func (s *Store) EnableTOTP(ctx context.Context, userID string, counter int64) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET totp_enabled = 1, totp_last_counter = ?, updated_at = ?
			 WHERE id = ? AND totp_secret != ''`,
			counter, nowUnix(), userID)
		if err != nil {
			return fmt.Errorf("store: 启用 TOTP 失败: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return deleteUserSessionsTx(ctx, tx, userID)
	})
}

// DisableTOTP 关闭双因子并清除密钥，同时消费验证码所在的时间窗并撤销该用户的全部会话。
//
// 三件事在同一事务里完成：
//   - 计数器检查与推进必须和状态变更原子，否则同一验证码可以在登录与关闭之间被用两次；
//     fresh 为 false 表示该时间窗已被用过，此时什么都不改。
//   - 尚未完成第二步的半登录会话在 TOTP 关闭后会被 requireAuth 直接视为完整会话，
//     必须一并清除。调用方需要为当前请求重新签发会话。
//
// 另外顺带关掉「危险操作需重新验证 TOTP」：没有第二因子，复核就无码可输，
// 开关留着只会把所有危险操作挡在一扇永远过不去的门外。放进同一事务，
// 免得 TOTP 关掉了、开关却因为中途失败还开着。
func (s *Store) DisableTOTP(ctx context.Context, userID string, counter int64) (fresh bool, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET totp_secret = '', totp_enabled = 0, totp_last_counter = 0, updated_at = ?
			 WHERE id = ? AND totp_enabled = 1 AND totp_last_counter < ?`,
			nowUnix(), userID, counter)
		if err != nil {
			return fmt.Errorf("store: 关闭 TOTP 失败: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		fresh = true
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO settings (key, value, updated_at) VALUES (?, 'false', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			SettingRequireTOTPForDanger, nowUnix()); err != nil {
			return fmt.Errorf("store: 关闭危险操作复核失败: %w", err)
		}
		return deleteUserSessionsTx(ctx, tx, userID)
	})
	return fresh, err
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

func (s *Store) scanUser(sc rowScanner) (*User, error) {
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
	// 密钥以密文落库。解不开时报错而不是当作"没有密钥"：
	// 后者在 TOTP 已启用时会让校验永远失败，掩盖了主密钥被换掉这个真正的原因。
	if u.TOTPSecret, err = s.openText(u.TOTPSecret, aadTOTPSecret+u.ID); err != nil {
		return nil, fmt.Errorf("store: 解密 TOTP 密钥失败（主密钥是否换过？）: %w", err)
	}
	u.TOTPEnabled = enabled != 0
	u.CreatedAt = unixToTime(created)
	u.UpdatedAt = unixToTime(updated)
	return &u, nil
}
