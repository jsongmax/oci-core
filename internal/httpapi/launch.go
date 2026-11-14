package httpapi

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"ocicore/internal/i18n"
	"ocicore/internal/jobsvc"
	"ocicore/internal/netsvc"
	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

// LaunchPreset 是创建实例向导里的快捷预设。
//
// 95% 的用户就是冲着免费额度来的，把这两档做成一键预设，
// 比让他们在几十个规格里翻找有用得多。
type LaunchPreset struct {
	Key         string  `json:"key"`
	Label       string  `json:"label"`
	Shape       string  `json:"shape"`
	Ocpus       float32 `json:"ocpus"`
	MemoryInGBs float32 `json:"memoryInGbs"`
	BootGB      int64   `json:"bootGb"`
	Description string  `json:"description"`
	FreeTier    bool    `json:"freeTier"`
}

func launchPresets() []LaunchPreset {
	return []LaunchPreset{
		{
			Key: "arm-free", Label: i18n.M("api.preset.arm_full"),
			Shape: "VM.Standard.A1.Flex",
			Ocpus: ociclient.AlwaysFreeARMOcpus, MemoryInGBs: ociclient.AlwaysFreeARMMemoryGB,
			BootGB:      50,
			Description: i18n.M("api.preset.arm_full.desc"),
			FreeTier:    true,
		},
		{
			Key: "arm-half", Label: i18n.M("api.preset.arm_half"),
			Shape: "VM.Standard.A1.Flex",
			Ocpus: ociclient.AlwaysFreeARMOcpus / 2, MemoryInGBs: ociclient.AlwaysFreeARMMemoryGB / 2,
			BootGB:      50,
			Description: i18n.M("api.preset.arm_half.desc"),
			FreeTier:    true,
		},
		{
			// 这一档对永久免费号是陷阱：照它开出来的实例会被 Oracle 回收。
			// 所以 FreeTier 标 false，标题里写明适用对象，别让人顺手点。
			Key: "arm-legacy", Label: i18n.M("api.preset.arm_legacy"),
			Shape: "VM.Standard.A1.Flex",
			Ocpus: ociclient.LegacyFreeARMOcpus, MemoryInGBs: ociclient.LegacyFreeARMMemoryGB,
			BootGB:      50,
			Description: i18n.M("api.preset.arm_legacy.desc"),
			FreeTier:    false,
		},
		{
			Key: "amd-free", Label: i18n.M("api.preset.amd"),
			Shape: "VM.Standard.E2.1.Micro", Ocpus: 1, MemoryInGBs: 1, BootGB: 50,
			Description: i18n.M("api.preset.amd.desc"), FreeTier: true,
		},
	}
}

func (s *Server) handleLaunchPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"presets": launchPresets()})
}

func (s *Server) handleListADs(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	ads, err := client.ListAvailabilityDomains(r.Context(), region, acc.CompartmentOCID)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"availabilityDomains": ads})
}

func (s *Server) handleListShapes(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	shapes, err := client.ListShapes(r.Context(), region, acc.CompartmentOCID,
		r.URL.Query().Get("availabilityDomain"))
	if err != nil {
		writeOCIError(w, err)
		return
	}

	// 同一个规格在多个可用域下会重复出现，按名字去重。
	seen := make(map[string]struct{}, len(shapes))
	unique := make([]ociclient.Shape, 0, len(shapes))
	for _, sh := range shapes {
		if _, dup := seen[sh.Shape]; dup {
			continue
		}
		seen[sh.Shape] = struct{}{}
		unique = append(unique, sh)
	}
	writeJSON(w, http.StatusOK, map[string]any{"shapes": unique})
}

func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	client, acc, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()

	// shape 过滤很关键：ARM 与 x86 的镜像不通用，
	// 不加过滤用户会选到一个根本起不来的镜像。
	images, err := client.ListImages(r.Context(), ociclient.ListImagesOptions{
		CompartmentID:          acc.CompartmentOCID,
		Region:                 region,
		Shape:                  q.Get("shape"),
		OperatingSystem:        q.Get("os"),
		OperatingSystemVersion: q.Get("osVersion"),
	})
	if err != nil {
		writeOCIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": images})
}

type launchRequest struct {
	AccountID          string  `json:"accountId"`
	Region             string  `json:"region"`
	AvailabilityDomain string  `json:"availabilityDomain"`
	DisplayName        string  `json:"displayName"`
	Shape              string  `json:"shape"`
	Ocpus              float32 `json:"ocpus"`
	MemoryInGBs        float32 `json:"memoryInGbs"`
	// BaselineOcpuUtilization 为空表示不突发。
	BaselineOcpuUtilization string `json:"baselineOcpuUtilization"`
	ImageID                 string `json:"imageId"`
	BootVolumeGB            int64  `json:"bootVolumeGb"`

	SubnetID          string `json:"subnetId"`
	AutoCreateNetwork bool   `json:"autoCreateNetwork"`
	AssignPublicIP    *bool  `json:"assignPublicIp"`
	EnableIPv6        bool   `json:"enableIpv6"`

	SSHPublicKey string `json:"sshPublicKey"`
	CloudInit    string `json:"cloudInit"`
}

// launchResult 是创建任务的结构化结果。
type launchResult struct {
	InstanceID string `json:"instanceId"`
	PublicIP   string `json:"publicIp,omitempty"`
}

// handleLaunchInstance 创建实例。
//
// 请求里只做必填项与凭据校验，随后交给后台任务：选可用域 → 校验规格 →
// 配额预检 → 准备网络 → LaunchInstance → 写入缓存 → 等到 RUNNING。
// 整个流程要几分钟，以前请求一直挂着，现在立刻返回 202 与任务快照。
func (s *Server) handleLaunchInstance(w http.ResponseWriter, r *http.Request) {
	var req launchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.AccountID == "" || req.Shape == "" || req.ImageID == "" {
		writeError(w, http.StatusBadRequest, "missing_fields",
			i18n.M("api.need_account_shape_image"))
		return
	}
	if strings.TrimSpace(req.DisplayName) == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", i18n.M("api.name_required"))
		return
	}
	if req.SubnetID == "" && !req.AutoCreateNetwork {
		writeError(w, http.StatusBadRequest, "missing_subnet",
			i18n.M("api.launch_need_subnet"))
		return
	}

	client, acc, err := s.conns.ForID(r.Context(), req.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}

	region := ociclient.NormalizeRegion(req.Region)
	if region == "" {
		region = acc.DefaultRegion
	}
	who := s.actorOf(r)

	s.submitJob(w, jobsvc.Spec{
		Kind: jobLaunch, Title: i18n.M("api.launch_title", strings.TrimSpace(req.DisplayName)), AccountID: acc.ID,
		// 创建、建网、等就绪加起来，容量紧张时十分钟也不算离谱。
		Timeout: 20 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		return s.runLaunch(ctx, p, client, acc, region, req, who)
	})
}

func (s *Server) runLaunch(ctx context.Context, p *jobsvc.Progress, client *ociclient.Client,
	acc *store.Account, region string, req launchRequest, who actor) (jobsvc.Outcome, error) {

	p.Step(i18n.M("api.step.launch_target", acc.Alias, region, req.Shape))

	ad := req.AvailabilityDomain
	if ad == "" {
		ads, err := client.ListAvailabilityDomains(ctx, region, acc.CompartmentOCID)
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		if len(ads) == 0 {
			return jobsvc.Outcome{}, i18n.Err("api.no_ads")
		}
		ad = ads[0].Name
		p.Step(i18n.M("api.step.ad_picked", ad))
	}
	p.Set(5)

	// 规格校验：拿真实的规格元数据来校验 OCPU/内存搭配，
	// 比硬编码 A1.Flex 的规则可靠——Oracle 会调整免费额度上限。
	shapes, err := client.ListShapes(ctx, region, acc.CompartmentOCID, ad)
	if err != nil {
		return jobsvc.Outcome{}, err
	}
	var target *ociclient.Shape
	for i := range shapes {
		if shapes[i].Shape == req.Shape {
			target = &shapes[i]
			break
		}
	}
	if target == nil {
		return jobsvc.Outcome{}, i18n.Err("api.shape_not_in_ad", req.Shape)
	}

	var shapeConfig *ociclient.ShapeConfig
	if target.IsFlexible {
		cfg := ociclient.ShapeConfig{Ocpus: req.Ocpus, MemoryInGBs: req.MemoryInGBs,
			BaselineOcpuUtilization: req.BaselineOcpuUtilization}
		if err := ociclient.ValidateShapeConfig(target, cfg); err != nil {
			return jobsvc.Outcome{}, err
		}
		shapeConfig = &cfg
	}
	p.Step(i18n.M("api.step.shape_ok"))
	p.Set(10)

	// 配额预检：配额已满的话 LaunchInstance 必定失败，
	// 提前查一次能把"重试无意义"这件事说清楚。
	if notice := s.checkLaunchQuota(ctx, client, acc, region, req.Shape, req.Ocpus); notice != "" {
		return jobsvc.Outcome{}, i18n.Err("api.quota_free_hint", notice)
	}
	p.Set(15)

	subnetID := req.SubnetID
	if subnetID == "" {
		p.Step(i18n.M("api.step.prepare_net"))
		netResult, err := netsvc.EnsureNetwork(ctx, client, netsvc.EnsureNetworkOptions{
			Region:        region,
			CompartmentID: acc.CompartmentOCID,
			EnableIPv6:    req.EnableIPv6,
		})
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		subnetID = netResult.SubnetID
		for _, step := range netResult.Steps {
			p.Step(step)
		}
	}
	p.Set(30)

	// 无论子网是手选的、复用的还是新建的，要 IPv6 就得先确认它真能分配 IPv6。
	// 开通不了就到此为止：带着 assignIpv6Ip 去创建只会换来 Oracle 一句
	// "Subnet must support ipv6"，用户看不出缺的是什么。
	var ipv6Warnings []string
	if req.EnableIPv6 {
		prep, err := netsvc.PrepareIPv6(ctx, client, region, subnetID)
		if err != nil {
			return jobsvc.Outcome{}, i18n.Err("api.launch_v6_failed", ipv6ErrText(err))
		}
		for _, step := range prep.Steps {
			p.Step(step)
		}
		ipv6Warnings = prep.Warnings
	}

	assignPublicIP := true
	if req.AssignPublicIP != nil {
		assignPublicIP = *req.AssignPublicIP
	}

	launchReq := ociclient.LaunchInstanceRequest{
		CompartmentID:      acc.CompartmentOCID,
		AvailabilityDomain: ad,
		DisplayName:        strings.TrimSpace(req.DisplayName),
		Shape:              req.Shape,
		ShapeConfig:        shapeConfig,
		SourceDetails: &ociclient.SourceDetails{
			SourceType: "image",
			ImageID:    req.ImageID,
		},
		CreateVnicDetails: &ociclient.CreateVnicDetails{
			SubnetID:       subnetID,
			AssignPublicIP: &assignPublicIP,
		},
		Metadata:     buildMetadata(req.SSHPublicKey, req.CloudInit),
		FreeformTags: map[string]string{"created-by": "ocicore"},
	}
	if req.BootVolumeGB > 0 {
		launchReq.SourceDetails.BootVolumeSizeInGBs = req.BootVolumeGB
	}
	if req.EnableIPv6 {
		yes := true
		launchReq.CreateVnicDetails.AssignIpv6IP = &yes
	}

	created, err := client.LaunchInstance(ctx, region, launchReq)
	if err != nil {
		return jobsvc.Outcome{}, err
	}
	p.SetInstance(created.ID)
	p.Step(i18n.M("api.step.launch_submitted"))
	p.Set(40)

	// 立刻写入缓存，让列表马上出现这一行（PROVISIONING 状态），
	// 而不是等下一轮同步。落定由后台轮询推送。
	row := store.Instance{
		ID:                 created.ID,
		AccountID:          acc.ID,
		Region:             region,
		CompartmentID:      created.CompartmentID,
		DisplayName:        created.DisplayName,
		AvailabilityDomain: created.AvailabilityDomain,
		FaultDomain:        created.FaultDomain,
		Shape:              created.Shape,
		LifecycleState:     created.LifecycleState,
		ImageID:            req.ImageID,
		SubnetID:           subnetID,
		TimeCreated:        created.TimeCreated,
	}
	// 「实例已就绪」的提示里要给出 SSH 命令，用户名取决于镜像系统。
	if s.instances != nil {
		row.ImageOS = s.instances.ImageOS(ctx, client, region, req.ImageID)
	}
	if created.ShapeConfig != nil {
		row.Ocpus = float64(created.ShapeConfig.Ocpus)
		row.MemoryGB = float64(created.ShapeConfig.MemoryInGBs)
		row.BaselineOcpu = created.ShapeConfig.BaselineOcpuUtilization
	}
	if err := s.st.UpsertInstance(ctx, row); err != nil {
		return jobsvc.Outcome{}, err
	}
	s.instances.Bus().Publish(instanceUpdatedEvent(&row))

	s.audit(who, store.AuditEntry{
		Action: "instance_launch", AccountID: acc.ID, Target: created.DisplayName,
		Detail: req.Shape + " · " + region + " · " + ad,
	})

	for _, warn := range ipv6Warnings {
		p.Step("⚠ IPv6：" + warn)
	}
	p.Step(i18n.M("api.step.launch_wait"))

	result := launchResult{InstanceID: created.ID}
	inst, err := s.waitInstanceSettled(ctx, p, created.ID, 40, 99, 0)
	if err != nil {
		return jobsvc.Outcome{Result: result}, err
	}
	if inst == nil || inst.LifecycleState != ociclient.LifecycleRunning {
		state := ociclient.LifecycleTerminated
		if inst != nil {
			state = inst.LifecycleState
		}
		return jobsvc.Outcome{Result: result},
			i18n.Err("api.launch_not_running", state)
	}

	result.PublicIP = inst.PublicIP
	summary := i18n.M("api.launch_ready", inst.DisplayName)
	if inst.PublicIP != "" {
		summary += i18n.M("api.launch_ready_ip", inst.PublicIP)
	}
	return jobsvc.Outcome{Summary: summary, Result: result}, nil
}

// ipv6ErrText 保留出错的是哪一步，同时把 OCI 错误压成「代码 · 说明」。
func ipv6ErrText(err error) string {
	text := err.Error()
	if apiErr, ok := ociclient.AsAPIError(err); ok {
		text = strings.Replace(text, apiErr.Error(), shortOCIError(apiErr), 1)
	}
	return text
}

// checkLaunchQuota 在创建前检查配额，返回非空字符串表示配额不足。
//
// 只对免费额度的 ARM 规格做检查：那是唯一会被频繁撞上限的资源，
// 其他规格查一遍纯属浪费 API 调用。
func (s *Server) checkLaunchQuota(ctx context.Context, client *ociclient.Client,
	acc *store.Account, region, shape string, ocpus float32) string {

	if !strings.Contains(shape, "A1.Flex") {
		return ""
	}
	avail, err := client.GetResourceAvailability(ctx, region, acc.CompartmentOCID,
		ociclient.LimitServiceCompute, ociclient.LimitARMCores, "")
	if err != nil || avail == nil || avail.Available == nil {
		// 查不到配额不该阻断创建——权限不足或接口异常都算不上"配额已满"。
		return ""
	}
	if float32(*avail.Available) < ocpus {
		return i18n.M("api.arm_quota_short", int(*avail.Available), trimFloat(ocpus))
	}
	return ""
}

// buildMetadata 组装 cloud-init 元数据。
func buildMetadata(sshKey, cloudInit string) map[string]string {
	meta := map[string]string{}
	if key := strings.TrimSpace(sshKey); key != "" {
		meta["ssh_authorized_keys"] = key
	}
	if ci := strings.TrimSpace(cloudInit); ci != "" {
		// OCI 要求 user_data 是 base64 编码的。
		meta["user_data"] = base64.StdEncoding.EncodeToString([]byte(ci))
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

// trimFloat 把 4.0 这样的值格式化成 "4"，避免界面上出现多余的小数点。
func trimFloat(v float32) string {
	whole := int(v)
	if float32(whole) == v {
		return itoa(whole)
	}
	// 只保留一位小数即可，OCPU 不会有更细的粒度。
	tenths := int(v*10) % 10
	return itoa(whole) + "." + itoa(tenths)
}
