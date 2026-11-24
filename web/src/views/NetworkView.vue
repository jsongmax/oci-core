<script setup lang="ts">
/**
 * §4.5 网络：VCN / 子网 / 安全规则 / 公网 IP / IPv6，跨账号聚合。
 *
 * 网络资源在 OCI 里是按（账号 × 区域）隔离的，没有跨账号的列表接口，
 * 因此这里对筛选器里的每个账号 × 其订阅区域各拉一次，再在前端合并。
 * 网络对象数量很少（一个账号通常一两个 VCN），这个代价可以接受。
 */
import { t } from '@/i18n'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useStore } from '@/store'
import { acctColor, copy } from '@/lib/format'
import { mask } from '@/lib/mask'
import { network, instances as instancesApi, accounts as accountsApi, errorText, type SubnetDTO, type VcnDTO, type SecurityListDTO, type PublicIpDTO, type ChangeIpDTO, type BindReservedIpDTO, type SwitchEphemeralIpDTO, type VnicInfoDTO, type RuleTemplateDTO, type RuleDirection, type IngressRuleDTO, type EgressRuleDTO } from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import PageTabs from '@/components/PageTabs.vue'
import AccountChip from '@/components/AccountChip.vue'
import MaskedText from '@/components/MaskedText.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import LoadingBar from '@/components/LoadingBar.vue'
import SelectMenu, { type SelectGroup } from '@/components/SelectMenu.vue'
import AccountGroupRow from '@/components/AccountGroupRow.vue'
import { groupByAccount } from '@/lib/grouping'

const { state, accountById, ask, toast, toastError, visibleInstances, trackJob, refreshInstances } = useStore()
const TABS = ['vcn', 'subnet', 'rules', 'publicIp', 'reservedIp', 'ipv6']
const active = ref('vcn')

/** 一条网络记录附带它属于哪个账号与区域——跨账号聚合后必须能归属。 */
interface Located<T> { accountId: string; region: string; item: T }

const loading = ref(false)
const publicIpsLoading = ref(false)
const reservedIpsLoading = ref(false)
const publicIpLoaded = ref(false)
const reservedIpLoaded = ref(false)
/**
 * 已经拿到过数据。刷新时有数据就保留旧列表调暗显示，只有一条都没有时才画骨架——
 * 以前每次刷新都把整页换回骨架，列表闪一下才回来。
 */
const hasNet = computed(() => vcns.value.length + subnets.value.length + securityLists.value.length > 0)
const loadError = ref('')
const vcns = ref<Located<VcnDTO>[]>([])
const subnets = ref<Located<SubnetDTO>[]>([])
const boundPublicIps = ref<Located<PublicIpDTO>[]>([])
const reservedPublicIps = ref<Located<PublicIpDTO>[]>([])
const loadedPublicIpTargets = ref<Set<string>>(new Set())
const templates = ref<RuleTemplateDTO[]>([])

const shownIp = (ip: string) => (ip && state.privacyMode ? mask(ip, 'ip') : ip)

function compareIp(a: string, b: string): number {
  const pa = a.split('.')
  const pb = b.split('.')
  if (pa.length === 4 && pb.length === 4) {
    for (let i = 0; i < 4; i++) {
      const na = Number(pa[i])
      const nb = Number(pb[i])
      if (!Number.isNaN(na) && !Number.isNaN(nb) && na !== nb) return na - nb
    }
    return 0
  }
  return a.localeCompare(b)
}

/** 安全规则标签页：当前正在编辑的安全列表。 */
const selectedListKey = ref('')
const securityLists = ref<Located<SecurityListDTO>[]>([])
const savingRules = ref(false)

const selectedAccounts = computed(() =>
  state.accounts.filter(a => state.accountFilter.has(a.id) && a.status !== 'disabled')
)

/** 并发拉取。上限 4 是为了不在同一租户上堆太多并发请求触发限流。 */
async function mapLimited<T, R>(items: T[], limit: number, fn: (item: T) => Promise<R>): Promise<R[]> {
  const out: R[] = []
  for (let i = 0; i < items.length; i += limit) {
    out.push(...await Promise.all(items.slice(i, i + limit).map(fn)))
  }
  return out
}

/**
 * 展开成（账号 × 区域）任务列表。
 *
 * 顶栏的区域筛选器必须参与：它以前只作用于实例页，在这里点了没有任何反应，
 * 用户以为筛了、实际上还是全区域在拉。筛选器为空表示"不限"，不是"全都不要"。
 */
const targets = computed(() =>
  selectedAccounts.value.flatMap(a =>
    a.regions
      .filter(r => state.regionFilter.size === 0 || state.regionFilter.has(r))
      .map(region => ({ accountId: a.id, region }))
  )
)

/**
 * 拉取代次。筛选器连点时，范围大的那一轮往往最慢，晚回来会把已经
 * 筛掉的账号重新显示出来。
 */
let networkSeq = 0
let publicIpSeq = 0
let reservedIpSeq = 0

async function loadNetwork() {
  const seq = ++networkSeq
  publicIpLoaded.value = false
  reservedIpLoaded.value = false
  if (targets.value.length === 0) {
    loading.value = false
    vcns.value = []
    subnets.value = []
    securityLists.value = []
    boundPublicIps.value = []
    reservedPublicIps.value = []
    loadedPublicIpTargets.value = new Set()
    return
  }

  loading.value = true
  loadError.value = ''
  const nextVcns: Located<VcnDTO>[] = []
  const nextSubnets: Located<SubnetDTO>[] = []
  const nextLists: Located<SecurityListDTO>[] = []
  const failures: string[] = []

  await mapLimited(targets.value, 4, async ({ accountId, region }) => {
    try {
      const [v, s, sl] = await Promise.all([
        network.vcns(accountId, region),
        network.subnets(accountId, region),
        network.securityLists(accountId, region)
      ])
      v.vcns.forEach(item => nextVcns.push({ accountId, region, item }))
      s.subnets.forEach(item => nextSubnets.push({ accountId, region, item }))
      sl.securityLists.forEach(item => nextLists.push({ accountId, region, item }))
    } catch (err) {
      // 单个区域失败不该让整页空白——记下来在页尾提示即可。
      failures.push(`${accountById(accountId).alias} · ${region}：${errorText(err)}`)
    }
  })

  // 并发拉取时每个任务在自己的响应回来后才 push，所以数组顺序 = 响应先后，
  // 每次刷新都可能不一样。多账号时用户会看到列表莫名重排，还会让安全规则页
  // 默认选中的那一条也跟着变。统一按 账号 → 区域 → 名称 排一次。
  if (seq !== networkSeq) return

  nextVcns.sort(byLocation(v => v.item.displayName))
  nextSubnets.sort(byLocation(v => v.item.displayName))
  nextLists.sort(byLocation(v => v.item.displayName))

  vcns.value = nextVcns
  subnets.value = nextSubnets
  securityLists.value = nextLists
  loadError.value = failures.join('\n')
  loading.value = false

  if (!selectedListKey.value && nextLists.length > 0) {
    selectedListKey.value = listKey(nextLists[0])
  }

  // 按当前页签懒加载公网 IP / 预留 IP，避免在 VCN/子网/规则页也并发拉全区公网 IP。
  if (active.value === 'publicIp') void loadPublicIps()
  else if (active.value === 'reservedIp') void loadReservedIps()
}

async function loadPublicIps() {
  const seq = ++publicIpSeq
  if (targets.value.length === 0) {
    boundPublicIps.value = []
    loadedPublicIpTargets.value = new Set()
    publicIpLoaded.value = true
    return
  }
  publicIpsLoading.value = true
  const nextIps: Located<PublicIpDTO>[] = []
  const nextIpTargets = new Set<string>()
  await mapLimited(targets.value, 4, async ({ accountId, region }) => {
    const ipsRes = await network.publicIps(accountId, region, 'ALL').catch(() => null)
    if (ipsRes) {
      nextIpTargets.add(`${accountId}|${region}`)
      ipsRes.publicIps.forEach(item => nextIps.push({ accountId, region, item }))
    }
  })
  if (seq !== publicIpSeq) return
  nextIps.sort((a, b) =>
    accountById(a.accountId).alias.localeCompare(accountById(b.accountId).alias, 'zh') ||
    a.region.localeCompare(b.region) ||
    (a.item.instanceName ?? '').localeCompare(b.item.instanceName ?? '', 'zh') ||
    Number(b.item.isPrimaryVnic !== false) - Number(a.item.isPrimaryVnic !== false) ||
    Number(b.item.isPrimaryPrivateIp !== false) - Number(a.item.isPrimaryPrivateIp !== false) ||
    compareIp(a.item.ipAddress, b.item.ipAddress)
  )
  boundPublicIps.value = nextIps
  loadedPublicIpTargets.value = nextIpTargets
  publicIpLoaded.value = true
  publicIpsLoading.value = false
}

async function loadReservedIps() {
  const seq = ++reservedIpSeq
  if (targets.value.length === 0) {
    reservedPublicIps.value = []
    reservedIpLoaded.value = true
    return
  }
  reservedIpsLoading.value = true
  const nextReserved: Located<PublicIpDTO>[] = []
  await mapLimited(targets.value, 4, async ({ accountId, region }) => {
    const resRes = await network.publicIps(accountId, region, 'RESERVED').catch(() => null)
    if (resRes) {
      resRes.publicIps.forEach(item => nextReserved.push({ accountId, region, item }))
    }
  })
  if (seq !== reservedIpSeq) return
  nextReserved.sort((a, b) =>
    accountById(a.accountId).alias.localeCompare(accountById(b.accountId).alias, 'zh') ||
    a.region.localeCompare(b.region) ||
    Number(!!(b.item.privateIpId || b.item.assignedEntityId)) - Number(!!(a.item.privateIpId || a.item.assignedEntityId)) ||
    compareIp(a.item.ipAddress, b.item.ipAddress)
  )
  reservedPublicIps.value = nextReserved
  reservedIpLoaded.value = true
  reservedIpsLoading.value = false
}

async function reloadIpLists() {
  publicIpLoaded.value = false
  reservedIpLoaded.value = false
  if (active.value === 'publicIp') await loadPublicIps()
  else if (active.value === 'reservedIp') await loadReservedIps()
}


/**
 * 除了"安全规则"，其余页签都是跨账号的清单，都值得分组。
 *
 * 一开始只放开了 VCN 与子网，理由是"公网 IP 和 IPv6 是扁平清单"——
 * 那是拿单账号的样子想当然：接第二个账号后，六条 IP 混在一起同样难分。
 * 安全规则页是"编辑单个安全列表"，那里确实用不上。
 */
const groupable = computed(() => active.value !== 'rules')

const grouped = <T extends { accountId: string }>(rows: T[]) =>
  groupByAccount(rows, state.groupByAccount, accountById)

const vcnGroups = computed(() => grouped(vcns.value))
const subnetGroups = computed(() => grouped(subnets.value))
const ipGroups = computed(() => grouped(publicIps.value))
const reservedGroups = computed(() => grouped(reservedIps.value))
const ipv6Groups = computed(() => grouped(ipv6Prefixes.value))

/** 稳定排序：账号别名 → 区域 → 名称。 */
function byLocation<T>(name: (x: Located<T>) => string) {
  return (a: Located<T>, b: Located<T>) =>
    accountById(a.accountId).alias.localeCompare(accountById(b.accountId).alias, 'zh') ||
    a.region.localeCompare(b.region) ||
    name(a).localeCompare(name(b), 'zh')
}

/** 安全列表下拉：按账号分档，每项带账号身份色点。 */
const listGroups = computed<SelectGroup[]>(() => {
  const byId = new Map<string, Located<SecurityListDTO>[]>()
  for (const l of securityLists.value) {
    const list = byId.get(l.accountId)
    if (list) list.push(l)
    else byId.set(l.accountId, [l])
  }
  return [...byId.entries()].map(([id, rows]) => {
    const a = accountById(id)
    return {
      label: `${a.code} · ${a.alias}`,
      options: rows.map(l => ({
        value: listKey(l),
        label: `${l.region} · ${l.item.displayName}`,
        dot: acctColor(a.colorIndex)
      }))
    }
  })
})

/** 模板追加到哪个安全列表——按钮是写操作，目标必须一眼可见。 */
const templateTargetNote = computed(() => {
  const l = selectedList.value
  if (!l) return t.networkPage.pickListFirst
  return t.networkPage.appendTo(accountById(l.accountId).alias, l.region, l.item.displayName)
})

const listKey = (l: Located<SecurityListDTO>) => `${l.accountId}|${l.region}|${l.item.id}`

const selectedList = computed(() =>
  securityLists.value.find(l => listKey(l) === selectedListKey.value)
)

/** 把入站/出站规则摊平成表格行，保留原始下标以便删除。 */
interface RuleRow {
  dir: 'ingress' | 'egress'
  index: number
  proto: string
  cidr: string
  ports: string
  desc: string
  danger: boolean
}

const PROTO_NAME: Record<string, string> = { '1': 'ICMP', '6': 'TCP', '17': 'UDP', all: 'ALL' }

function portsText(rule: IngressRuleDTO | EgressRuleDTO): string {
  const range = rule.tcpOptions?.destinationPortRange ?? rule.udpOptions?.destinationPortRange
  if (range) return range.min === range.max ? String(range.min) : `${range.min}–${range.max}`
  if (rule.icmpOptions) return `type ${rule.icmpOptions.type}`
  return t.networkPage.allPorts
}

const ruleRows = computed<RuleRow[]>(() => {
  const list = selectedList.value?.item
  if (!list) return []

  const allowAll = new Set(list.allowAllRules ?? [])
  const rows: RuleRow[] = []

  ;(list.ingressSecurityRules ?? []).forEach((rule, index) => {
    rows.push({
      dir: 'ingress', index,
      proto: PROTO_NAME[rule.protocol] ?? rule.protocol,
      cidr: rule.source,
      ports: portsText(rule),
      desc: rule.description || '—',
      danger: allowAll.has(index)
    })
  })
  ;(list.egressSecurityRules ?? []).forEach((rule, index) => {
    rows.push({
      dir: 'egress', index,
      proto: PROTO_NAME[rule.protocol] ?? rule.protocol,
      cidr: rule.destination,
      ports: portsText(rule),
      desc: rule.description || '—',
      danger: false
    })
  })
  return rows
})

/**
 * 提交完整规则集。
 *
 * OCI 的语义是整体替换而非增量追加，因此必须把当前完整的入站+出站
 * 一起提交——只提交改动的那几条会把其余规则静默删掉。
 */
async function saveRules(ingress: IngressRuleDTO[], egress: EgressRuleDTO[], successText: string): Promise<boolean> {
  const target = selectedList.value
  if (!target) return false

  savingRules.value = true
  try {
    const updated = await network.updateSecurityList(
      target.item.id, target.accountId, target.region, ingress, egress
    )
    target.item = { ...updated, allowAllRules: updated.allowAllRules ?? null }
    toast({ tone: 'success', title: successText })
    return true
  } catch (err) {
    toastError(t.networkPage.saveFailed, err)
    await loadNetwork()
    return false
  } finally {
    savingRules.value = false
  }
}

/**
 * 协议相关的选项。
 *
 * range 为空表示不限端口，此时**不能**退化成 0-0 的区间——那会生成一条
 * 只对端口 0 生效的规则，看着像加上了、实际什么都没放行。不限端口的
 * 正确写法是整个 tcpOptions/udpOptions 都不出现。
 *
 * ICMP 固定用 type 3 / code 4（需要分片但设置了 DF）：路径 MTU 发现靠它，
 * 丢了会表现为"小包正常、大包卡死"。
 */
function protocolOptions(protocol: string, range?: { min: number; max: number }) {
  if (protocol === '1') return { icmpOptions: { type: 3, code: 4 } }
  if (!range) return {}
  if (protocol === '6') return { tcpOptions: { destinationPortRange: range } }
  if (protocol === '17') return { udpOptions: { destinationPortRange: range } }
  return {}
}

function buildIngress(protocol: string, cidr: string, desc: string, range?: { min: number; max: number }): IngressRuleDTO {
  return {
    protocol, source: cidr, sourceType: 'CIDR_BLOCK', description: desc,
    ...protocolOptions(protocol, range)
  }
}

function buildEgress(protocol: string, cidr: string, desc: string, range?: { min: number; max: number }): EgressRuleDTO {
  return {
    protocol, destination: cidr, destinationType: 'CIDR_BLOCK', description: desc,
    ...protocolOptions(protocol, range)
  }
}

function addTemplate(tpl: RuleTemplateDTO) {
  const list = selectedList.value?.item
  if (!list) {
    toast({ tone: 'warning', title: t.networkPage.selectListFirst })
    return
  }

  const commit = () => {
    const ingress = [...(list.ingressSecurityRules ?? [])]
    const egress = [...(list.egressSecurityRules ?? [])]
    // ALL / ICMP 模板的 port 是 0，按"不限端口"处理。
    const range = tpl.port > 0 ? { min: tpl.port, max: tpl.port } : undefined
    if (tpl.direction === 'egress') {
      egress.push(buildEgress(tpl.protocol, '0.0.0.0/0', tpl.description, range))
    } else {
      ingress.push(buildIngress(tpl.protocol, '0.0.0.0/0', tpl.description, range))
    }
    void saveRules(ingress, egress, t.networkPage.templateAdded(tpl.label))
  }

  if (tpl.dangerous) {
    ask({
      level: 2, title: t.networkPage.allowAllTitle,
      body: t.networkPage.allowAllBody,
      okLabel: t.networkPage.addAnyway,
      onConfirm: commit
    })
    return
  }
  commit()
}

/**
 * 一键恢复默认出站规则。
 *
 * 不依赖模板接口是否加载成功：这条是把子网从"完全断网"里捞出来的
 * 应急按钮，正是网络不顺时最可能用到它。
 */
function restoreEgress() {
  const list = selectedList.value?.item
  if (!list) return
  const egress = [
    ...(list.egressSecurityRules ?? []),
    buildEgress('all', '0.0.0.0/0', t.networkPage.defaultEgressDesc)
  ]
  void saveRules(list.ingressSecurityRules ?? [], egress, t.networkPage.egressRestored)
}

/* ---------- 手动规则 ---------- */

/**
 * 手动添加一条规则。
 *
 * 模板覆盖不了的场景太多：只放行自己家的 IP、开一段端口给某个服务、
 * 出站只允许到某个内网段。这里就是给这些情况留的口子。
 */
const manual = reactive({
  direction: 'ingress' as RuleDirection,
  protocol: '6',
  cidr: '0.0.0.0/0',
  ports: '',
  description: ''
})

/** 下拉选项。label 为空表示不画分组标题。 */
const DIRECTION_GROUPS = computed<SelectGroup[]>(() => [{
  label: '',
  options: [
    { value: 'ingress', label: t.networkPage.dir.ingress },
    { value: 'egress', label: t.networkPage.dir.egress }
  ]
}])

const PROTOCOL_GROUPS: SelectGroup[] = [{
  label: '',
  options: [
    { value: '6', label: 'TCP' },
    { value: '17', label: 'UDP' },
    { value: '1', label: 'ICMP' },
    { value: 'all', label: 'ALL' }
  ]
}]

/** 只有 TCP/UDP 有端口。ALL 与 ICMP 的端口框没有意义。 */
const manualHasPorts = computed(() => manual.protocol === '6' || manual.protocol === '17')

/**
 * 校验 CIDR。
 *
 * 在本地拦掉明显的错误，是为了给出"每段取值 0–255"这种人话，而不是把
 * 一句 OCI 的 400 原样丢给用户。真正的判定仍然在服务端。
 */
function cidrError(cidr: string): string {
  const v = cidr.trim()
  if (!v) return t.networkPage.cidrRequired
  const parts = v.split('/')
  if (parts.length !== 2 || parts[1] === '') return t.networkPage.cidrFormat
  const [addr, maskText] = parts
  const mask = Number(maskText)
  if (!Number.isInteger(mask) || mask < 0) return t.networkPage.maskInvalid

  if (addr.includes(':')) {
    if (mask > 128) return t.networkPage.v6MaskMax
    if (!/^[0-9a-fA-F:]+$/.test(addr)) return t.networkPage.v6Invalid
    return ''
  }
  if (mask > 32) return t.networkPage.v4MaskMax
  const octets = addr.split('.')
  if (octets.length !== 4) return t.networkPage.v4Octets
  for (const o of octets) {
    if (!/^[0-9]{1,3}$/.test(o) || Number(o) > 255) return t.networkPage.v4Range(o)
  }
  return ''
}

/** 解析端口输入：空 = 全部端口，22 = 单个，8000-9000 = 区间。 */
function parsePorts(text: string): { min: number; max: number } | null | string {
  const v = text.trim()
  if (!v) return null
  const m = /^([0-9]{1,5})\s*[-~]\s*([0-9]{1,5})$/.exec(v)
  if (m) {
    const min = Number(m[1])
    const max = Number(m[2])
    if (min < 1 || max > 65535) return t.networkPage.portRange
    if (min > max) return t.networkPage.portOrder
    return { min, max }
  }
  if (!/^[0-9]{1,5}$/.test(v)) return t.networkPage.portFormat
  const port = Number(v)
  if (port < 1 || port > 65535) return t.networkPage.portRange
  return { min: port, max: port }
}

/** 空串表示当前输入可以提交。 */
const manualError = computed(() => {
  const bad = cidrError(manual.cidr)
  if (bad) return bad
  if (!manualHasPorts.value) return ''
  const ports = parsePorts(manual.ports)
  return typeof ports === 'string' ? ports : ''
})

/** 提交前把这条规则用人话念一遍——方向或 CIDR 写错的代价是断网或暴露端口。 */
const manualPreview = computed(() => {
  const dir = t.networkPage.dir[manual.direction]
  const proto = PROTO_NAME[manual.protocol] ?? manual.protocol
  const ports = manualHasPorts.value ? (manual.ports.trim() || t.networkPage.allPorts) : t.networkPage.allPorts
  const side = manual.direction === 'egress' ? t.networkPage.target : t.networkPage.source
  return `${dir} · ${proto} · ${side} ${manual.cidr.trim() || '?'} · ${ports}`
})

function addManualRule() {
  const list = selectedList.value?.item
  if (!list) {
    toast({ tone: 'warning', title: t.networkPage.selectListFirst })
    return
  }
  if (manualError.value) {
    toast({ tone: 'warning', title: manualError.value })
    return
  }

  const cidr = manual.cidr.trim()
  const parsed = manualHasPorts.value ? parsePorts(manual.ports) : null
  const range = typeof parsed === 'string' ? null : parsed
  const desc = manual.description.trim()

  const commit = () => {
    const ingress = [...(list.ingressSecurityRules ?? [])]
    const egress = [...(list.egressSecurityRules ?? [])]
    if (manual.direction === 'egress') {
      egress.push(buildEgress(manual.protocol, cidr, desc, range ?? undefined))
    } else {
      ingress.push(buildIngress(manual.protocol, cidr, desc, range ?? undefined))
    }
    // 等保存结果再清空：失败时（常见是权限不足）用户还要拿这段描述重试，提前清掉就找不回了。
    void saveRules(ingress, egress, t.networkPage.customAdded).then(ok => {
      if (ok) manual.description = ''
    })
  }

  // 入站 + 全网 + 不限端口 = 把所有端口挂到公网上，和模板里那条危险规则等价。
  // 1-65535 写法不同、效果一样，必须同样拦下来——判定与后端 IsAllowAllRule 保持一致。
  const allPorts = !range || (range.min <= 1 && range.max >= 65535)
  const wideOpen = manual.direction === 'ingress'
    && (cidr === '0.0.0.0/0' || cidr === '::/0')
    && (manual.protocol === 'all' || (manualHasPorts.value && allPorts))

  if (wideOpen) {
    ask({
      level: 2, title: t.networkPage.wideOpenTitle,
      body: t.networkPage.wideOpenBody(manualPreview.value, cidr),
      okLabel: t.networkPage.addAnyway,
      onConfirm: commit
    })
    return
  }
  commit()
}

function removeRule(row: RuleRow) {
  const list = selectedList.value?.item
  if (!list) return

  ask({
    level: 2, title: t.networkPage.deleteRuleTitle,
    body: t.networkPage.deleteRuleBody(`${t.networkPage.dir[row.dir]} ${row.proto} ${row.cidr} ${row.ports}`),
    okLabel: t.networkPage.deleteRule,
    onConfirm: () => {
      const ingress = [...(list.ingressSecurityRules ?? [])]
      const egress = [...(list.egressSecurityRules ?? [])]
      if (row.dir === 'ingress') ingress.splice(row.index, 1)
      else egress.splice(row.index, 1)
      void saveRules(ingress, egress, t.networkPage.ruleDeleted)
    }
  })
}

/* ---------- 公网 IP ---------- */

interface PublicIpRow {
  key: string
  publicIpId?: string
  accountId: string
  region: string
  ipAddress: string
  lifetime: string
  instanceId: string
  instanceName: string
  instanceState: string
  vnicId?: string
  vnicName?: string
  isPrimaryVnic: boolean
  privateIpId?: string
  privateIp?: string
  isPrimaryPrivateIp: boolean
}

const changingIpKey = ref('')
const unassigningIpKey = ref('')
const switchingEphemeralKey = ref('')
const switchingReservedRow = ref<PublicIpRow | null>(null)
const switchReservedId = ref('')
const quickCreatingSwitchReserved = ref(false)
const submittingSwitchReserved = ref(false)
const ipBatchMode = ref(false)
const selectedIpKeys = ref<Set<string>>(new Set())
const batchChangingIp = ref(false)
let batchReloadTimer = 0

const publicIps = computed<PublicIpRow[]>(() => {
  const instMap = new Map(state.instances.map(i => [i.id, i]))
  const rows: PublicIpRow[] = []

  for (const entry of boundPublicIps.value) {
    const item = entry.item
    if (!item.ipAddress || !item.instanceId) continue
    const live = instMap.get(item.instanceId)
    const instState = live?.state ?? item.instanceState ?? 'RUNNING'
    if (instState === 'TERMINATED') continue
    rows.push({
      key: `${entry.accountId}|${entry.region}|${item.id || item.ipAddress}`,
      publicIpId: item.lifetime === 'RESERVED' && item.id?.startsWith('ocid1.') ? item.id : undefined,
      accountId: entry.accountId,
      region: entry.region,
      ipAddress: item.ipAddress,
      lifetime: item.lifetime || 'EPHEMERAL',
      instanceId: item.instanceId,
      instanceName: live?.name ?? item.instanceName ?? '—',
      instanceState: instState,
      vnicId: item.vnicId,
      vnicName: item.vnicName,
      isPrimaryVnic: item.isPrimaryVnic !== false,
      privateIpId: item.privateIpId || undefined,
      privateIp: item.privateIp || undefined,
      isPrimaryPrivateIp: item.isPrimaryPrivateIp !== false
    })
  }

  for (const i of visibleInstances.value) {
    if (i.state === 'TERMINATED' || i.publicIp === '—') continue
    if (loadedPublicIpTargets.value.has(`${i.accountId}|${i.region}`)) continue
    rows.push({
      key: `${i.accountId}|${i.region}|primary:${i.id}`,
      accountId: i.accountId,
      region: i.region,
      ipAddress: i.publicIp,
      lifetime: 'EPHEMERAL',
      instanceId: i.id,
      instanceName: i.name,
      instanceState: i.state,
      isPrimaryVnic: true,
      privateIp: i.privateIp !== '—' ? i.privateIp : undefined,
      isPrimaryPrivateIp: true
    })
  }

  return rows
})

/** 按当前列表展示顺序排列的已选公网 IP（复制和批量提交都沿用页面顺序）。 */
const orderedPublicIps = computed(() => ipGroups.value.flatMap(g => g.rows))
const selectedPublicIps = computed(() =>
  orderedPublicIps.value.filter(r => selectedIpKeys.value.has(r.key))
)
const allPublicIpsSelected = computed(() =>
  publicIps.value.length > 0 && selectedPublicIps.value.length === publicIps.value.length
)
const somePublicIpsSelected = computed(() =>
  selectedPublicIps.value.length > 0 && selectedPublicIps.value.length < publicIps.value.length
)

watch(publicIps, rows => {
  if (!ipBatchMode.value) return
  const valid = new Set(rows.map(r => r.key))
  const next = new Set<string>()
  for (const k of selectedIpKeys.value) {
    if (valid.has(k)) next.add(k)
  }
  selectedIpKeys.value = next
  if (rows.length === 0) ipBatchMode.value = false
})

function toggleIpBatchMode() {
  if (ipBatchMode.value) {
    ipBatchMode.value = false
    selectedIpKeys.value = new Set()
    return
  }
  if (publicIps.value.length === 0) return
  switchingReservedRow.value = null
  ipBatchMode.value = true
  // 进入批量模式默认全选当前列表中的所有公网 IP。
  selectedIpKeys.value = new Set(publicIps.value.map(r => r.key))
}

function toggleSelectAllIps() {
  if (allPublicIpsSelected.value) {
    selectedIpKeys.value = new Set()
  } else {
    selectedIpKeys.value = new Set(publicIps.value.map(r => r.key))
  }
}

function toggleSelectIpRow(key: string) {
  const next = new Set(selectedIpKeys.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  selectedIpKeys.value = next
}

async function copySelectedPublicIps() {
  const rows = selectedPublicIps.value
  if (rows.length === 0) return
  const text = rows.map(r => r.ipAddress).join('\n')
  await copy(text)
  toast({ tone: 'success', title: t.networkPage.batchCopyDone(rows.length) })
}

function scheduleBatchIpReload() {
  window.clearTimeout(batchReloadTimer)
  batchReloadTimer = window.setTimeout(() => {
    void Promise.all([reloadIpLists(), refreshInstances()])
  }, 400)
}

onBeforeUnmount(() => {
  window.clearTimeout(batchReloadTimer)
})

function batchChangeSelectedIps() {
  const rows = [...selectedPublicIps.value]
  if (rows.length === 0 || batchChangingIp.value) return
  const reservedRows = rows.filter(r => r.lifetime === 'RESERVED' || !r.isPrimaryPrivateIp)
  const ephemeralCount = rows.length - reservedRows.length

  const run = async () => {
    batchChangingIp.value = true
    let ok = 0
    let fail = 0
    let lastErr: unknown = null
    try {
      for (const row of rows) {
        const reserved = row.lifetime === 'RESERVED' || !row.isPrimaryPrivateIp
        const isSecondaryPrivate = !row.isPrimaryPrivateIp
        try {
          const { job } = await instancesApi.changeIp(row.instanceId, {
            vnicId: row.isPrimaryVnic ? undefined : row.vnicId,
            privateIpId: isSecondaryPrivate ? row.privateIpId : undefined,
            allowReserved: reserved,
            confirm: reserved ? row.ipAddress : 'true',
            expectPublicIpId: row.publicIpId,
            expectPublicIp: row.ipAddress
          })
          ok++
          trackJob(job, {
            quiet: true,
            onSuccess: () => { scheduleBatchIpReload() }
          })
        } catch (err) {
          fail++
          lastErr = err
        }
      }
      if (ok > 0 && fail === 0) {
        toast({
          tone: 'accent',
          title: t.networkPage.batchSubmitted(ok),
          link: { label: t.common.viewJob, to: '/jobs' }
        })
      } else if (ok > 0) {
        toast({
          tone: 'warning',
          title: t.networkPage.batchSubmittedPartial(ok, fail),
          body: lastErr ? errorText(lastErr) : undefined,
          link: { label: t.common.viewJob, to: '/jobs' }
        })
      } else if (lastErr) {
        toastError(t.networkPage.batchSubmitFailed, lastErr)
      }
    } finally {
      batchChangingIp.value = false
    }
  }

  if (reservedRows.length > 0) {
    const losses = reservedRows.slice(0, 8).map(r => t.networkPage.reservedIpLoss(shownIp(r.ipAddress), r.instanceName))
    if (reservedRows.length > 8) {
      losses.push(`… +${reservedRows.length - 8}`)
    }
    ask({
      level: 3,
      title: t.networkPage.batchChangeTitle(rows.length),
      body: t.networkPage.batchChangeReservedBody(rows.length, reservedRows.length, ephemeralCount),
      noun: t.networkPage.batchConfirmNoun(rows.length),
      nounLabel: t.networkPage.batchConfirmLabel,
      losses,
      okLabel: t.networkPage.batchChangeIps,
      onConfirm: run
    })
    return
  }

  ask({
    level: 2,
    title: t.networkPage.batchChangeTitle(rows.length),
    body: t.networkPage.batchChangeBody(rows.length),
    okLabel: t.networkPage.batchChangeIps,
    onConfirm: run
  })
}
function ipTagInfo(row: PublicIpRow): { label: string; tone: 'sec-reserved' | 'reserved' | 'secondary' | 'ephemeral'; title?: string } {
  const reserved = row.lifetime === 'RESERVED'
  if (!row.isPrimaryPrivateIp) {
    const parts = [row.privateIp ? shownIp(row.privateIp) : '', !row.isPrimaryVnic && row.vnicName ? row.vnicName : ''].filter(Boolean)
    return {
      label: t.networkPage.ipTagSecondaryReserved,
      tone: 'sec-reserved',
      title: parts.length ? parts.join(' · ') : undefined
    }
  }
  if (!row.isPrimaryVnic) {
    return {
      label: reserved ? t.networkPage.ipTagSecondaryVnicReserved : t.networkPage.ipTagSecondaryVnic,
      tone: reserved ? 'reserved' : 'secondary',
      title: row.vnicName || undefined
    }
  }
  if (reserved) {
    return { label: t.networkPage.ipTagReserved, tone: 'reserved' }
  }
  return { label: t.networkPage.ipTagEphemeral, tone: 'ephemeral' }
}

function changeIp(row: PublicIpRow) {
  const reserved = row.lifetime === 'RESERVED' || !row.isPrimaryPrivateIp
  const isSecondaryPrivate = !row.isPrimaryPrivateIp
  const ipText = shownIp(row.ipAddress)
  const privText = row.privateIp ? shownIp(row.privateIp) : ''
  const title = isSecondaryPrivate
    ? t.networkPage.changeSecondaryReservedTitle(ipText)
    : reserved
      ? t.networkPage.changeReservedTitle(ipText)
      : t.networkPage.changeIpTitle(ipText)
  const body = isSecondaryPrivate
    ? t.networkPage.changeSecondaryReservedBody(row.instanceName, privText)
    : reserved
      ? t.networkPage.changeReservedBody(row.instanceName)
      : t.networkPage.changeIpBody(row.instanceName)

  const run = async () => {
    changingIpKey.value = row.key
    try {
      const { job } = await instancesApi.changeIp(row.instanceId, {
        vnicId: row.isPrimaryVnic ? undefined : row.vnicId,
        privateIpId: isSecondaryPrivate ? row.privateIpId : undefined,
        allowReserved: reserved,
        confirm: reserved ? row.ipAddress : 'true',
        expectPublicIpId: row.publicIpId,
        expectPublicIp: row.ipAddress
      })
      trackJob(job, {
        onSuccess: (finished) => {
          const res = finished.result as ChangeIpDTO | undefined
          if (res?.newIp) {
            toast({
              tone: 'success',
              title: t.networkPage.ipChangedTitle(row.instanceName),
              body: res.oldIp ? t.networkPage.ipChangedBody(shownIp(res.oldIp), shownIp(res.newIp)) : shownIp(res.newIp)
            })
          }
          void Promise.all([reloadIpLists(), refreshInstances()])
        }
      })
    } catch (err) {
      toastError(t.networkPage.changeIpFailed, err)
    } finally {
      changingIpKey.value = ''
    }
  }

  if (reserved) {
    ask({
      level: 3,
      title,
      body,
      noun: row.ipAddress,
      nounLabel: t.networkPage.ipAddressLabel,
      losses: [t.networkPage.reservedIpLoss(ipText)],
      okLabel: t.networkPage.changeIp,
      onConfirm: run
    })
    return
  }

  ask({
    level: 2,
    title,
    body,
    okLabel: t.networkPage.changeIp,
    onConfirm: run
  })
}

function rowIpBusy(row: PublicIpRow) {
  return changingIpKey.value === row.key
    || unassigningIpKey.value === row.key
    || switchingEphemeralKey.value === row.key
    || submittingSwitchReserved.value
    || batchChangingIp.value
}

function publicRowTargetLabel(row: PublicIpRow) {
  const parts = [row.instanceName]
  if (!row.isPrimaryVnic && row.vnicName) parts.push(row.vnicName)
  if (row.privateIp) parts.push(shownIp(row.privateIp))
  return parts.join(' · ')
}

const freeReservedForSwitch = computed(() => {
  const row = switchingReservedRow.value
  if (!row) return []
  return reservedPublicIps.value
    .filter(e =>
      e.accountId === row.accountId &&
      e.region === row.region &&
      !e.item.privateIpId &&
      !e.item.assignedEntityId &&
      (!e.item.lifecycleState || e.item.lifecycleState === 'AVAILABLE')
    )
    .map(e => e.item)
})

const freeReservedSwitchGroups = computed<SelectGroup[]>(() => [{
  label: '',
  options: freeReservedForSwitch.value.map(ip => ({
    value: ip.id,
    label: ip.displayName ? `${shownIp(ip.ipAddress)} · ${ip.displayName}` : shownIp(ip.ipAddress)
  }))
}])

function openSwitchReserved(row: PublicIpRow) {
  if (switchingReservedRow.value?.key === row.key) {
    switchingReservedRow.value = null
    return
  }
  switchingReservedRow.value = row
  switchReservedId.value = freeReservedForSwitch.value[0]?.id ?? ''
  if (!reservedIpLoaded.value) {
    void loadReservedIps().then(() => {
      if (switchingReservedRow.value?.key === row.key && !switchReservedId.value) {
        switchReservedId.value = freeReservedForSwitch.value[0]?.id ?? ''
      }
    })
  }
}

async function quickCreateSwitchReserved() {
  const row = switchingReservedRow.value
  if (!row) return
  quickCreatingSwitchReserved.value = true
  try {
    const created = await network.createReservedIp(row.accountId, row.region)
    toast({ tone: 'success', title: t.networkPage.createReservedDone(shownIp(created.ipAddress)) })
    reservedPublicIps.value = [{ accountId: row.accountId, region: row.region, item: created }, ...reservedPublicIps.value]
    switchReservedId.value = created.id
  } catch (err) {
    toastError(t.networkPage.createReservedFailed, err)
  } finally {
    quickCreatingSwitchReserved.value = false
  }
}

function submitSwitchReserved() {
  const row = switchingReservedRow.value
  const chosen = freeReservedForSwitch.value.find(ip => ip.id === switchReservedId.value)
  if (!row || !chosen) return
  const body = row.lifetime === 'RESERVED'
    ? t.networkPage.bindConfirmReserved(shownIp(row.ipAddress))
    : t.networkPage.bindConfirmEphemeral(shownIp(row.ipAddress))
  ask({
    level: 2,
    title: t.networkPage.bindConfirmTitle(shownIp(chosen.ipAddress), row.instanceName),
    body,
    okLabel: t.networkPage.confirmBind,
    onConfirm: async () => {
      submittingSwitchReserved.value = true
      try {
        const { job } = await instancesApi.bindReservedIp(row.instanceId, {
          reservedIpId: chosen.id,
          vnicId: row.isPrimaryVnic ? undefined : row.vnicId,
          privateIpId: !row.isPrimaryPrivateIp ? row.privateIpId : undefined,
          expectPublicIpId: row.publicIpId,
          expectPublicIp: row.ipAddress
        })
        switchingReservedRow.value = null
        trackJob(job, {
          onSuccess: (finished) => {
            const res = finished.result as BindReservedIpDTO | undefined
            toast({ tone: 'success', title: t.networkPage.bindDone(shownIp(res?.newIp || chosen.ipAddress), row.instanceName) })
            void Promise.all([reloadIpLists(), refreshInstances()])
          }
        })
      } catch (err) {
        toastError(t.networkPage.bindFailed, err)
      } finally {
        submittingSwitchReserved.value = false
      }
    }
  })
}

function switchToEphemeral(row: PublicIpRow) {
  if (row.lifetime !== 'RESERVED' || !row.isPrimaryPrivateIp) return
  ask({
    level: 2,
    title: t.networkPage.switchEphemeralTitle(shownIp(row.ipAddress)),
    body: t.networkPage.switchEphemeralBody(publicRowTargetLabel(row), shownIp(row.ipAddress)),
    okLabel: t.networkPage.switchEphemeral,
    onConfirm: async () => {
      switchingEphemeralKey.value = row.key
      try {
        const { job } = await instancesApi.switchEphemeralIp(row.instanceId, {
          vnicId: row.isPrimaryVnic ? undefined : row.vnicId,
          expectPublicIpId: row.publicIpId,
          expectPublicIp: row.ipAddress
        })
        if (switchingReservedRow.value?.key === row.key) switchingReservedRow.value = null
        trackJob(job, {
          onSuccess: (finished) => {
            const res = finished.result as SwitchEphemeralIpDTO | undefined
            toast({ tone: 'success', title: t.networkPage.switchEphemeralDone(row.instanceName, shownIp(res?.newIp || '')) })
            void Promise.all([reloadIpLists(), refreshInstances()])
          }
        })
      } catch (err) {
        toastError(t.networkPage.switchEphemeralFailed, err)
      } finally {
        switchingEphemeralKey.value = ''
      }
    }
  })
}

function unassignPublicIp(row: PublicIpRow) {
  const reserved = row.lifetime === 'RESERVED'
  const target = publicRowTargetLabel(row)
  ask({
    level: 2,
    title: reserved ? t.networkPage.unbindTitle(shownIp(row.ipAddress)) : t.networkPage.unassignEphemeralTitle(shownIp(row.ipAddress)),
    body: reserved ? t.networkPage.unbindBody(target) : t.networkPage.unassignEphemeralBody(target),
    okLabel: reserved ? t.networkPage.unbindIp : t.networkPage.releaseIp,
    onConfirm: async () => {
      unassigningIpKey.value = row.key
      try {
        const res = await instancesApi.unassignIp(row.instanceId, {
          vnicId: row.isPrimaryVnic ? undefined : row.vnicId,
          privateIpId: !row.isPrimaryPrivateIp ? row.privateIpId : undefined,
          expectPublicIpId: row.publicIpId,
          expectPublicIp: row.ipAddress
        })
        toast({
          tone: 'success',
          title: res.lifetime === 'RESERVED'
            ? t.networkPage.unbindDone(shownIp(res.oldIp))
            : t.networkPage.unassignEphemeralDone(shownIp(res.oldIp))
        })
        if (switchingReservedRow.value?.key === row.key) switchingReservedRow.value = null
        await Promise.all([reloadIpLists(), refreshInstances()])
      } catch (err) {
        toastError(t.networkPage.unassignFailed, err)
      } finally {
        unassigningIpKey.value = ''
      }
    }
  })
}

/* ---------- 预留 IP 管理 ---------- */

interface ReservedIpRow {
  id: string
  accountId: string
  region: string
  ipAddress: string
  displayName: string
  lifecycleState: string
  assigned: boolean
  instanceId?: string
  instanceName?: string
  instanceState?: string
  vnicId?: string
  vnicName?: string
  isPrimaryVnic: boolean
  privateIp?: string
  isPrimaryPrivateIp: boolean
}

const reservedIps = computed<ReservedIpRow[]>(() => {
  const instMap = new Map(state.instances.map(i => [i.id, i]))
  return reservedPublicIps.value.map(({ accountId, region, item }) => {
    const live = item.instanceId ? instMap.get(item.instanceId) : undefined
    const assigned = !!(item.privateIpId || item.assignedEntityId)
    return {
      id: item.id,
      accountId,
      region,
      ipAddress: item.ipAddress,
      displayName: item.displayName || '',
      lifecycleState: item.lifecycleState || '',
      assigned,
      instanceId: item.instanceId,
      instanceName: live?.name ?? item.instanceName,
      instanceState: live?.state ?? item.instanceState,
      vnicId: item.vnicId,
      vnicName: item.vnicName,
      isPrimaryVnic: item.isPrimaryVnic !== false,
      privateIp: item.privateIp,
      isPrimaryPrivateIp: item.isPrimaryPrivateIp !== false
    }
  })
})

const createReservedOpen = ref(false)
const creatingReserved = ref(false)
const createReservedForm = reactive({ targetKey: '', displayName: '' })

const targetMenuGroups = computed<SelectGroup[]>(() => {
  const byAcct = new Map<string, string[]>()
  for (const trg of targets.value) {
    const list = byAcct.get(trg.accountId)
    if (list) list.push(trg.region)
    else byAcct.set(trg.accountId, [trg.region])
  }
  return [...byAcct.entries()].map(([accountId, regions]) => {
    const a = accountById(accountId)
    return {
      label: `${a.code} · ${a.alias}`,
      options: regions.map(region => ({
        value: `${accountId}|${region}`,
        label: `${a.alias} · ${region}`,
        dot: acctColor(a.colorIndex)
      }))
    }
  })
})

function openCreateReserved() {
  createReservedOpen.value = !createReservedOpen.value
  if (createReservedOpen.value) {
    if (!createReservedForm.targetKey || !targets.value.some(x => `${x.accountId}|${x.region}` === createReservedForm.targetKey)) {
      const first = targets.value[0]
      createReservedForm.targetKey = first ? `${first.accountId}|${first.region}` : ''
    }
    createReservedForm.displayName = ''
  }
}

async function submitCreateReserved() {
  const [accountId, region] = createReservedForm.targetKey.split('|')
  if (!accountId || !region) return
  creatingReserved.value = true
  try {
    const created = await network.createReservedIp(accountId, region, createReservedForm.displayName.trim())
    createReservedOpen.value = false
    toast({ tone: 'success', title: t.networkPage.createReservedDone(shownIp(created.ipAddress)) })
    await reloadIpLists()
  } catch (err) {
    toastError(t.networkPage.createReservedFailed, err)
  } finally {
    creatingReserved.value = false
  }
}

const reservedBusyId = ref('')

function unbindReserved(row: ReservedIpRow) {
  const target = row.instanceName
    ? `${row.instanceName}${row.privateIp ? ` (${shownIp(row.privateIp)})` : ''}`
    : t.networkPage.reservedBoundOther
  ask({
    level: 2,
    title: t.networkPage.unbindTitle(shownIp(row.ipAddress)),
    body: t.networkPage.unbindBody(target),
    okLabel: t.networkPage.unbind,
    onConfirm: async () => {
      reservedBusyId.value = row.id
      try {
        await network.unbindReservedIp(row.id, row.accountId, row.region)
        toast({ tone: 'success', title: t.networkPage.unbindDone(shownIp(row.ipAddress)) })
        await Promise.all([reloadIpLists(), refreshInstances()])
      } catch (err) {
        toastError(t.networkPage.unbindFailed, err)
      } finally {
        reservedBusyId.value = ''
      }
    }
  })
}

function releaseReserved(row: ReservedIpRow) {
  const ipText = shownIp(row.ipAddress)
  ask({
    level: 3,
    title: t.networkPage.releaseTitle(ipText),
    body: t.networkPage.releaseBody,
    noun: row.ipAddress,
    nounLabel: t.networkPage.ipAddressLabel,
    losses: [t.networkPage.reservedIpLoss(ipText, row.displayName)],
    okLabel: t.networkPage.release,
    onConfirm: async () => {
      reservedBusyId.value = row.id
      try {
        await accountsApi.releaseReservedIp(row.accountId, row.id, row.region, row.ipAddress)
        toast({ tone: 'success', title: t.networkPage.releaseDone(ipText) })
        await reloadIpLists()
      } catch (err) {
        toastError(t.networkPage.releaseFailed, err)
      } finally {
        reservedBusyId.value = ''
      }
    }
  })
}

interface PrivateSlotOption {
  key: string
  kind: 'primary' | 'secondaryPrivate' | 'secondaryVnicPrimary' | 'secondaryVnicPrivate'
  vnicName: string
  privateIp: string
  vnicId?: string
  privateIpId?: string
  currentIp: string
  currentPublicIpId: string
  currentType: string
}

const bindingReserved = ref<ReservedIpRow | null>(null)
const bindInstId = ref('')
const bindSlotKey = ref('')
const bindSlotsLoading = ref(false)
const bindSlots = ref<PrivateSlotOption[]>([])
const submittingBind = ref(false)
let bindDetailSeq = 0

const bindInstOptions = computed<SelectGroup[]>(() => {
  const target = bindingReserved.value
  if (!target) return []
  const candidates = state.instances.filter(i =>
    i.accountId === target.accountId && i.region === target.region && i.state !== 'TERMINATED'
  )
  return [{
    label: '',
    options: candidates.map(i => ({
      value: i.id,
      label: `${i.name} (${i.publicIp !== '—' ? shownIp(i.publicIp) : shownIp(i.privateIp)})`
    }))
  }]
})

const bindSlotGroups = computed<SelectGroup[]>(() => [{
  label: '',
  options: bindSlots.value.map(s => {
    const priv = shownIp(s.privateIp)
    const base = s.kind === 'primary'
      ? t.networkPage.slotPrimary(priv)
      : s.kind === 'secondaryPrivate'
        ? t.networkPage.slotSecondaryPrivate(priv)
        : s.kind === 'secondaryVnicPrimary'
          ? t.networkPage.slotSecondaryVnicPrimary(s.vnicName, priv)
          : t.networkPage.slotSecondaryVnicPrivate(s.vnicName, priv)
    const cur = !s.currentIp
      ? t.networkPage.slotCurrentNone
      : s.currentType === 'RESERVED'
        ? t.networkPage.slotCurrentReserved(shownIp(s.currentIp))
        : t.networkPage.slotCurrentEphemeral(shownIp(s.currentIp))
    return { value: s.key, label: base + cur }
  })
}])

function buildSlotOptions(vnics: VnicInfoDTO[]): PrivateSlotOption[] {
  const out: PrivateSlotOption[] = []
  for (const v of vnics) {
    const vnicName = v.displayName || v.vnicId.slice(-6)
    if (!v.publicIpUnknown) {
      out.push({
        key: `vnic:${v.vnicId}:primary`,
        kind: v.isPrimary ? 'primary' : 'secondaryVnicPrimary',
        vnicName,
        privateIp: v.privateIp,
        vnicId: v.isPrimary ? undefined : v.vnicId,
        privateIpId: undefined,
        currentIp: v.publicIp || '',
        currentPublicIpId: v.publicIpId || '',
        currentType: v.publicIpType || (v.publicIp ? 'EPHEMERAL' : '')
      })
    }
    for (const sip of v.secondaryIps ?? []) {
      if (sip.publicIpUnknown) continue
      out.push({
        key: `priv:${sip.privateIpId}`,
        kind: v.isPrimary ? 'secondaryPrivate' : 'secondaryVnicPrivate',
        vnicName,
        privateIp: sip.privateIp,
        vnicId: v.isPrimary ? undefined : v.vnicId,
        privateIpId: sip.privateIpId,
        currentIp: sip.publicIp || '',
        currentPublicIpId: sip.publicIpId || '',
        currentType: sip.publicIpType || (sip.publicIp ? 'RESERVED' : '')
      })
    }
  }
  return out
}

async function loadBindSlots(instanceId: string) {
  const seq = ++bindDetailSeq
  bindSlots.value = []
  bindSlotKey.value = ''
  if (!instanceId) return
  bindSlotsLoading.value = true
  try {
    const detail = await instancesApi.detail(instanceId)
    if (seq !== bindDetailSeq) return
    bindSlots.value = buildSlotOptions(detail.vnics ?? [])
    bindSlotKey.value = bindSlots.value[0]?.key ?? ''
  } catch (err) {
    if (seq === bindDetailSeq) toastError(t.networkPage.bindFailed, err)
  } finally {
    if (seq === bindDetailSeq) bindSlotsLoading.value = false
  }
}

function openBindPanel(row: ReservedIpRow) {
  if (bindingReserved.value?.id === row.id) {
    bindingReserved.value = null
    return
  }
  bindingReserved.value = row
  const first = state.instances.find(i =>
    i.accountId === row.accountId && i.region === row.region && i.state !== 'TERMINATED'
  )
  bindInstId.value = first?.id ?? ''
  void loadBindSlots(bindInstId.value)
}

watch(bindInstId, id => {
  if (bindingReserved.value) void loadBindSlots(id)
})

function submitBindReserved() {
  const row = bindingReserved.value
  const inst = state.instances.find(i => i.id === bindInstId.value)
  const slot = bindSlots.value.find(s => s.key === bindSlotKey.value)
  if (!row || !inst || !slot) return
  const body = !slot.currentIp
    ? t.networkPage.bindConfirmEmpty
    : slot.currentType === 'RESERVED'
      ? t.networkPage.bindConfirmReserved(shownIp(slot.currentIp))
      : t.networkPage.bindConfirmEphemeral(shownIp(slot.currentIp))
  ask({
    level: 2,
    title: t.networkPage.bindConfirmTitle(shownIp(row.ipAddress), inst.name),
    body,
    okLabel: t.networkPage.confirmBind,
    onConfirm: async () => {
      submittingBind.value = true
      try {
        const { job } = await instancesApi.bindReservedIp(inst.id, {
          reservedIpId: row.id,
          vnicId: slot.vnicId,
          privateIpId: slot.privateIpId,
          expectPublicIpId: slot.currentPublicIpId,
          expectPublicIp: slot.currentIp
        })
        bindingReserved.value = null
        trackJob(job, {
          onSuccess: (finished) => {
            const res = finished.result as BindReservedIpDTO | undefined
            toast({ tone: 'success', title: t.networkPage.bindDone(shownIp(res?.newIp || row.ipAddress), inst.name) })
            void Promise.all([reloadIpLists(), refreshInstances()])
          }
        })
      } catch (err) {
        toastError(t.networkPage.bindFailed, err)
      } finally {
        submittingBind.value = false
      }
    }
  })
}

/* ---------- IPv6 ---------- */

const ipv6Prefixes = computed(() =>
  vcns.value
    .filter(v => (v.item.ipv6CidrBlocks?.length ?? 0) > 0)
    .flatMap(v => (v.item.ipv6CidrBlocks ?? []).map(prefix => ({
      accountId: v.accountId,
      region: v.region,
      vcn: v.item.displayName,
      prefix,
      assigned: subnets.value.filter(s => s.item.vcnId === v.item.id && s.item.ipv6CidrBlock).length
    })))
)

const subnetsOfVcn = (vcnId: string) => subnets.value.filter(s => s.item.vcnId === vcnId).length

const instancesIn = (accountId: string, region: string) =>
  state.instances.filter(i => i.accountId === accountId && i.region === region && i.state !== 'TERMINATED').length

onMounted(async () => {
  templates.value = (await network.ruleTemplates().catch(() => ({ templates: [] }))).templates
  await loadNetwork()
})

// 账号筛选变了就重新拉——否则会显示已被筛掉的账号的网络。
// 账号与区域两个筛选器都要触发重拉。只盯账号的话，改区域筛选后
// targets 变了但没人去拉，页面停在旧数据上——看起来像筛选器坏了。
watch(
  () => [[...state.accountFilter].join(','), [...state.regionFilter].join(',')].join('|'),
  () => void loadNetwork()
)

watch(
  () => state.networkChanged.at,
  (at) => { if (at > 0) void loadNetwork() }
)

watch(active, tab => {
  if (tab !== 'publicIp') {
    switchingReservedRow.value = null
    ipBatchMode.value = false
    selectedIpKeys.value = new Set()
  }
  if (tab !== 'reservedIp') {
    bindingReserved.value = null
    createReservedOpen.value = false
  }
  if (tab === 'publicIp' && !publicIpLoaded.value && !publicIpsLoading.value) void loadPublicIps()
  if (tab === 'reservedIp' && !reservedIpLoaded.value && !reservedIpsLoading.value) void loadReservedIps()
})
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">{{ t.networkPage.title }}</h1>
        <p class="page-sub">
          {{ t.networkPage.subtitle(vcns.length, subnets.length, securityLists.length) }}
        </p>
      </div>
      <div class="net-actions">
        <!-- 只在分组真正生效的页签上出现。安全规则页是"编辑单个安全列表"，
             公网 IP 与 IPv6 是扁平清单，分组对它们没有意义；开关一直挂着
             却点了没反应，比没有更糟。 -->
        <button v-if="groupable" class="pill" :class="{ 'is-active': state.groupByAccount }"
                @click="state.groupByAccount = !state.groupByAccount">{{ t.networkPage.groupByAccount }}</button>
        <button class="btn" :disabled="loading" @click="loadNetwork">
          <span v-if="loading" class="btn-spin" aria-hidden="true" />{{ loading ? t.networkPage.loadingShort : t.common.refresh }}
        </button>
      </div>
    </header>
    <LoadingBar :active="loading && hasNet" />

    <PageTabs :tabs="TABS" :labels="t.networkPage.tabs" v-model:active="active" />

    <p v-if="loadError" class="net-warn">{{ loadError }}</p>

    <!-- 外层按标签换 key：切标签时整块淡入淡出；安全规则页有多个根，Transition 需要单一子元素 -->
    <Transition name="swap" mode="out-in">
    <div :key="active" class="stale-able" :class="{ 'is-stale': loading && hasNet }">
    <SectionCard v-if="active === 'vcn'" title="VCN" :note="t.networkPage.vcnNote">
      <SkeletonRows v-if="loading && !hasNet" :rows="3" />
      <EmptyState v-else-if="vcns.length === 0" :title="t.networkPage.noVcns"
                  :body="t.networkPage.noVcnsBody" />
      <template v-else>
        <div class="head cols-vcn">
          <span>{{ t.networkPage.colName }}</span><span>{{ t.networkPage.colAccount }}</span><span>{{ t.networkPage.colRegion }}</span><span>CIDR</span><span>{{ t.networkPage.colSubnets }}</span><span>{{ t.networkPage.colInstances }}</span>
        </div>
        <template v-for="g in vcnGroups" :key="g.key">
        <AccountGroupRow v-if="g.account" :account="g.account" :count="g.rows.length" unit="vcn" />
        <div v-for="v in g.rows" :key="v.accountId + v.item.id" class="row cols-vcn">
          <span class="acct-bar" :style="{ background: acctColor(accountById(v.accountId).colorIndex) }" />
          <span class="mono t-xs">{{ v.item.displayName }}</span>
          <AccountChip :account="accountById(v.accountId)" />
          <span class="mono t-xs dim">{{ v.region }}</span>
          <span class="mono t-xs">{{ v.item.cidrBlock }}</span>
          <span class="mono t-xs dim">{{ t.networkPage.countSubnets(subnetsOfVcn(v.item.id)) }}</span>
          <span class="mono t-xs dim">{{ t.networkPage.countInstances(instancesIn(v.accountId, v.region)) }}</span>
        </div>
        </template>
      </template>
    </SectionCard>

    <SectionCard v-else-if="active === 'subnet'" :title="t.networkPage.subnetTitle">
      <SkeletonRows v-if="loading && !hasNet" :rows="3" />
      <EmptyState v-else-if="subnets.length === 0" :title="t.networkPage.noSubnets" />
      <template v-else>
        <div class="head cols-subnet">
          <span>{{ t.networkPage.colName }}</span><span>{{ t.networkPage.colAccount }}</span><span>{{ t.networkPage.colRegion }}</span><span>CIDR</span><span>{{ t.networkPage.colType }}</span><span>IPv6</span>
        </div>
        <template v-for="g in subnetGroups" :key="g.key">
        <AccountGroupRow v-if="g.account" :account="g.account" :count="g.rows.length" unit="subnet" />
        <div v-for="s in g.rows" :key="s.accountId + s.item.id" class="row cols-subnet">
          <span class="acct-bar" :style="{ background: acctColor(accountById(s.accountId).colorIndex) }" />
          <span class="mono t-xs">{{ s.item.displayName }}</span>
          <AccountChip :account="accountById(s.accountId)" />
          <span class="mono t-xs dim">{{ s.region }}</span>
          <span class="mono t-xs">{{ s.item.cidrBlock }}</span>
          <span class="t-xs" :class="{ dim: s.item.prohibitPublicIpOnVnic }">
            {{ s.item.prohibitPublicIpOnVnic ? t.networkPage.private : t.networkPage.public }}
          </span>
          <span class="mono t-xs dim">{{ s.item.ipv6CidrBlock || '—' }}</span>
        </div>
        </template>
      </template>
    </SectionCard>

    <template v-else-if="active === 'rules'">
      <!-- note 必须写明追加目标：这几个按钮是往某个安全列表里写规则的，
           多账号时不写清楚，用户很可能给错的租户开了一个 0.0.0.0/0 的端口。 -->
      <!-- 没有出站规则 = 该子网完全断网，而实例本身看起来一切正常。
           这种故障排查起来很费劲，界面必须主动说出来并给出一键修复。 -->
      <div v-if="selectedList?.item.noEgress" class="warn-bar">
        <span class="t-xs">
          {{ t.networkPage.noEgressWarn }}
          <strong>{{ t.networkPage.noInternet }}</strong>{{ t.networkPage.noEgressTail }}
        </span>
        <button class="btn btn--sm btn--primary" :disabled="savingRules" @click="restoreEgress">
          {{ t.networkPage.restoreEgress }}
        </button>
      </div>

      <SectionCard :title="t.networkPage.templatesTitle" :note="templateTargetNote">
        <div v-for="tpl in templates" :key="tpl.key" class="row cols-tpl">
          <span class="acct-bar" :style="{ background: tpl.dangerous ? 'var(--danger)' : 'var(--success)' }" />
          <span class="t-xs" :style="{ color: tpl.dangerous ? 'var(--danger)' : 'var(--success)', fontWeight: 600 }">
            {{ tpl.dangerous ? '⚠ ' : '' }}{{ tpl.label }}
          </span>
          <span class="t-xs dim">{{ tpl.description }}</span>
          <span class="mono t-xs dim">
            {{ t.networkPage.dir[tpl.direction === 'egress' ? 'egress' : 'ingress'] }}
            {{ PROTO_NAME[tpl.protocol] ?? tpl.protocol }} 0.0.0.0/0{{ tpl.port ? ' : ' + tpl.port : '' }}
          </span>
          <button class="btn btn--sm" :class="tpl.dangerous ? 'btn--danger' : ''"
                  :disabled="savingRules || !selectedList" @click="addTemplate(tpl)">{{ t.networkPage.add }}</button>
        </div>
      </SectionCard>

      <SectionCard :title="t.networkPage.customTitle" class="mt" :note="templateTargetNote">
        <div class="manual">
          <!-- 用 SelectMenu 而不是原生 <select>：原生下拉展开后的那张列表是
               浏览器画的，CSS 碰不到，深色主题下会弹出一片系统浅色。 -->
          <div class="field">
            <span class="lbl">{{ t.networkPage.direction }}</span>
            <SelectMenu :model-value="manual.direction" :groups="DIRECTION_GROUPS" :min-width="120"
                        :aria-label="t.networkPage.ruleDirection"
                        @update:model-value="manual.direction = $event as RuleDirection" />
          </div>
          <div class="field">
            <span class="lbl">{{ t.networkPage.protocol }}</span>
            <SelectMenu v-model="manual.protocol" :groups="PROTOCOL_GROUPS" :min-width="120"
                        :aria-label="t.networkPage.ruleProtocol" />
          </div>
          <div class="field">
            <label for="rule-cidr">{{ manual.direction === 'egress' ? t.networkPage.targetCidr : t.networkPage.sourceCidr }}</label>
            <input id="rule-cidr" v-model="manual.cidr" class="input mono" placeholder="0.0.0.0/0" />
          </div>
          <div class="field">
            <label for="rule-ports">{{ t.networkPage.ports }}</label>
            <input id="rule-ports" v-model="manual.ports" class="input mono"
                   :disabled="!manualHasPorts"
                   :placeholder="manualHasPorts ? t.networkPage.portsPlaceholder : t.networkPage.noPorts" />
          </div>
          <div class="field manual-desc">
            <label for="rule-desc">{{ t.networkPage.description }}</label>
            <input id="rule-desc" v-model="manual.description" class="input" :placeholder="t.networkPage.descPlaceholder" />
          </div>
        </div>

        <div class="manual-foot">
          <!-- 提交前把这条规则念一遍。方向和 CIDR 写反的后果一个是断网、
               一个是把端口挂上公网，两者都值得多看一眼再点。 -->
          <span class="t-xs" :style="{ color: manualError ? 'var(--danger)' : 'var(--text-secondary)' }">
            {{ manualError || manualPreview }}
          </span>
          <button class="btn btn--sm btn--primary"
                  :disabled="savingRules || !selectedList || !!manualError" @click="addManualRule">
            {{ t.networkPage.addRule }}
          </button>
        </div>
      </SectionCard>

      <SectionCard :title="t.networkPage.listTitle" class="mt"
                   :note="selectedList ? `${accountById(selectedList.accountId).code} · ${selectedList.region}` : ''">
        <div class="picker">
          <span class="t-xs dim">{{ t.networkPage.editTarget }}</span>
          <SelectMenu v-model="selectedListKey" :groups="listGroups" :min-width="360"
                      :aria-label="t.networkPage.editTargetLabel" :placeholder="t.networkPage.noEditable" />
        </div>

        <SkeletonRows v-if="loading && !hasNet" :rows="4" />
        <EmptyState v-else-if="!selectedList" :title="t.networkPage.noLists" />
        <template v-else>
          <div class="head cols-rule">
            <span>{{ t.networkPage.colDir }}</span><span>{{ t.networkPage.colProto }}</span><span>{{ t.networkPage.colCidr }}</span><span>{{ t.networkPage.colPorts }}</span><span>{{ t.networkPage.colDesc }}</span><span />
          </div>
          <div v-for="r in ruleRows" :key="r.dir + r.index" class="row cols-rule">
            <span class="acct-bar" :style="{ background: r.danger ? 'var(--danger)' : 'var(--success)' }" />
            <span class="t-xs" :style="{ color: r.danger ? 'var(--danger)' : 'var(--text-primary)' }">{{ t.networkPage.dir[r.dir] }}</span>
            <span class="mono t-xs">{{ r.proto }}</span>
            <span class="mono t-xs">{{ r.cidr }}</span>
            <span class="mono t-xs">{{ r.ports }}</span>
            <span class="t-xs" :style="{ color: r.danger ? 'var(--danger)' : 'var(--text-secondary)' }">
              {{ r.danger ? t.networkPage.openWarn : r.desc }}
            </span>
            <button class="btn btn--sm btn--danger" :disabled="savingRules" @click="removeRule(r)">{{ t.common.delete }}</button>
          </div>
        </template>
      </SectionCard>
    </template>

    <SectionCard v-else-if="active === 'publicIp'" :title="t.networkPage.ipTitle"
                 :note="t.networkPage.ipNote">
      <template #action>
        <div class="ip-batch-head">
          <template v-if="ipBatchMode">
            <span class="t-xs dim mono">{{ t.networkPage.batchSelected(selectedPublicIps.length, publicIps.length) }}</span>
            <button class="btn btn--sm" :disabled="batchChangingIp" @click="toggleSelectAllIps">
              {{ allPublicIpsSelected ? t.networkPage.deselectAll : t.networkPage.selectAll }}
            </button>
            <button class="btn btn--sm" :disabled="selectedPublicIps.length === 0" @click="copySelectedPublicIps">
              {{ t.networkPage.batchCopyIps }}
            </button>
            <button class="btn btn--sm btn--warning"
                    :disabled="selectedPublicIps.length === 0 || batchChangingIp"
                    @click="batchChangeSelectedIps">
              {{ batchChangingIp ? t.networkPage.batchChanging : t.networkPage.batchChangeIps }}
            </button>
            <button class="btn btn--sm" :disabled="batchChangingIp" @click="toggleIpBatchMode">
              {{ t.networkPage.exitBatch }}
            </button>
          </template>
          <button v-else class="btn btn--sm" :disabled="publicIps.length === 0" @click="toggleIpBatchMode">
            {{ t.networkPage.batchMode }}
          </button>
        </div>
      </template>
      <div v-if="switchingReservedRow" class="inline-form inline-form--switch">
        <div class="inline-form__head t-xs">{{ t.networkPage.switchReservedPanelTitle(publicRowTargetLabel(switchingReservedRow)) }}</div>
        <p class="inline-form__warn t-2xs">
          {{ switchingReservedRow.lifetime === 'RESERVED'
            ? t.networkPage.bindConfirmReserved(shownIp(switchingReservedRow.ipAddress))
            : t.networkPage.bindConfirmEphemeral(shownIp(switchingReservedRow.ipAddress)) }}
        </p>
        <label class="field">
          <span class="lbl">{{ t.networkPage.pickFreeReservedIp }}</span>
          <SelectMenu v-if="freeReservedForSwitch.length > 0" v-model="switchReservedId"
                      :groups="freeReservedSwitchGroups" :label="t.networkPage.pickFreeReservedIp" />
          <span v-else class="t-xs dim">{{ t.networkPage.noFreeReservedInRegion }}</span>
        </label>
        <button class="btn btn--sm" :disabled="quickCreatingSwitchReserved" @click="quickCreateSwitchReserved">
          {{ quickCreatingSwitchReserved ? t.networkPage.creatingReserved : t.networkPage.quickCreateReserved }}
        </button>
        <div class="inline-form__actions">
          <button class="btn btn--sm btn--primary" :disabled="submittingSwitchReserved || !switchReservedId" @click="submitSwitchReserved">
            {{ t.networkPage.confirmBind }}
          </button>
          <button class="btn btn--sm" @click="switchingReservedRow = null">{{ t.common.cancel }}</button>
        </div>
      </div>

      <SkeletonRows v-if="publicIpsLoading && !publicIpLoaded" :rows="2" />
      <EmptyState v-else-if="publicIps.length === 0" :title="t.networkPage.noIps" />
      <template v-else>
        <div class="head" :class="ipBatchMode ? 'cols-ip cols-ip--batch' : 'cols-ip'">
          <label v-if="ipBatchMode" class="ip-check">
            <input type="checkbox" :checked="allPublicIpsSelected" :indeterminate="somePublicIpsSelected"
                   :aria-label="t.networkPage.selectAll" @change="toggleSelectAllIps" />
          </label>
          <span>IP</span><span>{{ t.networkPage.colType }}</span><span>{{ t.networkPage.colAccount }}</span><span>{{ t.networkPage.colRegion }}</span><span>{{ t.networkPage.colBoundInstance }}</span><span>{{ t.networkPage.colState }}</span><span />
        </div>
        <template v-for="g in ipGroups" :key="g.key">
        <AccountGroupRow v-if="g.account" :account="g.account" :count="g.rows.length" unit="publicIp" />
        <div v-for="r in g.rows" :key="r.key" class="row"
             :class="[ipBatchMode ? 'cols-ip cols-ip--batch' : 'cols-ip', { 'is-selected': ipBatchMode && selectedIpKeys.has(r.key) }]">
          <span class="acct-bar" :style="{ background: acctColor(accountById(r.accountId).colorIndex) }" />
          <span v-if="ipBatchMode && selectedIpKeys.has(r.key)" class="sel-bar" />
          <label v-if="ipBatchMode" class="ip-check">
            <input type="checkbox" :checked="selectedIpKeys.has(r.key)"
                   :aria-label="t.networkPage.selectIp(shownIp(r.ipAddress))"
                   @change="toggleSelectIpRow(r.key)" />
          </label>
          <span class="mono t-xs"><MaskedText :value="r.ipAddress" kind="ip" copyable /></span>
          <span class="ip-tag-cell">
            <span class="ip-tag t-2xs" :class="`ip-tag--${ipTagInfo(r).tone}`" :title="ipTagInfo(r).title">
              {{ ipTagInfo(r).label }}
            </span>
          </span>
          <AccountChip :account="accountById(r.accountId)" />
          <span class="mono t-xs dim">{{ r.region }}</span>
          <span class="t-xs" :title="!r.isPrimaryPrivateIp && r.privateIp ? `${r.instanceName} · ${shownIp(r.privateIp)}` : r.instanceName">
            {{ r.instanceName }}<span v-if="!r.isPrimaryPrivateIp && r.privateIp" class="mono dim"> · <MaskedText :value="r.privateIp" kind="ip" /></span>
          </span>
          <span class="t-xs" :style="{ color: r.instanceState === 'STOPPED' ? 'var(--warning)' : 'var(--text-secondary)' }">
            {{ r.instanceState === 'STOPPED' ? t.networkPage.instanceStopped : t.networkPage.bound }}
          </span>
          <span class="ip-acts">
            <button v-if="r.lifetime !== 'RESERVED'" class="btn btn--sm" :disabled="rowIpBusy(r)" :title="t.networkPage.switchReservedHint" @click="openSwitchReserved(r)">
              {{ t.networkPage.switchReserved }}
            </button>
            <button v-if="r.lifetime === 'RESERVED' && r.isPrimaryPrivateIp" class="btn btn--sm"
                    :disabled="rowIpBusy(r)" :title="t.networkPage.switchEphemeralHint" @click="switchToEphemeral(r)">
              {{ t.networkPage.switchEphemeral }}
            </button>
            <button class="btn btn--sm btn--warning" :disabled="rowIpBusy(r)" @click="changeIp(r)">
              {{ t.networkPage.change }}
            </button>
            <button class="btn btn--sm" :class="{ 'btn--warning': r.lifetime !== 'RESERVED' }"
                    :disabled="rowIpBusy(r)"
                    :title="r.lifetime === 'RESERVED' ? t.networkPage.unassignReservedHint : t.networkPage.unassignEphemeralHint"
                    @click="unassignPublicIp(r)">
              {{ r.lifetime === 'RESERVED' ? t.networkPage.unbindIp : t.networkPage.releaseIp }}
            </button>
          </span>
        </div>
        </template>
      </template>
    </SectionCard>

    <SectionCard v-else-if="active === 'reservedIp'" :title="t.networkPage.reservedTitle"
                 :note="t.networkPage.reservedNote">
      <template #action>
        <button class="btn btn--sm btn--primary" :disabled="targets.length === 0" @click="openCreateReserved">
          {{ t.networkPage.createReserved }}
        </button>
      </template>

      <div v-if="createReservedOpen" class="inline-form">
        <label class="field">
          <span class="lbl">{{ t.networkPage.createReservedTarget }}</span>
          <SelectMenu v-model="createReservedForm.targetKey" :groups="targetMenuGroups" :label="t.networkPage.createReservedTarget" />
        </label>
        <label class="field">
          <span class="lbl">{{ t.networkPage.createReservedName }}</span>
          <input v-model="createReservedForm.displayName" class="input" :placeholder="t.networkPage.createReservedNamePlaceholder" />
        </label>
        <div class="inline-form__actions">
          <button class="btn btn--sm btn--primary" :disabled="creatingReserved || !createReservedForm.targetKey" @click="submitCreateReserved">
            {{ creatingReserved ? t.networkPage.creating : t.networkPage.createAction }}
          </button>
          <button class="btn btn--sm" @click="createReservedOpen = false">{{ t.common.cancel }}</button>
        </div>
      </div>

      <div v-if="bindingReserved" class="inline-form inline-form--bind">
        <div class="inline-form__head t-xs">{{ t.networkPage.bindPanelTitle(shownIp(bindingReserved.ipAddress)) }}</div>
        <p v-if="bindInstOptions[0]?.options.length === 0" class="t-xs dim">{{ t.networkPage.noInstancesInRegion }}</p>
        <template v-else>
          <label class="field">
            <span class="lbl">{{ t.networkPage.pickInstance }}</span>
            <SelectMenu v-model="bindInstId" :groups="bindInstOptions" :label="t.networkPage.pickInstance" />
          </label>
          <label class="field field--wide">
            <span class="lbl">{{ t.networkPage.pickPrivateSlot }}</span>
            <span v-if="bindSlotsLoading" class="t-xs dim">{{ t.networkPage.loadingSlots }}</span>
            <SelectMenu v-else v-model="bindSlotKey" :groups="bindSlotGroups" :label="t.networkPage.pickPrivateSlot" />
          </label>
        </template>
        <div class="inline-form__actions">
          <button class="btn btn--sm btn--primary" :disabled="submittingBind || bindSlotsLoading || !bindSlotKey" @click="submitBindReserved">
            {{ t.networkPage.confirmBind }}
          </button>
          <button class="btn btn--sm" @click="bindingReserved = null">{{ t.common.cancel }}</button>
        </div>
      </div>

      <SkeletonRows v-if="(loading && !hasNet) || (reservedIpsLoading && !reservedIpLoaded)" :rows="2" />
      <EmptyState v-else-if="reservedIps.length === 0" :title="t.networkPage.noReservedIps"
                  :body="t.networkPage.noReservedIpsBody" />
      <template v-else>
        <div class="head cols-reserved">
          <span>IP</span><span>{{ t.networkPage.colName }}</span><span>{{ t.networkPage.colAccount }}</span><span>{{ t.networkPage.colRegion }}</span><span>{{ t.networkPage.colBindTarget }}</span><span>{{ t.networkPage.colState }}</span><span />
        </div>
        <template v-for="g in reservedGroups" :key="g.key">
        <AccountGroupRow v-if="g.account" :account="g.account" :count="g.rows.length" unit="publicIp" />
        <div v-for="r in g.rows" :key="r.accountId + r.id" class="row cols-reserved">
          <span class="acct-bar" :style="{ background: acctColor(accountById(r.accountId).colorIndex) }" />
          <span class="mono t-xs"><MaskedText :value="r.ipAddress" kind="ip" copyable /></span>
          <span class="t-xs dim" :title="r.displayName || ''">{{ r.displayName || '—' }}</span>
          <AccountChip :account="accountById(r.accountId)" />
          <span class="mono t-xs dim">{{ r.region }}</span>
          <span v-if="r.instanceId" class="res-target t-xs" :title="r.privateIp ? `${r.instanceName} · ${shownIp(r.privateIp)}` : r.instanceName">
            <span class="res-target__name">{{ r.instanceName }}</span>
            <span class="ip-tag t-2xs" :class="!r.isPrimaryPrivateIp ? 'ip-tag--sec-reserved' : 'ip-tag--reserved'">
              {{ !r.isPrimaryPrivateIp ? t.networkPage.reservedTagSecondary : !r.isPrimaryVnic ? t.networkPage.reservedTagSecondaryVnic : t.networkPage.reservedTagPrimary }}
            </span>
            <span v-if="r.privateIp" class="mono dim"><MaskedText :value="r.privateIp" kind="ip" /></span>
          </span>
          <span v-else-if="r.assigned" class="t-xs dim">{{ t.networkPage.reservedBoundOther }}</span>
          <span v-else class="t-xs dim">{{ t.networkPage.reservedUnbound }}</span>
          <span class="t-xs" :style="{ color: r.assigned ? 'var(--text-secondary)' : (!r.lifecycleState || r.lifecycleState === 'AVAILABLE') ? 'var(--success)' : 'var(--warning)' }">
            {{ r.assigned ? t.networkPage.bound : (!r.lifecycleState || r.lifecycleState === 'AVAILABLE') ? t.networkPage.reservedAvailable : t.networkPage.reservedTransitioning }}
          </span>
          <span class="res-acts">
            <button v-if="r.assigned" class="btn btn--sm btn--warning" :disabled="reservedBusyId === r.id" @click="unbindReserved(r)">
              {{ t.networkPage.unbind }}
            </button>
            <template v-else>
              <button class="btn btn--sm" :disabled="reservedBusyId === r.id || (!!r.lifecycleState && r.lifecycleState !== 'AVAILABLE')" @click="openBindPanel(r)">
                {{ t.networkPage.bind }}
              </button>
              <button class="btn btn--sm btn--warning" :disabled="reservedBusyId === r.id || (!!r.lifecycleState && r.lifecycleState !== 'AVAILABLE')" @click="releaseReserved(r)">
                {{ t.networkPage.release }}
              </button>
            </template>
          </span>
        </div>
        </template>
      </template>
    </SectionCard>

    <SectionCard v-else :title="t.networkPage.v6Title" :note="t.networkPage.v6Note">
      <SkeletonRows v-if="loading && !hasNet" :rows="2" />
      <EmptyState v-else-if="ipv6Prefixes.length === 0" :title="t.networkPage.noV6"
                  :body="t.networkPage.noV6Body" />
      <template v-else>
        <div class="head cols-ipv6"><span>{{ t.networkPage.colPrefix }}</span><span>{{ t.networkPage.colAccount }}</span><span>{{ t.networkPage.colRegion }}</span><span>VCN</span><span>{{ t.networkPage.colAssigned }}</span></div>
        <template v-for="g in ipv6Groups" :key="g.key">
        <AccountGroupRow v-if="g.account" :account="g.account" :count="g.rows.length" unit="prefix" />
        <div v-for="p in g.rows" :key="p.prefix" class="row cols-ipv6">
          <span class="acct-bar" :style="{ background: acctColor(accountById(p.accountId).colorIndex) }" />
          <span class="mono t-xs">{{ p.prefix }}</span>
          <AccountChip :account="accountById(p.accountId)" />
          <span class="mono t-xs dim">{{ p.region }}</span>
          <span class="mono t-xs dim">{{ p.vcn }}</span>
          <span class="t-xs dim">{{ t.networkPage.countPrefix(p.assigned) }}</span>
        </div>
        </template>
      </template>
    </SectionCard>
    </div>
    </Transition>
  </div>
</template>

<style scoped>
.net-actions { display: flex; align-items: center; gap: 10px; }


.mt { margin-top: 16px; }
.net-warn {
  margin: 0 0 12px; padding: 10px 14px; border-radius: var(--radius-md);
  border: 1px solid var(--warning); background: var(--warning-soft);
  color: var(--warning); font-size: 12px; line-height: 18px; white-space: pre-line;
}
.picker { display: flex; align-items: center; gap: 10px; padding: 12px 20px; border-bottom: 1px solid var(--border-subtle); }
.picker .input { max-width: 420px; height: 32px; }

.head, .row { display: grid; align-items: center; gap: 12px; padding: 0 20px 0 14px; }
.head { height: 34px; background: var(--bg-inset); border-bottom: 1px solid var(--border-subtle); font-size: 11px; color: var(--text-tertiary); }
.row { min-height: 46px; border-bottom: 1px solid var(--border-subtle); position: relative; }
.row:hover { background: var(--bg-hover); }
.row > span:not(.acct-bar), .head > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row .btn { justify-self: end; }

.cols-vcn { grid-template-columns: minmax(160px, 1.4fr) 78px 150px 130px 90px 90px; }
.cols-subnet { grid-template-columns: minmax(160px, 1.3fr) 78px 140px 130px 90px 140px; }
.cols-tpl { grid-template-columns: 130px minmax(160px, 1fr) 260px 90px; }

/* 自定义规则表单。auto-fit 让窄屏上自动换行，不至于把输入框挤成一条缝。 */
.manual {
  display: grid; gap: 12px; padding: 16px 20px;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
}
.manual-desc { grid-column: 1 / -1; }
.manual .lbl { font-size: 12px; color: var(--text-secondary); }
/* SelectMenu 的触发器是 inline-flex，不撑格子；输入框跟着降到同高度对齐。 */
.manual :deep(.sm__trigger) { width: 100%; }
.manual .input { height: 32px; }
.manual-foot {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  padding: 0 20px 16px;
}

/* 断网警示条。放在卡片之外、页签正下方，进这一页就先看到。
   配色沿用上面的 .net-warn，两处说的是同一类事。 */
.warn-bar {
  display: flex; align-items: center; justify-content: space-between; gap: 16px;
  margin-bottom: 16px; padding: 12px 16px;
  border: 1px solid var(--warning); border-radius: var(--radius-md);
  background: var(--warning-soft); color: var(--warning); line-height: 18px;
}
.warn-bar .btn { flex-shrink: 0; }
.cols-rule { grid-template-columns: 62px 78px 150px 120px minmax(140px, 1fr) 90px; }
.cols-ip { grid-template-columns: 126px 136px 72px 124px minmax(120px, 1fr) 72px minmax(244px, 312px); }
.cols-ip--batch { grid-template-columns: 24px 126px 136px 72px 124px minmax(120px, 1fr) 72px minmax(244px, 312px); }
.ip-batch-head { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.ip-check { display: inline-flex; align-items: center; justify-content: center; cursor: pointer; }
.ip-check input { width: 14px; height: 14px; accent-color: var(--accent); cursor: pointer; }
.row.is-selected { background: color-mix(in srgb, var(--accent) 6%, transparent); }
.sel-bar { position: absolute; left: 0; top: 0; bottom: 0; width: 3px; background: var(--accent); z-index: 2; }
.cols-reserved { grid-template-columns: 136px minmax(100px, 0.8fr) 78px 132px minmax(200px, 1.4fr) 86px 136px; }
.cols-ipv6 { grid-template-columns: minmax(220px, 1.4fr) 78px 150px 140px 1fr; }
.ip-acts { display: flex; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: 6px; padding: 6px 0; overflow: visible !important; }
.ip-tag-cell { display: flex; align-items: center; overflow: visible !important; }
.ip-tag {
  display: inline-flex; align-items: center;
  padding: 2px 7px; border-radius: var(--radius-sm);
  border: 1px solid var(--border-default);
  color: var(--text-secondary);
  line-height: 16px; white-space: nowrap;
}
.ip-tag--ephemeral {
  border-color: var(--border-subtle);
  color: var(--text-tertiary);
}
.ip-tag--secondary {
  border-color: var(--border-default);
  color: var(--text-secondary);
}
.ip-tag--reserved {
  border-color: color-mix(in srgb, var(--info) 40%, transparent);
  background: var(--info-soft);
  color: var(--info);
}
.ip-tag--sec-reserved {
  border-color: color-mix(in srgb, var(--accent) 45%, transparent);
  background: var(--accent-soft);
  color: var(--accent);
}
.inline-form {
  display: grid; gap: 12px; align-items: end;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  padding: 14px 20px; border-bottom: 1px solid var(--border-subtle);
  background: var(--bg-inset);
}
.inline-form--bind { grid-template-columns: minmax(200px, 1fr) minmax(280px, 1.5fr) auto; }
.inline-form--switch { grid-template-columns: minmax(240px, 1fr) auto auto; }
.inline-form__head { grid-column: 1 / -1; font-weight: 500; color: var(--text-primary); }
.inline-form__warn { grid-column: 1 / -1; margin: -4px 0 0; color: var(--warning); line-height: 17px; }
.inline-form .lbl { font-size: 12px; color: var(--text-secondary); }
.inline-form :deep(.sm__trigger) { width: 100%; }
.inline-form .input { height: 32px; }
.inline-form__actions { display: flex; align-items: center; gap: 8px; justify-self: end; }
.res-target { display: inline-flex !important; align-items: center; gap: 6px; overflow: hidden; }
.res-target__name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.res-acts { display: flex; align-items: center; justify-content: flex-end; gap: 6px; overflow: visible !important; }
</style>
