package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Instance 是实例缓存行。
//
// 为什么要缓存：跨账号聚合要对每个（账号 × 区域）都发一轮 API 调用，
// 8 个账号 4 个区域就是 32 次往返，让用户每次打开列表都等两秒是不可接受的。
// 缓存让列表秒开，同步在后台进行。
type Instance struct {
	ID                 string    `json:"id"`
	AccountID          string    `json:"accountId"`
	Region             string    `json:"region"`
	CompartmentID      string    `json:"compartmentId"`
	DisplayName        string    `json:"displayName"`
	AvailabilityDomain string    `json:"availabilityDomain"`
	FaultDomain        string    `json:"faultDomain"`
	Shape              string    `json:"shape"`
	Ocpus              float64   `json:"ocpus"`
	MemoryGB           float64   `json:"memoryGb"`
	LifecycleState     string    `json:"lifecycleState"`
	ImageID            string    `json:"imageId"`
	PublicIP           string    `json:"publicIp"`
	PrivateIP          string    `json:"privateIp"`
	IPv6               string    `json:"ipv6"`
	VnicID             string    `json:"vnicId"`
	SubnetID           string    `json:"subnetId"`
	BootVolumeID       string    `json:"bootVolumeId"`
	BootVolumeGB       int64     `json:"bootVolumeGb"`
	BootVolumeVpus     int64     `json:"bootVolumeVpus"`
	TimeCreated        time.Time `json:"timeCreated"`
	// RunningSince 是面板观测到该实例进入 RUNNING 的时刻。
	//
	// 为 nil 表示不知道——首次同步时它就已经在跑了。OCI 不返回上次开机
	// 时间，只能自己观测；猜一个值比承认不知道更糟，所以宁可留空让前端
	// 退回显示"创建至今"，并明确标出那是个近似值。
	RunningSince *time.Time `json:"runningSince"`
	SyncedAt     time.Time  `json:"syncedAt"`
	// Note 是用户手写的备注。
	//
	// 它是本工具里唯一不来自 Oracle 的实例字段，因此绝不能出现在
	// UpsertInstance 的更新列表里——那个方法每 5 分钟被同步调用一次，
	// 把 note 列进去就等于每 5 分钟把用户写的东西抹一遍。
	Note string `json:"note"`
	// LastError 记录最近一次操作失败的原因，用于「失败必须可见地回滚」。
	// 前端读到非空值就在该行浮出错误条。
	LastError string `json:"lastError"`

	// 以下字段由查询时联表带出，方便前端直接渲染账号身份。
	AccountAlias      string `json:"accountAlias"`
	AccountCode       string `json:"accountCode"`
	AccountColorIndex int    `json:"accountColorIndex"`
}

const instanceColumns = `i.id, i.account_id, i.region, i.compartment_id, i.display_name,
	i.availability_domain, i.fault_domain, i.shape, i.ocpus, i.memory_gb, i.lifecycle_state,
	i.image_id, i.public_ip, i.private_ip, i.ipv6, i.vnic_id, i.subnet_id,
	i.boot_volume_id, i.boot_volume_gb, i.boot_volume_vpus,
	i.time_created, i.running_since, i.synced_at, i.last_error, i.note,
	a.alias, a.code, a.color_index`

// UpsertInstance 写入或更新一条实例缓存。
//
// 刻意不覆盖 last_error：同步流程不该抹掉一条尚未被用户看到的操作失败提示。
// 清除它由 ClearInstanceError 显式完成。
func (s *Store) UpsertInstance(ctx context.Context, in Instance) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO instances (id, account_id, region, compartment_id, display_name,
			availability_domain, fault_domain, shape, ocpus, memory_gb, lifecycle_state,
			image_id, public_ip, private_ip, ipv6, vnic_id, subnet_id,
			boot_volume_id, boot_volume_gb, boot_volume_vpus, time_created, synced_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			account_id=excluded.account_id, region=excluded.region,
			compartment_id=excluded.compartment_id, display_name=excluded.display_name,
			availability_domain=excluded.availability_domain, fault_domain=excluded.fault_domain,
			shape=excluded.shape, ocpus=excluded.ocpus, memory_gb=excluded.memory_gb,
			lifecycle_state=excluded.lifecycle_state, image_id=excluded.image_id,
			public_ip=excluded.public_ip, private_ip=excluded.private_ip, ipv6=excluded.ipv6,
			vnic_id=excluded.vnic_id, subnet_id=excluded.subnet_id,
			boot_volume_id=excluded.boot_volume_id, boot_volume_gb=excluded.boot_volume_gb,
			boot_volume_vpus=excluded.boot_volume_vpus,
			time_created=excluded.time_created, synced_at=excluded.synced_at,
			-- 只在"观测到状态跃迁进 RUNNING"时打点。首次见到就已经在跑的实例
			-- 保持 NULL：那种情况我们并不知道它是什么时候开的机，猜一个值
			-- 比承认不知道更糟。
			running_since = CASE
				WHEN excluded.lifecycle_state = 'RUNNING' AND instances.lifecycle_state <> 'RUNNING'
					THEN excluded.synced_at
				WHEN excluded.lifecycle_state <> 'RUNNING' THEN NULL
				ELSE instances.running_since
			END`,
		in.ID, in.AccountID, in.Region, in.CompartmentID, in.DisplayName,
		in.AvailabilityDomain, in.FaultDomain, in.Shape, in.Ocpus, in.MemoryGB, in.LifecycleState,
		in.ImageID, in.PublicIP, in.PrivateIP, in.IPv6, in.VnicID, in.SubnetID,
		in.BootVolumeID, in.BootVolumeGB, in.BootVolumeVpus,
		in.TimeCreated.Unix(), nowUnix())
	if err != nil {
		return fmt.Errorf("store: 写入实例缓存失败: %w", err)
	}
	return nil
}

// SetInstanceState 只更新生命周期状态，用于操作后的乐观更新与轮询落定。
func (s *Store) SetInstanceState(ctx context.Context, instanceID, state string) error {
	now := nowUnix()
	_, err := s.db.ExecContext(ctx, `
		UPDATE instances SET
			lifecycle_state = ?,
			running_since = CASE
				WHEN ? = 'RUNNING' AND lifecycle_state <> 'RUNNING' THEN ?
				WHEN ? <> 'RUNNING' THEN NULL
				ELSE running_since
			END,
			synced_at = ?
		WHERE id = ?`,
		state, state, now, state, now, instanceID)
	if err != nil {
		return fmt.Errorf("store: 更新实例状态失败: %w", err)
	}
	return nil
}

// SetInstanceNote 写入用户备注。空串表示清除。
//
// 单独一个方法而不是并进 UpsertInstance：后者是同步流程用的，
// 会用 Oracle 返回的数据覆盖整行；备注不来自 Oracle，必须只由这里改。
func (s *Store) SetInstanceNote(ctx context.Context, instanceID, note string) error {
	// 只动 note 一列。synced_at 是 PruneStaleInstances 判断「本轮没被刷新到」
	// 的依据，顺手更新它会让一台已从 Oracle 消失的实例躲过清理。
	res, err := s.db.ExecContext(ctx,
		`UPDATE instances SET note = ? WHERE id = ?`, note, instanceID)
	if err != nil {
		return fmt.Errorf("store: 写入实例备注失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetInstanceError 记录一次操作失败，供前端在该行浮出错误条。
func (s *Store) SetInstanceError(ctx context.Context, instanceID, message string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE instances SET last_error = ? WHERE id = ?`, message, instanceID)
	if err != nil {
		return fmt.Errorf("store: 记录实例错误失败: %w", err)
	}
	return nil
}

// ClearInstanceError 清除错误标记。用户确认过错误提示后调用。
func (s *Store) ClearInstanceError(ctx context.Context, instanceID string) error {
	return s.SetInstanceError(ctx, instanceID, "")
}

// InstanceFilter 是实例列表的过滤条件。
type InstanceFilter struct {
	AccountIDs []string
	Regions    []string
	States     []string
	// Search 同时匹配实例名、OCID 与公网 IP。
	Search string
	// IncludeTerminated 为 false 时隐藏已终止实例（默认行为）。
	IncludeTerminated bool
}

// ListInstances 读取实例缓存，联表带出账号身份信息。
func (s *Store) ListInstances(ctx context.Context, f InstanceFilter) ([]Instance, error) {
	where := []string{"1=1"}
	args := []any{}

	if len(f.AccountIDs) > 0 {
		where = append(where, "i.account_id IN ("+placeholders(len(f.AccountIDs))+")")
		for _, id := range f.AccountIDs {
			args = append(args, id)
		}
	}
	if len(f.Regions) > 0 {
		where = append(where, "i.region IN ("+placeholders(len(f.Regions))+")")
		for _, r := range f.Regions {
			args = append(args, r)
		}
	}
	if len(f.States) > 0 {
		where = append(where, "i.lifecycle_state IN ("+placeholders(len(f.States))+")")
		for _, st := range f.States {
			args = append(args, st)
		}
	} else if !f.IncludeTerminated {
		// 已终止实例默认不出现在列表里，否则用过一段时间后列表会被墓碑塞满。
		where = append(where, "i.lifecycle_state != 'TERMINATED'")
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		where = append(where, "(i.display_name LIKE ? OR i.id LIKE ? OR i.public_ip LIKE ?)")
		like := "%" + s + "%"
		args = append(args, like, like, like)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT `+instanceColumns+`
		FROM instances i JOIN accounts a ON a.id = i.account_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY a.created_at ASC, i.display_name ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询实例列表失败: %w", err)
	}
	defer rows.Close()

	out := make([]Instance, 0, 32)
	for rows.Next() {
		inst, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *inst)
	}
	return out, rows.Err()
}

// GetInstance 按 OCID 读取单条实例缓存。
func (s *Store) GetInstance(ctx context.Context, instanceID string) (*Instance, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+instanceColumns+`
		FROM instances i JOIN accounts a ON a.id = i.account_id
		WHERE i.id = ?`, instanceID)
	inst, err := scanInstance(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return inst, err
}

// DeleteInstance 从缓存中移除实例。终止完成后调用。
func (s *Store) DeleteInstance(ctx context.Context, instanceID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM instances WHERE id = ?`, instanceID)
	if err != nil {
		return fmt.Errorf("store: 删除实例缓存失败: %w", err)
	}
	return nil
}

// PruneStaleInstances 清掉某（账号 × 区域）下本轮同步未见到的实例。
//
// 这是同步流程识别"实例已在 Oracle 控制台被删掉"的唯一手段：
// 本轮同步开始前记下时间戳，结束后凡是 synced_at 早于它的都已不复存在。
func (s *Store) PruneStaleInstances(ctx context.Context, accountID, region string, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM instances WHERE account_id = ? AND region = ? AND synced_at < ?`,
		accountID, region, before.Unix())
	if err != nil {
		return 0, fmt.Errorf("store: 清理过期实例缓存失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// CountInstancesByState 统计各状态的实例数，用于总览页 KPI。
func (s *Store) CountInstancesByState(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT lifecycle_state, COUNT(*) FROM instances
		 WHERE lifecycle_state != 'TERMINATED' GROUP BY lifecycle_state`)
	if err != nil {
		return nil, fmt.Errorf("store: 统计实例状态失败: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int, 6)
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		out[state] = n
	}
	return out, rows.Err()
}

// AccountRegionCount 是实例分布矩阵的一格：某账号在某区域有多少台机器。
type AccountRegionCount struct {
	AccountID string `json:"accountId"`
	Region    string `json:"region"`
	Count     int    `json:"count"`
}

// InstanceDistribution 返回账号 × 区域的实例分布，用于总览页的分布矩阵。
func (s *Store) InstanceDistribution(ctx context.Context) ([]AccountRegionCount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT account_id, region, COUNT(*) FROM instances
		 WHERE lifecycle_state != 'TERMINATED'
		 GROUP BY account_id, region ORDER BY account_id, region`)
	if err != nil {
		return nil, fmt.Errorf("store: 统计实例分布失败: %w", err)
	}
	defer rows.Close()

	out := make([]AccountRegionCount, 0, 16)
	for rows.Next() {
		var c AccountRegionCount
		if err := rows.Scan(&c.AccountID, &c.Region, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanInstance(sc rowScanner) (*Instance, error) {
	var (
		inst    Instance
		created int64
		running sql.NullInt64
		synced  int64
	)
	err := sc.Scan(
		&inst.ID, &inst.AccountID, &inst.Region, &inst.CompartmentID, &inst.DisplayName,
		&inst.AvailabilityDomain, &inst.FaultDomain, &inst.Shape, &inst.Ocpus, &inst.MemoryGB,
		&inst.LifecycleState, &inst.ImageID, &inst.PublicIP, &inst.PrivateIP, &inst.IPv6,
		&inst.VnicID, &inst.SubnetID, &inst.BootVolumeID, &inst.BootVolumeGB, &inst.BootVolumeVpus,
		&created, &running, &synced, &inst.LastError, &inst.Note,
		&inst.AccountAlias, &inst.AccountCode, &inst.AccountColorIndex,
	)
	if err != nil {
		return nil, err
	}
	inst.TimeCreated = unixToTime(created)
	inst.RunningSince = nullUnixToTime(running)
	inst.SyncedAt = unixToTime(synced)
	return &inst, nil
}

// placeholders 生成 "?,?,?" 形式的占位符串。
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
