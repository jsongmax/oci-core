package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// 审计结果。
const (
	ResultOK   = "ok"
	ResultFail = "fail"
)

// AuditEntry 是一条操作审计记录。
//
// 这个面板持有所有租户的完整控制权，"谁在什么时候对哪个账号做了什么"
// 必须可追溯。审计日志只增不改，UI 上也不提供删除入口。
type AuditEntry struct {
	ID        int64     `json:"id"`
	UserID    string    `json:"userId"`
	Action    string    `json:"action"`
	AccountID string    `json:"accountId"`
	Target    string    `json:"target"`
	Detail    string    `json:"detail"`
	IP        string    `json:"ip"`
	Result    string    `json:"result"`
	CreatedAt time.Time `json:"createdAt"`
}

// Audit 写入一条审计记录。
//
// 刻意不返回 error 之外的东西，也不让调用方阻塞在这里：审计写失败不应
// 中断用户的正常操作，但必须能被上层记录到日志里。
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	if e.Result == "" {
		e.Result = ResultOK
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_logs (user_id, action, account_id, target, detail, ip, result, created_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		e.UserID, e.Action, e.AccountID, e.Target, e.Detail, e.IP, e.Result, nowUnix())
	if err != nil {
		return fmt.Errorf("store: 写入审计日志失败: %w", err)
	}
	return nil
}

// AuditFilter 是审计查询条件。零值表示不过滤。
type AuditFilter struct {
	UserID    string
	AccountID string
	Action    string
	Limit     int
	// BeforeID 只返回 id 小于它的记录，用于翻页。
	//
	// 用游标而不是 OFFSET：审计表持续写入，OFFSET 分页会在翻页过程中
	// 因为新记录插到前面而漏掉或重复条目。id 是自增主键，天然单调。
	BeforeID int64
}

// ListAudit 按时间倒序返回审计记录。
//
// hasMore 表示比返回的最后一条更旧的记录还有——靠多取一条判断，
// 免得为了这个信息再打一次 COUNT(*)。
func (s *Store) ListAudit(ctx context.Context, f AuditFilter) ([]AuditEntry, bool, error) {
	where := []string{"1=1"}
	args := []any{}
	if f.UserID != "" {
		where = append(where, "user_id = ?")
		args = append(args, f.UserID)
	}
	if f.AccountID != "" {
		where = append(where, "account_id = ?")
		args = append(args, f.AccountID)
	}
	if f.Action != "" {
		where = append(where, "action = ?")
		args = append(args, f.Action)
	}
	if f.BeforeID > 0 {
		where = append(where, "id < ?")
		args = append(args, f.BeforeID)
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	// 多取一条用来判断还有没有下一页，返回前再丢掉。
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, action, account_id, target, detail, ip, result, created_at
		FROM audit_logs WHERE `+strings.Join(where, " AND ")+`
		ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, false, fmt.Errorf("store: 查询审计日志失败: %w", err)
	}
	defer rows.Close()

	entries := make([]AuditEntry, 0, limit)
	for rows.Next() {
		var (
			e       AuditEntry
			created int64
		)
		if err := rows.Scan(&e.ID, &e.UserID, &e.Action, &e.AccountID, &e.Target,
			&e.Detail, &e.IP, &e.Result, &created); err != nil {
			return nil, false, err
		}
		e.CreatedAt = unixToTime(created)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}
	return entries, hasMore, nil
}

// EachAudit 按 id 倒序遍历全部匹配记录。
//
// 导出用。刻意不复用 ListAudit：那条路径有 500 上限，而导出必须是全量——
// 一个只导出前 200 条却叫「导出 CSV」的按钮比没有这个按钮更糟。
// 逐行回调而不是先攒成切片，避免几万条记录一次性堆在内存里。
func (s *Store) EachAudit(ctx context.Context, f AuditFilter, fn func(AuditEntry) error) error {
	where := []string{"1=1"}
	args := []any{}
	if f.UserID != "" {
		where = append(where, "user_id = ?")
		args = append(args, f.UserID)
	}
	if f.AccountID != "" {
		where = append(where, "account_id = ?")
		args = append(args, f.AccountID)
	}
	if f.Action != "" {
		where = append(where, "action = ?")
		args = append(args, f.Action)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, action, account_id, target, detail, ip, result, created_at
		FROM audit_logs WHERE `+strings.Join(where, " AND ")+`
		ORDER BY id DESC`, args...)
	if err != nil {
		return fmt.Errorf("store: 导出审计日志失败: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			e       AuditEntry
			created int64
		)
		if err := rows.Scan(&e.ID, &e.UserID, &e.Action, &e.AccountID, &e.Target,
			&e.Detail, &e.IP, &e.Result, &created); err != nil {
			return err
		}
		e.CreatedAt = unixToTime(created)
		if err := fn(e); err != nil {
			return err
		}
	}
	return rows.Err()
}

// CountAudit 返回匹配条件的总记录数。
func (s *Store) CountAudit(ctx context.Context, f AuditFilter) (int64, error) {
	where := []string{"1=1"}
	args := []any{}
	if f.AccountID != "" {
		where = append(where, "account_id = ?")
		args = append(args, f.AccountID)
	}
	if f.Action != "" {
		where = append(where, "action = ?")
		args = append(args, f.Action)
	}
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_logs WHERE `+strings.Join(where, " AND "), args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: 统计审计日志失败: %w", err)
	}
	return n, nil
}

// PruneAudit 删除 cutoff 之前的审计记录，返回删除条数。
//
// 审计日志本身是安全设施，默认不删——保留策略必须由用户显式开启。
// 这个函数只在设置里配了保留天数时才会被调用。
func (s *Store) PruneAudit(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM audit_logs WHERE created_at < ?`, cutoff.Unix())
	if err != nil {
		return 0, fmt.Errorf("store: 清理审计日志失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return n, nil
}
