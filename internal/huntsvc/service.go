// Package huntsvc 实现"容量守候"：反复尝试创建实例，直到成功或被叫停。
//
// 针对的是 Oracle 免费额度里 ARM 规格长期没有容量这一现实——LaunchInstance
// 大概率返回容量不足，需要在容量释放的那一刻恰好发出请求。
//
// 设计取向是**克制**。容量释放是分钟级事件，把请求间隔从 60 秒压到 5 秒，
// 命中率提升有限而请求量是 12 倍；而高频调用 LaunchInstance 是 Oracle 明确
// 不欢迎的行为，轻则 429，重则账号被标记。所以这里的默认值一律偏保守，
// 且把"为什么这么慢"通过 UI 讲清楚，而不是留给用户去猜。
package huntsvc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	"ocicore/internal/netsvc"
	"ocicore/internal/ociclient"
	"ocicore/internal/ociconn"
	"ocicore/internal/store"
)

const (
	// TickInterval 是调度循环的唤醒间隔。
	//
	// 不给每个任务起一个 goroutine + sleep：任务一多就是几十个睡眠协程，
	// 而且退避状态只活在栈上，进程一重启就丢。统一轮询 + 持久化的 next_at
	// 才是可恢复的。
	TickInterval = 10 * time.Second

	// MaxConcurrentAttempts 是同一时刻最多有几个任务在发 LaunchInstance。
	//
	// 这不是性能限制，是风控限制：并发越高，Oracle 看到的瞬时请求量越大。
	MaxConcurrentAttempts = 4

	// MinIntervalSeconds 是允许配置的最小间隔。
	//
	// 硬下限，不通过设置放开。低于这个值收益极小而风险陡增——
	// 容量释放后通常有数分钟的申领窗口，不是抢毫秒。
	MinIntervalSeconds = 30

	// DefaultIntervalSeconds 是不指定时的默认间隔。
	DefaultIntervalSeconds = 60

	// WarnIntervalSeconds 是"偏激进"的阈值，低于它前端必须给出警告。
	WarnIntervalSeconds = 60

	// 连续容量不足到一定次数后拉长间隔。一个已经撞了几百次的任务，
	// 说明这个可用域近期确实没货，再密集也是白打。
	slowdownAfter   = 10
	slowdownFactor  = 2
	crawlAfter      = 30
	crawlFactor     = 5
	maxBackoff      = 30 * time.Minute
	throttleBackoff = 5 * time.Minute
)

// Deps 是构造依赖。
type Deps struct {
	Store    *store.Store
	Conns    *ociconn.Factory
	OnLaunch func(ctx context.Context, task *store.HuntTask, inst *ociclient.Instance, region, subnetID, imageID string)
	OnEvent  func(ctx context.Context, event, title, body string)
}

// Service 是守候任务的调度器。
type Service struct {
	st       *store.Store
	conns    *ociconn.Factory
	onLaunch func(context.Context, *store.HuntTask, *ociclient.Instance, string, string, string)
	onEvent  func(context.Context, string, string, string)

	// inflight 防止同一个任务被两轮 tick 同时执行。
	// 一次尝试可能耗时数十秒（自动建网），而 tick 是 10 秒。
	mu       sync.Mutex
	inflight map[string]bool
}

func New(d Deps) *Service {
	return &Service{
		st:       d.Store,
		conns:    d.Conns,
		onLaunch: d.OnLaunch,
		onEvent:  d.OnEvent,
		inflight: make(map[string]bool),
	}
}

// Spec 是任务快照下来的创建参数。字段与创建实例表单一一对应。
type Spec struct {
	DisplayName  string  `json:"displayName"`
	Shape        string  `json:"shape"`
	Ocpus        float32 `json:"ocpus"`
	MemoryInGBs  float32 `json:"memoryInGbs"`
	ImageID      string  `json:"imageId"`
	BootVolumeGB int64   `json:"bootVolumeGb"`

	SubnetID          string `json:"subnetId"`
	AutoCreateNetwork bool   `json:"autoCreateNetwork"`
	AssignPublicIP    bool   `json:"assignPublicIp"`
	EnableIPv6        bool   `json:"enableIpv6"`

	SSHPublicKey string `json:"sshPublicKey"`
	CloudInit    string `json:"cloudInit"`
}

// EncodeSpec 把参数序列化成任务里存的那一列。
func EncodeSpec(s Spec) (string, error) {
	buf, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("huntsvc: 序列化创建参数失败: %w", err)
	}
	return string(buf), nil
}

// DecodeSpec 从任务里取回参数。
func DecodeSpec(raw string) (Spec, error) {
	var s Spec
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return s, fmt.Errorf("huntsvc: 解析创建参数失败: %w", err)
	}
	return s, nil
}

// NormalizeInterval 把用户填的间隔夹到允许范围内。
func NormalizeInterval(seconds int) int {
	if seconds <= 0 {
		return DefaultIntervalSeconds
	}
	if seconds < MinIntervalSeconds {
		return MinIntervalSeconds
	}
	if seconds > 3600 {
		return 3600
	}
	return seconds
}

// Run 启动调度循环，直到 ctx 结束。
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
	tasks, err := s.st.DueHuntTasks(ctx, time.Now(), MaxConcurrentAttempts)
	if err != nil {
		slog.Warn("守候：读取到期任务失败", "err", err)
		return
	}

	for i := range tasks {
		t := tasks[i]
		if !s.claim(t.ID) {
			continue
		}
		go func() {
			defer s.release(t.ID)
			s.attempt(ctx, &t)
		}()
	}
}

func (s *Service) claim(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight[id] {
		return false
	}
	s.inflight[id] = true
	return true
}

func (s *Service) release(id string) {
	s.mu.Lock()
	delete(s.inflight, id)
	s.mu.Unlock()
}

// attempt 执行一次尝试并把结果写回任务。
func (s *Service) attempt(ctx context.Context, t *store.HuntTask) {
	// 用独立 context：一次尝试可能要先建 VCN，用户在这期间做别的操作
	// 不该把它掐断，留下半个网络在那里没人收尾。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Minute)
	defer cancel()

	// 过期与次数上限在发请求之前判，省掉一次注定要被丢弃的调用。
	if stop, reason := s.shouldStop(t); stop {
		s.finish(ctx, t, store.HuntFailed, "Expired", reason)
		return
	}

	client, acc, err := s.conns.ForID(ctx, t.AccountID)
	if err != nil {
		// 凭据拿不到属于账号级问题，重试没有意义。
		s.finish(ctx, t, store.HuntFailed, "AuthFailed", "读取账号凭据失败："+err.Error())
		return
	}

	spec, err := DecodeSpec(t.Spec)
	if err != nil {
		s.finish(ctx, t, store.HuntFailed, "BadRequest", err.Error())
		return
	}

	// §防重复创建：任何一次重试之前先按标签查一遍。
	//
	// LaunchInstance 非幂等。请求实际到达并成功、而响应在网络上丢了的话，
	// 客户端看到的是超时；按"可重试"原样重发就会创建出第二台实例，
	// 直接吃掉 ARM 免费额度而用户毫不知情。多一个只读请求换掉这个风险，值。
	if t.Attempts > 0 {
		if inst := s.findExisting(ctx, client, acc, t); inst != nil {
			slog.Info("守候：发现上一轮其实已经建成", "task", t.ID, "instance", inst.ID)
			s.succeed(ctx, t, client, acc, inst, spec, inst.AvailabilityDomain)
			return
		}
	}

	ad, err := s.pickAD(ctx, client, acc, t)
	if err != nil {
		s.reschedule(ctx, t, "Transient", "读取可用域失败："+err.Error(), t.LastAD)
		return
	}

	subnetID := spec.SubnetID
	if subnetID == "" {
		if !spec.AutoCreateNetwork {
			s.finish(ctx, t, store.HuntFailed, "BadRequest", "任务没有子网，也没有勾选自动创建网络")
			return
		}
		netResult, err := netsvc.EnsureNetwork(ctx, client, netsvc.EnsureNetworkOptions{
			Region:        t.Region,
			CompartmentID: acc.CompartmentOCID,
			EnableIPv6:    spec.EnableIPv6,
		})
		if err != nil {
			s.reschedule(ctx, t, "Transient", "准备网络失败："+shortErr(err), ad)
			return
		}
		subnetID = netResult.SubnetID
	}

	// 前置容量检查。
	//
	// 容量报告是只读接口，LaunchInstance 才是 Oracle 风控盯的那个——
	// 用一次只读换掉一次创建，是这里性价比最高的一处。绝大多数轮次都会在
	// 这里被挡掉，真正发出去的创建请求能降一个数量级。
	//
	// 只当过滤器，不当判据：报告说有货仍然可能创建失败（宿主机池的整体状态
	// 不等于那一瞬间的分配结果），所以"有货"只是放行，不是宣布成功。
	// 查询本身失败时一律放行——宁可多试一次，也不要因为一个只读接口抽风
	// 就把整个任务卡死。
	if t.PrecheckCapacity {
		if skip, reason := s.noCapacity(ctx, client, acc, t, ad, spec); skip {
			s.record(ctx, t, store.HuntAttempt{
				Class: "OutOfCapacity", Error: reason, AD: ad,
				NextAt: time.Now().Add(nextDelay(t)),
			})
			return
		}
	}

	req := buildLaunchRequest(acc.CompartmentOCID, ad, subnetID, t.ID, spec)
	created, err := client.LaunchInstance(ctx, t.Region, req)
	if err != nil {
		s.handleLaunchError(ctx, t, acc, ad, err)
		return
	}

	s.succeed(ctx, t, client, acc, created, spec, ad)
}

// shouldStop 判断任务是否已经该停了。
func (s *Service) shouldStop(t *store.HuntTask) (bool, string) {
	if t.MaxAttempts > 0 && t.Attempts >= t.MaxAttempts {
		return true, fmt.Sprintf("已达到最大尝试次数 %d，任务停止", t.MaxAttempts)
	}
	if !t.ExpiresAt.IsZero() && time.Now().After(t.ExpiresAt) {
		return true, "任务已到期，自动停止"
	}
	return false, ""
}

// findExisting 按任务标签查已经建出来的实例。见 attempt 里的防重复创建说明。
func (s *Service) findExisting(ctx context.Context, client *ociclient.Client,
	acc *store.Account, t *store.HuntTask) *ociclient.Instance {

	list, err := client.ListInstances(ctx, ociclient.ListInstancesOptions{
		Region:        t.Region,
		CompartmentID: acc.CompartmentOCID,
	})
	if err != nil {
		// 查不到就当没有。这里宁可漏判也不能因为一次读失败就把任务判死——
		// 真重复了下一轮还会再查一次。
		slog.Warn("守候：查重失败", "task", t.ID, "err", err)
		return nil
	}
	for i := range list {
		inst := &list[i]
		if inst.LifecycleState == "TERMINATED" || inst.LifecycleState == "TERMINATING" {
			continue
		}
		if inst.FreeformTags[tagKey] == t.ID {
			return inst
		}
	}
	return nil
}

// pickAD 选下一个要试的可用域。
//
// 容量在可用域之间不均衡，只盯一个会显著降低命中率。失败即前进，
// 不在同一个 AD 上连续撞。
func (s *Service) pickAD(ctx context.Context, client *ociclient.Client,
	acc *store.Account, t *store.HuntTask) (string, error) {

	ads := t.ADList()
	if len(ads) == 0 {
		list, err := client.ListAvailabilityDomains(ctx, t.Region, acc.CompartmentOCID)
		if err != nil {
			return "", err
		}
		for _, a := range list {
			ads = append(ads, a.Name)
		}
	}
	if len(ads) == 0 {
		return "", errors.New("该区域没有可用域")
	}

	// 从上次用过的那个往后挪一格。找不到（首次、或 AD 列表变了）就从头开始。
	idx := 0
	for i, a := range ads {
		if a == t.LastAD {
			idx = (i + 1) % len(ads)
			break
		}
	}
	return ads[idx], nil
}

const tagKey = "ocicore-hunt"

func buildLaunchRequest(compartmentID, ad, subnetID, taskID string, spec Spec) ociclient.LaunchInstanceRequest {
	assignPublic := spec.AssignPublicIP
	req := ociclient.LaunchInstanceRequest{
		CompartmentID:      compartmentID,
		AvailabilityDomain: ad,
		DisplayName:        spec.DisplayName,
		Shape:              spec.Shape,
		SourceDetails: &ociclient.SourceDetails{
			SourceType: "image",
			ImageID:    spec.ImageID,
		},
		CreateVnicDetails: &ociclient.CreateVnicDetails{
			SubnetID:       subnetID,
			AssignPublicIP: &assignPublic,
		},
		Metadata: buildMetadata(spec.SSHPublicKey, spec.CloudInit),
		// 这个标签是防重复创建的依据，不能省。
		FreeformTags: map[string]string{"created-by": "ocicore", tagKey: taskID},
	}
	if spec.Ocpus > 0 || spec.MemoryInGBs > 0 {
		req.ShapeConfig = &ociclient.ShapeConfig{
			Ocpus: spec.Ocpus, MemoryInGBs: spec.MemoryInGBs,
		}
	}
	if spec.BootVolumeGB > 0 {
		req.SourceDetails.BootVolumeSizeInGBs = spec.BootVolumeGB
	}
	if spec.EnableIPv6 {
		yes := true
		req.CreateVnicDetails.AssignIpv6IP = &yes
	}
	// 故障域留空：指定 FD 只会缩小可调度范围，抢容量时是纯粹的减分项。
	return req
}

func buildMetadata(sshKey, cloudInit string) map[string]string {
	md := map[string]string{}
	if k := strings.TrimSpace(sshKey); k != "" {
		md["ssh_authorized_keys"] = k
	}
	if c := strings.TrimSpace(cloudInit); c != "" {
		// OCI 要求 user_data 是 base64 编码的。
		md["user_data"] = base64.StdEncoding.EncodeToString([]byte(c))
	}
	if len(md) == 0 {
		return nil
	}
	return md
}

// handleLaunchError 把创建失败翻译成"下一步做什么"。
func (s *Service) handleLaunchError(ctx context.Context, t *store.HuntTask,
	acc *store.Account, ad string, err error) {

	// 拿不到 APIError（网络层直接失败、超时）时按瞬时错误处理：
	// 状态未知，不能判死，但也不能默认重发——上面的查重会兜住。
	class := ociclient.ClassTransient
	var apiErr *ociclient.APIError
	if errors.As(err, &apiErr) {
		class = apiErr.Class
	}
	name := class.String()
	msg := shortErr(err)

	switch class {
	case ociclient.ClassQuotaExceeded:
		s.finish(ctx, t, store.HuntFailed, name,
			"配额已满，继续重试没有意义："+msg)
		return

	case ociclient.ClassAuthFailed, ociclient.ClassNotAuthorized, ociclient.ClassBadRequest:
		s.finish(ctx, t, store.HuntFailed, name, msg)
		return

	case ociclient.ClassThrottled:
		// 429 是**账号级**信号。只退避当前任务、同账号其他任务照跑等于没退：
		// Oracle 看到的仍然是同一个账号在持续高频请求。
		until := time.Now().Add(throttleBackoff)
		if apiErr != nil && apiErr.Backoff() > 0 {
			until = time.Now().Add(apiErr.Backoff())
		}
		if e := s.st.DeferHuntTasksForAccount(ctx, acc.ID, until); e != nil {
			slog.Warn("守候：账号级降速失败", "account", acc.ID, "err", e)
		}
		s.record(ctx, t, store.HuntAttempt{
			Class: name, Error: "被 Oracle 限流，该账号全部任务已降速：" + msg,
			AD: ad, NextAt: until,
		})
		return
	}

	// 容量不足与网络抖动：换个可用域接着来。
	s.reschedule(ctx, t, name, msg, ad)
}

// nextDelay 计算下一次尝试要等多久。
//
// 连续撞墙越久，间隔拉得越长：一个已经试了几百次的任务说明这个组合近期
// 确实没货，再密集也只是徒增被限流的概率。抖动是为了避免多个任务在整分钟
// 同时开火——那会形成一个尖峰，正是风控最容易注意到的形状。
func nextDelay(t *store.HuntTask) time.Duration {
	base := time.Duration(NormalizeInterval(t.IntervalSeconds)) * time.Second

	switch {
	case t.Attempts >= crawlAfter:
		base *= crawlFactor
	case t.Attempts >= slowdownAfter:
		base *= slowdownFactor
	}
	if base > maxBackoff {
		base = maxBackoff
	}

	jitter := 1 + (rand.Float64()-0.5)*0.4 // ±20%
	return time.Duration(float64(base) * jitter)
}

func (s *Service) reschedule(ctx context.Context, t *store.HuntTask, class, msg, ad string) {
	s.record(ctx, t, store.HuntAttempt{
		Class: class, Error: msg, AD: ad,
		NextAt: time.Now().Add(nextDelay(t)),
	})
}

func (s *Service) record(ctx context.Context, t *store.HuntTask, a store.HuntAttempt) {
	if err := s.st.RecordHuntAttempt(ctx, t.ID, a); err != nil &&
		!errors.Is(err, store.ErrHuntNotFound) {
		slog.Warn("守候：写回尝试结果失败", "task", t.ID, "err", err)
	}
}

func (s *Service) finish(ctx context.Context, t *store.HuntTask, state, class, msg string) {
	s.record(ctx, t, store.HuntAttempt{
		Class: class, Error: msg, AD: t.LastAD, State: state,
		// 停下来的任务不再排期。
		NextAt: time.Time{},
	})
	if s.onEvent != nil {
		s.onEvent(ctx, "hunt.stopped", "守候任务已停止："+t.Name, msg)
	}
	slog.Info("守候：任务停止", "task", t.ID, "state", state, "reason", msg)
}

func (s *Service) succeed(ctx context.Context, t *store.HuntTask, client *ociclient.Client,
	acc *store.Account, inst *ociclient.Instance, spec Spec, ad string) {

	s.record(ctx, t, store.HuntAttempt{
		Class: "Succeeded", Error: "", AD: ad,
		State: store.HuntSucceeded, InstanceID: inst.ID,
		NextAt: time.Time{},
	})

	if s.onLaunch != nil {
		s.onLaunch(ctx, t, inst, t.Region, spec.SubnetID, spec.ImageID)
	}
	if s.onEvent != nil {
		s.onEvent(ctx, "hunt.succeeded", "守候成功："+t.Name,
			fmt.Sprintf("已在 %s 创建 %s（尝试 %d 次）", ad, inst.DisplayName, t.Attempts+1))
	}
	slog.Info("守候：抢到了", "task", t.ID, "instance", inst.ID, "ad", ad, "attempts", t.Attempts+1)
}

// shortErr 截断过长的错误文本。这些字符串要落库并显示在表格一行里。
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

// noCapacity 查一次容量报告，返回"是否该跳过这一轮"。
//
// 三种情况明确区分：
//   - OUT_OF_HOST_CAPACITY  → 跳过，省下一次创建请求
//   - HARDWARE_NOT_SUPPORTED → 也跳过，而且这个状态不会因为等待而改变，
//     文案里要说清楚，否则用户会盯着一个永远不可能成功的任务干等
//   - 其余（含查询失败）    → 放行，照常尝试
func (s *Service) noCapacity(ctx context.Context, client *ociclient.Client,
	acc *store.Account, t *store.HuntTask, ad string, spec Spec) (bool, string) {

	req := ociclient.CapacityShapeRequest{InstanceShape: spec.Shape}
	if spec.Ocpus > 0 || spec.MemoryInGBs > 0 {
		// 固定规格带上 ShapeConfig 会被 OCI 拒，且错误信息指向参数格式，
		// 很难联想到是这里多塞了一个字段。
		req.InstanceShapeConfig = &ociclient.ShapeConfig{
			Ocpus: spec.Ocpus, MemoryInGBs: spec.MemoryInGBs,
		}
	}

	report, err := client.CreateCapacityReport(ctx, t.Region, acc.CompartmentOCID, ad,
		[]ociclient.CapacityShapeRequest{req})
	if err != nil {
		// 放行。容量报告只是优化，不该成为新的故障点。
		slog.Debug("守候：容量预检失败，按原计划尝试", "task", t.ID, "err", err)
		return false, ""
	}

	switch report.StatusOf(spec.Shape) {
	case ociclient.CapacityOutOfHost:
		return true, "容量预检：该可用域暂时没有容量，已跳过本轮创建请求"
	case ociclient.CapacityHardwareNotSupported:
		return true, "容量预检：该区域未部署此规格的硬件。这个状态不会自行改变，请换规格或换区域"
	default:
		return false, ""
	}
}
