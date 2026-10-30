/**
 * 演示站的「后端」：在浏览器内存里模拟全部接口，供 GitHub Pages 上的演示站使用。
 *
 * 只在 VITE_DEMO=true 的构建里被动态加载（见 api/http.ts、api/events.ts），
 * 正常构建会把这段代码整个摇掉，发版的二进制与镜像里没有它。
 *
 * 约定：
 *   - 所有数据都是编的。公网 IP 用文档保留段（203.0.113.0/24、198.51.100.0/24、
 *     2001:db8::/32），OCID 都带 demo 字样，不会撞上任何真实的人。
 *   - 写操作只改内存；刷新页面回到初始数据。
 *   - 长耗时操作走与真实后端相同的「任务 + SSE 事件」路径，进度与状态变化都会推送，
 *     页面上的过渡、提示、任务页都能完整走一遍。
 *   - 没有实现的接口统一返回 demo_unsupported，页面会提示而不是白屏。
 */
import { ApiError, AUTH_REQUIRED_EVENT } from '@/api/http'
import type { ServerEvent, ServerEventType, EventHandlers } from '@/api/events'
import type * as D from '@/api/types'
import { STATIC } from './static'

/* ---------- 小工具 ---------- */

const NOW = Date.now()
const iso = (ms: number) => new Date(ms).toISOString()
const ago = (ms: number) => iso(Date.now() - ms)
const MIN = 60_000
const HOUR = 60 * MIN
const DAY = 24 * HOUR
const sleep = (ms: number) => new Promise(r => setTimeout(r, ms))
const rand = (min: number, max: number) => min + Math.random() * (max - min)
let seq = 1000
const uid = (prefix: string) => `${prefix}${(++seq).toString(36)}${Math.random().toString(36).slice(2, 7)}`
const ocid = (kind: string, region: string) => `ocid1.${kind}.oc1.${region}.demo${uid('')}`

function fail(status: number, code: string, message: string): never {
  throw new ApiError(status, { code, message })
}
const unsupported = (what = '这个操作') =>
  fail(501, 'demo_unsupported', `演示站不支持${what}。下载部署后即可使用完整功能。`)

/** 文档保留段里挑一个没用过的地址 */
const usedIps = new Set<string>()
function freshIp(): string {
  for (;;) {
    const net = Math.random() < 0.5 ? '203.0.113' : '198.51.100'
    const ip = `${net}.${Math.floor(rand(10, 250))}`
    if (!usedIps.has(ip)) { usedIps.add(ip); return ip }
  }
}
const freshV6 = () => `2001:db8:${Math.floor(rand(4096, 65535)).toString(16)}:a${Math.floor(rand(0, 9))}00::${Math.floor(rand(16, 4095)).toString(16)}`

/* ---------- 账号 ---------- */

interface Acct extends D.AccountDTO { proxyId?: string }

function account(id: string, alias: string, code: string, color: number, home: string, regions: string[],
  paymentModel: string, extra: Partial<Acct> = {}): Acct {
  return {
    id, alias, code, colorIndex: color,
    tenancyOcid: `ocid1.tenancy.oc1..demo${id}`, userOcid: `ocid1.user.oc1..demo${id}`,
    fingerprint: 'de:mo:00:11:22:33:44:55:66:77:88:99:aa:bb:cc:dd',
    defaultRegion: home, compartmentOcid: '', enabled: true,
    status: 'ok', statusMessage: '', lastCheckedAt: ago(2 * HOUR),
    paymentModel, subscriptionState: 'ACTIVE',
    subscriptionStartsAt: ago(320 * DAY), subscriptionEndsAt: null,
    subscribedRegions: regions, homeRegion: home,
    email: `${code.toLowerCase()}@example.com`, tenancyName: `demo-${code.toLowerCase()}`,
    createdAt: ago(200 * DAY), updatedAt: ago(2 * HOUR),
    ...extra
  }
}

const accounts: Acct[] = [
  account('acc-tok', '东京主号', 'TOK', 0, 'ap-tokyo-1', ['ap-tokyo-1', 'ap-osaka-1'], 'PAY_AS_YOU_GO'),
  account('acc-sin', '新加坡', 'SIN', 1, 'ap-singapore-1', ['ap-singapore-1'], 'FREE_TRIAL',
    { subscriptionStartsAt: ago(410 * DAY) }),
  account('acc-pho', '凤凰城试用', 'PHO', 2, 'us-phoenix-1', ['us-phoenix-1'], 'FREE_TRIAL',
    { subscriptionStartsAt: ago(18 * DAY), subscriptionEndsAt: iso(NOW + 12 * DAY) }),
  account('acc-fra', '法兰克福', 'FRA', 3, 'eu-frankfurt-1', ['eu-frankfurt-1'], 'FREE_TRIAL', {
    status: 'error', lastCheckedAt: ago(40 * MIN),
    statusMessage: 'NotAuthenticated · API 密钥已被删除或指纹不匹配（演示数据）'
  })
]
const acctById = (id: string) => accounts.find(a => a.id === id) ?? fail(404, 'not_found', '账号不存在')

/* ---------- 网络（每个账号 × 区域一套） ---------- */

interface Net {
  accountId: string; region: string
  vcn: D.VcnDTO; subnets: D.SubnetDTO[]; securityList: D.SecurityListDTO
  igw: { id: string; name: string; isEnabled: boolean } | null
}
const nets: Net[] = []

function makeNet(accountId: string, region: string, name = 'ocicore-vcn', withGw = true, v6 = true): Net {
  const vcnId = ocid('vcn', region)
  const slId = ocid('securitylist', region)
  const n: Net = {
    accountId, region,
    vcn: {
      id: vcnId, displayName: name, cidrBlock: '10.0.0.0/16',
      ipv6CidrBlocks: v6 ? [`2001:db8:${Math.floor(rand(4096, 65535)).toString(16)}::/56`] : null,
      lifecycleState: 'AVAILABLE', defaultSecurityListId: slId, defaultRouteTableId: ocid('routetable', region)
    },
    subnets: [{
      id: ocid('subnet', region), vcnId, displayName: name.replace('vcn', 'subnet'), cidrBlock: '10.0.0.0/24',
      ipv6CidrBlock: v6 ? '2001:db8:1::/64' : '', availabilityDomain: '', prohibitPublicIpOnVnic: false,
      lifecycleState: 'AVAILABLE'
    }],
    securityList: {
      id: slId, vcnId, displayName: `Default Security List for ${name}`, lifecycleState: 'AVAILABLE',
      ingressSecurityRules: [
        { protocol: '6', source: '0.0.0.0/0', tcpOptions: { destinationPortRange: { min: 22, max: 22 } }, description: 'SSH' },
        { protocol: '6', source: '0.0.0.0/0', tcpOptions: { destinationPortRange: { min: 443, max: 443 } }, description: 'HTTPS' },
        { protocol: '1', source: '0.0.0.0/0', icmpOptions: { type: 3, code: 4 } }
      ],
      egressSecurityRules: [{ protocol: 'all', destination: '0.0.0.0/0' }],
      allowAllRules: [], noEgress: false
    },
    igw: withGw ? { id: ocid('internetgateway', region), name: 'ocicore-igw', isEnabled: true } : null
  }
  nets.push(n)
  return n
}

for (const a of accounts) for (const r of a.subscribedRegions ?? []) makeNet(a.id, r)
// 一个历史遗留、没有互联网网关的 VCN：资源页「待处理」会提示它。
makeNet('acc-tok', 'ap-osaka-1', 'vcn-legacy', false, false)

const netFor = (accountId: string, region: string) =>
  nets.find(n => n.accountId === accountId && n.region === region && n.igw) ??
  nets.find(n => n.accountId === accountId && n.region === region) ?? makeNet(accountId, region)

/* ---------- 实例 ---------- */

interface Inst {
  dto: D.InstanceDTO
  vnics: D.VnicInfoDTO[]
  blockVolumes: D.VolumeInfoDTO[]
  bootVpus: number
}
const instances: Inst[] = []

const AD = (region: string, n = 1) => `Demo:${region.toUpperCase()}-AD-${n}`

function makeInstance(opts: {
  accountId: string; region: string; name: string; shape: string; ocpus: number; memoryGb: number
  state?: D.LifecycleState; ipv6?: boolean; os?: string; bootGb?: number; createdDaysAgo?: number
  note?: string; baseline?: string; secondary?: boolean; lastError?: string
}): Inst {
  const a = acctById(opts.accountId)
  const net = netFor(opts.accountId, opts.region)
  const id = ocid('instance', opts.region)
  const ip = freshIp()
  const v6 = opts.ipv6 ? [freshV6()] : []
  const state = opts.state ?? 'RUNNING'
  const primary: D.VnicInfoDTO = {
    attachmentId: ocid('vnicattachment', opts.region), vnicId: ocid('vnic', opts.region),
    displayName: opts.name, isPrimary: true, nicIndex: 0,
    privateIp: `10.0.0.${Math.floor(rand(10, 250))}`, publicIp: state === 'STOPPED' && Math.random() < 0.3 ? '' : ip,
    publicIpId: ocid('publicip', opts.region), publicIpType: 'EPHEMERAL', privateIpId: ocid('privateip', opts.region),
    ipv6: v6, macAddress: '02:00:17:' + Array.from({ length: 3 }, () => Math.floor(rand(16, 255)).toString(16)).join(':').toUpperCase(),
    subnetId: net.subnets[0].id, subnetName: net.subnets[0].displayName, vcnId: net.vcn.id, vcnName: net.vcn.displayName
  }
  const vnics = [primary]
  if (opts.secondary) {
    vnics.push({
      ...primary, attachmentId: ocid('vnicattachment', opts.region), vnicId: ocid('vnic', opts.region),
      displayName: `${opts.name}-nic2`, isPrimary: false, privateIp: `10.0.0.${Math.floor(rand(10, 250))}`,
      publicIp: freshIp(), publicIpId: ocid('publicip', opts.region), ipv6: [freshV6()],
      macAddress: '02:00:17:0A:' + Math.floor(rand(16, 255)).toString(16).toUpperCase() + ':3C'
    })
  }
  const created = NOW - (opts.createdDaysAgo ?? 30) * DAY
  const inst: Inst = {
    dto: {
      id, accountId: a.id, region: opts.region, compartmentId: a.tenancyOcid,
      displayName: opts.name, availabilityDomain: AD(opts.region), faultDomain: 'FAULT-DOMAIN-1',
      shape: opts.shape, ocpus: opts.ocpus, memoryGb: opts.memoryGb,
      baselineOcpuUtilization: opts.baseline ?? '', lifecycleState: state,
      imageId: ocid('image', opts.region), imageOs: opts.os ?? 'Canonical Ubuntu',
      publicIp: primary.publicIp, privateIp: primary.privateIp, ipv6: v6.join(','),
      secondaryIpv4: '', secondaryIpv6: '',
      vnicId: primary.vnicId, subnetId: primary.subnetId,
      bootVolumeId: ocid('bootvolume', opts.region), bootVolumeGb: opts.bootGb ?? 50, bootVolumeVpus: 10,
      timeCreated: iso(created), runningSince: state === 'RUNNING' ? iso(Math.max(created, NOW - rand(2, 40) * DAY)) : null,
      note: opts.note ?? '', syncedAt: ago(3 * MIN), lastError: opts.lastError ?? '',
      accountAlias: a.alias, accountCode: a.code, accountColorIndex: a.colorIndex
    },
    vnics, blockVolumes: [], bootVpus: 10
  }
  syncSecondary(inst)
  instances.push(inst)
  return inst
}

function syncSecondary(i: Inst) {
  const sec = i.vnics.filter(v => !v.isPrimary)
  i.dto.secondaryIpv4 = sec.map(v => v.publicIp).filter(Boolean).join(',')
  i.dto.secondaryIpv6 = sec.flatMap(v => v.ipv6 ?? []).join(',')
  const p = i.vnics.find(v => v.isPrimary)
  if (p) { i.dto.publicIp = p.publicIp; i.dto.privateIp = p.privateIp; i.dto.ipv6 = (p.ipv6 ?? []).join(',') }
}

const web = makeInstance({ accountId: 'acc-tok', region: 'ap-tokyo-1', name: 'tok-web', shape: 'VM.Standard.A1.Flex',
  ocpus: 4, memoryGb: 24, ipv6: true, bootGb: 100, secondary: true, createdDaysAgo: 120, note: '主站 · Nginx + Docker' })
makeInstance({ accountId: 'acc-tok', region: 'ap-tokyo-1', name: 'tok-relay', shape: 'VM.Standard.E2.1.Micro',
  ocpus: 1, memoryGb: 1, createdDaysAgo: 90, note: '跳板机' })
makeInstance({ accountId: 'acc-tok', region: 'ap-tokyo-1', name: 'tok-backup', shape: 'VM.Standard.E2.1.Micro',
  ocpus: 1, memoryGb: 1, state: 'STOPPED', createdDaysAgo: 60, os: 'Oracle Linux' })
makeInstance({ accountId: 'acc-tok', region: 'ap-osaka-1', name: 'osaka-edge', shape: 'VM.Standard.A1.Flex',
  ocpus: 1, memoryGb: 6, ipv6: true, createdDaysAgo: 45 })
makeInstance({ accountId: 'acc-sin', region: 'ap-singapore-1', name: 'sg-arm', shape: 'VM.Standard.A1.Flex',
  ocpus: 2, memoryGb: 12, ipv6: true, createdDaysAgo: 300, os: 'Debian' })
makeInstance({ accountId: 'acc-sin', region: 'ap-singapore-1', name: 'sg-micro', shape: 'VM.Standard.E2.1.Micro',
  ocpus: 1, memoryGb: 1, baseline: '', createdDaysAgo: 280,
  lastError: '软重启超时：实例 10 分钟内没有回到 RUNNING（演示数据）' })
makeInstance({ accountId: 'acc-pho', region: 'us-phoenix-1', name: 'pho-lab', shape: 'VM.Standard.E4.Flex',
  ocpus: 1, memoryGb: 8, baseline: 'BASELINE_1_8', createdDaysAgo: 10, note: '试用期测试，到期前删掉' })
makeInstance({ accountId: 'acc-pho', region: 'us-phoenix-1', name: 'pho-micro', shape: 'VM.Standard.E2.1.Micro',
  ocpus: 1, memoryGb: 1, state: 'STOPPED', createdDaysAgo: 15 })
makeInstance({ accountId: 'acc-fra', region: 'eu-frankfurt-1', name: 'fra-old', shape: 'VM.Standard.A1.Flex',
  ocpus: 1, memoryGb: 6, state: 'STOPPED', createdDaysAgo: 500, os: 'Oracle Linux' })

web.blockVolumes.push({
  volumeId: ocid('volume', 'ap-tokyo-1'), attachmentId: ocid('volumeattachment', 'ap-tokyo-1'),
  displayName: 'data-01', sizeInGbs: 100, vpusPerGb: 10, device: '/dev/oracleoci/oraclevdb', isReadOnly: false, state: 'ATTACHED'
})

const instById = (id: string) => instances.find(i => i.dto.id === id) ?? fail(404, 'not_found', '实例不存在或尚未同步')

/* ---------- 未挂载的卷、保留 IP、控制台连接（资源页用） ---------- */

interface LooseVolume { accountId: string; vol: D.ResourceVolumeDTO }
const looseVolumes: LooseVolume[] = [
  { accountId: 'acc-tok', vol: {
    id: ocid('bootvolume', 'ap-tokyo-1'), kind: 'boot', displayName: 'instance-20260611-0912 (Boot Volume)',
    region: 'ap-tokyo-1', availabilityDomain: AD('ap-tokyo-1'), sizeInGbs: 47, vpusPerGb: 10,
    lifecycleState: 'AVAILABLE', timeCreated: ago(140 * DAY), attachedTo: [] } },
  { accountId: 'acc-sin', vol: {
    id: ocid('volume', 'ap-singapore-1'), kind: 'block', displayName: 'backup-old',
    region: 'ap-singapore-1', availabilityDomain: AD('ap-singapore-1'), sizeInGbs: 50, vpusPerGb: 0,
    lifecycleState: 'AVAILABLE', timeCreated: ago(200 * DAY), attachedTo: [] } }
]

interface Reserved { accountId: string; ip: D.ResourcePublicIpDTO }
const reservedIps: Reserved[] = [
  { accountId: 'acc-tok', ip: {
    id: ocid('publicip', 'ap-tokyo-1'), ipAddress: freshIp(), region: 'ap-tokyo-1', lifetime: 'RESERVED',
    lifecycleState: 'AVAILABLE', displayName: 'web-reserved', assigned: false } }
]

interface Console { accountId: string; conn: D.ResourceConsoleDTO }
const consoles: Console[] = [
  { accountId: 'acc-tok', conn: {
    id: ocid('instanceconsoleconnection', 'ap-tokyo-1'), region: 'ap-tokyo-1', lifecycleState: 'ACTIVE',
    fingerprint: 'a3:9f:12:7c:5e:02:bd:41:90:aa:63:f8:1e:2d:70:c4', instance: { instanceId: web.dto.id, name: web.dto.displayName } } }
]

/* ---------- 其余 ---------- */

let settings: D.SettingsDTO = { ...STATIC.settings }
let authed = true
const USER = 'demo'

const audit: D.AuditEntryDTO[] = []
let auditSeq = 100
function log(action: string, target = '', accountId = '', detail = '', result = 'ok', at = Date.now()) {
  audit.unshift({ id: ++auditSeq, userId: 'u-demo', action, accountId, target, detail, ip: '127.0.0.1', result, createdAt: iso(at) })
}
// 种一些历史记录，老的在前、新的在后，unshift 之后最新的在最上面。
;([
  [9 * DAY, 'setup', USER, ''], [9 * DAY - 5 * MIN, 'account_create', '东京主号', 'acc-tok'],
  [8 * DAY, 'account_create', '新加坡', 'acc-sin'], [6 * DAY, 'instance_launch', 'tok-web', 'acc-tok'],
  [5 * DAY, 'security_list_update', 'Default Security List for ocicore-vcn', 'acc-tok'],
  [3 * DAY, 'account_create', '凤凰城试用', 'acc-pho'], [2 * DAY, 'instance_change_ip', 'osaka-edge', 'acc-tok'],
  [30 * HOUR, 'instance_enable_ipv6', 'sg-arm', 'acc-sin'], [20 * HOUR, 'instance_attach_vnic', 'tok-web', 'acc-tok'],
  [6 * HOUR, 'account_check_auto', '法兰克福', 'acc-fra', 'fail'], [3 * HOUR, 'instance_softreset', 'sg-micro', 'acc-sin'],
  [2 * HOUR, 'console_create', 'tok-web', 'acc-tok'], [25 * MIN, 'login', USER, '']
] as [number, string, string, string, string?][]).forEach(([t, action, target, acc, res]) =>
  log(action, target, acc, '', res ?? 'ok', NOW - t))

const channels: D.ChannelDTO[] = [{
  id: 'ch-1', kind: 'telegram', name: '我的 Telegram',
  config: { token: '1234••••••••••••WXYZ', chatId: '10086' },
  events: ['account.auth', 'instance.created', 'danger.operation', 'hunt.succeeded'],
  enabled: true, lastError: '', lastSentAt: ago(2 * HOUR), createdAt: ago(9 * DAY), updatedAt: ago(9 * DAY)
}]

const proxies: D.ProxyDTO[] = [
  { id: 'px-1', label: '东京家宽', scheme: 'socks5', host: 'proxy-a.example.net', port: 1080, username: 'demo', hasPassword: true,
    enabled: true, lastStatus: 'ok', lastLatencyMs: 86, lastError: '', lastRegion: 'ap-tokyo-1',
    lastCheckedAt: ago(20 * MIN), lastOkAt: ago(20 * MIN), createdAt: ago(8 * DAY), updatedAt: ago(20 * MIN) },
  { id: 'px-2', label: '新加坡机房', scheme: 'http', host: 'proxy-b.example.net', port: 3128, username: '', hasPassword: false,
    enabled: true, lastStatus: 'ok', lastLatencyMs: 142, lastError: '', lastRegion: 'ap-singapore-1',
    lastCheckedAt: ago(20 * MIN), lastOkAt: ago(20 * MIN), createdAt: ago(8 * DAY), updatedAt: ago(20 * MIN) },
  { id: 'px-3', label: '', scheme: 'socks5', host: 'proxy-c.example.net', port: 1080, username: '', hasPassword: false,
    enabled: false, lastStatus: 'fail', lastLatencyMs: 0, lastError: '连接超时（演示数据）', lastRegion: 'us-phoenix-1',
    lastCheckedAt: ago(3 * HOUR), lastOkAt: ago(4 * DAY), createdAt: ago(8 * DAY), updatedAt: ago(3 * HOUR) }
]
const bindings: Record<string, string> = { 'acc-tok': 'px-1', 'acc-sin': 'px-2' }

const huntTasks: D.HuntTaskDTO[] = [
  { id: 'hunt-1', accountId: 'acc-sin', region: 'ap-singapore-1', name: '新加坡 ARM 2C12G', state: 'running',
    attempts: 1873, intervalSeconds: 60, precheckCapacity: true, lastClass: 'OutOfCapacity',
    lastError: '容量报告显示没有余量，本轮跳过（未发创建请求）', lastAd: 'AD-1', lastTryAt: ago(40_000), nextAt: iso(NOW + 20_000),
    instanceId: '', maxAttempts: 0, expiresAt: iso(NOW + 5 * DAY), createdAt: ago(31 * HOUR), updatedAt: ago(40_000),
    shape: 'VM.Standard.A1.Flex', ocpus: 2, memoryGb: 12, displayName: 'sg-arm-2', ads: null },
  { id: 'hunt-2', accountId: 'acc-tok', region: 'ap-tokyo-1', name: '东京 ARM 满配', state: 'succeeded',
    attempts: 412, intervalSeconds: 60, precheckCapacity: true, lastClass: '', lastError: '', lastAd: 'AD-1',
    lastTryAt: ago(6 * DAY), nextAt: '', instanceId: web.dto.id, maxAttempts: 0, expiresAt: '',
    createdAt: ago(7 * DAY), updatedAt: ago(6 * DAY), shape: 'VM.Standard.A1.Flex', ocpus: 4, memoryGb: 24,
    displayName: 'tok-web', ads: null }
]

const watches: D.CapacityWatchDTO[] = [
  { id: 'cw-1', accountId: 'acc-sin', region: 'ap-singapore-1', availabilityDomain: AD('ap-singapore-1'),
    availabilityDomainShort: 'AD-1', shape: 'VM.Standard.A1.Flex', ocpus: 2, memoryGb: 12, enabled: true,
    lastStatus: 'OUT_OF_HOST_CAPACITY', statusText: '没有容量', lastCount: 0, lastError: '',
    lastCheckedAt: ago(4 * MIN), lastChangedAt: ago(2 * DAY), createdAt: ago(5 * DAY), updatedAt: ago(4 * MIN) },
  { id: 'cw-2', accountId: 'acc-tok', region: 'ap-osaka-1', availabilityDomain: AD('ap-osaka-1'),
    availabilityDomainShort: 'AD-1', shape: 'VM.Standard.A1.Flex', ocpus: 4, memoryGb: 24, enabled: true,
    lastStatus: 'AVAILABLE', statusText: '有容量', lastCount: 3, lastError: '',
    lastCheckedAt: ago(4 * MIN), lastChangedAt: ago(9 * HOUR), createdAt: ago(5 * DAY), updatedAt: ago(4 * MIN) }
]

/* ---------- 事件总线与后台任务 ---------- */

const listeners = new Set<EventHandlers>()
function emit(type: ServerEventType, e: Partial<ServerEvent> = {}) {
  const event: ServerEvent = { type, at: iso(Date.now()), ...e }
  for (const h of listeners) h.onEvent?.(event)
}

export function demoConnect(handlers: EventHandlers): () => void {
  listeners.add(handlers)
  setTimeout(() => handlers.onOpen?.(false), 50)
  return () => { listeners.delete(handlers) }
}

const jobs: D.JobDTO[] = [
  { id: 'job-seed-1', kind: 'instance.change_ip', title: '更换公网 IP · osaka-edge', accountId: 'acc-tok',
    status: 'succeeded', progress: 100, summary: '新 IP 已生效', createdAt: ago(2 * DAY), startedAt: ago(2 * DAY),
    finishedAt: ago(2 * DAY - 40_000), steps: [{ at: ago(2 * DAY), text: '释放旧的临时 IP' }, { at: ago(2 * DAY - 20_000), text: '申请新的临时 IP' }] },
  { id: 'job-seed-2', kind: 'instance.sync', title: '手动同步', status: 'succeeded', progress: 100,
    summary: '4 个账号 · 6 个区域 · 9 台实例', createdAt: ago(5 * HOUR), startedAt: ago(5 * HOUR),
    finishedAt: ago(5 * HOUR - 8000), steps: [{ at: ago(5 * HOUR), text: '逐个区域拉取实例' }] }
]

interface JobPlan {
  kind: string; title: string; accountId?: string; instanceId?: string
  steps: string[]
  /** 整个任务大约跑多久 */
  ms?: number
  run: () => { summary?: string; result?: unknown } | void
  /** 在第几步之后执行 run（默认最后一步之后） */
}

function submit(plan: JobPlan): D.JobAcceptedDTO {
  const job: D.JobDTO = {
    id: uid('job-'), kind: plan.kind, title: plan.title, accountId: plan.accountId, instanceId: plan.instanceId,
    status: 'queued', progress: 0, steps: [], createdAt: iso(Date.now())
  }
  jobs.unshift(job)
  const total = plan.ms ?? 4000
  const per = total / (plan.steps.length + 1)
  const push = () => emit('job.updated', { data: structuredClone(job), accountId: job.accountId, instanceId: job.instanceId, state: job.status })
  void (async () => {
    await sleep(300)
    job.status = 'running'; job.startedAt = iso(Date.now())
    push()
    for (let n = 0; n < plan.steps.length; n++) {
      job.steps.push({ at: iso(Date.now()), text: plan.steps[n] })
      job.progress = Math.round(((n + 1) / (plan.steps.length + 1)) * 90)
      push()
      await sleep(per)
    }
    try {
      const out = plan.run() || {}
      job.status = 'succeeded'; job.progress = 100
      job.summary = out.summary; job.result = out.result
    } catch (err) {
      job.status = 'failed'
      job.error = err instanceof Error ? err.message : String(err)
    }
    job.finishedAt = iso(Date.now())
    push()
  })()
  return { job: structuredClone(job) }
}

function setState(i: Inst, state: D.LifecycleState) {
  i.dto.lifecycleState = state
  if (state === 'RUNNING') i.dto.runningSince = iso(Date.now())
  if (state !== 'RUNNING') i.dto.runningSince = null
  emit('instance.updated', { instanceId: i.dto.id, accountId: i.dto.accountId, state })
}

/** 开关机：先落到过渡态，几秒后落定，与真实后端同一个语义。 */
function transition(i: Inst, mid: D.LifecycleState, end: D.LifecycleState, ms = 4000) {
  setState(i, mid)
  setTimeout(() => {
    if (end === 'RUNNING' && !i.dto.publicIp && i.vnics[0]) { i.vnics[0].publicIp = freshIp(); syncSecondary(i) }
    setState(i, end)
  }, ms)
}

/* ---------- 汇总计算 ---------- */

const isArm = (s: string) => s.includes('.A1.')
const isMicro = (s: string) => s === 'VM.Standard.E2.1.Micro'
const liveInstances = () => instances.filter(i => i.dto.lifecycleState !== 'TERMINATED')

function quotaFor(a: Acct): D.AccountQuotaDTO {
  const mine = liveInstances().filter(i => i.dto.accountId === a.id && i.dto.region === a.homeRegion)
  const all = liveInstances().filter(i => i.dto.accountId === a.id)
  const paid = a.paymentModel === 'PAY_AS_YOU_GO'
  const ocpu = mine.filter(i => isArm(i.dto.shape)).reduce((n, i) => n + i.dto.ocpus, 0)
  const mem = mine.filter(i => isArm(i.dto.shape)).reduce((n, i) => n + i.dto.memoryGb, 0)
  const micro = mine.filter(i => isMicro(i.dto.shape)).length
  const block = all.reduce((n, i) => n + i.dto.bootVolumeGb + i.blockVolumes.reduce((m, v) => m + v.sizeInGbs, 0), 0) +
    looseVolumes.filter(v => v.accountId === a.id).reduce((n, v) => n + v.vol.sizeInGbs, 0)
  if (a.status === 'error') {
    return { accountId: a.id, region: a.homeRegion, items: null, error: 'NotAuthenticated（演示数据）', fetchedAt: iso(Date.now()) }
  }
  const BIG = 100_000_000
  return {
    accountId: a.id, region: a.homeRegion, fetchedAt: iso(Date.now()),
    items: [
      { key: 'ocpu', name: 'standard-a1-core-count', label: 'ARM OCPU', used: ocpu, limit: paid ? BIG : 2, known: true, unlimited: paid },
      { key: 'memory', name: 'standard-a1-memory-count', label: 'ARM 内存 (GB)', used: mem, limit: paid ? BIG : 12, known: true, unlimited: paid },
      { key: 'micro', name: 'vm-standard-e2-1-micro-count', label: 'AMD 微型实例', used: micro, limit: 2, known: true },
      { key: 'block', name: 'total-free-storage-gb-regional', label: '块存储 · 免费额度 (GB)', used: Math.min(block, 200), limit: 200, known: true }
    ]
  }
}

function resourcesFor(a: Acct, region?: string): D.AccountResourcesDTO {
  const regions = region ? [region] : (a.subscribedRegions ?? [a.homeRegion])
  const inRegion = (r: string) => regions.includes(r)
  const insts = liveInstances().filter(i => i.dto.accountId === a.id && inRegion(i.dto.region))
  const ref = (i: Inst) => ({ instanceId: i.dto.id, name: i.dto.displayName })

  const volumes: D.ResourceVolumeDTO[] = []
  for (const i of insts) {
    volumes.push({ id: i.dto.bootVolumeId, kind: 'boot', displayName: `${i.dto.displayName} (Boot Volume)`,
      region: i.dto.region, availabilityDomain: i.dto.availabilityDomain, sizeInGbs: i.dto.bootVolumeGb,
      vpusPerGb: i.dto.bootVolumeVpus, lifecycleState: 'AVAILABLE', timeCreated: i.dto.timeCreated, attachedTo: [ref(i)] })
    for (const b of i.blockVolumes) {
      volumes.push({ id: b.volumeId, kind: 'block', displayName: b.displayName, region: i.dto.region,
        availabilityDomain: i.dto.availabilityDomain, sizeInGbs: b.sizeInGbs, vpusPerGb: b.vpusPerGb,
        lifecycleState: 'AVAILABLE', timeCreated: i.dto.timeCreated, attachedTo: [ref(i)] })
    }
  }
  volumes.push(...looseVolumes.filter(v => v.accountId === a.id && inRegion(v.vol.region)).map(v => v.vol))

  const publicIps: D.ResourcePublicIpDTO[] = []
  for (const i of insts) for (const v of i.vnics) {
    if (!v.publicIp) continue
    publicIps.push({ id: v.publicIpId ?? v.vnicId, ipAddress: v.publicIp, region: i.dto.region, lifetime: 'EPHEMERAL',
      lifecycleState: 'ASSIGNED', displayName: '', assigned: true, instance: ref(i) })
  }
  publicIps.push(...reservedIps.filter(r => r.accountId === a.id && inRegion(r.ip.region)).map(r => r.ip))

  const vcns: D.ResourceVcnDTO[] = nets.filter(n => n.accountId === a.id && inRegion(n.region)).map(n => ({
    id: n.vcn.id, displayName: n.vcn.displayName, region: n.region, cidrBlocks: [n.vcn.cidrBlock],
    ipv6CidrBlocks: n.vcn.ipv6CidrBlocks ?? [], lifecycleState: 'AVAILABLE', subnets: n.subnets.length, internetGateway: n.igw
  }))

  const arm = insts.filter(i => isArm(i.dto.shape))
  const paid = a.paymentModel === 'PAY_AS_YOU_GO'
  return {
    accountId: a.id, regions,
    instances: insts.map(i => ({ id: i.dto.id, name: i.dto.displayName, region: i.dto.region, shape: i.dto.shape,
      lifecycleState: i.dto.lifecycleState, ocpus: i.dto.ocpus, memoryGb: i.dto.memoryGb, bootVolumeGb: i.dto.bootVolumeGb })),
    volumes, publicIps,
    consoleConnections: consoles.filter(c => c.accountId === a.id && inRegion(c.conn.region)).map(c => c.conn),
    vcns,
    usage: {
      armOcpus: arm.reduce((n, i) => n + i.dto.ocpus, 0), armMemoryGb: arm.reduce((n, i) => n + i.dto.memoryGb, 0),
      microInstances: insts.filter(i => isMicro(i.dto.shape)).length,
      storageGb: volumes.reduce((n, v) => n + v.sizeInGbs, 0)
    },
    freeTier: { tier: paid ? 'paid' : 'free', armOcpus: paid ? 4 : 2, armMemoryGb: paid ? 24 : 12,
      microInstances: 2, storageGb: 200, checkedAt: '2026-08-18' },
    quota: quotaFor(a),
    fetchedAt: iso(Date.now())
  }
}

function detailFor(i: Inst): D.InstanceDetailDTO {
  return {
    instance: structuredClone(i.dto),
    vnics: structuredClone(i.vnics),
    bootVolume: { id: i.dto.bootVolumeId, compartmentId: i.dto.compartmentId, displayName: `${i.dto.displayName} (Boot Volume)`,
      availabilityDomain: i.dto.availabilityDomain, sizeInGBs: i.dto.bootVolumeGb, vpusPerGB: i.dto.bootVolumeVpus,
      lifecycleState: 'AVAILABLE', imageId: i.dto.imageId, isHydrated: true, timeCreated: i.dto.timeCreated },
    bootVolumeAttachmentId: ocid('bootvolumeattachment', i.dto.region),
    blockVolumes: structuredClone(i.blockVolumes),
    metadata: { ssh_authorized_keys: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDemoKeyOnlyForTheDemoSite demo@example' },
    maxVnics: isMicro(i.dto.shape) ? 1 : Math.max(2, i.dto.ocpus)
  }
}

/** 一条平滑起伏的时间序列：基线 + 日内周期 + 噪声 */
function series(n: number, stepMs: number, base: number, amp: number, noise: number, end = Date.now()) {
  return Array.from({ length: n }, (_, k) => {
    const t = end - (n - 1 - k) * stepMs
    const v = base + amp * Math.sin((t / DAY) * 2 * Math.PI) + rand(-noise, noise)
    return { timestamp: iso(t), value: Math.max(0, v) }
  })
}

/* ---------- 路由 ---------- */

type Q = Record<string, string>
type Handler = (p: Record<string, string>, q: Q, body: any) => unknown
const routes: [string, RegExp, string[], Handler][] = []
function on(method: string, pattern: string, h: Handler) {
  const keys: string[] = []
  const re = new RegExp('^' + pattern.replace(/\{(\w+)\}/g, (_, k) => { keys.push(k); return '([^/]+)' }) + '$')
  routes.push([method, re, keys, h])
}

/* 认证 */
on('GET', '/api/status', () => ({ setupRequired: false, authenticated: authed, totpRequired: false, totpEnabled: false,
  username: authed ? USER : undefined, version: (import.meta.env.VITE_APP_VERSION as string) || 'demo' }))
on('POST', '/api/auth/login', () => { authed = true; log('login', USER); return { totpRequired: false, totpEnabled: false } })
on('POST', '/api/auth/logout', () => { authed = false; log('logout', USER); return { ok: true } })
on('GET', '/api/auth/me', () => ({ username: USER, totpEnabled: false, createdAt: ago(9 * DAY) }))
on('POST', '/api/setup', () => unsupported('重新初始化'))
on('POST', '/api/auth/totp/setup', () => unsupported('绑定两步验证'))
on('POST', '/api/auth/totp/enable', () => unsupported('绑定两步验证'))
on('POST', '/api/auth/totp/disable', () => unsupported('关闭两步验证'))
on('POST', '/api/auth/totp/verify', () => ({ ok: true }))
on('POST', '/api/auth/password', () => unsupported('修改口令'))
on('POST', '/api/auth/sessions/revoke-all', () => ({ ok: true }))

/* 账号 */
const checkOk = (a: Acct): D.CheckResultDTO => ({
  ok: a.status !== 'error', accountFatal: a.status === 'error',
  steps: [
    { key: 'sign', label: '签名与密钥', ok: a.status !== 'error', detail: a.status === 'error' ? '指纹与上传到 Oracle 的公钥不匹配' : '通过' },
    { key: 'user', label: '读取用户信息', ok: a.status !== 'error', detail: a.status === 'error' ? '跳过' : a.email },
    { key: 'compute', label: '列出实例', ok: a.status !== 'error', detail: a.status === 'error' ? '跳过' : '有权限' },
    { key: 'network', label: '列出 VCN', ok: a.status !== 'error', detail: a.status === 'error' ? '跳过' : '有权限' }
  ],
  userName: 'demo-user', userEmail: a.email, tenancyName: a.tenancyName,
  regions: a.subscribedRegions ?? [], homeRegion: a.homeRegion,
  errorCode: a.status === 'error' ? 'NotAuthenticated' : undefined,
  errorText: a.status === 'error' ? a.statusMessage : undefined
})
on('GET', '/api/accounts', () => ({ accounts: structuredClone(accounts) }))
on('GET', '/api/accounts/{id}', p => structuredClone(acctById(p.id)))
on('POST', '/api/accounts', (_, __, b) => {
  const id = uid('acc-')
  const region = b.defaultRegion || 'ap-tokyo-1'
  const a = account(id, b.alias, (b.code || 'NEW').toUpperCase(), b.colorIndex ?? (accounts.length % 8), region, [region], 'FREE_TRIAL',
    { createdAt: iso(Date.now()), subscriptionStartsAt: ago(1 * DAY) })
  accounts.push(a)
  makeNet(id, region)
  log('account_create', a.alias, id)
  return { account: structuredClone(a), check: checkOk(a) }
})
on('PATCH', '/api/accounts/{id}', (p, _, b) => {
  const a = acctById(p.id)
  for (const k of ['alias', 'code', 'colorIndex', 'enabled', 'defaultRegion'] as const) if (b[k] !== undefined) (a as any)[k] = b[k]
  a.updatedAt = iso(Date.now())
  log('account_update', a.alias, a.id)
  return structuredClone(a)
})
on('DELETE', '/api/accounts/{id}', (p, q) => {
  const a = acctById(p.id)
  if (q.confirm !== a.alias) fail(400, 'confirm_required', `删除账号需要在 confirm 参数中回传账号别名 "${a.alias}"`)
  accounts.splice(accounts.indexOf(a), 1)
  for (let n = instances.length - 1; n >= 0; n--) if (instances[n].dto.accountId === a.id) instances.splice(n, 1)
  log('account_delete', a.alias, a.id)
  return { ok: true }
})
on('POST', '/api/accounts/{id}/check', p => { const a = acctById(p.id); a.lastCheckedAt = iso(Date.now()); return checkOk(a) })
on('GET', '/api/accounts/{id}/regions', p => {
  const a = acctById(p.id)
  return { regions: ['ap-tokyo-1', 'ap-osaka-1', 'ap-singapore-1', 'us-phoenix-1', 'eu-frankfurt-1']
    .map(r => ({ regionName: r, isHomeRegion: r === a.homeRegion })) }
})
on('POST', '/api/accounts/parse-config', (_, __, b) => {
  const text: string = b.text ?? ''
  const get = (k: string) => (text.match(new RegExp(`^\\s*${k}\\s*=\\s*(.+)$`, 'm'))?.[1] ?? '').trim()
  const prof = { name: 'DEFAULT', userOcid: get('user'), fingerprint: get('fingerprint'), tenancyOcid: get('tenancy'),
    region: get('region'), keyFile: get('key_file'), hasPassPhrase: false, complete: false, missing: [] as string[], suggestedCode: 'NEW' }
  prof.missing = (['userOcid', 'fingerprint', 'tenancyOcid', 'region'] as const).filter(k => !prof[k])
  prof.complete = prof.missing.length === 0
  return { profiles: [prof] }
})
on('POST', '/api/accounts/check-draft', () => ({
  ok: true, accountFatal: false, steps: [{ key: 'sign', label: '签名与密钥', ok: true, detail: '通过（演示站不真正连接 Oracle）' }],
  regions: ['ap-tokyo-1'], homeRegion: 'ap-tokyo-1'
}))
on('GET', '/api/accounts/{id}/password-policy', p => ({
  policy: { accountId: p.id, supported: true, domainName: 'Default', domainUrl: 'https://idcs-demo.identity.oraclecloud.com',
    policyId: 'pp-demo', policyName: 'defaultPasswordPolicy', expiresAfterDays: 120, warnBeforeDays: 7, minLength: 8 },
  notice: '演示数据'
}))
on('PATCH', '/api/accounts/{id}/password-policy', (p, _, b) => {
  log('password_policy_update', acctById(p.id).alias, p.id, b.disable ? '不过期' : `${b.days} 天`)
  return { policy: { accountId: p.id, supported: true, domainName: 'Default', policyName: 'defaultPasswordPolicy',
    expiresAfterDays: b.disable ? null : b.days }, notice: '已保存（演示）' }
})

/* 资源页 */
on('GET', '/api/accounts/{id}/resources', (p, q) => resourcesFor(acctById(p.id), q.region))
on('DELETE', '/api/accounts/{id}/resources/volumes/{vid}', (p, q) => {
  const lv = looseVolumes.find(v => v.vol.id === p.vid) ?? fail(409, 'volume_attached', '这块卷仍挂在实例上，先分离再删除')
  const name = lv.vol.displayName || lv.vol.id
  if (q.confirm !== name) fail(400, 'confirm_required', `删除卷不可恢复，需要在 confirm 参数中回传卷名 "${name}"`)
  looseVolumes.splice(looseVolumes.indexOf(lv), 1)
  log('volume_delete', name, p.id, `${lv.vol.sizeInGbs} GB`)
  return { ok: true, notice: `${name} 已提交删除，几分钟内从列表消失。` }
})
on('DELETE', '/api/accounts/{id}/resources/public-ips/{ipid}', (p, q) => {
  const r = reservedIps.find(x => x.ip.id === p.ipid) ?? fail(400, 'not_reserved', '只能在这里释放保留 IP')
  if (q.confirm !== r.ip.ipAddress) fail(400, 'confirm_required', `需要在 confirm 参数中回传 IP 地址 "${r.ip.ipAddress}"`)
  reservedIps.splice(reservedIps.indexOf(r), 1)
  log('reserved_ip_release', r.ip.ipAddress, p.id)
  return { ok: true }
})
on('DELETE', '/api/accounts/{id}/resources/console-connections/{cid}', p => {
  const c = consoles.find(x => x.conn.id === p.cid)
  if (c) { consoles.splice(consoles.indexOf(c), 1); log('console_delete', c.conn.instance.name, p.id) }
  return { ok: true }
})

/* 实例 */
const syncStatus = () => ({ syncing: false, lastSync: ago(3 * MIN) })
on('GET', '/api/instances', (_, q) => {
  let list = q.includeTerminated === 'true' ? instances : liveInstances()
  if (q.accountIds) { const ids = q.accountIds.split(','); list = list.filter(i => ids.includes(i.dto.accountId)) }
  return { instances: list.map(i => structuredClone(i.dto)), sync: syncStatus() }
})
on('GET', '/api/instances/{id}', p => detailFor(instById(p.id)))
on('POST', '/api/instances/sync', () => {
  emit('sync.started')
  return submit({ kind: 'instance.sync', title: '手动同步', steps: ['逐个账号、区域拉取实例', '写入缓存'], ms: 2500,
    run: () => {
      setTimeout(() => emit('sync.finished'), 50)
      const report: D.SyncReportDTO = { startedAt: iso(Date.now()), durationMs: 2400, accounts: accounts.length,
        regions: 6, instances: liveInstances().length, terminated: 0, pruned: 0,
        errors: [{ accountId: 'acc-fra', accountAlias: '法兰克福', region: 'eu-frankfurt-1',
          message: 'NotAuthenticated', ociCode: 'NotAuthenticated' }] }
      return { summary: `${report.accounts} 个账号 · ${report.instances} 台实例`, result: report }
    } })
})
const ACTION: Record<string, [D.LifecycleState, D.LifecycleState]> = {
  START: ['STARTING', 'RUNNING'], SOFTSTOP: ['STOPPING', 'STOPPED'], STOP: ['STOPPING', 'STOPPED'],
  SOFTRESET: ['STOPPING', 'RUNNING'], RESET: ['STOPPING', 'RUNNING']
}
on('POST', '/api/instances/{id}/actions/{action}', p => {
  const i = instById(p.id)
  const t = ACTION[p.action] ?? fail(400, 'bad_action', '未知操作')
  i.dto.lastError = ''
  transition(i, t[0], t[1])
  log('instance_' + p.action.toLowerCase(), i.dto.displayName, i.dto.accountId)
  return structuredClone(i.dto)
})
on('PATCH', '/api/instances/{id}', (p, _, b) => {
  const i = instById(p.id)
  const old = i.dto.displayName
  i.dto.displayName = String(b.displayName ?? old).trim() || old
  i.vnics[0].displayName = i.dto.displayName
  log('instance_rename', i.dto.displayName, i.dto.accountId, `${old} → ${i.dto.displayName}`)
  emit('instance.updated', { instanceId: i.dto.id, accountId: i.dto.accountId, state: i.dto.lifecycleState })
  return structuredClone(i.dto)
})
on('PATCH', '/api/instances/{id}/note', (p, _, b) => { const i = instById(p.id); i.dto.note = b.note ?? ''; return structuredClone(i.dto) })
on('POST', '/api/instances/{id}/dismiss-error', p => { instById(p.id).dto.lastError = ''; return { ok: true } })
on('POST', '/api/instances/{id}/reshape', (p, _, b) => {
  const i = instById(p.id)
  return submit({ kind: 'instance.reshape', title: '改配置 · ' + i.dto.displayName, accountId: i.dto.accountId, instanceId: i.dto.id,
    steps: ['提交新配置', i.dto.lifecycleState === 'RUNNING' ? '等待 Oracle 重启实例' : '写回缓存'], ms: 5000,
    run: () => {
      i.dto.ocpus = b.ocpus ?? i.dto.ocpus; i.dto.memoryGb = b.memoryInGbs ?? i.dto.memoryGb
      if (b.baselineOcpuUtilization) i.dto.baselineOcpuUtilization = b.baselineOcpuUtilization === 'BASELINE_1_1' ? '' : b.baselineOcpuUtilization
      log('instance_reshape', i.dto.displayName, i.dto.accountId, `${i.dto.ocpus} OCPU / ${i.dto.memoryGb} GB`)
      emit('instance.updated', { instanceId: i.dto.id, accountId: i.dto.accountId, state: i.dto.lifecycleState })
      return { summary: `已改为 ${i.dto.ocpus} OCPU / ${i.dto.memoryGb} GB` }
    } })
})
on('DELETE', '/api/instances/{id}', (p, q) => {
  const i = instById(p.id)
  if (!settings.allowTerminate) fail(403, 'terminate_disabled', '终止实例功能已在设置中禁用')
  if (q.confirm !== i.dto.displayName) fail(400, 'confirm_required', `终止实例需要在 confirm 参数中回传实例名称 "${i.dto.displayName}"`)
  return submit({ kind: 'instance.terminate', title: '终止实例 ' + i.dto.displayName, accountId: i.dto.accountId, instanceId: i.dto.id,
    steps: ['提交终止请求', '等待实例终止'], ms: 5000,
    run: () => {
      instances.splice(instances.indexOf(i), 1)
      emit('instance.removed', { instanceId: i.dto.id, accountId: i.dto.accountId, state: 'TERMINATED' })
      log('instance_terminate', i.dto.displayName, i.dto.accountId)
      return { summary: '实例已终止' }
    } })
})
on('POST', '/api/instances/{id}/change-ip', p => {
  const i = instById(p.id)
  return submit({ kind: 'instance.change_ip', title: '更换公网 IP · ' + i.dto.displayName, accountId: i.dto.accountId, instanceId: i.dto.id,
    steps: ['释放旧的临时 IP', '申请新的临时 IP'], ms: 3500,
    run: () => {
      const oldIp = i.vnics[0].publicIp
      i.vnics[0].publicIp = freshIp(); syncSecondary(i)
      log('instance_change_ip', i.dto.displayName, i.dto.accountId, `${oldIp} → ${i.vnics[0].publicIp}`)
      return { summary: '新 IP 已生效', result: { oldIp, newIp: i.vnics[0].publicIp } }
    } })
})
on('POST', '/api/instances/{id}/enable-ipv6', p => {
  const i = instById(p.id)
  return submit({ kind: 'instance.enable_ipv6', title: '启用 IPv6 · ' + i.dto.displayName, accountId: i.dto.accountId, instanceId: i.dto.id,
    steps: ['为 VCN、子网开通 IPv6 前缀', '给网卡分配地址'], ms: 3500,
    run: () => {
      const address = freshV6()
      i.vnics[0].ipv6 = [...(i.vnics[0].ipv6 ?? []), address]; syncSecondary(i)
      log('instance_enable_ipv6', i.dto.displayName, i.dto.accountId, address)
      return { summary: '已分配 IPv6 ' + address, result: { address, steps: ['子网已有 IPv6 前缀', '已分配地址'] } }
    } })
})
on('DELETE', '/api/instances/{id}/ipv6', (p, q) => {
  const i = instById(p.id)
  return submit({ kind: 'instance.remove_ipv6', title: '移除 IPv6 · ' + i.dto.displayName, accountId: i.dto.accountId, instanceId: i.dto.id,
    steps: ['在实例的网卡上查找 ' + q.address, '删除 IPv6 地址'], ms: 2500,
    run: () => {
      for (const v of i.vnics) v.ipv6 = (v.ipv6 ?? []).filter(x => x !== q.address)
      syncSecondary(i)
      log('instance_remove_ipv6', i.dto.displayName, i.dto.accountId, q.address)
      return { summary: '已移除 IPv6 ' + q.address }
    } })
})
on('POST', '/api/instances/{id}/vnics', (p, _, b) => {
  const i = instById(p.id)
  return submit({ kind: 'instance.attach_vnic', title: '添加附属网卡 · ' + i.dto.displayName, accountId: i.dto.accountId, instanceId: i.dto.id,
    steps: ['创建网卡', '等待网卡挂载'], ms: 4000,
    run: () => {
      const base = i.vnics[0]
      i.vnics.push({ ...base, attachmentId: ocid('vnicattachment', i.dto.region), vnicId: ocid('vnic', i.dto.region),
        displayName: b.displayName || `vnic${Date.now().toString().slice(-6)}`, isPrimary: false,
        privateIp: `10.0.0.${Math.floor(rand(10, 250))}`, publicIp: b.assignPublicIp ? freshIp() : '',
        publicIpId: ocid('publicip', i.dto.region), ipv6: b.assignIpv6 ? [freshV6()] : [] })
      syncSecondary(i)
      emit('instance.network', { instanceId: i.dto.id, accountId: i.dto.accountId, state: 'ATTACHED' })
      log('instance_attach_vnic', i.dto.displayName, i.dto.accountId)
      return { summary: '附属网卡已挂载' }
    } })
})
on('DELETE', '/api/instances/{id}/vnics/{aid}', p => {
  const i = instById(p.id)
  const v = i.vnics.find(x => x.attachmentId === p.aid && !x.isPrimary) ?? fail(400, 'primary_vnic', '主网卡不能分离，只能分离附属网卡')
  return submit({ kind: 'instance.detach_vnic', title: '分离附属网卡 · ' + i.dto.displayName, accountId: i.dto.accountId, instanceId: i.dto.id,
    steps: ['提交分离请求', '等待网卡下线'], ms: 4000,
    run: () => {
      i.vnics.splice(i.vnics.indexOf(v), 1); syncSecondary(i)
      emit('instance.network', { instanceId: i.dto.id, accountId: i.dto.accountId, state: 'DETACHED' })
      log('instance_detach_vnic', i.dto.displayName, i.dto.accountId)
      return { summary: '附属网卡已分离' }
    } })
})
on('GET', '/api/instances/{id}/metrics', (p, q) => {
  const i = instById(p.id)
  const hours = Number(q.hours) || 6
  const n = 60
  const step = (hours * HOUR) / n
  const running = i.dto.lifecycleState === 'RUNNING'
  const k = running ? 1 : 0
  return {
    instanceId: i.dto.id, start: ago(hours * HOUR), end: iso(Date.now()), resolution: '1m',
    notice: '演示数据：曲线为随机生成。真实监控依赖实例内运行的 Oracle Cloud Agent。',
    series: [
      { metric: 'CpuUtilization', aggregation: 'mean', datapoints: series(n, step, 18 * k, 10 * k, 6 * k) },
      { metric: 'MemoryUtilization', aggregation: 'mean', datapoints: series(n, step, 46 * k, 4 * k, 3 * k) },
      { metric: 'NetworksBytesIn', aggregation: 'rate', datapoints: series(n, step, 180_000 * k, 120_000 * k, 60_000 * k) },
      { metric: 'NetworksBytesOut', aggregation: 'rate', datapoints: series(n, step, 420_000 * k, 300_000 * k, 120_000 * k) }
    ]
  }
})
on('GET', '/api/instances/{id}/traffic', p => {
  const i = instById(p.id)
  const now = new Date()
  const days = now.getUTCDate()
  const daily = Array.from({ length: days }, (_, d) => {
    const date = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), d + 1)).toISOString().slice(0, 10)
    return { date, outBytes: Math.round(rand(1.5, 6) * 1e9), inBytes: Math.round(rand(0.4, 2) * 1e9) }
  })
  return { instanceId: i.dto.id, month: now.toISOString().slice(0, 7), start: iso(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1)),
    end: iso(Date.now()), vnics: i.vnics.length,
    outBytes: daily.reduce((n, d) => n + d.outBytes, 0), inBytes: daily.reduce((n, d) => n + d.inBytes, 0),
    last24hOutBytes: daily.at(-1)?.outBytes ?? 0, last24hInBytes: daily.at(-1)?.inBytes ?? 0, series: daily,
    notice: '演示数据' }
})
on('POST', '/api/instances/{id}/console', p => {
  const i = instById(p.id)
  let c = consoles.find(x => x.conn.instance.instanceId === i.dto.id)
  if (!c) {
    c = { accountId: i.dto.accountId, conn: { id: ocid('instanceconsoleconnection', i.dto.region), region: i.dto.region,
      lifecycleState: 'ACTIVE', fingerprint: 'de:mo:fi:ng:er:pr:in:t0:00:00:00:00:00:00:00:00',
      instance: { instanceId: i.dto.id, name: i.dto.displayName } } }
    consoles.push(c)
    log('console_create', i.dto.displayName, i.dto.accountId)
  }
  const host = `instance-console.${i.dto.region}.oci.oraclecloud.com`
  return { id: c.conn.id, instanceId: i.dto.id, lifecycleState: 'ACTIVE',
    serialConsoleCommand: `ssh -o ProxyCommand='ssh -W %h:%p -p 443 ${c.conn.id}@${host}' ${i.dto.id}`,
    vncConsoleCommand: `ssh -o ProxyCommand='ssh -W %h:%p -p 443 ${c.conn.id}@${host}' -N -L localhost:5900:${i.dto.id}:5900 ${i.dto.id}`,
    notice: '演示站：命令里的 OCID 都是编的，连不上。', keyHint: '真实部署时，两层 ssh 都要用你注册的那把 RSA 私钥。' }
})
on('POST', '/api/instances/bulk', (_, __, b) => {
  const ids: string[] = b.instanceIds ?? []
  const t = ACTION[b.action] ?? fail(400, 'bad_action', '未知操作')
  return submit({ kind: 'instance.bulk', title: `批量操作 · ${ids.length} 台`, steps: ['逐台提交'], ms: 1500,
    run: () => {
      for (const id of ids) { const i = instances.find(x => x.dto.id === id); if (i) transition(i, t[0], t[1], rand(3000, 6000)) }
      log('instance_bulk_' + String(b.action).toLowerCase(), `${ids.length} 台实例`)
      return { summary: `已提交 ${ids.length} 台` }
    } })
})

/* 创建实例 */
on('GET', '/api/launch/presets', () => ({ presets: STATIC.launchPresets }))
on('GET', '/api/launch/availability-domains', (_, q) => {
  const r = q.region || 'ap-tokyo-1'
  return { availabilityDomains: [1, 2, 3].slice(0, r === 'ap-tokyo-1' || r === 'ap-singapore-1' ? 1 : 3)
    .map(n => ({ id: ocid('ad', r), name: AD(r, n), compartmentId: '' })) }
})
const SHAPES: D.ShapeDTO[] = [
  { shape: 'VM.Standard.A1.Flex', processorDescription: '3.0 GHz Ampere® Altra™', ocpus: 1, memoryInGBs: 6, isFlexible: true,
    baselineOcpuUtilizations: null, ocpuOptions: { min: 1, max: 80 }, memoryOptions: { minInGBs: 1, maxInGBs: 512, minPerOcpuInGBs: 1, maxPerOcpuInGBs: 64 } },
  { shape: 'VM.Standard.E2.1.Micro', processorDescription: '2.0 GHz AMD EPYC™ 7551', ocpus: 1, memoryInGBs: 1, isFlexible: false,
    baselineOcpuUtilizations: null, ocpuOptions: null, memoryOptions: null },
  { shape: 'VM.Standard.E4.Flex', processorDescription: '2.55 GHz AMD EPYC™ 7J13', ocpus: 1, memoryInGBs: 16, isFlexible: true,
    baselineOcpuUtilizations: ['BASELINE_1_8', 'BASELINE_1_2', 'BASELINE_1_1'], ocpuOptions: { min: 1, max: 64 },
    memoryOptions: { minInGBs: 1, maxInGBs: 1024, minPerOcpuInGBs: 1, maxPerOcpuInGBs: 64 } }
]
on('GET', '/api/launch/shapes', () => ({ shapes: SHAPES }))
on('GET', '/api/launch/images', (_, q) => {
  const arm = (q.shape ?? '').includes('A1')
  const suffix = arm ? '-aarch64' : ''
  return { images: [
    ['Canonical Ubuntu', '24.04'], ['Canonical Ubuntu', '22.04'], ['Oracle Linux', '9'], ['Debian', '12']
  ].map(([os, ver], n) => ({ id: ocid('image', 'demo') + n, displayName: `${os}-${ver}${suffix}-2026.09.01-0`,
    operatingSystem: os, operatingSystemVersion: ver, timeCreated: ago((n + 20) * DAY) })) }
})
on('POST', '/api/launch', (_, __, b) => {
  const a = acctById(b.accountId)
  return submit({ kind: 'instance.launch', title: '创建实例 ' + b.displayName, accountId: a.id,
    steps: ['检查网络', '提交创建请求', '等待实例 RUNNING'], ms: 7000,
    run: () => {
      const i = makeInstance({ accountId: a.id, region: b.region || a.homeRegion, name: b.displayName, shape: b.shape,
        ocpus: b.ocpus || 1, memoryGb: b.memoryInGbs || 1, ipv6: !!b.enableIpv6, bootGb: b.bootVolumeGb || 50,
        createdDaysAgo: 0, state: 'PROVISIONING' })
      emit('instance.updated', { instanceId: i.dto.id, accountId: a.id, state: 'PROVISIONING' })
      setTimeout(() => setState(i, 'RUNNING'), 2500)
      log('instance_launch', i.dto.displayName, a.id)
      return { summary: `${i.dto.displayName} 已创建` }
    } })
})

/* 网络 */
const netsOf = (q: Q) => nets.filter(n => n.accountId === q.accountId && n.region === (q.region || acctById(q.accountId).defaultRegion))
on('GET', '/api/network/vcns', (_, q) => ({ vcns: netsOf(q).map(n => n.vcn) }))
on('GET', '/api/network/subnets', (_, q) => ({ subnets: netsOf(q).flatMap(n => n.subnets).filter(s => !q.vcnId || s.vcnId === q.vcnId) }))
on('GET', '/api/network/security-lists', (_, q) => ({ securityLists: netsOf(q).map(n => n.securityList) }))
on('PUT', '/api/network/security-lists/{id}', (p, q, b) => {
  const n = nets.find(x => x.securityList.id === p.id) ?? fail(404, 'not_found', '安全列表不存在')
  n.securityList.ingressSecurityRules = b.ingress ?? []
  n.securityList.egressSecurityRules = b.egress ?? []
  n.securityList.noEgress = (b.egress ?? []).length === 0
  log('security_list_update', n.securityList.displayName, q.accountId)
  return n.securityList
})
on('GET', '/api/network/rule-templates', () => ({ templates: STATIC.ruleTemplates }))
on('GET', '/api/network/public-ips', (_, q) => ({
  publicIps: reservedIps.filter(r => r.accountId === q.accountId).map(r => ({ id: r.ip.id, displayName: r.ip.displayName,
    ipAddress: r.ip.ipAddress, lifetime: 'RESERVED', scope: 'REGION', privateIpId: '', lifecycleState: r.ip.lifecycleState }))
}))
on('POST', '/api/network/ensure', (_, q) => {
  const n = netFor(q.accountId, q.region || acctById(q.accountId).defaultRegion)
  return submit({ kind: 'network.ensure', title: '准备网络 · ' + n.region, accountId: q.accountId,
    steps: ['检查 VCN / 子网 / 网关 / 路由'], ms: 2500,
    run: () => ({ summary: '网络已就绪', result: { vcnId: n.vcn.id, vcnName: n.vcn.displayName, subnetId: n.subnets[0].id,
      subnetName: n.subnets[0].displayName, created: false, steps: ['已有可用的公网子网，无需新建'] } }) })
})

/* 存储 */
on('GET', '/api/storage/boot-volumes', (_, q) => ({ bootVolumes: liveInstances()
  .filter(i => i.dto.accountId === q.accountId && (!q.region || i.dto.region === q.region))
  .map(i => detailFor(i).bootVolume!) }))
on('GET', '/api/storage/volumes', (_, q) => ({ volumes: [
  ...liveInstances().filter(i => i.dto.accountId === q.accountId && (!q.region || i.dto.region === q.region))
    .flatMap(i => i.blockVolumes.map(b => ({ id: b.volumeId, compartmentId: '', displayName: b.displayName,
      availabilityDomain: i.dto.availabilityDomain, sizeInGBs: b.sizeInGbs, vpusPerGB: b.vpusPerGb, lifecycleState: 'AVAILABLE',
      imageId: '', isHydrated: true, timeCreated: i.dto.timeCreated }))),
  ...looseVolumes.filter(v => v.accountId === q.accountId && v.vol.kind === 'block' && (!q.region || v.vol.region === q.region))
    .map(v => ({ id: v.vol.id, compartmentId: '', displayName: v.vol.displayName, availabilityDomain: v.vol.availabilityDomain,
      sizeInGBs: v.vol.sizeInGbs, vpusPerGB: v.vol.vpusPerGb, lifecycleState: v.vol.lifecycleState, imageId: '',
      isHydrated: true, timeCreated: v.vol.timeCreated }))
] }))
const volumeJob = (kind: string, label: string, apply: () => string) =>
  submit({ kind, title: label, steps: ['提交修改', '等待卷落定'], ms: 3000, run: () => ({ summary: apply() }) })
on('PATCH', '/api/storage/boot-volumes/{id}', (p, _, b) => {
  const i = instances.find(x => x.dto.bootVolumeId === p.id) ?? fail(404, 'not_found', '引导卷不存在')
  return volumeJob('storage.boot_volume', '修改引导卷 · ' + i.dto.displayName, () => {
    if (b.sizeInGbs) i.dto.bootVolumeGb = b.sizeInGbs
    if (b.vpusPerGb !== undefined) i.dto.bootVolumeVpus = b.vpusPerGb
    log('boot_volume_update', i.dto.displayName, i.dto.accountId, `${i.dto.bootVolumeGb} GB · VPU ${i.dto.bootVolumeVpus}`)
    emit('instance.updated', { instanceId: i.dto.id, accountId: i.dto.accountId, state: i.dto.lifecycleState })
    return `${i.dto.bootVolumeGb} GB · VPU ${i.dto.bootVolumeVpus}`
  })
})
on('PATCH', '/api/storage/volumes/{id}', (p, _, b) => {
  const v = instances.flatMap(i => i.blockVolumes).find(x => x.volumeId === p.id) ?? fail(404, 'not_found', '卷不存在')
  return volumeJob('storage.volume', '修改块存储 · ' + v.displayName, () => {
    if (b.sizeInGbs) v.sizeInGbs = b.sizeInGbs
    if (b.vpusPerGb !== undefined) v.vpusPerGb = b.vpusPerGb
    log('volume_update', v.displayName)
    return `${v.sizeInGbs} GB · VPU ${v.vpusPerGb}`
  })
})
for (const path of ['boot-volume-attachments/detach', 'boot-volume-attachments/attach', 'volume-attachments/attach', 'volume-attachments/detach']) {
  on('POST', '/api/storage/' + path, () => ({ ok: true, notice: '已提交（演示站只模拟结果，不改变数据）' }))
}

/* 任务 */
on('GET', '/api/jobs', (_, q) => ({ jobs: structuredClone(q.status === 'active'
  ? jobs.filter(j => j.status === 'queued' || j.status === 'running') : jobs) }))
on('GET', '/api/jobs/{id}', p => structuredClone(jobs.find(j => j.id === p.id) ?? fail(404, 'not_found', '任务不存在')))
on('POST', '/api/jobs/{id}/cancel', () => fail(409, 'job_running', '任务已经开始执行，不能取消'))
on('DELETE', '/api/jobs/{id}', p => { const n = jobs.findIndex(j => j.id === p.id); if (n >= 0) jobs.splice(n, 1); return { ok: true } })
on('POST', '/api/jobs/clear', () => {
  const before = jobs.length
  for (let n = jobs.length - 1; n >= 0; n--) if (jobs[n].status !== 'queued' && jobs[n].status !== 'running') jobs.splice(n, 1)
  return { deleted: before - jobs.length }
})

/* 守候 */
on('GET', '/api/hunt', () => {
  // 页面每 15 秒拉一次：给运行中的任务长一点尝试次数，看起来在动。
  for (const t of huntTasks) if (t.state === 'running') {
    t.attempts += Math.floor(rand(0, 3)); t.lastTryAt = ago(rand(2, 30) * 1000); t.nextAt = iso(Date.now() + t.intervalSeconds * 1000)
  }
  return { tasks: structuredClone(huntTasks), limits: STATIC.huntLimits }
})
on('POST', '/api/hunt', (_, __, b) => {
  const a = acctById(b.accountId)
  const t: D.HuntTaskDTO = { id: uid('hunt-'), accountId: a.id, region: b.region || a.homeRegion,
    name: b.name || `${b.shape} · ${b.displayName}`, state: 'running', attempts: 0, intervalSeconds: b.intervalSeconds,
    precheckCapacity: b.precheckCapacity ?? true, lastClass: '', lastError: '', lastAd: '', lastTryAt: '',
    nextAt: iso(Date.now() + 5000), instanceId: '', maxAttempts: b.maxAttempts ?? 0,
    expiresAt: b.expiresInHours ? iso(Date.now() + b.expiresInHours * HOUR) : '', createdAt: iso(Date.now()), updatedAt: iso(Date.now()),
    shape: b.shape, ocpus: b.ocpus ?? 1, memoryGb: b.memoryInGbs ?? 6, displayName: b.displayName, ads: b.availabilityDomains ?? null }
  huntTasks.unshift(t)
  log('hunt_create', t.name, a.id)
  return { task: t, notice: '任务已创建（演示站不会真的去抢机）' }
})
on('POST', '/api/hunt/{id}/{action}', p => {
  const t = huntTasks.find(x => x.id === p.id) ?? fail(404, 'not_found', '任务不存在')
  t.state = p.action === 'pause' ? 'paused' : 'running'
  log('hunt_' + p.action, t.name, t.accountId)
  return { task: t }
})
on('DELETE', '/api/hunt/{id}', p => {
  const n = huntTasks.findIndex(x => x.id === p.id)
  if (n >= 0) { log('hunt_delete', huntTasks[n].name, huntTasks[n].accountId); huntTasks.splice(n, 1) }
  return { ok: true, notice: '已删除' }
})

/* 容量 */
on('GET', '/api/capacity', () => ({ watches: structuredClone(watches), probeIntervalSeconds: STATIC.probeIntervalSeconds }))
const probeRows = (region: string) => [1, 2, 3].map(n => {
  const ok = Math.random() < 0.35
  return { availabilityDomain: AD(region, n), short: `AD-${n}`, status: ok ? 'AVAILABLE' : 'OUT_OF_HOST_CAPACITY',
    statusText: ok ? '有容量' : '没有容量', availableCount: ok ? Math.floor(rand(1, 6)) : 0 }
})
on('POST', '/api/capacity/probe', (_, __, b) => ({ region: b.region || 'ap-tokyo-1', shape: b.shape,
  results: probeRows(b.region || 'ap-tokyo-1'), notice: '演示数据：结果随机生成' }))
on('POST', '/api/capacity', (_, __, b) => {
  const r = b.region || 'ap-tokyo-1'
  const w: D.CapacityWatchDTO = { id: uid('cw-'), accountId: b.accountId, region: r, availabilityDomain: b.availabilityDomain || AD(r),
    availabilityDomainShort: 'AD-1', shape: b.shape, ocpus: b.ocpus ?? 1, memoryGb: b.memoryGb ?? 6, enabled: true,
    lastStatus: 'OUT_OF_HOST_CAPACITY', statusText: '没有容量', lastCount: 0, lastError: '',
    lastCheckedAt: iso(Date.now()), lastChangedAt: iso(Date.now()), createdAt: iso(Date.now()), updatedAt: iso(Date.now()) }
  watches.unshift(w)
  log('capacity_watch_create', `${w.shape} · ${r}`, b.accountId)
  return { watch: w, notice: '已加入监控' }
})
on('POST', '/api/capacity/{id}/{action}', p => {
  const w = watches.find(x => x.id === p.id) ?? fail(404, 'not_found', '监控项不存在')
  w.enabled = p.action === 'enable'
  return { watch: w }
})
on('DELETE', '/api/capacity/{id}', p => { const n = watches.findIndex(x => x.id === p.id); if (n >= 0) watches.splice(n, 1); return { ok: true } })

/* 配额 / 审计 */
on('GET', '/api/quota', (_, q) => ({ quotas: accounts.filter(a => !q.accountId || a.id === q.accountId).map(quotaFor) }))
on('GET', '/api/regions', () => ({ regions: ['ap-tokyo-1', 'ap-osaka-1', 'ap-singapore-1', 'us-phoenix-1', 'eu-frankfurt-1'] }))
on('GET', '/api/audit', (_, q) => {
  let list = audit
  if (q.accountId) list = list.filter(e => e.accountId === q.accountId)
  if (q.action) list = list.filter(e => e.action === q.action)
  if (q.beforeId) list = list.filter(e => e.id < Number(q.beforeId))
  const limit = Number(q.limit) || 50
  return { entries: structuredClone(list.slice(0, limit)), hasMore: list.length > limit, total: list.length }
})

/* 通知与设置 */
on('GET', '/api/notifications/channels', () => ({ channels: structuredClone(channels), kinds: STATIC.channelKinds }))
on('GET', '/api/notifications/events', () => ({ events: STATIC.notifyEvents }))
const maskSecrets = (kind: string, config: Record<string, string>) => {
  const k = STATIC.channelKinds.find(x => x.kind === kind)
  const out = { ...config }
  for (const f of k?.fields ?? []) if (f.secret && out[f.key]) out[f.key] = out[f.key].slice(0, 4) + '••••••••' + out[f.key].slice(-4)
  return out
}
on('POST', '/api/notifications/channels', (_, __, b) => {
  const ch: D.ChannelDTO = { id: uid('ch-'), kind: b.kind, name: b.name, config: maskSecrets(b.kind, b.config ?? {}),
    events: b.events ?? [], enabled: true, lastError: '', lastSentAt: null, createdAt: iso(Date.now()), updatedAt: iso(Date.now()) }
  channels.push(ch)
  log('channel_create', ch.name)
  return ch
})
on('PATCH', '/api/notifications/channels/{id}', (p, _, b) => {
  const ch = channels.find(x => x.id === p.id) ?? fail(404, 'not_found', '渠道不存在')
  if (b.name !== undefined) ch.name = b.name
  if (b.events !== undefined) ch.events = b.events
  if (b.enabled !== undefined) ch.enabled = b.enabled
  if (b.config) ch.config = { ...ch.config, ...maskSecrets(ch.kind, b.config) }
  ch.updatedAt = iso(Date.now())
  log('channel_update', ch.name)
  return ch
})
on('DELETE', '/api/notifications/channels/{id}', p => {
  const n = channels.findIndex(x => x.id === p.id)
  if (n >= 0) { log('channel_delete', channels[n].name); channels.splice(n, 1) }
  return { ok: true }
})
on('POST', '/api/notifications/channels/{id}/test', () => ({ ok: true }))
on('GET', '/api/settings', () => settings)
on('PATCH', '/api/settings', (_, __, b) => {
  settings = { ...settings, ...b }
  log('settings_update', '')
  return { settings, notice: '已保存' }
})

/* 账单 */
const billingStatus = (a: Acct): D.BillingStatusDTO =>
  a.status === 'error' ? 'no_permission' : a.paymentModel === 'PAY_AS_YOU_GO' ? 'ok' : 'free'
on('GET', '/api/billing', () => {
  const summaries = accounts.map(a => {
    const st = billingStatus(a)
    return { accountId: a.id, status: st, currency: st === 'ok' ? 'USD' : '', thisMonth: st === 'ok' ? 3.42 : 0,
      lastMonth: st === 'ok' ? 4.18 : 0, region: a.homeRegion, fetchedAt: iso(Date.now()),
      error: st === 'no_permission' ? '缺少 read usage-report 权限（演示数据）' : undefined }
  })
  return { summaries, totals: [{ currency: 'USD', thisMonth: 3.42, lastMonth: 4.18, accounts: 1 }],
    countedAccounts: summaries.filter(s => s.status !== 'no_permission').length,
    noPermissionCount: summaries.filter(s => s.status === 'no_permission').length,
    notice: '演示数据。用量数据由 Oracle 每隔几小时结算一次，最新一天通常不完整。' }
})
on('GET', '/api/billing/{id}', (p, q) => {
  const a = acctById(p.id)
  const st = billingStatus(a)
  const days = Number(q.days) || 30
  const paid = st === 'ok'
  const s = Array.from({ length: days }, (_, d) => ({
    date: new Date(Date.now() - (days - 1 - d) * DAY).toISOString().slice(0, 10), amount: paid ? +rand(0.05, 0.18).toFixed(3) : 0 }))
  const total = +s.reduce((n, x) => n + x.amount, 0).toFixed(2)
  const b = (key: string, amount: number, quantity: number, unit: string) => ({ key, amount, quantity, unit })
  return { accountId: a.id, status: st, currency: paid ? 'USD' : '', region: a.homeRegion,
    start: ago(days * DAY), end: iso(Date.now()), days, total, series: s,
    services: [b('Compute', +(total * 0.7).toFixed(2), 2900, 'OCPU Hours'), b('Block Storage', +(total * 0.2).toFixed(2), 247, 'GB Months'),
      b('Networking', +(total * 0.1).toFixed(2), 312, 'GB')],
    regions: (a.subscribedRegions ?? []).map((r, n) => b(r, +(total * (n ? 0.2 : 0.8)).toFixed(2), 0, '')),
    usage: [b('Compute', 0, 2900, 'OCPU Hours'), b('Block Storage', 0, 247, 'GB Months')],
    skus: [b('Virtual Machine Standard - A1 - OCPU', +(total * 0.5).toFixed(2), 2900, 'OCPU Hours'),
      b('Block Volume Storage', +(total * 0.2).toFixed(2), 247, 'GB Months'),
      b('Outbound Data Transfer Zone 1', 0, 312, 'GB')],
    egress: b('Outbound Data Transfer', 0, 312, 'GB'), fetchedAt: iso(Date.now()),
    error: st === 'no_permission' ? '缺少 read usage-report 权限（演示数据）' : undefined }
})
on('GET', '/api/billing/{id}/egress', p => {
  const a = acctById(p.id)
  const now = new Date()
  const sr = Array.from({ length: now.getUTCDate() }, (_, d) => ({
    date: new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), d + 1)).toISOString().slice(0, 10), gb: +rand(6, 18).toFixed(1) }))
  return { accountId: a.id, status: billingStatus(a), region: a.homeRegion, month: now.toISOString().slice(0, 7),
    usedGb: +sr.reduce((n, x) => n + x.gb, 0).toFixed(1), freeGb: 10240, unit: 'GB', series: sr, amount: 0, currency: '',
    fetchedAt: iso(Date.now()) }
})

/* 代理 */
on('GET', '/api/proxies', () => ({ proxies: structuredClone(proxies), bindings: { ...bindings }, checkTimeoutMs: 8000,
  notice: '演示数据：代理地址都是编的。' }))
on('POST', '/api/proxies/import', (_, __, b) => {
  const lines = String(b.text ?? '').split('\n').map(s => s.trim()).filter(Boolean)
  const rows = lines.map((l, n) => {
    const m = l.match(/^(?:(socks5|http|https):\/\/)?(?:([^:@]+)(?::[^@]*)?@)?([\w.-]+):(\d+)$/)
    return m ? { line: n + 1, masked: `${m[1] ?? 'socks5'}://${m[3]}:${m[4]}`, label: '' }
      : { line: n + 1, masked: l.slice(0, 40), error: '认不出这一行的格式' }
  })
  if (!b.dryRun) for (const r of rows) if (!r.error) {
    const [scheme, rest] = r.masked.split('://')
    const [host, port] = rest.split(':')
    proxies.push({ id: uid('px-'), label: '', scheme, host, port: Number(port), username: '', hasPassword: false, enabled: true,
      lastStatus: 'unknown', lastLatencyMs: 0, lastError: '', lastRegion: '', lastCheckedAt: '', lastOkAt: '',
      createdAt: iso(Date.now()), updatedAt: iso(Date.now()) })
  }
  const ok = rows.filter(r => !r.error).length
  return { dryRun: !!b.dryRun, rows, ok, failed: rows.length - ok, skipped: 0 }
})
on('PATCH', '/api/proxies/{id}', (p, _, b) => {
  const px = proxies.find(x => x.id === p.id) ?? fail(404, 'not_found', '代理不存在')
  if (b.label !== undefined) px.label = b.label
  if (b.enabled !== undefined) px.enabled = b.enabled
  return { proxy: px }
})
on('DELETE', '/api/proxies/{id}', p => { const n = proxies.findIndex(x => x.id === p.id); if (n >= 0) proxies.splice(n, 1); return { ok: true } })
on('POST', '/api/proxies/bind', (_, __, b) => { if (b.proxyId) bindings[b.accountId] = b.proxyId; else delete bindings[b.accountId]; return { ok: true } })
const checkProxy = (px: D.ProxyDTO): D.ProxyCheckRowDTO => {
  const ok = px.enabled && Math.random() < 0.85
  px.lastStatus = ok ? 'ok' : 'fail'; px.lastLatencyMs = ok ? Math.floor(rand(60, 260)) : 0
  px.lastError = ok ? '' : '连接超时（演示数据）'; px.lastCheckedAt = iso(Date.now()); if (ok) px.lastOkAt = px.lastCheckedAt
  return { id: px.id, status: px.lastStatus, latencyMs: px.lastLatencyMs, error: px.lastError || undefined, checkedAt: px.lastCheckedAt, region: 'ap-tokyo-1' }
}
on('POST', '/api/proxies/check', () => ({ results: proxies.map(checkProxy) }))
on('POST', '/api/proxies/{id}/check', p => ({ results: [checkProxy(proxies.find(x => x.id === p.id) ?? fail(404, 'not_found', '代理不存在'))] }))

/* 总览（前端目前不调用，保留以免 404） */
on('GET', '/api/overview', () => unsupported('这个接口'))

/* ---------- 入口 ---------- */

/**
 * 代替 fetch 处理一次请求。加一点随机延迟，让加载动画、骨架屏在演示站上也能看到。
 */
export async function demoSend<T>(method: string, path: string, query?: Record<string, unknown>, body?: unknown): Promise<T> {
  const q: Q = {}
  for (const [k, v] of Object.entries(query ?? {})) if (v !== undefined && v !== null && v !== '') q[k] = String(v)
  for (const [m, re, keys, h] of routes) {
    if (m !== method) continue
    const hit = re.exec(path)
    if (!hit) continue
    const params: Record<string, string> = {}
    keys.forEach((k, n) => { params[k] = decodeURIComponent(hit[n + 1]) })
    await sleep(method === 'GET' ? rand(180, 520) : rand(250, 600))
    if (!authed && !['/api/status', '/api/auth/login'].includes(path)) {
      window.dispatchEvent(new CustomEvent(AUTH_REQUIRED_EVENT, { detail: { code: 'unauthenticated' } }))
      fail(401, 'unauthenticated', '请先登录')
    }
    // 返回深拷贝：页面会直接修改拿到的对象，不能让它改到这里的「数据库」。
    return structuredClone(h(params, q, structuredClone(body ?? {}))) as T
  }
  await sleep(200)
  return unsupported('这个操作')
}
