import type { MaskKind } from '@/lib/mask'

export type LifecycleState =
  | 'RUNNING' | 'STOPPED' | 'PROVISIONING' | 'STARTING' | 'STOPPING' | 'TERMINATING' | 'TERMINATED'

export type AccountStatus = 'ok' | 'checking' | 'error' | 'disabled'

/** 1–8，对应 --acct-N。接入时分配后不再变化（§5.2） */
export type AccountColorIndex = 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8

export interface Quota {
  ocpuUsed: number; ocpuLimit: number
  memUsed: number; memLimit: number
  blockUsed: number; blockLimit: number
  /** AMD 免费微型实例，通常是 2 台封顶 */
  microUsed: number; microLimit: number
  /** 哪些项没有实际上限。升级号的 ARM 就属于这种 */
  unlimited: { ocpu: boolean; mem: boolean; block: boolean; micro: boolean }
}

export type AccountTier = 'trial' | 'paid' | 'unknown'

export interface Account {
  /** 后端主键，16 位十六进制。前端只当作不透明标识使用 */
  id: string
  alias: string
  /** 三字母短代号，全局唯一，与身份色同时出现 */
  code: string
  colorIndex: AccountColorIndex
  tenancyTail: string
  regions: string[]
  status: AccountStatus
  /** 上次校验时刻（ISO）。文案在模板里现算，不预先格式化 */
  lastCheckedAt: string | null
  /** 校验失败时后端给的原因 */
  statusMessage: string
  /** 账号性质：试用 / 升级 / 未知 */
  tier: AccountTier
  /** 试用到期日（ISO）。仅试用号有 */
  trialEndsAt: string | null
  /** 甲骨文账号开户时刻（ISO）。取不到时为 null */
  openedAt: string | null
  email: string
  fingerprint: string
  quota: Quota
  /** 配额数据来自哪个区域（后端只查主区域），为空表示还没拉到 */
  quotaRegion: string
  createdAt: string
}

export interface Instance {
  /** 实例 OCID。是完整的 ocid1.instance… 串，不是数字 */
  id: string
  accountId: string
  name: string
  ocidTail: string
  region: string
  /** 给人看的简写，形如 AD-1。调 OCI 接口一律用 adFull */
  ad: string
  /** 完整可用域名，形如 xxxx:US-SANJOSE-1-AD-1 */
  adFull: string
  shape: string
  ocpu: number
  memGb: number
  publicIp: string
  privateIp: string
  ipv6?: string
  bootGb: number
  bootLimitGb: number
  vpu: number
  /** 面板观测到开机的时刻。null 表示未观测到，只能退回"创建至今" */
  runningSince: string | null
  /** 用户备注 */
  note: string
  /** 实例创建时刻 */
  createdAt: string
  state: LifecycleState
  /** 状态异常（如非用户操作导致的 STOPPED），列表中加 ⚠ */
  anomaly?: boolean
  /** 后端记录的最近一次操作失败原因，非空时该行浮出错误条 */
  lastError?: string
  /** 乐观更新期间按钮 spinner */
  busy?: boolean
  /** 刚落定：触发 instance-ready 高光，时间戳 */
  settledAt?: number
}

export interface ToastPayload {
  tone: 'success' | 'warning' | 'danger' | 'info' | 'accent'
  title: string
  body?: string
  command?: string
}

export interface ConfirmL2 {
  level: 2
  title: string
  body: string
  okLabel: string
  onConfirm: () => void
}

export interface ConfirmL3 {
  level: 3
  title: string
  body: string
  /** 必须手动输入的资源全名 */
  noun: string
  nounLabel: string
  losses: string[]
  okLabel: string
  onConfirm: () => void
}

export type ConfirmRequest = ConfirmL2 | ConfirmL3

export type DrawerRequest =
  | { kind: 'instance'; id: string; tab?: string }
  | { kind: 'account'; id: string; tab?: string }
  | { kind: 'add-account' }
  | { kind: 'create-instance' }

/** KeyValueList 的一行 */
export interface KvItem {
  k: string
  v: string
  mono?: boolean
  tone?: string
  copyable?: boolean
  /**
   * 非空表示这是敏感值，按该类型打码并带展开按钮。
   * 复制仍然复制原文。
   */
  secret?: MaskKind
}

/** CheckList 的一项（权限自检、连接校验、诊断） */
export interface CheckItem {
  tone: 'ok' | 'warn' | 'fail' | 'info'
  text: string
}
