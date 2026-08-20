/**
 * 后端返回的原始数据结构（DTO）。字段名与 Go 侧的 json tag 一一对应。
 *
 * 刻意与 @/types 里的视图模型分开：视图模型是为了渲染方便而设计的
 * （预格式化的 uptime、扁平的 quota），DTO 是后端的契约。
 * 两者之间的转换集中在 @/lib/adapt。
 */

export type LifecycleState =
  | 'RUNNING' | 'STOPPED' | 'PROVISIONING' | 'STARTING' | 'STOPPING' | 'TERMINATING' | 'TERMINATED'

export type AccountStatusDTO = 'unchecked' | 'ok' | 'error'

export interface StatusDTO {
  setupRequired: boolean
  authenticated: boolean
  totpRequired: boolean
  totpEnabled: boolean
  username?: string
  version: string
}

export interface MeDTO {
  username: string
  totpEnabled: boolean
  createdAt: string
}

export interface LoginResultDTO {
  totpRequired: boolean
  totpEnabled: boolean
}

export interface TotpSetupDTO {
  secret: string
  uri: string
}

export interface AccountDTO {
  id: string
  alias: string
  code: string
  colorIndex: number
  tenancyOcid: string
  userOcid: string
  fingerprint: string
  defaultRegion: string
  compartmentOcid: string
  proxyUrl: string
  enabled: boolean
  status: AccountStatusDTO
  statusMessage: string
  lastCheckedAt: string | null
  /** FREE_TRIAL / PAY_AS_YOU_GO，取不到时为空 */
  paymentModel?: string
  subscriptionState?: string
  /** 试用订阅上就是试用到期日 */
  /** 甲骨文账号开户时刻。注意与 createdAt（接进本面板的时刻）不是一回事 */
  subscriptionStartsAt?: string | null
  subscriptionEndsAt?: string | null
  subscribedRegions: string[] | null
  homeRegion: string
  email: string
  tenancyName: string
  createdAt: string
  updatedAt: string
}

export interface InstanceDTO {
  id: string
  accountId: string
  region: string
  compartmentId: string
  displayName: string
  availabilityDomain: string
  faultDomain: string
  shape: string
  ocpus: number
  memoryGb: number
  lifecycleState: LifecycleState
  imageId: string
  publicIp: string
  privateIp: string
  ipv6: string
  vnicId: string
  subnetId: string
  bootVolumeId: string
  bootVolumeGb: number
  bootVolumeVpus: number
  timeCreated: string
  /** 面板观测到进入 RUNNING 的时刻，null 表示首次同步时它就已经在跑了 */
  runningSince: string | null
  /** 用户手写的备注，不来自 Oracle */
  note: string
  syncedAt: string
  /** 最近一次操作失败的原因，非空时该行浮出错误条 */
  lastError: string
  accountAlias: string
  accountCode: string
  accountColorIndex: number
}

export interface SyncStatusDTO {
  syncing: boolean
  lastSync: string
}

export interface RegionErrorDTO {
  accountId: string
  accountAlias: string
  region: string
  message: string
  ociCode?: string
  advice?: string
}

export interface SyncReportDTO {
  startedAt: string
  durationMs: number
  accounts: number
  regions: number
  instances: number
  /** 已终止实例数。界面各处不显示它们，因此不计入 instances */
  terminated: number
  pruned: number
  errors: RegionErrorDTO[] | null
}

export interface CheckStepDTO {
  key: string
  label: string
  ok: boolean
  detail: string
}

export interface CheckResultDTO {
  ok: boolean
  steps: CheckStepDTO[]
  userName?: string
  userEmail?: string
  tenancyName?: string
  regions?: string[]
  homeRegion?: string
  errorCode?: string
  errorText?: string
  advice?: string
  accountFatal: boolean
}

export interface ConfigProfileDTO {
  name: string
  userOcid: string
  fingerprint: string
  tenancyOcid: string
  region: string
  keyFile: string
  hasPassPhrase: boolean
  complete: boolean
  missing: string[] | null
  suggestedCode: string
}

export interface QuotaItemDTO {
  /** 稳定的语义标识，UI 按它取值——不要匹配 name，那是会变的 OCI 限额名 */
  key: 'ocpu' | 'memory' | 'block' | 'micro' | string
  name: string
  label: string
  used: number
  limit: number
  /** false 表示这项没查到，UI 必须显示"未知"而不是 0 */
  known: boolean
  /** 没查到时的原因，供排障用 */
  error?: string
  /** true 表示没有实际上限（升级号的 ARM 限额 Oracle 返回一亿，那是哨兵值） */
  unlimited?: boolean
}

export interface AccountQuotaDTO {
  accountId: string
  region: string
  items: QuotaItemDTO[] | null
  error?: string
  fetchedAt: string
}

export interface VnicInfoDTO {
  attachmentId: string
  vnicId: string
  displayName: string
  isPrimary: boolean
  nicIndex: number
  privateIp: string
  publicIp: string
  publicIpId?: string
  publicIpType?: string
  privateIpId?: string
  ipv6: string[] | null
  macAddress: string
  subnetId: string
  subnetName?: string
  vcnId?: string
  vcnName?: string
}

export interface VolumeInfoDTO {
  volumeId: string
  attachmentId: string
  displayName: string
  sizeInGbs: number
  vpusPerGb: number
  device: string
  isReadOnly: boolean
  state: string
}

export interface BootVolumeDTO {
  id: string
  compartmentId: string
  displayName: string
  availabilityDomain: string
  sizeInGBs: number
  vpusPerGB: number
  lifecycleState: string
  imageId: string
  isHydrated: boolean
  timeCreated: string
}

export interface InstanceDetailDTO {
  instance: InstanceDTO
  vnics: VnicInfoDTO[] | null
  bootVolume: BootVolumeDTO | null
  /** 挂载关系 OCID，不是卷 OCID。分离引导卷用的是这个 */
  bootVolumeAttachmentId: string
  blockVolumes: VolumeInfoDTO[] | null
  metadata: Record<string, string> | null
  warnings?: string[]
}

export interface MetricSeriesDTO {
  metric: string
  aggregation: string
  datapoints: { timestamp: string; value: number }[] | null
  error?: string
}

export interface MetricsDTO {
  instanceId: string
  start: string
  end: string
  resolution: string
  series: MetricSeriesDTO[]
  notice: string
}

export interface AttentionItemDTO {
  kind: string
  accountId?: string
  target: string
  message: string
  severity: 'warning' | 'danger'
}

export interface OverviewDTO {
  accounts: { total: number; ok: number; error: number; disabled: number }
  instances: { total: number; byState: Record<string, number> }
  distribution: { accountId: string; region: string; count: number }[] | null
  regions: string[] | null
  sync: SyncStatusDTO
  attention: AttentionItemDTO[] | null
}

export interface SettingsDTO {
  allowTerminate: boolean
  allowBulkActions: boolean
  requireTotpForDanger: boolean
  syncIntervalMinutes: number
  /** 自动重跑凭据校验的间隔，0 为关闭 */
  checkIntervalHours: number
  /** 审计日志保留天数，0 为永久保留 */
  auditRetentionDays: number
}

export interface ChannelDTO {
  id: string
  kind: string
  name: string
  /** 机密字段已被后端打码，形如 1234••••••••WXYZ */
  config: Record<string, string>
  events: string[]
  enabled: boolean
  lastError: string
  lastSentAt: string | null
  createdAt: string
  updatedAt: string
}

export interface ChannelFieldDTO {
  key: string
  label: string
  required: boolean
  secret: boolean
  hint?: string
}

export interface ChannelKindDTO {
  kind: string
  label: string
  fields: ChannelFieldDTO[]
}

export interface NotifyEventDTO {
  key: string
  label: string
  description: string
}

export interface AuditEntryDTO {
  id: number
  userId: string
  action: string
  accountId: string
  target: string
  detail: string
  ip: string
  result: string
  createdAt: string
}

export interface ShapeDTO {
  shape: string
  processorDescription: string
  ocpus: number
  memoryInGBs: number
  isFlexible: boolean
  ocpuOptions: { min: number; max: number } | null
  memoryOptions: {
    minInGBs: number
    maxInGBs: number
    minPerOcpuInGBs: number
    maxPerOcpuInGBs: number
  } | null
}

export interface ImageDTO {
  id: string
  displayName: string
  operatingSystem: string
  operatingSystemVersion: string
  timeCreated: string
}

export interface AvailabilityDomainDTO {
  id: string
  name: string
  compartmentId: string
}

export interface LaunchPresetDTO {
  key: string
  label: string
  shape: string
  ocpus: number
  memoryInGbs: number
  bootGb: number
  description: string
  freeTier: boolean
}

export interface LaunchResultDTO {
  instance: InstanceDTO
  steps: string[] | null
  notice?: string
}

export interface VcnDTO {
  id: string
  displayName: string
  cidrBlock: string
  ipv6CidrBlocks: string[] | null
  lifecycleState: string
  defaultSecurityListId: string
  defaultRouteTableId: string
}

export interface SubnetDTO {
  id: string
  vcnId: string
  displayName: string
  cidrBlock: string
  ipv6CidrBlock: string
  availabilityDomain: string
  prohibitPublicIpOnVnic: boolean
  lifecycleState: string
}

export interface PortRange { min: number; max: number }

export interface IngressRuleDTO {
  protocol: string
  source: string
  sourceType?: string
  isStateless?: boolean
  tcpOptions?: { destinationPortRange?: PortRange; sourcePortRange?: PortRange }
  udpOptions?: { destinationPortRange?: PortRange; sourcePortRange?: PortRange }
  icmpOptions?: { type: number; code?: number }
  description?: string
}

export interface EgressRuleDTO {
  protocol: string
  destination: string
  destinationType?: string
  isStateless?: boolean
  tcpOptions?: { destinationPortRange?: PortRange; sourcePortRange?: PortRange }
  udpOptions?: { destinationPortRange?: PortRange; sourcePortRange?: PortRange }
  icmpOptions?: { type: number; code?: number }
  description?: string
}

export interface SecurityListDTO {
  id: string
  vcnId: string
  displayName: string
  ingressSecurityRules: IngressRuleDTO[] | null
  egressSecurityRules: EgressRuleDTO[] | null
  lifecycleState: string
  /** 后端标出的"等同全放行"规则下标，UI 需显著警示 */
  allowAllRules: number[] | null
}

export interface RuleTemplateDTO {
  key: string
  label: string
  protocol: string
  port: number
  description: string
  dangerous: boolean
}

export interface PublicIpDTO {
  id: string
  displayName: string
  ipAddress: string
  lifetime: string
  scope: string
  privateIpId: string
  lifecycleState: string
}

export interface EnsureNetworkDTO {
  vcnId: string
  vcnName: string
  subnetId: string
  subnetName: string
  created: boolean
  steps: string[] | null
}

export interface ChangeIpDTO {
  oldIp: string
  newIp: string
}

export interface EnableIpv6DTO {
  address: string
  steps: string[] | null
}

export interface ConsoleConnectionDTO {
  id: string
  instanceId: string
  lifecycleState: string
  serialConsoleCommand: string
  vncConsoleCommand: string
  notice: string
  /** 两层 ssh 都要指定私钥，Oracle 给的命令串里没有 -i */
  keyHint?: string
}

/* ---------- 容量守候（抢机） ---------- */

export interface HuntTaskDTO {
  id: string
  accountId: string
  region: string
  name: string
  /** running / paused / succeeded / failed */
  state: string
  attempts: number
  intervalSeconds: number
  /** 每轮先查容量报告，说没货就跳过、不发创建请求 */
  precheckCapacity: boolean
  lastClass: string
  lastError: string
  lastAd: string
  lastTryAt: string
  nextAt: string
  instanceId: string
  maxAttempts: number
  expiresAt: string
  createdAt: string
  updatedAt: string

  shape: string
  ocpus: number
  memoryGb: number
  displayName: string
  ads: string[] | null
}

export interface HuntLimitsDTO {
  /** 允许配置的最小间隔，硬下限 */
  minIntervalSeconds: number
  defaultIntervalSeconds: number
  /** 低于它前端必须给出限流风险警告 */
  warnIntervalSeconds: number
}

export interface CreateHuntInput {
  accountId: string
  region?: string
  name?: string
  availabilityDomains?: string[]
  intervalSeconds: number
  precheckCapacity?: boolean
  maxAttempts?: number
  expiresInHours?: number

  displayName: string
  shape: string
  ocpus?: number
  memoryInGbs?: number
  imageId: string
  bootVolumeGb?: number
  subnetId?: string
  autoCreateNetwork?: boolean
  assignPublicIp?: boolean
  enableIpv6?: boolean
  sshPublicKey?: string
  cloudInit?: string
}

/* ---------- 容量监控 ---------- */

export interface CapacityWatchDTO {
  id: string
  accountId: string
  region: string
  availabilityDomain: string
  availabilityDomainShort: string
  shape: string
  ocpus: number
  memoryGb: number
  enabled: boolean
  /** AVAILABLE / OUT_OF_HOST_CAPACITY / HARDWARE_NOT_SUPPORTED */
  lastStatus: string
  statusText: string
  lastCount: number
  lastError: string
  lastCheckedAt: string
  lastChangedAt: string
  createdAt: string
  updatedAt: string
}

export interface CapacityProbeRow {
  availabilityDomain: string
  short: string
  status: string
  statusText: string
  availableCount: number
  error?: string
}

export interface CapacityProbeResult {
  region: string
  shape: string
  results: CapacityProbeRow[]
  notice: string
}

export interface CapacityProbeInput {
  accountId: string
  region?: string
  /** 留空表示查该区域全部可用域 */
  availabilityDomain?: string
  shape: string
  ocpus?: number
  memoryGb?: number
}

/* ---------- 账单 ---------- */

/**
 * 账单状态。
 *
 * free 与 no_permission 都不是错误，但含义完全相反：前者是"确实没花钱"，
 * 后者是"不知道花没花钱"。合成一个状态会让用户误以为账号免费。
 */
export type BillingStatusDTO = 'ok' | 'free' | 'no_permission' | 'disabled' | 'error'

export interface BillingSummaryDTO {
  accountId: string
  status: BillingStatusDTO
  /** Oracle 返回的币种。免费号常常为空 */
  currency: string
  thisMonth: number
  lastMonth: number
  region: string
  error?: string
  fetchedAt: string
}

/** 按币种分开的跨账号合计。不同币种绝不相加 */
export interface BillingCurrencyTotalDTO {
  currency: string
  thisMonth: number
  lastMonth: number
  accounts: number
}

export interface BillingListDTO {
  summaries: BillingSummaryDTO[]
  totals: BillingCurrencyTotalDTO[]
  countedAccounts: number
  noPermissionCount: number
  notice: string
}

/** 按维度（服务 / 区域）分组的一项 */
export interface BillingBucketDTO {
  key: string
  amount: number
  /** 用量视角的数字，免费号只有这一半有内容 */
  quantity: number
  unit: string
}

export interface BillingDayDTO {
  /** UTC 日期，YYYY-MM-DD */
  date: string
  amount: number
}

export interface BillingDetailDTO {
  accountId: string
  status: BillingStatusDTO
  currency: string
  region: string
  start: string
  end: string
  days: number
  total: number
  series: BillingDayDTO[] | null
  services: BillingBucketDTO[] | null
  regions: BillingBucketDTO[] | null
  usage: BillingBucketDTO[] | null
  error?: string
  fetchedAt: string
}
