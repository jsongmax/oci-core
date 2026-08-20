package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 任务状态。
const (
	HuntPending   = "pending"
	HuntRunning   = "running"
	HuntSucceeded = "succeeded"
	HuntFailed    = "failed"
	HuntPaused    = "paused"
)

// HuntTask 是一个"反复尝试创建实例直到成功"的后台任务。
//
// 针对的是 Oracle 免费额度里 ARM 规格长期没有容量这一现实：LaunchInstance
// 大概率返回容量不足，需要在容量释放的那一刻恰好发出请求。
type HuntTask struct {
	ID        string `json:"id"`
	AccountID string `json:"accountId"`
	Region    string `json:"region"`
	Name      string `json:"name"`

	// Spec 是创建参数的 JSON 快照。
	//
	// 存 JSON 而不是拆成列：创建参数有十几项且会随 OCI 演进，
	// 拆列意味着每加一个参数就要迁一次表。任务只在创建时快照一次，
	// 后续每轮原样重放——参数在任务生命周期内不变是刻意的，
	// 否则用户改了设置而任务还在用旧参数跑，行为无法解释。
	Spec string `json:"-"`

	// ADs 是轮换范围，逗号分隔。空表示"该区域全部可用域"。
	ADs string `json:"-"`

	State    string `json:"state"`
	Attempts int    `json:"attempts"`

	// IntervalSeconds 是两次尝试之间的基准间隔。
	IntervalSeconds int `json:"intervalSeconds"`

	// PrecheckCapacity 表示每轮先查容量报告，说没货就跳过、不发创建请求。
	//
	// 默认开。容量报告是只读接口，而 LaunchInstance 才是 Oracle 风控盯的那个——
	// 用一次只读换掉一次创建，是这个功能里性价比最高的一处。
	PrecheckCapacity bool `json:"precheckCapacity"`

	LastClass string    `json:"lastClass"`
	LastError string    `json:"lastError"`
	LastAD    string    `json:"lastAd"`
	LastTryAt time.Time `json:"lastTryAt"`
	NextAt    time.Time `json:"nextAt"`

	InstanceID string `json:"instanceId"`

	MaxAttempts int       `json:"maxAttempts"`
	ExpiresAt   time.Time `json:"expiresAt"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ADList 把逗号分隔的可用域拆成切片。
func (t *HuntTask) ADList() []string {
	if strings.TrimSpace(t.ADs) == "" {
		return nil
	}
	parts := strings.Split(t.ADs, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ErrHuntNotFound 表示任务不存在。
var ErrHuntNotFound = errors.New("任务不存在")

// CreateHuntTask 写入一个新任务。
func (s *Store) CreateHuntTask(ctx context.Context, t HuntTask) (*HuntTask, error) {
	if t.ID == "" {
		id, err := newID()
		if err != nil {
			return nil, err
		}
		t.ID = id
	}
	if t.State == "" {
		t.State = HuntRunning
	}
	now := nowUnix()

	precheck := 0
	if t.PrecheckCapacity {
		precheck = 1
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO hunt_tasks
			(id, account_id, region, name, spec, ads, state, attempts, interval_seconds,
			 precheck_capacity, last_class, last_error, last_ad, last_try_at, next_at,
			 instance_id, max_attempts, expires_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,0,?,?,'','','',0,?,'',?,?,?,?)`,
		t.ID, t.AccountID, t.Region, t.Name, t.Spec, t.ADs, t.State, t.IntervalSeconds,
		precheck,
		// next_at 设成"现在"：新建的任务应当立刻试一次，让用户马上看到反馈，
		// 而不是先等一个间隔——那会让人以为没跑起来。
		now, t.MaxAttempts, timeToUnix(t.ExpiresAt), now, now)
	if err != nil {
		return nil, fmt.Errorf("store: 创建守候任务失败: %w", err)
	}
	return s.GetHuntTask(ctx, t.ID)
}

const huntColumns = `id, account_id, region, name, spec, ads, state, attempts,
	interval_seconds, precheck_capacity, last_class, last_error, last_ad, last_try_at,
	next_at, instance_id, max_attempts, expires_at, created_at, updated_at`

func scanHunt(row interface{ Scan(...any) error }) (*HuntTask, error) {
	var (
		t                                        HuntTask
		precheck                                 int
		lastTry, next, expires, created, updated int64
	)
	err := row.Scan(&t.ID, &t.AccountID, &t.Region, &t.Name, &t.Spec, &t.ADs,
		&t.State, &t.Attempts, &t.IntervalSeconds, &precheck, &t.LastClass, &t.LastError,
		&t.LastAD, &lastTry, &next, &t.InstanceID, &t.MaxAttempts, &expires,
		&created, &updated)
	if err != nil {
		return nil, err
	}
	t.PrecheckCapacity = precheck != 0
	t.LastTryAt = unixToTime(lastTry)
	t.NextAt = unixToTime(next)
	t.ExpiresAt = unixToTime(expires)
	t.CreatedAt = unixToTime(created)
	t.UpdatedAt = unixToTime(updated)
	return &t, nil
}

// GetHuntTask 按 ID 读取。
func (s *Store) GetHuntTask(ctx context.Context, id string) (*HuntTask, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+huntColumns+` FROM hunt_tasks WHERE id = ?`, id)
	t, err := scanHunt(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrHuntNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 读取守候任务失败: %w", err)
	}
	return t, nil
}

// ListHuntTasks 返回全部任务，最新创建的在前。
func (s *Store) ListHuntTasks(ctx context.Context) ([]HuntTask, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+huntColumns+` FROM hunt_tasks ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: 查询守候任务失败: %w", err)
	}
	defer rows.Close()

	// 空切片而不是 nil：nil 会序列化成 JSON null，前端 .forEach 直接抛异常。
	out := make([]HuntTask, 0)
	for rows.Next() {
		t, err := scanHunt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// DueHuntTasks 返回到点该尝试的任务，按 next_at 升序，最多 limit 条。
//
// 调度器只认这一个查询：所有"该不该动手"的判断都落在 next_at 上，
// 而 next_at 是持久化的——进程重启后退避进度原样恢复。
// 只存内存的话，重启一次就把退避清零，等于变相提高了请求频率。
func (s *Store) DueHuntTasks(ctx context.Context, now time.Time, limit int) ([]HuntTask, error) {
	if limit <= 0 {
		limit = 4
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+huntColumns+` FROM hunt_tasks
		WHERE state = ? AND next_at <= ?
		ORDER BY next_at ASC LIMIT ?`, HuntRunning, now.Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("store: 查询到期任务失败: %w", err)
	}
	defer rows.Close()

	out := make([]HuntTask, 0, limit)
	for rows.Next() {
		t, err := scanHunt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// HuntAttempt 描述一次尝试的结果。
type HuntAttempt struct {
	Class  string
	Error  string
	AD     string
	NextAt time.Time
	// State 非空时同时切换任务状态。
	State      string
	InstanceID string
}

// RecordHuntAttempt 记录一次尝试并推进任务状态。
func (s *Store) RecordHuntAttempt(ctx context.Context, id string, a HuntAttempt) error {
	now := nowUnix()
	sets := []string{
		"attempts = attempts + 1",
		"last_class = ?", "last_error = ?", "last_ad = ?",
		"last_try_at = ?", "next_at = ?", "updated_at = ?",
	}
	args := []any{a.Class, a.Error, a.AD, now, timeToUnix(a.NextAt), now}

	if a.State != "" {
		sets = append(sets, "state = ?")
		args = append(args, a.State)
	}
	if a.InstanceID != "" {
		sets = append(sets, "instance_id = ?")
		args = append(args, a.InstanceID)
	}
	args = append(args, id)

	res, err := s.db.ExecContext(ctx,
		`UPDATE hunt_tasks SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("store: 记录尝试结果失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrHuntNotFound
	}
	return nil
}

// SetHuntState 切换任务状态（暂停 / 恢复）。
//
// 恢复时把 next_at 拨到"现在"：用户点恢复就是想立刻看到动静，
// 让他对着一个几分钟的退避倒计时干等没有意义。
func (s *Store) SetHuntState(ctx context.Context, id, state string) (*HuntTask, error) {
	next := "next_at"
	if state == HuntRunning {
		next = "?"
	}
	args := []any{state}
	if state == HuntRunning {
		args = append(args, nowUnix())
	}
	args = append(args, nowUnix(), id)

	res, err := s.db.ExecContext(ctx,
		`UPDATE hunt_tasks SET state = ?, next_at = `+next+`, updated_at = ? WHERE id = ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 更新任务状态失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrHuntNotFound
	}
	return s.GetHuntTask(ctx, id)
}

// DeferHuntTasksForAccount 把某个账号下所有运行中的任务推迟到指定时刻。
//
// 429 是账号级信号，不是任务级的：只把当前任务退避、同账号的其他任务照跑，
// 等于没退——Oracle 看到的仍然是同一个账号在持续高频请求。
func (s *Store) DeferHuntTasksForAccount(ctx context.Context, accountID string, until time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE hunt_tasks SET next_at = ?, updated_at = ?
		WHERE account_id = ? AND state = ? AND next_at < ?`,
		until.Unix(), nowUnix(), accountID, HuntRunning, until.Unix())
	if err != nil {
		return fmt.Errorf("store: 账号级降速失败: %w", err)
	}
	return nil
}

// DeleteHuntTask 删除任务。
func (s *Store) DeleteHuntTask(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM hunt_tasks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: 删除守候任务失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrHuntNotFound
	}
	return nil
}

// CountActiveHuntTasks 统计某账号下运行中的任务数。
//
// 用来限制"单账号同时只能有一个任务在跑"：并行只会成倍放大请求量，
// 而容量是账号级共享的，跑两个任务并不会更快抢到。
func (s *Store) CountActiveHuntTasks(ctx context.Context, accountID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM hunt_tasks WHERE account_id = ? AND state IN (?, ?)`,
		accountID, HuntRunning, HuntPending).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: 统计运行中任务失败: %w", err)
	}
	return n, nil
}

func timeToUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
