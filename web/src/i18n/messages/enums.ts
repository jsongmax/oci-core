/**
 * 后端给出的枚举值（实例状态、审计动作、任务种类等）的显示名。
 * 键与后端常量一一对应，未登记的值由调用方原样显示。
 */
const zh = {
  lifecycle: {
    RUNNING: '运行中', STOPPED: '已停止', PROVISIONING: '创建中', STARTING: '启动中',
    STOPPING: '关机中', TERMINATING: '终止中', TERMINATED: '已终止'
  },
  audit: {
    login: '登录', logout: '登出', setup: '首次设置',
    totp_verify: '两步验证', totp_enable: '启用两步验证', totp_disable: '关闭两步验证',
    change_password: '修改口令', sessions_revoke_all: '强制会话下线',
    account_create: '添加账号', account_update: '修改账号',
    account_rotate_key: '轮换密钥', account_delete: '删除账号',
    account_check_auto: '自动复查凭据',
    password_policy_update: '修改密码有效期',
    instance_start: '开机', instance_softstop: '关机', instance_stop: '强制关机',
    instance_softreset: '重启', instance_reset: '强制重启',
    instance_rename: '重命名', instance_reshape: '改配置', instance_replace_boot_volume: '替换引导卷',
    instance_attach_vnic: '添加网卡', instance_detach_vnic: '分离网卡',
    instance_terminate: '终止实例', instance_launch: '创建实例',
    instance_change_ip: '更换公网 IP', instance_enable_ipv6: '启用 IPv6', instance_add_ipv6: '添加 IPv6',
    instance_remove_ipv6: '移除 IPv6',
    boot_volume_update: '修改引导卷', boot_volume_detach: '分离引导卷', boot_volume_attach: '挂载引导卷',
    volume_update: '修改块存储', volume_attach: '挂载块存储', volume_detach: '分离块存储', volume_delete: '删除卷',
    reserved_ip_release: '释放保留 IP',
    security_list_update: '修改安全规则', network_ensure: '自动建网',
    console_create: '建立控制台连接', console_delete: '删除控制台连接',
    capacity_watch_create: '添加容量监控',
    hunt_create: '创建守候任务', hunt_delete: '删除守候任务', hunt_pause: '暂停守候任务', hunt_resume: '恢复守候任务',
    settings_update: '修改设置',
    channel_create: '添加通知渠道', channel_update: '修改通知渠道', channel_delete: '删除通知渠道'
  } as Record<string, string>,
  auditBulk: '批量操作'
}

const en: typeof zh = {
  lifecycle: {
    RUNNING: 'Running', STOPPED: 'Stopped', PROVISIONING: 'Provisioning', STARTING: 'Starting',
    STOPPING: 'Stopping', TERMINATING: 'Terminating', TERMINATED: 'Terminated'
  },
  audit: {
    login: 'Sign in', logout: 'Sign out', setup: 'Initial setup',
    totp_verify: '2FA verification', totp_enable: 'Enable 2FA', totp_disable: 'Disable 2FA',
    change_password: 'Change password', sessions_revoke_all: 'Sign out all sessions',
    account_create: 'Add account', account_update: 'Edit account',
    account_rotate_key: 'Rotate API key', account_delete: 'Delete account',
    account_check_auto: 'Auto credential check',
    password_policy_update: 'Change password expiry',
    instance_start: 'Start', instance_softstop: 'Stop', instance_stop: 'Force stop',
    instance_softreset: 'Reboot', instance_reset: 'Force reboot',
    instance_rename: 'Rename', instance_reshape: 'Change shape', instance_replace_boot_volume: 'Replace boot volume',
    instance_attach_vnic: 'Attach VNIC', instance_detach_vnic: 'Detach VNIC',
    instance_terminate: 'Terminate instance', instance_launch: 'Create instance',
    instance_change_ip: 'Change public IP', instance_enable_ipv6: 'Enable IPv6', instance_add_ipv6: 'Add IPv6',
    instance_remove_ipv6: 'Remove IPv6',
    boot_volume_update: 'Edit boot volume', boot_volume_detach: 'Detach boot volume', boot_volume_attach: 'Attach boot volume',
    volume_update: 'Edit block volume', volume_attach: 'Attach block volume', volume_detach: 'Detach block volume',
    volume_delete: 'Delete volume',
    reserved_ip_release: 'Release reserved IP',
    security_list_update: 'Edit security rules', network_ensure: 'Auto network setup',
    console_create: 'Create console connection', console_delete: 'Delete console connection',
    capacity_watch_create: 'Add capacity watch',
    hunt_create: 'Create hunt', hunt_delete: 'Delete hunt', hunt_pause: 'Pause hunt', hunt_resume: 'Resume hunt',
    settings_update: 'Change settings',
    channel_create: 'Add notification channel', channel_update: 'Edit notification channel',
    channel_delete: 'Delete notification channel'
  },
  auditBulk: 'Bulk action'
}

export default { zh, en }
