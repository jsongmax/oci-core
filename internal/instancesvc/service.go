// Package instancesvc 负责实例的跨账号聚合、缓存同步与生命周期编排。
package instancesvc

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ocicore/internal/notify"
	"ocicore/internal/ociclient"
	"ocicore/internal/ociconn"
	"ocicore/internal/store"
)

// syncConcurrency 限制同时进行的（账号 × 区域）同步任务数。
//
// 上限的作用不只是省本机资源：同一租户上并发太多请求容易触发 OCI 限流，
// 而限流会连带影响用户正在手动执行的操作。宁可慢一点。
const syncConcurrency = 6

// syncTimeout 是单个（账号 × 区域）同步任务的超时。
const syncTimeout = 60 * time.Second

// Service 聚合多个账号的实例并维护本地缓存。
type Service struct {
	st    *store.Store
	conns *ociconn.Factory
	bus   *Bus

	// syncMu 保证同一时刻只跑一轮全量同步。
	syncMu   sync.Mutex
	syncing  bool
	lastSync time.Time
	statusMu sync.RWMutex

	// watching 记录正在被轮询的实例，避免对同一台机器重复起协程。
	watchMu  sync.Mutex
	watching map[string]struct{}

	// imageOS 缓存 imageId → 操作系统名。镜像创建后不可变，进程内缓存一次即可，
	// 不必每轮同步都为每台机器多发一次 GetImage。
	imageMu sync.Mutex
	imageOS map[string]string

	// notifier 可为 nil：通知是旁路能力，没配也不影响任何主流程。
	notifier Notifier

	// syncInterval 存的是 time.Duration 的纳秒值。
	//
	// 用原子量而不是加锁：后台循环每轮读一次，设置接口偶尔写一次，
	// 为这点竞争引入一把锁不划算，而且它和 syncMu 保护的是完全不同的东西，
	// 复用那把锁只会让"这把锁到底管什么"变得含糊。
	syncInterval atomic.Int64
}

// Notifier 是通知分发的最小接口。
//
// 用接口而不是直接依赖 notify.Dispatcher，是为了让测试能塞一个假的进来，
// 不必为了跑一次生命周期测试就去搭一套通知渠道。
type Notifier interface {
	Dispatch(ctx context.Context, msg notify.Message)
}

// SetNotifier 挂上通知分发器。
func (s *Service) SetNotifier(n Notifier) { s.notifier = n }

// notify 在配置了分发器时推送一条通知。
func (s *Service) notify(ctx context.Context, msg notify.Message) {
	if s.notifier == nil {
		return
	}
	s.notifier.Dispatch(ctx, msg)
}

// New 创建实例服务。
func New(st *store.Store, conns *ociconn.Factory, bus *Bus) *Service {
	return &Service{
		st:       st,
		conns:    conns,
		bus:      bus,
		watching: make(map[string]struct{}),
		imageOS:  make(map[string]string),
	}
}

// Bus 返回事件总线，供 HTTP 层挂 SSE。
func (s *Service) Bus() *Bus { return s.bus }

// RegionError 是某个（账号 × 区域）同步失败的记录。
type RegionError struct {
	AccountID    string `json:"accountId"`
	AccountAlias string `json:"accountAlias"`
	Region       string `json:"region"`
	Message      string `json:"message"`
	OciCode      string `json:"ociCode,omitempty"`
	Advice       string `json:"advice,omitempty"`
}

// SyncReport 汇报一轮同步的结果。
type SyncReport struct {
	StartedAt  time.Time     `json:"startedAt"`
	Duration   time.Duration `json:"-"`
	DurationMs int64         `json:"durationMs"`
	Accounts   int           `json:"accounts"`
	// Regions 是去重后的区域数。
	//
	// 曾经直接取 len(jobs)——那是（账号 × 区域）的任务数。两个账号开在同一个
	// 区域时会被算成两个区域，界面上就会出现「5 个账号 · 6 个区域」这种
	// 自相矛盾的数字。
	Regions int `json:"regions"`
	// Instances 只统计未终止的实例，与界面上看到的数量一致。
	//
	// 之前统计的是 Oracle 返回的全部实例。OCI 在实例终止后还会把它列出来
	// 一段时间，于是同步提示说 14 台、列表和总览却都显示 13 台——因为
	// 界面各处一律过滤 TERMINATED。两个数字都没错，但摆在一起就是 bug。
	Instances int `json:"instances"`
	// Terminated 是本轮见到的已终止实例数，单独列出而不是混进 Instances。
	Terminated int   `json:"terminated"`
	Pruned     int64 `json:"pruned"`
	// Errors 按（账号 × 区域）粒度隔离：一个账号认证失效不该让整张列表变空。
	Errors []RegionError `json:"errors"`
}

// Status 是同步器的当前状态。
type Status struct {
	Syncing  bool      `json:"syncing"`
	LastSync time.Time `json:"lastSync"`
}

// Status 返回同步器状态。
func (s *Service) Status() Status {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return Status{Syncing: s.syncing, LastSync: s.lastSync}
}

func (s *Service) setSyncing(v bool) {
	s.statusMu.Lock()
	s.syncing = v
	if !v {
		s.lastSync = time.Now()
	}
	s.statusMu.Unlock()
}

// SyncAll 刷新所有启用账号的实例缓存。
//
// 同一时刻只允许一轮同步：并发同步除了浪费 API 配额，还会让
// PruneStaleInstances 互相误删对方刚写入的行。
func (s *Service) SyncAll(ctx context.Context) (*SyncReport, error) {
	if !s.syncMu.TryLock() {
		return nil, fmt.Errorf("同步正在进行中，请稍后再试")
	}
	defer s.syncMu.Unlock()

	s.setSyncing(true)
	defer s.setSyncing(false)

	report := &SyncReport{StartedAt: time.Now()}
	s.bus.Publish(Event{Type: EventSyncStarted})

	accounts, err := s.st.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}

	type job struct {
		acc    store.Account
		region string
	}
	var jobs []job
	for _, acc := range accounts {
		if !acc.Enabled {
			continue
		}
		regions := acc.EffectiveRegions()
		if len(regions) == 0 {
			continue
		}
		report.Accounts++
		for _, region := range regions {
			jobs = append(jobs, job{acc: acc, region: region})
		}
	}
	// 去重：len(jobs) 是（账号 × 区域）的任务数，不是区域数。
	seenRegions := make(map[string]struct{}, len(jobs))
	for _, j := range jobs {
		seenRegions[j.region] = struct{}{}
	}
	report.Regions = len(seenRegions)

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	sem := make(chan struct{}, syncConcurrency)

	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			jobCtx, cancel := context.WithTimeout(ctx, syncTimeout)
			defer cancel()

			count, terminated, pruned, err := s.syncAccountRegion(jobCtx, &j.acc, j.region)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				report.Errors = append(report.Errors, toRegionError(&j.acc, j.region, err))
				return
			}
			report.Instances += count
			report.Terminated += terminated
			report.Pruned += pruned
		}(j)
	}
	wg.Wait()

	// 账号级的致命错误（凭据失效）要写回账号状态，让账号卡片变红。
	s.markFailedAccounts(ctx, report.Errors)

	report.Duration = time.Since(report.StartedAt)
	report.DurationMs = report.Duration.Milliseconds()
	sort.Slice(report.Errors, func(i, j int) bool {
		return report.Errors[i].AccountAlias < report.Errors[j].AccountAlias
	})

	s.bus.Publish(Event{Type: EventSyncFinished, Data: report})
	return report, nil
}

// SyncAccount 只刷新单个账号，用于账号详情页的手动同步。
func (s *Service) SyncAccount(ctx context.Context, accountID string) (*SyncReport, error) {
	acc, err := s.st.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	report := &SyncReport{StartedAt: time.Now(), Accounts: 1}

	for _, region := range acc.EffectiveRegions() {
		report.Regions++
		regionCtx, cancel := context.WithTimeout(ctx, syncTimeout)
		count, terminated, pruned, err := s.syncAccountRegion(regionCtx, acc, region)
		cancel()
		if err != nil {
			report.Errors = append(report.Errors, toRegionError(acc, region, err))
			continue
		}
		report.Instances += count
		report.Terminated += terminated
		report.Pruned += pruned
	}

	s.markFailedAccounts(ctx, report.Errors)
	report.Duration = time.Since(report.StartedAt)
	report.DurationMs = report.Duration.Milliseconds()
	s.bus.Publish(Event{Type: EventSyncFinished, AccountID: accountID, Data: report})
	return report, nil
}

// syncAccountRegion 同步一个账号在一个区域内的全部实例。
// syncAccountRegion 返回（未终止实例数, 已终止实例数, 清理掉的陈旧行数, 错误）。
func (s *Service) syncAccountRegion(ctx context.Context, acc *store.Account, region string) (int, int, int64, error) {
	client, err := s.conns.For(ctx, acc)
	if err != nil {
		return 0, 0, 0, err
	}

	// 记在拉取之前：本轮没被刷新到的行说明实例已在 Oracle 侧消失。
	startedAt := time.Now()

	instances, err := client.ListInstances(ctx, ociclient.ListInstancesOptions{
		CompartmentID: acc.CompartmentOCID,
		Region:        region,
	})
	if err != nil {
		return 0, 0, 0, err
	}

	// 网络与存储信息按区域批量取一次，避免每台机器都单独查一遍。
	netInfo := s.collectNetworkInfo(ctx, client, acc, region, instances)
	bootInfo := s.collectBootVolumeInfo(ctx, client, acc, region, instances)
	imageOS := s.collectImageOS(ctx, client, region, instances)

	saved, terminated := 0, 0
	for _, inst := range instances {
		row := store.Instance{
			ID:                 inst.ID,
			AccountID:          acc.ID,
			Region:             region,
			CompartmentID:      inst.CompartmentID,
			DisplayName:        inst.DisplayName,
			AvailabilityDomain: inst.AvailabilityDomain,
			FaultDomain:        inst.FaultDomain,
			Shape:              inst.Shape,
			LifecycleState:     inst.LifecycleState,
			ImageID:            inst.ImageID,
			ImageOS:            imageOS[inst.ImageID],
			TimeCreated:        inst.TimeCreated,
		}
		if inst.ShapeConfig != nil {
			row.Ocpus = float64(inst.ShapeConfig.Ocpus)
			row.MemoryGB = float64(inst.ShapeConfig.MemoryInGBs)
			row.BaselineOcpu = inst.ShapeConfig.BaselineOcpuUtilization
		}
		if n, ok := netInfo[inst.ID]; ok {
			row.PublicIP = n.publicIP
			row.PrivateIP = n.privateIP
			row.IPv6 = n.ipv6
			row.SecondaryIPv4 = n.secondaryIPv4
			row.SecondaryIPv6 = n.secondaryIPv6
			row.VnicID = n.vnicID
			row.SubnetID = n.subnetID
		}
		if b, ok := bootInfo[inst.ID]; ok {
			row.BootVolumeID = b.id
			row.BootVolumeGB = b.sizeGB
			row.BootVolumeVpus = b.vpus
		}

		if err := s.st.UpsertInstance(ctx, row); err != nil {
			slog.Warn("写入实例缓存失败", "instance", inst.ID, "err", err)
			continue
		}
		// 已终止的实例照常入库（列表里的"已终止"筛选还要用），
		// 但不计进对外报告的台数——界面各处都不显示它们。
		if inst.LifecycleState == ociclient.LifecycleTerminated {
			terminated++
			continue
		}
		saved++
	}

	pruned, err := s.st.PruneStaleInstances(ctx, acc.ID, region, startedAt)
	if err != nil {
		slog.Warn("清理实例缓存失败", "account", acc.ID, "region", region, "err", err)
	}
	return saved, terminated, pruned, nil
}

type netDetail struct {
	vnicID    string
	subnetID  string
	publicIP  string
	privateIP string
	// ipv6 是逗号分隔的全部地址；一张网卡可以有多个 IPv6。
	ipv6 string
	// 附属网卡上的公网 IPv4 与 IPv6，逗号分隔。
	secondaryIPv4 string
	secondaryIPv6 string
}

// collectNetworkInfo 取回每台实例全部网卡上的 IP。
//
// 公网 IP 是实例列表里最关键的一列（用户要拿它 SSH），所以必须在同步时就取到。
// IPv6 随 GetVnic 的 ipv6Addresses 一起返回，不额外发请求。
// 附属网卡只有多网卡的实例才有，绝大多数实例仍是每台一次 GetVnic。
func (s *Service) collectNetworkInfo(ctx context.Context, client *ociclient.Client,
	acc *store.Account, region string, instances []ociclient.Instance) map[string]netDetail {

	out := make(map[string]netDetail, len(instances))
	if len(instances) == 0 {
		return out
	}

	attachments, err := client.ListVnicAttachments(ctx, region, acc.CompartmentOCID, "")
	if err != nil {
		slog.Warn("读取网卡关联失败", "account", acc.Alias, "region", region, "err", err)
		return out
	}

	byInstance := make(map[string][]ociclient.VnicAttachment, len(instances))
	for _, att := range attachments {
		byInstance[att.InstanceID] = append(byInstance[att.InstanceID], att)
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	sem := make(chan struct{}, syncConcurrency)

	for instanceID, atts := range byInstance {
		wg.Add(1)
		go func(instanceID string, atts []ociclient.VnicAttachment) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			primary, secondary := splitVnics(ctx, client, region, atts)
			if primary == nil {
				return
			}
			d := netDetail{
				vnicID:    primary.ID,
				subnetID:  primary.SubnetID,
				publicIP:  primary.PublicIP,
				privateIP: primary.PrivateIP,
				ipv6:      strings.Join(primary.Ipv6Addresses, ","),
			}
			var v4, v6 []string
			for _, v := range secondary {
				if v.PublicIP != "" {
					v4 = append(v4, v.PublicIP)
				}
				v6 = append(v6, v.Ipv6Addresses...)
			}
			d.secondaryIPv4 = strings.Join(v4, ",")
			d.secondaryIPv6 = strings.Join(v6, ",")

			mu.Lock()
			out[instanceID] = d
			mu.Unlock()
		}(instanceID, atts)
	}
	wg.Wait()
	return out
}

// splitVnics 读取已挂载的网卡，按 isPrimary 分出主网卡与附属网卡。
//
// 不能按 nicIndex 取最小值：VM 规格只有一块物理网卡，所有 VNIC 的 nicIndex
// 都是 0，那样等于随机挑一张，附属网卡的 IP 会被当成主 IP 显示。
// 读不到 isPrimary 时（理论上不会）退回第一张能读到的网卡。
func splitVnics(ctx context.Context, client *ociclient.Client, region string,
	atts []ociclient.VnicAttachment) (*ociclient.Vnic, []ociclient.Vnic) {

	var all []ociclient.Vnic
	for _, att := range atts {
		if att.LifecycleState != "ATTACHED" || att.VnicID == "" {
			continue
		}
		vnic, err := client.GetVnic(ctx, region, att.VnicID)
		if err != nil {
			slog.Debug("读取网卡失败", "vnic", att.VnicID, "err", err)
			continue
		}
		all = append(all, *vnic)
	}
	if len(all) == 0 {
		return nil, nil
	}
	idx := 0
	for i, v := range all {
		if v.IsPrimary {
			idx = i
			break
		}
	}
	primary := all[idx]
	secondary := append(all[:idx:idx], all[idx+1:]...)
	return &primary, secondary
}

type bootDetail struct {
	id     string
	sizeGB int64
	vpus   int64
}

// collectImageOS 取回每个镜像的操作系统名，前端靠它推断 SSH 默认用户。
// 查不到的镜像映射为空串，不影响同步本身。
func (s *Service) collectImageOS(ctx context.Context, client *ociclient.Client,
	region string, instances []ociclient.Instance) map[string]string {

	out := make(map[string]string)
	for _, inst := range instances {
		if _, done := out[inst.ImageID]; !done && inst.ImageID != "" {
			out[inst.ImageID] = s.ImageOS(ctx, client, region, inst.ImageID)
		}
	}
	return out
}

// ImageOS 返回镜像的操作系统名，带进程内缓存。镜像创建后不可变，查一次就够。
// 查不到时返回空串——它只用来推断 SSH 用户，不值得让调用方处理错误。
func (s *Service) ImageOS(ctx context.Context, client *ociclient.Client, region, imageID string) string {
	s.imageMu.Lock()
	osName, cached := s.imageOS[imageID]
	s.imageMu.Unlock()
	if cached {
		return osName
	}

	img, err := client.GetImage(ctx, region, imageID)
	if err != nil {
		// 自定义镜像删掉后会一直 404，记成空值，免得每轮同步都再撞一次。
		// 其它错误（限流、网络）不缓存，下一轮再试。
		if apiErr, ok := ociclient.AsAPIError(err); ok && apiErr.StatusCode == http.StatusNotFound {
			s.imageMu.Lock()
			s.imageOS[imageID] = ""
			s.imageMu.Unlock()
		} else {
			slog.Debug("读取镜像失败", "image", imageID, "region", region, "err", err)
		}
		return ""
	}
	s.imageMu.Lock()
	s.imageOS[imageID] = img.OperatingSystem
	s.imageMu.Unlock()
	return img.OperatingSystem
}

// collectBootVolumeInfo 取回每台实例的引导卷信息。
func (s *Service) collectBootVolumeInfo(ctx context.Context, client *ociclient.Client,
	acc *store.Account, region string, instances []ociclient.Instance) map[string]bootDetail {

	out := make(map[string]bootDetail, len(instances))
	if len(instances) == 0 {
		return out
	}

	// 引导卷相关接口都按可用域查询，先收集本区域实际用到的可用域。
	ads := make(map[string]struct{})
	for _, inst := range instances {
		if inst.AvailabilityDomain != "" {
			ads[inst.AvailabilityDomain] = struct{}{}
		}
	}

	volumes := make(map[string]ociclient.BootVolume)
	attachments := make(map[string]string) // instanceID -> bootVolumeID

	for ad := range ads {
		vols, err := client.ListBootVolumes(ctx, region, acc.CompartmentOCID, ad)
		if err != nil {
			slog.Debug("读取引导卷失败", "region", region, "ad", ad, "err", err)
		} else {
			for _, v := range vols {
				volumes[v.ID] = v
			}
		}

		atts, err := client.ListBootVolumeAttachments(ctx, region, acc.CompartmentOCID, ad, "")
		if err != nil {
			slog.Debug("读取引导卷挂载失败", "region", region, "ad", ad, "err", err)
			continue
		}
		for _, att := range atts {
			if att.LifecycleState == "ATTACHED" {
				attachments[att.InstanceID] = att.BootVolumeID
			}
		}
	}

	for instanceID, volumeID := range attachments {
		if v, ok := volumes[volumeID]; ok {
			out[instanceID] = bootDetail{id: v.ID, sizeGB: v.SizeInGBs, vpus: v.VpusPerGB}
		} else {
			out[instanceID] = bootDetail{id: volumeID}
		}
	}
	return out
}

// markFailedAccounts 把凭据类失败写回账号状态。
//
// 只处理 AccountFatal 的错误：区域级的临时故障不该把整个账号标红，
// 那会让用户以为密钥出了问题而去做无谓的排查。
func (s *Service) markFailedAccounts(ctx context.Context, errs []RegionError) {
	seen := make(map[string]struct{}, len(errs))
	for _, e := range errs {
		if e.OciCode == "" || e.AccountID == "" {
			continue
		}
		if _, done := seen[e.AccountID]; done {
			continue
		}
		if !isAccountFatalCode(e.OciCode) {
			continue
		}
		seen[e.AccountID] = struct{}{}
		msg := e.OciCode + " " + e.Message
		if err := s.st.SetAccountStatus(ctx, e.AccountID, store.StatusError, msg); err != nil {
			slog.Warn("更新账号状态失败", "account", e.AccountID, "err", err)
			continue
		}
		s.bus.Publish(Event{
			Type: EventAccountStatus, AccountID: e.AccountID,
			State: store.StatusError, Message: msg,
		})
		s.notify(ctx, notify.Message{
			Event: notify.EventAccountAuthFail,
			Title: "账号 " + e.AccountAlias + " 凭据校验失败",
			Body:  e.Advice,
			Fields: map[string]string{
				"账号":  e.AccountAlias,
				"错误码": e.OciCode,
				"详情":  e.Message,
			},
		})
	}
}

func isAccountFatalCode(code string) bool {
	switch code {
	case "NotAuthenticated", "NotAuthorizedOrNotFound", "NotAuthorized",
		"InvalidAuthorization", "SignatureDoesNotMatch":
		return true
	}
	return false
}

func toRegionError(acc *store.Account, region string, err error) RegionError {
	re := RegionError{
		AccountID:    acc.ID,
		AccountAlias: acc.Alias,
		Region:       region,
		Message:      err.Error(),
	}
	if apiErr, ok := ociclient.AsAPIError(err); ok {
		re.Message = apiErr.Message
		re.OciCode = apiErr.Code
		re.Advice = apiErr.Advice()
	}
	return re
}

// StartBackgroundSync 每隔 interval 跑一轮全量同步，直到 ctx 取消。
//
// 启动后立刻同步一次，让面板打开时就有数据，不用等第一个周期。
func (s *Service) StartBackgroundSync(ctx context.Context, interval time.Duration) {
	s.SetSyncInterval(interval)

	go func() {
		// 稍等片刻再首次同步，避开服务启动时的其他初始化工作。
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}

		for {
			if report, err := s.SyncAll(ctx); err != nil {
				slog.Warn("后台同步失败", "err", err)
			} else if len(report.Errors) > 0 {
				slog.Info("后台同步完成，部分区域失败",
					"instances", report.Instances, "errors", len(report.Errors))
			}

			// 每轮重新读一次间隔，而不是闭包里捕获一个常量。
			//
			// 捕获常量的话，改设置就必须重启服务才生效——而"改了没反应"
			// 和"改坏了"在用户那里长得一模一样，只能靠一句提示文案打补丁。
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.SyncInterval()):
			}
		}
	}()
}

// SetSyncInterval 调整后台同步间隔，下一轮生效。
func (s *Service) SetSyncInterval(d time.Duration) {
	if d <= 0 {
		d = 5 * time.Minute
	}
	s.syncInterval.Store(int64(d))
}

// SyncInterval 返回当前的后台同步间隔。
func (s *Service) SyncInterval() time.Duration {
	if v := s.syncInterval.Load(); v > 0 {
		return time.Duration(v)
	}
	return 5 * time.Minute
}
