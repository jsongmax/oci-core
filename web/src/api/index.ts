/** 按业务域分组的后端接口。所有函数直接返回 DTO，转换交给 @/lib/adapt。 */
import { http } from './http'
import type * as D from './types'

export * from './http'
export type * from './types'

/* ---------- 认证与初始化 ---------- */

export const auth = {
  status: () => http.get<D.StatusDTO>('/api/status'),

  setup: (username: string, password: string) =>
    http.post<{ username: string; nextStep: string }>('/api/setup', { username, password }),

  login: (username: string, password: string) =>
    http.post<D.LoginResultDTO>('/api/auth/login', { username, password }),

  verifyTotp: (code: string) =>
    http.post<{ ok: boolean }>('/api/auth/totp/verify', { code }),

  logout: () => http.post<{ ok: boolean }>('/api/auth/logout'),

  me: () => http.get<D.MeDTO>('/api/auth/me'),

  totpSetup: () => http.post<D.TotpSetupDTO>('/api/auth/totp/setup'),

  totpEnable: (code: string) =>
    http.post<{ ok: boolean }>('/api/auth/totp/enable', { code }),

  /** 关闭两步验证需要口令 + 一次有效验证码——摘掉一道防线要两道防线共同确认。 */
  totpDisable: (password: string, code: string) =>
    http.post<{ ok: boolean; message: string }>('/api/auth/totp/disable', { password, code }),

  changePassword: (current: string, next: string) =>
    http.post<{ ok: boolean; message: string }>('/api/auth/password', { current, new: next })
}

/* ---------- 账号 ---------- */

export interface CreateAccountInput {
  alias: string
  code: string
  colorIndex?: number
  tenancyOcid: string
  userOcid: string
  fingerprint: string
  privateKeyPem: string
  defaultRegion: string
  compartmentOcid?: string
  proxyUrl?: string
  skipCheck?: boolean
}

export const accounts = {
  list: () => http.get<{ accounts: D.AccountDTO[] }>('/api/accounts'),

  get: (id: string) => http.get<D.AccountDTO>(`/api/accounts/${id}`),

  create: (input: CreateAccountInput) =>
    http.post<{ account: D.AccountDTO; check?: D.CheckResultDTO }>('/api/accounts', input),

  update: (id: string, patch: Record<string, unknown>) =>
    http.patch<D.AccountDTO>(`/api/accounts/${id}`, patch),

  /** 删除需回传账号别名——服务端强制，前端确认框可以被绕过。 */
  remove: (id: string, confirmAlias: string) =>
    http.del<{ ok: boolean }>(`/api/accounts/${id}`, { query: { confirm: confirmAlias } }),

  check: (id: string) => http.post<D.CheckResultDTO>(`/api/accounts/${id}/check`),

  regions: (id: string) =>
    http.get<{ regions: { regionName: string; isHomeRegion: boolean }[] }>(`/api/accounts/${id}/regions`),

  parseConfig: (text: string) =>
    http.post<{ profiles: D.ConfigProfileDTO[] }>('/api/accounts/parse-config', { text }),

  checkDraft: (draft: {
    tenancyOcid: string; userOcid: string; fingerprint: string
    privateKeyPem: string; region: string; proxyUrl?: string
  }) => http.post<D.CheckResultDTO>('/api/accounts/check-draft', draft)
}

/* ---------- 实例 ---------- */

export interface InstanceFilterInput {
  accountIds?: string[]
  regions?: string[]
  states?: string[]
  search?: string
  includeTerminated?: boolean
}

export type InstanceAction = 'START' | 'SOFTSTOP' | 'SOFTRESET' | 'STOP' | 'RESET'

export const instances = {
  list: (filter: InstanceFilterInput = {}) =>
    http.get<{ instances: D.InstanceDTO[]; sync: D.SyncStatusDTO }>('/api/instances', {
      query: {
        accountIds: filter.accountIds?.join(','),
        regions: filter.regions?.join(','),
        states: filter.states?.join(','),
        search: filter.search,
        includeTerminated: filter.includeTerminated ? 'true' : undefined
      }
    }),

  detail: (id: string) => http.get<D.InstanceDetailDTO>(`/api/instances/${encodeURIComponent(id)}`),

  sync: (accountId?: string) =>
    http.post<D.SyncReportDTO>('/api/instances/sync', undefined, { query: { accountId } }),

  /**
   * 返回的 lifecycleState 一定是过渡态（STARTING/STOPPING），不是终态。
   * 落定由 SSE 推送——这是后端刻意保证的语义，不要在前端"猜"成终态。
   */
  action: (id: string, action: InstanceAction, force = false) =>
    http.post<D.InstanceDTO>(
      `/api/instances/${encodeURIComponent(id)}/actions/${action}`,
      undefined,
      { query: { force: force ? 'true' : undefined } }
    ),

  rename: (id: string, displayName: string) =>
    http.patch<D.InstanceDTO>(`/api/instances/${encodeURIComponent(id)}`, { displayName }),

  setNote: (id: string, note: string) =>
    http.patch<D.InstanceDTO>(`/api/instances/${encodeURIComponent(id)}/note`, { note }),

  reshape: (id: string, ocpus: number, memoryInGbs: number) =>
    http.post<D.InstanceDTO>(`/api/instances/${encodeURIComponent(id)}/reshape`, { ocpus, memoryInGbs }),

  /** 终止需回传实例名。preserveBootVolume 决定引导卷是否保留。 */
  terminate: (id: string, confirmName: string, preserveBootVolume = false) =>
    http.del<{ ok: boolean }>(`/api/instances/${encodeURIComponent(id)}`, {
      query: {
        confirm: confirmName,
        preserveBootVolume: preserveBootVolume ? 'true' : undefined
      }
    }),

  dismissError: (id: string) =>
    http.post<{ ok: boolean }>(`/api/instances/${encodeURIComponent(id)}/dismiss-error`),

  changeIp: (id: string) =>
    http.post<D.ChangeIpDTO>(`/api/instances/${encodeURIComponent(id)}/change-ip`, undefined, {
      query: { confirm: 'true' }
    }),

  enableIpv6: (id: string) =>
    http.post<D.EnableIpv6DTO>(`/api/instances/${encodeURIComponent(id)}/enable-ipv6`),

  metrics: (id: string, hours = 6, metrics?: string[]) =>
    http.get<D.MetricsDTO>(`/api/instances/${encodeURIComponent(id)}/metrics`, {
      query: { hours, metrics: metrics?.join(',') }
    }),

  // publicKey 是必传的：Oracle 用它给串行控制台鉴权。
  // 这个参数一度漏掉，后端收到空 body 只能报「请求内容格式有误: EOF」——
  // 而前端明明刚校验过公钥非空，报错信息和实际原因对不上，很难查。
  console: (id: string, publicKey: string) =>
    http.post<D.ConsoleConnectionDTO>(
      `/api/instances/${encodeURIComponent(id)}/console`,
      { publicKey }
    ),

  bulk: (ids: string[], action: InstanceAction, force = false) =>
    http.post<{ results: { instanceId: string; ok: boolean; error?: string }[] }>(
      '/api/instances/bulk', { instanceIds: ids, action, force }
    )
}

/* ---------- 创建实例 ---------- */

export interface LaunchInput {
  accountId: string
  region: string
  availabilityDomain?: string
  displayName: string
  shape: string
  ocpus: number
  memoryInGbs: number
  imageId: string
  bootVolumeGb?: number
  subnetId?: string
  autoCreateNetwork?: boolean
  assignPublicIp?: boolean
  enableIpv6?: boolean
  sshPublicKey?: string
  cloudInit?: string
}

export const launch = {
  presets: () => http.get<{ presets: D.LaunchPresetDTO[] }>('/api/launch/presets'),

  availabilityDomains: (accountId: string, region?: string) =>
    http.get<{ availabilityDomains: D.AvailabilityDomainDTO[] }>('/api/launch/availability-domains', {
      query: { accountId, region }
    }),

  shapes: (accountId: string, region?: string, availabilityDomain?: string) =>
    http.get<{ shapes: D.ShapeDTO[] }>('/api/launch/shapes', {
      query: { accountId, region, availabilityDomain }
    }),

  /** shape 过滤是必要的：ARM 与 x86 的镜像不通用。 */
  images: (accountId: string, region?: string, shape?: string, os?: string) =>
    http.get<{ images: D.ImageDTO[] }>('/api/launch/images', {
      query: { accountId, region, shape, os }
    }),

  create: (input: LaunchInput) => http.post<D.LaunchResultDTO>('/api/launch', input)
}

/* ---------- 网络 ---------- */

export const network = {
  vcns: (accountId: string, region?: string) =>
    http.get<{ vcns: D.VcnDTO[] }>('/api/network/vcns', { query: { accountId, region } }),

  subnets: (accountId: string, region?: string, vcnId?: string) =>
    http.get<{ subnets: D.SubnetDTO[] }>('/api/network/subnets', { query: { accountId, region, vcnId } }),

  securityLists: (accountId: string, region?: string, vcnId?: string) =>
    http.get<{ securityLists: D.SecurityListDTO[] }>('/api/network/security-lists', {
      query: { accountId, region, vcnId }
    }),

  /**
   * 整体替换语义，不是增量追加。
   * 必须先读出完整规则集再整体提交，否则未提交的规则会被静默删掉。
   */
  updateSecurityList: (
    id: string, accountId: string, region: string,
    ingress: D.IngressRuleDTO[], egress: D.EgressRuleDTO[]
  ) => http.put<D.SecurityListDTO>(`/api/network/security-lists/${id}`, { ingress, egress }, {
    query: { accountId, region }
  }),

  ruleTemplates: () => http.get<{ templates: D.RuleTemplateDTO[] }>('/api/network/rule-templates'),

  publicIps: (accountId: string, region?: string, scope?: string) =>
    http.get<{ publicIps: D.PublicIpDTO[] }>('/api/network/public-ips', {
      query: { accountId, region, scope }
    }),

  ensure: (accountId: string, region?: string, ipv6 = false) =>
    http.post<D.EnsureNetworkDTO>('/api/network/ensure', undefined, {
      query: { accountId, region, ipv6: ipv6 ? 'true' : undefined }
    })
}

/* ---------- 存储 ---------- */

export const storage = {
  bootVolumes: (accountId: string, region?: string, availabilityDomain?: string) =>
    http.get<{ bootVolumes: D.BootVolumeDTO[] }>('/api/storage/boot-volumes', {
      query: { accountId, region, availabilityDomain }
    }),

  updateBootVolume: (
    id: string, accountId: string, region: string,
    patch: { displayName?: string; sizeInGbs?: number; vpusPerGb?: number }
  ) => http.patch<{ bootVolume: D.BootVolumeDTO; notice: string }>(
    `/api/storage/boot-volumes/${id}`, patch, { query: { accountId, region } }
  ),

  volumes: (accountId: string, region?: string) =>
    http.get<{ volumes: D.BootVolumeDTO[] }>('/api/storage/volumes', { query: { accountId, region } }),

  updateVolume: (
    id: string, accountId: string, region: string,
    patch: { displayName?: string; sizeInGbs?: number; vpusPerGb?: number }
  ) => http.patch<{ volume: D.BootVolumeDTO; notice: string }>(
    `/api/storage/volumes/${id}`, patch, { query: { accountId, region } }
  ),

  detachBootVolume: (attachmentId: string, accountId: string, region: string) =>
    http.post<{ ok: boolean }>('/api/storage/boot-volume-attachments/detach', undefined, {
      query: { accountId, region, attachmentId }
    }),

  attachBootVolume: (accountId: string, region: string, instanceId: string, bootVolumeId: string) =>
    http.post<{ ok: boolean }>('/api/storage/boot-volume-attachments/attach', undefined, {
      query: { accountId, region, instanceId, bootVolumeId }
    }),

  /**
   * 把卷挂到实例上当**数据盘**（不是引导卷）。
   * volumeId 可以是引导卷的 OCID——救援模式就是这么把坏机器的系统盘挂到好机器上改文件的。
   */
  attachVolume: (
    accountId: string, region: string, instanceId: string, volumeId: string, displayName?: string
  ) => http.post<{ ok: boolean; notice: string }>('/api/storage/volume-attachments/attach', undefined, {
    query: { accountId, region, instanceId, volumeId, displayName }
  }),

  detachVolume: (attachmentId: string, accountId: string, region: string) =>
    http.post<{ ok: boolean; notice: string }>('/api/storage/volume-attachments/detach', undefined, {
      query: { accountId, region, attachmentId }
    })
}

/* ---------- 容量守候（抢机） ---------- */

export const hunt = {
  list: () => http.get<{ tasks: D.HuntTaskDTO[]; limits: D.HuntLimitsDTO }>('/api/hunt'),

  create: (input: D.CreateHuntInput) =>
    http.post<{ task: D.HuntTaskDTO; notice: string }>('/api/hunt', input),

  pause: (id: string) => http.post<{ task: D.HuntTaskDTO }>(`/api/hunt/${id}/pause`),
  resume: (id: string) => http.post<{ task: D.HuntTaskDTO }>(`/api/hunt/${id}/resume`),
  remove: (id: string) => http.del<{ ok: boolean; notice: string }>(`/api/hunt/${id}`)
}

/* ---------- 容量监控 ---------- */

export const capacity = {
  list: () => http.get<{ watches: D.CapacityWatchDTO[]; probeIntervalSeconds: number }>('/api/capacity'),

  /** 立刻查一次，不落库。手动查询用。 */
  probe: (input: D.CapacityProbeInput) =>
    http.post<D.CapacityProbeResult>('/api/capacity/probe', input),

  create: (input: D.CapacityProbeInput) =>
    http.post<{ watch: D.CapacityWatchDTO; notice: string }>('/api/capacity', input),

  enable: (id: string) => http.post<{ watch: D.CapacityWatchDTO }>(`/api/capacity/${id}/enable`),
  disable: (id: string) => http.post<{ watch: D.CapacityWatchDTO }>(`/api/capacity/${id}/disable`),
  remove: (id: string) => http.del<{ ok: boolean }>(`/api/capacity/${id}`)
}

/* ---------- 配额 / 总览 / 审计 ---------- */

export const insights = {
  overview: () => http.get<D.OverviewDTO>('/api/overview'),

  quota: (accountId?: string, refresh = false) =>
    http.get<{ quotas: D.AccountQuotaDTO[] }>('/api/quota', {
      query: { accountId, refresh: refresh ? 'true' : undefined }
    }),

  audit: (filter: { accountId?: string; action?: string; limit?: number; beforeId?: number } = {}) =>
    http.get<{ entries: D.AuditEntryDTO[]; hasMore: boolean; total?: number }>(
      '/api/audit', { query: filter }),

  regions: () => http.get<{ regions: string[] }>('/api/regions')
}

/* ---------- 通知与设置 ---------- */

export const notifications = {
  list: () => http.get<{ channels: D.ChannelDTO[]; kinds: D.ChannelKindDTO[] }>('/api/notifications/channels'),

  events: () => http.get<{ events: D.NotifyEventDTO[] }>('/api/notifications/events'),

  create: (input: { kind: string; name: string; config: Record<string, string>; events: string[] }) =>
    http.post<D.ChannelDTO>('/api/notifications/channels', input),

  update: (id: string, patch: {
    name?: string; config?: Record<string, string>; events?: string[]; enabled?: boolean
  }) => http.patch<D.ChannelDTO>(`/api/notifications/channels/${id}`, patch),

  remove: (id: string) => http.del<{ ok: boolean }>(`/api/notifications/channels/${id}`),

  /** 始终返回 200，成功与否看 ok 字段。 */
  test: (id: string) => http.post<{ ok: boolean; error?: string }>(`/api/notifications/channels/${id}/test`)
}

export const settings = {
  get: () => http.get<D.SettingsDTO>('/api/settings'),

  update: (patch: Partial<D.SettingsDTO>) =>
    http.patch<{ settings: D.SettingsDTO; notice: string }>('/api/settings', patch)
}

/* ---------- 账单 ---------- */

export const billing = {
  list: (refresh = false) =>
    http.get<D.BillingListDTO>('/api/billing', {
      query: { refresh: refresh ? 'true' : undefined }
    }),

  detail: (accountId: string, days = 30, refresh = false) =>
    http.get<D.BillingDetailDTO>(`/api/billing/${accountId}`, {
      query: { days, refresh: refresh ? 'true' : undefined }
    })
}

/* ---------- 代理池 ---------- */

export const proxies = {
  list: () => http.get<D.ProxyListDTO>('/api/proxies'),

  /** dryRun 为真时只解析不落库，用于导入前预览 */
  import: (text: string, dryRun = false) =>
    http.post<D.ProxyImportResultDTO>('/api/proxies/import', { text, dryRun }),

  update: (id: string, patch: { label?: string; enabled?: boolean; password?: string }) =>
    http.patch<{ proxy: D.ProxyDTO }>(`/api/proxies/${id}`, patch),

  remove: (id: string) => http.del<{ ok: boolean }>(`/api/proxies/${id}`),

  /** proxyId 为空串表示解绑，回到本机直连 */
  bind: (accountId: string, proxyId: string) =>
    http.post<{ ok: boolean }>('/api/proxies/bind', { accountId, proxyId }),

  /** 不传 id 时检测全部 */
  check: (id?: string) =>
    http.post<{ results: D.ProxyCheckRowDTO[] }>(
      id ? `/api/proxies/${id}/check` : '/api/proxies/check')
}

/* ---------- 身份域密码策略 ---------- */

export const passwordPolicy = {
  get: (accountId: string) =>
    http.get<D.PasswordPolicyResultDTO>(`/api/accounts/${accountId}/password-policy`),

  /** days 为 null 且 disable 为真时尝试取消过期 */
  set: (accountId: string, input: { days?: number | null; disable?: boolean }) =>
    http.patch<D.PasswordPolicyResultDTO>(
      `/api/accounts/${accountId}/password-policy`, input)
}
