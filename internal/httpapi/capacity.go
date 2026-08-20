package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"ocicore/internal/capacitysvc"
	"ocicore/internal/notify"
	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

// capacityWatchDTO 在监控项上补一个人话状态，省得前端各自再映射一遍。
type capacityWatchDTO struct {
	store.CapacityWatch
	StatusText string `json:"statusText"`
	// AvailabilityDomainShort 是给人看的简写（AD-1）。完整名仍然保留，
	// 调 OCI 接口一律用完整名——用简写去查会静默筛不到任何东西。
	AvailabilityDomainShort string `json:"availabilityDomainShort"`
}

func toCapacityDTO(w store.CapacityWatch) capacityWatchDTO {
	return capacityWatchDTO{
		CapacityWatch:           w,
		StatusText:              ociclient.CapacityStatusText(w.LastStatus),
		AvailabilityDomainShort: shortADName(w.AvailabilityDomain),
	}
}

// shortADName 把 xxxx:US-SANJOSE-1-AD-1 压成 AD-1。
func shortADName(ad string) string {
	if i := strings.LastIndex(strings.ToUpper(ad), "AD-"); i >= 0 {
		return ad[i:]
	}
	return ad
}

func (s *Server) handleListCapacityWatches(w http.ResponseWriter, r *http.Request) {
	watches, err := s.st.ListCapacityWatches(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := make([]capacityWatchDTO, 0, len(watches))
	for _, x := range watches {
		out = append(out, toCapacityDTO(x))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"watches":              out,
		"probeIntervalSeconds": int(capacitysvc.DefaultProbeInterval.Seconds()),
	})
}

type capacityWatchRequest struct {
	AccountID          string  `json:"accountId"`
	Region             string  `json:"region"`
	AvailabilityDomain string  `json:"availabilityDomain"`
	Shape              string  `json:"shape"`
	Ocpus              float64 `json:"ocpus"`
	MemoryGB           float64 `json:"memoryGb"`
}

func (s *Server) handleCreateCapacityWatch(w http.ResponseWriter, r *http.Request) {
	var req capacityWatchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.AccountID == "" || req.Shape == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "accountId 与 shape 为必填项")
		return
	}

	_, acc, err := s.conns.ForID(r.Context(), req.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}

	region := ociclient.NormalizeRegion(req.Region)
	if region == "" {
		region = acc.DefaultRegion
	}
	if strings.TrimSpace(req.AvailabilityDomain) == "" {
		writeError(w, http.StatusBadRequest, "missing_ad",
			"需要完整的可用域名，例如 xxxx:US-SANJOSE-1-AD-1")
		return
	}

	watch, err := s.st.CreateCapacityWatch(r.Context(), store.CapacityWatch{
		AccountID:          acc.ID,
		Region:             region,
		AvailabilityDomain: req.AvailabilityDomain,
		Shape:              req.Shape,
		Ocpus:              req.Ocpus,
		MemoryGB:           req.MemoryGB,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "capacity_watch_create", AccountID: acc.ID,
		Target: req.Shape, Detail: region + " · " + shortADName(req.AvailabilityDomain),
		IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"watch":  toCapacityDTO(*watch),
		"notice": "已加入监控，首次查询将在半分钟内进行。",
	})
}

// handleProbeCapacity 立刻查一次，不落库。
//
// 手动查询用。加它是为了让人能先点一下看看返回对不对，
// 再决定要不要把自动化的东西打开——而不是一上来就有个循环在跑。
func (s *Server) handleProbeCapacity(w http.ResponseWriter, r *http.Request) {
	var req capacityWatchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.AccountID == "" || req.Shape == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "accountId 与 shape 为必填项")
		return
	}

	client, acc, err := s.conns.ForID(r.Context(), req.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}
	region := ociclient.NormalizeRegion(req.Region)
	if region == "" {
		region = acc.DefaultRegion
	}

	// 没指定可用域就把该区域全部查一遍——用户关心的是"这个区能不能开"，
	// 让他先去查一遍 AD 列表再挨个填，是把内部结构甩给使用者。
	ads := []string{req.AvailabilityDomain}
	if strings.TrimSpace(req.AvailabilityDomain) == "" {
		list, err := client.ListAvailabilityDomains(r.Context(), region, acc.CompartmentOCID)
		if err != nil {
			writeOCIError(w, err)
			return
		}
		ads = ads[:0]
		for _, a := range list {
			ads = append(ads, a.Name)
		}
	}

	type adResult struct {
		AvailabilityDomain string `json:"availabilityDomain"`
		Short              string `json:"short"`
		Status             string `json:"status"`
		StatusText         string `json:"statusText"`
		AvailableCount     int64  `json:"availableCount"`
		Error              string `json:"error,omitempty"`
	}

	results := make([]adResult, 0, len(ads))
	for _, ad := range ads {
		row := adResult{AvailabilityDomain: ad, Short: shortADName(ad)}
		report, err := s.capacity.Probe(r.Context(), client, acc.CompartmentOCID,
			region, ad, req.Shape, req.Ocpus, req.MemoryGB)
		if err != nil {
			// 单个 AD 失败不该让整次查询失败：其余 AD 的结果仍然有用，
			// 而且失败原因（比如权限）也需要原样让人看到。
			row.Error = capacityErrText(err)
			row.StatusText = "查询失败"
		} else {
			row.Status = report.StatusOf(req.Shape)
			row.StatusText = ociclient.CapacityStatusText(row.Status)
			for _, a := range report.ShapeAvailabilities {
				if a.InstanceShape == req.Shape {
					row.AvailableCount = a.AvailableCount
					break
				}
			}
		}
		results = append(results, row)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"region":  region,
		"shape":   req.Shape,
		"results": results,
		"notice": "只读查询，不创建任何资源。显示有容量时创建仍可能失败——" +
			"它反映宿主机池的整体状态，不是那一瞬间的分配结果。",
	})
}

func (s *Server) handleSetCapacityWatchEnabled(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	action := r.PathValue("action")

	var enabled bool
	switch action {
	case "enable":
		enabled = true
	case "disable":
		enabled = false
	default:
		writeError(w, http.StatusBadRequest, "bad_action", "只支持 enable 或 disable")
		return
	}

	watch, err := s.st.SetCapacityWatchEnabled(r.Context(), id, enabled)
	if errors.Is(err, store.ErrCapacityWatchNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "监控项不存在")
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"watch": toCapacityDTO(*watch)})
}

func (s *Server) handleDeleteCapacityWatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.st.DeleteCapacityWatch(r.Context(), id); errors.Is(err, store.ErrCapacityWatchNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "监控项不存在")
		return
	} else if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- 调度器接线 ----

// RunCapacityMonitor 启动容量监控轮询，直到 ctx 结束。
func (s *Server) RunCapacityMonitor(ctx context.Context) { s.capacity.Run(ctx) }

// onCapacityChange 只在状态真正变化时推通知。
//
// 从"没货"变成"有货"是唯一值得打扰用户的转变，措辞要克制：报告说有容量
// 不等于抢得到，把它写成"抢到了"会得到一个总是骗人的通知。
func (s *Server) onCapacityChange(ctx context.Context, w *store.CapacityWatch, prev string) {
	title := "容量状态变化：" + w.Shape
	if w.LastStatus == ociclient.CapacityAvailable {
		title = "有容量了：" + w.Shape
	}
	s.notifier.Dispatch(ctx, notify.Message{
		Event: notify.EventCapacityChanged,
		Title: title,
		Body:  "来自 Oracle 容量报告，说明值得一试，但不保证创建成功。",
		Fields: map[string]string{
			"区域":  w.Region,
			"可用域": shortADName(w.AvailabilityDomain),
			"规格":  w.Shape,
			"现在":  ociclient.CapacityStatusText(w.LastStatus),
			"此前":  ociclient.CapacityStatusText(prev),
		},
	})
}

// capacityErrText 把 OCI 错误压成一行，附上处理建议。
//
// 逐 AD 查询时单个失败不中断整次请求，所以错误得跟着那一行走，
// 不能像 writeOCIError 那样直接写进响应。
func capacityErrText(err error) string {
	if apiErr, ok := ociclient.AsAPIError(err); ok {
		if advice := apiErr.Advice(); advice != "" {
			return apiErr.Message + "（" + advice + "）"
		}
		return apiErr.Message
	}
	return err.Error()
}
