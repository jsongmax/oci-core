package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

// resourcesTTL 是账号资源汇总的缓存时长。
//
// 一次汇总在每个区域要发十来个调用（每个可用域还要分别查引导卷与临时 IP），
// 多区域账号轻松上百次。资源页是「盘点」用的，两分钟的滞后完全可以接受；
// 用户刚删完东西想看结果时，页面上的「刷新」会带 refresh=true 绕过缓存。
const resourcesTTL = 2 * time.Minute

// ResourceVolume 是一块引导卷或块存储卷，带上它挂在哪台实例上。
type ResourceVolume struct {
	ID                 string    `json:"id"`
	Kind               string    `json:"kind"` // boot / block
	DisplayName        string    `json:"displayName"`
	Region             string    `json:"region"`
	AvailabilityDomain string    `json:"availabilityDomain"`
	SizeInGBs          int64     `json:"sizeInGbs"`
	VpusPerGB          int64     `json:"vpusPerGb"`
	LifecycleState     string    `json:"lifecycleState"`
	TimeCreated        time.Time `json:"timeCreated"`
	// AttachedTo 是挂载的实例；空表示未挂载。块存储可以多挂（共享卷），所以是列表。
	AttachedTo []ResourceRef `json:"attachedTo"`
}

// ResourceRef 指向一台实例。Name 来自实例缓存，缓存里没有（例如别的工具刚建的）时为空。
type ResourceRef struct {
	InstanceID string `json:"instanceId"`
	Name       string `json:"name"`
}

// ResourcePublicIP 是一个公网 IP。
type ResourcePublicIP struct {
	ID             string `json:"id"`
	IPAddress      string `json:"ipAddress"`
	Region         string `json:"region"`
	Lifetime       string `json:"lifetime"` // EPHEMERAL / RESERVED
	LifecycleState string `json:"lifecycleState"`
	DisplayName    string `json:"displayName"`
	// Assigned 表示绑在了某个私网 IP 上。保留 IP 未绑定时仍然存在、仍占配额。
	Assigned bool `json:"assigned"`
	// Instance 按 IP 地址对照实例缓存得出；对照不上时为空。
	Instance *ResourceRef `json:"instance,omitempty"`
}

// ResourceConsole 是一条串行控制台连接。
type ResourceConsole struct {
	ID             string      `json:"id"`
	Region         string      `json:"region"`
	LifecycleState string      `json:"lifecycleState"`
	Fingerprint    string      `json:"fingerprint"`
	Instance       ResourceRef `json:"instance"`
}

// ResourceVcn 是一个 VCN 及其出网条件。
type ResourceVcn struct {
	ID             string   `json:"id"`
	DisplayName    string   `json:"displayName"`
	Region         string   `json:"region"`
	CidrBlocks     []string `json:"cidrBlocks"`
	Ipv6CidrBlocks []string `json:"ipv6CidrBlocks"`
	LifecycleState string   `json:"lifecycleState"`
	Subnets        int      `json:"subnets"`
	// InternetGateway 为 nil 表示 VCN 里没有互联网网关——实例连不上外网时最先该看的就是这个。
	InternetGateway *ResourceGateway `json:"internetGateway"`
}

// ResourceGateway 是互联网网关的状态。
type ResourceGateway struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsEnabled bool   `json:"isEnabled"`
}

// ResourceInstance 是实例缓存里的一行，资源页只需要这几个字段。
type ResourceInstance struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Region         string  `json:"region"`
	Shape          string  `json:"shape"`
	LifecycleState string  `json:"lifecycleState"`
	Ocpus          float64 `json:"ocpus"`
	MemoryGB       float64 `json:"memoryGb"`
	BootVolumeGB   int64   `json:"bootVolumeGb"`
}

// ResourceUsage 是本工具按资源清单自己算出的用量，用来对照永久免费的参考额度。
//
// 与 Quota 不是一回事：Quota 是 Oracle 给这个账号的真实限额（只查主区域），
// 升级号的限额往往远高于免费额度。超出免费额度不会被拦，但会开始计费——
// 这正是资源页要提醒的。
type ResourceUsage struct {
	ArmOcpus       float64 `json:"armOcpus"`
	ArmMemoryGB    float64 `json:"armMemoryGb"`
	MicroInstances int     `json:"microInstances"`
	// StorageGB 是引导卷与块存储的合计。永久免费的 200 GB 是两者共用的。
	StorageGB int64 `json:"storageGb"`
}

// ResourceFreeTier 是永久免费的参考额度（ociclient/freetier.go），带核对日期。
//
// 按账号类型取值：升级号（PAY_AS_YOU_GO）沿用 2026-06-15 之前的 ARM 额度
// 4 OCPU / 24 GB，免费号是 2 OCPU / 12 GB。拿免费号的数字去对照升级号，
// 会把一台完全不收费的 4 核 24 GB 报成「超出 2 核」。
type ResourceFreeTier struct {
	// Tier 是 free / paid / unknown。unknown 表示还没识别出账号类型
	// （没跑过连通性校验，或缺 organizations 读权限），按更紧的免费号额度对照。
	Tier           string `json:"tier"`
	ArmOcpus       int    `json:"armOcpus"`
	ArmMemoryGB    int    `json:"armMemoryGb"`
	MicroInstances int    `json:"microInstances"`
	StorageGB      int    `json:"storageGb"`
	CheckedAt      string `json:"checkedAt"`
}

// ResourceError 记录某个区域某一类资源没查到的原因。
// 局部降级：一类查不到不该让整页空白。
type ResourceError struct {
	Region string `json:"region"`
	Part   string `json:"part"`
	Error  string `json:"error"`
}

// AccountResources 是资源页的全部数据。
type AccountResources struct {
	AccountID          string             `json:"accountId"`
	Regions            []string           `json:"regions"`
	Instances          []ResourceInstance `json:"instances"`
	Volumes            []ResourceVolume   `json:"volumes"`
	PublicIPs          []ResourcePublicIP `json:"publicIps"`
	ConsoleConnections []ResourceConsole  `json:"consoleConnections"`
	Vcns               []ResourceVcn      `json:"vcns"`
	Usage              ResourceUsage      `json:"usage"`
	FreeTier           ResourceFreeTier   `json:"freeTier"`
	Quota              AccountQuota       `json:"quota"`
	Errors             []ResourceError    `json:"errors,omitempty"`
	FetchedAt          time.Time          `json:"fetchedAt"`
}

type resourcesCache struct {
	mu      sync.Mutex
	entries map[string]*AccountResources
}

func newResourcesCache() *resourcesCache {
	return &resourcesCache{entries: make(map[string]*AccountResources)}
}

func (c *resourcesCache) get(key string) (*AccountResources, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Since(e.FetchedAt) > resourcesTTL {
		return nil, false
	}
	return e, true
}

// invalidate 丢掉一个账号的全部缓存（各种区域组合都算）。清理操作成功后调用。
func (c *resourcesCache) invalidate(accountID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if strings.HasPrefix(k, accountID+"|") {
			delete(c.entries, k)
		}
	}
}

func (c *resourcesCache) put(key string, v *AccountResources) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = v
}

// handleAccountResources 汇总一个账号在各区域的资源。
//
// 查询参数：region 只看一个区域（默认全部已订阅区域），refresh=true 绕过缓存。
func (s *Server) handleAccountResources(w http.ResponseWriter, r *http.Request) {
	acc, err := s.st.GetAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !acc.Enabled {
		writeError(w, http.StatusBadRequest, "account_disabled", "账号已禁用")
		return
	}

	regions := acc.SubscribedRegions
	if len(regions) == 0 && acc.DefaultRegion != "" {
		regions = []string{acc.DefaultRegion}
	}
	if only := ociclient.NormalizeRegion(r.URL.Query().Get("region")); only != "" {
		regions = []string{only}
	}
	refresh := r.URL.Query().Get("refresh") == "true"

	key := acc.ID + "|" + strings.Join(regions, ",")
	if !refresh {
		if cached, ok := s.resources.get(key); ok {
			writeJSON(w, http.StatusOK, cached)
			return
		}
	}

	client, err := s.conns.For(r.Context(), acc)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	insts, err := s.st.ListInstances(ctx, store.InstanceFilter{AccountIDs: []string{acc.ID}})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	out := s.collectResources(ctx, client, acc, regions, insts, refresh)
	s.resources.put(key, out)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) collectResources(ctx context.Context, client *ociclient.Client, acc *store.Account,
	regions []string, insts []store.Instance, refreshQuota bool) *AccountResources {

	out := &AccountResources{
		AccountID: acc.ID, Regions: regions, FetchedAt: time.Now(),
		Instances: make([]ResourceInstance, 0), Volumes: make([]ResourceVolume, 0),
		PublicIPs: make([]ResourcePublicIP, 0), ConsoleConnections: make([]ResourceConsole, 0),
		Vcns:     make([]ResourceVcn, 0),
		FreeTier: freeTierFor(acc.PaymentModel),
	}

	wanted := make(map[string]bool, len(regions))
	for _, r := range regions {
		wanted[r] = true
	}
	names := make(map[string]string, len(insts))
	byIP := make(map[string]ResourceRef)
	for _, in := range insts {
		names[in.ID] = in.DisplayName
		ref := ResourceRef{InstanceID: in.ID, Name: in.DisplayName}
		for _, ip := range append([]string{in.PublicIP}, splitCSV(in.SecondaryIPv4)...) {
			if ip != "" {
				byIP[ip] = ref
			}
		}
		if !wanted[in.Region] || in.LifecycleState == ociclient.LifecycleTerminated {
			continue
		}
		out.Instances = append(out.Instances, ResourceInstance{
			ID: in.ID, Name: in.DisplayName, Region: in.Region, Shape: in.Shape,
			LifecycleState: in.LifecycleState, Ocpus: in.Ocpus, MemoryGB: in.MemoryGB,
			BootVolumeGB: in.BootVolumeGB,
		})
		// 停机的实例照样占额度：OCPU 与内存在 Oracle 那边按「已分配」算，不按「在跑」算。
		switch {
		case strings.Contains(in.Shape, ".A1."):
			out.Usage.ArmOcpus += in.Ocpus
			out.Usage.ArmMemoryGB += in.MemoryGB
		case in.Shape == "VM.Standard.E2.1.Micro":
			out.Usage.MicroInstances++
		}
	}
	ref := func(id string) ResourceRef { return ResourceRef{InstanceID: id, Name: names[id]} }

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	fail := func(region, part string, err error) {
		mu.Lock()
		out.Errors = append(out.Errors, ResourceError{Region: region, Part: part, Error: shortOCIError(err)})
		mu.Unlock()
	}

	// 配额只查主区域，与账号页的配额面板共用缓存。
	wg.Add(1)
	go func() {
		defer wg.Done()
		q := s.accountQuota(ctx, acc, refreshQuota)
		mu.Lock()
		out.Quota = q
		mu.Unlock()
	}()

	sem := make(chan struct{}, 3) // 区域间并发：再多就容易撞上 Oracle 的限流
	for _, region := range regions {
		wg.Add(1)
		go func(region string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rr := collectRegion(ctx, client, acc.CompartmentOCID, region, ref, byIP, fail)
			mu.Lock()
			out.Volumes = append(out.Volumes, rr.volumes...)
			out.PublicIPs = append(out.PublicIPs, rr.ips...)
			out.ConsoleConnections = append(out.ConsoleConnections, rr.consoles...)
			out.Vcns = append(out.Vcns, rr.vcns...)
			mu.Unlock()
		}(region)
	}
	wg.Wait()

	for _, v := range out.Volumes {
		if v.LifecycleState != ociclient.VolumeTerminated && v.LifecycleState != ociclient.VolumeTerminating {
			out.Usage.StorageGB += v.SizeInGBs
		}
	}

	// 输出顺序固定：并发收集的结果顺序是随机的，每次刷新都乱跳会很难对照。
	sort.Slice(out.Volumes, func(i, j int) bool {
		a, b := out.Volumes[i], out.Volumes[j]
		if a.Region != b.Region {
			return a.Region < b.Region
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.DisplayName < b.DisplayName
	})
	sort.Slice(out.PublicIPs, func(i, j int) bool {
		a, b := out.PublicIPs[i], out.PublicIPs[j]
		if a.Region != b.Region {
			return a.Region < b.Region
		}
		return a.IPAddress < b.IPAddress
	})
	sort.Slice(out.ConsoleConnections, func(i, j int) bool {
		return out.ConsoleConnections[i].Instance.Name < out.ConsoleConnections[j].Instance.Name
	})
	sort.Slice(out.Vcns, func(i, j int) bool {
		a, b := out.Vcns[i], out.Vcns[j]
		if a.Region != b.Region {
			return a.Region < b.Region
		}
		return a.DisplayName < b.DisplayName
	})
	sort.Slice(out.Errors, func(i, j int) bool {
		return out.Errors[i].Region+out.Errors[i].Part < out.Errors[j].Region+out.Errors[j].Part
	})
	return out
}

type regionResources struct {
	volumes  []ResourceVolume
	ips      []ResourcePublicIP
	consoles []ResourceConsole
	vcns     []ResourceVcn
}

// collectRegion 查一个区域的卷、公网 IP、控制台连接与 VCN。
//
// 引导卷、引导卷挂载关系、临时 IP 都是可用域级别的，要逐个 AD 查；
// 其余是区域级别的，一次就够。各类之间互不依赖，全部并发。
func collectRegion(ctx context.Context, client *ociclient.Client, compartment, region string,
	ref func(string) ResourceRef, byIP map[string]ResourceRef, fail func(region, part string, err error)) regionResources {

	var (
		mu sync.Mutex
		wg sync.WaitGroup

		boots       []ociclient.BootVolume
		bootAtts    []ociclient.BootVolumeAttachment
		blocks      []ociclient.Volume
		blockAtts   []ociclient.VolumeAttachment
		ips         []ociclient.PublicIP
		consoles    []ociclient.InstanceConsoleConnection
		vcns        []ociclient.Vcn
		subnets     []ociclient.Subnet
		gateways    []ociclient.InternetGateway
		vcnFailed   bool
		bootsFailed bool
	)
	run := func(part string, f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f(); err != nil {
				fail(region, part, err)
			}
		}()
	}

	ads, err := client.ListAvailabilityDomains(ctx, region, "")
	if err != nil {
		fail(region, "可用域", err)
	}
	for _, ad := range ads {
		run("引导卷", func() error {
			v, err := client.ListBootVolumes(ctx, region, compartment, ad.Name)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				bootsFailed = true
				return err
			}
			boots = append(boots, v...)
			return nil
		})
		run("引导卷挂载", func() error {
			v, err := client.ListBootVolumeAttachments(ctx, region, compartment, ad.Name, "")
			if err != nil {
				return err
			}
			mu.Lock()
			bootAtts = append(bootAtts, v...)
			mu.Unlock()
			return nil
		})
		run("临时公网 IP", func() error {
			v, err := client.ListPublicIPs(ctx, region, compartment, "AVAILABILITY_DOMAIN", ad.Name)
			if err != nil {
				return err
			}
			mu.Lock()
			ips = append(ips, v...)
			mu.Unlock()
			return nil
		})
	}
	run("块存储", func() error {
		v, err := client.ListVolumes(ctx, region, compartment)
		mu.Lock()
		blocks = v
		mu.Unlock()
		return err
	})
	run("块存储挂载", func() error {
		v, err := client.ListVolumeAttachments(ctx, region, compartment, "")
		mu.Lock()
		blockAtts = v
		mu.Unlock()
		return err
	})
	run("保留公网 IP", func() error {
		v, err := client.ListPublicIPs(ctx, region, compartment, "REGION", "")
		if err != nil {
			return err
		}
		mu.Lock()
		ips = append(ips, v...)
		mu.Unlock()
		return nil
	})
	run("控制台连接", func() error {
		v, err := client.ListConsoleConnections(ctx, region, compartment, "")
		mu.Lock()
		consoles = v
		mu.Unlock()
		return err
	})
	run("VCN", func() error {
		v, err := client.ListVcns(ctx, region, compartment)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			vcnFailed = true
			return err
		}
		vcns = v
		return nil
	})
	run("子网", func() error {
		v, err := client.ListSubnets(ctx, region, compartment, "")
		mu.Lock()
		subnets = v
		mu.Unlock()
		return err
	})
	run("互联网网关", func() error {
		v, err := client.ListInternetGateways(ctx, region, compartment, "")
		mu.Lock()
		gateways = v
		mu.Unlock()
		return err
	})
	wg.Wait()

	var out regionResources

	// 挂载关系只看 ATTACHED / ATTACHING：分离过的历史关系也会被列出来，
	// 算进去的话一块早就闲置的卷会显示成「挂着」。
	attached := func(state string) bool { return state == "ATTACHED" || state == "ATTACHING" }
	bootTo := map[string][]ResourceRef{}
	for _, a := range bootAtts {
		if attached(a.LifecycleState) {
			bootTo[a.BootVolumeID] = append(bootTo[a.BootVolumeID], ref(a.InstanceID))
		}
	}
	blockTo := map[string][]ResourceRef{}
	for _, a := range blockAtts {
		if attached(a.LifecycleState) {
			blockTo[a.VolumeID] = append(blockTo[a.VolumeID], ref(a.InstanceID))
		}
	}
	// 正在删除 / 释放的条目不列出：它们几分钟内就会消失，留着的话「待处理」里
	// 仍带着删除按钮，用户以为没删掉又点一次（控制台连接先前就要删两次）。
	gone := func(state string) bool {
		return state == ociclient.VolumeTerminated || state == ociclient.VolumeTerminating
	}
	if !bootsFailed {
		for _, b := range boots {
			if gone(b.LifecycleState) {
				continue
			}
			out.volumes = append(out.volumes, ResourceVolume{
				ID: b.ID, Kind: "boot", DisplayName: b.DisplayName, Region: region,
				AvailabilityDomain: b.AvailabilityDomain, SizeInGBs: b.SizeInGBs, VpusPerGB: b.VpusPerGB,
				LifecycleState: b.LifecycleState, TimeCreated: b.TimeCreated,
				AttachedTo: nonNil(bootTo[b.ID]),
			})
		}
	}
	for _, b := range blocks {
		if gone(b.LifecycleState) {
			continue
		}
		out.volumes = append(out.volumes, ResourceVolume{
			ID: b.ID, Kind: "block", DisplayName: b.DisplayName, Region: region,
			AvailabilityDomain: b.AvailabilityDomain, SizeInGBs: b.SizeInGBs, VpusPerGB: b.VpusPerGB,
			LifecycleState: b.LifecycleState, TimeCreated: b.TimeCreated,
			AttachedTo: nonNil(blockTo[b.ID]),
		})
	}

	seen := map[string]bool{}
	for _, ip := range ips {
		if seen[ip.ID] || ip.LifecycleState == "TERMINATED" || ip.LifecycleState == "TERMINATING" {
			continue
		}
		seen[ip.ID] = true
		item := ResourcePublicIP{
			ID: ip.ID, IPAddress: ip.IPAddress, Region: region, Lifetime: ip.Lifetime,
			LifecycleState: ip.LifecycleState, DisplayName: ip.DisplayName,
			Assigned: ip.PrivateIPID != "" || ip.AssignedEntityID != "",
		}
		if r, ok := byIP[ip.IPAddress]; ok {
			r := r
			item.Instance = &r
		}
		out.ips = append(out.ips, item)
	}

	for _, c := range consoles {
		if c.LifecycleState == "DELETED" || c.LifecycleState == "DELETING" {
			continue
		}
		out.consoles = append(out.consoles, ResourceConsole{
			ID: c.ID, Region: region, LifecycleState: c.LifecycleState,
			Fingerprint: c.Fingerprint, Instance: ref(c.InstanceID),
		})
	}

	if !vcnFailed {
		subnetCount := map[string]int{}
		for _, sn := range subnets {
			subnetCount[sn.VcnID]++
		}
		gw := map[string]*ResourceGateway{}
		for _, g := range gateways {
			if g.LifecycleState == "TERMINATED" {
				continue
			}
			gw[g.VcnID] = &ResourceGateway{ID: g.ID, Name: g.DisplayName, IsEnabled: g.IsEnabled}
		}
		for _, v := range vcns {
			if v.LifecycleState == "TERMINATED" {
				continue
			}
			cidrs := v.CidrBlocks
			if len(cidrs) == 0 && v.CidrBlock != "" {
				cidrs = []string{v.CidrBlock}
			}
			out.vcns = append(out.vcns, ResourceVcn{
				ID: v.ID, DisplayName: v.DisplayName, Region: region,
				CidrBlocks: nonNilStrings(cidrs), Ipv6CidrBlocks: nonNilStrings(v.Ipv6CidrBlocks),
				LifecycleState: v.LifecycleState, Subnets: subnetCount[v.ID], InternetGateway: gw[v.ID],
			})
		}
	}
	return out
}

// freeTierFor 按账号的付费模式给出参考额度。
func freeTierFor(paymentModel string) ResourceFreeTier {
	ft := ResourceFreeTier{
		Tier:     "unknown",
		ArmOcpus: ociclient.AlwaysFreeARMOcpus, ArmMemoryGB: ociclient.AlwaysFreeARMMemoryGB,
		MicroInstances: ociclient.AlwaysFreeMicroInstances, StorageGB: ociclient.AlwaysFreeBlockGB,
		CheckedAt: ociclient.AlwaysFreeCheckedAt,
	}
	switch paymentModel {
	case "":
	case ociclient.PaymentFreeTrial:
		ft.Tier = "free"
	default:
		ft.Tier = "paid"
		ft.ArmOcpus, ft.ArmMemoryGB = ociclient.LegacyFreeARMOcpus, ociclient.LegacyFreeARMMemoryGB
	}
	return ft
}

func nonNil(refs []ResourceRef) []ResourceRef {
	if refs == nil {
		return []ResourceRef{}
	}
	return refs
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
