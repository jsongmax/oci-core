package httpapi

import (
	"context"
	"log/slog"
	"time"

	"ocicore/internal/instancesvc"
	"ocicore/internal/notify"
	"ocicore/internal/store"
)

// checkSweepInterval 是巡检循环的唤醒间隔。
//
// 这不是校验间隔——校验间隔由设置决定，可能是 6 小时。这里每分钟醒一次
// 只是为了"看看有没有该校验的账号了"，顺带让设置改动一分钟内生效，
// 而不必重启服务。
const checkSweepInterval = time.Minute

// maxChecksPerSweep 限制单次唤醒最多校验几个账号。
//
// 账号是批量导入的，它们的 last_checked_at 会挤在同一分钟里，到期时刻
// 也就挤在一起。不限流的话，二十个账号会在同一秒向 Oracle 发四十个请求——
// 对面看到的是一次小规模突发。每分钟放两个，二十个账号摊到十分钟。
const maxChecksPerSweep = 2

// RunAccountChecker 定期重跑凭据校验，直到 ctx 结束。
//
// 凭据会在面板不知情的情况下失效：密钥被轮换、IAM 用户被删、账号被封。
// 没有这个循环的话，卡片上的"校验通过"只代表最后一次有人手动点过按钮，
// 可能是三天前的事——而界面看起来像是在持续监控。
func (s *Server) RunAccountChecker(ctx context.Context) {
	ticker := time.NewTicker(checkSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepAccountChecks(ctx)
		}
	}
}

func (s *Server) sweepAccountChecks(ctx context.Context) {
	settings, err := s.st.Settings(ctx)
	if err != nil {
		slog.Warn("巡检读取设置失败", "err", err)
		return
	}
	// 0 表示用户关掉了自动校验。
	if settings.CheckIntervalHours <= 0 {
		return
	}
	interval := time.Duration(settings.CheckIntervalHours) * time.Hour

	accounts, err := s.st.ListAccounts(ctx)
	if err != nil {
		slog.Warn("巡检读取账号失败", "err", err)
		return
	}

	done := 0
	for i := range accounts {
		if done >= maxChecksPerSweep {
			return
		}
		acc := &accounts[i]
		if !acc.Enabled || !checkDue(acc, interval) {
			continue
		}
		done++
		s.recheckAccount(ctx, acc)
	}
}

// checkDue 判断一个账号是否到了该复查的时候。
func checkDue(acc *store.Account, interval time.Duration) bool {
	// 从未校验过的账号立刻查一次：这多半是导入后校验失败留下的，
	// 拖着不查只会让卡片一直显示"尚未校验"。
	if acc.LastCheckedAt == nil {
		return true
	}
	return time.Since(*acc.LastCheckedAt) >= interval
}

// recheckAccount 跑一次校验，并在状态发生变化时推事件、发通知。
func (s *Server) recheckAccount(ctx context.Context, acc *store.Account) {
	// 单个账号卡住不能拖垮整轮巡检。
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	before := acc.Status
	result := s.checkAccount(ctx, acc)
	if result == nil {
		return
	}

	after := store.StatusOK
	message := ""
	if !result.OK {
		after = store.StatusError
		message = result.ErrorText
		if result.ErrorCode != "" {
			message = result.ErrorCode + " " + message
		}
	}

	_ = s.st.Audit(ctx, store.AuditEntry{
		Action: "account_check_auto", Target: acc.ID,
		Result: auditResult(result.OK),
	})

	// 状态没变就不打扰：每 6 小时推一次"还是好的"只会让用户学会忽略通知。
	if before == after {
		return
	}

	if s.instances != nil {
		s.instances.Bus().Publish(instancesvc.Event{
			Type: instancesvc.EventAccountStatus, AccountID: acc.ID,
			State: after, Message: message,
		})
	}

	if after == store.StatusError {
		slog.Warn("账号自动校验失败", "account", acc.ID, "alias", acc.Alias, "err", message)
		s.notifier.Dispatch(ctx, notify.Message{
			Event: notify.EventAccountAuthFail,
			Title: "账号 " + acc.Alias + " 凭据已失效",
			Body:  result.Advice,
			Fields: map[string]string{
				"账号":   acc.Alias,
				"错误":   message,
				"发现方式": "定期自动校验",
			},
		})
		return
	}
	slog.Info("账号自动校验已恢复", "account", acc.ID, "alias", acc.Alias)
}

func auditResult(ok bool) string {
	if ok {
		return store.ResultOK
	}
	return store.ResultFail
}

// auditPruneInterval 是清理审计日志的检查间隔。
//
// 一天一次足够：保留期以天计，早删晚删几小时没有意义，而每次 DELETE
// 都会在 SQLite 上产生一次写事务，没必要频繁触发。
const auditPruneInterval = 24 * time.Hour

// RunAuditPruner 按保留策略定期清理审计日志，直到 ctx 结束。
//
// 默认设置是 0（永久保留），此时这个循环什么都不做——审计日志是安全设施，
// 自动删除必须由用户显式开启，不能是默认行为。
func (s *Server) RunAuditPruner(ctx context.Context) {
	ticker := time.NewTicker(auditPruneInterval)
	defer ticker.Stop()

	// 启动时先跑一次：容器可能几天才重启一次，等满 24 小时太久。
	s.pruneAuditOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pruneAuditOnce(ctx)
		}
	}
}

func (s *Server) pruneAuditOnce(ctx context.Context) {
	settings, err := s.st.Settings(ctx)
	if err != nil {
		slog.Warn("清理审计日志读取设置失败", "err", err)
		return
	}
	if settings.AuditRetentionDays <= 0 {
		return
	}

	cutoff := time.Now().AddDate(0, 0, -settings.AuditRetentionDays)
	n, err := s.st.PruneAudit(ctx, cutoff)
	if err != nil {
		slog.Warn("清理审计日志失败", "err", err)
		return
	}
	if n > 0 {
		slog.Info("已清理过期审计日志", "count", n, "retentionDays", settings.AuditRetentionDays)
	}
}
