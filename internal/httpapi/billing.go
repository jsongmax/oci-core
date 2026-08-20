package httpapi

import (
	"net/http"
	"sort"
	"strconv"

	"ocicore/internal/billingsvc"
	"ocicore/internal/store"
)

// CurrencyTotal 是某个币种下的跨账号合计。
//
// 按币种分组而不是给一个总数：不同账号可能结算在不同币种，
// 把 USD 和 CNY 加成一个数字是错的，而且错得看不出来。
type CurrencyTotal struct {
	Currency  string  `json:"currency"`
	ThisMonth float64 `json:"thisMonth"`
	LastMonth float64 `json:"lastMonth"`
	// Accounts 是计入这个币种的账号数。
	Accounts int `json:"accounts"`
}

// handleBilling 返回全部账号的账单概况。
func (s *Server) handleBilling(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	refresh := r.URL.Query().Get("refresh") == "true"

	var accounts []store.Account
	if id := r.URL.Query().Get("accountId"); id != "" {
		acc, err := s.st.GetAccount(ctx, id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		accounts = []store.Account{*acc}
	} else {
		all, err := s.st.ListAccounts(ctx)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		accounts = all
	}

	summaries := s.billing.Summaries(ctx, accounts, refresh)

	// 只有真正查到费用的账号计入合计。没权限、被停用、出错的账号
	// 计成 0 会让总数看着像"这几个账号没花钱"，而事实是"不知道"。
	byCurrency := make(map[string]*CurrencyTotal)
	var counted, noPermission int
	for _, sum := range summaries {
		switch sum.Status {
		case billingsvc.StatusNoPermission:
			noPermission++
			continue
		case billingsvc.StatusOK, billingsvc.StatusFree:
		default:
			continue
		}
		counted++
		cur := sum.Currency
		if cur == "" {
			// 免费号常常一条费用记录都没有，Oracle 也就不带币种回来。
			// 归到 "—" 这个桶里，总比凭空写死 USD 诚实。
			cur = "—"
		}
		entry := byCurrency[cur]
		if entry == nil {
			entry = &CurrencyTotal{Currency: cur}
			byCurrency[cur] = entry
		}
		entry.ThisMonth += sum.ThisMonth
		entry.LastMonth += sum.LastMonth
		entry.Accounts++
	}

	totals := make([]CurrencyTotal, 0, len(byCurrency))
	for _, v := range byCurrency {
		totals = append(totals, *v)
	}
	sort.Slice(totals, func(i, j int) bool {
		if totals[i].ThisMonth != totals[j].ThisMonth {
			return totals[i].ThisMonth > totals[j].ThisMonth
		}
		return totals[i].Currency < totals[j].Currency
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"summaries":         summaries,
		"totals":            totals,
		"countedAccounts":   counted,
		"noPermissionCount": noPermission,
		// 数据延迟必须写在接口里而不是只写在前端：这是 Oracle 的事实约束，
		// 不是某一个页面的展示细节。
		"notice": "用量数据由 Oracle 每隔几小时结算一次，最新一天通常不完整。这里显示的不是实时消费。",
	})
}

// handleBillingDetail 返回单个账号的账单明细。
func (s *Server) handleBillingDetail(w http.ResponseWriter, r *http.Request) {
	acc, err := s.st.GetAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}

	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 30
	}
	refresh := r.URL.Query().Get("refresh") == "true"

	detail := s.billing.DetailFor(r.Context(), acc, days, refresh)
	writeJSON(w, http.StatusOK, detail)
}
