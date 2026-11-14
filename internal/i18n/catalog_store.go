package i18n

func init() {
	register(map[string][2]string{
		"store.key_parse_failed":       {"私钥解析失败", "Couldn't parse the private key"},
		"store.alias_required":         {"账号别名不能为空", "Account alias is required"},
		"store.color_range":            {"身份色序号必须在 1..%d 之间", "Identity color must be between 1 and %d"},
		"store.tenancy_or_code_taken":  {"该租户或代号已被占用", "This tenancy or short code is already in use"},
		"store.region_required":        {"默认区域不能为空", "Default region is required"},
		"store.fingerprint_mismatch":   {"指纹与新私钥不匹配：配置为 %s，私钥实际指纹为 %s", "Fingerprint doesn't match the new private key: configured %s, key's actual fingerprint %s"},
		"store.fingerprint_needs_key":  {"修改指纹必须同时提供匹配的新私钥", "Changing the fingerprint requires a matching new private key"},
		"store.code_taken":             {"该代号已被占用", "This short code is already in use"},
		"store.stored_key_unparseable": {"已存储的私钥无法解析", "The stored private key can't be parsed"},
		"store.code_length":            {"账号代号必须是 2–4 个字符", "Short code must be 2–4 characters"},
		"store.code_charset":           {"账号代号只能包含字母和数字", "Short code may only contain letters and digits"},
		"store.capacity_watch_missing": {"容量监控项不存在", "Capacity watch not found"},
		"store.task_missing":           {"任务不存在", "Task not found"},
		"store.job_missing":            {"任务不存在", "Job not found"},
		"store.channel_name_required":  {"渠道名称不能为空", "Channel name is required"},
		"store.channel_kind_required":  {"渠道类型不能为空", "Channel type is required"},
		"store.channel_decrypt_failed": {"渠道配置解密失败（主密钥是否换过？），请重新填写配置", "Couldn't decrypt the channel configuration (was the master key changed?). Please fill it in again."},
		"store.proxy_exists":           {"该代理已存在", "This proxy already exists"},
		"store.proxy_invalid":          {"代理地址无效，支持 host:port、host:port:user:pass、scheme://user:pass@host:port", "Invalid proxy address. Supported: host:port, host:port:user:pass, scheme://user:pass@host:port"},
		"store.proxy_still_bound":      {"该代理仍被 %d 个账号绑定，请先解绑", "This proxy is still bound to %d account(s); unbind it first"},
		"store.sync_interval_range":    {"同步间隔必须在 2–1440 分钟之间，当前 %d", "Sync interval must be 2–1440 minutes (got %d)"},
		"store.check_interval_range":   {"校验间隔必须是 0（关闭）或 1–168 小时，当前 %d", "Check interval must be 0 (off) or 1–168 hours (got %d)"},
		"store.retention_range":        {"审计保留天数必须是 0（永久）或 7–3650 天，当前 %d", "Audit retention must be 0 (forever) or 7–3650 days (got %d)"},
		"store.language_invalid":       {"不支持的语言 %q", "Unsupported language %q"},
		"store.username_required":      {"用户名不能为空", "Username is required"},
		"store.username_taken":         {"用户名已存在", "Username already exists"},
		"store.totp_already_enabled":   {"两步验证已启用，请先关闭再重新绑定", "2FA is already enabled; disable it before setting it up again"},
	})
}
