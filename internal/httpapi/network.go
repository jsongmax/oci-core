package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"ocicore/internal/instancesvc"
	"ocicore/internal/jobsvc"
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

	out := make([]annotatedSecurityList, 0, len(lists))
	for _, list := range lists {
		out = append(out, annotateSecurityList(list))
	}
	writeJSON(w, http.StatusOK, map[string]any{"securityLists": out})
}

// annotatedSecurityList 是带风险标注的安全列表。
type annotatedSecurityList struct {
	ociclient.SecurityList
	// AllowAllRules 是把全部端口开给整个公网的入站规则下标，前端据此打警示标记。
	AllowAllRules []int `json:"allowAllRules"`
	// NoEgress 表示这个安全列表一条出站规则都没有。
	//
	// 安全列表是白名单，没有出站规则 = 该子网的所有对外流量被丢弃：
	// 装不了包、解析不了 DNS，而实例状态一切正常。这种故障很难自己想到，
	// 界面必须主动说出来。
	NoEgress bool `json:"noEgress"`
}

// annotateSecurityList 计算风险标注。
//
// 列表接口与保存接口必须用同一份：保存接口以前直接回原始对象，前端拿它
// 覆盖本地数据后警示标记全部消失，刚加上的危险规则反而不显示红色，
// 要刷新页面才出来。
func annotateSecurityList(list ociclient.SecurityList) annotatedSecurityList {
	a := annotatedSecurityList{SecurityList: list, NoEgress: !netsvc.HasEgress(list)}
	for i, rule := range list.IngressSecurityRules {
		if netsvc.IsAllowAllRule(rule) {
			a.AllowAllRules = append(a.AllowAllRules, i)
		}
	}
	return a
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
	// 出站清空会让整个子网断网，事后排查时这一行就是线索。
	if len(req.Egress) == 0 {
		detail += "（已无出站规则，该子网将无法访问外网）"
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "security_list_update",
		AccountID: r.URL.Query().Get("accountId"), Target: updated.DisplayName,
		Detail: detail, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, annotateSecurityList(*updated))
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

// handleEnsureNetwork 在目标区域准备好一个可用的公网子网。建网要连着建
// VCN、子网、网关和路由，可能要一两分钟，放到后台任务里跑。
func (s *Server) handleEnsureNetwork(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}

	enableIPv6 := r.URL.Query().Get("ipv6") == "true"
	who := s.actorOf(r)
	title := "准备网络 · " + region
	if enableIPv6 {
		title += "（含 IPv6）"
	}

	s.submitJob(w, jobsvc.Spec{
		Kind: jobEnsureNet, Title: title, AccountID: acc.ID, Timeout: 5 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		p.Step("检查 VCN / 子网 / 网关 / 路由")
		result, err := netsvc.EnsureNetwork(ctx, client, netsvc.EnsureNetworkOptions{
			Region:        region,
			CompartmentID: acc.CompartmentOCID,
			EnableIPv6:    enableIPv6,
		})
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		for _, step := range result.Steps {
			p.Step(step)
		}
		p.Set(70)

		if enableIPv6 {
			prep, err := netsvc.PrepareIPv6(ctx, client, region, result.SubnetID)
			if err != nil {
				return jobsvc.Outcome{Result: result}, errors.New("子网已就绪，但没能开通 IPv6：" + ipv6ErrText(err))
			}
			result.Steps = append(result.Steps, prep.Steps...)
			for _, step := range prep.Steps {
				p.Step(step)
			}
			for _, warn := range prep.Warnings {
				result.Steps = append(result.Steps, "⚠ "+warn)
				p.Step("⚠ " + warn)
			}
		}

		s.audit(who, store.AuditEntry{
			Action: "network_ensure", AccountID: acc.ID,
			Target: region, Detail: strings.Join(result.Steps, "；"),
		})
		return jobsvc.Outcome{Summary: "子网 " + result.SubnetName + " 已就绪", Result: result}, nil
	})
}

// handleChangePublicIP 更换实例某张网卡的公网 IP。这是 L2 级操作。
//
// 查询参数 vnicId 可选，留空为主网卡。网卡上还没有公网 IP 时就是直接分配一个。
// 确认与凭据在请求里校验，换 IP 本身（删旧地址、等释放、申请新地址）
// 交给后台任务。
func (s *Server) handleChangePublicIP(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("id")

	inst, err := s.st.GetInstance(r.Context(), instanceID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	vnicID := strings.TrimSpace(r.URL.Query().Get("vnicId"))
	primary := vnicID == "" || vnicID == inst.VnicID
	if primary {
		vnicID = inst.VnicID
	}
	if vnicID == "" {
		writeError(w, http.StatusBadRequest, "no_vnic",
			"尚未同步到该实例的网卡信息，请先执行一次同步")
		return
	}

	// L2 门槛：原 IP 不可找回，SSH 会立刻中断，必须显式确认。
	if r.URL.Query().Get("confirm") != "true" {
		msg := "更换后原公网 IP 不可找回，用它的连接会中断。"
		if primary && inst.PublicIP != "" {
			msg = "更换后原 IP " + inst.PublicIP + " 不可找回，当前 SSH 连接会中断。"
		}
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code:    "confirm_required",
			Message: msg + "如确认要执行，请附带 confirm=true。",
		})
		return
	}

	client, acc, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}
	who := s.actorOf(r)

	title := "更换公网 IP · " + inst.DisplayName
	if !primary {
		title += "（附属网卡）"
	}
	s.submitJob(w, jobsvc.Spec{
		Kind: jobChangeIP, Title: title,
		AccountID: inst.AccountID, InstanceID: inst.ID, Timeout: 3 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		where := "主网卡"
		if !primary {
			p.Step("确认网卡属于该实例")
			vnic, err := findInstanceVnic(ctx, client, inst, acc.CompartmentOCID, vnicID)
			if err != nil {
				return jobsvc.Outcome{}, err
			}
			where = vnicLabel(vnic)
		}
		if primary && inst.PublicIP != "" {
			p.Step("释放原公网 IP " + inst.PublicIP + " 并申请新地址")
		} else {
			p.Step("为" + where + "申请公网 IP（已有临时 IP 则先释放）")
		}
		p.Set(20)

		result, err := netsvc.ChangePublicIP(ctx, client, inst.Region, vnicID, acc.CompartmentOCID)
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		p.Step("新地址 " + result.NewIP + " 已分配")

		if primary {
			// 立刻把新 IP 写回缓存，用户不必等下一轮同步才看到。重新读一次：
			// 任务排队期间这一行可能已被同步改过，不能拿请求时的旧快照覆盖。
			if fresh, err := s.st.GetInstance(ctx, inst.ID); err == nil {
				fresh.PublicIP = result.NewIP
				if err := s.st.UpsertInstance(ctx, *fresh); err == nil {
					s.instances.Bus().Publish(instanceUpdatedEvent(fresh))
				}
			}
		} else {
			// 附属网卡的地址记在 secondary 列里，整体重读一次网络信息。
			_ = s.instances.RefreshNetwork(ctx, inst.ID)
		}

		change := result.OldIP + " → " + result.NewIP
		if result.OldIP == "" {
			change = "新分配 " + result.NewIP
		}
		s.audit(who, store.AuditEntry{
			Action: "instance_change_ip", AccountID: inst.AccountID,
			Target: inst.DisplayName, Detail: where + " · " + change,
		})
		s.notifier.Dispatch(ctx, notify.Message{
			Event: notify.EventDangerOperation,
			Title: "实例 " + inst.DisplayName + " 的公网 IP 已更换",
			Body:  "原有 SSH 连接已中断，请使用新地址重新连接。",
			Fields: map[string]string{
				"实例":   inst.DisplayName,
				"原 IP": result.OldIP,
				"新 IP": result.NewIP,
				"操作者":  who.username,
			},
		})

		summary := "新 IP " + result.NewIP
		if result.OldIP != "" {
			summary = result.OldIP + " → " + result.NewIP
		}
		return jobsvc.Outcome{Summary: summary, Result: result}, nil
	})
}

// handleEnableIPv6 为实例分配 IPv6 地址。要依次给 VCN、子网开前缀再分配地址，
// 放到后台任务里跑。
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
	who := s.actorOf(r)

	s.submitJob(w, jobsvc.Spec{
		Kind: jobEnableIPv6, Title: "启用 IPv6 · " + inst.DisplayName,
		AccountID: inst.AccountID, InstanceID: inst.ID, Timeout: 3 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		p.Step("为 VCN、子网开通 IPv6 前缀并给网卡分配地址")
		result, err := netsvc.EnableIPv6(ctx, client, inst.Region, inst.VnicID, inst.SubnetID)
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		for _, step := range result.Steps {
			p.Step(step)
		}

		if fresh, err := s.st.GetInstance(ctx, inst.ID); err == nil {
			fresh.IPv6 = result.Address
			if err := s.st.UpsertInstance(ctx, *fresh); err == nil {
				s.instances.Bus().Publish(instanceUpdatedEvent(fresh))
			}
		}

		s.audit(who, store.AuditEntry{
			Action: "instance_enable_ipv6", AccountID: inst.AccountID,
			Target: inst.DisplayName, Detail: result.Address,
		})
		return jobsvc.Outcome{Summary: "已分配 IPv6 " + result.Address, Result: result}, nil
	})
}

// findInstanceVnic 确认网卡确实挂在这台实例上并取回它。
//
// 网卡 OCID 来自客户端，不校验的话，凭一个 OCID 就能改同账号里别的实例的地址。
func findInstanceVnic(ctx context.Context, client *ociclient.Client, inst *store.Instance,
	compartmentID, vnicID string) (*ociclient.Vnic, error) {
	atts, err := client.ListVnicAttachments(ctx, inst.Region, compartmentID, inst.ID)
	if err != nil {
		return nil, err
	}
	for _, a := range atts {
		if a.VnicID == vnicID && a.LifecycleState == "ATTACHED" {
			return client.GetVnic(ctx, inst.Region, vnicID)
		}
	}
	return nil, errors.New("这张网卡不在该实例上，或已经分离")
}

// handleAddIPv6 给实例的某张网卡（主网卡或附属网卡）再分配一个 IPv6 地址。
//
// 与 enable-ipv6 不同，网卡已有地址时照样新分配一个。子网、VCN 没开通 IPv6 时
// 一并开通——VCN 开通后关不掉，所以和 enable-ipv6 一样走 danger。
func (s *Server) handleAddIPv6(w http.ResponseWriter, r *http.Request) {
	inst, err := s.st.GetInstance(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	vnicID := r.PathValue("vnicId")
	client, acc, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}
	who := s.actorOf(r)

	s.submitJob(w, jobsvc.Spec{
		Kind: jobAddIPv6, Title: "添加 IPv6 · " + inst.DisplayName,
		AccountID: inst.AccountID, InstanceID: inst.ID, Timeout: 3 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		p.Step("确认网卡属于该实例")
		vnic, err := findInstanceVnic(ctx, client, inst, acc.CompartmentOCID, vnicID)
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		p.Set(20)
		p.Step("检查子网的 IPv6 前缀并分配地址")
		result, err := netsvc.AddIPv6(ctx, client, inst.Region, vnic.ID, vnic.SubnetID)
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		for _, step := range result.Steps {
			p.Step(step)
		}
		_ = s.instances.RefreshNetwork(ctx, inst.ID)

		s.audit(who, store.AuditEntry{
			Action: "instance_add_ipv6", AccountID: inst.AccountID,
			Target: inst.DisplayName, Detail: vnicLabel(vnic) + " · " + result.Address,
		})
		return jobsvc.Outcome{Summary: "已分配 IPv6 " + result.Address, Result: result}, nil
	})
}

// vnicLabel 是审计与任务里称呼一张网卡的方式。
func vnicLabel(v *ociclient.Vnic) string {
	kind := "附属网卡"
	if v.IsPrimary {
		kind = "主网卡"
	}
	if v.DisplayName != "" {
		return kind + " " + v.DisplayName
	}
	return kind
}

// vnicSettleTimeout 比 instancesvc 里等网卡落定的上限略长，让那边先给出结论。
const vnicSettleTimeout = 5 * time.Minute

// waitVnicSettled 等 instancesvc 推送这台实例的网卡落定事件。
// 订阅必须在发起挂载/分离之前建立，否则可能错过很快就落定的那一条。
func waitVnicSettled(ctx context.Context, events <-chan instancesvc.Event, instanceID string) (string, error) {
	timeout := time.NewTimer(vnicSettleTimeout)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timeout.C:
			return "", errors.New("等待网卡状态落定超时，请稍后在实例详情里确认")
		case e, ok := <-events:
			if !ok {
				return "", errors.New("事件流已关闭")
			}
			if e.Type == instancesvc.EventInstanceNetwork && e.InstanceID == instanceID {
				return e.State, nil
			}
		}
	}
}

// handleAttachVnic 为实例添加附属网卡。校验子网、开通 IPv6、挂载、等落定都在后台任务里。
func (s *Server) handleAttachVnic(w http.ResponseWriter, r *http.Request) {
	var req instancesvc.AttachVnicRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.SubnetID) == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "请选择子网")
		return
	}
	inst, err := s.st.GetInstance(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	who := s.actorOf(r)

	s.submitJob(w, jobsvc.Spec{
		Kind: jobAttachVnic, Title: "添加附属网卡 · " + inst.DisplayName,
		AccountID: inst.AccountID, InstanceID: inst.ID, Timeout: 8 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		events, unsubscribe := s.instances.Bus().Subscribe()
		defer unsubscribe()

		p.Step("校验网卡数量与子网")
		_, result, err := s.instances.AttachVnic(ctx, inst.ID, req)
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		for _, warn := range result.Warnings {
			p.Step("⚠ " + warn)
		}

		detail := "子网 " + req.SubnetID
		if req.AssignPublicIP {
			detail += "，分配公网 IPv4"
		}
		if req.AssignIPv6 {
			detail += "，分配 IPv6"
		}
		s.audit(who, store.AuditEntry{
			Action: "instance_attach_vnic", AccountID: inst.AccountID,
			Target: inst.DisplayName, Detail: detail,
		})

		p.Step("已提交挂载，等待网卡就绪")
		p.Set(40)
		state, err := waitVnicSettled(ctx, events, inst.ID)
		if err != nil {
			return jobsvc.Outcome{Result: result}, err
		}
		result.State = state
		if state != "ATTACHED" {
			return jobsvc.Outcome{Result: result}, errors.New("网卡没有挂上，Oracle 返回的状态是 " + state)
		}
		return jobsvc.Outcome{Summary: "附属网卡已挂载", Result: result}, nil
	})
}

// handleDetachVnic 分离一张附属网卡。网卡上的临时公网 IP 随之释放，是 L2 级操作。
func (s *Server) handleDetachVnic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("confirm") != "true" {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code:    "confirm_required",
			Message: "分离后这张网卡上的临时公网 IP 会被释放，不可找回。如确认要执行，请附带 confirm=true。",
		})
		return
	}
	inst, err := s.st.GetInstance(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	attachmentID := r.PathValue("attachmentId")
	who := s.actorOf(r)

	s.submitJob(w, jobsvc.Spec{
		Kind: jobDetachVnic, Title: "分离附属网卡 · " + inst.DisplayName,
		AccountID: inst.AccountID, InstanceID: inst.ID, Timeout: 8 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		events, unsubscribe := s.instances.Bus().Subscribe()
		defer unsubscribe()

		_, vnic, err := s.instances.DetachVnic(ctx, inst.ID, attachmentID)
		if err != nil {
			return jobsvc.Outcome{}, err
		}

		name := vnic.DisplayName
		if name == "" {
			name = vnic.ID
		}
		detail := name + " · 私网 " + vnic.PrivateIP
		if vnic.PublicIP != "" {
			detail += " · 公网 " + vnic.PublicIP
		}
		p.Step("已提交分离：" + detail)
		p.Set(30)

		s.audit(who, store.AuditEntry{
			Action: "instance_detach_vnic", AccountID: inst.AccountID,
			Target: inst.DisplayName, Detail: detail,
		})
		fields := map[string]string{"实例": inst.DisplayName, "网卡": name, "操作者": who.username}
		if vnic.PublicIP != "" {
			fields["释放的公网 IP"] = vnic.PublicIP
		}
		s.notifier.Dispatch(ctx, notify.Message{
			Event:  notify.EventDangerOperation,
			Title:  "实例 " + inst.DisplayName + " 分离了一张附属网卡",
			Body:   "网卡上的临时公网 IP 已释放。",
			Fields: fields,
		})

		p.Step("等待网卡下线")
		state, err := waitVnicSettled(ctx, events, inst.ID)
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		if state != "DETACHED" {
			return jobsvc.Outcome{}, errors.New("网卡分离没有完成，状态 " + state)
		}
		return jobsvc.Outcome{Summary: "网卡 " + name + " 已分离"}, nil
	})
}

func writeVnicError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, instancesvc.ErrVnicLimit):
		writeError(w, http.StatusBadRequest, "vnic_limit", err.Error())
	case errors.Is(err, instancesvc.ErrPrimaryVnic):
		writeError(w, http.StatusBadRequest, "primary_vnic", err.Error())
	case errors.Is(err, instancesvc.ErrVnicNotFound):
		writeError(w, http.StatusNotFound, "vnic_not_found", err.Error())
	case errors.Is(err, instancesvc.ErrVnicInvalid):
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
	default:
		writeInstanceError(w, err)
	}
}
