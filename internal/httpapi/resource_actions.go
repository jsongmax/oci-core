package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ocicore/internal/jobsvc"
	"ocicore/internal/notify"
	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

// 资源页的清理操作。
//
// 共同的规矩：
//   - 服务端重新确认资源确实闲置（卷未挂载、保留 IP 未绑定），不信任资源页上
//     可能已过期两分钟的缓存——这段时间里别人可能刚把卷挂上去。
//   - 删卷、释放保留 IP 不可恢复，走 danger（两步验证复核）并要求回传名称 / 地址。
//   - 删控制台连接不丢任何数据，只要求 confirm=true。
//   - 成功后清掉该账号的资源汇总缓存，页面刷新立刻看到结果。

// resourceTarget 解析 /api/accounts/{id}/resources/... 的账号与区域。区域必填。
func (s *Server) resourceTarget(w http.ResponseWriter, r *http.Request) (*ociclient.Client, *store.Account, string, bool) {
	region := ociclient.NormalizeRegion(r.URL.Query().Get("region"))
	if region == "" {
		writeError(w, http.StatusBadRequest, "missing_region", "缺少 region 参数")
		return nil, nil, "", false
	}
	client, acc, err := s.conns.ForID(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "账号不存在")
		} else {
			writeError(w, http.StatusBadGateway, "credentials", err.Error())
		}
		return nil, nil, "", false
	}
	return client, acc, region, true
}

// handleDeleteVolume 删除一块未挂载的引导卷或块存储卷。
//
// 查询参数：region、kind（boot / block）、confirm（回传卷名；卷没有名字时回传 OCID）。
func (s *Server) handleDeleteVolume(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resourceTarget(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	volumeID := r.PathValue("volumeId")
	kind := r.URL.Query().Get("kind")
	var (
		name, state string
		size        int64
		attached    bool
	)
	isAttached := func(s string) bool { return s == "ATTACHED" || s == "ATTACHING" }

	switch kind {
	case "boot":
		v, err := client.GetBootVolume(ctx, region, volumeID)
		if err != nil {
			writeOCIError(w, err)
			return
		}
		name, state, size = v.DisplayName, v.LifecycleState, v.SizeInGBs
		atts, err := client.ListBootVolumeAttachments(ctx, region, acc.CompartmentOCID, v.AvailabilityDomain, "")
		if err != nil {
			writeOCIError(w, err)
			return
		}
		for _, a := range atts {
			if a.BootVolumeID == volumeID && isAttached(a.LifecycleState) {
				attached = true
			}
		}
	case "block":
		v, err := client.GetVolume(ctx, region, volumeID)
		if err != nil {
			writeOCIError(w, err)
			return
		}
		name, state, size = v.DisplayName, v.LifecycleState, v.SizeInGBs
		atts, err := client.ListVolumeAttachments(ctx, region, acc.CompartmentOCID, "")
		if err != nil {
			writeOCIError(w, err)
			return
		}
		for _, a := range atts {
			if a.VolumeID == volumeID && isAttached(a.LifecycleState) {
				attached = true
			}
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid_kind", "kind 只能是 boot 或 block")
		return
	}

	if attached {
		writeError(w, http.StatusConflict, "volume_attached", "这块卷仍挂在实例上，先分离再删除")
		return
	}
	if state != ociclient.VolumeAvailable {
		writeError(w, http.StatusConflict, "volume_busy", "卷当前状态为 "+state+"，只能删除 AVAILABLE 状态的卷")
		return
	}
	want := orText(name, volumeID)
	if strings.TrimSpace(r.URL.Query().Get("confirm")) != want {
		writeError(w, http.StatusBadRequest, "confirm_required",
			"删除卷不可恢复，需要在 confirm 参数中回传卷名 "+strconv.Quote(want))
		return
	}

	if kind == "boot" {
		err := client.DeleteBootVolume(ctx, region, volumeID)
		if err != nil {
			writeOCIError(w, err)
			return
		}
	} else if err := client.DeleteVolume(ctx, region, volumeID); err != nil {
		writeOCIError(w, err)
		return
	}

	label := map[string]string{"boot": "引导卷", "block": "块存储"}[kind]
	detail := label + " · " + strconv.FormatInt(size, 10) + " GB · " + region
	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "volume_delete", AccountID: acc.ID,
		Target: want, Detail: detail, IP: s.clientIP(r),
	})
	s.notifier.Dispatch(r.Context(), notify.Message{
		Event: notify.EventDangerOperation,
		Title: "已删除" + label + " " + want,
		Body:  "卷上的数据不可恢复。",
		Fields: map[string]string{
			"账号": acc.Alias, "卷": want, "容量": strconv.FormatInt(size, 10) + " GB",
			"区域": region, "操作者": user.Username,
		},
	})
	s.resources.invalidate(acc.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "notice": label + " " + want + " 已提交删除，几分钟内从列表消失。"})
}

// handleReleaseReservedIP 释放一个未绑定的保留公网 IP。
//
// 只处理保留 IP：临时 IP 的「释放」就是更换公网 IP，那在实例上做。
// 查询参数：region、confirm（回传 IP 地址）。
func (s *Server) handleReleaseReservedIP(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resourceTarget(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	ip, err := client.GetPublicIP(ctx, region, r.PathValue("ipId"))
	if err != nil {
		writeOCIError(w, err)
		return
	}
	if ip.Lifetime != ociclient.PublicIPReserved {
		writeError(w, http.StatusBadRequest, "not_reserved", "只能在这里释放保留 IP；临时 IP 请在实例上更换")
		return
	}
	if ip.PrivateIPID != "" || ip.AssignedEntityID != "" {
		writeError(w, http.StatusConflict, "ip_assigned", "这个保留 IP 仍绑定在网卡上，先解绑再释放")
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("confirm")) != ip.IPAddress {
		writeError(w, http.StatusBadRequest, "confirm_required",
			"释放后这个地址永久失去，需要在 confirm 参数中回传 IP 地址 "+strconv.Quote(ip.IPAddress))
		return
	}

	if err := client.DeletePublicIP(ctx, region, ip.ID); err != nil {
		writeOCIError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "reserved_ip_release", AccountID: acc.ID,
		Target: ip.IPAddress, Detail: orText(ip.DisplayName, "保留 IP") + " · " + region, IP: s.clientIP(r),
	})
	s.notifier.Dispatch(r.Context(), notify.Message{
		Event: notify.EventDangerOperation,
		Title: "已释放保留 IP " + ip.IPAddress,
		Body:  "这个地址已归还 Oracle，不可找回。",
		Fields: map[string]string{
			"账号": acc.Alias, "IP": ip.IPAddress, "区域": region, "操作者": user.Username,
		},
	})
	s.resources.invalidate(acc.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteConsoleConnection 删除一条串行控制台连接。不丢数据，只要求 confirm=true。
func (s *Server) handleDeleteConsoleConnection(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resourceTarget(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("confirm") != "true" {
		writeError(w, http.StatusBadRequest, "confirm_required", "删除控制台连接需要附带 confirm=true")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	connID := r.PathValue("connId")
	if err := client.DeleteConsoleConnection(ctx, region, connID); err != nil {
		writeOCIError(w, err)
		return
	}
	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "console_delete", AccountID: acc.ID,
		Target: connID, Detail: region, IP: s.clientIP(r),
	})
	s.resources.invalidate(acc.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRemoveIPv6 删除实例网卡上的一个 IPv6 地址。主网卡、附属网卡都可以。
//
// 查询参数：address、confirm=true。删除后这个地址可能被分给别的实例，
// 走 danger 与开启 IPv6 对称。
func (s *Server) handleRemoveIPv6(w http.ResponseWriter, r *http.Request) {
	inst, err := s.st.GetInstance(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	address := strings.TrimSpace(r.URL.Query().Get("address"))
	if address == "" {
		writeError(w, http.StatusBadRequest, "missing_address", "缺少 address 参数")
		return
	}
	if r.URL.Query().Get("confirm") != "true" {
		writeError(w, http.StatusBadRequest, "confirm_required",
			"删除后这个 IPv6 地址不可找回，用它的连接会中断。如确认要执行，请附带 confirm=true。")
		return
	}
	client, acc, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}
	who := s.actorOf(r)

	s.submitJob(w, jobsvc.Spec{
		Kind: jobRemoveIPv6, Title: "移除 IPv6 · " + inst.DisplayName,
		AccountID: inst.AccountID, InstanceID: inst.ID, Timeout: 2 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		p.Step("在实例的网卡上查找 " + address)
		atts, err := client.ListVnicAttachments(ctx, inst.Region, acc.CompartmentOCID, inst.ID)
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		var target *ociclient.Ipv6
		for _, a := range atts {
			if a.LifecycleState != "ATTACHED" {
				continue
			}
			v6s, err := client.ListIpv6s(ctx, inst.Region, a.VnicID)
			if err != nil {
				return jobsvc.Outcome{}, err
			}
			for i := range v6s {
				if strings.EqualFold(v6s[i].IPAddress, address) {
					target = &v6s[i]
				}
			}
		}
		if target == nil {
			return jobsvc.Outcome{}, errors.New("实例的网卡上没有找到 " + address + "，可能已经被删除")
		}

		p.Step("删除 IPv6 地址")
		if err := client.DeleteIpv6(ctx, inst.Region, target.ID); err != nil {
			return jobsvc.Outcome{}, err
		}
		p.Step("刷新网络信息")
		_ = s.instances.RefreshNetwork(ctx, inst.ID)

		s.audit(who, store.AuditEntry{
			Action: "instance_remove_ipv6", AccountID: inst.AccountID,
			Target: inst.DisplayName, Detail: address,
		})
		return jobsvc.Outcome{Summary: "已移除 IPv6 " + address}, nil
	})
}
