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

// 后台任务状态。
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCanceled  = "canceled"
	// JobInterrupted 表示进程在任务跑到一半时退出了。它不等于失败：
	// 请求可能已经发到 Oracle，实际结果要去对应页面确认。
	JobInterrupted = "interrupted"
)

// JobFinished 报告状态是否已是终态。
func JobFinished(status string) bool {
	switch status {
	case JobSucceeded, JobFailed, JobCanceled, JobInterrupted:
		return true
	}
	return false
}

// JobStep 是任务过程中的一条进度记录。
type JobStep struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// Job 是一次在后台执行的长耗时操作（创建实例、换 IP、改配置……）。
//
// 和 HuntTask 不是一回事：守候任务是无限重试的调度项，Job 是一次性的，
// 跑完就进入终态，留在列表里供回看。
type Job struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	AccountID  string `json:"accountId,omitempty"`
	InstanceID string `json:"instanceId,omitempty"`

	Status   string    `json:"status"`
	Progress int       `json:"progress"`
	Steps    []JobStep `json:"steps"`
	// Summary 是成功后给人看的一句结论，Error 是失败原因。
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`
	// Result 是任务的结构化结果，原样转给前端（如换 IP 后的新旧地址）。
	Result json.RawMessage `json:"result,omitempty"`

	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// ErrJobNotFound 表示任务不存在。
var ErrJobNotFound = errors.New("任务不存在")

const jobColumns = `id, kind, title, account_id, instance_id, status, progress, steps,
	summary, error, result, created_at, started_at, finished_at`

// SaveJob 写入或覆盖一条任务。任务的全部字段都由 jobsvc 在内存里维护，
// 这里整行覆盖即可，不做字段级更新。
func (s *Store) SaveJob(ctx context.Context, j *Job) error {
	if j.ID == "" {
		id, err := newID()
		if err != nil {
			return err
		}
		j.ID = id
	}
	steps, err := json.Marshal(j.Steps)
	if err != nil {
		return fmt.Errorf("store: 序列化任务步骤失败: %w", err)
	}
	var result any
	if len(j.Result) > 0 {
		result = string(j.Result)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO jobs (`+jobColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status, progress = excluded.progress, steps = excluded.steps,
			summary = excluded.summary, error = excluded.error, result = excluded.result,
			started_at = excluded.started_at, finished_at = excluded.finished_at`,
		j.ID, j.Kind, j.Title, j.AccountID, j.InstanceID, j.Status, j.Progress, string(steps),
		j.Summary, j.Error, result, j.CreatedAt.Unix(), optUnix(j.StartedAt), optUnix(j.FinishedAt))
	if err != nil {
		return fmt.Errorf("store: 保存任务失败: %w", err)
	}
	return nil
}

func optUnix(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}

func scanJob(row interface{ Scan(...any) error }) (*Job, error) {
	var (
		j                 Job
		steps             string
		result            sql.NullString
		created           int64
		started, finished sql.NullInt64
	)
	err := row.Scan(&j.ID, &j.Kind, &j.Title, &j.AccountID, &j.InstanceID, &j.Status,
		&j.Progress, &steps, &j.Summary, &j.Error, &result, &created, &started, &finished)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(steps), &j.Steps); err != nil || j.Steps == nil {
		j.Steps = []JobStep{}
	}
	if result.Valid && result.String != "" {
		j.Result = json.RawMessage(result.String)
	}
	j.CreatedAt = unixToTime(created)
	j.StartedAt = nullUnixToTime(started)
	j.FinishedAt = nullUnixToTime(finished)
	return &j, nil
}

// GetJob 按 ID 读取。
func (s *Store) GetJob(ctx context.Context, id string) (*Job, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrJobNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 读取任务失败: %w", err)
	}
	return j, nil
}

// ListJobs 返回最近的任务，最新的在前。activeOnly 只返回未结束的。
func (s *Store) ListJobs(ctx context.Context, activeOnly bool, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 200
	}
	query := `SELECT ` + jobColumns + ` FROM jobs`
	args := []any{}
	if activeOnly {
		query += ` WHERE status IN (?, ?)`
		args = append(args, JobQueued, JobRunning)
	}
	query += ` ORDER BY created_at DESC, rowid DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询任务失败: %w", err)
	}
	defer rows.Close()

	out := make([]Job, 0)
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// DeleteJob 删除一条已结束的任务。进行中的任务不允许删，返回 ErrConflict。
func (s *Store) DeleteJob(ctx context.Context, id string) error {
	j, err := s.GetJob(ctx, id)
	if err != nil {
		return err
	}
	if !JobFinished(j.Status) {
		return ErrConflict
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: 删除任务失败: %w", err)
	}
	return nil
}

// DeleteFinishedJobs 清空所有已结束的任务，返回删除条数。
func (s *Store) DeleteFinishedJobs(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM jobs WHERE status NOT IN (?, ?)`, JobQueued, JobRunning)
	if err != nil {
		return 0, fmt.Errorf("store: 清理任务失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// InterruptUnfinishedJobs 把上次进程留下的未结束任务标成中断。
//
// 内存里的协程随进程一起没了，不改的话这些行会永远显示「进行中」。
// 启动时调用一次。
func (s *Store) InterruptUnfinishedJobs(ctx context.Context, reason string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET status = ?, error = ?, finished_at = ?
		WHERE status IN (?, ?)`,
		JobInterrupted, reason, nowUnix(), JobQueued, JobRunning)
	if err != nil {
		return 0, fmt.Errorf("store: 标记中断任务失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PruneJobs 只保留最近 keep 条已结束的任务。
//
// 任务列表是给人回看的，不是审计日志——审计另有一份。不设上限的话，
// 每次批量操作都会多一行，一年下来列表就没法看了。
func (s *Store) PruneJobs(ctx context.Context, keep int) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM jobs WHERE status NOT IN (?, ?) AND id NOT IN (
			SELECT id FROM jobs WHERE status NOT IN (?, ?)
			ORDER BY created_at DESC LIMIT ?)`,
		JobQueued, JobRunning, JobQueued, JobRunning, keep)
	if err != nil && !strings.Contains(err.Error(), "no such table") {
		return fmt.Errorf("store: 清理旧任务失败: %w", err)
	}
	return nil
}
