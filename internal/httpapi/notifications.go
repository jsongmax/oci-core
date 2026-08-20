package httpapi

import (
	"net/http"
	"strings"
	"time"

	"ocicore/internal/notify"
	"ocicore/internal/store"
)

// secretFields 列出各渠道里需要打码的配置项。
//
// 与私钥同理：token 和 webhook 地址存进来之后就不该再回流到客户端。
// 拿到 Telegram token 就能冒充这个机器人，拿到群机器人 webhook 就能往群里发任何东西。
var secretFields = map[string]bool{
	"token": true, "password": true, "secret": true, "webhook": true,
}

// maskChannel 返回可安全下发给前端的渠道视图。
func maskChannel(ch store.Channel) store.Channel {
	masked := make(map[string]string, len(ch.Config))
	for k, v := range ch.Config {
		if secretFields[k] && v != "" {
			masked[k] = maskValue(v)
			continue
		}
		masked[k] = v
	}
	ch.Config = masked
	return ch
}

// maskValue 保留首尾各几位，中间打码——让用户能认出是哪一个，又拿不到完整值。
func maskValue(v string) string {
	const keep = 4
	if len(v) <= keep*2 {
		return strings.Repeat("•", 8)
	}
	return v[:keep] + strings.Repeat("•", 8) + v[len(v)-keep:]
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := s.st.ListChannels(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := make([]store.Channel, 0, len(channels))
	for _, ch := range channels {
		out = append(out, maskChannel(ch))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channels": out,
		"kinds":    notify.KindDefs(),
	})
}

// handleNotificationEvents 返回可订阅的事件清单，供前端渲染订阅矩阵。
func (s *Server) handleNotificationEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"events": notify.EventDefs()})
}

type createChannelRequest struct {
	Kind   string            `json:"kind"`
	Name   string            `json:"name"`
	Config map[string]string `json:"config"`
	Events []string          `json:"events"`
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	var req createChannelRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateChannelConfig(req.Kind, req.Config); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
		return
	}

	ch, err := s.st.CreateChannel(r.Context(), store.NewChannel{
		Kind:   req.Kind,
		Name:   req.Name,
		Config: req.Config,
		Events: req.Events,
	})
	if err != nil {
		writeChannelError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "channel_create", Target: ch.Name,
		Detail: ch.Kind, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusCreated, maskChannel(*ch))
}

type updateChannelRequest struct {
	Name    *string           `json:"name"`
	Config  map[string]string `json:"config"`
	Events  []string          `json:"events"`
	Enabled *bool             `json:"enabled"`
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	var req updateChannelRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	id := r.PathValue("id")
	current, err := s.st.GetChannel(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// 前端拿到的是打码后的值，原样提交回来会把真实配置覆盖成一串圆点。
	// 因此只接受"看起来不是打码结果"的字段，其余保留原值。
	if req.Config != nil {
		merged := make(map[string]string, len(current.Config))
		for k, v := range current.Config {
			merged[k] = v
		}
		for k, v := range req.Config {
			if secretFields[k] && isMasked(v) {
				continue
			}
			merged[k] = v
		}
		req.Config = merged
		if err := validateChannelConfig(current.Kind, merged); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
			return
		}
	}

	ch, err := s.st.UpdateChannel(r.Context(), id, store.ChannelUpdate{
		Name:    req.Name,
		Config:  req.Config,
		Events:  req.Events,
		Enabled: req.Enabled,
	})
	if err != nil {
		writeChannelError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "channel_update", Target: ch.Name, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, maskChannel(*ch))
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ch, err := s.st.GetChannel(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.st.DeleteChannel(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "channel_delete", Target: ch.Name, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTestChannel 立刻发一条测试消息，把结果原样返回。
//
// 这是通知配置里最关键的交互：配错 token 是常态，
// 必须能当场看到成功还是失败，而不是等真出事时才发现没收到。
func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	ch, err := s.st.GetChannel(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}

	msg := notify.Message{
		Event: "test",
		Title: "OCI Core 测试通知",
		Body:  "如果你看到这条消息，说明该渠道配置正确。",
		Fields: map[string]string{
			"渠道": ch.Name,
			"类型": ch.Kind,
		},
	}

	sendErr := s.notifier.Send(r.Context(), ch, msg)
	errMsg := ""
	if sendErr != nil {
		errMsg = sendErr.Error()
	}
	_ = s.st.RecordChannelSend(r.Context(), ch.ID, errMsg)

	if sendErr != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": errMsg,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// validateChannelConfig 检查必填字段是否齐全。
func validateChannelConfig(kind string, config map[string]string) error {
	for _, def := range notify.KindDefs() {
		if def.Kind != kind {
			continue
		}
		var missing []string
		for _, field := range def.Fields {
			if field.Required && strings.TrimSpace(config[field.Key]) == "" {
				missing = append(missing, field.Label)
			}
		}
		if len(missing) > 0 {
			return errMissingFields(def.Label, missing)
		}
		return nil
	}
	return errUnknownKind(kind)
}

func isMasked(v string) bool { return strings.Contains(v, "••••") }

func writeChannelError(w http.ResponseWriter, err error) {
	if strings.Contains(err.Error(), "store: ") {
		writeStoreError(w, err)
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
}

func errMissingFields(kindLabel string, missing []string) error {
	return &inputError{msg: kindLabel + " 渠道缺少必填项：" + strings.Join(missing, "、")}
}

func errUnknownKind(kind string) error {
	return &inputError{msg: "不支持的渠道类型 " + kind}
}

// inputError 是纯粹的输入校验错误，与 store 层的错误区分开。
type inputError struct{ msg string }

func (e *inputError) Error() string { return e.msg }

// ---- 设置 ----

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.st.Settings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

type updateSettingsRequest struct {
	AllowTerminate       *bool `json:"allowTerminate"`
	AllowBulkActions     *bool `json:"allowBulkActions"`
	RequireTOTPForDanger *bool `json:"requireTotpForDanger"`
	SyncIntervalMinutes  *int  `json:"syncIntervalMinutes"`
	CheckIntervalHours   *int  `json:"checkIntervalHours"`
	AuditRetentionDays   *int  `json:"auditRetentionDays"`
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req updateSettingsRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	settings, err := s.st.UpdateSettings(r.Context(), store.SettingsUpdate{
		AllowTerminate:       req.AllowTerminate,
		AllowBulkActions:     req.AllowBulkActions,
		RequireTOTPForDanger: req.RequireTOTPForDanger,
		SyncIntervalMinutes:  req.SyncIntervalMinutes,
		CheckIntervalHours:   req.CheckIntervalHours,
		AuditRetentionDays:   req.AuditRetentionDays,
	})
	if err != nil {
		writeChannelError(w, err)
		return
	}

	// 立刻应用到正在跑的后台循环。
	//
	// 以前这里什么都不做，改同步间隔要重启服务才生效，只能用一句提示文案
	// 打补丁——而"改了没反应"和"改坏了"在用户那里长得一模一样。
	// 巡检与清理循环每轮自己重读设置，只有同步间隔需要显式推过去。
	if s.instances != nil && req.SyncIntervalMinutes != nil {
		s.instances.SetSyncInterval(time.Duration(settings.SyncIntervalMinutes) * time.Minute)
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "settings_update", IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": settings,
		"notice":   "已保存并立即生效。",
	})
}
