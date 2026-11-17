package permsvc

import (
	"strings"
)

// Status 是一项结论的三态。
type Status string

const (
	StatusOK      Status = "ok"
	StatusMissing Status = "missing"
	StatusUnknown Status = "unknown"
)

// policyView 是用来做判断的策略快照。
type policyView struct {
	statements []statement
	// unparsed 是与权限有关但解析失败的语句数。非零时"未授权"只能算"无法判断"。
	unparsed int

	// groups 是用户所在的组。groupsKnown=false 表示没读到成员关系，
	// 此时只有 any-user 语句能确定生效。
	groups      []groupInfo
	groupsKnown bool

	tenancyOCID     string
	compartmentOCID string
	compartmentName string
	// compartmentComplete 表示账号区间所在链路上的策略都已读到。
	// 账号区间就是根区间时恒为 true。
	compartmentComplete bool
}

type groupInfo struct {
	ID   string
	Name string
}

// policyVerdict 是策略层对一项权限的判断。
type policyVerdict struct {
	Status Status
	// Via 是授予（或否决）它的语句原文，便于用户核对。
	Via string
	// ReasonKey 说明为什么是 unknown。
	ReasonKey string
}

// subjectMatches 判断语句主体是否包含当前用户。返回 (匹配, 确定)。
func (pv *policyView) subjectMatches(st *statement) (bool, bool) {
	switch st.Subject {
	case subjectAnyUser:
		return true, true
	case subjectOther:
		return false, true
	}
	if !pv.groupsKnown {
		return false, false
	}
	for _, ref := range st.Groups {
		for _, g := range pv.groups {
			if ref.ID != "" {
				if strings.EqualFold(ref.ID, g.ID) {
					return true, true
				}
				continue
			}
			// 经典 IAM 接口只返回默认域的组；写了其他域名的引用不可能指向它们。
			if ref.Domain != "" && !strings.EqualFold(ref.Domain, "Default") {
				continue
			}
			if strings.EqualFold(ref.Name, g.Name) {
				return true, true
			}
		}
	}
	return false, true
}

// locationApplies 判断语句的作用范围是否覆盖本次请求的区间。返回 (覆盖, 确定)。
func (pv *policyView) locationApplies(st *statement, tenancyOnly bool) (bool, bool) {
	if st.Location == locTenancy {
		return true, true
	}
	// 区间级语句永远覆盖不到租户根。
	if tenancyOnly || pv.compartmentOCID == "" || pv.compartmentOCID == pv.tenancyOCID {
		return false, true
	}
	switch st.Location {
	case locCompartmentID:
		if strings.EqualFold(st.LocationRef, pv.compartmentOCID) {
			return true, true
		}
	case locCompartmentName:
		// 路径写法 a:b:c 的最后一段才是目标区间名。
		name := st.LocationRef
		if i := strings.LastIndex(name, ":"); i >= 0 {
			name = name[i+1:]
		}
		if pv.compartmentName != "" && strings.EqualFold(name, pv.compartmentName) {
			return true, true
		}
	}
	// 也可能是祖先区间——不知道区间树就没法确定。
	return false, false
}

// evaluate 给出策略层对一项权限的判断。
func (pv *policyView) evaluate(n Need) policyVerdict {
	var (
		granted     *statement
		conditional *statement
		denied      *statement
		uncertain   bool
	)
	for i := range pv.statements {
		st := &pv.statements[i]
		if st.Deny {
			// 否决语句只要涉及这个资源类型就视为可能生效：动词在否决语义下的范围
			// 文档没有给出逐项权限表，这里宁可降级为"无法判断"。
			if !(st.grants(n) || (st.ResourceType != "" && covers(st.ResourceType, n.Type))) {
				continue
			}
			if m, _ := pv.subjectMatches(st); m {
				denied = st
			}
			continue
		}
		if !st.grants(n) {
			continue
		}
		subj, subjSure := pv.subjectMatches(st)
		loc, locSure := pv.locationApplies(st, n.TenancyOnly)
		switch {
		case subj && loc && !st.Conditional:
			if granted == nil {
				granted = st
			}
		case subj && loc:
			conditional = st
		case !subjSure || !locSure:
			uncertain = true
		}
	}

	switch {
	case denied != nil:
		return policyVerdict{Status: StatusUnknown, Via: denied.Raw, ReasonKey: "perm.why.deny"}
	case granted != nil:
		return policyVerdict{Status: StatusOK, Via: granted.Raw}
	case conditional != nil:
		return policyVerdict{Status: StatusUnknown, Via: conditional.Raw, ReasonKey: "perm.why.conditional"}
	case !pv.groupsKnown:
		return policyVerdict{Status: StatusUnknown, ReasonKey: "perm.why.no_groups"}
	case uncertain:
		return policyVerdict{Status: StatusUnknown, ReasonKey: "perm.why.compartment"}
	case pv.unparsed > 0:
		return policyVerdict{Status: StatusUnknown, ReasonKey: "perm.why.unparsed"}
	case !n.TenancyOnly && !pv.compartmentComplete:
		return policyVerdict{Status: StatusUnknown, ReasonKey: "perm.why.compartment"}
	}
	return policyVerdict{Status: StatusMissing}
}
