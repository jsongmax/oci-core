package instancesvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"ocicore/internal/netsvc"
	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

const (
	// vnicWatchInterval / vnicWatchTimeout 用于等待网卡挂载或分离落定。
	// 挂载通常十几秒就好，分离要等系统里的网卡下线，慢一些。
	vnicWatchInterval = 4 * time.Second
	vnicWatchTimeout  = 4 * time.Minute
)

var (
	// ErrVnicLimit 表示已达到规格允许的网卡数上限。
	ErrVnicLimit = errors.New("已达到该规格的网卡数上限")
	// ErrPrimaryVnic 表示试图分离主网卡。OCI 本身也会拒绝，这里提前拦下给出人话。
	ErrPrimaryVnic = errors.New("主网卡不能分离，只能分离附属网卡")
	// ErrVnicNotFound 表示挂载关系不属于该实例，或已经分离。
	ErrVnicNotFound = errors.New("这张网卡不属于该实例，或已经分离")
	// ErrVnicInvalid 表示请求本身不成立（子网不匹配、子网禁公网 IP 等）。
	ErrVnicInvalid = errors.New("网卡参数无效")
)

// AttachVnicRequest 是添加附属网卡的参数。
type AttachVnicRequest struct {
	SubnetID       string `json:"subnetId"`
	DisplayName    string `json:"displayName"`
	AssignPublicIP bool   `json:"assignPublicIp"`
	AssignIPv6     bool   `json:"assignIpv6"`
}

// AttachVnicResult 是添加附属网卡的结果。网卡此时处于 ATTACHING，
// 落定由后台等待，完成后经 SSE 推送 instance.updated。
type AttachVnicResult struct {
	AttachmentID string   `json:"attachmentId"`
	State        string   `json:"state"`
	Warnings     []string `json:"warnings,omitempty"`
}

// AttachVnic 为实例添加一张附属网卡。
//
// 能在本地判断的失败都提前拦下：网卡数已满、子网不在实例所在的可用域、
// 子网禁止公网 IP 却要分配公网 IP。这些交给 Oracle 也会失败，但它的报错
// 只说参数不对，看不出是哪一项。
func (s *Service) AttachVnic(ctx context.Context, instanceID string, req AttachVnicRequest) (*store.Instance, *AttachVnicResult, error) {
	cached, err := s.st.GetInstance(ctx, instanceID)
	if err != nil {
		return nil, nil, err
	}
	if cached.LifecycleState == ociclient.LifecycleTerminating || cached.LifecycleState == ociclient.LifecycleTerminated {
		return nil, nil, fmt.Errorf("%w：实例已终止", ErrVnicInvalid)
	}
	req.SubnetID = strings.TrimSpace(req.SubnetID)
	if req.SubnetID == "" {
		return nil, nil, fmt.Errorf("%w：请选择子网", ErrVnicInvalid)
	}

	client, acc, err := s.conns.ForID(ctx, cached.AccountID)
	if err != nil {
		return nil, nil, err
	}

	inst, err := client.GetInstance(ctx, cached.Region, instanceID)
	if err != nil {
		return nil, nil, err
	}
	atts, err := client.ListVnicAttachments(ctx, cached.Region, acc.CompartmentOCID, instanceID)
	if err != nil {
		return nil, nil, err
	}
	used := 0
	for _, a := range atts {
		if a.LifecycleState == "ATTACHED" || a.LifecycleState == "ATTACHING" {
			used++
		}
	}
	if inst.ShapeConfig != nil && inst.ShapeConfig.MaxVnicAttachments > 0 && used >= inst.ShapeConfig.MaxVnicAttachments {
		return nil, nil, fmt.Errorf("%w：%s 当前配置最多 %d 张网卡，已有 %d 张",
			ErrVnicLimit, inst.Shape, inst.ShapeConfig.MaxVnicAttachments, used)
	}

	subnet, err := client.GetSubnet(ctx, cached.Region, req.SubnetID)
	if err != nil {
		return nil, nil, err
	}
	// 可用域专属子网只能给同一可用域的实例用；区域子网（AD 为空）不受限。
	if subnet.AvailabilityDomain != "" && subnet.AvailabilityDomain != cached.AvailabilityDomain {
		return nil, nil, fmt.Errorf("%w：子网 %s 属于另一个可用域，实例在 %s",
			ErrVnicInvalid, subnet.DisplayName, cached.AvailabilityDomain)
	}
	if req.AssignPublicIP && subnet.ProhibitPublicIPOnVnic {
		return nil, nil, fmt.Errorf("%w：子网 %s 禁止分配公网 IP，请换一个子网或不分配公网 IPv4",
			ErrVnicInvalid, subnet.DisplayName)
	}

	result := &AttachVnicResult{}
	// 与创建实例同一套：要 IPv6 就先确认子网真能分配，否则 Oracle 只回一句
	// "Subnet must support ipv6"，看不出缺的是子网前缀。
	if req.AssignIPv6 {
		prep, err := netsvc.PrepareIPv6(ctx, client, cached.Region, req.SubnetID)
		if err != nil {
			return nil, nil, fmt.Errorf("没能为子网开通 IPv6，网卡未添加：%w", err)
		}
		result.Warnings = prep.Warnings
	}

	assignPublic := req.AssignPublicIP
	details := ociclient.CreateVnicDetails{
		SubnetID:       req.SubnetID,
		AssignPublicIP: &assignPublic,
		DisplayName:    strings.TrimSpace(req.DisplayName),
	}
	if req.AssignIPv6 {
		yes := true
		details.AssignIpv6IP = &yes
	}

	att, err := client.AttachVnic(ctx, cached.Region, instanceID, details, details.DisplayName)
	if err != nil {
		return nil, nil, err
	}
	result.AttachmentID = att.ID
	result.State = att.LifecycleState

	s.watchVnic(cached.ID, att.ID, "ATTACHED")
	return cached, result, nil
}

// DetachVnic 分离一张附属网卡。网卡上的临时公网 IP 随之释放，不可找回。
func (s *Service) DetachVnic(ctx context.Context, instanceID, attachmentID string) (*store.Instance, *ociclient.Vnic, error) {
	cached, err := s.st.GetInstance(ctx, instanceID)
	if err != nil {
		return nil, nil, err
	}
	client, acc, err := s.conns.ForID(ctx, cached.AccountID)
	if err != nil {
		return nil, nil, err
	}

	// 只认这台实例名下的挂载关系，不信任前端传来的 ID 指向别处。
	atts, err := client.ListVnicAttachments(ctx, cached.Region, acc.CompartmentOCID, instanceID)
	if err != nil {
		return nil, nil, err
	}
	var target *ociclient.VnicAttachment
	for i := range atts {
		if atts[i].ID == attachmentID && atts[i].LifecycleState == "ATTACHED" {
			target = &atts[i]
			break
		}
	}
	if target == nil {
		return nil, nil, ErrVnicNotFound
	}

	vnic, err := client.GetVnic(ctx, cached.Region, target.VnicID)
	if err != nil {
		return nil, nil, err
	}
	if vnic.IsPrimary {
		return nil, nil, ErrPrimaryVnic
	}

	if err := client.DetachVnic(ctx, cached.Region, attachmentID); err != nil {
		return nil, nil, err
	}
	s.watchVnic(cached.ID, attachmentID, "DETACHED")
	return cached, vnic, nil
}

// watchVnic 在后台等待挂载关系落到 want，然后刷新实例的网络缓存并推送更新。
//
// 用 context.Background 的理由同 watchLoop：用户点完就关浏览器是正常的，
// 不能让缓存停在「网卡还没挂上」的那一刻。
func (s *Service) watchVnic(instanceID, attachmentID, want string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), vnicWatchTimeout)
		defer cancel()

		ticker := time.NewTicker(vnicWatchInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				slog.Warn("等待网卡状态落定超时", "instance", instanceID, "attachment", attachmentID, "want", want)
				return
			case <-ticker.C:
			}

			cached, err := s.st.GetInstance(ctx, instanceID)
			if err != nil {
				return
			}
			client, _, err := s.conns.ForID(ctx, cached.AccountID)
			if err != nil {
				return
			}

			settle := func(outcome string) {
				s.refreshNetwork(ctx, cached, client)
				s.bus.Publish(Event{
					Type: EventInstanceNetwork, InstanceID: instanceID,
					AccountID: cached.AccountID, State: outcome,
				})
			}

			att, err := client.GetVnicAttachment(ctx, cached.Region, attachmentID)
			if err != nil {
				// 分离完成后挂载关系可能直接查不到，这也算落定。
				if apiErr, ok := ociclient.AsAPIError(err); ok && apiErr.StatusCode == 404 && want == "DETACHED" {
					settle("DETACHED")
					return
				}
				continue
			}
			switch att.LifecycleState {
			case want:
				settle(want)
				return
			case "DETACHED":
				// 想要 ATTACHED 却落到 DETACHED：挂载失败了，刷新一次让界面回到真实状态。
				settle("FAILED")
				return
			}
		}
	}()
}

// RefreshNetwork 重新拉取实例的网卡与 IP 并写回缓存，供 IPv6 增删这类
// 不经过生命周期轮询的操作在完成后调用。
func (s *Service) RefreshNetwork(ctx context.Context, instanceID string) error {
	cached, err := s.st.GetInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	client, _, err := s.conns.ForID(ctx, cached.AccountID)
	if err != nil {
		return err
	}
	s.refreshNetwork(ctx, cached, client)
	return nil
}
