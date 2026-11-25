package permsvc

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"ocicore/internal/i18n"
	"ocicore/internal/ociclient"
)

// 只读探测的名字。每个探测调一个只读接口，成功即证明对应权限存在。
const (
	probeInstances = "instances"
	probeVcns      = "vcns"
	probeVolumes   = "volumes"
	probeCapacity  = "capacity"
	probeLimits    = "limits"
	probeUsage     = "usage"
	probeAudit     = "audit"
)

// probeProves 是每个探测能直接证明的权限（官方文档里该接口要求的权限）。
var probeProves = map[string]Need{
	probeInstances: pInstanceRead,   // ListInstances：INSTANCE_READ
	probeVcns:      pVcnRead,        // ListVcns：VCN_READ
	probeVolumes:   pVolumeInspect,  // ListVolumes：VOLUME_INSPECT
	probeCapacity:  pCapacityReport, // CreateComputeCapacityReport：COMPUTE_CAPACITY_REPORT_CREATE
	probeLimits:    pLimitsInspect,  // ListLimitValues：inspect limits
	probeUsage:     pUsageReportRead,
	probeAudit:     pAuditEventsRead, // ListEvents：AUDIT_EVENT_READ
}

// probeAPI 是探测调用的接口名，展示给用户核对。
var probeAPI = map[string]string{
	probeInstances: "ListInstances", probeVcns: "ListVcns", probeVolumes: "ListVolumes",
	probeCapacity: "CreateComputeCapacityReport", probeLimits: "ListLimitValues",
	probeUsage: "RequestSummarizedUsages", probeAudit: "ListEvents",
}

// checkTimeout 是一次权限自检的上限。探测与读策略并发进行，正常几秒内完成。
const checkTimeout = 40 * time.Second

// Report 是一次权限自检的完整结果，会原样存进数据库并返回给前端。
// 其中的文字都是 i18n 信封，在写响应时按请求语言展开。
type Report struct {
	CheckedAt time.Time `json:"checkedAt"`
	// Policy 说明策略解析这一路的情况：ok / partial / unavailable。
	Policy     string   `json:"policy"`
	PolicyNote []string `json:"policyNote,omitempty"`
	Groups     []string `json:"groups"`
	Summary    Summary  `json:"summary"`
	Items      []Item   `json:"items"`
}

// Summary 是账号卡片上显示的概要。
type Summary struct {
	OK      int       `json:"ok"`
	Missing int       `json:"missing"`
	Unknown int       `json:"unknown"`
	At      time.Time `json:"checkedAt"`
}

// Item 是一项功能的结论。
type Item struct {
	Key      string       `json:"key"`
	Category string       `json:"category"`
	Label    string       `json:"label"`
	Status   Status       `json:"status"`
	Reason   string       `json:"reason,omitempty"`
	Fix      []string     `json:"fix,omitempty"`
	Needs    []NeedResult `json:"needs"`
}

// NeedResult 是功能所需的一项权限的结论。
type NeedResult struct {
	Perm   string `json:"perm"`
	Type   string `json:"type"`
	Verb   string `json:"verb"`
	Status Status `json:"status"`
	// Source 是结论来源：probe（只读探测）/ policy（策略解析）/ 空（无从判断）。
	Source string `json:"source,omitempty"`
	// Via 是依据：授予它的策略语句原文，或探测调用的接口名。
	Via    string `json:"via,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// probeResult 是一个探测的结论：ok / missing / unknown。
type probeResult struct {
	status Status
	detail string
}

// Check 对一个账号做权限自检。compartmentOCID 为空表示根区间。
func Check(ctx context.Context, client *ociclient.Client, compartmentOCID string) *Report {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	tenancy := client.TenancyOCID()
	if compartmentOCID == "" {
		compartmentOCID = tenancy
	}

	var (
		wg                 sync.WaitGroup
		mu                 sync.Mutex
		probes             = map[string]probeResult{}
		pv                 = &policyView{tenancyOCID: tenancy, compartmentOCID: compartmentOCID}
		notes              []string
		policyOK, groupsOK bool
	)

	// —— 策略这一路 ——
	wg.Add(1)
	go func() {
		defer wg.Done()
		view, policyNotes, pOK, gOK := loadPolicies(ctx, client, tenancy, compartmentOCID)
		mu.Lock()
		*pv = *view
		notes = append(notes, policyNotes...)
		policyOK, groupsOK = pOK, gOK
		mu.Unlock()
	}()

	// —— 只读探测这一路 ——
	for name, fn := range probeFuncs(client, tenancy, compartmentOCID) {
		wg.Add(1)
		go func(name string, fn func(context.Context) error) {
			defer wg.Done()
			res := classify(fn(ctx))
			mu.Lock()
			probes[name] = res
			mu.Unlock()
		}(name, fn)
	}
	wg.Wait()

	report := &Report{CheckedAt: time.Now().UTC(), PolicyNote: notes, Groups: make([]string, 0)}
	switch {
	case policyOK && groupsOK && pv.unparsed == 0:
		report.Policy = "ok"
	case policyOK:
		report.Policy = "partial"
	default:
		report.Policy = "unavailable"
	}
	for _, g := range pv.groups {
		report.Groups = append(report.Groups, g.Name)
	}

	for _, capb := range capabilities {
		item := evaluateCapability(capb, pv, policyOK, probes, report.Groups)
		report.Items = append(report.Items, item)
		switch item.Status {
		case StatusOK:
			report.Summary.OK++
		case StatusMissing:
			report.Summary.Missing++
		default:
			report.Summary.Unknown++
		}
	}
	report.Summary.At = report.CheckedAt
	return report
}

// classify 把探测调用的结果归为三态。
//
// 只有 NotAuthorizedOrNotFound 一类才算"缺失"：探测用的区间是账号自己的区间，
// 必然存在，所以这个错误码在这里只可能是没权限。其余错误（网络、限流、参数）
// 说明不了权限的有无。
func classify(err error) probeResult {
	if err == nil {
		return probeResult{status: StatusOK}
	}
	if apiErr, ok := ociclient.AsAPIError(err); ok {
		if apiErr.Class == ociclient.ClassNotAuthorized && apiErr.StatusCode != 401 {
			return probeResult{status: StatusMissing, detail: apiErr.Code}
		}
		return probeResult{status: StatusUnknown, detail: apiErr.Code}
	}
	return probeResult{status: StatusUnknown, detail: err.Error()}
}

func probeFuncs(client *ociclient.Client, tenancy, compartment string) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		probeInstances: func(ctx context.Context) error {
			_, err := client.ListInstances(ctx, ociclient.ListInstancesOptions{CompartmentID: compartment, Limit: 1})
			return err
		},
		probeVcns: func(ctx context.Context) error {
			_, err := client.ListVcns(ctx, "", compartment)
			return err
		},
		probeVolumes: func(ctx context.Context) error {
			_, err := client.ListVolumes(ctx, "", compartment)
			return err
		},
		probeCapacity: func(ctx context.Context) error {
			// 容量报告是只读查询：不创建资源、不占配额（见 ociclient.CreateCapacityReport）。
			ads, err := client.ListAvailabilityDomains(ctx, "", tenancy)
			if err != nil {
				return errSkip
			}
			if len(ads) == 0 {
				return errSkip
			}
			_, err = client.CreateCapacityReport(ctx, "", ads[0].Name,
				[]ociclient.CapacityShapeRequest{{InstanceShape: "VM.Standard.E2.1.Micro"}})
			return err
		},
		probeLimits: func(ctx context.Context) error {
			_, err := client.ListLimitValues(ctx, "", tenancy, "compute", "standard-a1-core-count")
			return err
		},
		probeUsage: func(ctx context.Context) error {
			today := ociclient.AlignDay(time.Now().UTC())
			_, err := client.SummarizeUsage(ctx, ociclient.UsageQuery{
				TenantID: tenancy, Start: today, End: today.Add(24 * time.Hour),
				Granularity: ociclient.GranularityTotal,
			})
			return err
		},
		probeAudit: func(ctx context.Context) error {
			now := time.Now().UTC()
			_, err := client.ListAuditEvents(ctx, ociclient.ListAuditEventsOptions{
				CompartmentID: compartment,
				StartTime:     now.Add(-5 * time.Minute),
				EndTime:       now,
			})
			return err
		},
	}
}

// errSkip 表示探测的前置步骤没走通，无法据此判断。
var errSkip = i18n.Err("perm.probe_skipped")

// loadPolicies 读出策略与用户所在的组。返回 (快照, 说明, 策略可读, 组可读)。
func loadPolicies(ctx context.Context, client *ociclient.Client, tenancy, compartment string) (*policyView, []string, bool, bool) {
	pv := &policyView{tenancyOCID: tenancy, compartmentOCID: compartment, compartmentComplete: compartment == tenancy}
	var notes []string

	// 组：先拿成员关系，再拿组名（策略语句里写的是组名）。
	groupsOK := false
	if memberships, err := client.ListUserGroupMemberships(ctx); err != nil {
		notes = append(notes, i18n.M("perm.src.no_groups_read"))
	} else if len(memberships) == 0 {
		// 经典接口只覆盖默认身份域。成员关系为空，多半是用户在别的身份域里。
		notes = append(notes, i18n.M("perm.src.no_groups"))
	} else if groups, err := client.ListGroups(ctx); err != nil {
		notes = append(notes, i18n.M("perm.src.no_groups_read"))
	} else {
		names := make(map[string]string, len(groups))
		for _, g := range groups {
			names[g.ID] = g.Name
		}
		for _, m := range memberships {
			if m.LifecycleState != "" && m.LifecycleState != "ACTIVE" {
				continue
			}
			pv.groups = append(pv.groups, groupInfo{ID: m.GroupID, Name: names[m.GroupID]})
		}
		sort.Slice(pv.groups, func(i, j int) bool { return pv.groups[i].Name < pv.groups[j].Name })
		groupsOK = true
	}
	pv.groupsKnown = groupsOK

	// 策略：租户级语句只能挂在根区间；账号用的是子区间时再读它自己的策略。
	compartments := []string{tenancy}
	if compartment != tenancy {
		compartments = append(compartments, compartment)
		// 子区间若是根区间的直接下级，链路上就只有这两处策略；否则中间层读不全。
		if children, err := client.ListCompartments(ctx, tenancy); err == nil {
			for _, c := range children {
				if c.ID == compartment {
					pv.compartmentName = c.Name
					pv.compartmentComplete = true
				}
			}
		}
		if !pv.compartmentComplete {
			notes = append(notes, i18n.M("perm.src.nested_compartment"))
		}
	}
	policyOK := true
	for _, comp := range compartments {
		policies, err := client.ListPolicies(ctx, comp)
		if err != nil {
			if comp == tenancy {
				policyOK = false
				notes = append(notes, i18n.M("perm.src.no_policy_read"))
				break
			}
			pv.compartmentComplete = false
			continue
		}
		for _, p := range policies {
			if p.LifecycleState != "" && p.LifecycleState != "ACTIVE" {
				continue
			}
			for _, raw := range p.Statements {
				st, relevant, ok := parseStatement(raw)
				switch {
				case !relevant:
				case !ok:
					pv.unparsed++
				default:
					pv.statements = append(pv.statements, st)
				}
			}
		}
	}
	if pv.unparsed > 0 {
		notes = append(notes, i18n.M("perm.src.unparsed", pv.unparsed))
	}
	return pv, notes, policyOK, groupsOK
}

// evaluateCapability 合并探测与策略两路结论。
//
// 每项权限：探测给出的确定结论优先；否则用策略结论；都没有就是无法判断。
// 功能整体：有一项缺失即缺失，全部可用才算可用，其余为无法判断。
func evaluateCapability(capb Capability, pv *policyView, policyOK bool, probes map[string]probeResult, groups []string) Item {
	item := Item{
		Key: capb.Key, Category: capb.Category,
		Label: i18n.M("perm.cap." + capb.Key),
		Needs: make([]NeedResult, 0, len(capb.Needs)),
	}
	missing := map[string]Verb{}
	allOK, anyMissing := true, false
	var firstReason string

	for _, n := range capb.Needs {
		nr := NeedResult{Perm: n.Perm, Type: n.Type, Verb: n.Verb.String(), Status: StatusUnknown}

		decided := false
		for name, proven := range probeProves {
			if proven.Perm != n.Perm {
				continue
			}
			if res, ok := probes[name]; ok && res.status != StatusUnknown {
				nr.Status, nr.Source, nr.Via = res.status, "probe", probeAPI[name]
				if res.status == StatusMissing {
					nr.Reason = i18n.M("perm.why.probe_denied", probeAPI[name])
				}
				decided = true
			}
		}
		if !decided {
			if policyOK {
				v := pv.evaluate(n)
				nr.Status, nr.Source, nr.Via = v.Status, "policy", v.Via
				switch {
				case v.ReasonKey != "":
					nr.Reason = i18n.M(v.ReasonKey)
				case v.Status == StatusMissing:
					nr.Reason = i18n.M("perm.why.not_granted")
				}
			} else {
				nr.Reason = i18n.M("perm.why.no_policy")
			}
		}

		switch nr.Status {
		case StatusMissing:
			anyMissing = true
			allOK = false
			fam := n.Type
			if f, ok := suggestFamily[n.Type]; ok {
				fam = f
			}
			if missing[fam] < n.Verb {
				missing[fam] = n.Verb
			}
		case StatusUnknown:
			allOK = false
			if firstReason == "" {
				firstReason = nr.Reason
			}
		}
		item.Needs = append(item.Needs, nr)
	}

	switch {
	case anyMissing:
		item.Status = StatusMissing
		item.Fix = fixStatements(missing, groups)
	case allOK:
		item.Status = StatusOK
	default:
		item.Status = StatusUnknown
		item.Reason = firstReason
		// 探测证实了一部分、其余判断不了时要明说，否则"探测成功了却显示无法判断"会让人困惑。
		for _, nr := range item.Needs {
			if nr.Source == "probe" && nr.Status == StatusOK {
				item.Reason = i18n.M("perm.why.probe_partial", nr.Via)
				break
			}
		}
	}
	return item
}

// fixStatements 生成补齐缺失权限的策略语句，每个资源类型一条，动词取所需最高者。
func fixStatements(missing map[string]Verb, groups []string) []string {
	group := "<group-name>"
	if len(groups) == 1 && groups[0] != "" {
		group = groups[0]
		if strings.ContainsAny(group, " \t") {
			group = "'" + group + "'"
		}
	}
	types := make([]string, 0, len(missing))
	for t := range missing {
		types = append(types, t)
	}
	sort.Strings(types)
	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, "Allow group "+group+" to "+missing[t].String()+" "+t+" in tenancy")
	}
	return out
}
