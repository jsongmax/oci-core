package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Channel 是一个通知渠道。
//
// Config 用 map 而非具体结构体：各渠道需要的字段差异很大
// （Telegram 要 token 和 chatId，SMTP 要五六个字段），
// 让 notify 包各自解析比在这里定义一个巨大的联合结构清楚得多。
type Channel struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind"`
	Name       string            `json:"name"`
	Config     map[string]string `json:"config"`
	Events     []string          `json:"events"`
	Enabled    bool              `json:"enabled"`
	LastError  string            `json:"lastError"`
	LastSentAt *time.Time        `json:"lastSentAt"`
	CreatedAt  time.Time         `json:"createdAt"`
	UpdatedAt  time.Time         `json:"updatedAt"`
}

const channelColumns = `id, kind, name, config, events, enabled, last_error, last_sent_at, created_at, updated_at`

// NewChannel 是创建渠道的输入。
type NewChannel struct {
	Kind   string
	Name   string
	Config map[string]string
	Events []string
}

// ChannelUpdate 描述可修改的字段。nil 表示不改动。
type ChannelUpdate struct {
	Name    *string
	Config  map[string]string
	Events  []string
	Enabled *bool
}

// ListChannels 返回全部通知渠道。
func (s *Store) ListChannels(ctx context.Context) ([]Channel, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+channelColumns+` FROM notification_channels ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: 查询通知渠道失败: %w", err)
	}
	defer rows.Close()

	out := make([]Channel, 0, 8)
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ch)
	}
	return out, rows.Err()
}

// GetChannel 按 ID 取回渠道。
func (s *Store) GetChannel(ctx context.Context, id string) (*Channel, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+channelColumns+` FROM notification_channels WHERE id = ?`, id)
	ch, err := scanChannel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return ch, err
}

// CreateChannel 新建通知渠道。
func (s *Store) CreateChannel(ctx context.Context, in NewChannel) (*Channel, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, errors.New("渠道名称不能为空")
	}
	if strings.TrimSpace(in.Kind) == "" {
		return nil, errors.New("渠道类型不能为空")
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	configJSON, err := json.Marshal(orMap(in.Config))
	if err != nil {
		return nil, fmt.Errorf("store: 序列化渠道配置失败: %w", err)
	}
	eventsJSON, err := json.Marshal(orSlice(in.Events))
	if err != nil {
		return nil, fmt.Errorf("store: 序列化事件订阅失败: %w", err)
	}

	now := nowUnix()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO notification_channels (id, kind, name, config, events, enabled, created_at, updated_at)
		VALUES (?,?,?,?,?,1,?,?)`,
		id, in.Kind, strings.TrimSpace(in.Name), string(configJSON), string(eventsJSON), now, now)
	if err != nil {
		return nil, fmt.Errorf("store: 创建通知渠道失败: %w", err)
	}
	return s.GetChannel(ctx, id)
}

// UpdateChannel 修改渠道。
func (s *Store) UpdateChannel(ctx context.Context, id string, up ChannelUpdate) (*Channel, error) {
	if _, err := s.GetChannel(ctx, id); err != nil {
		return nil, err
	}

	sets := []string{"updated_at = ?"}
	args := []any{nowUnix()}

	if up.Name != nil {
		name := strings.TrimSpace(*up.Name)
		if name == "" {
			return nil, errors.New("渠道名称不能为空")
		}
		sets = append(sets, "name = ?")
		args = append(args, name)
	}
	if up.Config != nil {
		data, err := json.Marshal(up.Config)
		if err != nil {
			return nil, fmt.Errorf("store: 序列化渠道配置失败: %w", err)
		}
		sets = append(sets, "config = ?")
		args = append(args, string(data))
	}
	if up.Events != nil {
		data, err := json.Marshal(up.Events)
		if err != nil {
			return nil, fmt.Errorf("store: 序列化事件订阅失败: %w", err)
		}
		sets = append(sets, "events = ?")
		args = append(args, string(data))
	}
	if up.Enabled != nil {
		sets = append(sets, "enabled = ?")
		args = append(args, boolToInt(*up.Enabled))
	}

	args = append(args, id)
	_, err := s.db.ExecContext(ctx,
		`UPDATE notification_channels SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 更新通知渠道失败: %w", err)
	}
	return s.GetChannel(ctx, id)
}

// DeleteChannel 删除渠道。
func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM notification_channels WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: 删除通知渠道失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordChannelSend 记录一次发送结果。errMsg 为空表示成功。
func (s *Store) RecordChannelSend(ctx context.Context, id, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE notification_channels SET last_error = ?, last_sent_at = ? WHERE id = ?`,
		errMsg, nowUnix(), id)
	if err != nil {
		return fmt.Errorf("store: 记录发送结果失败: %w", err)
	}
	return nil
}

func scanChannel(sc rowScanner) (*Channel, error) {
	var (
		ch         Channel
		configJSON string
		eventsJSON string
		enabled    int
		lastSent   sql.NullInt64
		created    int64
		updated    int64
	)
	err := sc.Scan(&ch.ID, &ch.Kind, &ch.Name, &configJSON, &eventsJSON,
		&enabled, &ch.LastError, &lastSent, &created, &updated)
	if err != nil {
		return nil, err
	}

	ch.Config = map[string]string{}
	_ = json.Unmarshal([]byte(configJSON), &ch.Config)
	ch.Events = []string{}
	_ = json.Unmarshal([]byte(eventsJSON), &ch.Events)

	ch.Enabled = enabled != 0
	ch.LastSentAt = nullUnixToTime(lastSent)
	ch.CreatedAt = unixToTime(created)
	ch.UpdatedAt = unixToTime(updated)
	return &ch, nil
}

func orMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func orSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
