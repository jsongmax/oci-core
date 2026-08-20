<script setup lang="ts">
/**
 * §4.5 网络：VCN / 子网 / 安全规则 / 公网 IP / IPv6，跨账号聚合。
 *
 * 网络资源在 OCI 里是按（账号 × 区域）隔离的，没有跨账号的列表接口，
 * 因此这里对筛选器里的每个账号 × 其订阅区域各拉一次，再在前端合并。
 * 网络对象数量很少（一个账号通常一两个 VCN），这个代价可以接受。
 */
import { computed, onMounted, ref, watch } from 'vue'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import { network, errorText, type SubnetDTO, type VcnDTO, type SecurityListDTO, type RuleTemplateDTO, type IngressRuleDTO, type EgressRuleDTO } from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import PageTabs from '@/components/PageTabs.vue'
import AccountChip from '@/components/AccountChip.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import SelectMenu, { type SelectGroup } from '@/components/SelectMenu.vue'
import AccountGroupRow from '@/components/AccountGroupRow.vue'
import { groupByAccount } from '@/lib/grouping'

const { state, accountById, ask, toast, toastError, visibleInstances } = useStore()
const TABS = ['VCN', '子网', '安全规则', '公网 IP', 'IPv6']
const active = ref('VCN')

/** 一条网络记录附带它属于哪个账号与区域——跨账号聚合后必须能归属。 */
interface Located<T> { accountId: string; region: string; item: T }

const loading = ref(false)
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

async function loadNetwork() {
  if (targets.value.length === 0) {
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
const groupable = computed(() => active.value !== '安全规则')

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
  if (!l) return '请先在下方选择要编辑的安全列表'
  return `追加到 ${accountById(l.accountId).alias} · ${l.region} · ${l.item.displayName}`
})

const listKey = (l: Located<SecurityListDTO>) => `${l.accountId}|${l.region}|${l.item.id}`

const selectedList = computed(() =>
  securityLists.value.find(l => listKey(l) === selectedListKey.value)
)

/** 把入站/出站规则摊平成表格行，保留原始下标以便删除。 */
interface RuleRow {
  dir: '入站' | '出站'
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
  return '全部端口'
}

const ruleRows = computed<RuleRow[]>(() => {
  const list = selectedList.value?.item
  if (!list) return []

  const allowAll = new Set(list.allowAllRules ?? [])
  const rows: RuleRow[] = []

  ;(list.ingressSecurityRules ?? []).forEach((rule, index) => {
    rows.push({
      dir: '入站', index,
      proto: PROTO_NAME[rule.protocol] ?? rule.protocol,
      cidr: rule.source,
      ports: portsText(rule),
      desc: rule.description || '—',
      danger: allowAll.has(index)
    })
  })
  ;(list.egressSecurityRules ?? []).forEach((rule, index) => {
    rows.push({
      dir: '出站', index,
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
    toastError('保存安全规则失败', err)
    await loadNetwork()
  } finally {
    savingRules.value = false
  }
}

function buildRule(t: RuleTemplateDTO): IngressRuleDTO {
  const rule: IngressRuleDTO = {
    protocol: t.protocol,
    source: '0.0.0.0/0',
    sourceType: 'CIDR_BLOCK',
    description: t.description
  }
  if (t.protocol === '6') rule.tcpOptions = { destinationPortRange: { min: t.port, max: t.port } }
  if (t.protocol === '17') rule.udpOptions = { destinationPortRange: { min: t.port, max: t.port } }
  if (t.protocol === '1') rule.icmpOptions = { type: 3, code: 4 }
  return rule
}

function addTemplate(t: RuleTemplateDTO) {
  const list = selectedList.value?.item
  if (!list) {
    toast({ tone: 'warning', title: '请先选择一个安全列表' })
    return
  }

  const commit = () => {
    const ingress = [...(list.ingressSecurityRules ?? []), buildRule(t)]
    void saveRules(ingress, list.egressSecurityRules ?? [], `已追加 ${t.label} 规则`)
  }

  if (t.dangerous) {
    ask({
      level: 2, title: '追加全放行规则',
      body: '该规则会把实例的所有端口暴露在公网，任何人都可以尝试连接。这通常不是你想要的。',
      okLabel: '仍然追加',
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
    level: 2, title: '删除安全规则',
    body: `${row.dir} ${row.proto} ${row.cidr} ${row.ports} —— 删除后依赖该规则的连接会立即中断。`,
    okLabel: '删除规则',
    onConfirm: () => {
      const ingress = [...(list.ingressSecurityRules ?? [])]
      const egress = [...(list.egressSecurityRules ?? [])]
      if (row.dir === '入站') ingress.splice(row.index, 1)
      else egress.splice(row.index, 1)
      void saveRules(ingress, egress, '规则已删除')
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
    title: `更换公网 IP ${inst.publicIp}`,
    body: `更换后原 IP 不可找回，${inst.name} 的 SSH 连接将中断，DNS 记录需要手动更新。`,
    okLabel: '更换 IP',
    onConfirm: async () => {
      try {
        const result = await (await import('@/api')).instances.changeIp(id)
        inst.publicIp = result.newIp
        toast({
          tone: 'success',
          title: `${inst.name} 已更换 IP`,
          body: `${result.oldIp} → ${result.newIp}`,
          command: `ssh ubuntu@${result.newIp}`
        })
      } catch (err) {
        toastError('更换 IP 失败', err)
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
        <h1 class="page-title">网络</h1>
        <p class="page-sub">
          跨账号聚合 · VCN {{ vcns.length }} · 子网 {{ subnets.length }} · 安全列表 {{ securityLists.length }}
        </p>
      </div>
      <div class="net-actions">
        <!-- 只在分组真正生效的页签上出现。安全规则页是"编辑单个安全列表"，
             公网 IP 与 IPv6 是扁平清单，分组对它们没有意义；开关一直挂着
             却点了没反应，比没有更糟。 -->
        <button v-if="groupable" class="pill" :class="{ 'is-active': state.groupByAccount }"
                @click="state.groupByAccount = !state.groupByAccount">按账号分组</button>
        <button class="btn" :disabled="loading" @click="loadNetwork">
          {{ loading ? '加载中…' : '刷新' }}
        </button>
      </div>
    </header>

    <PageTabs :tabs="TABS" v-model:active="active" />

    <p v-if="loadError" class="net-warn">{{ loadError }}</p>

    <SectionCard v-if="active === 'VCN'" title="VCN" note="跨账号聚合，带身份色条">
      <SkeletonRows v-if="loading" :rows="3" />
      <EmptyState v-else-if="vcns.length === 0" title="所选账号下暂无 VCN"
                  body="创建实例时勾选「自动创建网络」即可自动建好一套。" />
      <template v-else>
        <div class="head cols-vcn">
          <span>名称</span><span>账号</span><span>区域</span><span>CIDR</span><span>子网</span><span>实例</span>
        </div>
        <template v-for="g in vcnGroups" :key="g.key">
        <AccountGroupRow v-if="g.account" :account="g.account" :count="g.rows.length" unit="个 VCN" />
        <div v-for="v in g.rows" :key="v.accountId + v.item.id" class="row cols-vcn">
          <span class="acct-bar" :style="{ background: acctColor(accountById(v.accountId).colorIndex) }" />
          <span class="mono t-xs">{{ v.item.displayName }}</span>
          <AccountChip :account="accountById(v.accountId)" />
          <span class="mono t-xs dim">{{ v.region }}</span>
          <span class="mono t-xs">{{ v.item.cidrBlock }}</span>
          <span class="mono t-xs dim">{{ subnetsOfVcn(v.item.id) }} 个</span>
          <span class="mono t-xs dim">{{ instancesIn(v.accountId, v.region) }} 台</span>
        </div>
        </template>
      </template>
    </SectionCard>

    <SectionCard v-else-if="active === '子网'" title="子网">
      <SkeletonRows v-if="loading" :rows="3" />
      <EmptyState v-else-if="subnets.length === 0" title="所选账号下暂无子网" />
      <template v-else>
        <div class="head cols-subnet">
          <span>名称</span><span>账号</span><span>区域</span><span>CIDR</span><span>类型</span><span>IPv6</span>
        </div>
        <template v-for="g in subnetGroups" :key="g.key">
        <AccountGroupRow v-if="g.account" :account="g.account" :count="g.rows.length" unit="个子网" />
        <div v-for="s in g.rows" :key="s.accountId + s.item.id" class="row cols-subnet">
          <span class="acct-bar" :style="{ background: acctColor(accountById(s.accountId).colorIndex) }" />
          <span class="mono t-xs">{{ s.item.displayName }}</span>
          <AccountChip :account="accountById(s.accountId)" />
          <span class="mono t-xs dim">{{ s.region }}</span>
          <span class="mono t-xs">{{ s.item.cidrBlock }}</span>
          <span class="t-xs" :class="{ dim: s.item.prohibitPublicIpOnVnic }">
            {{ s.item.prohibitPublicIpOnVnic ? '私有' : '公共' }}
          </span>
          <span class="mono t-xs dim">{{ s.item.ipv6CidrBlock || '—' }}</span>
        </div>
        </template>
      </template>
    </SectionCard>

    <template v-else-if="active === '安全规则'">
      <!-- note 必须写明追加目标：这几个按钮是往某个安全列表里写规则的，
           多账号时不写清楚，用户很可能给错的租户开了一个 0.0.0.0/0 的端口。 -->
      <SectionCard title="常用端口模板" :note="templateTargetNote">
        <div v-for="t in templates" :key="t.key" class="row cols-tpl">
          <span class="acct-bar" :style="{ background: t.dangerous ? 'var(--danger)' : 'var(--success)' }" />
          <span class="t-xs" :style="{ color: t.dangerous ? 'var(--danger)' : 'var(--success)', fontWeight: 600 }">
            {{ t.dangerous ? '⚠ ' : '' }}{{ t.label }}
          </span>
          <span class="t-xs dim">{{ t.description }}</span>
          <span class="mono t-xs dim">
            入站 {{ PROTO_NAME[t.protocol] ?? t.protocol }} 0.0.0.0/0{{ t.port ? ' : ' + t.port : '' }}
          </span>
          <button class="btn btn--sm" :class="t.dangerous ? 'btn--danger' : ''"
                  :disabled="savingRules || !selectedList" @click="addTemplate(t)">追加</button>
        </div>
      </SectionCard>

      <SectionCard title="安全列表" class="mt"
                   :note="selectedList ? `${accountById(selectedList.accountId).code} · ${selectedList.region}` : ''">
        <div class="picker">
          <span class="t-xs dim">编辑目标</span>
          <SelectMenu v-model="selectedListKey" :groups="listGroups" :min-width="360"
                      aria-label="编辑目标安全列表" placeholder="没有可编辑的安全列表" />
        </div>

        <SkeletonRows v-if="loading" :rows="4" />
        <EmptyState v-else-if="!selectedList" title="所选账号下暂无安全列表" />
        <template v-else>
          <div class="head cols-rule">
            <span>方向</span><span>协议</span><span>源 / 目标 CIDR</span><span>端口</span><span>描述</span><span />
          </div>
          <div v-for="r in ruleRows" :key="r.dir + r.index" class="row cols-rule">
            <span class="acct-bar" :style="{ background: r.danger ? 'var(--danger)' : 'var(--success)' }" />
            <span class="t-xs" :style="{ color: r.danger ? 'var(--danger)' : 'var(--text-primary)' }">{{ r.dir }}</span>
            <span class="mono t-xs">{{ r.proto }}</span>
            <span class="mono t-xs">{{ r.cidr }}</span>
            <span class="mono t-xs">{{ r.ports }}</span>
            <span class="t-xs" :style="{ color: r.danger ? 'var(--danger)' : 'var(--text-secondary)' }">
              {{ r.danger ? '⚠ 全放行，建议删除' : r.desc }}
            </span>
            <button class="btn btn--sm btn--danger" :disabled="savingRules" @click="removeRule(r)">删除</button>
          </div>
        </template>
      </SectionCard>
    </template>

    <SectionCard v-else-if="active === '公网 IP'" title="公网 IPv4"
                 note="更换即删除并重新分配，原 IP 不可找回">
      <EmptyState v-if="publicIps.length === 0" title="所选账号下暂无带公网 IP 的实例" />
      <template v-else>
        <div class="head cols-ip">
          <span>IP</span><span>账号</span><span>区域</span><span>绑定实例</span><span>状态</span><span />
        </div>
        <template v-for="g in ipGroups" :key="g.key">
        <AccountGroupRow v-if="g.account" :account="g.account" :count="g.rows.length" unit="个公网 IP" />
        <div v-for="i in g.rows" :key="i.id" class="row cols-ip">
          <span class="acct-bar" :style="{ background: acctColor(accountById(i.accountId).colorIndex) }" />
          <span class="mono t-xs">{{ i.publicIp }}</span>
          <AccountChip :account="accountById(i.accountId)" />
          <span class="mono t-xs dim">{{ i.region }}</span>
          <span class="t-xs">{{ i.name }}</span>
          <span class="t-xs" :style="{ color: i.state === 'STOPPED' ? 'var(--warning)' : 'var(--text-secondary)' }">
            {{ i.state === 'STOPPED' ? '实例已停止' : '已绑定' }}
          </span>
          <button class="btn btn--sm btn--warning" @click="changeIp(i.id)">更换</button>
        </div>
        </template>
      </template>
    </SectionCard>

    <SectionCard v-else title="IPv6 前缀" note="Oracle 免费分配 /56，可按子网切 /64">
      <SkeletonRows v-if="loading" :rows="2" />
      <EmptyState v-else-if="ipv6Prefixes.length === 0" title="尚未启用 IPv6"
                  body="在实例详情的「网络」页可以为单台实例启用；创建实例时勾选 IPv6 会自动配好整套。" />
      <template v-else>
        <div class="head cols-ipv6"><span>前缀</span><span>账号</span><span>区域</span><span>VCN</span><span>已分配子网</span></div>
        <template v-for="g in ipv6Groups" :key="g.key">
        <AccountGroupRow v-if="g.account" :account="g.account" :count="g.rows.length" unit="个前缀" />
        <div v-for="p in g.rows" :key="p.prefix" class="row cols-ipv6">
          <span class="acct-bar" :style="{ background: acctColor(accountById(p.accountId).colorIndex) }" />
          <span class="mono t-xs">{{ p.prefix }}</span>
          <AccountChip :account="accountById(p.accountId)" />
          <span class="mono t-xs dim">{{ p.region }}</span>
          <span class="mono t-xs dim">{{ p.vcn }}</span>
          <span class="t-xs dim">{{ p.assigned }} 个</span>
        </div>
        </template>
      </template>
    </SectionCard>
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
.cols-rule { grid-template-columns: 62px 78px 150px 120px minmax(140px, 1fr) 90px; }
.cols-ip { grid-template-columns: 140px 78px 150px minmax(140px, 1fr) 110px 90px; }
.cols-ipv6 { grid-template-columns: minmax(220px, 1.4fr) 78px 150px 140px 1fr; }
</style>
