package permsvc

import (
	"strings"
)

// 策略语句的语法见官方文档 Policy Syntax：
//
//	Allow <subject> to <verb> <resource-type> in <location> [where <conditions>]
//	Allow <subject> to {PERMISSION_A, PERMISSION_B} in <location> [where <conditions>]
//
// 这里只解析与"某个 API 用户在某个区间里能做什么"相关的部分。
// 解析不了的 Allow / Deny 语句不会被当作"没有授权"，而是让结论降级为"无法判断"。

// subjectKind 是语句授权的主体类别。
type subjectKind int

const (
	subjectGroups  subjectKind = iota // group A, group id ocid…
	subjectAnyUser                    // any-user / any-group：对所有用户生效
	subjectOther                      // dynamic-group、service 等：与 API 用户无关
)

// groupRef 是语句里的一个组引用。Domain 为空表示未写身份域（即默认域）。
type groupRef struct {
	ID     string
	Domain string
	Name   string
}

// locationKind 是语句的作用范围。
type locationKind int

const (
	locTenancy locationKind = iota
	locCompartmentID
	locCompartmentName
)

// statement 是一条解析后的策略语句。
type statement struct {
	Raw     string
	Deny    bool
	Subject subjectKind
	Groups  []groupRef

	// 二选一：Verb + ResourceType，或 Perms。
	Verb         Verb
	ResourceType string
	Perms        []string

	Location    locationKind
	LocationRef string // 区间 OCID 或名称（可能是 a:b:c 路径）
	Conditional bool   // 带 where 子句
}

// parseStatement 解析一条语句。ok=false 表示这条语句与权限判断有关但解析失败；
// relevant=false 表示它是 Define / Endorse / Admit 等与本判断无关的语句。
func parseStatement(raw string) (st statement, relevant, ok bool) {
	st.Raw = raw
	s := strings.Join(strings.Fields(raw), " ")
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "allow "):
		s = s[len("allow "):]
	case strings.HasPrefix(lower, "deny "):
		st.Deny = true
		s = s[len("deny "):]
	default:
		return st, false, false
	}

	// 先切出 where 子句，再找 to / in。关键字只在引号与花括号之外才算数——
	// 组名可以带空格，例如 group 'Ops to go'。
	if i := findKeyword(s, "where"); i >= 0 {
		st.Conditional = true
		s = strings.TrimSpace(s[:i])
	}
	toIdx := findKeyword(s, "to")
	if toIdx < 0 {
		return st, true, false
	}
	subject := strings.TrimSpace(s[:toIdx])
	rest := strings.TrimSpace(s[toIdx+len("to"):])
	inIdx := findKeyword(rest, "in")
	if inIdx < 0 {
		return st, true, false
	}
	what := strings.TrimSpace(rest[:inIdx])
	loc := strings.TrimSpace(rest[inIdx+len("in"):])

	if !parseSubject(subject, &st) || !parseWhat(what, &st) || !parseLocation(loc, &st) {
		return st, true, false
	}
	return st, true, true
}

// findKeyword 返回 s 中第一个位于引号与花括号之外、前后都是空格（或边界）的关键字位置。
// 匹配不区分大小写。
func findKeyword(s, kw string) int {
	var quote byte
	depth := 0
	n := len(kw)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
			continue
		case c == '\'' || c == '"':
			quote = c
			continue
		case c == '{':
			depth++
			continue
		case c == '}':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth > 0 || i+n > len(s) || !strings.EqualFold(s[i:i+n], kw) {
			continue
		}
		before := i == 0 || s[i-1] == ' '
		after := i+n == len(s) || s[i+n] == ' '
		if before && after {
			return i
		}
	}
	return -1
}

// splitTopLevel 按逗号切分，忽略引号里的逗号。
func splitTopLevel(s string) []string {
	var parts []string
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case ',':
			parts = append(parts, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	return append(parts, strings.TrimSpace(s[start:]))
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

func parseSubject(s string, st *statement) bool {
	lower := strings.ToLower(s)
	switch {
	case lower == "any-user" || lower == "any-group":
		st.Subject = subjectAnyUser
		return true
	case strings.HasPrefix(lower, "dynamic-group "), strings.HasPrefix(lower, "service "),
		strings.HasPrefix(lower, "resource "):
		st.Subject = subjectOther
		return true
	case !strings.HasPrefix(lower, "group "):
		return false
	}

	st.Subject = subjectGroups
	for _, part := range splitTopLevel(s) {
		// 每一项可能重复写 group 关键字：group A, group B。
		if strings.HasPrefix(strings.ToLower(part), "group ") {
			part = strings.TrimSpace(part[len("group "):])
		}
		if strings.HasPrefix(strings.ToLower(part), "id ") {
			st.Groups = append(st.Groups, groupRef{ID: strings.TrimSpace(part[len("id "):])})
			continue
		}
		ref, ok := parseGroupName(part)
		if !ok {
			return false
		}
		st.Groups = append(st.Groups, ref)
	}
	return len(st.Groups) > 0
}

// parseGroupName 解析 Name、'Name'、'Domain'/'Name'、Domain/Name 四种写法。
func parseGroupName(s string) (groupRef, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return groupRef{}, false
	}
	// 斜杠在引号外才是域分隔符。
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if c == '/' {
			return groupRef{Domain: unquote(s[:i]), Name: unquote(s[i+1:])}, true
		}
	}
	return groupRef{Name: unquote(s)}, true
}

func parseWhat(s string, st *statement) bool {
	if strings.HasPrefix(s, "{") {
		if !strings.HasSuffix(s, "}") {
			return false
		}
		for _, p := range splitTopLevel(s[1 : len(s)-1]) {
			if p != "" {
				st.Perms = append(st.Perms, strings.ToUpper(p))
			}
		}
		return len(st.Perms) > 0
	}
	fields := strings.Fields(strings.ToLower(s))
	if len(fields) != 2 {
		return false
	}
	v, ok := parseVerb(fields[0])
	if !ok {
		return false
	}
	st.Verb = v
	st.ResourceType = fields[1]
	return true
}

func parseLocation(s string, st *statement) bool {
	lower := strings.ToLower(s)
	switch {
	case lower == "tenancy":
		st.Location = locTenancy
	case strings.HasPrefix(lower, "compartment id "):
		st.Location = locCompartmentID
		st.LocationRef = strings.TrimSpace(s[len("compartment id "):])
	case strings.HasPrefix(lower, "compartment "):
		st.Location = locCompartmentName
		st.LocationRef = unquote(s[len("compartment "):])
	default:
		return false
	}
	return st.LocationRef != "" || st.Location == locTenancy
}

// grants 判断语句是否涉及某项权限（不看主体与位置）。
func (st *statement) grants(n Need) bool {
	if len(st.Perms) > 0 {
		for _, p := range st.Perms {
			if p == n.Perm {
				return true
			}
		}
		return false
	}
	return st.Verb >= n.Verb && covers(st.ResourceType, n.Type)
}
