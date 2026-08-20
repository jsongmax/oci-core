package httpapi

import (
	"net/http"
	"strings"
	"sync"

	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

type consoleRequest struct {
	/** SSH 公钥。Oracle 用它鉴权，只有持有对应私钥的人才连得上控制台。 */
	PublicKey string `json:"publicKey"`
}

// handleCreateConsole 为实例建立串行控制台连接。
//
// 本工具不代管任何 SSH 私钥：公钥由用户提交，返回的连接命令也由用户
// 在自己的终端里执行。面板只负责把 Oracle 那套繁琐的连接串拼好。
func (s *Server) handleCreateConsole(w http.ResponseWriter, r *http.Request) {
	inst, err := s.st.GetInstance(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}

	var req consoleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	key := strings.TrimSpace(req.PublicKey)
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing_key",
			"请提供 SSH 公钥。Oracle 用它鉴权，本工具不会代管你的私钥。")
		return
	}
	if !strings.HasPrefix(key, "ssh-") && !strings.HasPrefix(key, "ecdsa-") {
		writeError(w, http.StatusBadRequest, "invalid_key",
			"这看起来不是 SSH 公钥，应以 ssh-rsa / ssh-ed25519 之类开头")
		return
	}
	// Oracle 的串行控制台只接受 RSA。
	//
	// 提前拦下来是为了给一句能照着做的话——直接发过去的话，Oracle 回的是
	// Invalid ssh public key type "ssh-ed25519"，用户既不知道该换成什么，
	// 也不知道这个限制只针对控制台（实例登录用的密钥没有这个限制）。
	if !strings.HasPrefix(key, "ssh-rsa") {
		writeError(w, http.StatusBadRequest, "console_needs_rsa",
			"Oracle 的串行控制台只接受 RSA 公钥，不支持 ed25519 / ecdsa。"+
				"请另生成一把：ssh-keygen -t rsa -b 4096 -f ~/.ssh/oci_console"+
				"（这个限制只针对控制台，实例登录用的密钥不受影响）")
		return
	}

	client, acc, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}

	// 一台实例只能有一个活跃的控制台连接，已存在就直接复用，
	// 否则 Oracle 会返回 409 而用户完全不知道发生了什么。
	existing, err := client.ListConsoleConnections(r.Context(), inst.Region, acc.CompartmentOCID, inst.ID)
	if err == nil {
		for i := range existing {
			if existing[i].LifecycleState == "ACTIVE" {
				writeJSON(w, http.StatusOK, consoleResponse(&existing[i]))
				return
			}
		}
	}

	conn, err := client.CreateConsoleConnection(r.Context(), inst.Region, acc.CompartmentOCID, inst.ID, key)
	if err != nil {
		writeOCIError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "console_create", AccountID: inst.AccountID,
		Target: inst.DisplayName, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, consoleResponse(conn))
}

func consoleResponse(conn *ociclient.InstanceConsoleConnection) map[string]any {
	return map[string]any{
		"id":                   conn.ID,
		"instanceId":           conn.InstanceID,
		"lifecycleState":       conn.LifecycleState,
		"serialConsoleCommand": conn.ConnectionString,
		"vncConsoleCommand":    conn.VncConnectionString,
		// Oracle 返回的命令串里没有 -i，它假设你的默认密钥就是注册的那把。
		// 而控制台要求 RSA，多数人的默认密钥是 ed25519——照抄必然失败，
		// 且失败信息是代理主机的 Permission denied，看起来像控制台本身的问题。
		"notice": "串行控制台适合排查「机器起不来」的场景——改坏 fstab、" +
			"防火墙把自己关在门外时都能救回来。",
		// 两层 ssh 都要显式 -i：只给外层加，内层的 ProxyCommand 仍然会拿默认密钥
		// 去试，然后被拒。写进 ~/.ssh/config 比每次记着加两个 -i 靠谱。
		"keyHint": "命令里是两层 ssh：外层连实例，内层的 ProxyCommand 先连 Oracle 的代理主机，" +
			"两层都要用你注册的那把 RSA 私钥。写进 ~/.ssh/config 最省事：\n\n" +
			`Host instance-console.*.oci.oraclecloud.com
    IdentityFile ~/.ssh/你的私钥
    IdentitiesOnly yes

Host ocid1.instance.*
    IdentityFile ~/.ssh/你的私钥
    IdentitiesOnly yes`,
	}
}

// ---- 引导卷分离与挂载（救援模式）----

func (s *Server) handleDetachBootVolume(w http.ResponseWriter, r *http.Request) {
	client, _, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	attachmentID := r.URL.Query().Get("attachmentId")
	if attachmentID == "" {
		writeError(w, http.StatusBadRequest, "missing_attachment", "缺少 attachmentId 参数")
		return
	}

	if err := client.DetachBootVolume(r.Context(), region, attachmentID); err != nil {
		writeOCIError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "boot_volume_detach",
		AccountID: r.URL.Query().Get("accountId"), Target: attachmentID, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"notice": "引导卷已分离。该实例在重新挂载引导卷之前无法启动。",
	})
}

func (s *Server) handleAttachBootVolume(w http.ResponseWriter, r *http.Request) {
	client, _, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	instanceID, bootVolumeID := q.Get("instanceId"), q.Get("bootVolumeId")
	if instanceID == "" || bootVolumeID == "" {
		writeError(w, http.StatusBadRequest, "missing_params",
			"需要 instanceId 与 bootVolumeId 两个参数")
		return
	}

	attachment, err := client.AttachBootVolume(r.Context(), region, instanceID, bootVolumeID, "")
	if err != nil {
		writeOCIError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "boot_volume_attach",
		AccountID: q.Get("accountId"), Target: instanceID,
		Detail: bootVolumeID, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "attachment": attachment})
}

// handleAttachVolume 把一块卷挂到实例上当数据盘。
//
// 救援流程的中间一环：坏机器的引导卷分离之后，挂到一台能 SSH 的机器上，
// mount 起来改里面的文件（补 authorized_keys、还原 fstab……），再挂回去。
// 没有这个接口的话这一步只能去 Oracle 控制台点，救援模式就是半截的。
func (s *Server) handleAttachVolume(w http.ResponseWriter, r *http.Request) {
	client, _, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	instanceID, volumeID := q.Get("instanceId"), q.Get("volumeId")
	if instanceID == "" || volumeID == "" {
		writeError(w, http.StatusBadRequest, "missing_params",
			"需要 instanceId 与 volumeId 两个参数")
		return
	}

	// 只允许半虚拟化。iSCSI 挂完还要在客机里跑 iscsiadm 才看得到设备，
	// 而面板没法替用户跑那串命令，给了选项只会让人挂上之后发现 lsblk 里什么都没有。
	attachment, err := client.AttachVolume(r.Context(), region, instanceID, volumeID,
		q.Get("displayName"), ociclient.AttachmentTypeParavirtualized)
	if err != nil {
		writeOCIError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "volume_attach",
		AccountID: q.Get("accountId"), Target: instanceID,
		Detail: volumeID, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"attachment": attachment,
		"notice": "已挂为数据盘。登录目标实例后用 lsblk 找到新增的设备（通常是 /dev/sdb），" +
			"mount 它的根分区就能改里面的文件。",
	})
}

// handleDetachVolume 分离数据盘挂载。
func (s *Server) handleDetachVolume(w http.ResponseWriter, r *http.Request) {
	client, _, region, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	attachmentID := r.URL.Query().Get("attachmentId")
	if attachmentID == "" {
		writeError(w, http.StatusBadRequest, "missing_attachment", "缺少 attachmentId 参数")
		return
	}

	if err := client.DetachVolume(r.Context(), region, attachmentID); err != nil {
		writeOCIError(w, err)
		return
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "volume_detach",
		AccountID: r.URL.Query().Get("accountId"), Target: attachmentID, IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"notice": "已分离。分离前记得先在实例里 umount，否则卷上可能有没落盘的写入。" +
			"接下来可以把它挂回原实例当引导卷。",
	})
}

// ---- 批量操作 ----

type bulkRequest struct {
	InstanceIDs []string `json:"instanceIds"`
	Action      string   `json:"action"`
	Force       bool     `json:"force"`
}

type bulkResult struct {
	InstanceID string `json:"instanceId"`
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

// maxBulkSize 限制单次批量操作的规模。
//
// 上限的意义不在于性能，而在于防呆：一次误选把三十台机器全关掉
// 的代价太大，超过这个数应当分批执行并逐批确认。
const maxBulkSize = 20

// handleBulkAction 批量执行开关机。
//
// 刻意不支持批量终止：那是 L3 操作，必须逐台输名确认。
func (s *Server) handleBulkAction(w http.ResponseWriter, r *http.Request) {
	var req bulkRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	policy, err := s.st.Settings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !policy.AllowBulkActions {
		writeError(w, http.StatusForbidden, "bulk_disabled", "批量操作已在设置中禁用")
		return
	}

	action := strings.ToUpper(strings.TrimSpace(req.Action))
	if err := ociclient.ValidateAction(action); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_action", err.Error())
		return
	}
	if isForcefulAction(action) && !req.Force {
		writeError(w, http.StatusBadRequest, "confirm_required",
			"批量强制操作会直接切断电源，请附带 force=true 确认")
		return
	}
	if len(req.InstanceIDs) == 0 {
		writeError(w, http.StatusBadRequest, "empty_selection", "没有选中任何实例")
		return
	}
	if len(req.InstanceIDs) > maxBulkSize {
		writeError(w, http.StatusBadRequest, "too_many",
			"单次批量操作最多 "+itoa(maxBulkSize)+" 台，请分批执行")
		return
	}

	results := make([]bulkResult, len(req.InstanceIDs))
	var wg sync.WaitGroup
	// 并发度压到 3：批量操作往往集中在同一个租户上，
	// 并发太高很容易把这个账号打进限流，反而拖慢所有人。
	sem := make(chan struct{}, 3)

	for i, id := range req.InstanceIDs {
		wg.Add(1)
		go func(idx int, instanceID string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			entry := bulkResult{InstanceID: instanceID}
			if cached, err := s.st.GetInstance(r.Context(), instanceID); err == nil {
				entry.Name = cached.DisplayName
			}

			if _, err := s.instances.Action(r.Context(), instanceID, action); err != nil {
				entry.Error = errorTextOf(err)
			} else {
				entry.OK = true
			}
			results[idx] = entry
		}(i, id)
	}
	wg.Wait()

	succeeded := 0
	for _, res := range results {
		if res.OK {
			succeeded++
		}
	}

	user := userFrom(r.Context())
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "instance_bulk_" + strings.ToLower(action),
		Target: itoa(len(req.InstanceIDs)) + " 台实例",
		Detail: "成功 " + itoa(succeeded) + " 台", IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"results":   results,
		"succeeded": succeeded,
		"failed":    len(results) - succeeded,
	})
}

func errorTextOf(err error) string {
	if apiErr, ok := ociclient.AsAPIError(err); ok {
		if apiErr.Code != "" {
			return apiErr.Code + " · " + apiErr.Message
		}
		return apiErr.Message
	}
	return err.Error()
}

// ---- 会话管理 ----

// handleRevokeSessions 强制该用户的全部会话下线（含当前会话）。
//
// 对应设计规格里「会话管理与强制下线」。怀疑凭据泄露时这是第一件该做的事。
func (s *Server) handleRevokeSessions(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if err := s.st.DeleteUserSessions(r.Context(), user.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	s.clearSessionCookie(w, r)
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "sessions_revoke_all", IP: s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "所有会话已下线，请重新登录",
	})
}
