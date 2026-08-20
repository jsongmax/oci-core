/**
 * DTO → 视图模型的转换。
 *
 * 后端给的是时间戳与原始字段，视图要的是「12天4小时」「3 分钟前校验通过」
 * 这类可直接渲染的文本。格式化属于视图层的职责，因此集中在这里，
 * 而不是让后端返回预格式化的字符串。
 */
import type { AccountDTO, AccountQuotaDTO, InstanceDTO } from '@/api/types'
import type { Account, AccountColorIndex, AccountStatus, AccountTier, Instance, Quota } from '@/types'
import { now } from '@/lib/clock'

/** 相对时间：3 分钟前 / 2 小时前 / 5 天前。 */
export function relativeTime(iso: string | null | undefined): string {
  if (!iso) return '从未'
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return '未知'

  // 读全局时钟而不是 Date.now()：在模板里调用时，时间一跳就会重新渲染。
  const seconds = Math.floor((now.value - then) / 1000)
  if (seconds < 0) return '刚刚'
  if (seconds < 60) return '刚刚'
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时前`
  if (seconds < 86400 * 30) return `${Math.floor(seconds / 86400)} 天前`
  return new Date(iso).toLocaleDateString('zh-CN')
}

/** 运行时长：12天4小时 / 3小时20分 / 8分钟。 */
function durationText(iso: string | null | undefined): string {
  if (!iso) return '—'
  const from = new Date(iso).getTime()
  if (Number.isNaN(from)) return '—'

  const minutes = Math.floor((now.value - from) / 60000)
  if (minutes < 1) return '刚刚'
  if (minutes < 60) return `${minutes}分钟`

  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}小时${minutes % 60}分`

  const days = Math.floor(hours / 24)
  return `${days}天${hours % 24}小时`
}

/**
 * 实例的运行时长。必须在模板里调用——含相对时间，算一次存起来就不会走了。
 *
 * 这一列以前显示的是"创建至今"，却挂着"运行时长"的表头：实例重启过也
 * 照样显示创建以来的总时长，看起来像它从没停过。
 *
 * OCI 不返回上次开机时间，只能由本面板观测状态跃迁记录（runningSince）。
 * 首次同步时就已经在跑的实例观测不到，这时退回显示创建至今，并加 ~ 前缀
 * 标明是近似值——宁可标注不确定，也不要给一个看起来精确的错数字。
 */
export function instanceUptime(i: Instance): { text: string; approx: boolean } {
  // 没在跑就没有"运行时长"这回事。
  if (i.state !== 'RUNNING') return { text: '—', approx: false }
  if (i.runningSince) return { text: durationText(i.runningSince), approx: false }
  return { text: durationText(i.createdAt), approx: true }
}

/** 日期：2024-03-11。 */
export function dateText(iso: string | null | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toISOString().slice(0, 10)
}

/** OCID 尾段，用于列表里的次要标识。 */
export function ocidTail(ocid: string, length = 5): string {
  if (!ocid) return ''
  return ocid.slice(-length)
}

/** 可用域全名 → AD-1。OCI 的 AD 名形如 "xxxx:AP-TOKYO-1-AD-1"。 */
export function shortAd(availabilityDomain: string): string {
  const match = /AD-?(\d+)$/i.exec(availabilityDomain)
  return match ? `AD-${match[1]}` : availabilityDomain || '—'
}

/**
 * 账号连通性状态：后端只有三态，前端多一个 disabled。
 * 禁用优先于校验结果——一个被禁用的账号即使凭据有效也不该显示成绿灯。
 */
function accountStatus(dto: AccountDTO): AccountStatus {
  if (!dto.enabled) return 'disabled'
  if (dto.status === 'error') return 'error'
  if (dto.status === 'unchecked') return 'checking'
  return 'ok'
}

/**
 * 账号状态的一句话描述。必须在模板里调用，不能预先算好存进视图模型。
 *
 * 这段文案里含相对时间。算一次存起来的话，字符串就冻在了那一刻——
 * 页面开着不动，"2 分钟前"会一直是"2 分钟前"。在模板里调用才能跟着
 * 全局时钟重算。
 */
export function accountStatusText(a: Account): string {
  if (a.status === 'disabled') return '已在设置中手动禁用'
  if (a.status === 'checking') return '尚未校验凭据'
  if (a.status === 'error') {
    return a.statusMessage
      ? `${relativeTime(a.lastCheckedAt)}校验失败：${a.statusMessage}`
      : `${relativeTime(a.lastCheckedAt)}校验失败`
  }
  return `${relativeTime(a.lastCheckedAt)}校验通过`
}


/**
 * 账号性质。取不到订阅信息时返回 unknown,不猜。
 *
 * 尤其不能拿配额值反推:试用期内的账号拿到的限额远高于永久免费额度
 * (实测 ARM 16 OCPU / 96 GB,而 2026-06 起永久免费只有 2 / 12),按配额判断会把试用号
 * 认成升级号——正好认反了那个需要提醒用户的方向。
 */
function accountTier(paymentModel: string | undefined): AccountTier {
  if (paymentModel === 'FREE_TRIAL') return 'trial'
  if (paymentModel) return 'paid'
  return 'unknown'
}

/** 试用到期还剩几天。已过期返回负数,非试用号返回 null。 */
/**
 * 甲骨文账号开户至今多少天。取不到开户时间返回 null。
 *
 * 数据源是订阅的 timeCreated，不是本面板的 createdAt——后者只是
 * 「什么时候接进来的」。一个用了两年的老号今天才接进面板，
 * 拿 createdAt 算会显示成 0 天。
 */
export function accountAgeDays(a: Account): number | null {
  if (!a.openedAt) return null
  const t = new Date(a.openedAt).getTime()
  if (Number.isNaN(t)) return null
  return Math.max(0, Math.floor((now.value - t) / 86_400_000))
}

/** 「已创建 N 天」，超过两个月折成月/年；取不到则返回空串。 */
export function accountAgeText(a: Account): string {
  const d = accountAgeDays(a)
  if (d === null) return ''
  if (d === 0) return '今天创建'
  if (d < 60) return `已创建 ${d} 天`
  if (d < 365) return `已创建 ${Math.floor(d / 30)} 个月`
  const years = Math.floor(d / 365)
  const months = Math.floor((d % 365) / 30)
  return months > 0 ? `已创建 ${years} 年 ${months} 个月` : `已创建 ${years} 年`
}

export function trialDaysLeft(a: Account): number | null {
  if (!a.trialEndsAt) return null
  const end = new Date(a.trialEndsAt).getTime()
  if (Number.isNaN(end)) return null
  return Math.ceil((end - now.value) / 86_400_000)
}

const EMPTY_QUOTA: Quota = {
  ocpuUsed: 0, ocpuLimit: 0, memUsed: 0, memLimit: 0,
  blockUsed: 0, blockLimit: 0, microUsed: 0, microLimit: 0,
  unlimited: { ocpu: false, mem: false, block: false, micro: false }
}

/** 把后端的配额条目摊平成视图要的六个数字。 */
export function toQuota(dto: AccountQuotaDTO | undefined): Quota {
  if (!dto?.items) return { ...EMPTY_QUOTA }

  const quota: Quota = { ...EMPTY_QUOTA, unlimited: { ...EMPTY_QUOTA.unlimited } }
  for (const item of dto.items) {
    // known 为 false 时后端的 used/limit 是没意义的零值，
    // 直接跳过让它保持 0/0——UI 会把 limit 为 0 渲染成"未知"。
    if (!item.known) continue
    // 按 key 而不是 name 取值：name 是 OCI 的限额名，Oracle 改一次
    // 这里就会整片静默归零。
    switch (item.key) {
      case 'ocpu':
        quota.ocpuUsed = item.used
        quota.ocpuLimit = item.limit
        quota.unlimited.ocpu = !!item.unlimited
        break
      case 'memory':
        quota.memUsed = item.used
        quota.memLimit = item.limit
        quota.unlimited.mem = !!item.unlimited
        break
      case 'block':
        quota.blockUsed = item.used
        quota.blockLimit = item.limit
        quota.unlimited.block = !!item.unlimited
        break
      case 'micro':
        quota.microUsed = item.used
        quota.microLimit = item.limit
        quota.unlimited.micro = !!item.unlimited
        break
    }
  }
  return quota
}

export function toAccount(dto: AccountDTO, quota?: AccountQuotaDTO): Account {
  return {
    id: dto.id,
    alias: dto.alias,
    code: dto.code,
    colorIndex: (dto.colorIndex || 1) as AccountColorIndex,
    tenancyTail: ocidTail(dto.tenancyOcid),
    regions: dto.subscribedRegions?.length ? dto.subscribedRegions : [dto.defaultRegion].filter(Boolean),
    status: accountStatus(dto),
    lastCheckedAt: dto.lastCheckedAt,
    statusMessage: dto.statusMessage ?? '',
    tier: accountTier(dto.paymentModel),
    trialEndsAt: dto.paymentModel === 'FREE_TRIAL' ? (dto.subscriptionEndsAt ?? null) : null,
    openedAt: dto.subscriptionStartsAt ?? null,
    email: dto.email || dto.tenancyName || '—',
    fingerprint: dto.fingerprint,
    quota: toQuota(quota),
    quotaRegion: quota?.region ?? '',
    createdAt: dateText(dto.createdAt)
  }
}

export function toInstance(dto: InstanceDTO): Instance {
  return {
    id: dto.id,
    accountId: dto.accountId,
    name: dto.displayName,
    ocidTail: ocidTail(dto.id),
    region: dto.region,
    ad: shortAd(dto.availabilityDomain),
    // 完整 AD 名（xxxx:US-SANJOSE-1-AD-1）。ad 是给人看的简写，
    // 拿它去调 OCI 接口会静默筛不到任何东西——没有报错，只是空列表。
    adFull: dto.availabilityDomain,
    shape: dto.shape,
    ocpu: dto.ocpus,
    memGb: dto.memoryGb,
    publicIp: dto.publicIp || '—',
    privateIp: dto.privateIp || '—',
    ipv6: dto.ipv6 || undefined,
    bootGb: dto.bootVolumeGb,
    // 免费额度的块存储总量是 200 GB，用作进度条的分母。
    bootLimitGb: 200,
    vpu: dto.bootVolumeVpus,
    runningSince: dto.runningSince,
    createdAt: dto.timeCreated,
    note: dto.note ?? '',
    state: dto.lifecycleState,
    // 后端记录的操作失败即前端的异常标记：该行需要浮出错误条。
    anomaly: dto.lastError !== '',
    lastError: dto.lastError || undefined
  }
}
