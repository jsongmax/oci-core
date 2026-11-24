package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"ocicore/internal/i18n"
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
		writeError(w, http.StatusBadRequest, "missing_account", i18n.M("api.missing_account"))
		return nil, nil, "", false
	}

	client, acc, err := s.conns.ForID(r.Context(), accountID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", i18n.M("api.account_not_found"))
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
		writeOCIErrorWithPermHint(w, err, i18n.M("oci.net_perm_hint"))
		return
	}

	// 全放行是高风险变更，审计日志里必须留痕。
	dangerous := 0
	for _, rule := range req.Ingress {
		if netsvc.IsAllowAllRule(rule) {
			dangerous++
		}
	}
	detail := i18n.M("api.rules_detail", len(req.Ingress), len(req.Egress))
	if dangerous > 0 {
		detail += i18n.M("api.rules_danger", dangerous)
	}
	// 出站清空会让整个子网断网，事后排查时这一行就是线索。
	if len(req.Egress) == 0 {
		detail += i18n.M("api.rules_no_egress")
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

// boundPublicIP 是带实例与网卡/私网归属信息的公网 IPv4，供网络页「公网 IP」列表使用。
type boundPublicIP struct {
	ociclient.PublicIP
	InstanceID         string `json:"instanceId,omitempty"`
	InstanceName       string `json:"instanceName,omitempty"`
	InstanceState      string `json:"instanceState,omitempty"`
	VnicID             string `json:"vnicId,omitempty"`
	VnicName           string `json:"vnicName,omitempty"`
	IsPrimaryVnic      bool   `json:"isPrimaryVnic"`
	PrivateIP          string `json:"privateIp,omitempty"`
	IsPrimaryPrivateIP bool   `json:"isPrimaryPrivateIp"`
}

func (s *Server) handleListPublicIPs(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	scope := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("scope")))
	if scope == "" {
		scope = "REGION" // 保留 IP
	}
	if scope == "ALL" {
		items, err := s.listBoundPublicIPs(r.Context(), client, acc, region)
		if err != nil {
			writeOCIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"publicIps": items})
		return
	}
	if scope == "RESERVED" {
		items, err := s.listReservedPublicIPs(r.Context(), client, acc, region)
		if err != nil {
			writeOCIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"publicIps": items})
		return
	}
	ips, err := client.ListPublicIPs(r.Context(), region, acc.CompartmentOCID,
		scope, r.URL.Query().Get("availabilityDomain"))
	if err != nil {
		writeOCIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"publicIps": ips})
}

func boundPublicIPPrivateID(pub ociclient.PublicIP) string {
	if pub.PrivateIPID != "" {
		return pub.PrivateIPID
	}
	if strings.HasPrefix(pub.AssignedEntityID, "ocid1.privateip.") {
		return pub.AssignedEntityID
	}
	return ""
}

func compareIPAddr(a, b string) int {
	addrA, errA := netip.ParseAddr(a)
	addrB, errB := netip.ParseAddr(b)
	if errA == nil && errB == nil {
		return addrA.Compare(addrB)
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func (s *Server) refreshInstanceAfterIPMutation(ctx context.Context, instID, accountID string) {
	rfCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if s.instances != nil && instID != "" {
		_ = s.instances.RefreshNetwork(rfCtx, instID)
	}
	if s.resources != nil && accountID != "" {
		s.resources.invalidate(accountID)
	}
}

// resolveBoundReservedPrivateIPs 解析已绑定预留 IP 对应的私网槽位。
// 已命中实例主公网 IP 的预留 IP 直接复用缓存信息，避免多余的 GetPrivateIP 调用；
// 单个 GetPrivateIP 失败时不回退到全区 VNIC 串行 ListPrivateIPs，防止触发限流放大。
func resolveBoundReservedPrivateIPs(ctx context.Context, client *ociclient.Client, region string,
	boundReserved []ociclient.PublicIP, instByPrimaryPub map[string]store.Instance) map[string]ociclient.PrivateIP {

	privByID := make(map[string]ociclient.PrivateIP, len(boundReserved))
	toLookup := make([]string, 0, len(boundReserved))
	for _, pub := range boundReserved {
		privID := boundPublicIPPrivateID(pub)
		if privID == "" {
			continue
		}
		if inst, ok := instByPrimaryPub[pub.IPAddress]; ok && inst.VnicID != "" {
			privByID[privID] = ociclient.PrivateIP{
				ID:        privID,
				VnicID:    inst.VnicID,
				IPAddress: inst.PrivateIP,
				IsPrimary: true,
			}
			continue
		}
		toLookup = append(toLookup, privID)
	}
	if len(toLookup) == 0 {
		return privByID
	}

	var (
		privMu  sync.Mutex
		privWG  sync.WaitGroup
		privSem = make(chan struct{}, 4)
	)
	for _, privID := range toLookup {
		privWG.Add(1)
		go func(privID string) {
			defer privWG.Done()
			privSem <- struct{}{}
			defer func() { <-privSem }()
			priv, err := client.GetPrivateIP(ctx, region, privID)
			if err != nil || priv == nil {
				return
			}
			privMu.Lock()
			privByID[privID] = *priv
			privMu.Unlock()
		}(privID)
	}
	privWG.Wait()
	return privByID
}

func (s *Server) listBoundPublicIPs(ctx context.Context, client *ociclient.Client, acc *store.Account, region string) ([]boundPublicIP, error) {
	insts, err := s.st.ListInstances(ctx, store.InstanceFilter{
		AccountIDs: []string{acc.ID},
		Regions:    []string{region},
	})
	if err != nil {
		return nil, err
	}
	if len(insts) == 0 {
		return make([]boundPublicIP, 0), nil
	}

	reserved, err := client.ListPublicIPs(ctx, region, acc.CompartmentOCID, "REGION", "")
	if err != nil {
		return nil, err
	}

	reservedByIP := make(map[string]ociclient.PublicIP, len(reserved))
	boundReserved := make([]ociclient.PublicIP, 0, len(reserved))
	for _, pub := range reserved {
		if pub.IPAddress == "" {
			continue
		}
		reservedByIP[pub.IPAddress] = pub
		if boundPublicIPPrivateID(pub) != "" {
			boundReserved = append(boundReserved, pub)
		}
	}

	instByID := make(map[string]store.Instance, len(insts))
	instByPrimaryVnic := make(map[string]store.Instance, len(insts))
	instByPrimaryPub := make(map[string]store.Instance, len(insts))
	for _, inst := range insts {
		instByID[inst.ID] = inst
		if inst.VnicID != "" {
			instByPrimaryVnic[inst.VnicID] = inst
		}
		if inst.PublicIP != "" {
			instByPrimaryPub[inst.PublicIP] = inst
		}
	}

	privByID := resolveBoundReservedPrivateIPs(ctx, client, region, boundReserved, instByPrimaryPub)

	needAttachments := false
	instNeedSecVnic := make(map[string]bool)
	unknownVnicIDs := make(map[string]bool)
	for _, priv := range privByID {
		if priv.VnicID != "" {
			if _, ok := instByPrimaryVnic[priv.VnicID]; !ok {
				needAttachments = true
				unknownVnicIDs[priv.VnicID] = true
			}
		}
	}
	for _, inst := range insts {
		for _, ip := range splitCSV(inst.SecondaryIPv4) {
			if _, isReserved := reservedByIP[ip]; !isReserved {
				needAttachments = true
				instNeedSecVnic[inst.ID] = true
			}
		}
	}

	vnicToInst := make(map[string]store.Instance, len(instByPrimaryVnic))
	for vnicID, inst := range instByPrimaryVnic {
		vnicToInst[vnicID] = inst
	}
	secVnicsByInst := make(map[string][]ociclient.Vnic)
	vnicByID := make(map[string]ociclient.Vnic)

	if needAttachments {
		if atts, err := client.ListVnicAttachments(ctx, region, acc.CompartmentOCID, ""); err == nil {
			var toFetch []ociclient.VnicAttachment
			for _, att := range atts {
				if att.LifecycleState != "ATTACHED" || att.VnicID == "" {
					continue
				}
				inst, ok := instByID[att.InstanceID]
				if !ok {
					continue
				}
				vnicToInst[att.VnicID] = inst
				isSecondaryVnic := inst.VnicID == "" || att.VnicID != inst.VnicID
				if isSecondaryVnic && (instNeedSecVnic[inst.ID] || unknownVnicIDs[att.VnicID]) {
					toFetch = append(toFetch, att)
				}
			}
			var (
				vnicMu  sync.Mutex
				vnicWG  sync.WaitGroup
				vnicSem = make(chan struct{}, 4)
			)
			for _, att := range toFetch {
				vnicWG.Add(1)
				go func(att ociclient.VnicAttachment) {
					defer vnicWG.Done()
					vnicSem <- struct{}{}
					defer func() { <-vnicSem }()
					vnic, err := client.GetVnic(ctx, region, att.VnicID)
					if err != nil || vnic == nil {
						return
					}
					vnicMu.Lock()
					vnicByID[vnic.ID] = *vnic
					if !vnic.IsPrimary {
						secVnicsByInst[att.InstanceID] = append(secVnicsByInst[att.InstanceID], *vnic)
					}
					vnicMu.Unlock()
				}(att)
			}
			vnicWG.Wait()
		}
	}

	type resolvedReserved struct {
		pub  ociclient.PublicIP
		priv ociclient.PrivateIP
	}
	reservedByInst := make(map[string][]resolvedReserved)
	for _, pub := range boundReserved {
		privID := boundPublicIPPrivateID(pub)
		priv, ok := privByID[privID]
		if !ok || priv.VnicID == "" {
			continue
		}
		inst, ok := vnicToInst[priv.VnicID]
		if !ok {
			continue
		}
		reservedByInst[inst.ID] = append(reservedByInst[inst.ID], resolvedReserved{pub: pub, priv: priv})
	}

	out := make([]boundPublicIP, 0, len(insts)+len(boundReserved))
	for _, inst := range insts {
		seenIP := make(map[string]bool)

		if inst.PublicIP != "" {
			row := boundPublicIP{
				PublicIP: ociclient.PublicIP{
					ID:             "primary:" + inst.ID,
					IPAddress:      inst.PublicIP,
					Lifetime:       ociclient.PublicIPEphemeral,
					Scope:          "AVAILABILITY_DOMAIN",
					LifecycleState: "ASSIGNED",
				},
				InstanceID:         inst.ID,
				InstanceName:       inst.DisplayName,
				InstanceState:      inst.LifecycleState,
				VnicID:             inst.VnicID,
				IsPrimaryVnic:      true,
				PrivateIP:          inst.PrivateIP,
				IsPrimaryPrivateIP: true,
			}
			if pub, ok := reservedByIP[inst.PublicIP]; ok {
				privID := boundPublicIPPrivateID(pub)
				if priv, hasPriv := privByID[privID]; !hasPriv || priv.IsPrimary {
					row.PublicIP = pub
					row.PrivateIPID = privID
					if hasPriv && priv.IPAddress != "" {
						row.PrivateIP = priv.IPAddress
					}
				}
			}
			out = append(out, row)
			seenIP[inst.PublicIP] = true
		}

		secVnics := secVnicsByInst[inst.ID]
		sort.Slice(secVnics, func(i, j int) bool {
			if secVnics[i].DisplayName != secVnics[j].DisplayName {
				return secVnics[i].DisplayName < secVnics[j].DisplayName
			}
			return secVnics[i].ID < secVnics[j].ID
		})
		for _, vnic := range secVnics {
			if vnic.PublicIP == "" || seenIP[vnic.PublicIP] {
				continue
			}
			row := boundPublicIP{
				PublicIP: ociclient.PublicIP{
					ID:             "vnic:" + vnic.ID,
					IPAddress:      vnic.PublicIP,
					Lifetime:       ociclient.PublicIPEphemeral,
					Scope:          "AVAILABILITY_DOMAIN",
					LifecycleState: "ASSIGNED",
				},
				InstanceID:         inst.ID,
				InstanceName:       inst.DisplayName,
				InstanceState:      inst.LifecycleState,
				VnicID:             vnic.ID,
				VnicName:           vnic.DisplayName,
				IsPrimaryVnic:      false,
				PrivateIP:          vnic.PrivateIP,
				IsPrimaryPrivateIP: true,
			}
			if pub, ok := reservedByIP[vnic.PublicIP]; ok {
				privID := boundPublicIPPrivateID(pub)
				if priv, hasPriv := privByID[privID]; !hasPriv || priv.IsPrimary {
					row.PublicIP = pub
					row.PrivateIPID = privID
				}
			}
			out = append(out, row)
			seenIP[vnic.PublicIP] = true
		}

		extra := reservedByInst[inst.ID]
		sort.Slice(extra, func(i, j int) bool {
			iPrimaryVnic := extra[i].priv.VnicID == inst.VnicID
			jPrimaryVnic := extra[j].priv.VnicID == inst.VnicID
			if iPrimaryVnic != jPrimaryVnic {
				return iPrimaryVnic
			}
			if extra[i].priv.IPAddress != extra[j].priv.IPAddress {
				return compareIPAddr(extra[i].priv.IPAddress, extra[j].priv.IPAddress) < 0
			}
			return compareIPAddr(extra[i].pub.IPAddress, extra[j].pub.IPAddress) < 0
		})
		for _, item := range extra {
			if seenIP[item.pub.IPAddress] {
				continue
			}
			isPrimaryVnic := inst.VnicID != "" && item.priv.VnicID == inst.VnicID
			vnicName := ""
			if vnic, ok := vnicByID[item.priv.VnicID]; ok {
				isPrimaryVnic = vnic.IsPrimary
				vnicName = vnic.DisplayName
			}
			row := boundPublicIP{
				PublicIP:           item.pub,
				InstanceID:         inst.ID,
				InstanceName:       inst.DisplayName,
				InstanceState:      inst.LifecycleState,
				VnicID:             item.priv.VnicID,
				VnicName:           vnicName,
				IsPrimaryVnic:      isPrimaryVnic,
				PrivateIP:          item.priv.IPAddress,
				IsPrimaryPrivateIP: item.priv.IsPrimary,
			}
			row.PrivateIPID = item.priv.ID
			out = append(out, row)
			seenIP[item.pub.IPAddress] = true
		}
	}

	return out, nil
}

// listReservedPublicIPs 列出某账号某区域的全部预留公网 IP（含空闲与已绑定），并补齐实例和私网槽位归属。
func (s *Server) listReservedPublicIPs(ctx context.Context, client *ociclient.Client, acc *store.Account, region string) ([]boundPublicIP, error) {
	reserved, err := client.ListPublicIPs(ctx, region, acc.CompartmentOCID, "REGION", "")
	if err != nil {
		return nil, err
	}
	live := make([]ociclient.PublicIP, 0, len(reserved))
	boundReserved := make([]ociclient.PublicIP, 0, len(reserved))
	for _, pub := range reserved {
		if pub.LifecycleState == "TERMINATED" || pub.LifecycleState == "TERMINATING" {
			continue
		}
		live = append(live, pub)
		if boundPublicIPPrivateID(pub) != "" {
			boundReserved = append(boundReserved, pub)
		}
	}
	if len(live) == 0 {
		return make([]boundPublicIP, 0), nil
	}

	insts, _ := s.st.ListInstances(ctx, store.InstanceFilter{
		AccountIDs: []string{acc.ID},
		Regions:    []string{region},
	})
	instByID := make(map[string]store.Instance, len(insts))
	instByPrimaryVnic := make(map[string]store.Instance, len(insts))
	instByPrimaryPub := make(map[string]store.Instance, len(insts))
	for _, inst := range insts {
		instByID[inst.ID] = inst
		if inst.VnicID != "" {
			instByPrimaryVnic[inst.VnicID] = inst
		}
		if inst.PublicIP != "" {
			instByPrimaryPub[inst.PublicIP] = inst
		}
	}

	privByID := resolveBoundReservedPrivateIPs(ctx, client, region, boundReserved, instByPrimaryPub)

	vnicToInst := make(map[string]store.Instance, len(instByPrimaryVnic))
	for vnicID, inst := range instByPrimaryVnic {
		vnicToInst[vnicID] = inst
	}
	vnicByID := make(map[string]ociclient.Vnic)
	unknownVnicIDs := make(map[string]bool)
	needAttachments := false
	for _, priv := range privByID {
		if priv.VnicID != "" {
			if _, ok := instByPrimaryVnic[priv.VnicID]; !ok {
				needAttachments = true
				unknownVnicIDs[priv.VnicID] = true
			}
		}
	}
	if needAttachments && len(insts) > 0 {
		if atts, err := client.ListVnicAttachments(ctx, region, acc.CompartmentOCID, ""); err == nil {
			var toFetch []string
			for _, att := range atts {
				if att.LifecycleState != "ATTACHED" || att.VnicID == "" {
					continue
				}
				inst, ok := instByID[att.InstanceID]
				if !ok {
					continue
				}
				vnicToInst[att.VnicID] = inst
				if unknownVnicIDs[att.VnicID] {
					toFetch = append(toFetch, att.VnicID)
				}
			}
			var (
				vnicMu  sync.Mutex
				vnicWG  sync.WaitGroup
				vnicSem = make(chan struct{}, 4)
			)
			for _, vnicID := range toFetch {
				vnicWG.Add(1)
				go func(vnicID string) {
					defer vnicWG.Done()
					vnicSem <- struct{}{}
					defer func() { <-vnicSem }()
					if vnic, err := client.GetVnic(ctx, region, vnicID); err == nil && vnic != nil {
						vnicMu.Lock()
						vnicByID[vnic.ID] = *vnic
						vnicMu.Unlock()
					}
				}(vnicID)
			}
			vnicWG.Wait()
		}
	}

	out := make([]boundPublicIP, 0, len(live))
	for _, pub := range live {
		privID := boundPublicIPPrivateID(pub)
		row := boundPublicIP{PublicIP: pub}
		if privID != "" {
			row.PrivateIPID = privID
			if priv, ok := privByID[privID]; ok {
				row.PrivateIP = priv.IPAddress
				row.IsPrimaryPrivateIP = priv.IsPrimary
				row.VnicID = priv.VnicID
				if inst, ok := vnicToInst[priv.VnicID]; ok {
					row.InstanceID = inst.ID
					row.InstanceName = inst.DisplayName
					row.InstanceState = inst.LifecycleState
					row.IsPrimaryVnic = inst.VnicID != "" && priv.VnicID == inst.VnicID
				}
				if vnic, ok := vnicByID[priv.VnicID]; ok {
					row.IsPrimaryVnic = vnic.IsPrimary
					row.VnicName = vnic.DisplayName
				}
			}
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		iBound := boundPublicIPPrivateID(out[i].PublicIP) != ""
		jBound := boundPublicIPPrivateID(out[j].PublicIP) != ""
		if iBound != jBound {
			return iBound
		}
		return compareIPAddr(out[i].IPAddress, out[j].IPAddress) < 0
	})
	return out, nil
}

type createReservedIPRequest struct {
	DisplayName string `json:"displayName"`
}

// handleCreateReservedIP 在目标账号区域创建一个空闲的预留公网 IPv4。
func (s *Server) handleCreateReservedIP(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	var req createReservedIPRequest
	if r.Body != nil && r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	created, err := client.CreatePublicIP(ctx, region, acc.CompartmentOCID,
		ociclient.PublicIPReserved, "", strings.TrimSpace(req.DisplayName))
	if err != nil {
		writeOCIError(w, err)
		return
	}
	s.resources.invalidate(acc.ID)
	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "reserved_ip_create", AccountID: acc.ID,
		Target: created.IPAddress, Detail: orText(created.DisplayName, i18n.M("api.reserved_ip")) + " · " + region, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusCreated, created)
}

// handleUnbindReservedIP 将预留公网 IP 从当前私网 IP 解绑（不删除预留 IP，回到空闲池）。
func (s *Server) handleUnbindReservedIP(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("confirm") != "true" {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code:    "confirm_required",
			Message: i18n.M("api.unbind_reserved_confirm"),
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	ipID := r.PathValue("id")
	pub, err := client.GetPublicIP(ctx, region, ipID)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	if pub.Lifetime != ociclient.PublicIPReserved {
		writeError(w, http.StatusBadRequest, "not_reserved", i18n.M("api.not_reserved"))
		return
	}
	privID := boundPublicIPPrivateID(*pub)
	if privID == "" {
		writeError(w, http.StatusConflict, "not_bound", i18n.M("api.ip_not_bound"))
		return
	}

	// 先定位受影响的实例，解绑后同步刷新它的缓存。
	var affectedInst *store.Instance
	var affectedPrimary bool
	if priv, err := client.GetPrivateIP(ctx, region, privID); err == nil && priv != nil && priv.VnicID != "" {
		affectedPrimary = priv.IsPrimary
		if insts, err := s.st.ListInstances(ctx, store.InstanceFilter{
			AccountIDs: []string{acc.ID},
			Regions:    []string{region},
		}); err == nil {
			for idx := range insts {
				if insts[idx].VnicID == priv.VnicID {
					affectedInst = &insts[idx]
					break
				}
			}
			if affectedInst == nil {
				if atts, err := client.ListVnicAttachments(ctx, region, acc.CompartmentOCID, ""); err == nil {
					for _, att := range atts {
						if att.LifecycleState == "ATTACHED" && att.VnicID == priv.VnicID {
							for idx := range insts {
								if insts[idx].ID == att.InstanceID {
									affectedInst = &insts[idx]
									break
								}
							}
							break
						}
					}
				}
			}
		}
	}

	if affectedInst != nil && s.jobs != nil {
		unlock, lockErr := s.jobs.AcquireKey(ctx, affectedInst.ID)
		if lockErr != nil {
			writeError(w, http.StatusRequestTimeout, "lock_timeout", lockErr.Error())
			return
		}
		defer unlock()
	}

	refreshed := false
	defer func() {
		if !refreshed {
			instID := ""
			if affectedInst != nil {
				instID = affectedInst.ID
			}
			s.refreshInstanceAfterIPMutation(ctx, instID, acc.ID)
		}
	}()

	updated, err := netsvc.UnbindReservedIP(ctx, client, region, pub.ID)
	if err != nil {
		writeOCIError(w, err)
		return
	}

	if affectedInst != nil {
		if affectedPrimary && affectedInst.PublicIP == pub.IPAddress {
			if fresh, err := s.st.GetInstance(ctx, affectedInst.ID); err == nil {
				fresh.PublicIP = ""
				if err := s.st.UpsertInstance(ctx, *fresh); err == nil {
					s.instances.Bus().Publish(instanceUpdatedEvent(fresh))
				}
			}
		} else {
			_ = s.instances.RefreshNetwork(ctx, affectedInst.ID)
		}
	}
	s.resources.invalidate(acc.ID)
	refreshed = true

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "reserved_ip_unbind", AccountID: acc.ID,
		Target: pub.IPAddress, Detail: orText(pub.DisplayName, i18n.M("api.reserved_ip")) + " · " + region, IP: s.clientIP(r),
	})
	s.notifier.Dispatch(r.Context(), notify.Message{
		Event: notify.EventDangerOperation,
		Title: i18n.M("api.reserved_unbound_title", pub.IPAddress),
		Body:  i18n.M("api.reserved_unbound_body"),
		Fields: map[string]string{
			i18n.M("f.account"): acc.Alias, "IP": pub.IPAddress, i18n.M("f.region"): region, i18n.M("f.operator"): user.Username,
		},
	})
	writeJSON(w, http.StatusOK, updated)
}

type bindReservedIPRequest struct {
	ReservedIPID     string  `json:"reservedIpId"`
	VnicID           string  `json:"vnicId"`
	PrivateIPID      string  `json:"privateIpId"`
	ExpectPublicIPID *string `json:"expectPublicIpId,omitempty"`
	ExpectPublicIP   *string `json:"expectPublicIp,omitempty"`
}

// handleBindReservedIP 将已有预留公网 IP 绑定到实例的指定私网 IP 槽位（支持从临时 IP 或空槽切换）。
func (s *Server) handleBindReservedIP(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("id")
	inst, err := s.st.GetInstance(r.Context(), instanceID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req bindReservedIPRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.ReservedIPID = strings.TrimSpace(req.ReservedIPID)
	req.VnicID = strings.TrimSpace(req.VnicID)
	req.PrivateIPID = strings.TrimSpace(req.PrivateIPID)
	if req.ReservedIPID == "" {
		writeError(w, http.StatusBadRequest, "missing_reserved_ip", i18n.M("api.reserved_ip_id_required"))
		return
	}
	if r.URL.Query().Get("confirm") != "true" {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code:    "confirm_required",
			Message: i18n.M("api.bind_ip_confirm"),
		})
		return
	}

	vnicID := req.VnicID
	primaryVnic := vnicID == "" || vnicID == inst.VnicID
	if primaryVnic {
		vnicID = inst.VnicID
	}
	if vnicID == "" {
		writeError(w, http.StatusBadRequest, "no_vnic", i18n.M("api.no_vnic_synced"))
		return
	}

	client, acc, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}
	who := s.actorOf(r)

	title := i18n.M("api.bind_ip_title", inst.DisplayName)
	if req.PrivateIPID != "" {
		title += i18n.M("api.secondary_ip_suffix")
	} else if !primaryVnic {
		title += i18n.M("api.secondary_suffix")
	}

	s.submitJob(w, jobsvc.Spec{
		Kind: jobBindIP, Title: title,
		AccountID: inst.AccountID, InstanceID: inst.ID, Timeout: 3 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		where := i18n.M("api.primary_vnic")
		if !primaryVnic {
			p.Step(i18n.M("api.step.check_vnic"))
			vnic, err := findInstanceVnic(ctx, client, inst, acc.CompartmentOCID, vnicID)
			if err != nil {
				return jobsvc.Outcome{}, err
			}
			where = vnicLabel(vnic)
		}
		p.Step(i18n.M("api.step.bind_ip", where))
		p.Set(25)

		refreshed := false
		defer func() {
			if !refreshed {
				s.refreshInstanceAfterIPMutation(ctx, inst.ID, acc.ID)
			}
		}()

		result, err := netsvc.BindReservedIP(ctx, client, inst.Region, vnicID, netsvc.BindReservedIPOptions{
			PrivateIPID:      req.PrivateIPID,
			ReservedIPID:     req.ReservedIPID,
			ExpectPublicIPID: req.ExpectPublicIPID,
			ExpectPublicIP:   req.ExpectPublicIP,
		})
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		p.Step(i18n.M("api.ip_bound_summary", result.NewIP))

		if primaryVnic && result.IsPrimary {
			if fresh, err := s.st.GetInstance(ctx, inst.ID); err == nil {
				fresh.PublicIP = result.NewIP
				if err := s.st.UpsertInstance(ctx, *fresh); err == nil {
					s.instances.Bus().Publish(instanceUpdatedEvent(fresh))
				}
			}
		} else {
			_ = s.instances.RefreshNetwork(ctx, inst.ID)
		}
		s.resources.invalidate(acc.ID)
		refreshed = true

		auditWhere := where
		if !result.IsPrimary && result.PrivateIP != "" {
			auditWhere = where + " · " + result.PrivateIP
		}
		detail := result.NewIP
		if result.OldIP != "" {
			detail = result.OldIP + " → " + result.NewIP
		}
		s.audit(who, store.AuditEntry{
			Action: "instance_bind_reserved_ip", AccountID: inst.AccountID,
			Target: inst.DisplayName, Detail: auditWhere + " · " + detail,
		})
		s.notifier.Dispatch(ctx, notify.Message{
			Event: notify.EventDangerOperation,
			Title: i18n.M("api.ip_bound_title", inst.DisplayName),
			Body:  i18n.M("api.ip_bound_body"),
			Fields: map[string]string{
				i18n.M("f.instance"): inst.DisplayName,
				i18n.M("f.old_ip"):   result.OldIP,
				i18n.M("f.new_ip"):   result.NewIP,
				i18n.M("f.operator"): who.username,
			},
		})

		summary := i18n.M("api.ip_bound_summary", result.NewIP)
		if result.OldIP != "" {
			summary = result.OldIP + " → " + result.NewIP
		}
		return jobsvc.Outcome{Summary: summary, Result: result}, nil
	})
}

type unassignPublicIPRequest struct {
	VnicID           string  `json:"vnicId"`
	PrivateIPID      string  `json:"privateIpId"`
	ExpectPublicIPID *string `json:"expectPublicIpId,omitempty"`
	ExpectPublicIP   *string `json:"expectPublicIp,omitempty"`
}

// handleUnassignPublicIP 取消绑定实例指定私网 IP 槽位的公网 IP（临时 IP 删除释放，预留 IP 无损解绑）。
func (s *Server) handleUnassignPublicIP(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("id")
	inst, err := s.st.GetInstance(r.Context(), instanceID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req unassignPublicIPRequest
	if r.Body != nil && r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	if r.URL.Query().Get("confirm") != "true" {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code:    "confirm_required",
			Message: i18n.M("api.unassign_ip_confirm"),
		})
		return
	}

	vnicID := strings.TrimSpace(req.VnicID)
	privateIPID := strings.TrimSpace(req.PrivateIPID)
	primaryVnic := vnicID == "" || vnicID == inst.VnicID
	if primaryVnic {
		vnicID = inst.VnicID
	}
	if vnicID == "" {
		writeError(w, http.StatusBadRequest, "no_vnic", i18n.M("api.no_vnic_synced"))
		return
	}

	client, acc, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	if s.jobs != nil {
		unlock, lockErr := s.jobs.AcquireKey(ctx, inst.ID)
		if lockErr != nil {
			writeError(w, http.StatusRequestTimeout, "lock_timeout", lockErr.Error())
			return
		}
		defer unlock()
	}

	where := i18n.M("api.primary_vnic")
	if !primaryVnic {
		vnic, err := findInstanceVnic(ctx, client, inst, acc.CompartmentOCID, vnicID)
		if err != nil {
			writeOCIError(w, err)
			return
		}
		where = vnicLabel(vnic)
	}

	refreshed := false
	defer func() {
		if !refreshed {
			s.refreshInstanceAfterIPMutation(ctx, inst.ID, acc.ID)
		}
	}()

	result, err := netsvc.UnassignPublicIP(ctx, client, inst.Region, vnicID, netsvc.UnassignPublicIPOptions{
		PrivateIPID:      privateIPID,
		ExpectPublicIPID: req.ExpectPublicIPID,
		ExpectPublicIP:   req.ExpectPublicIP,
	})
	if err != nil {
		if errors.Is(err, netsvc.ErrStateChanged) {
			writeError(w, http.StatusConflict, "state_changed", err.Error())
			return
		}
		writeOCIError(w, err)
		return
	}

	if primaryVnic && result.IsPrimary {
		if fresh, err := s.st.GetInstance(ctx, inst.ID); err == nil {
			fresh.PublicIP = ""
			if err := s.st.UpsertInstance(ctx, *fresh); err == nil {
				s.instances.Bus().Publish(instanceUpdatedEvent(fresh))
			}
		}
	} else {
		_ = s.instances.RefreshNetwork(ctx, inst.ID)
	}
	s.resources.invalidate(acc.ID)
	refreshed = true

	user := userFrom(r.Context())
	auditWhere := where
	if !result.IsPrimary && result.PrivateIP != "" {
		auditWhere = where + " · " + result.PrivateIP
	}
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "instance_unassign_ip", AccountID: inst.AccountID,
		Target: inst.DisplayName, Detail: auditWhere + " · " + result.OldIP, IP: s.clientIP(r),
	})
	bodyKey := "api.ip_unassigned_ephemeral"
	if result.Lifetime == ociclient.PublicIPReserved {
		bodyKey = "api.ip_unassigned_reserved"
	}
	s.notifier.Dispatch(r.Context(), notify.Message{
		Event: notify.EventDangerOperation,
		Title: i18n.M("api.ip_unassigned_title", inst.DisplayName),
		Body:  i18n.M(bodyKey),
		Fields: map[string]string{
			i18n.M("f.instance"):    inst.DisplayName,
			i18n.M("f.released_ip"): result.OldIP,
			i18n.M("f.operator"):    user.Username,
		},
	})
	writeJSON(w, http.StatusOK, result)
}

type switchEphemeralIPRequest struct {
	VnicID           string  `json:"vnicId"`
	PrivateIPID      string  `json:"privateIpId"`
	ExpectPublicIPID *string `json:"expectPublicIpId,omitempty"`
	ExpectPublicIP   *string `json:"expectPublicIp,omitempty"`
}

// handleSwitchEphemeralIP 将实例网卡的主私网 IP 槽切回临时公网 IP（若当前是预留 IP 会无损解绑回空闲池）。
func (s *Server) handleSwitchEphemeralIP(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("id")
	inst, err := s.st.GetInstance(r.Context(), instanceID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req switchEphemeralIPRequest
	if r.Body != nil && r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	if r.URL.Query().Get("confirm") != "true" {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code:    "confirm_required",
			Message: i18n.M("api.switch_ephemeral_confirm"),
		})
		return
	}

	vnicID := strings.TrimSpace(req.VnicID)
	privateIPID := strings.TrimSpace(req.PrivateIPID)
	primaryVnic := vnicID == "" || vnicID == inst.VnicID
	if primaryVnic {
		vnicID = inst.VnicID
	}
	if vnicID == "" {
		writeError(w, http.StatusBadRequest, "no_vnic", i18n.M("api.no_vnic_synced"))
		return
	}

	client, acc, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}
	who := s.actorOf(r)

	title := i18n.M("api.switch_ephemeral_title", inst.DisplayName)
	if !primaryVnic {
		title += i18n.M("api.secondary_suffix")
	}

	s.submitJob(w, jobsvc.Spec{
		Kind: jobSwitchEphIP, Title: title,
		AccountID: inst.AccountID, InstanceID: inst.ID, Timeout: 3 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		where := i18n.M("api.primary_vnic")
		if !primaryVnic {
			p.Step(i18n.M("api.step.check_vnic"))
			vnic, err := findInstanceVnic(ctx, client, inst, acc.CompartmentOCID, vnicID)
			if err != nil {
				return jobsvc.Outcome{}, err
			}
			where = vnicLabel(vnic)
		}
		p.Step(i18n.M("api.step.switch_ephemeral", where))
		p.Set(25)

		refreshed := false
		defer func() {
			if !refreshed {
				s.refreshInstanceAfterIPMutation(ctx, inst.ID, acc.ID)
			}
		}()

		result, err := netsvc.SwitchToEphemeralIP(ctx, client, inst.Region, vnicID, acc.CompartmentOCID, netsvc.SwitchToEphemeralIPOptions{
			PrivateIPID:      privateIPID,
			ExpectPublicIPID: req.ExpectPublicIPID,
			ExpectPublicIP:   req.ExpectPublicIP,
		})
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		p.Step(i18n.M("api.ip_switched_ephemeral_summary", result.NewIP))

		if primaryVnic && result.IsPrimary {
			if fresh, err := s.st.GetInstance(ctx, inst.ID); err == nil {
				fresh.PublicIP = result.NewIP
				if err := s.st.UpsertInstance(ctx, *fresh); err == nil {
					s.instances.Bus().Publish(instanceUpdatedEvent(fresh))
				}
			}
		} else {
			_ = s.instances.RefreshNetwork(ctx, inst.ID)
		}
		s.resources.invalidate(acc.ID)
		refreshed = true

		detail := result.NewIP
		if result.OldIP != "" {
			detail = result.OldIP + " → " + result.NewIP
		}
		s.audit(who, store.AuditEntry{
			Action: "instance_switch_ephemeral_ip", AccountID: inst.AccountID,
			Target: inst.DisplayName, Detail: where + " · " + detail,
		})
		s.notifier.Dispatch(ctx, notify.Message{
			Event: notify.EventDangerOperation,
			Title: i18n.M("api.ip_switched_ephemeral_title", inst.DisplayName),
			Body:  i18n.M("api.ip_switched_ephemeral_body"),
			Fields: map[string]string{
				i18n.M("f.instance"): inst.DisplayName,
				i18n.M("f.old_ip"):   result.OldIP,
				i18n.M("f.new_ip"):   result.NewIP,
				i18n.M("f.operator"): who.username,
			},
		})

		summary := i18n.M("api.ip_switched_ephemeral_summary", result.NewIP)
		if result.OldIP != "" {
			summary = result.OldIP + " → " + result.NewIP
		}
		return jobsvc.Outcome{Summary: summary, Result: result}, nil
	})
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
	title := i18n.M("api.ensure_net_title", region)
	if enableIPv6 {
		title += i18n.M("api.with_ipv6")
	}

	s.submitJob(w, jobsvc.Spec{
		Kind: jobEnsureNet, Title: title, AccountID: acc.ID, Timeout: 5 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		p.Step(i18n.M("api.step.check_net"))
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
				return jobsvc.Outcome{Result: result}, i18n.Err("api.subnet_ready_no_v6", ipv6ErrText(err))
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
		return jobsvc.Outcome{Summary: i18n.M("api.subnet_ready", result.SubnetName), Result: result}, nil
	})
}

// handleChangePublicIP 更换实例某张网卡的公网 IP。这是 L2 级操作。
//
// 查询参数 vnicId 可选，留空为主网卡；privateIpId 可选，留空为该网卡的主私网 IP；
// allowReserved=true 时允许释放现有保留公网 IP 并重新申请新的保留 IP。
// changePublicIPRequest 是更换公网 IP 的可选请求体（用于可靠传递可为空串的乐观锁字段）。
type changePublicIPRequest struct {
	ExpectPublicIPID *string `json:"expectPublicIpId,omitempty"`
	ExpectPublicIP   *string `json:"expectPublicIp,omitempty"`
}

// 目标私网 IP 上还没有公网 IP 时就是直接分配一个。
// 确认与凭据在请求里校验，换 IP 本身（删旧地址、等释放、申请新地址）
// 交给后台任务。
func (s *Server) handleChangePublicIP(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("id")

	inst, err := s.st.GetInstance(r.Context(), instanceID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req changePublicIPRequest
	if r.Body != nil && r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	vnicID := strings.TrimSpace(r.URL.Query().Get("vnicId"))
	privateIPID := strings.TrimSpace(r.URL.Query().Get("privateIpId"))
	allowReserved := r.URL.Query().Get("allowReserved") == "true"
	expectPublicIPID := req.ExpectPublicIPID
	if r.URL.Query().Has("expectPublicIpId") {
		v := r.URL.Query().Get("expectPublicIpId")
		expectPublicIPID = &v
	}
	expectPublicIP := req.ExpectPublicIP
	if r.URL.Query().Has("expectPublicIp") {
		v := r.URL.Query().Get("expectPublicIp")
		expectPublicIP = &v
	}
	primaryVnic := vnicID == "" || vnicID == inst.VnicID
	if primaryVnic {
		vnicID = inst.VnicID
	}
	primaryTarget := primaryVnic && privateIPID == ""
	if vnicID == "" {
		writeError(w, http.StatusBadRequest, "no_vnic",
			i18n.M("api.no_vnic_synced"))
		return
	}

	confirmVal := strings.TrimSpace(r.URL.Query().Get("confirm"))
	if allowReserved {
		// 删除/更换预留公网 IP 属于 L3 危险操作，必须在 confirm 中回传原预留 IP 地址。
		if net.ParseIP(confirmVal) == nil {
			writeJSON(w, http.StatusBadRequest, errorBody{
				Code:    "confirm_required",
				Message: i18n.M("api.change_reserved_ip_confirm"),
			})
			return
		}
	} else {
		// L2 门槛：原 IP 不可找回，SSH 会立刻中断，必须显式确认。
		if confirmVal != "true" {
			msg := i18n.M("api.change_ip_confirm")
			if primaryTarget && inst.PublicIP != "" {
				msg = i18n.M("api.change_ip_confirm_ip", inst.PublicIP)
			}
			writeJSON(w, http.StatusBadRequest, errorBody{
				Code:    "confirm_required",
				Message: msg,
			})
			return
		}
	}

	client, acc, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}
	who := s.actorOf(r)

	title := i18n.M("api.change_ip_title", inst.DisplayName)
	if privateIPID != "" {
		title += i18n.M("api.secondary_ip_suffix")
	} else if !primaryVnic {
		title += i18n.M("api.secondary_suffix")
	}
	s.submitJob(w, jobsvc.Spec{
		Kind: jobChangeIP, Title: title,
		AccountID: inst.AccountID, InstanceID: inst.ID, Timeout: 3 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		where := i18n.M("api.primary_vnic")
		if !primaryVnic {
			p.Step(i18n.M("api.step.check_vnic"))
			vnic, err := findInstanceVnic(ctx, client, inst, acc.CompartmentOCID, vnicID)
			if err != nil {
				return jobsvc.Outcome{}, err
			}
			where = vnicLabel(vnic)
		}
		if primaryTarget && inst.PublicIP != "" {
			p.Step(i18n.M("api.step.release_ip", inst.PublicIP))
		} else {
			p.Step(i18n.M("api.step.request_ip", where))
		}
		p.Set(20)

		refreshed := false
		defer func() {
			if !refreshed {
				s.refreshInstanceAfterIPMutation(ctx, inst.ID, acc.ID)
			}
		}()

		result, err := netsvc.ChangePublicIP(ctx, client, inst.Region, vnicID, acc.CompartmentOCID, netsvc.ChangePublicIPOptions{
			PrivateIPID:      privateIPID,
			AllowReserved:    allowReserved,
			ConfirmIP:        confirmVal,
			ExpectPublicIPID: expectPublicIPID,
			ExpectPublicIP:   expectPublicIP,
		})
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		result.IsPrimaryVnic = primaryVnic
		p.Step(i18n.M("api.step.new_ip", result.NewIP))

		if primaryVnic && result.IsPrimary {
			// 立刻把新 IP 写回缓存，用户不必等下一轮同步才看到。重新读一次：
			// 任务排队期间这一行可能已被同步改过，不能拿请求时的旧快照覆盖。
			if fresh, err := s.st.GetInstance(ctx, inst.ID); err == nil {
				fresh.PublicIP = result.NewIP
				if err := s.st.UpsertInstance(ctx, *fresh); err == nil {
					s.instances.Bus().Publish(instanceUpdatedEvent(fresh))
				}
			}
		} else {
			// 附属网卡或辅助私网 IP 的地址记在 secondary 列里，整体重读一次网络信息。
			_ = s.instances.RefreshNetwork(ctx, inst.ID)
		}
		s.resources.invalidate(acc.ID)
		refreshed = true

		auditWhere := where
		if !result.IsPrimary && result.PrivateIP != "" {
			auditWhere = where + " · " + result.PrivateIP
		}
		change := result.OldIP + " → " + result.NewIP
		if result.OldIP == "" {
			change = i18n.M("api.newly_assigned", result.NewIP)
		}
		s.audit(who, store.AuditEntry{
			Action: "instance_change_ip", AccountID: inst.AccountID,
			Target: inst.DisplayName, Detail: auditWhere + " · " + change,
		})
		s.notifier.Dispatch(ctx, notify.Message{
			Event: notify.EventDangerOperation,
			Title: i18n.M("api.ip_changed_title", inst.DisplayName),
			Body:  i18n.M("api.ip_changed_body"),
			Fields: map[string]string{
				i18n.M("f.instance"): inst.DisplayName,
				i18n.M("f.old_ip"):   result.OldIP,
				i18n.M("f.new_ip"):   result.NewIP,
				i18n.M("f.operator"): who.username,
			},
		})

		summary := i18n.M("api.new_ip", result.NewIP)
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
			i18n.M("api.no_net_synced"))
		return
	}

	client, _, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}
	who := s.actorOf(r)

	s.submitJob(w, jobsvc.Spec{
		Kind: jobEnableIPv6, Title: i18n.M("api.enable_v6_title", inst.DisplayName),
		AccountID: inst.AccountID, InstanceID: inst.ID, Timeout: 3 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		p.Step(i18n.M("api.step.enable_v6"))
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
		return jobsvc.Outcome{Summary: i18n.M("api.v6_assigned", result.Address), Result: result}, nil
	})
}

type createSecondaryPrivateIPRequest struct {
	DisplayName string `json:"displayName"`
	IPAddress   string `json:"ipAddress"`
}

// handleCreateSecondaryPrivateIP 为实例指定网卡创建一个辅助私网 IPv4 槽位。
func (s *Server) handleCreateSecondaryPrivateIP(w http.ResponseWriter, r *http.Request) {
	inst, err := s.st.GetInstance(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req createSecondaryPrivateIPRequest
	if r.Body != nil && r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	vnicID := strings.TrimSpace(r.PathValue("vnicId"))
	if vnicID == "" {
		writeError(w, http.StatusBadRequest, "no_vnic", i18n.M("api.no_vnic_synced"))
		return
	}

	client, acc, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if s.jobs != nil {
		unlock, lockErr := s.jobs.AcquireKey(ctx, inst.ID)
		if lockErr != nil {
			writeError(w, http.StatusRequestTimeout, "lock_timeout", lockErr.Error())
			return
		}
		defer unlock()
	}

	where := i18n.M("api.primary_vnic")
	if vnicID != inst.VnicID {
		vnic, err := findInstanceVnic(ctx, client, inst, acc.CompartmentOCID, vnicID)
		if err != nil {
			writeOCIError(w, err)
			return
		}
		where = vnicLabel(vnic)
	}

	result, err := netsvc.CreateSecondaryPrivateIP(ctx, client, inst.Region, vnicID, req.DisplayName, req.IPAddress)
	if err != nil {
		if errors.Is(err, netsvc.ErrInvalidPrivateIP) {
			writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
			return
		}
		writeOCIError(w, err)
		return
	}

	_ = s.instances.RefreshNetwork(ctx, inst.ID)
	s.resources.invalidate(acc.ID)

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "instance_create_private_ip", AccountID: inst.AccountID,
		Target: inst.DisplayName, Detail: where + " · " + result.PrivateIP, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusCreated, result)
}

// handleDeleteSecondaryPrivateIP 释放（删除）实例网卡上的辅助私网 IP。
// 若其上绑定了预留公网 IP，会先无损解绑回空闲池。
func (s *Server) handleDeleteSecondaryPrivateIP(w http.ResponseWriter, r *http.Request) {
	inst, err := s.st.GetInstance(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if r.URL.Query().Get("confirm") != "true" {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code:    "confirm_required",
			Message: i18n.M("api.delete_private_ip_confirm"),
		})
		return
	}
	vnicID := strings.TrimSpace(r.PathValue("vnicId"))
	privateIPID := strings.TrimSpace(r.PathValue("privateIpId"))
	if vnicID == "" {
		writeError(w, http.StatusBadRequest, "no_vnic", i18n.M("api.no_vnic_synced"))
		return
	}

	client, acc, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	if s.jobs != nil {
		unlock, lockErr := s.jobs.AcquireKey(ctx, inst.ID)
		if lockErr != nil {
			writeError(w, http.StatusRequestTimeout, "lock_timeout", lockErr.Error())
			return
		}
		defer unlock()
	}

	where := i18n.M("api.primary_vnic")
	if vnicID != inst.VnicID {
		vnic, err := findInstanceVnic(ctx, client, inst, acc.CompartmentOCID, vnicID)
		if err != nil {
			writeOCIError(w, err)
			return
		}
		where = vnicLabel(vnic)
	}

	refreshed := false
	defer func() {
		if !refreshed {
			s.refreshInstanceAfterIPMutation(ctx, inst.ID, acc.ID)
		}
	}()

	result, err := netsvc.DeleteSecondaryPrivateIP(ctx, client, inst.Region, vnicID, privateIPID)
	if err != nil {
		switch {
		case errors.Is(err, netsvc.ErrPrimaryPrivateIP):
			writeError(w, http.StatusBadRequest, "primary_private_ip", err.Error())
		case errors.Is(err, netsvc.ErrPrivateIPNotFound):
			writeError(w, http.StatusNotFound, "private_ip_not_found", err.Error())
		default:
			writeOCIError(w, err)
		}
		return
	}

	_ = s.instances.RefreshNetwork(ctx, inst.ID)
	s.resources.invalidate(acc.ID)
	refreshed = true

	user := userFrom(r.Context())
	detail := where + " · " + result.PrivateIP
	if result.UnboundPublicIP != "" {
		detail += " (" + result.UnboundPublicIP + ")"
	}
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "instance_delete_private_ip", AccountID: inst.AccountID,
		Target: inst.DisplayName, Detail: detail, IP: s.clientIP(r),
	})
	body := i18n.M("api.private_ip_deleted_body")
	fields := map[string]string{
		i18n.M("f.instance"): inst.DisplayName,
		i18n.M("f.vnic"):     where,
		i18n.M("f.operator"): user.Username,
	}
	if result.UnboundPublicIP != "" {
		body = i18n.M("api.private_ip_deleted_unbound", result.UnboundPublicIP)
		fields[i18n.M("f.released_ip")] = result.UnboundPublicIP
	}
	s.notifier.Dispatch(r.Context(), notify.Message{
		Event:  notify.EventDangerOperation,
		Title:  i18n.M("api.private_ip_deleted_title", inst.DisplayName),
		Body:   body,
		Fields: fields,
	})
	writeJSON(w, http.StatusOK, result)
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
	return nil, i18n.Err("api.vnic_not_on_instance")
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
		Kind: jobAddIPv6, Title: i18n.M("api.add_v6_title", inst.DisplayName),
		AccountID: inst.AccountID, InstanceID: inst.ID, Timeout: 3 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		p.Step(i18n.M("api.step.check_vnic"))
		vnic, err := findInstanceVnic(ctx, client, inst, acc.CompartmentOCID, vnicID)
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		p.Set(20)
		p.Step(i18n.M("api.step.add_v6"))
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
		return jobsvc.Outcome{Summary: i18n.M("api.v6_assigned", result.Address), Result: result}, nil
	})
}

// vnicLabel 是审计与任务里称呼一张网卡的方式。
func vnicLabel(v *ociclient.Vnic) string {
	kind := i18n.M("api.secondary_vnic")
	if v.IsPrimary {
		kind = i18n.M("api.primary_vnic")
	}
	if v.DisplayName != "" {
		return i18n.M("api.vnic_named", kind, v.DisplayName)
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
			return "", i18n.Err("api.vnic_wait_timeout")
		case e, ok := <-events:
			if !ok {
				return "", i18n.Err("api.event_stream_closed")
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
		writeError(w, http.StatusBadRequest, "invalid_input", i18n.M("api.choose_subnet"))
		return
	}
	inst, err := s.st.GetInstance(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	who := s.actorOf(r)

	s.submitJob(w, jobsvc.Spec{
		Kind: jobAttachVnic, Title: i18n.M("api.attach_vnic_title", inst.DisplayName),
		AccountID: inst.AccountID, InstanceID: inst.ID, Timeout: 8 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		events, unsubscribe := s.instances.Bus().Subscribe()
		defer unsubscribe()

		p.Step(i18n.M("api.step.check_vnic_limit"))
		_, result, err := s.instances.AttachVnic(ctx, inst.ID, req)
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		for _, warn := range result.Warnings {
			p.Step("⚠ " + warn)
		}

		detail := i18n.M("api.detail.subnet", req.SubnetID)
		if req.AssignPublicIP {
			detail += i18n.M("api.detail.with_v4")
		}
		if req.AssignIPv6 {
			detail += i18n.M("api.detail.with_v6")
		}
		s.audit(who, store.AuditEntry{
			Action: "instance_attach_vnic", AccountID: inst.AccountID,
			Target: inst.DisplayName, Detail: detail,
		})

		p.Step(i18n.M("api.step.attach_wait"))
		p.Set(40)
		state, err := waitVnicSettled(ctx, events, inst.ID)
		if err != nil {
			return jobsvc.Outcome{Result: result}, err
		}
		result.State = state
		if state != "ATTACHED" {
			return jobsvc.Outcome{Result: result}, i18n.Err("api.vnic_not_attached", state)
		}
		return jobsvc.Outcome{Summary: i18n.M("api.vnic_attached"), Result: result}, nil
	})
}

// handleDetachVnic 分离一张附属网卡。网卡上的临时公网 IP 随之释放，是 L2 级操作。
func (s *Server) handleDetachVnic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("confirm") != "true" {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code:    "confirm_required",
			Message: i18n.M("api.detach_confirm"),
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
		Kind: jobDetachVnic, Title: i18n.M("api.detach_vnic_title", inst.DisplayName),
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
		detail := i18n.M("api.detail.private", name, vnic.PrivateIP)
		if vnic.PublicIP != "" {
			detail += i18n.M("api.detail.public", vnic.PublicIP)
		}
		p.Step(i18n.M("api.step.detach_submitted", detail))
		p.Set(30)

		s.audit(who, store.AuditEntry{
			Action: "instance_detach_vnic", AccountID: inst.AccountID,
			Target: inst.DisplayName, Detail: detail,
		})
		fields := map[string]string{i18n.M("f.instance"): inst.DisplayName, i18n.M("f.vnic"): name, i18n.M("f.operator"): who.username}
		if vnic.PublicIP != "" {
			fields[i18n.M("f.released_ip")] = vnic.PublicIP
		}
		s.notifier.Dispatch(ctx, notify.Message{
			Event:  notify.EventDangerOperation,
			Title:  i18n.M("api.vnic_detached_title", inst.DisplayName),
			Body:   i18n.M("api.vnic_detached_body"),
			Fields: fields,
		})

		p.Step(i18n.M("api.step.detach_wait"))
		state, err := waitVnicSettled(ctx, events, inst.ID)
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		if state != "DETACHED" {
			return jobsvc.Outcome{}, i18n.Err("api.vnic_detach_incomplete", state)
		}
		return jobsvc.Outcome{Summary: i18n.M("api.vnic_detached", name)}, nil
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
