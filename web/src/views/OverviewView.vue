<script setup lang="ts">
/** §4.2 总览：KPI count-up · 需要注意的 · 分布矩阵 · 配额 · 最近操作 */
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useStore } from '@/store'
import { acctColor, countUp, shortRegion, pct } from '@/lib/format'
import { accountStatusText, relativeTime } from '@/lib/adapt'
import { insights, hunt as huntApi, type AuditEntryDTO, type HuntTaskDTO } from '@/api'
import { now } from '@/lib/clock'
import QuotaMeter from '@/components/QuotaMeter.vue'
import SectionCard from '@/components/SectionCard.vue'

const router = useRouter()
const { state, accountById, openDrawer, allRegions, visibleInstances } = useStore()

/**
 * 总览必须跟着顶栏的账号 / 区域筛选器走。
 *
 * 之前整页直接读 state.accounts 与 state.instances，两个筛选器在这一页
 * 点了完全没反应——KPI、矩阵、配额、告警全是全量数据。控件摆在最显眼的
 * 位置却不起作用，比没有更糟。
 *
 * 筛选器为空表示"不限"，不是"全都不要"。
 */
const shownAccounts = computed(() =>
  state.accounts.filter(a => state.accountFilter.size === 0 || state.accountFilter.has(a.id))
)

/** 矩阵的列。同样受区域筛选影响，但不能改 store 里的 allRegions——顶栏下拉靠它列选项。 */
const shownRegions = computed(() =>
  allRegions.value.filter(r => state.regionFilter.size === 0 || state.regionFilter.has(r))
)

/** 未终止且在筛选范围内的实例。visibleInstances 已经处理了两个筛选器。 */
const shownInstances = computed(() =>
  visibleInstances.value.filter(i => i.state !== 'TERMINATED')
)

const live = ref([0, 0, 0, 0])
const totals = computed(() => {
  const active = shownInstances.value
  return [
    shownAccounts.value.length,
    active.length,
    active.filter(i => i.state === 'RUNNING').length,
    0
  ]
})

onMounted(async () => {
  totals.value.forEach((t, n) => countUp(t, v => { live.value[n] = v }))
  try {
    auditEntries.value = (await insights.audit({ limit: 20 })).entries
  } catch {
    // 时间线是锦上添花，读不到不影响总览的其余部分。
  }
  try {
    huntTasks.value = (await huntApi.list()).tasks
  } catch {
    // 同上。没有守候任务时这张卡整个不渲染。
  }
})

// 数据到齐后重新跑一次 count-up，否则首屏骨架期间会停在 0。
watch(totals, next => {
  next.forEach((t, n) => countUp(t, v => { live.value[n] = v }))
})

/** 账号健康度摘要。全部正常时不该显示"0 认证失败"这种噪音。 */
const accountSummary = computed(() => {
  const failed = shownAccounts.value.filter(a => a.status === 'error').length
  const disabled = shownAccounts.value.filter(a => a.status === 'disabled').length
  const parts: string[] = []
  if (failed) parts.push(`${failed} 认证失败`)
  if (disabled) parts.push(`${disabled} 已禁用`)
  return parts.length ? parts.join(' · ') : '全部正常'
})

const syncSummary = computed(() => {
  if (state.syncing) return '正在同步…'
  if (!state.lastSync) return '尚未同步实例列表'
  return `上次同步实例 ${relativeTime(state.lastSync)}`
})

/** 页头的绝对时间。副行给的是相对时间，这里给准确值，便于对日志。 */
const lastSyncAbsolute = computed(() => {
  if (!state.lastSync) return '尚未同步'
  const d = new Date(state.lastSync)
  if (Number.isNaN(d.getTime())) return '尚未同步'
  return `同步于 ${d.toLocaleString('zh-CN', { hour12: false })}`
})

/**
 * 总览只放得下几个账号的配额。
 *
 * 原来取的是 accounts.slice(0, 4)——按接入顺序砍掉其余账号，界面上
 * 不留任何痕迹。账号多了以后，最该看的那个很可能正好在第五位。
 * 改成按 ARM 用量降序，并把没列出的个数写进标题。
 */
const QUOTA_PREVIEW = 6

const sortedByUsage = computed(() =>
  [...shownAccounts.value].sort((a, b) => {
    const ra = a.quota.ocpuLimit ? a.quota.ocpuUsed / a.quota.ocpuLimit : -1
    const rb = b.quota.ocpuLimit ? b.quota.ocpuUsed / b.quota.ocpuLimit : -1
    return rb - ra
  })
)
const topQuotaAccounts = computed(() => sortedByUsage.value.slice(0, QUOTA_PREVIEW))
const hiddenQuotaAccounts = computed(() =>
  Math.max(0, shownAccounts.value.length - QUOTA_PREVIEW)
)

const kpis = computed(() => [
  { label: '接入账号', value: String(live.value[0]), sub: accountSummary.value, tone: 'var(--text-primary)' },
  { label: '实例总数', value: String(live.value[1]), sub: `${shownRegions.value.length} 个区域`, tone: 'var(--text-primary)' },
  { label: '运行中', value: String(live.value[2]), sub: `${totals.value[1] - totals.value[2]} 已停止`, tone: 'var(--success)' },
  // 这张卡原来叫"同步状态"，值却是 SSE 连接的通断——两件事混在一格里，
  // 谁都看不懂它在说什么。现在标题说的就是那个值本身，上次同步降到副行。
  { label: '实时连接', value: state.live ? '已连接' : '已断开', sub: syncSummary.value,
    tone: state.live ? 'var(--success)' : 'var(--warning)' }
])

interface Alert {
  code: string; severity: '严重' | '异常' | '告警' | '提示'
  text: string; detail: string; accountId: string; go: () => void
}

const alerts = computed<Alert[]>(() => {
  const out: Alert[] = []
  shownAccounts.value.filter(a => a.status === 'error').forEach(a => out.push({
    code: '401', severity: '严重', accountId: a.id,
    text: `${a.alias} 凭据失效：NotAuthenticated —— 密钥可能已在 Oracle 控制台被删除`,
    detail: `${a.code} · ${accountStatusText(a)}`,
    go: () => openDrawer({ kind: 'account', id: a.id, tab: '概览' })
  }))
  shownInstances.value.filter(i => i.anomaly).forEach(i => out.push({
    code: 'STOP', severity: '异常', accountId: i.accountId,
    text: `${i.name} 意外进入 STOPPED，非用户操作`, detail: i.region,
    go: () => openDrawer({ kind: 'instance', id: i.id, tab: '操作记录' })
  }))
  shownAccounts.value.filter(a => a.quota.memUsed / a.quota.memLimit >= 0.9).forEach(a => out.push({
    code: 'QUOTA', severity: '告警', accountId: a.id,
    text: `${a.alias} 内存配额 ${a.quota.memUsed} / ${a.quota.memLimit} GB，已达 ${pct(a.quota.memUsed, a.quota.memLimit)}%`,
    detail: `${a.code} · ARM`,
    go: () => openDrawer({ kind: 'account', id: a.id, tab: '配额' })
  }))
  shownInstances.value.filter(i => i.bootGb / i.bootLimitGb >= 0.9).forEach(i => out.push({
    code: 'DISK', severity: '告警', accountId: i.accountId,
    text: `${i.name} 引导卷 ${i.bootGb} / ${i.bootLimitGb} GB，剩余 ${i.bootLimitGb - i.bootGb} GB`,
    detail: `VPU ${i.vpu}`,
    go: () => router.push('/storage')
  }))
  return out
})

const severityTone = (s: Alert['severity']) =>
  s === '严重' ? 'var(--danger)' : s === '提示' ? 'var(--info)' : 'var(--warning)'
const severityBg = (s: Alert['severity']) =>
  s === '严重' ? 'var(--danger-soft)' : s === '提示' ? 'var(--info-soft)' : 'var(--warning-soft)'

const matrix = computed(() => shownAccounts.value.map(a => ({
  account: a,
  cells: shownRegions.value.map(r => {
    const n = shownInstances.value.filter(i => i.accountId === a.id && i.region === r).length
    return {
      n,
      bg: n
        ? `color-mix(in srgb, ${acctColor(a.colorIndex)} ${Math.min(38, 12 + n * 7)}%, var(--bg-inset))`
        : 'var(--bg-inset)'
    }
  })
})))

/** 最近操作时间线，取自真实审计日志。 */
const auditEntries = ref<AuditEntryDTO[]>([])

const ACTION_LABEL: Record<string, string> = {
  login: '登录', logout: '登出', setup: '首次设置',
  change_password: '修改口令', sessions_revoke_all: '强制会话下线',
  account_create: '添加账号', account_update: '修改账号',
  account_rotate_key: '轮换密钥', account_delete: '删除账号',
  instance_start: '开机', instance_softstop: '关机', instance_stop: '强制关机',
  instance_softreset: '重启', instance_reset: '强制重启',
  instance_rename: '重命名', instance_reshape: '改配置',
  instance_terminate: '终止实例', instance_launch: '创建实例',
  instance_change_ip: '更换公网 IP', instance_enable_ipv6: '启用 IPv6',
  boot_volume_update: '修改引导卷', boot_volume_detach: '分离引导卷',
  security_list_update: '修改安全规则', network_ensure: '自动建网',
  console_create: '建立控制台连接', settings_update: '修改设置',
  hunt_create: '建立守候任务', hunt_pause: '暂停守候', hunt_resume: '恢复守候',
  hunt_delete: '删除守候任务', volume_attach: '挂载数据盘', volume_detach: '分离数据盘',
  boot_volume_attach: '挂载引导卷', volume_update: '修改块存储',
  channel_create: '添加通知渠道', channel_update: '修改通知渠道',
  channel_delete: '删除通知渠道', totp_enable: '启用两步验证'
}

/* ---------- 守候任务 ---------- */

const huntTasks = ref<HuntTaskDTO[]>([])

/**
 * 只显示还在跑或刚出结果的任务。
 *
 * 总览是"一眼看清现状"的地方，把历史上所有停掉的任务堆在这里只会挤掉
 * 真正需要注意的东西。完整列表在守候页。
 */
const activeHunts = computed(() =>
  huntTasks.value.filter(t => t.state === 'running' || t.state === 'paused'))

function huntCountdown(t: HuntTaskDTO): string {
  if (t.state !== 'running') return '已暂停'
  const ms = new Date(t.nextAt).getTime() - now.value
  if (Number.isNaN(ms)) return '—'
  if (ms <= 0) return '即将尝试'
  const sec = Math.round(ms / 1000)
  if (sec < 60) return `${sec} 秒后`
  const m = Math.floor(sec / 60)
  return m < 60 ? `${m} 分后` : `${Math.floor(m / 60)} 小时后`
}

const timeline = computed(() =>
  auditEntries.value.slice(0, 8).map(e => {
    const a = e.accountId ? state.accounts.find(x => x.id === e.accountId) : undefined
    const label = ACTION_LABEL[e.action] ?? e.action
    return {
      code: a?.code ?? '—',
      // 账号可能已被删除，颜色要有兜底，不能用 ! 强断言。
      color: a ? acctColor(a.colorIndex) : 'var(--border-default)',
      text: e.target ? `${label} ${e.target}` : label,
      result: e.result === 'ok' ? '成功' : '失败',
      at: relativeTime(e.createdAt)
    }
  })
)
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">总览</h1>
        <p class="page-sub">{{ shownAccounts.length }} 个账号 · {{ shownRegions.length }} 个区域 · 跨账号态势</p>
      </div>
      <span class="dim-3 mono t-xs">{{ lastSyncAbsolute }}</span>
    </header>

    <div class="kpis">
      <div v-for="k in kpis" :key="k.label" class="card card-pad">
        <p class="kpi__label">{{ k.label }}</p>
        <p class="kpi__value mono" :style="{ color: k.tone }">{{ k.value }}</p>
        <p class="kpi__sub">{{ k.sub }}</p>
      </div>
    </div>

    <!-- 无异常时整块隐藏 -->
    <section v-if="alerts.length" class="card alerts">
      <header class="alerts__head">
        <span class="alerts__dot" />
        <h2 class="t-lg alerts__title">需要注意的</h2>
        <span class="mono t-2xs dim-3">{{ alerts.length }} 项</span>
      </header>
      <button v-for="(a, n) in alerts" :key="n" class="alerts__row" @click="a.go()">
        <span class="acct-bar" :style="{ background: acctColor(accountById(a.accountId).colorIndex) }" />
        <span class="alerts__code mono" :style="{ color: severityTone(a.severity) }">{{ a.code }}</span>
        <span class="alerts__sev" :style="{ color: severityTone(a.severity), background: severityBg(a.severity) }">
          {{ a.severity }}
        </span>
        <span class="alerts__text">{{ a.text }}</span>
        <span class="alerts__detail mono">{{ a.detail }}</span>
        <span class="dim-3">›</span>
      </button>
    </section>

    <div class="grid-6-4">
      <SectionCard title="实例分布矩阵" note="行为账号，列为区域，色块深浅表示密度">
        <!-- 列数必须跟着区域数走。写死列数时，格子会在错误的位置换行——
             1 账号 1 区域刚好排成一行看不出问题，接第二个账号就散了。 -->
        <div class="matrix-scroll">
        <div class="matrix" :style="{ gridTemplateColumns: `92px repeat(${shownRegions.length}, minmax(48px, 1fr))` }">
          <span />
          <span v-for="r in shownRegions" :key="r" class="matrix__col mono">{{ shortRegion(r) }}</span>
          <template v-for="row in matrix" :key="row.account.id">
            <span class="matrix__row-head">
              <span class="matrix__dot" :style="{ background: acctColor(row.account.colorIndex) }" />
              <span class="mono">{{ row.account.code }}</span>
            </span>
            <span v-for="(c, n) in row.cells" :key="n" class="matrix__cell mono"
                  :style="{ background: c.bg, color: c.n ? 'var(--text-primary)' : 'var(--text-tertiary)' }">
              {{ c.n || '·' }}
            </span>
          </template>
        </div>
        </div>
      </SectionCard>

      <!-- 有任务才渲染。没有守候任务时这里不该占一张空卡片——
           总览上的空卡片比空白更糟，它看起来像加载失败。 -->
      <SectionCard v-if="activeHunts.length" title="容量守候"
                   :note="`${activeHunts.length} 个任务进行中`" class="stack">
        <button v-for="t in activeHunts" :key="t.id" class="hunt" @click="router.push('/hunt')">
          <span class="acct-bar" :style="{ background: acctColor(accountById(t.accountId).colorIndex) }" />
          <span class="hunt__code mono"
                :style="{ color: acctColor(accountById(t.accountId).colorIndex) }">
            {{ accountById(t.accountId).code }}
          </span>
          <span class="hunt__name">{{ t.name }}</span>
          <span class="hunt__meta mono">{{ t.attempts }} 次</span>
          <span class="hunt__at mono">{{ huntCountdown(t) }}</span>
        </button>
      </SectionCard>

      <!-- 时间线放在左列而不是整页通栏：右边的配额卡很高，左列到这里为止
           会空出一大块。把它搬上来，两列高度接近，页面也短了一屏。 -->
      <SectionCard title="最近操作" class="stack">
        <div v-for="(t, n) in timeline" :key="n" class="tl__row">
          <span class="acct-bar" :style="{ background: t.color }" />
          <span class="tl__code mono" :style="{ color: t.color }">{{ t.code }}</span>
          <span class="tl__text">{{ t.text }}</span>
          <span class="tl__result" :style="{ color: t.result === '成功' ? 'var(--success)' : 'var(--danger)' }">{{ t.result }}</span>
          <span class="tl__at mono">{{ t.at }}</span>
        </div>
      </SectionCard>

      <SectionCard title="配额与用量"
                   :note="hiddenQuotaAccounts > 0
                     ? `按 ARM 用量排序，另有 ${hiddenQuotaAccounts} 个账号未列出`
                     : '≥90% 转警告色，满额转 danger'">
        <div class="quotas">
          <div v-for="a in topQuotaAccounts" :key="a.id" class="quotas__group">
            <p class="quotas__head">
              <span class="matrix__dot" :style="{ background: acctColor(a.colorIndex) }" />
              <span class="mono" :style="{ color: acctColor(a.colorIndex) }">{{ a.code }}</span>
              <span class="dim t-xs">{{ a.alias }}</span>
            </p>
            <QuotaMeter label="ARM OCPU" :used="a.quota.ocpuUsed" :limit="a.quota.ocpuLimit"
                        :unlimited="a.quota.unlimited.ocpu" />
            <QuotaMeter label="内存" :used="a.quota.memUsed" :limit="a.quota.memLimit" unit=" GB"
                        :unlimited="a.quota.unlimited.mem" />
            <QuotaMeter label="块存储 · 免费" :used="a.quota.blockUsed" :limit="a.quota.blockLimit" unit=" GB"
                        :unlimited="a.quota.unlimited.block" />
          </div>
        </div>
      </SectionCard>
    </div>

  </div>
</template>

<style scoped>
.kpis { display: grid; grid-template-columns: repeat(4, 1fr); gap: 16px; }
.kpi__label { margin: 0; font-size: 12px; color: var(--text-secondary); }
.kpi__value { margin: 4px 0 0; font-size: 40px; line-height: 48px; font-weight: 600; }
.kpi__sub { margin: 0; font-size: 12px; color: var(--text-tertiary); }

.alerts { margin-top: 24px; overflow: hidden; }
.alerts__head { display: flex; align-items: center; gap: 10px; padding: 14px 20px; border-bottom: 1px solid var(--border-subtle); }
.alerts__dot { width: 6px; height: 6px; border-radius: var(--radius-full); background: var(--warning); }
.alerts__title { margin: 0; font-weight: 600; }
.alerts__row {
  display: flex; align-items: center; gap: 14px; width: 100%; height: 52px; padding: 0 20px 0 14px;
  border: 0; border-bottom: 1px solid var(--border-subtle); background: transparent;
  color: var(--text-primary); cursor: pointer; position: relative; text-align: left;
}
.alerts__row:hover { background: var(--bg-hover); }
.alerts__code { width: 44px; flex: 0 0 auto; font-size: 11px; font-weight: 600; }
.alerts__sev { width: 62px; flex: 0 0 auto; text-align: center; padding: 2px 0; border-radius: var(--radius-full); font-size: 11px; font-weight: 600; }
.alerts__text { flex: 1 1 auto; min-width: 0; font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.alerts__detail { flex: 0 0 auto; font-size: 11px; color: var(--text-tertiary); }

/* 左列可以放多张卡，配额卡始终占右列。
   显式指定网格位置而不是靠文档流：卡片是条件渲染的（守候卡没任务就不存在），
   靠顺序自动排会让配额卡在没有守候卡时跳到左列去。 */
.grid-6-4 {
  margin-top: 24px;
  display: grid;
  grid-template-columns: 6fr 4fr;
  gap: 16px;
  align-items: start;
}
.grid-6-4 > :last-child { grid-column: 2; grid-row: 1 / span 99; }
.grid-6-4 > :not(:last-child) { grid-column: 1; }
.stack { min-width: 0; }

.hunt {
  display: flex; align-items: center; gap: 12px; width: 100%; height: 44px;
  padding: 0 16px 0 14px; border: 0; border-bottom: 1px solid var(--border-subtle);
  background: transparent; color: var(--text-primary); cursor: pointer;
  position: relative; text-align: left;
}
.hunt:last-child { border-bottom: none; }
.hunt:hover { background: var(--bg-hover); }
.hunt__code { width: 44px; flex: 0 0 auto; font-size: 11px; font-weight: 600; }
.hunt__name { flex: 1 1 auto; min-width: 0; font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.hunt__meta { flex: 0 0 auto; font-size: 11px; color: var(--text-tertiary); }
.hunt__at { width: 78px; flex: 0 0 auto; text-align: right; font-size: 11px; color: var(--text-secondary); }
/* 区域多了以后格子会被压扁，给一个最小宽度并让容器横向滚动。
   SectionCard 是 overflow: hidden，滚动条必须放在它内部这一层。 */
.matrix-scroll { overflow-x: auto; }
.matrix { padding: 16px; display: grid; gap: 4px; align-items: center; min-width: max-content; }
.matrix__col { font-size: 10px; color: var(--text-tertiary); text-align: center; }
.matrix__row-head { display: flex; align-items: center; gap: 6px; height: 30px; font-size: 11px; font-weight: 600; color: var(--text-secondary); }
.matrix__dot { width: 6px; height: 6px; border-radius: var(--radius-full); flex: 0 0 auto; }
.matrix__cell { height: 30px; border-radius: var(--radius-sm); display: flex; align-items: center; justify-content: center; font-size: 12px; font-weight: 600; }

.quotas { padding: 16px; display: flex; flex-direction: column; gap: 14px; }
.quotas__group { display: flex; flex-direction: column; gap: 5px; }
.quotas__head { margin: 0 0 3px; display: flex; align-items: center; gap: 7px; font-size: 11px; font-weight: 600; }

.tl__row { display: flex; align-items: center; gap: 14px; height: 44px; padding: 0 20px 0 14px; border-bottom: 1px solid var(--border-subtle); position: relative; }
.tl__code { width: 44px; flex: 0 0 auto; font-size: 11px; font-weight: 600; }
.tl__text { flex: 1 1 auto; font-size: 13px; }
.tl__result { font-size: 11px; font-weight: 600; }
.tl__at { width: 132px; text-align: right; font-size: 11px; color: var(--text-tertiary); }

@media (max-width: 1279px) {
  .kpis { grid-template-columns: repeat(2, 1fr); }
  .grid-6-4 { grid-template-columns: 1fr; }
  /* 单列时必须把显式定位撤掉。留着 grid-column: 2 会让浏览器凭空造出
     第二条隐式列，配额卡跑到屏幕外，看起来就是这张卡整个消失了。 */
  .grid-6-4 > :last-child,
  .grid-6-4 > :not(:last-child) { grid-column: 1; grid-row: auto; }
}
</style>
