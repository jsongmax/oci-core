package instancesvc

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

// detailTimeout 是拉取实例详情的整体上限。
const detailTimeout = 25 * time.Second

// VnicInfo 是详情页展示的一张网卡。
type VnicInfo struct {
	AttachmentID string   `json:"attachmentId"`
	VnicID       string   `json:"vnicId"`
	DisplayName  string   `json:"displayName"`
	IsPrimary    bool     `json:"isPrimary"`
	NicIndex     int      `json:"nicIndex"`
	PrivateIP    string   `json:"privateIp"`
	PublicIP     string   `json:"publicIp"`
	PublicIPID   string   `json:"publicIpId,omitempty"`
	PublicIPType string   `json:"publicIpType,omitempty"`
	PrivateIPID  string   `json:"privateIpId,omitempty"`
	IPv6         []string `json:"ipv6"`
	MacAddress   string   `json:"macAddress"`
	SubnetID     string   `json:"subnetId"`
	SubnetName   string   `json:"subnetName,omitempty"`
	VcnID        string   `json:"vcnId,omitempty"`
	VcnName      string   `json:"vcnName,omitempty"`
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
			warn("读取实例信息失败：" + shortErr(err))
			return
		}
		mu.Lock()
		detail.Instance.LifecycleState = inst.LifecycleState
		detail.Metadata = inst.Metadata
		mu.Unlock()
	}()

	// 网络：网卡 + 公网 IP 对象 + IPv6。
	wg.Add(1)
	go func() {
		defer wg.Done()
		vnics, err := s.collectVnicDetail(ctx, client, acc, cached)
		if err != nil {
			warn("读取网络信息失败：" + shortErr(err))
			return
		}
		mu.Lock()
		detail.Vnics = vnics
		mu.Unlock()
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
			warn("读取引导卷失败：" + shortErr(err))
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
			warn("读取块存储失败：" + shortErr(err))
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

		// 找到主私网 IP 对应的公网 IP 对象。
		// 换 IP 功能需要它的 OCID，而 vnic.publicIp 只是个字符串。
		if privateIPs, err := client.ListPrivateIPs(ctx, cached.Region, vnic.ID); err == nil {
			for _, p := range privateIPs {
				if !p.IsPrimary {
					continue
				}
				info.PrivateIPID = p.ID
				if pub, err := client.GetPublicIPByPrivateIP(ctx, cached.Region, p.ID); err == nil && pub != nil {
					info.PublicIPID = pub.ID
					info.PublicIPType = pub.Lifetime
				}
				break
			}
		}

		if v6s, err := client.ListIpv6s(ctx, cached.Region, vnic.ID); err == nil {
			for _, v6 := range v6s {
				info.IPv6 = append(info.IPv6, v6.IPAddress)
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
