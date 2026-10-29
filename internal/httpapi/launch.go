package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

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
			Key: "arm-free", Label: "免费额度 ARM（满配）",
			Shape: "VM.Standard.A1.Flex",
			Ocpus: ociclient.AlwaysFreeARMOcpus, MemoryInGBs: ociclient.AlwaysFreeARMMemoryGB,
			BootGB:      50,
			Description: "Ampere A1，永久免费额度上限（2026-06-15 起为 2C12G）。容量紧张时较难开出。",
			FreeTier:    true,
		},
		{
			Key: "arm-half", Label: "免费额度 ARM（半配）",
			Shape: "VM.Standard.A1.Flex",
			Ocpus: ociclient.AlwaysFreeARMOcpus / 2, MemoryInGBs: ociclient.AlwaysFreeARMMemoryGB / 2,
			BootGB:      50,
			Description: "占用一半免费额度，可以开两台。",
			FreeTier:    true,
		},
		{
			// 这一档对永久免费号是陷阱：照它开出来的实例会被 Oracle 回收。
			// 所以 FreeTier 标 false，标题里写明适用对象，别让人顺手点。
			Key: "arm-legacy", Label: "ARM 4C24G（仅升级号）",
			Shape: "VM.Standard.A1.Flex",
			Ocpus: ociclient.LegacyFreeARMOcpus, MemoryInGBs: ociclient.LegacyFreeARMMemoryGB,
			BootGB: 50,
			Description: "2026-06-15 前的免费上限。升级号（PAYG）据报仍可免费使用；" +
				"永久免费号超出新限额的实例已从 2026-08-18 起被 Oracle 回收。",
			FreeTier: false,
		},
		{
			Key: "amd-free", Label: "免费额度 AMD",
			Shape: "VM.Standard.E2.1.Micro", Ocpus: 1, MemoryInGBs: 1, BootGB: 50,
			Description: "配置很低但基本随时能开，适合做跳板机。", FreeTier: true,
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
			"accountId、shape 与 imageId 为必填项")
		return
	}
	if strings.TrimSpace(req.DisplayName) == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "请填写实例名称")
		return
	}
	if req.SubnetID == "" && !req.AutoCreateNetwork {
		writeError(w, http.StatusBadRequest, "missing_subnet",
			"请选择子网，或勾选自动创建网络")
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
		Kind: jobLaunch, Title: "创建实例 " + strings.TrimSpace(req.DisplayName), AccountID: acc.ID,
		// 创建、建网、等就绪加起来，容量紧张时十分钟也不算离谱。
		Timeout: 20 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		return s.runLaunch(ctx, p, client, acc, region, req, who)
	})
}

func (s *Server) runLaunch(ctx context.Context, p *jobsvc.Progress, client *ociclient.Client,
	acc *store.Account, region string, req launchRequest, who actor) (jobsvc.Outcome, error) {

	p.Stepf("账号 %s · 区域 %s · 规格 %s", acc.Alias, region, req.Shape)

	ad := req.AvailabilityDomain
	if ad == "" {
		ads, err := client.ListAvailabilityDomains(ctx, region, acc.CompartmentOCID)
		if err != nil {
			return jobsvc.Outcome{}, err
		}
		if len(ads) == 0 {
			return jobsvc.Outcome{}, errors.New("该区域没有可用域")
		}
		ad = ads[0].Name
		p.Step("未指定可用域，已选用 " + ad)
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
		return jobsvc.Outcome{}, errors.New("该可用域不提供规格 " + req.Shape)
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
	p.Step("规格校验通过")
	p.Set(10)

	// 配额预检：配额已满的话 LaunchInstance 必定失败，
	// 提前查一次能把"重试无意义"这件事说清楚。
	if notice := s.checkLaunchQuota(ctx, client, acc, region, req.Shape, req.Ocpus); notice != "" {
		return jobsvc.Outcome{}, errors.New(notice + "请先释放已有资源，或在账号详情的配额页确认限额。")
	}
	p.Set(15)

	subnetID := req.SubnetID
	if subnetID == "" {
		p.Step("正在准备网络（VCN / 子网 / 网关 / 路由）")
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
			return jobsvc.Outcome{}, errors.New("没能为子网开通 IPv6，实例未创建：" + ipv6ErrText(err) +
				"。可以关闭 IPv6 后重试，或换一个子网。")
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
	p.Step("已提交创建请求，实例进入 PROVISIONING")
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
	p.Step("等待实例就绪，通常需要 1–3 分钟")

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
			errors.New("实例已提交创建，但没有进入 RUNNING，当前状态 " + state)
	}

	result.PublicIP = inst.PublicIP
	summary := "实例 " + inst.DisplayName + " 已就绪"
	if inst.PublicIP != "" {
		summary += "，公网 IP " + inst.PublicIP
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
		return "ARM 免费额度剩余 " + itoa(int(*avail.Available)) +
			" OCPU，不足以创建 " + trimFloat(ocpus) + " OCPU 的实例。"
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
