package i18n

// permsvc：权限自检。权限名、资源类型与动词保持官方原文，不翻译。
func init() {
	register(map[string][2]string{
		"perm.cap.instance.view":      {"查看实例列表与详情", "View instances and details"},
		"perm.cap.instance.power":     {"启动、停止、重启实例", "Start, stop and reboot instances"},
		"perm.cap.instance.update":    {"修改实例配置", "Update instance configuration"},
		"perm.cap.instance.launch":    {"创建实例", "Launch instances"},
		"perm.cap.instance.terminate": {"终止实例", "Terminate instances"},
		"perm.cap.instance.console":   {"创建与删除控制台连接", "Create and delete console connections"},
		"perm.cap.capacity.report":    {"容量查询、容量监控与守候预检", "Capacity checks, capacity watch and hunt pre-checks"},
		"perm.cap.network.view":       {"查看 VCN、子网与安全列表", "View VCNs, subnets and security lists"},
		"perm.cap.network.rules":      {"修改安全规则", "Update security rules"},
		"perm.cap.network.publicip":   {"更换临时公网 IP", "Replace ephemeral public IPs"},
		"perm.cap.network.ipv6":       {"添加与删除 IPv6 地址", "Add and remove IPv6 addresses"},
		"perm.cap.network.vnic":       {"附加与分离辅助网卡", "Attach and detach secondary VNICs"},
		"perm.cap.storage.view":       {"查看引导卷与块存储卷", "View boot volumes and block volumes"},
		"perm.cap.storage.update":     {"引导卷扩容与 VPU 调整", "Resize boot volumes and change VPUs"},
		"perm.cap.storage.attach":     {"挂载与卸载块存储卷", "Attach and detach block volumes"},
		"perm.cap.storage.delete":     {"删除引导卷与块存储卷", "Delete boot volumes and block volumes"},
		"perm.cap.account.quota":      {"查看配额与用量", "View service limits and usage"},
		"perm.cap.account.metrics":    {"查看监控指标", "View monitoring metrics"},
		"perm.cap.account.billing":    {"查看账单与出站流量", "View billing and outbound traffic"},

		"perm.why.probe_denied":  {"只读探测 %s 返回 NotAuthorizedOrNotFound。", "The read-only probe %s returned NotAuthorizedOrNotFound."},
		"perm.why.not_granted":   {"没有任何适用于该用户的策略语句授予此权限。", "No policy statement that applies to this user grants this permission."},
		"perm.why.probe_partial": {"只读探测 %s 证实了部分所需权限，其余权限无法判断。", "The read-only probe %s confirmed some of the required permissions; the rest cannot be determined."},
		"perm.why.no_policy":     {"无法读取策略，且没有可用的只读探测，无法判断。", "Policies could not be read and no read-only probe covers this permission."},
		"perm.why.no_groups":     {"无法确定该用户所在的组，只能识别对 any-user 生效的语句。", "The user's groups could not be determined; only statements for any-user can be evaluated."},
		"perm.why.conditional":   {"授予该权限的语句带有 where 条件，是否生效取决于请求内容。", "The granting statement has a where clause, so whether it applies depends on the request."},
		"perm.why.deny":          {"存在可能否决该权限的 Deny 语句。", "A Deny statement may override this permission."},
		"perm.why.compartment":   {"授权语句作用于其他区间，无法确定是否覆盖本账号使用的区间。", "A statement grants this in another compartment; it cannot be determined whether it covers the compartment this account uses."},
		"perm.why.unparsed":      {"部分策略语句无法解析，不能确定其中没有授予该权限。", "Some policy statements could not be parsed, so they may still grant this permission."},

		"perm.src.no_policy_read":     {"无法读取根区间的策略。策略解析需要 inspect policies in tenancy；未授予时仅依据只读探测判断。", "Policies in the root compartment could not be read. Policy analysis requires inspect policies in tenancy; without it, results rely on read-only probes only."},
		"perm.src.no_groups_read":     {"无法读取用户所在的组。需要 inspect users in tenancy 与 inspect groups in tenancy。", "The user's group memberships could not be read. This requires inspect users in tenancy and inspect groups in tenancy."},
		"perm.src.no_groups":          {"在默认身份域中未找到该用户所属的组。若用户位于其他身份域，按组授予的语句无法识别。", "No group memberships were found for this user in the Default identity domain. If the user belongs to another identity domain, group-based statements cannot be evaluated."},
		"perm.src.nested_compartment": {"本账号使用的区间不是根区间的直接下级，中间层区间的策略未读取。", "The compartment this account uses is not a direct child of the root compartment; policies in intermediate compartments were not read."},
		"perm.src.unparsed":           {"有 %d 条策略语句无法解析。", "%d policy statement(s) could not be parsed."},
		"perm.probe_skipped":          {"探测的前置步骤失败，已跳过。", "A prerequisite step of the probe failed; skipped."},
	})
}
