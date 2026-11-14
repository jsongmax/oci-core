<script setup lang="ts">
/** §4.4.3 实例详情抽屉（720）：概览 / 网络 / 存储 / 监控 / 控制台 / 操作记录 */
import { t, fmtDateTime } from '@/i18n'
import { computed, defineAsyncComponent, onMounted, reactive, ref, watch } from 'vue'
import { useStore } from '@/store'
import { isTransitional } from '@/lib/lifecycle'
import { acctColor, byteScale, formatBytes, formatBitRate, formatBits, formatPercent } from '@/lib/format'
import { relativeTime, instanceUptime, ipv6LeaseHint } from '@/lib/adapt'
import {
  instances as instancesApi, insights, storage, network, errorText,
  type AuditEntryDTO, type InstanceDetailDTO, type MetricsDTO, type InstanceTrafficDTO
} from '@/api'
import type * as D from '@/api/types'
import type { KvItem } from '@/types'
import { mask } from '@/lib/mask'
import AppDrawer from '@/components/AppDrawer.vue'
import DrawerHeader from '@/components/DrawerHeader.vue'
import DrawerTabs from '@/components/DrawerTabs.vue'
import DrawerBody from '@/components/DrawerBody.vue'
import SectionCard from '@/components/SectionCard.vue'
import KeyValueList from '@/components/KeyValueList.vue'
import CheckList from '@/components/CheckList.vue'
import CodeBlock from '@/components/CodeBlock.vue'
import StateBadge from '@/components/StateBadge.vue'
import BootVolumeEditor from '@/components/BootVolumeEditor.vue'
import ReplaceBootVolume from '@/components/ReplaceBootVolume.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import SwitchRow from '@/components/SwitchRow.vue'
import type { MetricSeries } from '@/components/MetricChart.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'

/** ECharts 体积不小，只在打开监控页时才加载。 */
const MetricChart = defineAsyncComponent(() => import('@/components/MetricChart.vue'))

const props = defineProps<{ id: string; tab?: string }>()
const {
  state, accountById, closeDrawer, ask, toast, toastError, trackJob, activeJobsFor,
  start, stop, restart, terminate
} = useStore()

/** 这台实例上正在后台跑的任务（换 IP、改配置……）。 */
const runningJobs = computed(() => activeJobsFor(props.id))
// 任务结束后详情（网卡、IP、配置）可能变了，重拉一次。
watch(() => runningJobs.value.length, (n, old) => { if (n < old) void loadDetail() })

const TABS = ['overview', 'network', 'storage', 'metrics', 'console', 'history']
const active = ref(props.tab && TABS.includes(props.tab) ? props.tab : 'overview')

const inst = computed(() => state.instances.find(i => i.id === props.id))

/** 运行时长。未观测到本次开机时退回"创建至今"，并把这件事说清楚。 */
const uptimeDetail = computed(() => {
  const i = inst.value
  if (!i) return '—'
  const u = instanceUptime(i)
  return u.approx && u.text !== '—' ? t.instDrawer.uptimeApprox(u.text) : u.text
})
const account = computed(() => (inst.value ? accountById(inst.value.accountId) : undefined))
const color = computed(() => (account.value ? acctColor(account.value.colorIndex) : 'var(--border-default)'))
const locked = computed(() => !!inst.value && isTransitional(inst.value.state))

/* ---------- 详情 ---------- */

const detail = ref<InstanceDetailDTO | null>(null)
const detailLoading = ref(false)
const detailError = ref('')

async function loadDetail() {
  detailLoading.value = true
  detailError.value = ''
  try {
    detail.value = await instancesApi.detail(props.id)
  } catch (err) {
    detailError.value = errorText(err)
  } finally {
    detailLoading.value = false
  }
}

const primaryVnic = computed(() =>
  detail.value?.vnics?.find(v => v.isPrimary) ?? detail.value?.vnics?.[0]
)

const secondaryVnics = computed(() =>
  detail.value?.vnics?.filter(v => v !== primaryVnic.value) ?? [])

/**
 * IPv6 每个地址单占一行。原先拼成一串显示，既不能逐个复制，
 * 也没法走 ip 打码——maskIP 只认单个地址。
 */
function ipv6Items(v: D.VnicInfoDTO): KvItem[] {
  const ipv6 = v.ipv6
  if (!ipv6?.length) return [{ k: 'IPv6', v: t.instDrawer.notEnabled, tone: 'var(--text-secondary)' }]
  // 后端按创建时间排好了；旧后端没有 ipv6Info，就只显示地址。
  const created = new Map((v.ipv6Info ?? []).map(e => [e.address, e.timeCreated]))
  return ipv6.map((ip, n) => {
    const at = created.get(ip)
    const date = at ? new Date(at) : null
    const base = ipv6.length > 1 ? `IPv6 #${n + 1}` : 'IPv6'
    return {
      // 日期直接写在标签上：批量分配的异常要扫一眼就能看出来，靠悬停一个个看太慢。
      k: date ? `${base} · ${shortDate(date)}` : base,
      title: date ? t.instDrawer.createdAt(fmtDateTime(date)) : undefined,
      v: ip, mono: true, copyable: true, secret: 'ip' as const,
      action: {
        label: removingIpv6.value === ip ? t.instDrawer.removing : t.instDrawer.remove, tone: 'warning' as const,
        disabled: locked.value || !!removingIpv6.value, onClick: () => removeIpv6(ip, v.isPrimary)
      }
    }
  })
}

const shortDate = (d: Date) =>
  `${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`

/* ---------- 移除 IPv6 ---------- */

/** 正在移除的地址；同一时刻只允许一个，任务在实例上本来也是串行的。 */
const removingIpv6 = ref('')

/** 与「开启 IPv6」对称：删掉的地址不可找回，用它的连接会断。确认框里的地址按隐私模式打码。 */
function removeIpv6(address: string, primary: boolean) {
  const i = inst.value
  if (!i) return
  const shown = state.privacyMode ? mask(address, 'ip') : address
  ask({
    level: 2,
    title: t.instDrawer.removeV6Title(i.name),
    body: t.instDrawer.removeV6Body(shown),
    okLabel: t.instDrawer.removeV6,
    onConfirm: async () => {
      removingIpv6.value = address
      try {
        const { job } = await instancesApi.removeIpv6(i.id, address)
        const id = i.id
        // 提交时就算好提示：任务结束时抽屉可能已经换了一台。
        const hint = ipv6LeaseHint(i.osFamily, false, primary)
        trackJob(job, {
          onSuccess: done => {
            // 带命令时一直留着，要复制到实例里去跑。
            toast({ tone: 'success', title: done.summary || t.instDrawer.v6Removed, body: hint.note, command: hint.command },
              hint.command ? 0 : 10000)
            if (props.id === id) void loadDetail()
          }
        })
      } catch (err) {
        toastError(t.instDrawer.removeV6Failed, err)
      } finally {
        removingIpv6.value = ''
      }
    }
  })
}

/** 一张网卡的地址与所在网络。主网卡与附属网卡共用，保持两边的行一致。 */
function vnicItems(v: D.VnicInfoDTO): KvItem[] {
  return [
    { k: t.instDrawer.publicV4, v: v.publicIp || t.instDrawer.unassigned, mono: true, copyable: !!v.publicIp, secret: 'ip' },
    ...(v.publicIp ? [{ k: t.instDrawer.type, v: v.publicIpType === 'RESERVED' ? t.instDrawer.reservedIp : t.instDrawer.ephemeralIp }] : []),
    { k: t.instDrawer.privateV4, v: v.privateIp || '—', mono: true, copyable: !!v.privateIp, secret: 'ip' },
    ...ipv6Items(v),
    { k: 'VCN', v: v.vcnName || '—', mono: true },
    { k: t.instDrawer.subnet, v: v.subnetName || '—', mono: true },
    { k: 'MAC', v: v.macAddress || '—', mono: true }
  ]
}

/* ---------- 更换 / 分配公网 IP ---------- */

/** 正在换 IP 的网卡 OCID；同一时刻只允许一张。 */
const changingIpVnic = ref('')

/**
 * 主网卡、附属网卡共用。网卡还没有公网 IP 时就是新分配一个。
 * 与行内 ⋯ 菜单、网络页走同一个接口（那两处只管主网卡）。
 * 确认框里的原 IP 按隐私模式打码——确认框同样会被截图。
 */
function changeIp(v: D.VnicInfoDTO) {
  const i = inst.value
  if (!i) return
  const assign = !v.publicIp
  const where = v.isPrimary ? t.instDrawer.primaryVnic : t.instDrawer.secondaryVnic(v.displayName || '')
  const ip = v.publicIp && state.privacyMode ? mask(v.publicIp, 'ip') : v.publicIp
  ask({
    level: 2,
    title: assign ? t.instDrawer.assignIpTitle(i.name, where) : t.instDrawer.changeIpTitle(i.name, where),
    body: assign
      ? t.instDrawer.assignIpBody
      : t.instDrawer.changeIpBody(ip),
    okLabel: assign ? t.instDrawer.assignIp : t.instDrawer.changeIp,
    onConfirm: async () => {
      changingIpVnic.value = v.vnicId
      try {
        const { job } = await instancesApi.changeIp(i.id, v.isPrimary ? undefined : v.vnicId)
        const id = i.id
        trackJob(job, { onSuccess: () => { if (props.id === id) void loadDetail() } })
      } catch (err) {
        toastError(assign ? t.instDrawer.assignIpFailed : t.instDrawer.changeIpFailed, err)
      } finally {
        changingIpVnic.value = ''
      }
    }
  })
}

const ipButtonTitle = (v: D.VnicInfoDTO) =>
  v.publicIpType === 'RESERVED' ? t.instDrawer.reservedNoChange
    : v.publicIp ? t.instDrawer.changeIpHint : t.instDrawer.assignIpHint

/* ---------- 附属网卡增删 ---------- */

const vnicCount = computed(() => detail.value?.vnics?.length ?? 0)
const maxVnics = computed(() => detail.value?.maxVnics ?? 0)
/** 上限读不到（0）时不拦，交给 Oracle 判断 */
const vnicFull = computed(() => maxVnics.value > 0 && vnicCount.value >= maxVnics.value)

const addingVnic = ref(false)
const attachingVnic = ref(false)
const vnicSubnets = ref<D.SubnetDTO[]>([])
const vnicSubnetsLoading = ref(false)
const vnicForm = reactive({ subnetId: '', name: '', publicIp: true, ipv6: false })

const vnicSubnet = computed(() => vnicSubnets.value.find(s => s.id === vnicForm.subnetId))
const vnicSubnetGroups = computed(() => [{
  label: '',
  options: vnicSubnets.value.map(sn => ({
    value: sn.id,
    label: `${sn.displayName} · ${sn.cidrBlock}${sn.prohibitPublicIpOnVnic ? t.instDrawer.noPublicIpTag : ''}`
  }))
}])
// 选中禁公网 IP 的子网时自动关掉开关，而不是等提交时被后端拒。
watch(vnicSubnet, sn => { if (sn?.prohibitPublicIpOnVnic) vnicForm.publicIp = false })

/**
 * 打开添加表单并读取候选子网。
 *
 * 只列实例能用的子网：区域子网，或与实例同一可用域的 AD 子网。
 * 默认选主网卡所在的子网——最常见的用法就是同网段多挂一个公网 IP。
 */
async function openAddVnic() {
  const i = inst.value
  if (!i) return
  addingVnic.value = true
  Object.assign(vnicForm, { subnetId: primaryVnic.value?.subnetId ?? '', name: '', publicIp: true, ipv6: false })
  vnicSubnetsLoading.value = true
  try {
    const { subnets } = await network.subnets(i.accountId, i.region)
    const ad = detail.value?.instance.availabilityDomain ?? ''
    vnicSubnets.value = subnets.filter(sn =>
      sn.lifecycleState === 'AVAILABLE' && (!sn.availabilityDomain || sn.availabilityDomain === ad))
    if (!vnicSubnets.value.some(sn => sn.id === vnicForm.subnetId)) {
      vnicForm.subnetId = vnicSubnets.value[0]?.id ?? ''
    }
  } catch (err) {
    toastError(t.instDrawer.subnetsFailed, err)
  } finally {
    vnicSubnetsLoading.value = false
  }
}

async function submitVnic() {
  const i = inst.value
  if (!i || !vnicForm.subnetId) return
  attachingVnic.value = true
  try {
    const { job } = await instancesApi.attachVnic(i.id, {
      subnetId: vnicForm.subnetId,
      displayName: vnicForm.name.trim() || undefined,
      assignPublicIp: vnicForm.publicIp,
      assignIpv6: vnicForm.ipv6
    })
    addingVnic.value = false
    // 通常十几秒到一分钟；落定后 instance.network 会让这里自动刷新。
    trackJob(job)
  } catch (err) {
    toastError(t.instDrawer.addVnicFailed, err)
  } finally {
    attachingVnic.value = false
  }
}

/** 分离附属网卡。确认框里的 IP 按隐私模式打码——确认框也会被截图。 */
function detachVnic(v: D.VnicInfoDTO) {
  const i = inst.value
  if (!i) return
  const name = v.displayName || t.instDrawer.unnamedVnic
  const ip = v.publicIp && state.privacyMode ? mask(v.publicIp, 'ip') : v.publicIp
  ask({
    level: 2,
    title: t.instDrawer.detachVnicTitle(name),
    body: (ip ? t.instDrawer.detachVnicIp(ip) : '') +
      t.instDrawer.detachVnicBody,
    okLabel: t.instDrawer.detach,
    onConfirm: async () => {
      try {
        const { job } = await instancesApi.detachVnic(i.id, v.attachmentId)
        trackJob(job)
      } catch (err) {
        toastError(t.instDrawer.detachVnicFailed, err)
      }
    }
  })
}

// 网卡增减在后台落定后，后端推 instance.network；只重拉当前打开的这台。
watch(() => state.networkChanged, c => { if (c.id === props.id) void loadDetail() })

/* ---------- IPv6 ---------- */

/** 正在添加 IPv6 的网卡 OCID；同一时刻只允许一张。 */
const addingIpv6Vnic = ref('')

/**
 * 给某张网卡再分配一个 IPv6。已有地址也照样新分配（多个地址是合法用法）。
 * 子网和 VCN 没开通时后端会一并开通，并照 IPv4 的配置补 ::/0 路由与出站规则——
 * 和创建实例时勾选 IPv6 走同一套逻辑。
 */
function addIpv6(v: D.VnicInfoDTO) {
  const i = inst.value
  if (!i) return
  const first = !v.ipv6?.length
  const where = v.isPrimary ? t.instDrawer.primaryVnic : t.instDrawer.secondaryVnic(v.displayName || '')
  ask({
    level: 2, title: t.instDrawer.addV6Title(i.name, where),
    body: (first
      ? t.instDrawer.addV6First
      : t.instDrawer.addV6More(v.ipv6!.length)),
    okLabel: t.instDrawer.addV6,
    onConfirm: async () => {
      addingIpv6Vnic.value = v.vnicId
      try {
        const { job } = await instancesApi.addIpv6(i.id, v.vnicId)
        const id = i.id
        const name = i.name
        const hint = ipv6LeaseHint(i.osFamily, true, v.isPrimary)
        trackJob(job, {
          onSuccess: done => {
            const result = done.result as D.EnableIpv6DTO | undefined
            toast({
              tone: 'success', title: t.instDrawer.v6Assigned(name),
              body: [result?.address, ...(result?.steps ?? []), hint.note].filter(Boolean).join('\n'),
              command: hint.command
            }, 0)
            // 抽屉可能已经关了或换了一台，只在还开着这台时重拉。
            if (props.id === id) void loadDetail()
          }
        })
      } catch (err) {
        toastError(t.instDrawer.addV6Failed, err)
      } finally {
        addingIpv6Vnic.value = ''
      }
    }
  })
}

const cloudInit = computed(() => {
  const raw = detail.value?.metadata?.user_data
  if (!raw) return ''
  try {
    // 后端存的是 base64，这里解回来给用户看原文。
    return decodeURIComponent(escape(atob(raw)))
  } catch {
    return raw
  }
})

const sshKeys = computed(() => detail.value?.metadata?.ssh_authorized_keys ?? '')

/* ---------- 监控 ---------- */

const metrics = ref<MetricsDTO | null>(null)
const metricsLoading = ref(false)
const metricHours = ref(24)

/** 连续切时间范围时，大范围的查询往往更慢——晚回来会把图换成上一个范围的。 */
let metricsSeq = 0

async function loadMetrics() {
  const seq = ++metricsSeq
  metricsLoading.value = true
  try {
    const res = await instancesApi.metrics(props.id, metricHours.value)
    if (seq === metricsSeq) metrics.value = res
  } catch (err) {
    if (seq === metricsSeq) toastError(t.instDrawer.metricsFailed, err)
  } finally {
    if (seq === metricsSeq) metricsLoading.value = false
  }
}

const pointsOf = (name: string) =>
  metrics.value?.series.find(s => s.metric === name)?.datapoints ?? []

/** 转成图表要的 [毫秒时间戳, 值]。 */
const chartData = (name: string, scale = 1): [number, number][] =>
  pointsOf(name).map(d => [new Date(d.timestamp).getTime(), d.value * scale])

/** 一条序列的最新值、平均值、峰值。没有数据点时返回 null，界面显示「—」而不是 0。 */
function statsOf(name: string): { last: number; avg: number; peak: number } | null {
  const pts = pointsOf(name)
  if (!pts.length) return null
  let sum = 0
  let peak = 0
  for (const p of pts) {
    sum += p.value
    peak = Math.max(peak, p.value)
  }
  return { last: pts[pts.length - 1].value, avg: sum / pts.length, peak }
}

const cpuStats = computed(() => statsOf('CpuUtilization'))
const memStats = computed(() => statsOf('MemoryUtilization'))
const outStats = computed(() => statsOf('NetworksBytesOut'))
const inStats = computed(() => statsOf('NetworksBytesIn'))

/**
 * 按最近一个内存使用率数据点折算出的已用 GB。
 *
 * 原先这里画的是 memGb / memGb，永远 100%、永远标红"已满"——那是总量，
 * 不是用量。没有数据点时返回 null，免得把"不知道"画成 0。
 */
const memUsedGb = computed<number | null>(() => {
  const memGb = inst.value?.memGb
  if (!memStats.value || !memGb) return null
  return Math.round(memGb * memStats.value.last / 10) / 10
})

/** 使用率到 90% 才提示偏高：持续满载的机器值得看一眼。 */
const HIGH_USAGE = 90

const rateSeries = computed<MetricSeries[]>(() => [
  // 指标是每秒字节数，图表用每秒比特，刻度才落在整数 Mbps 上。
  { name: t.instDrawer.outbound, data: chartData('NetworksBytesOut', 8) },
  { name: t.instDrawer.inbound, data: chartData('NetworksBytesIn', 8) }
])

const usageSeries = computed<MetricSeries[]>(() => [
  { name: 'CPU', data: chartData('CpuUtilization') },
  { name: t.instDrawer.memory, data: chartData('MemoryUtilization') }
])

/** 粒度说明，例如「每 5 分钟一个点」。 */
const resolutionText = computed(() => {
  const r = metrics.value?.resolution
  if (!r) return ''
  const m = /^(\d+)([mhd])$/.exec(r)
  if (!m) return r
  const unit = { m: t.instDrawer.unitMin, h: t.instDrawer.unitHour, d: t.instDrawer.unitDay }[m[2] as 'm' | 'h' | 'd']
  return t.instDrawer.perPoint(m[1], unit)
})

const rangeText = computed(() =>
  metricHours.value < 24 ? t.instDrawer.lastHours(metricHours.value) : t.instDrawer.lastDays(metricHours.value / 24))

/** 单条指标查询失败时后端会带上错误；部分失败也要让人看见。 */
const metricErrors = computed(() =>
  (metrics.value?.series ?? []).filter(s => s.error).map(s => `${s.metric}：${s.error}`))

const noMetricData = computed(() =>
  !!metrics.value && metrics.value.series.every(s => (s.datapoints?.length ?? 0) === 0)
)

/* ---------- 本月已用流量 ---------- */

/**
 * 与上面的监控图分开取：图看的是速率、跟着时间范围走；
 * 这里看的是本月累计字节数，固定按自然月。数据来自 VCN 网卡指标，不依赖 Cloud Agent。
 */
const traffic = ref<InstanceTrafficDTO | null>(null)
const trafficLoading = ref(false)
const trafficError = ref('')

async function loadTraffic() {
  trafficLoading.value = true
  trafficError.value = ''
  try {
    traffic.value = await instancesApi.traffic(props.id)
  } catch (err) {
    trafficError.value = errorText(err)
  } finally {
    trafficLoading.value = false
  }
}

const trafficDays = computed(() => (traffic.value?.series ?? []).map(d => d.date.slice(5)))
/** 柱状图统一用一个单位（按本月单日最大值挑），刻度才是整数。 */
const trafficScale = computed(() => byteScale(
  (traffic.value?.series ?? []).reduce((m, d) => Math.max(m, d.outBytes, d.inBytes), 0)))
const trafficSeries = computed<MetricSeries[]>(() => {
  const f = trafficScale.value.factor
  return [
    { name: t.instDrawer.outbound, data: (traffic.value?.series ?? []).map(d => d.outBytes / f) },
    { name: t.instDrawer.inbound, data: (traffic.value?.series ?? []).map(d => d.inBytes / f) }
  ]
})

function refreshMonitoring() {
  void loadMetrics()
  void loadTraffic()
}

/* ---------- 控制台 ---------- */

const sshPublicKey = ref('')
const consoleCreating = ref(false)
const consoleResult = ref<{ serialConsoleCommand: string; vncConsoleCommand: string } | null>(null)

async function createConsole() {
  const key = sshPublicKey.value.trim()
  if (!key) {
    toast({ tone: 'warning', title: t.instDrawer.needSshKey })
    return
  }
  // 本地先拦一次，省掉一次注定失败的往返。
  if (!key.startsWith('ssh-rsa')) {
    toast({
      tone: 'warning',
      title: t.instDrawer.rsaOnlyTitle,
      body: t.instDrawer.rsaOnlyBody,
      command: 'ssh-keygen -t rsa -b 4096 -f ~/.ssh/oci_console'
    })
    return
  }
  consoleCreating.value = true
  try {
    const result = await instancesApi.console(props.id, key)
    consoleResult.value = result
    toast({ tone: 'success', title: t.instDrawer.consoleCreated, body: result.notice })
  } catch (err) {
    toastError(t.instDrawer.consoleFailed, err)
  } finally {
    consoleCreating.value = false
  }
}

/* ---------- 操作记录 ---------- */

const auditRows = ref<AuditEntryDTO[]>([])

async function loadAudit() {
  if (!inst.value) return
  try {
    const { entries } = await insights.audit({ accountId: inst.value.accountId, limit: 200 })
    // 后端按账号过滤，这里再按目标资源名收敛到这一台机器。
    auditRows.value = entries.filter(e => e.target === inst.value?.name)
  } catch {
    auditRows.value = []
  }
}

/* ---------- 存储 ---------- */

async function detachBoot() {
  // 注意用的是挂载关系 ID 而不是卷 ID——两者混用会直接 404。
  const attachmentId = detail.value?.bootVolumeAttachmentId
  const inst_ = inst.value
  if (!inst_ || !attachmentId) return

  ask({
    level: 2,
    title: t.instDrawer.detachBootTitle(inst_.name),
    body: t.instDrawer.detachBootBody,
    okLabel: t.instDrawer.detachBoot,
    onConfirm: async () => {
      try {
        await storage.detachBootVolume(attachmentId, inst_.accountId, inst_.region)
        toast({ tone: 'warning', title: t.instDrawer.bootDetached, body: t.instDrawer.bootDetachedBody })
        await loadDetail()
      } catch (err) {
        toastError(t.instDrawer.detachBootFailed, err)
      }
    }
  })
}

/* ---------- 救援模式 ---------- */

/**
 * 引导卷是否已经卸下来了。
 *
 * 只看「关机 + 没有挂载关系」，不能顺带要求 detail.bootVolume 有值：详情里的
 * 引导卷是拿缓存的 BootVolumeID 去查的，而那个 ID 本身来自挂载关系——卷一分离，
 * 同步就找不到它，ID 被清空，于是卷也查不出来。真分离了反而认不出来。
 */
const bootDetached = computed(() =>
  !!inst.value && inst.value.state === 'STOPPED' && !detail.value?.bootVolumeAttachmentId)

/* 分离之后卷「无主」了，只能从区域里的引导卷列表里把它找回来。 */
const looseVolumes = ref<D.BootVolumeDTO[]>([])
const looseLoading = ref(false)
const looseError = ref('')
const chosenVolume = ref('')

async function loadLooseVolumes() {
  const i = inst.value
  if (!i) return
  looseLoading.value = true
  looseError.value = ''
  try {
    const res = await storage.bootVolumes(i.accountId, i.region, i.adFull)
    looseVolumes.value = (res.bootVolumes ?? []).filter(v => v.lifecycleState === 'AVAILABLE')
    // 默认选中最像本机的那块：优先详情里已知的卷，其次按名字猜。
    // Oracle 建实例时把引导卷取成同名，绝大多数情况一猜一个准。
    const known = detail.value?.bootVolume?.id
    const byName = looseVolumes.value.find(v => v.displayName === i.name)
    chosenVolume.value = known || byName?.id || ''
  } catch (err) {
    // 不能静默吞掉：读失败和「这个可用域里确实没有卷」在界面上长得一模一样，
    // 都是一个空下拉框，人会以为卷丢了。
    looseVolumes.value = []
    looseError.value = errorText(err)
  } finally {
    looseLoading.value = false
  }
}

// 进入「已分离」状态时才去拉列表——正常实例没必要多打一次 OCI。
watch(bootDetached, on => { if (on) loadLooseVolumes() }, { immediate: true })

const looseVolumeGroups = computed(() => [{
  label: t.instDrawer.bootsOf(inst.value?.ad ?? ''),
  options: looseVolumes.value.map(v => ({
    value: v.id, label: `${v.displayName} · ${v.sizeInGBs} GB`
  }))
}])

/**
 * 能接收这块卷的实例。
 *
 * 三个硬条件缺一不可：同账号（跨账号看不见对方的资源）、同区域、
 * **同可用域**——卷不能跨 AD 挂载，这是最容易踩的一条，Oracle 只会回一句
 * 语焉不详的 400。另外目标机得是运行中的，否则挂上去也没法登进去改文件。
 */
const rescueTargets = computed(() => {
  const i = inst.value
  if (!i) return []
  return state.instances.filter(t =>
    t.id !== i.id &&
    t.accountId === i.accountId &&
    t.region === i.region &&
    t.adFull === i.adFull &&
    t.state === 'RUNNING')
})

const rescueTarget = ref('')
const rescueBusy = ref(false)

/**
 * 挂载后在救援机上要跑的命令。
 *
 * 写死 /dev/sdb 会坑人——救援机自己可能已经挂了别的盘，新盘未必是 sdb。
 * 所以第一条是 lsblk，让人自己确认；后面的命令都以 $DEV 变量表述。
 * 分区选 1 是因为 OCI 的 Ubuntu/OL 镜像根分区固定是第一个，
 * 14/15/16 分别是 BIOS boot、EFI 和 /boot，挂错了会找不到 home 目录。
 */
const rescueSteps = computed(() => [
  t.instDrawer.rescue1,
  'lsblk',
  '',
  t.instDrawer.rescue2,
  'DEV=/dev/sdb',
  'sudo mkdir -p /mnt/rescue',
  'sudo mount ${DEV}1 /mnt/rescue',
  t.instDrawer.rescueUser,
  '',
  t.instDrawer.rescue3,
  'USER=ubuntu',
  'sudo mkdir -p /mnt/rescue/home/$USER/.ssh',
  t.instDrawer.rescueKey,
  'sudo chmod 700 /mnt/rescue/home/$USER/.ssh',
  'sudo chmod 600 /mnt/rescue/home/$USER/.ssh/authorized_keys',
  t.instDrawer.rescueUid,
  'sudo chown -R 1000:1000 /mnt/rescue/home/$USER/.ssh',
  '',
  t.instDrawer.rescue4,
  'sudo umount /mnt/rescue'
].join('\n'))

const rescueTargetGroups = computed(() => [{
  label: t.instDrawer.sameAdRunning,
  options: rescueTargets.value.map(x => ({
    value: x.id, label: `${x.name} · ${x.publicIp || t.instDrawer.noPublicIp}`
  }))
}])

/** 把本机的引导卷挂到选中的救援机上当数据盘。 */
async function attachToRescue() {
  const i = inst.value
  const volumeId = chosenVolume.value
  const target = rescueTargets.value.find(t => t.id === rescueTarget.value)
  if (!i || !volumeId || !target) return

  ask({
    level: 2,
    title: t.instDrawer.attachRescueTitle(i.name, target.name),
    body: t.instDrawer.attachRescueBody(target.name),
    okLabel: t.instDrawer.attachAsData,
    onConfirm: async () => {
      rescueBusy.value = true
      try {
        const res = await storage.attachVolume(
          i.accountId, i.region, target.id, volumeId, `rescue-${i.name}`)
        toast({ tone: 'success', title: t.instDrawer.attachedTo(target.name), body: res.notice })
        await loadDetail()
      } catch (err) {
        toastError(t.instDrawer.attachFailed, err)
      } finally {
        rescueBusy.value = false
      }
    }
  })
}

/** 分离一块数据盘。救援收尾用，也用于「附加块存储」里的普通盘。 */
async function detachData(attachmentId: string, name: string) {
  const i = inst.value
  if (!i) return
  ask({
    level: 2,
    title: t.instDrawer.detachTitle(name),
    body: t.instDrawer.detachBody,
    okLabel: t.instDrawer.detach,
    onConfirm: async () => {
      try {
        const res = await storage.detachVolume(attachmentId, i.accountId, i.region)
        toast({ tone: 'success', title: t.instDrawer.detached, body: res.notice })
        await loadDetail()
      } catch (err) {
        toastError(t.instDrawer.detachFailed, err)
      }
    }
  })
}

/** 把引导卷挂回自己，恢复可启动状态。 */
async function reattachBoot() {
  const i = inst.value
  const volumeId = chosenVolume.value
  if (!i || !volumeId) return
  ask({
    level: 2,
    title: t.instDrawer.reattachTitle(i.name),
    body: t.instDrawer.reattachBody,
    okLabel: t.instDrawer.reattach,
    onConfirm: async () => {
      rescueBusy.value = true
      try {
        await storage.attachBootVolume(i.accountId, i.region, i.id, volumeId)
        toast({ tone: 'success', title: t.instDrawer.reattached, body: t.instDrawer.reattachedBody })
        await loadDetail()
      } catch (err) {
        toastError(t.instDrawer.reattachFailed, err)
      } finally {
        rescueBusy.value = false
      }
    }
  })
}

function askTerminate() {
  const i = inst.value
  if (!i) return
  ask({
    level: 3, title: t.instDrawer.terminateTitle(i.name),
    body: t.instDrawer.terminateBody,
    noun: i.name, nounLabel: t.rowActions.instanceName,
    losses: [
      t.rowActions.lossInstance,
      t.instDrawer.lossBoot(i.bootGb),
      i.publicIp !== '—' ? t.rowActions.lossIp(i.publicIp) : t.instDrawer.lossNet
    ],
    okLabel: t.rowActions.terminatePermanently,
    onConfirm: () => { closeDrawer(); terminate(i.id) }
  })
}

onMounted(() => {
  void loadDetail()
  void loadAudit()
  // 直接以「监控」页打开抽屉时 watch(active) 不会触发，这里补一次。
  if (active.value === 'metrics') refreshMonitoring()
})

watch(active, tab => {
  if (tab === 'metrics' && !metrics.value) void loadMetrics()
  if (tab === 'metrics' && !traffic.value) void loadTraffic()
  if (tab === 'history') void loadAudit()
})

watch(metricHours, () => void loadMetrics())
</script>

<template>
  <AppDrawer v-if="inst && account" width="wide" @close="closeDrawer()">
    <DrawerHeader :color="color" :code="account.code" :title="inst.name"
                  :sub="`${inst.region} · ${inst.shape} · ocid1…${inst.ocidTail}`"
                  @close="closeDrawer()">
      <template #badge>
        <StateBadge :state="inst.state" :settled="!!inst.settledAt" />
      </template>
      <template #actions>
        <button v-if="inst.state === 'STOPPED'" class="btn btn--sm"
                style="border-color: var(--success); color: var(--success)"
                :disabled="locked" @click="start(inst.id)">{{ t.instDrawer.start }}</button>
        <button v-else class="btn btn--sm" :disabled="locked" @click="stop(inst.id)">{{ t.instDrawer.stop }}</button>
        <button class="btn btn--sm" :disabled="locked" @click="restart(inst.id)">{{ t.instDrawer.softReboot }}</button>
        <button class="btn btn--sm btn--danger" :disabled="locked || !state.options.allowTerminate"
                :title="state.options.allowTerminate ? '' : t.rowActions.disabledInSettings"
                @click="askTerminate()">{{ t.instDrawer.terminate }}</button>
      </template>
    </DrawerHeader>

    <DrawerTabs :tabs="TABS" :labels="t.instDrawer.tabs" v-model:active="active" />

    <DrawerBody>
      <RouterLink v-for="j in runningJobs" :key="j.id" to="/jobs" class="job-banner" @click="closeDrawer()">
        <span class="job-banner__spin" />
        <span class="job-banner__text">{{ j.status === 'queued' ? t.instDrawer.queued : t.instDrawer.running }}: {{ j.title }}</span>
        <span class="mono job-banner__pct">{{ j.progress }}%</span>
      </RouterLink>
      <p v-if="inst.lastError" class="err">{{ inst.lastError }}</p>
      <p v-if="detailError" class="err">{{ detailError }}</p>
      <p v-for="w in detail?.warnings ?? []" :key="w" class="warn">{{ w }}</p>

      <!-- 切标签时内容淡入淡出：外层按标签换 key，Transition 需要单一子元素 -->
      <Transition name="swap" mode="out-in">
      <div :key="active">
      <template v-if="active === 'overview'">
        <SectionCard :title="t.instDrawer.basicInfo">
          <KeyValueList :items="[
            { k: 'OCID', v: inst.id, mono: true, copyable: true },
            { k: t.instDrawer.kState, v: t.enums.lifecycle[inst.state] ?? inst.state, mono: true },
            { k: t.instDrawer.kRegionAd, v: `${inst.region} · ${inst.ad}`, mono: true },
            { k: t.instDrawer.kUptime, v: uptimeDetail, mono: true },
            { k: t.instDrawer.kAccount, v: `${account.alias} · ${account.code}`, tone: color }
          ]" />
        </SectionCard>

        <SectionCard :title="t.instDrawer.computeConfig">
          <KeyValueList :items="[
            { k: 'Shape', v: inst.shape, mono: true },
            { k: 'OCPU', v: String(inst.ocpu), mono: true },
            { k: t.instDrawer.kMemory, v: `${inst.memGb} GB`, mono: true },
            { k: t.instDrawer.kBoot, v: `${inst.bootGb} GB · VPU ${inst.vpu}`, mono: true }
          ]" />
        </SectionCard>

        <SectionCard v-if="sshKeys" :title="t.instDrawer.sshKeys" :note="t.instDrawer.injected">
          <CodeBlock :code="sshKeys" />
        </SectionCard>

        <SectionCard v-if="cloudInit" :title="t.instDrawer.cloudInitMeta" :note="t.instDrawer.injectedReadonly">
          <CodeBlock :code="cloudInit" />
        </SectionCard>
      </template>

      <template v-else-if="active === 'network'">
        <SkeletonRows v-if="detailLoading" :rows="4" />
        <template v-else>
          <!-- 网卡放在一起：主网卡在前、附属网卡随后，增删网卡的入口放最下面。
               主网卡的两项操作（换 IP、开 IPv6）直接挂在卡片头上，不再让人去行内菜单找。 -->
          <SectionCard v-if="primaryVnic" :title="t.instDrawer.primaryCard(primaryVnic.displayName || t.common.unnamed)"
                       :note="`nic ${primaryVnic.nicIndex}`">
            <template #action>
              <span class="card-actions">
                <button class="btn btn--sm" :disabled="!!addingIpv6Vnic || locked" @click="addIpv6(primaryVnic)">
                  {{ addingIpv6Vnic === primaryVnic.vnicId ? t.instDrawer.adding : primaryVnic.ipv6?.length ? t.instDrawer.addV6 : t.instDrawer.enableV6 }}
                </button>
                <button class="btn btn--sm" :class="{ 'btn--warning': primaryVnic.publicIp }"
                        :disabled="!!changingIpVnic || locked || primaryVnic.publicIpType === 'RESERVED'"
                        :title="ipButtonTitle(primaryVnic)" @click="changeIp(primaryVnic)">
                  {{ changingIpVnic === primaryVnic.vnicId ? t.instDrawer.processing : primaryVnic.publicIp ? t.instDrawer.changePublicIp : t.instDrawer.assignPublicIp }}
                </button>
              </span>
            </template>
            <KeyValueList :items="vnicItems(primaryVnic)" />
          </SectionCard>

          <SectionCard v-for="v in secondaryVnics" :key="v.vnicId"
                       :title="t.instDrawer.secondaryCard(v.displayName || t.common.unnamed)" :note="`nic ${v.nicIndex}`">
            <template #action>
              <span class="card-actions">
                <button class="btn btn--sm" :disabled="!!addingIpv6Vnic || locked" @click="addIpv6(v)">
                  {{ addingIpv6Vnic === v.vnicId ? t.instDrawer.adding : t.instDrawer.addV6 }}
                </button>
                <button class="btn btn--sm" :class="{ 'btn--warning': v.publicIp }"
                        :disabled="!!changingIpVnic || locked || v.publicIpType === 'RESERVED'"
                        :title="ipButtonTitle(v)" @click="changeIp(v)">
                  {{ changingIpVnic === v.vnicId ? t.instDrawer.processing : v.publicIp ? t.instDrawer.changePublicIp : t.instDrawer.assignPublicIp }}
                </button>
                <button class="btn btn--sm" :disabled="locked" @click="detachVnic(v)">{{ t.instDrawer.detach }}</button>
              </span>
            </template>
            <KeyValueList :items="vnicItems(v)" />
          </SectionCard>

          <SectionCard :title="t.instDrawer.addVnicTitle"
                       :note="maxVnics ? t.instDrawer.vnicUsage(vnicCount, maxVnics) : ''">
            <template #action>
              <button v-if="!addingVnic" class="btn btn--sm" :disabled="locked || vnicFull"
                      :title="vnicFull ? t.instDrawer.vnicLimit : ''" @click="openAddVnic()">{{ t.instDrawer.addVnic }}</button>
            </template>
            <div v-if="addingVnic" class="pad vnic-form">
              <div class="field">
                <label>{{ t.instDrawer.subnet }}</label>
                <SelectMenu v-model="vnicForm.subnetId" :groups="vnicSubnetGroups"
                            :placeholder="vnicSubnetsLoading ? t.common.loading : t.instDrawer.pickSubnet" :aria-label="t.instDrawer.vnicSubnet" />
              </div>
              <div class="field">
                <label for="vnicname">{{ t.instDrawer.vnicName }}</label>
                <input id="vnicname" v-model="vnicForm.name" class="input mono" maxlength="255"
                       :placeholder="t.instDrawer.vnicNamePlaceholder" />
              </div>
              <SwitchRow v-model="vnicForm.publicIp" :title="t.createInst.assignV4"
                         :sub="vnicSubnet?.prohibitPublicIpOnVnic ? t.instDrawer.subnetNoPublic : t.instDrawer.vnicV4Sub" />
              <SwitchRow v-model="vnicForm.ipv6" :title="t.instDrawer.assignV6"
                         :sub="t.instDrawer.assignV6Sub" />
              <div class="vnic-form__actions">
                <button class="btn btn--sm btn--primary"
                        :disabled="attachingVnic || !vnicForm.subnetId || (vnicForm.publicIp && !!vnicSubnet?.prohibitPublicIpOnVnic)"
                        @click="submitVnic()">{{ attachingVnic ? t.instDrawer.adding : t.instDrawer.add }}</button>
                <button class="btn btn--sm" :disabled="attachingVnic" @click="addingVnic = false">{{ t.common.cancel }}</button>
              </div>
            </div>
            <CheckList :items="[
              { tone: 'info', text: t.instDrawer.vnicInfo1 },
              { tone: 'warn', text: t.instDrawer.vnicInfo2 },
              { tone: 'info', text: t.instDrawer.vnicInfo3 }
            ]" />
          </SectionCard>
        </template>
      </template>

      <template v-else-if="active === 'storage'">
        <BootVolumeEditor :instance="inst" :account="account"
                          :boot-volume-id="detail?.bootVolume?.id" />
        <ReplaceBootVolume :instance="inst" />

        <SectionCard :title="t.instDrawer.blockVolumes">
          <SkeletonRows v-if="detailLoading" :rows="2" />
          <template v-else-if="(detail?.blockVolumes?.length ?? 0) > 0">
            <div v-for="v in detail?.blockVolumes ?? []" :key="v.volumeId" class="rule">
              <span class="rule__bar" :style="{ background: color }" />
              <span class="mono t-xs">{{ v.displayName }} · {{ v.sizeInGbs }} GB · VPU {{ v.vpusPerGb }}</span>
              <span class="rule__desc mono t-2xs dim-3">{{ v.device || v.state }}</span>
              <button v-if="v.attachmentId" class="btn btn--xs btn--warning"
                      @click="detachData(v.attachmentId, v.displayName)">{{ t.instDrawer.detach }}</button>
            </div>
          </template>
          <CheckList v-else :items="[{ tone: 'info', text: t.instDrawer.noBlockVolumes }]" />
        </SectionCard>

        <SectionCard :title="t.instDrawer.rescueMode" :note="t.instDrawer.rescueNote">
          <!-- 第一步：引导卷还挂着 -->
          <div v-if="!bootDetached" class="pad">
            <CheckList :items="[
              { tone: inst.state === 'STOPPED' ? 'ok' : 'warn',
                text: inst.state === 'STOPPED'
                  ? t.instDrawer.canDetach
                  : t.instDrawer.needStop },
              { tone: 'info', text: t.instDrawer.rescueUse }
            ]" />
            <button class="btn btn--sm btn--warning"
                    :disabled="inst.state !== 'STOPPED' || !detail?.bootVolumeAttachmentId"
                    @click="detachBoot()">{{ t.instDrawer.detachBoot }}</button>
          </div>

          <!-- 第二步：已分离，挂到救援机 -->
          <div v-else class="pad">
            <CheckList :items="[
              { tone: 'warn', text: t.instDrawer.detachedWarn },
              ...(rescueTargets.length
                ? [{ tone: 'info' as const,
                     text: t.instDrawer.rescueTargets(rescueTargets.length, inst.ad) }]
                : [{ tone: 'warn' as const,
                     text: t.instDrawer.noRescueTargets(inst.region, inst.ad) }])
            ]" />

            <!-- 卷分离之后就「无主」了，缓存里的 BootVolumeID 也被同步清掉，
                 所以得让人从本可用域的引导卷里认一下是哪块。默认按同名猜。 -->
            <div class="rescue">
              <span class="t-2xs dim-3">{{ t.instDrawer.volumeToRescue }}</span>
              <SelectMenu v-model="chosenVolume" :groups="looseVolumeGroups"
                          :placeholder="looseLoading ? t.common.loading : t.instDrawer.pickBoot"
                          :aria-label="t.instDrawer.bootToRescue" />
              <button class="btn btn--xs" :disabled="looseLoading" @click="loadLooseVolumes()">{{ t.instDrawer.reread }}</button>
            </div>
            <CheckList v-if="looseError"
                       :items="[{ tone: 'fail', text: t.instDrawer.looseFailed(looseError) }]" />
            <CheckList v-else-if="!looseLoading && !looseVolumes.length"
                       :items="[{ tone: 'warn',
                                  text: t.instDrawer.noLoose(inst.ad) }]" />

            <div v-if="rescueTargets.length" class="rescue">
              <span class="t-2xs dim-3">{{ t.instDrawer.attachTo }}</span>
              <SelectMenu v-model="rescueTarget" :groups="rescueTargetGroups"
                          :placeholder="t.instDrawer.pickRescue" :aria-label="t.instDrawer.rescueTarget" />
              <button class="btn btn--sm btn--primary"
                      :disabled="!rescueTarget || !chosenVolume || rescueBusy"
                      @click="attachToRescue()">{{ t.instDrawer.attachAsData }}</button>
            </div>

            <details class="rescue__how">
              <summary class="t-xs dim-2">{{ t.instDrawer.howToFix }}</summary>
              <CodeBlock :code="rescueSteps" />
            </details>

            <div class="rescue__back">
              <span class="t-2xs dim-3">{{ t.instDrawer.afterFix }}</span>
              <button class="btn btn--sm" :disabled="rescueBusy || !chosenVolume"
                      @click="reattachBoot()">{{ t.instDrawer.reattach }}</button>
            </div>
          </div>
        </SectionCard>
      </template>

      <template v-else-if="active === 'metrics'">
        <SectionCard :title="t.instDrawer.timeRange">
          <div class="pad pad--row">
            <div class="range">
              <button v-for="h in [6, 24, 72, 168]" :key="h" class="btn btn--sm"
                      :class="{ 'is-on': metricHours === h }" @click="metricHours = h">
                {{ h < 24 ? t.instDrawer.hours(h) : t.instDrawer.days(h / 24) }}
              </button>
            </div>
            <button class="btn btn--sm" :disabled="metricsLoading || trafficLoading" @click="refreshMonitoring()">
              {{ metricsLoading || trafficLoading ? t.instDrawer.loading : t.common.refresh }}
            </button>
          </div>
        </SectionCard>

        <CheckList v-if="noMetricData"
                   :items="[{ tone: 'warn', text: metrics?.notice ?? t.instDrawer.noMetrics }]" />
        <CheckList v-else-if="metricErrors.length"
                   :items="metricErrors.map(text => ({ tone: 'warn' as const, text }))" />

        <!-- 概况：先给结论，图表负责细节 -->
        <div v-if="metrics" class="tiles">
          <div class="tile">
            <span class="tile__k">{{ t.instDrawer.cpuLatest }}</span>
            <span class="tile__v mono" :class="{ 'is-high': (cpuStats?.last ?? 0) >= HIGH_USAGE }">
              {{ cpuStats ? formatPercent(cpuStats.last) : '—' }}
            </span>
            <span v-if="cpuStats" class="tile__sub mono">
              {{ t.instDrawer.avgPeak(formatPercent(cpuStats.avg), formatPercent(cpuStats.peak)) }}
            </span>
            <span v-if="(cpuStats?.last ?? 0) >= HIGH_USAGE" class="tile__flag">{{ t.instDrawer.high }}</span>
          </div>
          <div class="tile">
            <span class="tile__k">{{ t.instDrawer.memLatest }}</span>
            <span class="tile__v mono" :class="{ 'is-high': (memStats?.last ?? 0) >= HIGH_USAGE }">
              {{ memStats ? formatPercent(memStats.last) : '—' }}
            </span>
            <span v-if="memStats" class="tile__sub mono">
              <template v-if="memUsedGb !== null">{{ t.instDrawer.memApprox(memUsedGb, inst.memGb) }}</template>{{ t.instDrawer.peak(formatPercent(memStats.peak)) }}
            </span>
            <span v-if="(memStats?.last ?? 0) >= HIGH_USAGE" class="tile__flag">{{ t.instDrawer.high }}</span>
          </div>
          <div class="tile">
            <span class="tile__k"><i class="dot dot--1" />{{ t.instDrawer.outLatest }}</span>
            <span class="tile__v mono">{{ outStats ? formatBitRate(outStats.last) : '—' }}</span>
            <span v-if="outStats" class="tile__sub mono">
              {{ t.instDrawer.avgPeak(formatBitRate(outStats.avg), formatBitRate(outStats.peak)) }}
            </span>
          </div>
          <div class="tile">
            <span class="tile__k"><i class="dot dot--2" />{{ t.instDrawer.inLatest }}</span>
            <span class="tile__v mono">{{ inStats ? formatBitRate(inStats.last) : '—' }}</span>
            <span v-if="inStats" class="tile__sub mono">
              {{ t.instDrawer.avgPeak(formatBitRate(inStats.avg), formatBitRate(inStats.peak)) }}
            </span>
          </div>
        </div>
        <p v-if="metrics" class="t-2xs dim-3 tiles__foot">
          {{ t.instDrawer.statsOf(rangeText) }}{{ resolutionText ? ' · ' + resolutionText : '' }}{{ t.instDrawer.agentSource }}
        </p>

        <SectionCard :title="t.instDrawer.netRate" :note="t.instDrawer.netRateNote(rangeText)">
          <SkeletonRows v-if="metricsLoading && !metrics" :rows="3" />
          <MetricChart v-else :series="rateSeries" :format="formatBits" :height="220"
                       :empty-text="t.instDrawer.noTraffic" />
        </SectionCard>

        <SectionCard :title="t.instDrawer.cpuMem" :note="t.instDrawer.cpuMemNote(rangeText)">
          <SkeletonRows v-if="metricsLoading && !metrics" :rows="3" />
          <MetricChart v-else :series="usageSeries" :format="formatPercent" :y-max="100" :height="220"
                       :empty-text="t.instDrawer.noCpuMem" />
        </SectionCard>

        <SectionCard :title="t.instDrawer.monthTraffic"
                     :note="traffic ? t.instDrawer.monthTrafficNote(traffic.month, traffic.vnics) : t.instDrawer.vcnVnicStats">
          <SkeletonRows v-if="trafficLoading && !traffic" :rows="2" />
          <p v-else-if="trafficError" class="pad t-xs traffic__err">{{ trafficError }}</p>
          <template v-else-if="traffic">
            <div class="tiles tiles--inset">
              <div class="tile">
                <span class="tile__k"><i class="dot dot--1" />{{ t.instDrawer.outMonth }}</span>
                <span class="tile__v mono">{{ formatBytes(traffic.outBytes) }}</span>
                <span class="tile__sub mono">{{ t.instDrawer.last24h(formatBytes(traffic.last24hOutBytes)) }}</span>
              </div>
              <div class="tile">
                <span class="tile__k"><i class="dot dot--2" />{{ t.instDrawer.inMonth }}</span>
                <span class="tile__v mono">{{ formatBytes(traffic.inBytes) }}</span>
                <span class="tile__sub mono">{{ t.instDrawer.last24h(formatBytes(traffic.last24hInBytes)) }}</span>
              </div>
            </div>
            <MetricChart type="bar" :categories="trafficDays" :series="trafficSeries"
                         :format="trafficScale.format" :height="200" :empty-text="t.instDrawer.noMonthTraffic" />
            <div class="pad pad--tight">
              <p v-if="traffic.error" class="t-2xs traffic__err">{{ t.instDrawer.partialVnics(traffic.error) }}</p>
              <p class="t-2xs dim-3 traffic__note">{{ traffic.notice }}</p>
            </div>
          </template>
        </SectionCard>
      </template>

      <template v-else-if="active === 'console'">
        <SectionCard :title="t.instDrawer.serialConsole" :note="t.instDrawer.serialNote">
          <CheckList :items="[
            { tone: 'info', text: t.instDrawer.serialInfo1 },
            { tone: 'info', text: t.instDrawer.serialInfo2 }
          ]" />
        </SectionCard>

        <SectionCard :title="t.instDrawer.createConn">
          <div class="pad">
            <div class="field">
              <label for="conkey">{{ t.instDrawer.rsaKey }}</label>
              <textarea id="conkey" v-model="sshPublicKey" class="textarea" style="height: 68px"
                        spellcheck="false" placeholder="ssh-rsa AAAAB3NzaC1yc2E…" />
              <p class="hint">
                {{ t.instDrawer.rsaHint }}
                <code>ssh-keygen -t rsa -b 4096 -f ~/.ssh/oci_console</code>
              </p>
            </div>
            <button class="btn btn--sm" :disabled="consoleCreating" @click="createConsole()">
              {{ consoleCreating ? t.instDrawer.creating : t.instDrawer.createConsole }}
            </button>
          </div>
        </SectionCard>

        <template v-if="consoleResult">
          <SectionCard :title="t.instDrawer.serialCmd" :note="t.instDrawer.runLocally">
            <CodeBlock copyable :code="consoleResult.serialConsoleCommand" />
          </SectionCard>
          <SectionCard :title="t.instDrawer.vncCmd" :note="t.instDrawer.vncNote">
            <CodeBlock copyable :code="consoleResult.vncConsoleCommand" />
          </SectionCard>
        </template>
      </template>

      <template v-else>
        <SectionCard :title="t.instDrawer.instanceAudit" :note="t.instDrawer.nRecords(auditRows.length)">
          <div v-for="r in auditRows" :key="r.id" class="rule">
            <span class="rule__bar"
                  :style="{ background: r.result === 'ok' ? 'var(--success)' : 'var(--danger)' }" />
            <span class="t-xs">{{ r.action }}{{ r.detail ? ` · ${r.detail}` : '' }}</span>
            <span class="rule__desc mono t-2xs dim-3">{{ relativeTime(r.createdAt) }}</span>
          </div>
          <CheckList v-if="auditRows.length === 0"
                     :items="[{ tone: 'info', text: t.instDrawer.noRecords }]" />
        </SectionCard>
      </template>
      </div>
      </Transition>
    </DrawerBody>
  </AppDrawer>
</template>

<style scoped>
.card-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 6px; }
.vnic-form { display: flex; flex-direction: column; gap: 12px; border-bottom: 1px solid var(--border-subtle); }
.vnic-form__actions { display: flex; gap: 8px; }
.rule { display: flex; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid var(--border-subtle); }
.rule__bar { width: 3px; height: 22px; flex: 0 0 auto; border-radius: var(--radius-full); }
.rule__desc { margin-left: auto; text-align: right; white-space: nowrap; }
.pad { padding: 14px 16px; display: flex; flex-direction: column; gap: 12px; }
.pad--row { flex-direction: row; align-items: center; justify-content: space-between; }
.range { display: flex; gap: 6px; }
.pad--tight { padding-top: 0; gap: 6px; }

/* 概况卡：抽屉宽度下是 2×2，窄屏时自动折成一列 */
.tiles { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
@media (max-width: 480px) { .tiles { grid-template-columns: 1fr; } }
.tiles--inset { padding: 14px 16px 0; }
.tiles__foot { margin: -4px 0 0; }
.tile {
  display: flex; flex-direction: column; gap: 4px; min-width: 0;
  padding: 12px 14px; border-radius: var(--radius-md);
  background: var(--bg-surface); border: 1px solid var(--border-subtle);
}
.tiles--inset .tile { background: var(--bg-inset); border-color: transparent; }
.tile__k { display: flex; align-items: center; gap: 6px; font-size: 11px; color: var(--text-secondary); }
.tile__v { font-size: 20px; font-weight: 600; line-height: 1.25; color: var(--text-primary); }
.tile__v.is-high { color: var(--warning); }
.tile__sub { font-size: 11px; color: var(--text-tertiary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tile__flag { font-size: 11px; font-weight: 600; color: var(--warning); }
/* 与 MetricChart 的分类色一致：出站蓝、入站橙 */
.dot { width: 8px; height: 8px; border-radius: 2px; flex: 0 0 auto; }
.dot--1 { background: #2a78d6; }
.dot--2 { background: #eb6834; }
:root[data-theme='dark'] .dot--1 { background: #3987e5; }
:root[data-theme='dark'] .dot--2 { background: #d95926; }
.traffic__err { color: var(--danger); margin: 0; }
.traffic__note { margin: 0; line-height: 1.7; }
.range .btn.is-on { border-color: var(--accent); color: var(--accent); background: var(--accent-soft); }
.err, .warn {
  margin: 12px 16px 0; padding: 10px 12px; border-radius: var(--radius-md);
  font-size: 12px; line-height: 18px;
}
.err { border: 1px solid var(--danger); background: var(--danger-soft); color: var(--danger); }
.warn { border: 1px solid var(--warning); background: var(--warning-soft); color: var(--warning); }

/* ---- 救援模式 ---- */

.rescue {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-top: 10px;
}

.rescue__how {
  margin-top: 12px;
}

.rescue__how summary {
  cursor: pointer;
  user-select: none;
  padding: 4px 0;
}

.rescue__back {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px solid var(--border-subtle);
}

.job-banner {
  display: flex; align-items: center; gap: 10px; margin: 0 0 10px; padding: 9px 12px;
  border-radius: var(--radius-md); border: 1px solid var(--accent);
  background: var(--bg-inset); color: var(--text-primary); font-size: 12px; text-decoration: none;
}
.job-banner:hover { background: var(--bg-hover); }
.job-banner__spin {
  width: 10px; height: 10px; flex: 0 0 auto; border-radius: var(--radius-full);
  border: 2px solid var(--accent); border-right-color: transparent; animation: job-spin 0.9s linear infinite;
}
.job-banner__text { flex: 1 1 auto; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.job-banner__pct { color: var(--text-secondary); }
@keyframes job-spin { to { transform: rotate(360deg); } }
</style>
