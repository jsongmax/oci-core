/**
 * 审计日志的显示规则。总览的「最近操作」与设置里的审计日志共用。
 *
 * 以前两页各写一份动作名对照表，新加的动作往往只登记了一边，另一边就显示成
 * account_check_auto 这样的原始键名。改动作名时只改这里。
 */

const ACTION_LABEL: Record<string, string> = {
  login: '登录', logout: '登出', setup: '首次设置',
  totp_verify: '两步验证', totp_enable: '启用两步验证', totp_disable: '关闭两步验证',
  change_password: '修改口令', sessions_revoke_all: '强制会话下线',
  account_create: '添加账号', account_update: '修改账号',
  account_rotate_key: '轮换密钥', account_delete: '删除账号',
  account_check_auto: '自动复查凭据',
  password_policy_update: '修改密码有效期',
  instance_start: '开机', instance_softstop: '关机', instance_stop: '强制关机',
  instance_softreset: '重启', instance_reset: '强制重启',
  instance_rename: '重命名', instance_reshape: '改配置',
  instance_attach_vnic: '添加网卡', instance_detach_vnic: '分离网卡',
  instance_terminate: '终止实例', instance_launch: '创建实例',
  instance_change_ip: '更换公网 IP', instance_enable_ipv6: '启用 IPv6', instance_remove_ipv6: '移除 IPv6',
  boot_volume_update: '修改引导卷', boot_volume_detach: '分离引导卷', boot_volume_attach: '挂载引导卷',
  volume_update: '修改块存储', volume_attach: '挂载块存储', volume_detach: '分离块存储', volume_delete: '删除卷',
  reserved_ip_release: '释放保留 IP',
  security_list_update: '修改安全规则', network_ensure: '自动建网',
  console_create: '建立控制台连接', console_delete: '删除控制台连接',
  capacity_watch_create: '添加容量监控',
  hunt_create: '创建守候任务', hunt_delete: '删除守候任务', hunt_pause: '暂停守候任务', hunt_resume: '恢复守候任务',
  settings_update: '修改设置',
  channel_create: '添加通知渠道', channel_update: '修改通知渠道', channel_delete: '删除通知渠道'
}

/** 动作名映射成中文。未登记的动作原样显示，不至于变成空白。 */
export function actionLabel(action: string): string {
  return ACTION_LABEL[action] ?? (action.startsWith('instance_bulk_') ? '批量操作' : action)
}

/**
 * 系统自己做的例行动作。审计日志里照常保留，但不进总览的「最近操作」——
 * 每个账号几小时一次的凭据复查会把用户真正做过的事挤出那 8 条。
 */
const ROUTINE = new Set(['account_check_auto'])
export const isRoutineAction = (action: string) => ROUTINE.has(action)

/**
 * 操作对象的显示文本。OCID 只留尾巴：一整串既挤坏布局，也不该原样摆在总览上
 * （旧版本把控制台连接的 OCID 当作对象记了下来，这里一并兜住）。
 */
export function auditTarget(target: string): string {
  if (/^ocid1\./.test(target)) return 'ocid1…' + target.slice(-6)
  return target
}
