package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ocicore/internal/instancesvc"
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

func (s *Server) handleSyncInstances(w http.ResponseWriter, r *http.Request) {
	accountID := r.URL.Query().Get("accountId")

	// 同步可能耗时数十秒，用独立的 context 而不是请求的——
	// 用户关掉页面不应该让一轮已经开始的同步半途而废。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()

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
		writeInstanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleInstanceAction(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("id")
	action := strings.ToUpper(r.PathValue("action"))

	// 强制操作（拔电源式的 STOP / RESET）有丢数据的风险，
	// 归入 L2：必须显式带上 force=true 才放行。
	if isForcefulAction(action) && r.URL.Query().Get("force") != "true" {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: "confirm_required",
			Message: "该操作会直接切断电源，可能导致数据丢失。" +
				"如确认要执行，请附带 force=true。",
		})
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
		writeError(w, http.StatusBadRequest, "note_too_long", "备注最多 500 个字符")
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
	Ocpus       float32 `json:"ocpus"`
	MemoryInGBs float32 `json:"memoryInGbs"`
}

func (s *Server) handleReshapeInstance(w http.ResponseWriter, r *http.Request) {
	var req reshapeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	inst, err := s.instances.Reshape(r.Context(), r.PathValue("id"), instancesvc.ReshapeRequest{
		Ocpus:       req.Ocpus,
		MemoryInGBs: req.MemoryInGBs,
	})
	if err != nil {
		writeInstanceError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "instance_reshape",
		AccountID: inst.AccountID, Target: inst.DisplayName, IP: s.clientIP(r),
		Detail: fmt.Sprintf("%g OCPU / %g GB", req.Ocpus, req.MemoryInGBs),
	})
	writeJSON(w, http.StatusOK, inst)
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
			"终止实例功能已在设置中禁用")
		return
	}

	if confirm := strings.TrimSpace(r.URL.Query().Get("confirm")); confirm != inst.DisplayName {
		writeError(w, http.StatusBadRequest, "confirm_required",
			"终止实例需要在 confirm 参数中回传实例名称 "+strconv.Quote(inst.DisplayName))
		return
	}

	preserveBootVolume := r.URL.Query().Get("preserveBootVolume") == "true"
	if err := s.instances.Terminate(r.Context(), instanceID, preserveBootVolume); err != nil {
		writeInstanceError(w, err)
		return
	}

	user := userFrom(r.Context())
	detail := "引导卷一并删除"
	if preserveBootVolume {
		detail = "保留引导卷"
	}
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "instance_terminate",
		AccountID: inst.AccountID, Target: inst.DisplayName,
		Detail: detail, IP: s.clientIP(r),
	})
	s.notifier.Dispatch(r.Context(), notify.Message{
		Event: notify.EventDangerOperation,
		Title: "实例 " + inst.DisplayName + " 已终止",
		Fields: map[string]string{
			"实例":  inst.DisplayName,
			"区域":  inst.Region,
			"引导卷": detail,
			"操作者": user.Username,
			"来源":  s.clientIP(r),
		},
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
		writeError(w, http.StatusInternalServerError, "no_streaming", "当前服务器不支持流式响应")
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
				Message: orText(acc.StatusMessage, "凭据校验失败"), Severity: "danger",
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
			Kind: "instances_stopped", Target: fmt.Sprintf("%d 台实例", stopped),
			Message: "有实例处于已停止状态", Severity: "warning",
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---- 助手 ----

// writeInstanceError 把实例操作的错误映射成合适的状态码。
func writeInstanceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "实例不存在或尚未同步")
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
