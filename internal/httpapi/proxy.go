package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"ocicore/internal/proxypool"
	"ocicore/internal/store"
)

// checkConcurrency 是批量检测的并发上限。
//
// 代理检测是纯出网请求，不打 Oracle 的接口，所以不受全局限流器管。
// 但也不能放任——十几条代理同时握手会把小 VPS 的连接数顶上去。
const checkConcurrency = 6

func (s *Server) handleListProxies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	proxies, err := s.st.ListProxies(ctx)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	bindings, err := s.st.ProxyBindings(ctx)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"proxies": proxies,
		// accountId -> proxyId。前端的分配矩阵靠它渲染。
		"bindings":       bindings,
		"checkTimeoutMs": proxypool.DefaultCheckTimeout.Milliseconds(),
		"notice": "代理只改变出口 IP，不改变身份——每个请求仍带该账号的密钥签名，" +
			"Oracle 始终知道是哪个租户在调用。它降低的是「多个账号从同一 IP 出去」这一个信号，" +
			"不是万能的防关联手段。",
	})
}

// proxyImportRequest 是批量导入的入参。
type proxyImportRequest struct {
	Text string `json:"text"`
	// DryRun 为真时只解析不落库，用于导入前的预览。
	DryRun bool `json:"dryRun"`
}

// handleImportProxies 解析粘贴进来的代理列表。
//
// 分两步走：先 dryRun 出预览让用户核对，再真正导入。代理列表动辄十几行、
// 格式还五花八门，直接写库的话用户要到失败时才知道哪一行有问题。
func (s *Server) handleImportProxies(w http.ResponseWriter, r *http.Request) {
	var req proxyImportRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	results := proxypool.ParseBulk(req.Text)
	if len(results) == 0 {
		writeError(w, http.StatusBadRequest, "empty", "没有解析到任何代理")
		return
	}

	type importRow struct {
		Line   int    `json:"line"`
		Masked string `json:"masked"`
		Label  string `json:"label,omitempty"`
		Error  string `json:"error,omitempty"`
		// Skipped 表示这条已经存在，不算失败。
		Skipped bool `json:"skipped,omitempty"`
	}

	rows := make([]importRow, 0, len(results))
	var ok, failed, skipped int

	for _, res := range results {
		row := importRow{Line: res.Line, Masked: res.Masked, Label: res.Proxy.Label}
		switch {
		case res.Error != "":
			row.Error = res.Error
			row.Masked = res.Raw
			failed++
		case req.DryRun:
			ok++
		default:
			if _, err := s.st.CreateProxy(r.Context(), res.Proxy); err != nil {
				if errors.Is(err, store.ErrProxyExists) {
					row.Skipped = true
					skipped++
				} else {
					row.Error = err.Error()
					failed++
				}
			} else {
				ok++
			}
		}
		rows = append(rows, row)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"dryRun":  req.DryRun,
		"rows":    rows,
		"ok":      ok,
		"failed":  failed,
		"skipped": skipped,
	})
}

type proxyUpdateRequest struct {
	Label   *string `json:"label"`
	Enabled *bool   `json:"enabled"`
	// Password 非 nil 时重设密码，空串表示清除。绝不回显。
	Password *string `json:"password"`
}

func (s *Server) handleUpdateProxy(w http.ResponseWriter, r *http.Request) {
	var req proxyUpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := s.st.UpdateProxy(r.Context(), r.PathValue("id"), store.ProxyUpdate{
		Label: req.Label, Enabled: req.Enabled, Password: req.Password,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// 改动可能让绑定账号的连接需要重建，缓存以账号 updated_at 为准，
	// store 已经把那一行推过了；这里再显式作废一次，省得等下一次读取。
	s.invalidateProxyAccounts(r.Context(), p.ID)
	writeJSON(w, http.StatusOK, map[string]any{"proxy": p})
}

func (s *Server) handleDeleteProxy(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteProxy(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeStoreError(w, err)
			return
		}
		// 仍被绑定时 store 返回的是普通错误，属于用户可修正的冲突。
		writeError(w, http.StatusConflict, "proxy_in_use", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// proxyBindRequest 绑定/解绑。ProxyID 为空表示解绑，回到本机直连。
type proxyBindRequest struct {
	AccountID string `json:"accountId"`
	ProxyID   string `json:"proxyId"`
}

func (s *Server) handleBindProxy(w http.ResponseWriter, r *http.Request) {
	var req proxyBindRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.AccountID == "" {
		writeError(w, http.StatusBadRequest, "invalid_account", "缺少账号 ID")
		return
	}

	if err := s.st.BindProxy(r.Context(), req.AccountID, req.ProxyID); err != nil {
		if errors.Is(err, proxypool.ErrDuplicateBinding) {
			// 这不是普通的参数错误，值得把原因讲清楚——用户十有八九
			// 觉得"多个账号共用一条代理"是理所当然的。
			writeError(w, http.StatusConflict, "proxy_shared",
				"该代理已绑定其他账号。一条代理只能绑一个账号——两个账号共用同一出口，"+
					"等于把它们绑在同一个 IP 上，反而制造了本来不存在的关联信号。")
			return
		}
		writeStoreError(w, err)
		return
	}

	// 账号的 updated_at 已被 store 推进，缓存会自然失效；
	// 这里直接作废，让下一次调用立刻走新代理。
	s.conns.Invalidate(req.AccountID)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCheckProxies 执行存活检测。
//
// 不带 id 时检测全部。检测走的是「通过该代理访问 OCI 端点」，
// 是未认证请求，不消耗配额也不产生费用。
func (s *Server) handleCheckProxies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var targets []store.Proxy
	if id := r.PathValue("id"); id != "" {
		p, err := s.st.GetProxy(ctx, id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		targets = []store.Proxy{*p}
	} else {
		all, err := s.st.ListProxies(ctx)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		targets = all
	}

	regions := s.proxyCheckRegions(ctx)
	checker := proxypool.NewChecker(proxypool.DefaultCheckTimeout)

	// 整批的时间上限：单条超时 10 秒，六路并发，留足余量。
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	results := make([]proxypool.CheckResult, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, checkConcurrency)

	for i := range targets {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			p := targets[idx]
			url, err := s.st.ProxyURL(ctx, p.ID)
			if err != nil {
				results[idx] = proxypool.CheckResult{
					Status: proxypool.StatusFail, Error: err.Error(), CheckedAt: time.Now(),
				}
				return
			}
			res := checker.Check(ctx, url, regionFor(regions, p.ID))
			results[idx] = res
			// 写回失败不影响本次返回：用户已经看到结果了。
			_ = s.st.RecordProxyCheck(ctx, p.ID, res)
		}(i)
	}
	wg.Wait()

	type row struct {
		ID string `json:"id"`
		proxypool.CheckResult
	}
	out := make([]row, len(targets))
	for i := range targets {
		out[i] = row{ID: targets[i].ID, CheckResult: results[i]}
	}

	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// fallbackCheckRegion 是未绑定账号的代理拿来测试的区域。
//
// 刚导入还没分配的代理必须也能测——那正是用户最想先测一遍的时候。
// 挑阿什本是因为它是 Oracle 最早、最不可能下线的区域；这个数字只用于
// 判断"这条代理通不通"，不用于判断"快不快"，所以选哪个区域并不敏感。
const fallbackCheckRegion = "us-ashburn-1"

// regionFor 返回该代理检测时该打的区域。
//
// 未绑定的代理没有"所属账号的 home region"可用，此前会拿到空串并被
// 判成"区域无效"——于是刚导入的代理一条都测不了，而那恰恰是最需要
// 测一遍的时刻。
func regionFor(regions map[string]string, proxyID string) string {
	if r := regions[proxyID]; r != "" {
		return r
	}
	if r := regions[""]; r != "" {
		return r
	}
	return fallbackCheckRegion
}

// proxyCheckRegions 决定每条代理该打哪个区域。
//
// 用所绑账号的 home region，而不是固定一个端点：一条美国代理连东京 OCI
// 和连阿什本 OCI 的延迟差好几倍，固定测一个地方给出的数字是误导——
// 用户会拿它来判断这条代理"快不快"，而那跟他实际要走的路无关。
func (s *Server) proxyCheckRegions(ctx context.Context) map[string]string {
	out := make(map[string]string)

	bindings, err := s.st.ProxyBindings(ctx)
	if err != nil {
		return out
	}
	accounts, err := s.st.ListAccounts(ctx)
	if err != nil {
		return out
	}
	byID := make(map[string]store.Account, len(accounts))
	for _, a := range accounts {
		byID[a.ID] = a
	}

	// 空键存一个兜底区域：未绑定的代理优先用用户自己账号的区域测，
	// 那比写死一个区域更接近他实际会走的路。
	for _, a := range accounts {
		if r := a.HomeRegion; r != "" {
			out[""] = r
			break
		}
		if r := a.DefaultRegion; r != "" {
			out[""] = r
		}
	}

	for accID, proxyID := range bindings {
		acc, ok := byID[accID]
		if !ok {
			continue
		}
		region := acc.HomeRegion
		if region == "" {
			region = acc.DefaultRegion
		}
		if region != "" {
			out[proxyID] = region
		}
	}
	return out
}

// invalidateProxyAccounts 作废所有绑定了该代理的账号的连接缓存。
func (s *Server) invalidateProxyAccounts(ctx context.Context, proxyID string) {
	bindings, err := s.st.ProxyBindings(ctx)
	if err != nil {
		return
	}
	for accID, pid := range bindings {
		if pid == proxyID {
			s.conns.Invalidate(accID)
		}
	}
}
