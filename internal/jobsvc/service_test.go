package jobsvc

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ocicore/internal/cryptobox"
	"ocicore/internal/store"
)

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	key := make([]byte, cryptobox.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := cryptobox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	svc := New(Deps{Store: st})
	t.Cleanup(svc.Wait)
	return svc, st
}

func waitFinished(t *testing.T, st *store.Store, id string) *store.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := st.GetJob(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if store.JobFinished(job.Status) {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("任务 %s 没有在期限内结束", id)
	return nil
}

func TestSubmitSucceeds(t *testing.T) {
	svc, st := newTestService(t)

	job, err := svc.Submit(Spec{Kind: "test", Title: "成功"}, func(ctx context.Context, p *Progress) (Outcome, error) {
		p.Step("第一步")
		p.Set(50)
		return Outcome{Summary: "完成", Result: map[string]string{"ip": "1.2.3.4"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != store.JobQueued {
		t.Fatalf("刚提交的状态 = %s，期望 queued", job.Status)
	}

	got := waitFinished(t, st, job.ID)
	if got.Status != store.JobSucceeded || got.Progress != 100 || got.Summary != "完成" {
		t.Fatalf("结果不对：%+v", got)
	}
	if len(got.Steps) != 1 || got.Steps[0].Text != "第一步" {
		t.Fatalf("步骤不对：%+v", got.Steps)
	}
	if string(got.Result) != `{"ip":"1.2.3.4"}` {
		t.Fatalf("result = %s", got.Result)
	}
	if got.StartedAt == nil || got.FinishedAt == nil {
		t.Fatal("开始与结束时间应当都有值")
	}
}

func TestSubmitFailsAndRecoversPanic(t *testing.T) {
	svc, st := newTestService(t)

	failed, _ := svc.Submit(Spec{Kind: "test", Title: "失败"}, func(ctx context.Context, p *Progress) (Outcome, error) {
		return Outcome{}, errors.New("出错了")
	})
	panicked, _ := svc.Submit(Spec{Kind: "test", Title: "崩溃"}, func(ctx context.Context, p *Progress) (Outcome, error) {
		panic("boom")
	})

	if got := waitFinished(t, st, failed.ID); got.Status != store.JobFailed || got.Error != "出错了" {
		t.Fatalf("失败任务：%+v", got)
	}
	if got := waitFinished(t, st, panicked.ID); got.Status != store.JobFailed || got.Error == "" {
		t.Fatalf("崩溃任务应记为失败：%+v", got)
	}
}

func TestSameInstanceRunsSerially(t *testing.T) {
	svc, st := newTestService(t)

	var (
		mu      sync.Mutex
		running int
		overlap bool
	)
	body := func(ctx context.Context, p *Progress) (Outcome, error) {
		mu.Lock()
		running++
		if running > 1 {
			overlap = true
		}
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return Outcome{}, nil
	}

	var ids []string
	for i := 0; i < 3; i++ {
		job, err := svc.Submit(Spec{Kind: "test", Title: "同一台", InstanceID: "inst-1"}, body)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, job.ID)
	}
	for _, id := range ids {
		waitFinished(t, st, id)
	}
	if overlap {
		t.Fatal("同一实例上的任务不应并发执行")
	}
}

func TestCancelQueuedOnly(t *testing.T) {
	svc, st := newTestService(t)

	release := make(chan struct{})
	started := make(chan struct{})
	first, _ := svc.Submit(Spec{Kind: "test", Title: "占位", InstanceID: "inst-1"}, func(ctx context.Context, p *Progress) (Outcome, error) {
		close(started)
		<-release
		return Outcome{}, nil
	})
	<-started
	second, _ := svc.Submit(Spec{Kind: "test", Title: "排队", InstanceID: "inst-1"}, func(ctx context.Context, p *Progress) (Outcome, error) {
		t.Error("被取消的任务不应执行")
		return Outcome{}, nil
	})

	if err := svc.Cancel(context.Background(), second.ID); err != nil {
		t.Fatalf("取消排队任务失败：%v", err)
	}
	if got := waitFinished(t, st, second.ID); got.Status != store.JobCanceled {
		t.Fatalf("状态 = %s，期望 canceled", got.Status)
	}

	if err := svc.Cancel(context.Background(), first.ID); !errors.Is(err, ErrNotCancelable) {
		t.Fatalf("执行中的任务应拒绝取消，得到 %v", err)
	}
	close(release)
	waitFinished(t, st, first.ID)
}

func TestRecoverMarksInterrupted(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	now := time.Now()
	stale := &store.Job{Kind: "test", Title: "重启前", Status: store.JobRunning,
		Steps: []store.JobStep{}, CreatedAt: now, StartedAt: &now}
	if err := st.SaveJob(ctx, stale); err != nil {
		t.Fatal(err)
	}

	svc.Recover(ctx)

	got, err := st.GetJob(ctx, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.JobInterrupted || got.FinishedAt == nil {
		t.Fatalf("重启前的任务应标为中断：%+v", got)
	}
	if err := st.DeleteJob(ctx, stale.ID); err != nil {
		t.Fatalf("已中断的任务应可删除：%v", err)
	}
}
