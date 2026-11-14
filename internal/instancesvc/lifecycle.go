package instancesvc

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"ocicore/internal/i18n"
	"ocicore/internal/notify"
	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

const (
	// bootVolumeAttempts 是实例落定后查引导卷的次数，每次间隔 watchInterval。
	bootVolumeAttempts = 4
	// watchInterval 是生命周期转换的轮询间隔。
	// 转换通常要 30–90 秒，5 秒一次既能及时反馈又不至于打爆 API。
	watchInterval = 5 * time.Second
	// watchTimeout 是放弃轮询的上限。超过说明 OCI 侧卡住了，
	// 继续轮询没有意义，交给下一轮全量同步兜底。
	watchTimeout = 15 * time.Minute
)

// ErrInstanceBusy 表示实例正处于转换中，不接受新操作。
var ErrInstanceBusy = i18n.Err("inst.busy")

// Action 对实例执行开关机等操作。
//
// 返回的实例状态一定是过渡态（STARTING / STOPPING），不是终态——
// 这正是设计规格要求的"乐观更新只能更新到过渡态"。真正落定由后台轮询完成。
func (s *Service) Action(ctx context.Context, instanceID, action string) (*store.Instance, error) {
	if err := ociclient.ValidateAction(action); err != nil {
		return nil, err
	}

	cached, err := s.st.GetInstance(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	// 转换期间拒绝新操作，与前端"过渡期间按钮禁用"形成双保险——
	// 前端可以被绕过，后端不能。
	if ociclient.IsTransitionalState(cached.LifecycleState) {
		return nil, ErrInstanceBusy
	}

	client, _, err := s.conns.ForID(ctx, cached.AccountID)
	if err != nil {
		return nil, err
	}

	updated, err := client.InstanceAction(ctx, cached.Region, instanceID, action)
	if err != nil {
		s.recordFailure(ctx, instanceID, err)
		return nil, err
	}

	// 操作已被受理，清掉上一次可能残留的错误提示。
	_ = s.st.ClearInstanceError(ctx, instanceID)
	if err := s.st.SetInstanceState(ctx, instanceID, updated.LifecycleState); err != nil {
		slog.Warn("更新实例状态失败", "instance", instanceID, "err", err)
	}
	s.bus.Publish(Event{
		Type: EventInstanceUpdated, InstanceID: instanceID,
		AccountID: cached.AccountID, State: updated.LifecycleState,
	})

	s.Watch(instanceID)
	return s.st.GetInstance(ctx, instanceID)
}

// Terminate 终止实例。
//
// 这是不可逆操作，HTTP 层必须已经完成 L3 输名确认与操作策略校验才允许调到这里。
func (s *Service) Terminate(ctx context.Context, instanceID string, preserveBootVolume bool) error {
	cached, err := s.st.GetInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	client, _, err := s.conns.ForID(ctx, cached.AccountID)
	if err != nil {
		return err
	}

	if err := client.TerminateInstance(ctx, cached.Region, instanceID, preserveBootVolume); err != nil {
		s.recordFailure(ctx, instanceID, err)
		return err
	}

	_ = s.st.ClearInstanceError(ctx, instanceID)
	_ = s.st.SetInstanceState(ctx, instanceID, ociclient.LifecycleTerminating)
	s.bus.Publish(Event{
		Type: EventInstanceUpdated, InstanceID: instanceID,
		AccountID: cached.AccountID, State: ociclient.LifecycleTerminating,
	})

	s.Watch(instanceID)
	return nil
}

// Rename 修改实例显示名。这是安全操作，不需要停机。
func (s *Service) Rename(ctx context.Context, instanceID, displayName string) (*store.Instance, error) {
	name := strings.TrimSpace(displayName)
	if name == "" {
		return nil, i18n.Err("inst.name_required")
	}

	cached, err := s.st.GetInstance(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	client, _, err := s.conns.ForID(ctx, cached.AccountID)
	if err != nil {
		return nil, err
	}

	updated, err := client.UpdateInstance(ctx, cached.Region, instanceID,
		ociclient.UpdateInstanceRequest{DisplayName: name})
	if err != nil {
		s.recordFailure(ctx, instanceID, err)
		return nil, err
	}

	cached.DisplayName = updated.DisplayName
	if err := s.st.UpsertInstance(ctx, *cached); err != nil {
		return nil, err
	}
	s.bus.Publish(Event{Type: EventInstanceUpdated, InstanceID: instanceID, AccountID: cached.AccountID})
	return s.st.GetInstance(ctx, instanceID)
}

// ReshapeRequest 是修改实例规格的请求。
type ReshapeRequest struct {
	Ocpus       float32
	MemoryInGBs float32
	// BaselineOcpu 为空表示不改突发设置；要改回独占传 BASELINE_1_1。
	BaselineOcpu string
}

// Reshape 修改实例的 OCPU 与内存。
//
// OCI 要求实例处于 STOPPED 才能改配置。这里提前拦下并给出明确提示，
// 而不是让用户收到一句晦涩的 409 IncorrectState。
func (s *Service) Reshape(ctx context.Context, instanceID string, req ReshapeRequest) (*store.Instance, error) {
	cached, err := s.st.GetInstance(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	// 不要求先关机。
	//
	// Oracle 允许直接改运行中实例的 shapeConfig，保存时它自己重启一次；
	// 之前这里硬性要求 STOPPED，是我加的、比 Oracle 更严的限制，而且方向反了：
	// 它逼着用户走「停机 → 改配置 → 开机」，而完整停机会释放宿主机上的容量，
	// A1 紧张的区域里很可能就再也开不回来。让 Oracle 自己重启反而更安全。
	//
	// 仍然挡住过渡态：正在关机/开机的实例上改配置，结果无法预期。
	if ociclient.IsTransitionalState(cached.LifecycleState) {
		return nil, i18n.Err("inst.reshape_transitional", cached.LifecycleState)
	}
	if req.Ocpus <= 0 || req.MemoryInGBs <= 0 {
		return nil, i18n.Err("inst.reshape_positive")
	}

	client, acc, err := s.conns.ForID(ctx, cached.AccountID)
	if err != nil {
		return nil, err
	}

	// 拿到该规格的取值范围后再校验，比硬编码 A1.Flex 的规则可靠——
	// Oracle 会调整免费额度的上限，规格元数据才是权威来源。
	if shapes, err := client.ListShapes(ctx, cached.Region, acc.CompartmentOCID, cached.AvailabilityDomain); err == nil {
		for i := range shapes {
			if shapes[i].Shape == cached.Shape {
				cfg := ociclient.ShapeConfig{Ocpus: req.Ocpus, MemoryInGBs: req.MemoryInGBs,
					BaselineOcpuUtilization: req.BaselineOcpu}
				if err := ociclient.ValidateShapeConfig(&shapes[i], cfg); err != nil {
					return nil, err
				}
				break
			}
		}
	}

	updated, err := client.UpdateInstance(ctx, cached.Region, instanceID, ociclient.UpdateInstanceRequest{
		ShapeConfig: &ociclient.ShapeConfig{Ocpus: req.Ocpus, MemoryInGBs: req.MemoryInGBs,
			BaselineOcpuUtilization: req.BaselineOcpu},
	})
	if err != nil {
		s.recordFailure(ctx, instanceID, err)
		return nil, err
	}

	_ = s.st.ClearInstanceError(ctx, instanceID)
	if updated.ShapeConfig != nil {
		cached.Ocpus = float64(updated.ShapeConfig.Ocpus)
		cached.MemoryGB = float64(updated.ShapeConfig.MemoryInGBs)
		cached.BaselineOcpu = updated.ShapeConfig.BaselineOcpuUtilization
	}
	cached.LifecycleState = updated.LifecycleState
	if err := s.st.UpsertInstance(ctx, *cached); err != nil {
		return nil, err
	}
	s.bus.Publish(Event{
		Type: EventInstanceUpdated, InstanceID: instanceID,
		AccountID: cached.AccountID, State: updated.LifecycleState,
	})
	return s.st.GetInstance(ctx, instanceID)
}

// ReplaceBootVolumeRequest 是用镜像替换引导卷（重装系统）的请求。
type ReplaceBootVolumeRequest struct {
	ImageID string
	// BootVolumeGB 为 0 时按镜像默认大小。
	BootVolumeGB int64
	// PreserveOld 为 true 时旧引导卷保留下来，仍占块存储配额。
	PreserveOld bool
	// SSHPublicKey / UserData 非空时写进实例元数据，新系统首次启动时 cloud-init 会读取。
	// UserData 已经是 base64 编码。
	SSHPublicKey string
	UserData     string
}

// MinBootVolumeGB 是 Oracle 对引导卷的最小容量要求。
const MinBootVolumeGB = 50

// ReplaceBootVolume 用新镜像替换实例的引导卷，相当于重装系统。
//
// 这条路保留实例本身：OCID、公网 IP（临时 IP 一般不变）和 A1 宿主机上的容量
// 都还在。终止再重建在 A1 紧张的区域里很可能就抢不回来了。
//
// 运行中和已关机的实例都可以换；过渡态不行——卡在「关机中」的机器得先强制关机。
func (s *Service) ReplaceBootVolume(ctx context.Context, instanceID string, req ReplaceBootVolumeRequest) (*store.Instance, error) {
	if strings.TrimSpace(req.ImageID) == "" {
		return nil, i18n.Err("inst.image_required")
	}
	if req.BootVolumeGB != 0 && req.BootVolumeGB < MinBootVolumeGB {
		return nil, i18n.Err("inst.boot_min", MinBootVolumeGB)
	}

	cached, err := s.st.GetInstance(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if ociclient.IsTransitionalState(cached.LifecycleState) {
		return nil, i18n.Err("inst.replace_transitional", cached.LifecycleState)
	}

	client, _, err := s.conns.ForID(ctx, cached.AccountID)
	if err != nil {
		return nil, err
	}

	preserve := req.PreserveOld
	update := ociclient.UpdateInstanceRequest{
		SourceDetails: &ociclient.UpdateInstanceSourceDetails{
			SourceType:                  ociclient.SourceTypeImage,
			ImageID:                     req.ImageID,
			BootVolumeSizeInGBs:         req.BootVolumeGB,
			IsPreserveBootVolumeEnabled: &preserve,
		},
	}

	// 元数据是整体替换的，必须在 Oracle 上现有的那份基础上改，
	// 否则会把用户之前配的其它键一并抹掉。
	sshKey := strings.TrimSpace(req.SSHPublicKey)
	if sshKey != "" || req.UserData != "" {
		live, err := client.GetInstance(ctx, cached.Region, instanceID)
		if err != nil {
			return nil, err
		}
		meta := make(map[string]string, len(live.Metadata)+2)
		for k, v := range live.Metadata {
			meta[k] = v
		}
		if sshKey != "" {
			meta["ssh_authorized_keys"] = sshKey
		}
		if req.UserData != "" {
			meta["user_data"] = req.UserData
		}
		update.Metadata = meta
	}

	updated, err := client.UpdateInstance(ctx, cached.Region, instanceID, update)
	if err != nil {
		s.recordFailure(ctx, instanceID, err)
		return nil, err
	}

	_ = s.st.ClearInstanceError(ctx, instanceID)
	// 旧盘的信息作废：清掉后落定时 refreshBootVolume 才会去查新盘。
	// 换了 ID，UpsertInstance 里「同一块卷容量为 0 则保留旧值」的保护不会生效。
	cached.ImageID = req.ImageID
	cached.ImageOS = s.ImageOS(ctx, client, cached.Region, req.ImageID)
	cached.BootVolumeID = ""
	cached.BootVolumeGB = 0
	cached.BootVolumeVpus = 0
	if updated.LifecycleState != "" {
		cached.LifecycleState = updated.LifecycleState
	}
	if err := s.st.UpsertInstance(ctx, *cached); err != nil {
		return nil, err
	}
	s.bus.Publish(Event{
		Type: EventInstanceUpdated, InstanceID: instanceID,
		AccountID: cached.AccountID, State: cached.LifecycleState,
	})
	return s.st.GetInstance(ctx, instanceID)
}

// RefreshBootVolume 重新读取实例当前挂着的引导卷并写回缓存，不管缓存里是否已有值。
//
// 替换引导卷时实例中途会停在 STOPPED，那一刻轮询协程就会去补引导卷信息，
// 可能读到还没换下来的旧盘。任务在实例最终落定后再调一次这里，以 Oracle 为准。
func (s *Service) RefreshBootVolume(ctx context.Context, instanceID string) error {
	cached, err := s.st.GetInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	client, _, err := s.conns.ForID(ctx, cached.AccountID)
	if err != nil {
		return err
	}
	vol, err := findBootVolume(ctx, client, cached.Region, cached.CompartmentID, cached.AvailabilityDomain, cached.ID)
	if err != nil {
		return err
	}
	if vol == nil {
		return i18n.Err("inst.no_attached_boot")
	}
	fresh, err := s.st.GetInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	fresh.BootVolumeID = vol.ID
	fresh.BootVolumeGB = vol.SizeInGBs
	fresh.BootVolumeVpus = vol.VpusPerGB
	if err := s.st.UpsertInstance(ctx, *fresh); err != nil {
		return err
	}
	s.bus.Publish(Event{
		Type: EventInstanceUpdated, InstanceID: instanceID,
		AccountID: cached.AccountID, State: fresh.LifecycleState,
	})
	return nil
}

// Watch 启动对某台实例的状态轮询。重复调用是安全的——
// 同一台机器同时只会有一个轮询协程。
func (s *Service) Watch(instanceID string) {
	s.watchMu.Lock()
	if _, exists := s.watching[instanceID]; exists {
		s.watchMu.Unlock()
		return
	}
	s.watching[instanceID] = struct{}{}
	s.watchMu.Unlock()

	go s.watchLoop(instanceID)
}

// watchLoop 轮询实例直到状态落定。
//
// 用 context.Background 而不是请求的 context：用户点完关机就关掉浏览器
// 是完全正常的行为，轮询必须继续到状态落定，否则缓存会永远停在过渡态。
func (s *Service) watchLoop(instanceID string) {
	defer func() {
		s.watchMu.Lock()
		delete(s.watching, instanceID)
		s.watchMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), watchTimeout)
	defer cancel()

	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Warn("实例状态轮询超时", "instance", instanceID)
			return
		case <-ticker.C:
		}

		cached, err := s.st.GetInstance(ctx, instanceID)
		if err != nil {
			// 缓存行没了说明实例已被清理，无需继续。
			return
		}

		client, _, err := s.conns.ForID(ctx, cached.AccountID)
		if err != nil {
			slog.Warn("轮询建连失败", "instance", instanceID, "err", err)
			return
		}

		inst, err := client.GetInstance(ctx, cached.Region, instanceID)
		if err != nil {
			// 终止完成后 OCI 会返回 404，这是预期结局而非故障。
			if apiErr, ok := ociclient.AsAPIError(err); ok && apiErr.StatusCode == 404 {
				s.finishTerminated(ctx, cached)
				return
			}
			slog.Debug("轮询实例失败", "instance", instanceID, "err", err)
			continue
		}

		if inst.LifecycleState == cached.LifecycleState {
			continue
		}

		if inst.LifecycleState == ociclient.LifecycleTerminated {
			s.finishTerminated(ctx, cached)
			return
		}

		_ = s.st.SetInstanceState(ctx, instanceID, inst.LifecycleState)
		s.bus.Publish(Event{
			Type: EventInstanceUpdated, InstanceID: instanceID,
			AccountID: cached.AccountID, State: inst.LifecycleState,
		})

		if !ociclient.IsTransitionalState(inst.LifecycleState) {
			// 状态已落定。开机会分配新的公网 IP，需要补一次网络信息，
			// 否则列表里的 IP 列会一直显示旧值。
			s.refreshNetwork(ctx, cached, client)
			s.refreshBootVolume(ctx, cached, client)

			// 从 PROVISIONING 落到 RUNNING 才算"新实例就绪"——
			// 这是用户真正在等的那一刻，也是唯一值得推送的实例事件。
			if cached.LifecycleState == ociclient.LifecycleProvisioning &&
				inst.LifecycleState == ociclient.LifecycleRunning {
				s.notifyInstanceReady(ctx, cached.ID)
			}
			return
		}
	}
}

// finishTerminated 处理实例已终止：从缓存移除并通知前端。
func (s *Service) finishTerminated(ctx context.Context, cached *store.Instance) {
	if err := s.st.DeleteInstance(ctx, cached.ID); err != nil {
		slog.Warn("移除实例缓存失败", "instance", cached.ID, "err", err)
	}
	s.bus.Publish(Event{
		Type: EventInstanceRemoved, InstanceID: cached.ID,
		AccountID: cached.AccountID, State: ociclient.LifecycleTerminated,
	})
}

// refreshNetwork 重新拉取实例的网络信息。
func (s *Service) refreshNetwork(ctx context.Context, cached *store.Instance, client *ociclient.Client) {
	atts, err := client.ListVnicAttachments(ctx, cached.Region, cached.CompartmentID, cached.ID)
	if err != nil || len(atts) == 0 {
		return
	}

	vnic, secondary := splitVnics(ctx, client, cached.Region, atts)
	if vnic == nil {
		return
	}

	fresh, err := s.st.GetInstance(ctx, cached.ID)
	if err != nil {
		return
	}
	fresh.PublicIP = vnic.PublicIP
	fresh.PrivateIP = vnic.PrivateIP
	fresh.VnicID = vnic.ID
	fresh.SubnetID = vnic.SubnetID
	fresh.IPv6 = strings.Join(vnic.Ipv6Addresses, ",")
	fresh.SecondaryIPv4, fresh.SecondaryIPv6 = secondaryIPs(secondary)
	if err := s.st.UpsertInstance(ctx, *fresh); err != nil {
		return
	}
	s.bus.Publish(Event{
		Type: EventInstanceUpdated, InstanceID: cached.ID,
		AccountID: cached.AccountID, State: fresh.LifecycleState,
	})
}

// findBootVolume 查实例当前挂着的引导卷。没有挂载中的引导卷时返回 nil, nil
// （还没挂好，或救援模式下被分离了）。
func findBootVolume(ctx context.Context, client *ociclient.Client,
	region, compartmentID, availabilityDomain, instanceID string) (*ociclient.BootVolume, error) {

	atts, err := client.ListBootVolumeAttachments(ctx, region, compartmentID, availabilityDomain, instanceID)
	if err != nil {
		return nil, err
	}
	for _, att := range atts {
		if att.LifecycleState == "ATTACHED" {
			return client.GetBootVolume(ctx, region, att.BootVolumeID)
		}
	}
	return nil, nil
}

// refreshBootVolume 给还不知道容量的实例补上引导卷信息。
//
// 创建时写入缓存的那一行没有引导卷，以前要等下一轮全量同步才有，这期间列表上
// 显示 0 GB。已经有容量的实例直接跳过：开关机不会改变引导卷，不必每次多发请求。
// 实例刚落定时挂载关系可能还没就绪，所以重试几次。
func (s *Service) refreshBootVolume(ctx context.Context, cached *store.Instance, client *ociclient.Client) {
	if current, err := s.st.GetInstance(ctx, cached.ID); err != nil || current.BootVolumeGB > 0 {
		return
	}

	for attempt := 0; attempt < bootVolumeAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(watchInterval):
			}
		}
		vol, err := findBootVolume(ctx, client, cached.Region, cached.CompartmentID, cached.AvailabilityDomain, cached.ID)
		if err != nil {
			slog.Debug("读取引导卷失败", "instance", cached.ID, "err", err)
			continue
		}
		if vol == nil {
			continue
		}
		// 查询期间实例可能被改名、改状态，写回前重新读一次。
		fresh, err := s.st.GetInstance(ctx, cached.ID)
		if err != nil {
			return
		}
		fresh.BootVolumeID = vol.ID
		fresh.BootVolumeGB = vol.SizeInGBs
		fresh.BootVolumeVpus = vol.VpusPerGB
		if err := s.st.UpsertInstance(ctx, *fresh); err != nil {
			slog.Warn("写入引导卷信息失败", "instance", cached.ID, "err", err)
			return
		}
		s.bus.Publish(Event{
			Type: EventInstanceUpdated, InstanceID: cached.ID,
			AccountID: cached.AccountID, State: fresh.LifecycleState,
		})
		return
	}
}

// notifyInstanceReady 推送"新实例已就绪"。
//
// 刻意在 refreshNetwork 之后再读一次缓存：用户最想知道的是公网 IP，
// 而那要等网络信息补齐才有值。
func (s *Service) notifyInstanceReady(ctx context.Context, instanceID string) {
	inst, err := s.st.GetInstance(ctx, instanceID)
	if err != nil {
		return
	}
	fields := map[string]string{
		i18n.M("f.instance"): inst.DisplayName,
		i18n.M("f.region"):   inst.Region,
		i18n.M("f.shape"):    inst.Shape,
	}
	body := ""
	if inst.PublicIP != "" {
		fields[i18n.M("f.public_ip")] = inst.PublicIP
		body = "ssh ubuntu@" + inst.PublicIP
	}
	s.notify(ctx, notify.Message{
		Event:  notify.EventInstanceCreated,
		Title:  i18n.M("inst.ready_title", inst.DisplayName),
		Body:   body,
		Fields: fields,
	})
}

// recordFailure 记录一次操作失败，让前端能在该行浮出错误条。
//
// 对应设计规格的"失败必须可见地回滚，不能静默恢复"。
func (s *Service) recordFailure(ctx context.Context, instanceID string, err error) {
	msg := err.Error()
	if apiErr, ok := ociclient.AsAPIError(err); ok {
		code := apiErr.Code
		if code == "" {
			code = fmt.Sprintf("HTTP %d", apiErr.StatusCode)
		}
		msg = code + " · " + apiErr.Message
	}
	if e := s.st.SetInstanceError(ctx, instanceID, msg); e != nil {
		slog.Warn("记录实例错误失败", "instance", instanceID, "err", e)
	}
	s.bus.Publish(Event{Type: EventInstanceError, InstanceID: instanceID, Message: msg})
}

// ResumeWatches 在服务启动时为所有停在过渡态的实例重新起轮询。
//
// 进程重启会丢掉内存里的轮询协程，没有这一步的话，那些行会永远
// 卡在"关机中"，直到下一轮全量同步——而全量同步默认 5 分钟才一次。
func (s *Service) ResumeWatches(ctx context.Context) int {
	instances, err := s.st.ListInstances(ctx, store.InstanceFilter{})
	if err != nil {
		slog.Warn("恢复轮询失败", "err", err)
		return 0
	}
	resumed := 0
	for _, inst := range instances {
		if ociclient.IsTransitionalState(inst.LifecycleState) {
			s.Watch(inst.ID)
			resumed++
		}
	}
	if resumed > 0 {
		slog.Info("已恢复实例状态轮询", "count", resumed)
	}
	return resumed
}

// WatchingCount 返回正在轮询的实例数，用于诊断。
func (s *Service) WatchingCount() int {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	return len(s.watching)
}
