package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"ocicore/internal/netsvc"
	"ocicore/internal/notify"
	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

// resolveTarget 从查询参数解析出账号、区域与客户端。
//
// 网络与存储的接口都要"在某个账号的某个区域里操作"，
// 这段解析在每个 handler 里重复一遍太啰嗦，抽出来统一处理。
func (s *Server) resolveTarget(w http.ResponseWriter, r *http.Request) (*ociclient.Client, *store.Account, string, bool) {
	accountID := r.URL.Query().Get("accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, "missing_account", "缺少 accountId 参数")
		return nil, nil, "", false
	}

	client, acc, err := s.conns.ForID(r.Context(), accountID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "账号不存在")
		} else {
			writeError(w, http.StatusBadGateway, "credentials", err.Error())
		}
		return nil, nil, "", false
	}

	region := ociclient.NormalizeRegion(r.URL.Query().Get("region"))
	if region == "" {
		region = acc.DefaultRegion
	}
	return client, acc, region, true
}

func (s *Server) handleListVcns(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	vcns, err := client.ListVcns(r.Context(), region, acc.CompartmentOCID)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"vcns": vcns})
}

func (s *Server) handleListSubnets(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	subnets, err := client.ListSubnets(r.Context(), region, acc.CompartmentOCID, r.URL.Query().Get("vcnId"))
	if err != nil {
		writeOCIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subnets": subnets})
}

func (s *Server) handleListSecurityLists(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	lists, err := client.ListSecurityLists(r.Context(), region, acc.CompartmentOCID, r.URL.Query().Get("vcnId"))
	if err != nil {
		writeOCIError(w, err)
		return
	}

	// 标出等同于"全部放行"的规则，让前端能打醒目的警示标记。
	type annotated struct {
		ociclient.SecurityList
		AllowAllRules []int `json:"allowAllRules"`
	}
	out := make([]annotated, 0, len(lists))
	for _, list := range lists {
		a := annotated{SecurityList: list}
		for i, rule := range list.IngressSecurityRules {
			if netsvc.IsAllowAllRule(rule) {
				a.AllowAllRules = append(a.AllowAllRules, i)
			}
		}
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, map[string]any{"securityLists": out})
}

type updateSecurityListRequest struct {
	Ingress []ociclient.IngressSecurityRule `json:"ingress"`
	Egress  []ociclient.EgressSecurityRule  `json:"egress"`
}

// handleUpdateSecurityList 覆盖写安全规则。
//
// OCI 的语义是整体替换而非增量追加，因此前端必须提交完整规则集。
// 这一点在接口文档里要写清楚，否则很容易把没提交的规则静默删掉。
func (s *Server) handleUpdateSecurityList(w http.ResponseWriter, r *http.Request) {
	client, _, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	var req updateSecurityListRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	listID := r.PathValue("id")
	updated, err := client.UpdateSecurityList(r.Context(), region, listID, req.Ingress, req.Egress)
	if err != nil {
		writeOCIError(w, err)
		return
	}

	// 全放行是高风险变更，审计日志里必须留痕。
	dangerous := 0
	for _, rule := range req.Ingress {
		if netsvc.IsAllowAllRule(rule) {
			dangerous++
		}
	}
	detail := "入站 " + itoa(len(req.Ingress)) + " 条，出站 " + itoa(len(req.Egress)) + " 条"
	if dangerous > 0 {
		detail += "（含 " + itoa(dangerous) + " 条全放行规则）"
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "security_list_update",
		AccountID: r.URL.Query().Get("accountId"), Target: updated.DisplayName,
		Detail: detail, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleRuleTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"templates": netsvc.RuleTemplates()})
}

func (s *Server) handleListPublicIPs(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "REGION" // 保留 IP
	}
	ips, err := client.ListPublicIPs(r.Context(), region, acc.CompartmentOCID,
		scope, r.URL.Query().Get("availabilityDomain"))
	if err != nil {
		writeOCIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"publicIps": ips})
}

// handleEnsureNetwork 在目标区域准备好一个可用的公网子网。
func (s *Server) handleEnsureNetwork(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	result, err := netsvc.EnsureNetwork(ctx, client, netsvc.EnsureNetworkOptions{
		Region:        region,
		CompartmentID: acc.CompartmentOCID,
		EnableIPv6:    r.URL.Query().Get("ipv6") == "true",
	})
	if err != nil {
		writeOCIError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "network_ensure", AccountID: acc.ID,
		Target: region, Detail: strings.Join(result.Steps, "；"), IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, result)
}

// handleChangePublicIP 更换实例的公网 IP。这是 L2 级操作。
func (s *Server) handleChangePublicIP(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("id")

	inst, err := s.st.GetInstance(r.Context(), instanceID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if inst.VnicID == "" {
		writeError(w, http.StatusBadRequest, "no_vnic",
			"尚未同步到该实例的网卡信息，请先执行一次同步")
		return
	}

	// L2 门槛：原 IP 不可找回，SSH 会立刻中断，必须显式确认。
	if r.URL.Query().Get("confirm") != "true" {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: "confirm_required",
			Message: "更换后原 IP " + inst.PublicIP + " 不可找回，当前 SSH 连接会中断。" +
				"如确认要执行，请附带 confirm=true。",
		})
		return
	}

	client, acc, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	result, err := netsvc.ChangePublicIP(ctx, client, inst.Region, inst.VnicID, acc.CompartmentOCID)
	if err != nil {
		if errors.Is(err, netsvc.ErrReservedIP) {
			writeError(w, http.StatusBadRequest, "reserved_ip", err.Error())
			return
		}
		writeOCIError(w, err)
		return
	}

	// 立刻把新 IP 写回缓存，用户不必等下一轮同步才看到。
	inst.PublicIP = result.NewIP
	if err := s.st.UpsertInstance(r.Context(), *inst); err == nil {
		s.instances.Bus().Publish(instanceUpdatedEvent(inst))
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "instance_change_ip", AccountID: inst.AccountID,
		Target: inst.DisplayName, Detail: result.OldIP + " → " + result.NewIP, IP: s.clientIP(r),
	})
	s.notifier.Dispatch(r.Context(), notify.Message{
		Event: notify.EventDangerOperation,
		Title: "实例 " + inst.DisplayName + " 的公网 IP 已更换",
		Body:  "原有 SSH 连接已中断，请使用新地址重新连接。",
		Fields: map[string]string{
			"实例":   inst.DisplayName,
			"原 IP": result.OldIP,
			"新 IP": result.NewIP,
			"操作者":  user.Username,
		},
	})
	writeJSON(w, http.StatusOK, result)
}

// handleEnableIPv6 为实例分配 IPv6 地址。
func (s *Server) handleEnableIPv6(w http.ResponseWriter, r *http.Request) {
	inst, err := s.st.GetInstance(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if inst.VnicID == "" || inst.SubnetID == "" {
		writeError(w, http.StatusBadRequest, "no_vnic",
			"尚未同步到该实例的网络信息，请先执行一次同步")
		return
	}

	client, _, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	result, err := netsvc.EnableIPv6(ctx, client, inst.Region, inst.VnicID, inst.SubnetID)
	if err != nil {
		writeOCIError(w, err)
		return
	}

	inst.IPv6 = result.Address
	if err := s.st.UpsertInstance(r.Context(), *inst); err == nil {
		s.instances.Bus().Publish(instanceUpdatedEvent(inst))
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "instance_enable_ipv6", AccountID: inst.AccountID,
		Target: inst.DisplayName, Detail: result.Address, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, result)
}
