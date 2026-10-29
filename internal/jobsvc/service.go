// Package jobsvc 把长耗时操作放到后台执行，并记录进度供任务列表页查看。
//
// 创建实例、换公网 IP、改配置这类操作要几十秒到几分钟，以前是请求一直挂着，
// 界面上只能干等。现在 HTTP 层只做参数与权限校验，真正的工作交给这里：
// 立刻返回一条任务，进度经 SSE 推给前端，结果落库，刷新页面也看得到。
package jobsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

const (
	// maxConcurrent 是同时执行的任务数上限。
	//
	// 多数任务集中在少数几个租户上，放得太开容易把账号打进 OCI 限流，
	// 连带拖慢用户手动执行的其他操作。多出来的任务排队。
	maxConcurrent = 4
	// defaultTimeout 是没有指定超时的任务的上限。
	defaultTimeout = 10 * time.Minute
	// keepFinished 是保留的已结束任务条数。
	keepFinished = 300
)

// ErrNotCancelable 表示任务已经开始执行，不能取消。
//
// 只允许取消排队中的任务：执行到一半的操作（比如换 IP 已经删掉旧地址、
// 还没分配新地址）中途打断，会把资源留在比失败更糟的状态。
var ErrNotCancelable = errors.New("任务已经开始执行，不能取消")

// Spec 描述一个要提交的任务。
type Spec struct {
	Kind       string
	Title      string
	AccountID  string
	InstanceID string
	// Timeout 为 0 时用 defaultTimeout。
	Timeout time.Duration
}

// Outcome 是任务成功时的产出。
type Outcome struct {
	Summary string
	Result  any
}

// Func 是任务体。返回 error 即失败，error 的文字会展示给用户。
type Func func(ctx context.Context, p *Progress) (Outcome, error)

// Deps 是构造 Service 所需的依赖。
type Deps struct {
	Store *store.Store
	// OnChange 在任务每次变化后调用，用于推送 SSE。可为 nil。
	OnChange func(job store.Job)
}

// Service 管理后台任务。
type Service struct {
	st       *store.Store
	onChange func(job store.Job)
	sem      chan struct{}
	wg       sync.WaitGroup

	mu sync.Mutex
	// pending 是排队中任务的取消函数。
	pending map[string]context.CancelFunc
	// keyLocks 让同一台实例上的任务串行执行：同时改配置又换 IP，
	// 两个请求互相看到对方的中间状态，结果没法解释。
	keyLocks map[string]*keyLock
}

type keyLock struct {
	ch   chan struct{}
	refs int
}

// New 构造任务服务。
func New(deps Deps) *Service {
	return &Service{
		st:       deps.Store,
		onChange: deps.OnChange,
		sem:      make(chan struct{}, maxConcurrent),
		pending:  make(map[string]context.CancelFunc),
		keyLocks: make(map[string]*keyLock),
	}
}

// Recover 在启动时把上次进程留下的未结束任务标成中断，并清理过旧的记录。
func (s *Service) Recover(ctx context.Context) {
	n, err := s.st.InterruptUnfinishedJobs(ctx,
		"服务重启，任务中断。请求可能已经发到 Oracle，请到对应页面确认实际状态。")
	if err != nil {
		slog.Warn("标记中断任务失败", "err", err)
	} else if n > 0 {
		slog.Info("已标记重启前未完成的任务", "count", n)
	}
	if err := s.st.PruneJobs(ctx, keepFinished); err != nil {
		slog.Warn("清理旧任务失败", "err", err)
	}
}

// Submit 提交一个任务并立刻返回。任务在后台执行，不受调用方 context 影响——
// 用户点完就关掉页面是正常的，操作必须照样做完。
func (s *Service) Submit(spec Spec, fn Func) (*store.Job, error) {
	job := &store.Job{
		Kind:       spec.Kind,
		Title:      spec.Title,
		AccountID:  spec.AccountID,
		InstanceID: spec.InstanceID,
		Status:     store.JobQueued,
		Steps:      []store.JobStep{},
		CreatedAt:  time.Now(),
	}
	if err := s.st.SaveJob(context.Background(), job); err != nil {
		return nil, err
	}

	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	s.pending[job.ID] = cancel
	s.mu.Unlock()

	snapshot := *job
	s.publish(&snapshot)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run(ctx, cancel, job, spec.InstanceID, timeout, fn)
	}()
	return &snapshot, nil
}

// Wait 阻塞到所有已提交的任务结束。供测试与优雅退出使用。
func (s *Service) Wait() {
	s.wg.Wait()
}

// Cancel 取消一个排队中的任务。
func (s *Service) Cancel(ctx context.Context, id string) error {
	s.mu.Lock()
	cancel, ok := s.pending[id]
	s.mu.Unlock()
	if ok {
		cancel()
		return nil
	}
	job, err := s.st.GetJob(ctx, id)
	if err != nil {
		return err
	}
	if store.JobFinished(job.Status) {
		return nil
	}
	return ErrNotCancelable
}

func (s *Service) run(ctx context.Context, cancel context.CancelFunc, job *store.Job,
	key string, timeout time.Duration, fn Func) {
	defer cancel()

	p := &Progress{svc: s, job: job}

	// 排队：先拿同实例的锁，再拿全局并发名额。顺序固定，不会互相等死。
	unlock, ok := s.lockKey(ctx, key)
	if ok {
		defer unlock()
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		case <-ctx.Done():
			ok = false
		}
	}

	s.mu.Lock()
	delete(s.pending, job.ID)
	s.mu.Unlock()

	if !ok {
		p.finish(store.JobCanceled, Outcome{}, errors.New("已在开始执行前取消"))
		return
	}

	now := time.Now()
	p.mu.Lock()
	job.Status = store.JobRunning
	job.StartedAt = &now
	p.mu.Unlock()
	p.save()

	runCtx, stop := context.WithTimeout(context.Background(), timeout)
	defer stop()

	out, err := s.call(runCtx, p, fn)
	switch {
	case err == nil:
		p.finish(store.JobSucceeded, out, nil)
	case errors.Is(err, context.DeadlineExceeded) && runCtx.Err() != nil:
		p.finish(store.JobFailed, out, fmt.Errorf("超过 %s 仍未完成，已停止等待。操作可能仍在 Oracle 侧进行，请稍后到对应页面确认", timeout))
	default:
		p.finish(store.JobFailed, out, err)
	}
}

// call 执行任务体并兜住 panic：一个任务出错不能带走整个进程。
func (s *Service) call(ctx context.Context, p *Progress, fn Func) (out Outcome, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("后台任务崩溃", "job", p.job.ID, "kind", p.job.Kind, "panic", r)
			err = fmt.Errorf("内部错误：%v", r)
		}
	}()
	return fn(ctx, p)
}

// lockKey 获取某个键的独占锁。key 为空时不加锁。
func (s *Service) lockKey(ctx context.Context, key string) (func(), bool) {
	if key == "" {
		return func() {}, true
	}
	s.mu.Lock()
	l, ok := s.keyLocks[key]
	if !ok {
		l = &keyLock{ch: make(chan struct{}, 1)}
		s.keyLocks[key] = l
	}
	l.refs++
	s.mu.Unlock()

	release := func() {
		s.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(s.keyLocks, key)
		}
		s.mu.Unlock()
	}

	select {
	case l.ch <- struct{}{}:
		return func() { <-l.ch; release() }, true
	case <-ctx.Done():
		release()
		return nil, false
	}
}

func (s *Service) publish(job *store.Job) {
	if s.onChange != nil {
		s.onChange(*job)
	}
}

// Progress 让任务体汇报进度。
type Progress struct {
	svc *Service
	mu  sync.Mutex
	job *store.Job
}

// Step 追加一条进度说明。
func (p *Progress) Step(text string) {
	p.mu.Lock()
	p.job.Steps = append(p.job.Steps, store.JobStep{At: time.Now(), Text: text})
	p.mu.Unlock()
	p.save()
}

// Stepf 是 Step 的格式化版本。
func (p *Progress) Stepf(format string, args ...any) {
	p.Step(fmt.Sprintf(format, args...))
}

// Set 设置完成百分比（0–100）。只增不减：进度条往回跳会让人以为出错了。
func (p *Progress) Set(percent int) {
	if percent > 99 {
		// 100 只留给成功落定的那一刻。
		percent = 99
	}
	p.mu.Lock()
	changed := percent > p.job.Progress
	if changed {
		p.job.Progress = percent
	}
	p.mu.Unlock()
	if changed {
		p.save()
	}
}

// SetInstance 在任务执行中途补上实例 ID（创建实例时，提交后才拿得到）。
func (p *Progress) SetInstance(id string) {
	p.mu.Lock()
	p.job.InstanceID = id
	p.mu.Unlock()
	p.save()
}

func (p *Progress) finish(status string, out Outcome, err error) {
	now := time.Now()
	p.mu.Lock()
	p.job.Status = status
	p.job.FinishedAt = &now
	if status == store.JobSucceeded {
		p.job.Progress = 100
	}
	p.job.Summary = out.Summary
	if out.Result != nil {
		if raw, e := json.Marshal(out.Result); e == nil {
			p.job.Result = raw
		}
	}
	if err != nil {
		p.job.Error = ErrorText(err)
	}
	p.mu.Unlock()
	p.save()
}

func (p *Progress) save() {
	p.mu.Lock()
	snapshot := *p.job
	snapshot.Steps = append([]store.JobStep(nil), p.job.Steps...)
	p.mu.Unlock()

	if err := p.svc.st.SaveJob(context.Background(), &snapshot); err != nil {
		slog.Warn("保存任务进度失败", "job", snapshot.ID, "err", err)
	}
	p.svc.publish(&snapshot)
}

// ErrorText 把错误压成一行给人看的文字，OCI 错误保留错误码——用户排障要拿它去搜。
func ErrorText(err error) string {
	text := err.Error()
	if apiErr, ok := ociclient.AsAPIError(err); ok {
		code := apiErr.Code
		if code == "" {
			code = fmt.Sprintf("HTTP %d", apiErr.StatusCode)
		}
		// 被包过一层的（"没能为子网开通 IPv6：…"）保留外层说明，只压缩 OCI 那段。
		return strings.Replace(text, apiErr.Error(), code+" · "+apiErr.Message, 1)
	}
	return text
}
