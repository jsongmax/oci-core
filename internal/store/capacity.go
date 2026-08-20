package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CapacityWatch 是一个容量监控项：盯住「某账号 × 某可用域 × 某规格」有没有货。
//
// 数据来自 Oracle 官方的容量报告接口，只读，不创建任何资源。
type CapacityWatch struct {
	ID                 string  `json:"id"`
	AccountID          string  `json:"accountId"`
	Region             string  `json:"region"`
	AvailabilityDomain string  `json:"availabilityDomain"`
	Shape              string  `json:"shape"`
	Ocpus              float64 `json:"ocpus"`
	MemoryGB           float64 `json:"memoryGb"`
	Enabled            bool    `json:"enabled"`

	LastStatus string `json:"lastStatus"`
	LastCount  int64  `json:"lastCount"`
	LastError  string `json:"lastError"`
	// LastCheckedAt 是最近查询的时刻；LastChangedAt 是状态最近一次真正变化的时刻。
	// 通知只看后者——天天推"还是没货"没有意义。
	LastCheckedAt time.Time `json:"lastCheckedAt"`
	LastChangedAt time.Time `json:"lastChangedAt"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ErrCapacityWatchNotFound 表示监控项不存在。
var ErrCapacityWatchNotFound = errors.New("容量监控项不存在")

const capacityColumns = `id, account_id, region, availability_domain, shape, ocpus,
	memory_gb, enabled, last_status, last_count, last_error, last_checked_at,
	last_changed_at, created_at, updated_at`

func scanCapacity(row interface{ Scan(...any) error }) (*CapacityWatch, error) {
	var (
		w                                 CapacityWatch
		enabled                           int
		checked, changed, created, update int64
	)
	err := row.Scan(&w.ID, &w.AccountID, &w.Region, &w.AvailabilityDomain, &w.Shape,
		&w.Ocpus, &w.MemoryGB, &enabled, &w.LastStatus, &w.LastCount, &w.LastError,
		&checked, &changed, &created, &update)
	if err != nil {
		return nil, err
	}
	w.Enabled = enabled != 0
	w.LastCheckedAt = unixToTime(checked)
	w.LastChangedAt = unixToTime(changed)
	w.CreatedAt = unixToTime(created)
	w.UpdatedAt = unixToTime(update)
	return &w, nil
}

// CreateCapacityWatch 新建一个监控项。
//
// 同一组（账号 × 可用域 × 规格）重复添加时不报错，直接返回已有的那条：
// 用户重复点"添加"是很自然的动作，为此弹一个错误没有意义。
func (s *Store) CreateCapacityWatch(ctx context.Context, w CapacityWatch) (*CapacityWatch, error) {
	if w.ID == "" {
		id, err := newID()
		if err != nil {
			return nil, err
		}
		w.ID = id
	}
	now := nowUnix()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO capacity_watches
			(id, account_id, region, availability_domain, shape, ocpus, memory_gb,
			 enabled, last_status, last_count, last_error, last_checked_at,
			 last_changed_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,1,'',0,'',0,0,?,?)
		ON CONFLICT(account_id, availability_domain, shape, ocpus, memory_gb)
		DO UPDATE SET enabled = 1, updated_at = excluded.updated_at`,
		w.ID, w.AccountID, w.Region, w.AvailabilityDomain, w.Shape, w.Ocpus, w.MemoryGB,
		now, now)
	if err != nil {
		return nil, fmt.Errorf("store: 创建容量监控失败: %w", err)
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT `+capacityColumns+` FROM capacity_watches
		WHERE account_id = ? AND availability_domain = ? AND shape = ?
		  AND ocpus = ? AND memory_gb = ?`,
		w.AccountID, w.AvailabilityDomain, w.Shape, w.Ocpus, w.MemoryGB)
	got, err := scanCapacity(row)
	if err != nil {
		return nil, fmt.Errorf("store: 读取容量监控失败: %w", err)
	}
	return got, nil
}

// ListCapacityWatches 返回全部监控项。
func (s *Store) ListCapacityWatches(ctx context.Context) ([]CapacityWatch, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+capacityColumns+` FROM capacity_watches ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: 查询容量监控失败: %w", err)
	}
	defer rows.Close()

	// 空切片而不是 nil：nil 序列化成 JSON null，前端 .forEach 直接抛异常。
	out := make([]CapacityWatch, 0)
	for rows.Next() {
		w, err := scanCapacity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// GetCapacityWatch 按 ID 读取。
func (s *Store) GetCapacityWatch(ctx context.Context, id string) (*CapacityWatch, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+capacityColumns+` FROM capacity_watches WHERE id = ?`, id)
	w, err := scanCapacity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCapacityWatchNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 读取容量监控失败: %w", err)
	}
	return w, nil
}

// DueCapacityWatches 返回该复查的监控项，最旧的优先。
func (s *Store) DueCapacityWatches(ctx context.Context, before time.Time, limit int) ([]CapacityWatch, error) {
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+capacityColumns+` FROM capacity_watches
		WHERE enabled = 1 AND last_checked_at <= ?
		ORDER BY last_checked_at ASC LIMIT ?`, before.Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("store: 查询到期容量监控失败: %w", err)
	}
	defer rows.Close()

	out := make([]CapacityWatch, 0, limit)
	for rows.Next() {
		w, err := scanCapacity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// RecordCapacityResult 写回一次查询结果，返回状态是否发生了变化。
//
// "是否变化"由这里判定而不是交给调用方：判定要和写入在同一次操作里完成，
// 否则两个并发的查询都会认为自己是"第一个看到变化的人"，通知就重了。
func (s *Store) RecordCapacityResult(ctx context.Context, id, status string, count int64, errMsg string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: 开启事务失败: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // 提交成功后回滚是无操作

	var prev string
	if err := tx.QueryRowContext(ctx,
		`SELECT last_status FROM capacity_watches WHERE id = ?`, id).Scan(&prev); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrCapacityWatchNotFound
		}
		return false, fmt.Errorf("store: 读取上次状态失败: %w", err)
	}

	changed := prev != status && status != ""
	now := nowUnix()

	sets := "last_status = ?, last_count = ?, last_error = ?, last_checked_at = ?, updated_at = ?"
	args := []any{status, count, errMsg, now, now}
	if changed {
		sets += ", last_changed_at = ?"
		args = append(args, now)
	}
	args = append(args, id)

	if _, err := tx.ExecContext(ctx,
		`UPDATE capacity_watches SET `+sets+` WHERE id = ?`, args...); err != nil {
		return false, fmt.Errorf("store: 写回容量结果失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: 提交容量结果失败: %w", err)
	}

	// 首次查询（prev 为空）不算"变化"：那只是第一次拿到数据，
	// 不是状态发生了转变。为它推一条"有容量了"的通知是误报。
	return changed && prev != "", nil
}

// SetCapacityWatchEnabled 启用/停用一个监控项。
func (s *Store) SetCapacityWatchEnabled(ctx context.Context, id string, enabled bool) (*CapacityWatch, error) {
	v := 0
	if enabled {
		v = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE capacity_watches SET enabled = ?, updated_at = ? WHERE id = ?`,
		v, nowUnix(), id)
	if err != nil {
		return nil, fmt.Errorf("store: 更新容量监控失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrCapacityWatchNotFound
	}
	return s.GetCapacityWatch(ctx, id)
}

// DeleteCapacityWatch 删除监控项。
func (s *Store) DeleteCapacityWatch(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM capacity_watches WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: 删除容量监控失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCapacityWatchNotFound
	}
	return nil
}

// TouchCapacityWatch 只更新查询时刻，用于查询失败时把它排到队尾，
// 避免一个坏掉的监控项每轮都被优先取出来重试。
func (s *Store) TouchCapacityWatch(ctx context.Context, id, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE capacity_watches SET last_checked_at = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		nowUnix(), errMsg, nowUnix(), id)
	if err != nil {
		return fmt.Errorf("store: 更新查询时刻失败: %w", err)
	}
	return nil
}
