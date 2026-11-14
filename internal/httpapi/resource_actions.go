package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ocicore/internal/i18n"
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
		writeError(w, http.StatusBadRequest, "missing_region", i18n.M("api.missing_region"))
		return nil, nil, "", false
	}
	client, acc, err := s.conns.ForID(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", i18n.M("api.account_not_found"))
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
		writeError(w, http.StatusBadRequest, "invalid_kind", i18n.M("api.invalid_kind"))
		return
	}

	if attached {
		writeError(w, http.StatusConflict, "volume_attached", i18n.M("api.volume_attached"))
		return
	}
	if state != ociclient.VolumeAvailable {
		writeError(w, http.StatusConflict, "volume_busy", i18n.M("api.volume_busy", state))
		return
	}
	want := orText(name, volumeID)
	if strings.TrimSpace(r.URL.Query().Get("confirm")) != want {
		writeError(w, http.StatusBadRequest, "confirm_required",
			i18n.M("api.volume_confirm", want))
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

	label := map[string]string{"boot": i18n.M("api.kind.boot"), "block": i18n.M("api.kind.block")}[kind]
	detail := label + " · " + strconv.FormatInt(size, 10) + " GB · " + region
	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "volume_delete", AccountID: acc.ID,
		Target: want, Detail: detail, IP: s.clientIP(r),
	})
	s.notifier.Dispatch(r.Context(), notify.Message{
		Event: notify.EventDangerOperation,
		Title: i18n.M("api.volume_deleted_title", label, want),
		Body:  i18n.M("api.volume_deleted_body"),
		Fields: map[string]string{
			i18n.M("f.account"): acc.Alias, i18n.M("f.volume"): want, i18n.M("f.size"): strconv.FormatInt(size, 10) + " GB",
			i18n.M("f.region"): region, i18n.M("f.operator"): user.Username,
		},
	})
	s.resources.invalidate(acc.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "notice": i18n.M("api.volume_delete_notice", label, want)})
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
		writeError(w, http.StatusBadRequest, "not_reserved", i18n.M("api.not_reserved"))
		return
	}
	if ip.PrivateIPID != "" || ip.AssignedEntityID != "" {
		writeError(w, http.StatusConflict, "ip_assigned", i18n.M("api.ip_assigned"))
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("confirm")) != ip.IPAddress {
		writeError(w, http.StatusBadRequest, "confirm_required",
			i18n.M("api.ip_release_confirm", ip.IPAddress))
		return
	}

	if err := client.DeletePublicIP(ctx, region, ip.ID); err != nil {
		writeOCIError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "reserved_ip_release", AccountID: acc.ID,
		Target: ip.IPAddress, Detail: orText(ip.DisplayName, i18n.M("api.reserved_ip")) + " · " + region, IP: s.clientIP(r),
	})
	s.notifier.Dispatch(r.Context(), notify.Message{
		Event: notify.EventDangerOperation,
		Title: i18n.M("api.ip_released_title", ip.IPAddress),
		Body:  i18n.M("api.ip_released_body"),
		Fields: map[string]string{
			i18n.M("f.account"): acc.Alias, "IP": ip.IPAddress, i18n.M("f.region"): region, i18n.M("f.operator"): user.Username,
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
		writeError(w, http.StatusBadRequest, "confirm_required", i18n.M("api.console_delete_confirm"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	connID := r.PathValue("connId")

	// 先查出这条连接：要知道它属于哪台实例（审计里记实例名，而不是一长串 OCID），
	// 也要知道它是不是已经在删了。
	conns, err := client.ListConsoleConnections(ctx, region, acc.CompartmentOCID, "")
	if err != nil {
		writeOCIError(w, err)
		return
	}
	var conn *ociclient.InstanceConsoleConnection
	for i := range conns {
		if conns[i].ID == connID {
			conn = &conns[i]
		}
	}
	if conn == nil {
		writeError(w, http.StatusNotFound, "not_found", i18n.M("api.console_not_found"))
		return
	}
	// 删除是异步的：连接会先停在 DELETING 一段时间。这期间再点一次不该再发请求、
	// 再记一条审计——先前就是这样，同一条连接要「删两次」才从列表里消失。
	if conn.LifecycleState == "DELETING" || conn.LifecycleState == "DELETED" {
		s.resources.invalidate(acc.ID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "alreadyDeleting": true})
		return
	}

	if err := client.DeleteConsoleConnection(ctx, region, connID); err != nil {
		writeOCIError(w, err)
		return
	}
	target := conn.InstanceID
	if inst, err := s.st.GetInstance(r.Context(), conn.InstanceID); err == nil {
		target = inst.DisplayName
	}
	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "console_delete", AccountID: acc.ID,
		Target: target, Detail: region, IP: s.clientIP(r),
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
		writeError(w, http.StatusBadRequest, "missing_address", i18n.M("api.missing_address"))
		return
	}
	if r.URL.Query().Get("confirm") != "true" {
		writeError(w, http.StatusBadRequest, "confirm_required",
			i18n.M("api.v6_remove_confirm"))
		return
	}
	client, acc, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}
	who := s.actorOf(r)

	s.submitJob(w, jobsvc.Spec{
		Kind: jobRemoveIPv6, Title: i18n.M("api.remove_v6_title", inst.DisplayName),
		AccountID: inst.AccountID, InstanceID: inst.ID, Timeout: 2 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		p.Step(i18n.M("api.step.find_v6", address))
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
			return jobsvc.Outcome{}, i18n.Err("api.v6_not_found", address)
		}

		p.Step(i18n.M("api.step.delete_v6"))
		if err := client.DeleteIpv6(ctx, inst.Region, target.ID); err != nil {
			return jobsvc.Outcome{}, err
		}
		p.Step(i18n.M("api.step.refresh_net"))
		_ = s.instances.RefreshNetwork(ctx, inst.ID)

		s.audit(who, store.AuditEntry{
			Action: "instance_remove_ipv6", AccountID: inst.AccountID,
			Target: inst.DisplayName, Detail: address,
		})
		return jobsvc.Outcome{Summary: i18n.M("api.v6_removed", address)}, nil
	})
}
