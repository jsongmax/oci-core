package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// 设置项的键名。用常量而非散落的字面量，避免拼写错误导致读到默认值却不报错。
const (
	SettingAllowTerminate       = "policy.allow_terminate"
	SettingAllowBulkActions     = "policy.allow_bulk_actions"
	SettingRequireTOTPForDanger = "policy.require_totp_for_danger"
	SettingSyncIntervalMinutes  = "sync.interval_minutes"
	SettingCheckIntervalHours   = "check.interval_hours"
	SettingAuditRetentionDays   = "audit.retention_days"
)

// Settings 是操作策略与运行参数。
//
// 这些开关的存在意义是让用户能按自己的风险偏好收紧默认行为。
// 默认值取"功能可用 + 有确认门槛"：真正的防线是 L3 输名确认，
// 而不是把功能直接关掉——那样只会逼用户去 Oracle 控制台操作，
// 反而绕过了本工具的审计日志。
type Settings struct {
	AllowTerminate       bool `json:"allowTerminate"`
	AllowBulkActions     bool `json:"allowBulkActions"`
	RequireTOTPForDanger bool `json:"requireTotpForDanger"`
	SyncIntervalMinutes  int  `json:"syncIntervalMinutes"`
	// CheckIntervalHours 是自动重跑凭据校验的间隔，0 表示关闭。
	//
	// 凭据会在面板不知情的情况下失效——密钥被轮换、IAM 用户被删、
	// 账号被 Oracle 封停。不定期复查的话，卡片上的"校验通过"可能是
	// 三天前的结论，而用户以为那是当前状态。
	CheckIntervalHours int `json:"checkIntervalHours"`
	// AuditRetentionDays 是审计日志的保留天数，0 表示永久保留。
	//
	// 默认 0。审计日志是安全设施，"谁在什么时候对哪个账号做了什么"
	// 一旦被自动删掉就再也追不回来了——这种事必须由用户显式选择，
	// 不能替他决定。存储压力也不构成理由：按几十行/天算，一年不过几 MB。
	AuditRetentionDays int `json:"auditRetentionDays"`
}

// DefaultSettings 返回出厂默认值。
func DefaultSettings() Settings {
	return Settings{
		AllowTerminate:       true,
		AllowBulkActions:     true,
		RequireTOTPForDanger: false,
		SyncIntervalMinutes:  5,
		// 6 小时：凭据失效是低频事件，每天四次足够及时。
		// 每次只发两个只读请求，对 API 配额和风控都无关痛痒。
		CheckIntervalHours: 6,
		// 0 = 永久保留。见字段注释。
		AuditRetentionDays: 0,
	}
}

// Settings 读取当前设置，未显式配置过的项使用默认值。
func (s *Store) Settings(ctx context.Context) (Settings, error) {
	out := DefaultSettings()

	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return out, fmt.Errorf("store: 读取设置失败: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return out, err
		}
		switch key {
		case SettingAllowTerminate:
			out.AllowTerminate = value == "true"
		case SettingAllowBulkActions:
			out.AllowBulkActions = value == "true"
		case SettingRequireTOTPForDanger:
			out.RequireTOTPForDanger = value == "true"
		case SettingSyncIntervalMinutes:
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				out.SyncIntervalMinutes = n
			}
		case SettingCheckIntervalHours:
			// 这里允许 0：0 是"关闭自动校验"，是个合法选择。
			if n, err := strconv.Atoi(value); err == nil && n >= 0 {
				out.CheckIntervalHours = n
			}
		case SettingAuditRetentionDays:
			if n, err := strconv.Atoi(value); err == nil && n >= 0 {
				out.AuditRetentionDays = n
			}
		}
	}
	return out, rows.Err()
}

// SettingsUpdate 描述要修改的设置项。nil 表示不改动。
type SettingsUpdate struct {
	AllowTerminate       *bool
	AllowBulkActions     *bool
	RequireTOTPForDanger *bool
	SyncIntervalMinutes  *int
	CheckIntervalHours   *int
	AuditRetentionDays   *int
}

// UpdateSettings 写入设置并返回更新后的完整配置。
func (s *Store) UpdateSettings(ctx context.Context, up SettingsUpdate) (Settings, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Settings{}, fmt.Errorf("store: 开启事务失败: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // 提交成功后回滚是无操作

	put := func(key, value string) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO settings (key, value, updated_at) VALUES (?,?,?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			key, value, nowUnix())
		return err
	}

	if up.AllowTerminate != nil {
		if err := put(SettingAllowTerminate, strconv.FormatBool(*up.AllowTerminate)); err != nil {
			return Settings{}, fmt.Errorf("store: 写入设置失败: %w", err)
		}
	}
	if up.AllowBulkActions != nil {
		if err := put(SettingAllowBulkActions, strconv.FormatBool(*up.AllowBulkActions)); err != nil {
			return Settings{}, fmt.Errorf("store: 写入设置失败: %w", err)
		}
	}
	if up.RequireTOTPForDanger != nil {
		if err := put(SettingRequireTOTPForDanger, strconv.FormatBool(*up.RequireTOTPForDanger)); err != nil {
			return Settings{}, fmt.Errorf("store: 写入设置失败: %w", err)
		}
	}
	if up.SyncIntervalMinutes != nil {
		n := *up.SyncIntervalMinutes
		// 下限 2 分钟。一轮全量同步会对每个（账号 × 区域）发一组请求——
		// 五个账号就是几十个调用，1 分钟一轮等于全天候压着 Oracle 打，
		// 而实时性本来就由 SSE 与生命周期轮询保证，全量同步只是兜底对账。
		// 下限放在这里而不是前端：前端能绕过，这里绕不过。
		if n < 2 || n > 1440 {
			return Settings{}, fmt.Errorf("同步间隔必须在 2–1440 分钟之间，当前 %d", n)
		}
		if err := put(SettingSyncIntervalMinutes, strconv.Itoa(n)); err != nil {
			return Settings{}, fmt.Errorf("store: 写入设置失败: %w", err)
		}
	}

	if up.CheckIntervalHours != nil {
		n := *up.CheckIntervalHours
		// 0 是"关闭"，是个合法选择，单独放行。
		// 其余下限 1 小时：凭据失效是低频事件，比这更密没有意义。
		// 上限一周：再长就等于没有，不如直接关掉说得明白。
		if n != 0 && (n < 1 || n > 168) {
			return Settings{}, fmt.Errorf("校验间隔必须是 0（关闭）或 1–168 小时，当前 %d", n)
		}
		if err := put(SettingCheckIntervalHours, strconv.Itoa(n)); err != nil {
			return Settings{}, fmt.Errorf("store: 写入设置失败: %w", err)
		}
	}

	if up.AuditRetentionDays != nil {
		n := *up.AuditRetentionDays
		// 下限 7 天：比这更短就等于没有审计了。0 是"永久保留"，单独放行。
		if n != 0 && (n < 7 || n > 3650) {
			return Settings{}, fmt.Errorf("审计保留天数必须是 0（永久）或 7–3650 天，当前 %d", n)
		}
		if err := put(SettingAuditRetentionDays, strconv.Itoa(n)); err != nil {
			return Settings{}, fmt.Errorf("store: 写入设置失败: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Settings{}, fmt.Errorf("store: 提交设置失败: %w", err)
	}
	return s.Settings(ctx)
}

// GetSettingJSON 读取一个任意结构的 JSON 设置项。
func (s *Store) GetSettingJSON(ctx context.Context, key string, dst any) (bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err != nil {
		return false, nil
	}
	if err := json.Unmarshal([]byte(value), dst); err != nil {
		return false, fmt.Errorf("store: 解析设置 %s 失败: %w", key, err)
	}
	return true, nil
}

// SetSettingJSON 写入一个任意结构的 JSON 设置项。
func (s *Store) SetSettingJSON(ctx context.Context, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("store: 序列化设置 %s 失败: %w", key, err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?,?,?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, string(data), nowUnix())
	if err != nil {
		return fmt.Errorf("store: 写入设置失败: %w", err)
	}
	return nil
}
