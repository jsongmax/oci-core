package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ocicore/internal/huntsvc"
	"ocicore/internal/notify"
	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

// huntTaskDTO 是任务的对外表示。
//
// 比 store.HuntTask 多出账号别名与规格摘要：表格一行要能自解释，
// 让前端拿着 accountId 再去别处拼装只会多一次查找和一次不同步的机会。
type huntTaskDTO struct {
	store.HuntTask
	Shape       string   `json:"shape"`
	Ocpus       float32  `json:"ocpus"`
	MemoryGB    float32  `json:"memoryGb"`
	DisplayName string   `json:"displayName"`
	ADs         []string `json:"ads"`
}

func toHuntDTO(t store.HuntTask) huntTaskDTO {
	dto := huntTaskDTO{HuntTask: t, ADs: t.ADList()}
	if spec, err := huntsvc.DecodeSpec(t.Spec); err == nil {
		dto.Shape = spec.Shape
		dto.Ocpus = spec.Ocpus
		dto.MemoryGB = spec.MemoryInGBs
		dto.DisplayName = spec.DisplayName
	}
	return dto
}

func (s *Server) handleListHuntTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.st.ListHuntTasks(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := make([]huntTaskDTO, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, toHuntDTO(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tasks": out,
		"limits": map[string]any{
			"minIntervalSeconds":     huntsvc.MinIntervalSeconds,
			"defaultIntervalSeconds": huntsvc.DefaultIntervalSeconds,
			"warnIntervalSeconds":    huntsvc.WarnIntervalSeconds,
		},
	})
}

type createHuntRequest struct {
	AccountID string   `json:"accountId"`
	Region    string   `json:"region"`
	Name      string   `json:"name"`
	ADs       []string `json:"availabilityDomains"`

	IntervalSeconds int `json:"intervalSeconds"`
	MaxAttempts     int `json:"maxAttempts"`
	ExpiresInHours  int `json:"expiresInHours"`
	// PrecheckCapacity 用指针：不传时默认开，而 bool 零值是 false——
	// 那会让老前端建出来的任务静默关掉预检，行为悄悄变差。
	PrecheckCapacity *bool `json:"precheckCapacity"`

	// 创建参数，与创建实例表单一致。
	DisplayName  string  `json:"displayName"`
	Shape        string  `json:"shape"`
	Ocpus        float32 `json:"ocpus"`
	MemoryInGBs  float32 `json:"memoryInGbs"`
	ImageID      string  `json:"imageId"`
	BootVolumeGB int64   `json:"bootVolumeGb"`

	SubnetID          string `json:"subnetId"`
	AutoCreateNetwork bool   `json:"autoCreateNetwork"`
	AssignPublicIP    *bool  `json:"assignPublicIp"`
	EnableIPv6        bool   `json:"enableIpv6"`

	SSHPublicKey string `json:"sshPublicKey"`
	CloudInit    string `json:"cloudInit"`
}

// defaultExpiryHours 是不指定时的任务寿命。
//
// 默认给上限而不是"永不过期"：这个功能最容易出的事故不是抢不到，
// 而是用户建完就忘，一个任务在无人看管下持续调用 Oracle 好几个月。
const defaultExpiryHours = 7 * 24

func (s *Server) handleCreateHuntTask(w http.ResponseWriter, r *http.Request) {
	var req createHuntRequest
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
			"请选择子网，或勾选自动创建网络——任务在后台跑，中途没法再问你要参数")
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

	// 单账号只允许一个在跑。并行不会更快抢到——容量是账号级共享的，
	// 只会把同一个账号的请求量翻倍，正好撞在风控上。
	active, err := s.st.CountActiveHuntTasks(r.Context(), acc.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if active > 0 {
		writeError(w, http.StatusConflict, "hunt_already_running",
			"该账号已有一个守候任务在运行。容量是账号级共享的，并行不会更快，"+
				"只会让请求量翻倍。请先停掉现有任务。")
		return
	}

	// 规格校验放在创建时做一次，而不是留给每一轮尝试。
	// 参数写错的话，任务会安静地失败几百次，用户看到的只是"一直没抢到"。
	ad := ""
	if len(req.ADs) > 0 {
		ad = req.ADs[0]
	} else {
		ads, err := client.ListAvailabilityDomains(r.Context(), region, acc.CompartmentOCID)
		if err != nil {
			writeOCIError(w, err)
			return
		}
		if len(ads) == 0 {
			writeError(w, http.StatusBadRequest, "no_ad", "该区域没有可用域")
			return
		}
		ad = ads[0].Name
	}

	shapes, err := client.ListShapes(r.Context(), region, acc.CompartmentOCID, ad)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	var target *ociclient.Shape
	for i := range shapes {
		if shapes[i].Shape == req.Shape {
			target = &shapes[i]
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusBadRequest, "invalid_shape",
			"该可用域不提供规格 "+req.Shape)
		return
	}
	if target.IsFlexible {
		cfg := ociclient.ShapeConfig{Ocpus: req.Ocpus, MemoryInGBs: req.MemoryInGBs}
		if err := ociclient.ValidateShapeConfig(target, cfg); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_shape_config", err.Error())
			return
		}
	}

	assignPublic := true
	if req.AssignPublicIP != nil {
		assignPublic = *req.AssignPublicIP
	}

	spec, err := huntsvc.EncodeSpec(huntsvc.Spec{
		DisplayName:       strings.TrimSpace(req.DisplayName),
		Shape:             req.Shape,
		Ocpus:             req.Ocpus,
		MemoryInGBs:       req.MemoryInGBs,
		ImageID:           req.ImageID,
		BootVolumeGB:      req.BootVolumeGB,
		SubnetID:          req.SubnetID,
		AutoCreateNetwork: req.AutoCreateNetwork,
		AssignPublicIP:    assignPublic,
		EnableIPv6:        req.EnableIPv6,
		SSHPublicKey:      req.SSHPublicKey,
		CloudInit:         req.CloudInit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode_spec", err.Error())
		return
	}

	hours := req.ExpiresInHours
	if hours <= 0 {
		hours = defaultExpiryHours
	}
	interval := huntsvc.NormalizeInterval(req.IntervalSeconds)

	precheck := true
	if req.PrecheckCapacity != nil {
		precheck = *req.PrecheckCapacity
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = strings.TrimSpace(req.DisplayName)
	}

	task, err := s.st.CreateHuntTask(r.Context(), store.HuntTask{
		AccountID:        acc.ID,
		Region:           region,
		Name:             name,
		Spec:             spec,
		ADs:              strings.Join(req.ADs, ","),
		State:            store.HuntRunning,
		IntervalSeconds:  interval,
		PrecheckCapacity: precheck,
		MaxAttempts:      req.MaxAttempts,
		ExpiresAt:        time.Now().Add(time.Duration(hours) * time.Hour),
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "hunt_create", AccountID: acc.ID,
		Target: name, Detail: req.Shape + " · " + region + " · 每 " + strconv.Itoa(interval) + " 秒",
		IP: s.clientIP(r),
	})

	notice := "任务已启动，第一次尝试会立刻进行。"
	if interval < huntsvc.WarnIntervalSeconds {
		notice += "注意：你设置的间隔低于 " + strconv.Itoa(huntsvc.WarnIntervalSeconds) +
			" 秒，被 Oracle 限流的概率明显更高。"
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"task":   toHuntDTO(*task),
		"notice": notice,
	})
}

func (s *Server) handleSetHuntState(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	action := r.PathValue("action")

	var state string
	switch action {
	case "pause":
		state = store.HuntPaused
	case "resume":
		state = store.HuntRunning
	default:
		writeError(w, http.StatusBadRequest, "bad_action", "只支持 pause 或 resume")
		return
	}

	task, err := s.st.SetHuntState(r.Context(), id, state)
	if errors.Is(err, store.ErrHuntNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "任务不存在")
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "hunt_" + action, AccountID: task.AccountID,
		Target: task.Name, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]any{"task": toHuntDTO(*task)})
}

func (s *Server) handleDeleteHuntTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// 先读一次拿名字与账号，删完就查不到了，审计日志会缺关键信息。
	task, err := s.st.GetHuntTask(r.Context(), id)
	if errors.Is(err, store.ErrHuntNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "任务不存在")
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}

	if err := s.st.DeleteHuntTask(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "hunt_delete", AccountID: task.AccountID,
		Target: task.Name, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"notice": "任务已删除。已抢到的实例不受影响，要销毁请去实例页终止。",
	})
}

// ---- 调度器回调 ----

// RunHunter 启动容量守候的调度循环，直到 ctx 结束。
func (s *Server) RunHunter(ctx context.Context) { s.hunter.Run(ctx) }

// onHuntLaunched 在抢到实例后把它并入实例缓存。
//
// 不做这一步的话，实例要等到下一轮全量同步（默认 5 分钟）才会出现在列表里，
// 而用户刚收到"抢到了"的通知——那 5 分钟里他会以为通知是假的。
func (s *Server) onHuntLaunched(ctx context.Context, task *store.HuntTask,
	inst *ociclient.Instance, region, subnetID, imageID string) {

	if s.instances == nil {
		return
	}
	row := store.Instance{
		ID:                 inst.ID,
		AccountID:          task.AccountID,
		Region:             region,
		CompartmentID:      inst.CompartmentID,
		DisplayName:        inst.DisplayName,
		AvailabilityDomain: inst.AvailabilityDomain,
		FaultDomain:        inst.FaultDomain,
		Shape:              inst.Shape,
		LifecycleState:     inst.LifecycleState,
		ImageID:            imageID,
		SubnetID:           subnetID,
		TimeCreated:        inst.TimeCreated,
	}
	if inst.ShapeConfig != nil {
		row.Ocpus = float64(inst.ShapeConfig.Ocpus)
		row.MemoryGB = float64(inst.ShapeConfig.MemoryInGBs)
	}
	if err := s.st.UpsertInstance(ctx, row); err != nil {
		slog.Warn("守候：写入实例缓存失败", "instance", inst.ID, "err", err)
		return
	}
	// 起轮询，等它从 PROVISIONING 落定并拿到公网 IP。
	s.instances.Watch(inst.ID)
	s.instances.Bus().Publish(instanceUpdatedEvent(&row))
}

// onHuntEvent 把守候的关键节点推到通知渠道。
//
// 这个功能的价值就在于"人不用盯着"，那么结果必须主动送达；
// 只写进面板等于要求用户定期回来看，等于没做。
func (s *Server) onHuntEvent(ctx context.Context, event, title, body string) {
	ev := notify.EventHuntStopped
	if event == "hunt.succeeded" {
		ev = notify.EventHuntSucceeded
	}
	s.notifier.Dispatch(ctx, notify.Message{Event: ev, Title: title, Body: body})
}
