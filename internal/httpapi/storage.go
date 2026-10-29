package httpapi

import (
	"context"
	"net/http"
	"time"

	"ocicore/internal/instancesvc"
	"ocicore/internal/jobsvc"
	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

func (s *Server) handleListBootVolumes(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	volumes, err := client.ListBootVolumes(r.Context(), region, acc.CompartmentOCID,
		r.URL.Query().Get("availabilityDomain"))
	if err != nil {
		writeOCIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bootVolumes": volumes})
}

func (s *Server) handleListVolumes(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	volumes, err := client.ListVolumes(r.Context(), region, acc.CompartmentOCID)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"volumes": volumes})
}

// updateVolumeRequest 是引导卷与块存储卷通用的修改请求。
type updateVolumeRequest struct {
	DisplayName string `json:"displayName"`
	SizeInGBs   int64  `json:"sizeInGbs"`
	VpusPerGB   *int64 `json:"vpusPerGb"`
}

func (s *Server) handleUpdateBootVolume(w http.ResponseWriter, r *http.Request) {
	client, _, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	var req updateVolumeRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	volumeID := r.PathValue("id")
	current, err := client.GetBootVolume(r.Context(), region, volumeID)
	if err != nil {
		writeOCIError(w, err)
		return
	}

	// 扩容与 VPU 都在客户端先校验一遍：OCI 的拒绝信息比较晦涩，
	// 提前说清楚"只能扩不能缩"用户才知道该怎么改。
	if req.SizeInGBs > 0 {
		if err := ociclient.ValidateVolumeResize(current.SizeInGBs, req.SizeInGBs); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_size", err.Error())
			return
		}
	}
	if req.VpusPerGB != nil {
		if err := ociclient.ValidateVpus(*req.VpusPerGB); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_vpus", err.Error())
			return
		}
	}

	accountID := r.URL.Query().Get("accountId")
	who := s.actorOf(r)
	name := orText(current.DisplayName, volumeID)

	s.submitJob(w, jobsvc.Spec{
		Kind: jobBootVolume, Title: "修改引导卷 · " + name, AccountID: accountID,
		Timeout: 5 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		p.Step("提交修改")
		updated, err := client.UpdateBootVolume(ctx, region, volumeID, ociclient.UpdateBootVolumeRequest{
			DisplayName: req.DisplayName,
			SizeInGBs:   req.SizeInGBs,
			VpusPerGB:   req.VpusPerGB,
		})
		if err != nil {
			return jobsvc.Outcome{}, err
		}

		s.syncBootVolumeCache(ctx, updated)

		change := describeVolumeChange(current.SizeInGBs, current.VpusPerGB, updated.SizeInGBs, updated.VpusPerGB)
		s.audit(who, store.AuditEntry{
			Action: "boot_volume_update", AccountID: accountID,
			Target: updated.DisplayName, Detail: change,
		})
		// 扩容后系统盘里的分区不会自动跟着变大，必须提醒用户去实例内部处理，
		// 否则会以为"扩了但没生效"。
		notice := noticeForResize(current.SizeInGBs, updated.SizeInGBs)
		if notice != "" {
			p.Step(notice)
		}
		return jobsvc.Outcome{
			Summary: change,
			Result:  map[string]any{"bootVolume": updated, "notice": notice},
		}, nil
	})
}

func (s *Server) handleUpdateVolume(w http.ResponseWriter, r *http.Request) {
	client, _, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	var req updateVolumeRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	volumeID := r.PathValue("id")

	// 块存储没有单独的 Get 接口封装，从列表里定位当前值做校验。
	var current *ociclient.Volume
	if volumes, err := client.ListVolumes(r.Context(), region, ""); err == nil {
		for i := range volumes {
			if volumes[i].ID == volumeID {
				current = &volumes[i]
				break
			}
		}
	}
	if current != nil && req.SizeInGBs > 0 {
		if err := ociclient.ValidateVolumeResize(current.SizeInGBs, req.SizeInGBs); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_size", err.Error())
			return
		}
	}
	if req.VpusPerGB != nil {
		if err := ociclient.ValidateVpus(*req.VpusPerGB); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_vpus", err.Error())
			return
		}
	}

	accountID := r.URL.Query().Get("accountId")
	who := s.actorOf(r)
	name := volumeID
	if current != nil {
		name = orText(current.DisplayName, volumeID)
	}

	s.submitJob(w, jobsvc.Spec{
		Kind: jobVolumeUpdate, Title: "修改块存储卷 · " + name, AccountID: accountID,
		Timeout: 5 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		p.Step("提交修改")
		updated, err := client.UpdateVolume(ctx, region, volumeID, ociclient.UpdateVolumeRequest{
			DisplayName: req.DisplayName,
			SizeInGBs:   req.SizeInGBs,
			VpusPerGB:   req.VpusPerGB,
		})
		if err != nil {
			return jobsvc.Outcome{}, err
		}

		s.audit(who, store.AuditEntry{
			Action: "volume_update", AccountID: accountID, Target: updated.DisplayName,
		})

		summary := "已保存"
		var notice string
		if current != nil {
			notice = noticeForResize(current.SizeInGBs, updated.SizeInGBs)
			summary = describeVolumeChange(current.SizeInGBs, current.VpusPerGB, updated.SizeInGBs, updated.VpusPerGB)
		}
		if notice != "" {
			p.Step(notice)
		}
		return jobsvc.Outcome{
			Summary: summary,
			Result:  map[string]any{"volume": updated, "notice": notice},
		}, nil
	})
}

// syncBootVolumeCache 把引导卷的新容量写回实例缓存，
// 让列表里的引导卷列立刻更新，不用等下一轮同步。
func (s *Server) syncBootVolumeCache(ctx context.Context, bv *ociclient.BootVolume) {
	instances, err := s.st.ListInstances(ctx, store.InstanceFilter{})
	if err != nil {
		return
	}
	for i := range instances {
		if instances[i].BootVolumeID != bv.ID {
			continue
		}
		instances[i].BootVolumeGB = bv.SizeInGBs
		instances[i].BootVolumeVpus = bv.VpusPerGB
		if err := s.st.UpsertInstance(ctx, instances[i]); err == nil {
			s.instances.Bus().Publish(instanceUpdatedEvent(&instances[i]))
		}
		return
	}
}

func noticeForResize(oldGB, newGB int64) string {
	if newGB <= oldGB {
		return ""
	}
	return "云盘已扩容到 " + itoa(int(newGB)) + " GB。" +
		"还需要登录实例扩展分区与文件系统，容量才会真正可用。"
}

func describeVolumeChange(oldSize, oldVpus, newSize, newVpus int64) string {
	var parts []string
	if oldSize != newSize {
		parts = append(parts, itoa(int(oldSize))+" GB → "+itoa(int(newSize))+" GB")
	}
	if oldVpus != newVpus {
		parts = append(parts, "VPU "+itoa(int(oldVpus))+" → "+itoa(int(newVpus)))
	}
	if len(parts) == 0 {
		return "仅改名"
	}
	return joinWith(parts, "，")
}

func joinWith(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

// instanceUpdatedEvent 构造一条实例更新事件。
func instanceUpdatedEvent(inst *store.Instance) instancesvc.Event {
	return instancesvc.Event{
		Type:       instancesvc.EventInstanceUpdated,
		InstanceID: inst.ID,
		AccountID:  inst.AccountID,
		State:      inst.LifecycleState,
	}
}
