<script setup lang="ts">
/**
 * §4.5 网络：VCN / 子网 / 安全规则 / 公网 IP / IPv6，跨账号聚合。
 *
 * 网络资源在 OCI 里是按（账号 × 区域）隔离的，没有跨账号的列表接口，
 * 因此这里对筛选器里的每个账号 × 其订阅区域各拉一次，再在前端合并。
 * 网络对象数量很少（一个账号通常一两个 VCN），这个代价可以接受。
 */
import { t } from '@/i18n'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import { network, errorText, type SubnetDTO, type VcnDTO, type SecurityListDTO, type RuleTemplateDTO, type RuleDirection, type IngressRuleDTO, type EgressRuleDTO } from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import PageTabs from '@/components/PageTabs.vue'
import AccountChip from '@/components/AccountChip.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import LoadingBar from '@/components/LoadingBar.vue'
import SelectMenu, { type SelectGroup } from '@/components/SelectMenu.vue'
import AccountGroupRow from '@/components/AccountGroupRow.vue'
import { groupByAccount } from '@/lib/grouping'

const { state, accountById, ask, toast, toastError, visibleInstances, trackJob } = useStore()
const TABS = ['vcn', 'subnet', 'rules', 'publicIp', 'ipv6']
const active = ref('vcn')

/** 一条网络记录附带它属于哪个账号与区域——跨账号聚合后必须能归属。 */
interface Located<T> { accountId: string; region: string; item: T }

const loading = ref(false)
/**
 * 已经拿到过数据。刷新时有数据就保留旧列表调暗显示，只有一条都没有时才画骨架——
 * 以前每次刷新都把整页换回骨架，列表闪一下才回来。
 */
const hasNet = computed(() => vcns.value.length + subnets.value.length + securityLists.value.length > 0)
const loadError = ref('')
const vcns = ref<Located<VcnDTO>[]>([])
const subnets = ref<Located<SubnetDTO>[]>([])
const templates = ref<RuleTemplateDTO[]>([])

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

async function loadNetwork() {
  const seq = ++networkSeq
  if (targets.value.length === 0) {
    loading.value = false
    vcns.value = []
    subnets.value = []
    securityLists.value = []
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
async function saveRules(ingress: IngressRuleDTO[], egress: EgressRuleDTO[], successText: string) {
  const target = selectedList.value
  if (!target) return

  savingRules.value = true
  try {
    const updated = await network.updateSecurityList(
      target.item.id, target.accountId, target.region, ingress, egress
    )
    target.item = { ...updated, allowAllRules: updated.allowAllRules ?? null }
    toast({ tone: 'success', title: successText })
  } catch (err) {
    toastError(t.networkPage.saveFailed, err)
    await loadNetwork()
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
    void saveRules(ingress, egress, t.networkPage.customAdded)
    manual.description = ''
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

const publicIps = computed(() =>
  visibleInstances.value.filter(i => i.state !== 'TERMINATED' && i.publicIp !== '—')
)

function changeIp(id: string) {
  const inst = state.instances.find(x => x.id === id)
  if (!inst) return

  ask({
    level: 2,
    title: t.networkPage.changeIpTitle(inst.publicIp),
    body: t.networkPage.changeIpBody(inst.name),
    okLabel: t.networkPage.changeIp,
    onConfirm: async () => {
      try {
        const { job } = await (await import('@/api')).instances.changeIp(id)
        trackJob(job)
      } catch (err) {
        toastError(t.networkPage.changeIpFailed, err)
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
      <EmptyState v-if="publicIps.length === 0" :title="t.networkPage.noIps" />
      <template v-else>
        <div class="head cols-ip">
          <span>IP</span><span>{{ t.networkPage.colAccount }}</span><span>{{ t.networkPage.colRegion }}</span><span>{{ t.networkPage.colBoundInstance }}</span><span>{{ t.networkPage.colState }}</span><span />
        </div>
        <template v-for="g in ipGroups" :key="g.key">
        <AccountGroupRow v-if="g.account" :account="g.account" :count="g.rows.length" unit="publicIp" />
        <div v-for="i in g.rows" :key="i.id" class="row cols-ip">
          <span class="acct-bar" :style="{ background: acctColor(accountById(i.accountId).colorIndex) }" />
          <span class="mono t-xs">{{ i.publicIp }}</span>
          <AccountChip :account="accountById(i.accountId)" />
          <span class="mono t-xs dim">{{ i.region }}</span>
          <span class="t-xs">{{ i.name }}</span>
          <span class="t-xs" :style="{ color: i.state === 'STOPPED' ? 'var(--warning)' : 'var(--text-secondary)' }">
            {{ i.state === 'STOPPED' ? t.networkPage.instanceStopped : t.networkPage.bound }}
          </span>
          <button class="btn btn--sm btn--warning" @click="changeIp(i.id)">{{ t.networkPage.change }}</button>
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
.cols-ip { grid-template-columns: 140px 78px 150px minmax(140px, 1fr) 110px 90px; }
.cols-ipv6 { grid-template-columns: minmax(220px, 1.4fr) 78px 150px 140px 1fr; }
</style>
