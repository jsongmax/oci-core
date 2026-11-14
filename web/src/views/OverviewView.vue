<script setup lang="ts">
/** §4.2 总览：KPI count-up · 需要注意的 · 分布矩阵 · 配额 · 最近操作 */
import { t, fmtDateTime } from '@/i18n'
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useStore } from '@/store'
import { acctColor, countUp, shortRegion, pct } from '@/lib/format'
import { accountStatusText, relativeTime } from '@/lib/adapt'
import { actionLabel, auditTarget, isRoutineAction } from '@/lib/audit'
import { insights, hunt as huntApi, type AuditEntryDTO, type HuntTaskDTO } from '@/api'
import { now } from '@/lib/clock'
import QuotaMeter from '@/components/QuotaMeter.vue'
import SectionCard from '@/components/SectionCard.vue'
import LoadingBar from '@/components/LoadingBar.vue'

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

/**
 * 首次加载、还没拿到任何账号时显示骨架。不这样的话，进来先看到四个 0 和
 * 一张空矩阵，像是「没有账号」，一两秒后又突然变成真实数字。
 */
const booting = computed(() => state.loading && state.accounts.length === 0)

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
    // 多取一些：例行复查会被过滤掉，取 20 条可能剩不够 8 条。
    auditEntries.value = (await insights.audit({ limit: 50 })).entries
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
  if (failed) parts.push(t.overviewHunt.ov.authFailed(failed))
  if (disabled) parts.push(t.overviewHunt.ov.disabled(disabled))
  return parts.length ? parts.join(' · ') : t.overviewHunt.ov.allOk
})

const syncSummary = computed(() => {
  if (state.syncing) return t.overviewHunt.ov.syncing
  if (!state.lastSync) return t.overviewHunt.ov.neverSyncedList
  return t.overviewHunt.ov.lastSync(relativeTime(state.lastSync))
})

/** 页头的绝对时间。副行给的是相对时间，这里给准确值，便于对日志。 */
const lastSyncAbsolute = computed(() => {
  if (!state.lastSync) return t.overviewHunt.ov.neverSynced
  const d = new Date(state.lastSync)
  if (Number.isNaN(d.getTime())) return t.overviewHunt.ov.neverSynced
  return t.overviewHunt.ov.syncedAt(fmtDateTime(d))
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
  { key: 'accounts', label: t.overviewHunt.ov.kpiAccounts, value: String(live.value[0]), sub: accountSummary.value, tone: 'var(--text-primary)' },
  { key: 'instances', label: t.overviewHunt.ov.kpiInstances, value: String(live.value[1]), sub: t.overviewHunt.ov.kpiRegions(shownRegions.value.length), tone: 'var(--text-primary)' },
  { key: 'running', label: t.overviewHunt.ov.kpiRunning, value: String(live.value[2]), sub: t.overviewHunt.ov.kpiStopped(totals.value[1] - totals.value[2]), tone: 'var(--success)' },
  // 这张卡原来叫"同步状态"，值却是 SSE 连接的通断——两件事混在一格里，
  // 谁都看不懂它在说什么。现在标题说的就是那个值本身，上次同步降到副行。
  { key: 'live', label: t.overviewHunt.ov.kpiLive, value: state.live ? t.overviewHunt.ov.connected : t.overviewHunt.ov.disconnected, sub: syncSummary.value,
    tone: state.live ? 'var(--success)' : 'var(--warning)' }
])

interface Alert {
  code: string; severity: 'critical' | 'error' | 'warning' | 'info'
  text: string; detail: string; accountId: string; go: () => void
}

const alerts = computed<Alert[]>(() => {
  const out: Alert[] = []
  shownAccounts.value.filter(a => a.status === 'error').forEach(a => out.push({
    code: '401', severity: 'critical', accountId: a.id,
    text: t.overviewHunt.ov.alertCred(a.alias),
    detail: `${a.code} · ${accountStatusText(a)}`,
    go: () => openDrawer({ kind: 'account', id: a.id, tab: 'overview' })
  }))
  // anomaly 只表示「实例上记着一条最近操作失败的原因」，与它此刻是什么状态无关。
  // 以前这里一律写「意外进入 STOPPED」，一台软重启超时、其实还在跑的机器也会被说成停了。
  shownInstances.value.filter(i => i.anomaly).forEach(i => out.push({
    code: 'FAIL', severity: 'error', accountId: i.accountId,
    text: t.overviewHunt.ov.alertFail(i.name, i.lastError ?? ''), detail: i.region,
    go: () => openDrawer({ kind: 'instance', id: i.id, tab: 'history' })
  }))
  shownAccounts.value.filter(a => a.quota.memUsed / a.quota.memLimit >= 0.9).forEach(a => out.push({
    code: 'QUOTA', severity: 'warning', accountId: a.id,
    text: t.overviewHunt.ov.alertQuota(a.alias, a.quota.memUsed, a.quota.memLimit, pct(a.quota.memUsed, a.quota.memLimit)),
    detail: `${a.code} · ARM`,
    go: () => openDrawer({ kind: 'account', id: a.id, tab: 'quota' })
  }))
  shownInstances.value.filter(i => i.bootGb / i.bootLimitGb >= 0.9).forEach(i => out.push({
    code: 'DISK', severity: 'warning', accountId: i.accountId,
    text: t.overviewHunt.ov.alertDisk(i.name, i.bootGb, i.bootLimitGb),
    detail: `VPU ${i.vpu}`,
    go: () => router.push('/storage')
  }))
  return out
})

const severityTone = (s: Alert['severity']) =>
  s === 'critical' ? 'var(--danger)' : s === 'info' ? 'var(--info)' : 'var(--warning)'
const severityBg = (s: Alert['severity']) =>
  s === 'critical' ? 'var(--danger-soft)' : s === 'info' ? 'var(--info-soft)' : 'var(--warning-soft)'

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

function huntCountdown(task: HuntTaskDTO): string {
  if (task.state !== 'running') return t.overviewHunt.ov.paused
  const ms = new Date(task.nextAt).getTime() - now.value
  if (Number.isNaN(ms)) return '—'
  if (ms <= 0) return t.overviewHunt.ov.soon
  const sec = Math.round(ms / 1000)
  if (sec < 60) return t.overviewHunt.ov.inSec(sec)
  const m = Math.floor(sec / 60)
  return m < 60 ? t.overviewHunt.ov.inMin(m) : t.overviewHunt.ov.inHour(Math.floor(m / 60))
}

const timeline = computed(() =>
  // 例行的自动复查不进这里（审计日志里照常有），否则 8 条里大半是它。
  auditEntries.value.filter(e => !isRoutineAction(e.action)).slice(0, 8).map(e => {
    const a = e.accountId ? state.accounts.find(x => x.id === e.accountId) : undefined
    const label = actionLabel(e.action)
    return {
      code: a?.code ?? '—',
      // 账号可能已被删除，颜色要有兜底，不能用 ! 强断言。
      color: a ? acctColor(a.colorIndex) : 'var(--border-default)',
      text: e.target ? `${label} ${auditTarget(e.target)}` : label,
      ok: e.result === 'ok',
      at: relativeTime(e.createdAt)
    }
  })
)
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">{{ t.overviewHunt.ov.title }}</h1>
        <p class="page-sub">{{ t.overviewHunt.ov.subtitle(shownAccounts.length, shownRegions.length) }}</p>
      </div>
      <span class="dim-3 mono t-xs">{{ lastSyncAbsolute }}</span>
    </header>
    <LoadingBar :active="state.loading && !booting" />

    <template v-if="booting">
      <div class="kpis" aria-busy="true">
        <div v-for="n in 4" :key="n" class="card card-pad sk-kpi">
          <span class="skeleton" style="width: 64px; height: 12px" />
          <span class="skeleton" style="width: 72px; height: 36px; margin: 10px 0 8px" />
          <span class="skeleton" style="width: 96px; height: 10px" />
        </div>
      </div>
      <div class="grid-6-4">
        <div class="col-left">
          <div v-for="n in 2" :key="n" class="card sk-card">
            <div class="sk-card__head"><span class="skeleton" style="width: 96px; height: 14px" /></div>
            <div class="sk-card__body">
              <span v-for="m in 4" :key="m" class="skeleton" :style="{ width: 60 + ((m * 17 + n * 11) % 35) + '%' }" />
            </div>
          </div>
        </div>
        <div class="card sk-card">
          <div class="sk-card__head"><span class="skeleton" style="width: 80px; height: 14px" /></div>
          <div class="sk-card__body">
            <span v-for="m in 6" :key="m" class="skeleton" :style="{ width: 50 + ((m * 23) % 45) + '%' }" />
          </div>
        </div>
      </div>
    </template>

    <template v-else>
    <div class="kpis">
      <div v-for="(k, n) in kpis" :key="k.key" class="card card-pad reveal" :style="{ '--i': n }">
        <p class="kpi__label">{{ k.label }}</p>
        <p class="kpi__value mono" :style="{ color: k.tone }">{{ k.value }}</p>
        <p class="kpi__sub">{{ k.sub }}</p>
      </div>
    </div>

    <!-- 无异常时整块隐藏 -->
    <section v-if="alerts.length" class="card alerts reveal" style="--i: 2">
      <header class="alerts__head">
        <span class="alerts__dot" />
        <h2 class="t-lg alerts__title">{{ t.overviewHunt.ov.attention }}</h2>
        <span class="mono t-2xs dim-3">{{ t.overviewHunt.ov.items(alerts.length) }}</span>
      </header>
      <button v-for="(a, n) in alerts" :key="n" class="alerts__row" @click="a.go()">
        <span class="acct-bar" :style="{ background: acctColor(accountById(a.accountId).colorIndex) }" />
        <span class="alerts__code mono" :style="{ color: severityTone(a.severity) }">{{ a.code }}</span>
        <span class="alerts__sev" :style="{ color: severityTone(a.severity), background: severityBg(a.severity) }">
          {{ t.overviewHunt.ov.severity[a.severity] }}
        </span>
        <span class="alerts__text">{{ a.text }}</span>
        <span class="alerts__detail mono">{{ a.detail }}</span>
        <span class="dim-3">›</span>
      </button>
    </section>

    <div class="grid-6-4 reveal" style="--i: 3">
      <div class="col-left">
      <SectionCard :title="t.overviewHunt.ov.matrixTitle" :note="t.overviewHunt.ov.matrixNote">
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
      <SectionCard v-if="activeHunts.length" :title="t.overviewHunt.ov.huntTitle"
                   :note="t.overviewHunt.ov.huntNote(activeHunts.length)" class="stack">
        <button v-for="tk in activeHunts" :key="tk.id" class="hunt" @click="router.push('/hunt')">
          <span class="acct-bar" :style="{ background: acctColor(accountById(tk.accountId).colorIndex) }" />
          <span class="hunt__code mono"
                :style="{ color: acctColor(accountById(tk.accountId).colorIndex) }">
            {{ accountById(tk.accountId).code }}
          </span>
          <span class="hunt__name">{{ tk.name }}</span>
          <span class="hunt__meta mono">{{ t.overviewHunt.ov.attempts(tk.attempts) }}</span>
          <span class="hunt__at mono">{{ huntCountdown(tk) }}</span>
        </button>
      </SectionCard>

      <!-- 时间线放在左列而不是整页通栏：右边的配额卡很高，左列到这里为止
           会空出一大块。把它搬上来，两列高度接近，页面也短了一屏。 -->
      <SectionCard :title="t.overviewHunt.ov.recent" class="stack">
        <div v-for="(tk, n) in timeline" :key="n" class="tl__row">
          <span class="acct-bar" :style="{ background: tk.color }" />
          <span class="tl__code mono" :style="{ color: tk.color }">{{ tk.code }}</span>
          <span class="tl__text" :title="tk.text">{{ tk.text }}</span>
          <span class="tl__result" :style="{ color: tk.ok ? 'var(--success)' : 'var(--danger)' }">{{ tk.ok ? t.overviewHunt.ov.ok : t.overviewHunt.ov.failed }}</span>
          <span class="tl__at mono">{{ tk.at }}</span>
        </div>
      </SectionCard>
      </div>

      <SectionCard :title="t.overviewHunt.ov.quotaTitle"
                   :note="hiddenQuotaAccounts > 0
                     ? t.overviewHunt.ov.quotaHidden(hiddenQuotaAccounts)
                     : t.overviewHunt.ov.quotaLegend">
        <div class="quotas">
          <div v-for="a in topQuotaAccounts" :key="a.id" class="quotas__group">
            <p class="quotas__head">
              <span class="matrix__dot" :style="{ background: acctColor(a.colorIndex) }" />
              <span class="mono" :style="{ color: acctColor(a.colorIndex) }">{{ a.code }}</span>
              <span class="dim t-xs">{{ a.alias }}</span>
            </p>
            <QuotaMeter label="ARM OCPU" :used="a.quota.ocpuUsed" :limit="a.quota.ocpuLimit"
                        :unlimited="a.quota.unlimited.ocpu" />
            <QuotaMeter :label="t.overviewHunt.ov.memory" :used="a.quota.memUsed" :limit="a.quota.memLimit" unit=" GB"
                        :unlimited="a.quota.unlimited.mem" />
            <QuotaMeter :label="t.overviewHunt.ov.blockFree" :used="a.quota.blockUsed" :limit="a.quota.blockLimit" unit=" GB"
                        :unlimited="a.quota.unlimited.block" />
          </div>
        </div>
      </SectionCard>
    </div>
    </template>

  </div>
</template>

<style scoped>
.kpis { display: grid; grid-template-columns: repeat(4, 1fr); gap: 16px; }
.kpi__label { margin: 0; font-size: 12px; color: var(--text-secondary); }
.kpi__value { margin: 4px 0 0; font-size: 40px; line-height: 48px; font-weight: 600; }
.kpi__sub { margin: 0; font-size: 12px; color: var(--text-tertiary); }

/* 骨架：与 KPI 卡、左右两列卡片同形，数据到了原地替换 */
.sk-kpi { display: flex; flex-direction: column; }
.sk-card { overflow: hidden; }
.sk-card__head { height: 46px; display: flex; align-items: center; padding: 0 16px; border-bottom: 1px solid var(--border-subtle); }
.sk-card__body { padding: 14px 16px; display: flex; flex-direction: column; gap: 12px; }
.sk-card__body .skeleton { height: 12px; }

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

/* 左列的卡片装进一个纵向容器，网格只有左右两格，配额卡固定在右列。
   卡片是条件渲染的（守候卡没任务就不存在），放进容器后不会影响配额卡的位置。
   以前让配额卡 grid-row: 1 / span 99 跨行：隐式生成的 99 行之间各算一次
   gap，页面底部凭空多出约 1500px 空白。 */
.grid-6-4 {
  margin-top: 24px;
  display: grid;
  grid-template-columns: 6fr 4fr;
  gap: 16px;
  align-items: start;
}
.col-left { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
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
.tl__text { flex: 1 1 auto; min-width: 0; font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 结果列不许被挤窄：文字太长时截断的是左边的说明，不是「成功」两个字 */
.tl__result { flex: 0 0 auto; font-size: 11px; font-weight: 600; white-space: nowrap; }
.tl__at { width: 132px; text-align: right; font-size: 11px; color: var(--text-tertiary); }

@media (max-width: 1279px) {
  .kpis { grid-template-columns: repeat(2, 1fr); }
  .grid-6-4 { grid-template-columns: 1fr; }
}
</style>
