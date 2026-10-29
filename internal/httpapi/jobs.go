package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"ocicore/internal/instancesvc"
	"ocicore/internal/jobsvc"
	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

// 后台任务种类。前端按它显示图标与文案。
const (
	jobLaunch       = "instance.launch"
	jobChangeIP     = "instance.change_ip"
	jobReshape      = "instance.reshape"
	jobTerminate    = "instance.terminate"
	jobEnableIPv6   = "instance.enable_ipv6"
	jobRemoveIPv6   = "instance.remove_ipv6"
	jobAttachVnic   = "instance.attach_vnic"
	jobDetachVnic   = "instance.detach_vnic"
	jobBulkAction   = "instance.bulk"
	jobSync         = "instance.sync"
	jobEnsureNet    = "network.ensure"
	jobBootVolume   = "storage.boot_volume"
	jobVolumeUpdate = "storage.volume"
)

// actor 是发起操作的人。任务在请求结束后才跑完，审计与通知需要的信息
// 必须在请求还活着的时候取出来。
type actor struct {
	userID   string
	username string
	ip       string
}

func (s *Server) actorOf(r *http.Request) actor {
	user := userFrom(r.Context())
	return actor{userID: user.ID, username: user.Username, ip: s.clientIP(r)}
}

// audit 在任务里写审计日志。用独立 context：任务的 context 可能已经超时。
func (s *Server) audit(a actor, e store.AuditEntry) {
	e.UserID, e.IP = a.userID, a.ip
	_ = s.st.Audit(context.Background(), e)
}

// submitJob 提交任务并以 202 返回任务快照。
func (s *Server) submitJob(w http.ResponseWriter, spec jobsvc.Spec, fn jobsvc.Func) {
	job, err := s.jobs.Submit(spec, fn)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

// onJobChange 把任务进度推给前端。
func (s *Server) onJobChange(job store.Job) {
	if s.instances == nil {
		return
	}
	s.instances.Bus().Publish(instancesvc.Event{
		Type: instancesvc.EventJobUpdated, AccountID: job.AccountID,
		InstanceID: job.InstanceID, State: job.Status, Data: job,
	})
}

// ---- 任务列表接口 ----

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.st.ListJobs(r.Context(), r.URL.Query().Get("status") == "active", 300)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.st.GetJob(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJobError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	if err := s.jobs.Cancel(r.Context(), r.PathValue("id")); err != nil {
		writeJobError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteJob(r.Context(), r.PathValue("id")); err != nil {
		writeJobError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleClearJobs(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.DeleteFinishedJobs(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

func writeJobError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrJobNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "job_running", "任务还没结束，不能删除")
	case errors.Is(err, jobsvc.ErrNotCancelable):
		writeError(w, http.StatusConflict, "job_running", err.Error())
	default:
		writeStoreError(w, err)
	}
}

// ---- 任务体里常用的等待 ----

// settlePollInterval 是等实例状态落定时读缓存的间隔。只读本地库，
// 真正去问 Oracle 的是 instancesvc 的轮询协程。
const settlePollInterval = 3 * time.Second

// waitInstanceSettled 等实例离开过渡态，期间把状态变化记成进度。
// 进度从 from 线性挪向 to，让进度条在漫长的 PROVISIONING 里也在动。
//
// grace 是「还没见到过渡态」时最多等多久：改配置这类操作提交后实例要过一会
// 才开始重启，缓存里一开始还是 RUNNING，不能一看是稳态就收工。
//
// 返回 nil 实例表示它已从缓存移除（终止完成）。
func (s *Server) waitInstanceSettled(ctx context.Context, p *jobsvc.Progress, id string, from, to int, grace time.Duration) (*store.Instance, error) {
	s.instances.Watch(id)

	last := ""
	tick := 0
	seenTransition := false
	deadline := time.Now().Add(grace)
	for {
		inst, err := s.st.GetInstance(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if inst.LifecycleState != last {
			if last != "" {
				p.Stepf("实例状态：%s → %s", last, inst.LifecycleState)
			}
			last = inst.LifecycleState
		}
		if ociclient.IsTransitionalState(inst.LifecycleState) {
			seenTransition = true
		} else if tick > 0 && (seenTransition || !time.Now().Before(deadline)) {
			// 第一轮哪怕已是稳态也再看一眼：刚提交的操作，缓存里可能还是旧状态。
			return inst, nil
		}

		tick++
		// 约 3 分钟走完 from→to 的九成，之后停在那里等。
		if span := to - from; span > 0 {
			step := span * tick / 60
			if step > span*9/10 {
				step = span * 9 / 10
			}
			p.Set(from + step)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(settlePollInterval):
		}
		// 轮询协程有 15 分钟上限，超时后就没人更新缓存了；重新挂上是幂等的。
		s.instances.Watch(id)
	}
}
