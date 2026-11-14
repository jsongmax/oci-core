package i18n

// billingsvc / huntsvc
func init() {
	register(map[string][2]string{
		"bill.uncategorized":    {"未分类", "Uncategorized"},
		"bill.egress":           {"出站流量", "Outbound traffic"},
		"bill.svc.compute":      {"计算", "Compute"},
		"bill.svc.block":        {"块存储", "Block Storage"},
		"bill.svc.object":       {"对象存储", "Object Storage"},
		"bill.svc.archive":      {"归档存储", "Archive Storage"},
		"bill.svc.file":         {"文件存储", "File Storage"},
		"bill.svc.network":      {"网络", "Networking"},
		"bill.svc.transfer":     {"数据传输", "Data Transfer"},
		"bill.svc.lb":           {"负载均衡", "Load Balancer"},
		"bill.svc.database":     {"数据库", "Database"},
		"bill.svc.adb":          {"自治数据库", "Autonomous Database"},
		"bill.svc.monitoring":   {"监控", "Monitoring"},
		"bill.svc.logging":      {"日志", "Logging"},
		"bill.svc.notification": {"通知", "Notifications"},
		"bill.svc.vault":        {"密钥管理", "Vault"},
		"bill.svc.identity":     {"身份", "Identity"},
		"bill.svc.rm":           {"资源管理器", "Resource Manager"},

		"hunt.creds_failed":   {"读取账号凭据失败：%s", "Couldn't read account credentials: %s"},
		"hunt.ads_failed":     {"读取可用域失败：%s", "Couldn't read availability domains: %s"},
		"hunt.no_subnet":      {"任务没有子网，也没有勾选自动创建网络", "The task has no subnet and automatic network creation isn't enabled"},
		"hunt.network_failed": {"准备网络失败：%s", "Couldn't prepare the network: %s"},
		"hunt.v6_failed":      {"为子网开通 IPv6 失败：%s", "Couldn't enable IPv6 on the subnet: %s"},
		"hunt.max_attempts":   {"已达到最大尝试次数 %d，任务停止", "Reached the maximum of %d attempts; task stopped"},
		"hunt.expired":        {"任务已到期，自动停止", "The task expired and stopped automatically"},
		"hunt.no_ads":         {"该区域没有可用域", "This region has no availability domains"},
		"hunt.quota_full":     {"配额已满，继续重试没有意义：%s", "Quota is full; retrying won't help: %s"},
		"hunt.throttled":      {"被 Oracle 限流，该账号全部任务已降速：%s", "Throttled by Oracle; all tasks for this account slowed down: %s"},
		"hunt.stopped_title":  {"守候任务已停止：%s", "Capacity hunt stopped: %s"},
		"hunt.ok_title":       {"守候成功：%s", "Capacity hunt succeeded: %s"},
		"hunt.ok_body":        {"已在 %s 创建 %s（尝试 %d 次）", "Created %[2]s in %[1]s (after %[3]d attempts)"},
		"hunt.precheck_out":   {"容量预检：该可用域暂时没有容量，已跳过本轮创建请求", "Capacity pre-check: no capacity in this AD right now; skipped this round's create request"},
		"hunt.precheck_no_hw": {"容量预检：该区域未部署此规格的硬件。这个状态不会自行改变，请换规格或换区域", "Capacity pre-check: this shape's hardware isn't deployed in the region. That won't change by itself — switch shape or region"},
	})
}
