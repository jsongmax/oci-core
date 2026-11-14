package httpapi

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ocicore/internal/i18n"
	"ocicore/internal/jobsvc"
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
			i18n.M("api.console.need_key"))
		return
	}
	if !strings.HasPrefix(key, "ssh-") && !strings.HasPrefix(key, "ecdsa-") {
		writeError(w, http.StatusBadRequest, "invalid_key",
			i18n.M("api.console.not_ssh_key"))
		return
	}
	// Oracle 的串行控制台只接受 RSA。
	//
	// 提前拦下来是为了给一句能照着做的话——直接发过去的话，Oracle 回的是
	// Invalid ssh public key type "ssh-ed25519"，用户既不知道该换成什么，
	// 也不知道这个限制只针对控制台（实例登录用的密钥没有这个限制）。
	if !strings.HasPrefix(key, "ssh-rsa") {
		writeError(w, http.StatusBadRequest, "console_needs_rsa",
			i18n.M("api.console.rsa_only"))
		return
	}

	client, acc, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}

	// 一台实例只能有一个活跃的控制台连接，否则 Oracle 会返回 409 而用户完全
	// 不知道发生了什么。已有连接时：
	//   - 公钥一致就直接复用；
	//   - 公钥不一致就删掉旧的再建。先前一律复用，换了公钥的人拿到的仍是绑着
	//     旧公钥的连接，照着命令连只会 Permission denied，界面上看不出原因。
	replaced := false
	existing, err := client.ListConsoleConnections(r.Context(), inst.Region, acc.CompartmentOCID, inst.ID)
	if err == nil {
		want := sshKeyMD5Fingerprint(key)
		for i := range existing {
			if existing[i].LifecycleState != "ACTIVE" {
				continue
			}
			if want == "" || strings.EqualFold(existing[i].Fingerprint, want) {
				writeJSON(w, http.StatusOK, consoleResponse(&existing[i]))
				return
			}
			if err := s.replaceConsoleConnection(r.Context(), client, inst.Region, acc.CompartmentOCID,
				inst.ID, existing[i].ID); err != nil {
				writeError(w, http.StatusBadGateway, "console_replace",
					i18n.M("api.console.replace_failed", shortOCIError(err)))
				return
			}
			replaced = true
			break
		}
	}

	conn, err := client.CreateConsoleConnection(r.Context(), inst.Region, acc.CompartmentOCID, inst.ID, key)
	if err != nil {
		writeOCIError(w, err)
		return
	}

	user := userFrom(r.Context())
	detail := ""
	if replaced {
		detail = i18n.M("api.console.replaced_old")
	}
	_ = s.st.Audit(r.Context(), store.AuditEntry{
		UserID: user.ID, Action: "console_create", AccountID: inst.AccountID,
		Target: inst.DisplayName, Detail: detail, IP: s.clientIP(r),
	})
	resp := consoleResponse(conn)
	if replaced {
		resp["replaced"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}

// sshKeyMD5Fingerprint 按 OCI 控制台连接记录的格式（ssh-keygen -E md5 的
// aa:bb:… 形式）计算 OpenSSH 公钥的指纹。解析不了时返回空串，调用方应当
// 退回「直接复用」，宁可多复用一次也不要误删别人的连接。
func sshKeyMD5Fingerprint(publicKey string) string {
	fields := strings.Fields(publicKey)
	if len(fields) < 2 {
		return ""
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return ""
	}
	sum := md5.Sum(blob)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = hex.EncodeToString([]byte{b})
	}
	return strings.Join(parts, ":")
}

// replaceConsoleConnection 删除旧连接并等它真正消失。
//
// 删除是异步的：连接会先停在 DELETING，这期间再建新连接 Oracle 仍会回 409。
func (s *Server) replaceConsoleConnection(ctx context.Context, client *ociclient.Client,
	region, compartment, instanceID, connID string) error {

	if err := client.DeleteConsoleConnection(ctx, region, connID); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for {
		conns, err := client.ListConsoleConnections(ctx, region, compartment, instanceID)
		if err == nil {
			busy := false
			for _, c := range conns {
				if c.ID == connID && c.LifecycleState != "DELETED" {
					busy = true
				}
			}
			if !busy {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return i18n.Err("api.console.wait_delete")
		case <-time.After(3 * time.Second):
		}
	}
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
		"notice": i18n.M("api.console.notice"),
		// 两层 ssh 都要显式 -i：只给外层加，内层的 ProxyCommand 仍然会拿默认密钥
		// 去试，然后被拒。写进 ~/.ssh/config 比每次记着加两个 -i 靠谱。
		"keyHint": i18n.M("api.console.key_hint"),
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
		writeError(w, http.StatusBadRequest, "missing_attachment", i18n.M("api.missing_attachment"))
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
		"notice": i18n.M("api.boot_detached"),
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
			i18n.M("api.need_instance_boot"))
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
			i18n.M("api.need_instance_volume"))
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
		"notice":     i18n.M("api.attached_as_data"),
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
		writeError(w, http.StatusBadRequest, "missing_attachment", i18n.M("api.missing_attachment"))
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
		"ok":     true,
		"notice": i18n.M("api.data_detached"),
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
		writeError(w, http.StatusForbidden, "bulk_disabled", i18n.M("api.bulk_disabled"))
		return
	}

	action := strings.ToUpper(strings.TrimSpace(req.Action))
	if err := ociclient.ValidateAction(action); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_action", err.Error())
		return
	}
	if isForcefulAction(action) && !req.Force {
		writeError(w, http.StatusBadRequest, "confirm_required",
			i18n.M("api.bulk_force_confirm"))
		return
	}
	if len(req.InstanceIDs) == 0 {
		writeError(w, http.StatusBadRequest, "empty_selection", i18n.M("api.empty_selection"))
		return
	}
	if len(req.InstanceIDs) > maxBulkSize {
		writeError(w, http.StatusBadRequest, "too_many",
			i18n.M("api.bulk_too_many", maxBulkSize))
		return
	}
	// 批量开机不会丢数据也不会断业务，其余批量动作一次波及多台机器，算危险操作。
	if action != "START" && !s.requireDangerTOTP(w, r) {
		return
	}

	verb := bulkVerb(action)
	who := s.actorOf(r)
	ids := append([]string(nil), req.InstanceIDs...)

	s.submitJob(w, jobsvc.Spec{
		Kind: jobBulkAction, Title: i18n.M("api.bulk_title", verb, len(ids)),
		Timeout: 20 * time.Minute,
	}, func(ctx context.Context, p *jobsvc.Progress) (jobsvc.Outcome, error) {
		results := make([]bulkResult, len(ids))
		var (
			wg   sync.WaitGroup
			done atomic.Int32
		)
		// 并发度压到 3：批量操作往往集中在同一个租户上，
		// 并发太高很容易把这个账号打进限流，反而拖慢所有人。
		sem := make(chan struct{}, 3)

		for i, id := range ids {
			wg.Add(1)
			go func(idx int, instanceID string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				entry := bulkResult{InstanceID: instanceID, Name: instanceID}
				if cached, err := s.st.GetInstance(ctx, instanceID); err == nil {
					entry.Name = cached.DisplayName
				}

				if _, err := s.instances.Action(ctx, instanceID, action); errors.Is(err, store.ErrNotFound) {
					entry.Error = i18n.M("api.instance_not_found")
					p.Step(i18n.M("api.bulk.failed_step", entry.Name, entry.Error))
				} else if err != nil {
					entry.Error = errorTextOf(err)
					p.Step(i18n.M("api.bulk.failed_step", entry.Name, entry.Error))
				} else {
					entry.OK = true
					p.Step(i18n.M("api.bulk.submitted_step", entry.Name, verb))
				}
				results[idx] = entry
				p.Set(int(done.Add(1)) * 30 / len(ids))
			}(i, id)
		}
		wg.Wait()

		succeeded := 0
		for _, res := range results {
			if res.OK {
				succeeded++
			}
		}
		s.audit(who, store.AuditEntry{
			Action: "instance_bulk_" + strings.ToLower(action),
			Target: i18n.M("api.bulk.target", len(ids)),
			Detail: i18n.M("api.bulk.detail", succeeded),
		})

		// 逐台等状态落定，进度条按台数推进。
		if succeeded > 0 {
			p.Step(i18n.M("api.step.wait_settle"))
		}
		waited := 0
		for _, res := range results {
			if !res.OK {
				continue
			}
			from := 30 + 69*waited/succeeded
			to := 30 + 69*(waited+1)/succeeded
			if inst, err := s.waitInstanceSettled(ctx, p, res.InstanceID, from, to, 0); err != nil {
				return jobsvc.Outcome{}, err
			} else if inst != nil {
				p.Step(res.Name + "：" + inst.LifecycleState)
			}
			waited++
		}

		out := jobsvc.Outcome{
			Summary: i18n.M("api.bulk.summary", succeeded, len(ids)-succeeded),
			Result: map[string]any{
				"results":   results,
				"succeeded": succeeded,
				"failed":    len(ids) - succeeded,
			},
		}
		if succeeded == 0 {
			return out, i18n.Err("api.bulk.all_failed", len(ids), verb)
		}
		return out, nil
	})
}

// bulkVerb 是批量操作在任务标题里的说法。
func bulkVerb(action string) string {
	switch action {
	case ociclient.ActionStart:
		return i18n.M("api.verb.start")
	case ociclient.ActionStop:
		return i18n.M("api.verb.force_stop")
	case ociclient.ActionReset:
		return i18n.M("api.verb.force_reboot")
	case ociclient.ActionSoftReset:
		return i18n.M("api.verb.reboot")
	default:
		return i18n.M("api.verb.stop")
	}
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
		"message": i18n.M("api.sessions_revoked"),
	})
}
