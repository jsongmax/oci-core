// Package capacitysvc 轮询 Oracle 的容量报告接口，盯住关心的规格什么时候有货。
//
// 和"容量守候"（抢机）的区别是根本性的：那个反复调 LaunchInstance 去撞，
// 这个调的是只读的容量报告——不创建任何资源、不消耗配额，Oracle 的风控盯的
// 是前者不是后者。所以这里可以放心地定期跑，而抢机必须克制。
//
// 一个必须记住的前提：报告说有容量，不等于创建一定成功。它反映的是宿主机池
// 的整体状态，真正的分配还要看那一瞬间的争抢。所以它只能当**过滤器**用
// （说没货就别去撞），不能当**判据**用（说有货就宣布抢到了）。
package capacitysvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"ocicore/internal/ociclient"
	"ocicore/internal/ociconn"
	"ocicore/internal/store"
)

const (
	// TickInterval 是调度循环的唤醒间隔。
	TickInterval = 30 * time.Second

	// DefaultProbeInterval 是同一个监控项两次查询之间的最小间隔。
	//
	// 5 分钟：容量释放后通常有数分钟的申领窗口，更密的轮询换不来更早的发现，
	// 只是白白多打接口。
	DefaultProbeInterval = 5 * time.Minute

	// MaxPerTick 是单轮最多查几项，避免账号一多就一次性打出去几十个请求。
	MaxPerTick = 6
)

// Deps 是构造依赖。
type Deps struct {
	Store *store.Store
	Conns *ociconn.Factory
	// OnChange 在某个监控项的状态**发生变化**时调用。
	// 只在变化时推：天天告诉用户"还是没货"没有意义，推多了就没人看了。
	OnChange func(ctx context.Context, w *store.CapacityWatch, prevStatus string)
}

// Service 是容量监控的调度器。
type Service struct {
	st       *store.Store
	conns    *ociconn.Factory
	onChange func(context.Context, *store.CapacityWatch, string)
	interval time.Duration
}

func New(d Deps) *Service {
	return &Service{
		st:       d.Store,
		conns:    d.Conns,
		onChange: d.OnChange,
		interval: DefaultProbeInterval,
	}
}

// Run 启动轮询循环，直到 ctx 结束。
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(TickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Service) tick(ctx context.Context) {
	due, err := s.st.DueCapacityWatches(ctx, time.Now().Add(-s.interval), MaxPerTick)
	if err != nil {
		slog.Warn("容量监控：读取到期项失败", "err", err)
		return
	}
	for i := range due {
		w := due[i]
		if err := s.probeOne(ctx, &w); err != nil {
			slog.Debug("容量监控：查询失败", "watch", w.ID, "err", err)
		}
	}
}

// probeOne 查一个监控项并写回结果。
func (s *Service) probeOne(ctx context.Context, w *store.CapacityWatch) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()

	client, acc, err := s.conns.ForID(ctx, w.AccountID)
	if err != nil {
		// 拿不到凭据就把它排到队尾，不要每轮都优先重试同一个坏项。
		_ = s.st.TouchCapacityWatch(ctx, w.ID, "读取账号凭据失败："+err.Error())
		return err
	}

	report, err := s.Probe(ctx, client, acc.CompartmentOCID, w.Region,
		w.AvailabilityDomain, w.Shape, w.Ocpus, w.MemoryGB)
	if err != nil {
		_ = s.st.TouchCapacityWatch(ctx, w.ID, shortErr(err))
		return err
	}

	prev := w.LastStatus
	status := report.StatusOf(w.Shape)
	var count int64
	for _, a := range report.ShapeAvailabilities {
		if a.InstanceShape == w.Shape {
			count = a.AvailableCount
			break
		}
	}

	changed, err := s.st.RecordCapacityResult(ctx, w.ID, status, count, "")
	if err != nil && !errors.Is(err, store.ErrCapacityWatchNotFound) {
		return err
	}
	if changed && s.onChange != nil {
		fresh, err := s.st.GetCapacityWatch(ctx, w.ID)
		if err == nil {
			s.onChange(ctx, fresh, prev)
		}
	}
	return nil
}

// Probe 直接查一次容量，不涉及监控项。手动查询与抢机前置检查都走它。
//
// 弹性规格才带 ShapeConfig：E2.1.Micro 这类固定规格带上会被 OCI 拒掉，
// 而返回的错误指向参数格式，很难联想到是这里多塞了一个字段。
func (s *Service) Probe(ctx context.Context, client *ociclient.Client,
	compartmentID, region, ad, shape string, ocpus, memoryGB float64) (*ociclient.CapacityReport, error) {

	req := ociclient.CapacityShapeRequest{InstanceShape: shape}
	if ocpus > 0 || memoryGB > 0 {
		req.InstanceShapeConfig = &ociclient.ShapeConfig{
			Ocpus:       float32(ocpus),
			MemoryInGBs: float32(memoryGB),
		}
	}
	return client.CreateCapacityReport(ctx, region, compartmentID, ad,
		[]ociclient.CapacityShapeRequest{req})
}

func shortErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	const max = 300
	if len(msg) > max {
		return msg[:max] + "…"
	}
	return msg
}

// Describe 生成一句人话的状态描述，通知与日志共用。
func Describe(w *store.CapacityWatch) string {
	return fmt.Sprintf("%s · %s · %s",
		w.Region, ociclient.CapacityStatusText(w.LastStatus), w.Shape)
}
