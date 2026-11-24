package instancesvc

import (
	"context"
	"log/slog"
	"net/netip"
	"sort"
	"sync"
	"time"

	"ocicore/internal/i18n"
	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

// detailTimeout 是拉取实例详情的整体上限。
const detailTimeout = 25 * time.Second

// VnicInfo 是详情页展示的一张网卡。
type VnicInfo struct {
	AttachmentID string `json:"attachmentId"`
	VnicID       string `json:"vnicId"`
	DisplayName  string `json:"displayName"`
	IsPrimary    bool   `json:"isPrimary"`
	NicIndex     int    `json:"nicIndex"`
	PrivateIP    string `json:"privateIp"`
	PublicIP     string `json:"publicIp"`
	PublicIPID   string `json:"publicIpId,omitempty"`
	PublicIPType string `json:"publicIpType,omitempty"`
	// PublicIPUnknown 为 true 表示查询该私网槽位的公网 IP 对象失败（非 404），
	// 前端据此禁用公网 IP 变更按钮，避免把"查不到"误当成"未分配"。
	PublicIPUnknown bool              `json:"publicIpUnknown,omitempty"`
	PrivateIPID     string            `json:"privateIpId,omitempty"`
	SecondaryIPs    []SecondaryIPInfo `json:"secondaryIps,omitempty"`
	IPv6            []string          `json:"ipv6"`
	// IPv6Info 与 IPv6 一一对应，多带创建时间。保留 IPv6 是为了不打断已有调用方。
	IPv6Info   []IPv6Entry `json:"ipv6Info"`
	MacAddress string      `json:"macAddress"`
	SubnetID   string      `json:"subnetId"`
	SubnetName string      `json:"subnetName,omitempty"`
	VcnID      string      `json:"vcnId,omitempty"`
	VcnName    string      `json:"vcnName,omitempty"`
}

// SecondaryIPInfo 是同一张网卡上的辅助私网 IP 及其绑定的公网 IP。
type SecondaryIPInfo struct {
	PrivateIPID     string `json:"privateIpId"`
	PrivateIP       string `json:"privateIp"`
	DisplayName     string `json:"displayName,omitempty"`
	PublicIPID      string `json:"publicIpId,omitempty"`
	PublicIP        string `json:"publicIp,omitempty"`
	PublicIPType    string `json:"publicIpType,omitempty"`
	PublicIPUnknown bool   `json:"publicIpUnknown,omitempty"`
}

// IPv6Entry 是网卡上的一个 IPv6 地址。
type IPv6Entry struct {
	Address string `json:"address"`
	// TimeCreated 取不到时为空，前端不显示。
	TimeCreated *time.Time `json:"timeCreated,omitempty"`
}

// VolumeInfo 是挂载在实例上的块存储卷。
type VolumeInfo struct {
	VolumeID     string `json:"volumeId"`
	AttachmentID string `json:"attachmentId"`
	DisplayName  string `json:"displayName"`
	SizeInGBs    int64  `json:"sizeInGbs"`
	VpusPerGB    int64  `json:"vpusPerGb"`
	Device       string `json:"device"`
	IsReadOnly   bool   `json:"isReadOnly"`
	State        string `json:"state"`
}

// Detail 是实例详情抽屉需要的全部数据。
type Detail struct {
	Instance   store.Instance        `json:"instance"`
	Vnics      []VnicInfo            `json:"vnics"`
	BootVolume *ociclient.BootVolume `json:"bootVolume"`
	// BootVolumeAttachmentID 是挂载关系的 OCID，不是卷的 OCID。
	// 分离引导卷（救援模式）要的是前者，两者混用会直接 404。
	BootVolumeAttachmentID string            `json:"bootVolumeAttachmentId"`
	BlockVolumes           []VolumeInfo      `json:"blockVolumes"`
	Metadata               map[string]string `json:"metadata"`
	// MaxVnics 是当前规格与配置允许的网卡数上限；0 表示读不到，前端不拦。
	MaxVnics int `json:"maxVnics"`
	// Warnings 记录部分数据拉取失败的原因。
	// 详情页允许"局部降级"——网络信息取不到不该让整个抽屉打不开。
	Warnings []string `json:"warnings,omitempty"`
}

// Detail 拉取实例的完整详情。
//
// 与列表同步不同，这里会额外查 IPv6 与块存储——详情页是低频操作，
// 多花几次 API 调用换取信息完整是划算的。
func (s *Service) Detail(ctx context.Context, instanceID string) (*Detail, error) {
	cached, err := s.st.GetInstance(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	client, acc, err := s.conns.ForID(ctx, cached.AccountID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, detailTimeout)
	defer cancel()

	detail := &Detail{Instance: *cached}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	warn := func(msg string) {
		mu.Lock()
		detail.Warnings = append(detail.Warnings, msg)
		mu.Unlock()
	}

	// 实例本体：拿到最新状态与 cloud-init 元数据。
	wg.Add(1)
	go func() {
		defer wg.Done()
		inst, err := client.GetInstance(ctx, cached.Region, instanceID)
		if err != nil {
			warn(i18n.M("inst.warn.read_instance", shortErr(err)))
			return
		}
		mu.Lock()
		detail.Instance.LifecycleState = inst.LifecycleState
		detail.Metadata = inst.Metadata
		if inst.ShapeConfig != nil {
			detail.MaxVnics = inst.ShapeConfig.MaxVnicAttachments
		}
		mu.Unlock()
	}()

	// 网络：网卡 + 公网 IP 对象 + IPv6。
	wg.Add(1)
	go func() {
		defer wg.Done()
		vnics, err := s.collectVnicDetail(ctx, client, acc, cached)
		if err != nil {
			warn(i18n.M("inst.warn.read_network", shortErr(err)))
			return
		}
		mu.Lock()
		detail.Vnics = vnics
		mu.Unlock()
		for _, v := range vnics {
			unknown := v.PublicIPUnknown
			if !unknown {
				for _, s := range v.SecondaryIPs {
					if s.PublicIPUnknown {
						unknown = true
						break
					}
				}
			}
			if unknown {
				warn(i18n.M("inst.warn.read_public_ip"))
				break
			}
		}
	}()

	// 引导卷及其挂载关系。
	wg.Add(1)
	go func() {
		defer wg.Done()
		if cached.BootVolumeID == "" {
			return
		}
		bv, err := client.GetBootVolume(ctx, cached.Region, cached.BootVolumeID)
		if err != nil {
			warn(i18n.M("inst.warn.read_boot", shortErr(err)))
			return
		}
		mu.Lock()
		detail.BootVolume = bv
		mu.Unlock()

		// 分离引导卷要的是挂载关系 ID，得单独查一次。
		atts, err := client.ListBootVolumeAttachments(ctx, cached.Region,
			acc.CompartmentOCID, cached.AvailabilityDomain, cached.ID)
		if err != nil {
			return
		}
		for _, att := range atts {
			if att.LifecycleState == "ATTACHED" && att.BootVolumeID == cached.BootVolumeID {
				mu.Lock()
				detail.BootVolumeAttachmentID = att.ID
				mu.Unlock()
				return
			}
		}
	}()

	// 附加块存储。
	wg.Add(1)
	go func() {
		defer wg.Done()
		vols, err := s.collectBlockVolumes(ctx, client, acc, cached)
		if err != nil {
			warn(i18n.M("inst.warn.read_block", shortErr(err)))
			return
		}
		mu.Lock()
		detail.BlockVolumes = vols
		mu.Unlock()
	}()

	wg.Wait()
	return detail, nil
}

func (s *Service) collectVnicDetail(ctx context.Context, client *ociclient.Client,
	acc *store.Account, cached *store.Instance) ([]VnicInfo, error) {

	atts, err := client.ListVnicAttachments(ctx, cached.Region, acc.CompartmentOCID, cached.ID)
	if err != nil {
		return nil, err
	}

	// 子网与 VCN 名称查一次缓存起来，多网卡时不必重复查询。
	subnetNames := map[string]ociclient.Subnet{}
	vcnNames := map[string]string{}

	out := make([]VnicInfo, 0, len(atts))
	for _, att := range atts {
		if att.LifecycleState != "ATTACHED" {
			continue
		}
		vnic, err := client.GetVnic(ctx, cached.Region, att.VnicID)
		if err != nil {
			slog.Debug("读取网卡失败", "vnic", att.VnicID, "err", err)
			continue
		}

		info := VnicInfo{
			AttachmentID: att.ID,
			VnicID:       vnic.ID,
			DisplayName:  vnic.DisplayName,
			IsPrimary:    vnic.IsPrimary,
			NicIndex:     att.NicIndex,
			PrivateIP:    vnic.PrivateIP,
			PublicIP:     vnic.PublicIP,
			MacAddress:   vnic.MacAddress,
			SubnetID:     vnic.SubnetID,
		}

		// 读取网卡上的全部私网 IP（主 + 辅助）及其绑定的公网 IP 对象。
		// 换 IP 功能需要它的 OCID，而 vnic.publicIp 只是个字符串。
		privateIPs, privErr := client.ListPrivateIPs(ctx, cached.Region, vnic.ID)
		if privErr != nil {
			slog.Debug("读取网卡私网 IP 失败", "vnic", vnic.ID, "err", privErr)
			info.PublicIPUnknown = true
		} else if len(privateIPs) > 0 {
			pubs := make([]*ociclient.PublicIP, len(privateIPs))
			pubErrs := make([]error, len(privateIPs))
			var ipWG sync.WaitGroup
			for idx, priv := range privateIPs {
				ipWG.Add(1)
				go func(pos int, privID string) {
					defer ipWG.Done()
					pub, err := client.GetPublicIPByPrivateIP(ctx, cached.Region, privID)
					if err != nil {
						slog.Debug("查询私网槽位公网 IP 失败", "privateIp", privID, "err", err)
						pubErrs[pos] = err
						return
					}
					if pub != nil {
						pubs[pos] = pub
					}
				}(idx, priv.ID)
			}
			ipWG.Wait()

			for idx, priv := range privateIPs {
				pub := pubs[idx]
				if priv.IsPrimary {
					info.PrivateIPID = priv.ID
					if pubErrs[idx] != nil {
						info.PublicIPUnknown = true
					} else if pub != nil {
						info.PublicIPID = pub.ID
						info.PublicIPType = pub.Lifetime
						if info.PublicIP == "" {
							info.PublicIP = pub.IPAddress
						}
					}
					continue
				}
				sec := SecondaryIPInfo{
					PrivateIPID:     priv.ID,
					PrivateIP:       priv.IPAddress,
					DisplayName:     priv.DisplayName,
					PublicIPUnknown: pubErrs[idx] != nil,
				}
				if pub != nil {
					sec.PublicIPID = pub.ID
					sec.PublicIP = pub.IPAddress
					sec.PublicIPType = pub.Lifetime
				}
				info.SecondaryIPs = append(info.SecondaryIPs, sec)
			}
			sort.SliceStable(info.SecondaryIPs, func(i, j int) bool {
				return compareIPAddr(info.SecondaryIPs[i].PrivateIP, info.SecondaryIPs[j].PrivateIP) < 0
			})
		}

		if v6s, err := client.ListIpv6s(ctx, cached.Region, vnic.ID); err == nil {
			// 按分配时间排：多出来的地址排在后面，一眼能看出是哪次加的。
			sort.SliceStable(v6s, func(i, j int) bool { return v6s[i].TimeCreated.Before(v6s[j].TimeCreated) })
			for _, v6 := range v6s {
				info.IPv6 = append(info.IPv6, v6.IPAddress)
				entry := IPv6Entry{Address: v6.IPAddress}
				if !v6.TimeCreated.IsZero() {
					t := v6.TimeCreated
					entry.TimeCreated = &t
				}
				info.IPv6Info = append(info.IPv6Info, entry)
			}
		}

		if vnic.SubnetID != "" {
			sub, ok := subnetNames[vnic.SubnetID]
			if !ok {
				subs, err := client.ListSubnets(ctx, cached.Region, acc.CompartmentOCID, "")
				if err == nil {
					for _, x := range subs {
						subnetNames[x.ID] = x
					}
					sub, ok = subnetNames[vnic.SubnetID]
				}
			}
			if ok {
				info.SubnetName = sub.DisplayName
				info.VcnID = sub.VcnID
				if name, found := vcnNames[sub.VcnID]; found {
					info.VcnName = name
				} else if vcn, err := client.GetVcn(ctx, cached.Region, sub.VcnID); err == nil {
					vcnNames[sub.VcnID] = vcn.DisplayName
					info.VcnName = vcn.DisplayName
				}
			}
		}

		out = append(out, info)
	}
	return out, nil
}

func (s *Service) collectBlockVolumes(ctx context.Context, client *ociclient.Client,
	acc *store.Account, cached *store.Instance) ([]VolumeInfo, error) {

	atts, err := client.ListVolumeAttachments(ctx, cached.Region, acc.CompartmentOCID, cached.ID)
	if err != nil {
		return nil, err
	}
	if len(atts) == 0 {
		return nil, nil
	}

	volumes := map[string]ociclient.Volume{}
	if vols, err := client.ListVolumes(ctx, cached.Region, acc.CompartmentOCID); err == nil {
		for _, v := range vols {
			volumes[v.ID] = v
		}
	}

	out := make([]VolumeInfo, 0, len(atts))
	for _, att := range atts {
		if att.LifecycleState == "DETACHED" {
			continue
		}
		info := VolumeInfo{
			VolumeID:     att.VolumeID,
			AttachmentID: att.ID,
			DisplayName:  att.DisplayName,
			Device:       att.Device,
			IsReadOnly:   att.IsReadOnly,
			State:        att.LifecycleState,
		}
		if v, ok := volumes[att.VolumeID]; ok {
			if info.DisplayName == "" {
				info.DisplayName = v.DisplayName
			}
			info.SizeInGBs = v.SizeInGBs
			info.VpusPerGB = v.VpusPerGB
		}
		out = append(out, info)
	}
	return out, nil
}

func shortErr(err error) string {
	if apiErr, ok := ociclient.AsAPIError(err); ok {
		if apiErr.Code != "" {
			return apiErr.Code + " · " + apiErr.Message
		}
		return apiErr.Message
	}
	return err.Error()
}

// compareIPAddr 按 IP 数值顺序比较，解析失败时回退到字典序。
func compareIPAddr(a, b string) int {
	addrA, errA := netip.ParseAddr(a)
	addrB, errB := netip.ParseAddr(b)
	if errA == nil && errB == nil {
		return addrA.Compare(addrB)
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
