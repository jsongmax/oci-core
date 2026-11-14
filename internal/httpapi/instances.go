package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"ocicore/internal/i18n"
	"ocicore/internal/instancesvc"
	"ocicore/internal/jobsvc"
	"ocicore/internal/notify"
	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

func (s *Server) handleListInstances(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.InstanceFilter{
		AccountIDs:        splitCSV(q.Get("accountIds")),
		Regions:           splitCSV(q.Get("regions")),
		States:            splitCSV(q.Get("states")),
		Search:            q.Get("search"),
		IncludeTerminated: q.Get("includeTerminated") == "true",
	}

	instances, err := s.st.ListInstances(r.Context(), filter)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"instances": instances,
		"sync":      s.instances.Status(),
	})
}

func (s *Server) handleGetInstance(w http.ResponseWriter, r *http.Request) {
	detail, err := s.instances.Detail(r.Context(), r.PathValue("id"))
	if err != nil {
		writeInstanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// handleSyncInstances 触发一次全量（或单账号）同步。同步要挨个区域去问
// Oracle，可能几十秒，放到后台任务里跑，结果在任务列表里看。
func (s *Server) handleSyncInstances(w http.ResponseWriter, r *http.Request) {
	accountID := r.URL.Query().Get("accountId")

	title := i18n.M("api.sync_all_title")
	if accountID != "" {
		acc, err := s.st.GetAccount(r.Context(), accountID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		title = i18n.M("api.sync_account_title", acc.Alias)
	}

	s.submitJob(w, jobsvc.Spec{
		Kind: jobSync, Title: title, AccountID: accountID, Timeout: 6 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		p.Step(i18n.M("api.step.sync"))
		var (
			report *instancesvc.SyncReport
			err    error
		)
		if accountID != "" {
			report, err = s.instances.SyncAccount(ctx, accountID)
		} else {
			report, err = s.instances.SyncAll(ctx)
		}
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		for _, e := range report.Errors {
			p.Stepf("⚠ %s · %s：%s", e.AccountAlias, e.Region, orText(e.OciCode, e.Message))
		}
		summary := i18n.M("api.sync_summary",
			report.Instances, report.Regions, float64(report.DurationMs)/1000)
		if report.Terminated > 0 {
			summary += i18n.M("api.sync_terminated", report.Terminated)
		}
		if n := len(report.Errors); n > 0 {
			summary += i18n.M("api.sync_failed_regions", n)
		}
		return jobsvc.Outcome{Summary: summary, Result: report}, nil
	})
}

func (s *Server) handleInstanceAction(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("id")
	action := strings.ToUpper(r.PathValue("action"))

	// 强制操作（拔电源式的 STOP / RESET）有丢数据的风险，
	// 归入 L2：必须显式带上 force=true 才放行。
	if isForcefulAction(action) && r.URL.Query().Get("force") != "true" {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code:    "confirm_required",
			Message: i18n.M("api.force_confirm"),
		})
		return
	}
	if isForcefulAction(action) && !s.requireDangerTOTP(w, r) {
		return
	}

	inst, err := s.instances.Action(r.Context(), instanceID, action)
	if err != nil {
		writeInstanceError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "instance_" + strings.ToLower(action),
		AccountID: inst.AccountID, Target: inst.DisplayName, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, inst)
}

type renameInstanceRequest struct {
	DisplayName string `json:"displayName"`
}

func (s *Server) handleRenameInstance(w http.ResponseWriter, r *http.Request) {
	var req renameInstanceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	inst, err := s.instances.Rename(r.Context(), r.PathValue("id"), req.DisplayName)
	if err != nil {
		writeInstanceError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "instance_rename",
		AccountID: inst.AccountID, Target: inst.DisplayName, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, inst)
}

type noteRequest struct {
	Note string `json:"note"`
}

// handleSetInstanceNote 写入用户备注。
//
// 长度上限 500：备注是给人看的一句话，不是日志。不设上限的话，一次误粘贴
// 就能把几 MB 塞进这一行，而它会随每次实例列表请求发给前端。
func (s *Server) handleSetInstanceNote(w http.ResponseWriter, r *http.Request) {
	var req noteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	note := strings.TrimSpace(req.Note)
	if len([]rune(note)) > 500 {
		writeError(w, http.StatusBadRequest, "note_too_long", i18n.M("api.note_too_long"))
		return
	}

	instanceID := r.PathValue("id")
	if err := s.st.SetInstanceNote(r.Context(), instanceID, note); err != nil {
		writeStoreError(w, err)
		return
	}
	inst, err := s.st.GetInstance(r.Context(), instanceID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, inst)
}

type reshapeRequest struct {
	Ocpus                   float32 `json:"ocpus"`
	MemoryInGBs             float32 `json:"memoryInGbs"`
	BaselineOcpuUtilization string  `json:"baselineOcpuUtilization"`
}

// handleReshapeInstance 修改实例的 OCPU 与内存。
//
// 运行中的实例改配置时 Oracle 会自己重启一次，整个过程要一两分钟。
// 任务会一直等到重启落定，进度条才走完。
func (s *Server) handleReshapeInstance(w http.ResponseWriter, r *http.Request) {
	var req reshapeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Ocpus <= 0 || req.MemoryInGBs <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_input", i18n.M("api.reshape_positive"))
		return
	}
	inst, err := s.st.GetInstance(r.Context(), r.PathValue("id"))
	if err != nil {
		writeInstanceError(w, err)
		return
	}
	if ociclient.IsTransitionalState(inst.LifecycleState) {
		writeInstanceError(w, instancesvc.ErrInstanceBusy)
		return
	}
	who := s.actorOf(r)
	detail := fmt.Sprintf("%g OCPU / %g GB", req.Ocpus, req.MemoryInGBs)

	s.submitJob(w, jobsvc.Spec{
		Kind: jobReshape, Title: i18n.M("api.reshape_title", inst.DisplayName, detail),
		AccountID: inst.AccountID, InstanceID: inst.ID, Timeout: 15 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		wasRunning := inst.LifecycleState == ociclient.LifecycleRunning
		p.Step(i18n.M("api.step.reshape_submit"))
		updated, err := s.instances.Reshape(ctx, inst.ID, instancesvc.ReshapeRequest{
			Ocpus:        req.Ocpus,
			MemoryInGBs:  req.MemoryInGBs,
			BaselineOcpu: req.BaselineOcpuUtilization,
		})
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		s.audit(who, store.AuditEntry{
			Action: "instance_reshape", AccountID: inst.AccountID,
			Target: inst.DisplayName, Detail: detail,
		})
		p.Set(30)

		// 关着的机器改完就生效；运行中的要等 Oracle 重启一轮。
		var grace time.Duration
		if wasRunning {
			p.Step(i18n.M("api.step.reshape_wait"))
			grace = 2 * time.Minute
		}
		final, err := s.waitInstanceSettled(ctx, p, inst.ID, 30, 99, grace)
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		if final == nil {
			return jobsvc.Outcome{}, i18n.Err("api.instance_gone")
		}
		summary := i18n.M("api.reshape_done", updated.Ocpus, updated.MemoryGB, final.LifecycleState)
		return jobsvc.Outcome{Summary: summary}, nil
	})
}

type replaceBootVolumeRequest struct {
	ImageID      string `json:"imageId"`
	BootVolumeGB int64  `json:"bootVolumeGb"`
	// PreserveBootVolume 决定旧引导卷是否保留。
	PreserveBootVolume bool   `json:"preserveBootVolume"`
	SSHPublicKey       string `json:"sshPublicKey"`
	CloudInit          string `json:"cloudInit"`
	// Confirm 必须等于实例名：重装会抹掉系统盘上的全部数据。
	Confirm string `json:"confirm"`
}

// handleReplaceBootVolume 用新镜像替换引导卷，即重装系统。L3 级危险操作。
//
// 和终止一样由服务端校验确认串。实例本身保留，所以不受「允许终止」开关约束。
func (s *Server) handleReplaceBootVolume(w http.ResponseWriter, r *http.Request) {
	var req replaceBootVolumeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ImageID) == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", i18n.M("api.image_required"))
		return
	}
	if req.BootVolumeGB != 0 && req.BootVolumeGB < instancesvc.MinBootVolumeGB {
		writeError(w, http.StatusBadRequest, "invalid_input",
			i18n.M("api.boot_min", instancesvc.MinBootVolumeGB))
		return
	}

	inst, err := s.st.GetInstance(r.Context(), r.PathValue("id"))
	if err != nil {
		writeInstanceError(w, err)
		return
	}
	if strings.TrimSpace(req.Confirm) != inst.DisplayName {
		writeError(w, http.StatusBadRequest, "confirm_required",
			i18n.M("api.replace_confirm", inst.DisplayName))
		return
	}
	if ociclient.IsTransitionalState(inst.LifecycleState) {
		writeInstanceError(w, instancesvc.ErrInstanceBusy)
		return
	}

	detail := i18n.M("api.old_boot_deleted")
	if req.PreserveBootVolume {
		detail = i18n.M("api.old_boot_kept")
	}
	userData := ""
	if ci := strings.TrimSpace(req.CloudInit); ci != "" {
		userData = buildMetadata("", ci)["user_data"]
	}
	who := s.actorOf(r)
	wasRunning := inst.LifecycleState == ociclient.LifecycleRunning

	s.submitJob(w, jobsvc.Spec{
		Kind: jobReplaceBoot, Title: i18n.M("api.replace_title", inst.DisplayName),
		AccountID: inst.AccountID, InstanceID: inst.ID, Timeout: 30 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		p.Step(i18n.M("api.step.replace_submit", detail))
		updated, err := s.instances.ReplaceBootVolume(ctx, inst.ID, instancesvc.ReplaceBootVolumeRequest{
			ImageID:      req.ImageID,
			BootVolumeGB: req.BootVolumeGB,
			PreserveOld:  req.PreserveBootVolume,
			SSHPublicKey: req.SSHPublicKey,
			UserData:     userData,
		})
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		osName := updated.ImageOS
		if osName == "" {
			osName = req.ImageID
		}
		s.audit(who, store.AuditEntry{
			Action: "instance_replace_boot_volume", AccountID: inst.AccountID,
			Target: inst.DisplayName, Detail: osName + "，" + detail,
		})
		s.notifier.Dispatch(ctx, notify.Message{
			Event: notify.EventDangerOperation,
			Title: i18n.M("api.replace_notify_title", inst.DisplayName),
			Fields: map[string]string{
				i18n.M("f.instance"): inst.DisplayName,
				i18n.M("f.region"):   inst.Region,
				i18n.M("f.new_os"):   osName,
				i18n.M("f.old_boot"): detail,
				i18n.M("f.operator"): who.username,
				i18n.M("f.source"):   who.ip,
			},
		})
		p.Set(15)

		// Oracle 受理后要过一会才开始停机换盘，缓存一开始可能还是原状态。
		p.Step(i18n.M("api.step.replace_wait"))
		final, err := s.waitInstanceSettled(ctx, p, inst.ID, 15, 70, 3*time.Minute)
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		if final == nil {
			return jobsvc.Outcome{}, i18n.Err("api.instance_gone")
		}
		// 运行中的实例会经过 STOPPED 再开回来，中途那一下 STOPPED 不算落定。
		if wasRunning && final.LifecycleState == ociclient.LifecycleStopped {
			p.Step(i18n.M("api.step.replace_restart"))
			final, err = s.waitInstanceSettled(ctx, p, inst.ID, 70, 95, 5*time.Minute)
			if err != nil {
				return jobsvc.Outcome{}, err
			}
			if final == nil {
				return jobsvc.Outcome{}, i18n.Err("api.instance_gone")
			}
		}

		p.Step(i18n.M("api.step.read_new_boot"))
		if err := s.instances.RefreshBootVolume(ctx, inst.ID); err != nil {
			// 不影响结果：下一轮同步会补上。
			p.Step(i18n.M("api.step.new_boot_missing", err.Error()))
		}
		return jobsvc.Outcome{Summary: i18n.M("api.replace_done", osName, final.LifecycleState, detail)}, nil
	})
}

// handleTerminateInstance 终止实例。这是 L3 级危险操作。
//
// 服务端必须自己校验确认串：前端的输名确认框可以被绕过。
func (s *Server) handleTerminateInstance(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("id")

	inst, err := s.st.GetInstance(r.Context(), instanceID)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	policy, err := s.st.Settings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !policy.AllowTerminate {
		writeError(w, http.StatusForbidden, "terminate_disabled",
			i18n.M("api.terminate_disabled"))
		return
	}

	if confirm := strings.TrimSpace(r.URL.Query().Get("confirm")); confirm != inst.DisplayName {
		writeError(w, http.StatusBadRequest, "confirm_required",
			i18n.M("api.terminate_confirm", inst.DisplayName))
		return
	}

	preserveBootVolume := r.URL.Query().Get("preserveBootVolume") == "true"
	detail := i18n.M("api.boot_deleted_too")
	if preserveBootVolume {
		detail = i18n.M("api.boot_kept")
	}
	who := s.actorOf(r)

	s.submitJob(w, jobsvc.Spec{
		Kind: jobTerminate, Title: i18n.M("api.terminate_title", inst.DisplayName),
		AccountID: inst.AccountID, InstanceID: inst.ID, Timeout: 20 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		p.Step(i18n.M("api.step.terminate_submit", detail))
		if err := s.instances.Terminate(ctx, inst.ID, preserveBootVolume); err != nil {
			return jobsvc.Outcome{}, err
		}
		s.audit(who, store.AuditEntry{
			Action: "instance_terminate", AccountID: inst.AccountID,
			Target: inst.DisplayName, Detail: detail,
		})
		s.notifier.Dispatch(ctx, notify.Message{
			Event: notify.EventDangerOperation,
			Title: i18n.M("api.terminated_title", inst.DisplayName),
			Fields: map[string]string{
				i18n.M("f.instance"):    inst.DisplayName,
				i18n.M("f.region"):      inst.Region,
				i18n.M("f.boot_volume"): detail,
				i18n.M("f.operator"):    who.username,
				i18n.M("f.source"):      who.ip,
			},
		})
		p.Step(i18n.M("api.step.terminate_wait"))
		p.Set(20)
		final, err := s.waitInstanceSettled(ctx, p, inst.ID, 20, 99, 0)
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		if final != nil && final.LifecycleState != ociclient.LifecycleTerminated {
			return jobsvc.Outcome{}, i18n.Err("api.not_terminated", final.LifecycleState)
		}
		return jobsvc.Outcome{Summary: i18n.M("api.terminated_summary", detail)}, nil
	})
}

// handleDismissInstanceError 清除某行的错误提示。用户看过之后主动关掉。
func (s *Server) handleDismissInstanceError(w http.ResponseWriter, r *http.Request) {
	if err := s.st.ClearInstanceError(r.Context(), r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleEvents 是 SSE 事件流。
//
// 选 SSE 而非 WebSocket：事件只需要服务端推给客户端，是单向的。
// SSE 走普通 HTTP，浏览器自带断线重连，Nginx 配置也简单得多。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no_streaming", i18n.M("api.no_streaming"))
		return
	}

	// 解除这条响应的写超时。
	//
	// http.Server 上配了 WriteTimeout（保护普通接口不被慢客户端拖住），
	// 而那是从请求被接受起算的**绝对**截止时间，不是空闲计时器。SSE 是一个
	// 永不结束的响应，于是每隔 WriteTimeout 就被服务端自己掐断一次——下面那个
	// 25 秒心跳一点忙都帮不上，它防的是中间代理的空闲超时，不是这个。
	//
	// 表现是浏览器控制台每分钟冒一条 ERR_HTTP2_PROTOCOL_ERROR 200 (OK)：
	// 响应头早发出去了所以是 200，流被中途重置所以是 protocol error。
	// 功能上 EventSource 会自己重连，但每次都断掉几秒的实时推送。
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		// 拿不到底层连接时只记一笔，不影响这条流本身——大不了退回到
		// 每 WriteTimeout 断一次重连，和修之前一样。
		slog.Debug("SSE 无法解除写超时", "err", err)
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// 关掉 Nginx 的响应缓冲，否则事件会被攒着一起发，实时性全没了。
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	events, unsubscribe := s.instances.Bus().Subscribe()
	defer unsubscribe()
	lang := langOf(w)

	fmt.Fprintf(w, "retry: 3000\n\n")
	flusher.Flush()

	// 心跳防止中间的代理因为长时间无数据而掐断连接。
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case e, ok := <-events:
			if !ok {
				return
			}
			data, err := json.Marshal(e)
			if err != nil {
				continue
			}
			// 每条连接按自己的语言展开（前端切换语言时会带新语言重连）。
			data = i18n.RenderJSON(lang, data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, data)
			flusher.Flush()

		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// overviewResponse 是总览页需要的全部聚合数据。
type overviewResponse struct {
	Accounts struct {
		Total    int `json:"total"`
		OK       int `json:"ok"`
		Error    int `json:"error"`
		Disabled int `json:"disabled"`
	} `json:"accounts"`
	Instances struct {
		Total   int            `json:"total"`
		ByState map[string]int `json:"byState"`
	} `json:"instances"`
	Distribution []store.AccountRegionCount `json:"distribution"`
	Regions      []string                   `json:"regions"`
	Sync         instancesvc.Status         `json:"sync"`
	// Attention 是「需要注意的」列表：账号异常、实例异常停止等。
	// 无内容时前端整块隐藏，不占用注意力。
	Attention []attentionItem `json:"attention"`
}

type attentionItem struct {
	Kind      string `json:"kind"`
	AccountID string `json:"accountId,omitempty"`
	Target    string `json:"target"`
	Message   string `json:"message"`
	Severity  string `json:"severity"` // warning / danger
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	accounts, err := s.st.ListAccounts(ctx)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	byState, err := s.st.CountInstancesByState(ctx)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	distribution, err := s.st.InstanceDistribution(ctx)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	var resp overviewResponse
	resp.Accounts.Total = len(accounts)
	resp.Instances.ByState = byState
	resp.Distribution = distribution
	resp.Sync = s.instances.Status()

	regionSet := map[string]struct{}{}
	for i := range accounts {
		acc := &accounts[i]
		switch {
		case !acc.Enabled:
			resp.Accounts.Disabled++
		case acc.Status == store.StatusError:
			resp.Accounts.Error++
			resp.Attention = append(resp.Attention, attentionItem{
				Kind: "account_error", AccountID: acc.ID, Target: acc.Alias,
				Message: orText(acc.StatusMessage, i18n.M("api.cred_check_failed")), Severity: "danger",
			})
		case acc.Status == store.StatusOK:
			resp.Accounts.OK++
		}
		for _, region := range acc.SubscribedRegions {
			regionSet[region] = struct{}{}
		}
	}
	for _, n := range byState {
		resp.Instances.Total += n
	}
	for region := range regionSet {
		resp.Regions = append(resp.Regions, region)
	}

	// 停止的实例值得提一句：免费额度的机器被 Oracle 回收前
	// 通常先表现为异常停止，早点发现还有救。
	if stopped := byState[ociclient.LifecycleStopped]; stopped > 0 {
		resp.Attention = append(resp.Attention, attentionItem{
			Kind: "instances_stopped", Target: i18n.M("api.n_instances", stopped),
			Message: i18n.M("api.instances_stopped"), Severity: "warning",
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---- 助手 ----

// writeInstanceError 把实例操作的错误映射成合适的状态码。
func writeInstanceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", i18n.M("api.instance_not_found"))
		return
	case errors.Is(err, instancesvc.ErrInstanceBusy):
		writeError(w, http.StatusConflict, "instance_busy", err.Error())
		return
	}
	if _, ok := ociclient.AsAPIError(err); ok {
		writeOCIError(w, err)
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
}

// isForcefulAction 报告操作是否会直接切断电源。
func isForcefulAction(action string) bool {
	return action == ociclient.ActionStop || action == ociclient.ActionReset
}

func splitCSV(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func orText(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
