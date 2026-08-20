<script setup lang="ts">
/**
 * 账单。
 *
 * 数据来自 Oracle 的 Usage API——只读接口，查询本身不产生任何费用。
 *
 * 这一页要处理的核心现实：本工具的用户绝大多数是免费额度账号，金额恒为零。
 * 一屏 0.00 和「功能坏了」长得一模一样，所以金额为零时页面转而展示**用量**
 * （用了多少 OCPU 小时、多少 GB 月），那才是免费号用户真正关心的数字。
 *
 * 第二件事：缺 read usage-report 权限是常态而不是故障——很多人为本工具
 * 单独建的 IAM 用户只授了 compute / network / volume。那种情况下给的是
 * 一段可以直接照抄的策略，不是一句「查询失败」。
 */
import { computed, onMounted, ref, watch } from 'vue'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import { relativeTime } from '@/lib/adapt'
import {
  billing as billingApi, errorText,
  type BillingSummaryDTO, type BillingDetailDTO,
  type BillingCurrencyTotalDTO, type BillingStatusDTO, type BillingBucketDTO
} from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import CheckList from '@/components/CheckList.vue'
import CodeBlock from '@/components/CodeBlock.vue'
import AccountChip from '@/components/AccountChip.vue'

const { accountById } = useStore()

/* ---------- 概况 ---------- */

const summaries = ref<BillingSummaryDTO[]>([])
const totals = ref<BillingCurrencyTotalDTO[]>([])
const noPermissionCount = ref(0)
const notice = ref('')
const loading = ref(false)
const loadError = ref('')

async function load(refresh = false) {
  loading.value = true
  loadError.value = ''
  try {
    const res = await billingApi.list(refresh)
    summaries.value = res.summaries
    totals.value = res.totals
    noPermissionCount.value = res.noPermissionCount
    notice.value = res.notice
  } catch (err) {
    loadError.value = errorText(err)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
})

/**
 * 不设自动刷新定时器。
 *
 * Usage API 的数据有几小时到一天的延迟，每 30 秒刷一次只会得到一模一样的
 * 数字，白白多打 Oracle 的接口。后端也按 30 分钟缓存，这里给手动刷新就够。
 */

/* ---------- 排序与展示 ---------- */

/** 有花费的排前面，其次是免费的，缺权限与出错的沉底。 */
const STATUS_RANK: Record<BillingStatusDTO, number> = {
  ok: 0, free: 1, no_permission: 2, error: 3, disabled: 4
}

const rows = computed(() =>
  [...summaries.value].sort((a, b) => {
    const ra = STATUS_RANK[a.status] ?? 9
    const rb = STATUS_RANK[b.status] ?? 9
    if (ra !== rb) return ra - rb
    if (a.thisMonth !== b.thisMonth) return b.thisMonth - a.thisMonth
    return accountById(a.accountId).code.localeCompare(accountById(b.accountId).code)
  })
)

const STATUS_TEXT: Record<BillingStatusDTO, string> = {
  ok: '',
  free: '免费额度内',
  no_permission: '缺查询权限',
  disabled: '账号已停用',
  error: '查询失败'
}

const STATUS_TONE: Record<BillingStatusDTO, string> = {
  ok: 'var(--text-primary)',
  free: 'var(--success)',
  no_permission: 'var(--warning)',
  disabled: 'var(--text-tertiary)',
  error: 'var(--danger)'
}

/**
 * 金额格式化。
 *
 * 云账单的小额条目经常落在 0.001 这个量级，两位小数会把它们全部显示成
 * 0.00——看起来和「真的免费」没有区别。所以非零的极小额改用四位小数。
 */
function money(amount: number, currency: string): string {
  if (!amount) return '0.00'
  const digits = Math.abs(amount) < 0.01 ? 4 : 2
  const n = amount.toLocaleString('zh-CN', {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits
  })
  return currency && currency !== '—' ? `${n} ${currency}` : n
}

/** 用量数字：整数就不带小数，小数最多两位。 */
function qty(v: number): string {
  return v.toLocaleString('zh-CN', { maximumFractionDigits: 2 })
}

/** 环比。上月为零时没有意义，返回空串而不是 +∞ 或 +100%。 */
function deltaText(s: BillingSummaryDTO | BillingCurrencyTotalDTO): string {
  if (!s.lastMonth || !s.thisMonth) return ''
  const d = (s.thisMonth - s.lastMonth) / s.lastMonth
  if (Math.abs(d) < 0.005) return '与上月持平'
  return `${d > 0 ? '↑' : '↓'} ${Math.abs(d * 100).toFixed(0)}%`
}

function deltaTone(s: BillingSummaryDTO | BillingCurrencyTotalDTO): string {
  if (!s.lastMonth || !s.thisMonth) return 'var(--text-tertiary)'
  const d = s.thisMonth - s.lastMonth
  // 账单涨了是需要注意的事，跌了不是。这里不用红绿表示好坏，
  // 只用 warning 标出「变多了」——省钱不需要被表扬。
  return d > 0 ? 'var(--warning)' : 'var(--text-tertiary)'
}

/* ---------- 明细 ---------- */

const selected = ref('')
const days = ref(30)
const detail = ref<BillingDetailDTO | null>(null)
const detailLoading = ref(false)
const detailError = ref('')

const DAY_OPTIONS = [7, 30, 90]

async function loadDetail(refresh = false) {
  if (!selected.value) return
  detailLoading.value = true
  detailError.value = ''
  try {
    detail.value = await billingApi.detail(selected.value, days.value, refresh)
  } catch (err) {
    detailError.value = errorText(err)
    detail.value = null
  } finally {
    detailLoading.value = false
  }
}

function select(accountId: string) {
  // 再点一次收起。展开态没有独立入口，这是唯一的关闭方式。
  selected.value = selected.value === accountId ? '' : accountId
  detail.value = null
  if (selected.value) void loadDetail()
}

watch(days, () => {
  if (selected.value) void loadDetail()
})

const selectedAccount = computed(() =>
  selected.value ? accountById(selected.value) : null)

/* ---------- 日趋势柱状图 ---------- */

const series = computed(() => detail.value?.series ?? [])

const peak = computed(() =>
  series.value.reduce((m, d) => Math.max(m, d.amount), 0))

/**
 * 柱高按峰值归一化，留 8% 上边距。
 *
 * 有金额但极小的那些天要保底给 1.5%——按比例算出来不足一个像素的柱子
 * 会渲染成空白，让「这天花了 0.003」和「这天没花钱」看起来一样。
 */
function barHeight(amount: number): number {
  if (!amount || peak.value <= 0) return 0
  return Math.max(1.5, (amount / peak.value) * 92)
}

/** 只在首尾和月初打标签，否则 90 根柱子的日期会糊成一片。 */
function axisLabel(index: number): string {
  const list = series.value
  if (!list.length) return ''
  if (index === 0 || index === list.length - 1) return list[index].date.slice(5)
  if (list[index].date.endsWith('-01')) return list[index].date.slice(5)
  return ''
}

const hasCost = computed(() => (detail.value?.total ?? 0) > 0)

/** 免费号看用量，付费号看金额。两者的主表格不是同一张。 */
const usageRows = computed<BillingBucketDTO[]>(() => detail.value?.usage ?? [])
const serviceRows = computed<BillingBucketDTO[]>(() => detail.value?.services ?? [])
const regionRows = computed<BillingBucketDTO[]>(() => detail.value?.regions ?? [])

/** 构成条的宽度百分比，按该维度里的最大值归一化。 */
function share(rows: BillingBucketDTO[], v: number): number {
  const max = rows.reduce((m, r) => Math.max(m, r.amount), 0)
  return max > 0 ? Math.max(2, (v / max) * 100) : 0
}

function shareQty(rows: BillingBucketDTO[], v: number): number {
  const max = rows.reduce((m, r) => Math.max(m, r.quantity), 0)
  return max > 0 ? Math.max(2, (v / max) * 100) : 0
}

/* ---------- 权限 ---------- */

const POLICY = `Allow group OCI Core to read usage-report in tenancy`

const anyNoPermission = computed(() => noPermissionCount.value > 0)

/**
 * 标题栏只报有金额的币种。
 *
 * 免费账号那一桶币种为空、金额恒为零，跟在真实数字后面变成
 * 「本月至今 4,588.00 JPY · 0.00 · 共 5 个账号」——那个 0.00 不带信息，
 * 只是把真正的数字挤到一边。KPI 卡片里仍然保留它，那里「计入 N 个账号」
 * 说得清它代表什么。
 */
const billedTotals = computed(() =>
  totals.value.filter(t => t.thisMonth > 0 || t.lastMonth > 0))

/** 币种占位符 '—' 不参与拼接，否则标签会拖一个没有下文的分隔点。 */
const currencyLabel = (c: string) => (c && c !== '—' ? ' · ' + c : '')
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">账单</h1>
        <p class="page-sub">
          <template v-if="billedTotals.length">
            本月至今
            <template v-for="(t, i) in billedTotals" :key="t.currency">
              <span v-if="i > 0"> · </span>
              <span class="mono">{{ money(t.thisMonth, t.currency) }}</span>
            </template>
            · 共 {{ summaries.length }} 个账号
          </template>
          <template v-else-if="summaries.length">
            {{ summaries.length }} 个账号 · 本月暂无费用产生
          </template>
          <template v-else>Oracle 用量与成本 · 只读查询，不产生费用</template>
        </p>
      </div>
      <div class="head-actions">
        <button class="btn" :disabled="loading" @click="load(true)">
          {{ loading ? '刷新中…' : '刷新' }}
        </button>
      </div>
    </header>

    <p v-if="loadError" class="load-err">{{ loadError }}</p>

    <CheckList class="warn-box" :items="[
      { tone: 'info', text: notice || '用量数据由 Oracle 每隔几小时结算一次，最新一天通常不完整。这里显示的不是实时消费。' },
      { tone: 'info', text: '查询走 Usage API，是只读接口，不创建资源、不消耗配额、本身不产生费用。' },
      { tone: 'info', text: '发票与付款记录不在这里——那部分只在 Oracle 官网的账户中心，没有可用的接口。' }
    ]" />

    <!-- 缺权限是常态而不是故障，所以给的是可照抄的策略而不是报错 -->
    <SectionCard v-if="anyNoPermission" title="有账号缺少查询权限"
                 :note="`${noPermissionCount} 个账号读不到用量数据`">
      <div class="perm">
        <p class="t-xs perm__body">
          账单查询需要一项本工具其余功能都用不到的权限。如果你为本工具单独建了
          IAM 用户，只授了计算 / 网络 / 存储三项，就会是这个状态——账号本身是好的。
          在 Oracle 控制台 → 身份与安全 → 策略里补上这条：
        </p>
        <CodeBlock copyable :code="POLICY" />
        <p class="t-2xs dim-3 perm__foot">
          已经有 <span class="mono">read all-resources in tenancy</span> 的账号无需再加，那条已经覆盖。
          补完权限后点右上角刷新。
        </p>
      </div>
    </SectionCard>

    <!-- 按币种分开的合计。绝不把不同币种加成一个数 -->
    <div v-if="totals.length" class="kpis">
      <div v-for="t in totals" :key="t.currency" class="kpi card">
        <span class="t-2xs dim-3">本月至今{{ currencyLabel(t.currency) }}</span>
        <span class="kpi__v mono">{{ money(t.thisMonth, '') }}</span>
        <span class="kpi__meta t-2xs">
          <span class="dim-3">上月 {{ money(t.lastMonth, '') }}</span>
          <span v-if="deltaText(t)" :style="{ color: deltaTone(t) }">{{ deltaText(t) }}</span>
        </span>
        <span class="t-2xs dim-3">计入 {{ t.accounts }} 个账号</span>
      </div>
    </div>

    <SectionCard title="按账号" note="点一行看它的明细">
      <SkeletonRows v-if="loading && summaries.length === 0" :rows="4" />

      <EmptyState v-else-if="summaries.length === 0" icon="¤" title="还没有账号"
                  sub="先在账号页接入 Oracle 账号，这里才有账单可看。" />

      <template v-else>
        <div v-for="s in rows" :key="s.accountId" class="acct"
             :class="{ 'is-open': selected === s.accountId }"
             role="button" tabindex="0"
             @click="select(s.accountId)"
             @keydown.enter="select(s.accountId)"
             @keydown.space.prevent="select(s.accountId)">
          <span class="acct__bar"
                :style="{ background: acctColor(accountById(s.accountId).colorIndex) }" />

          <AccountChip :account="accountById(s.accountId)" variant="full" />

          <span class="acct__spacer" />

          <span v-if="s.status === 'ok'" class="mono acct__amt">
            {{ money(s.thisMonth, s.currency) }}
          </span>
          <span v-else class="t-xs acct__state" :style="{ color: STATUS_TONE[s.status] }">
            {{ STATUS_TEXT[s.status] }}
          </span>

          <span v-if="s.status === 'ok' && deltaText(s)" class="t-2xs acct__delta"
                :style="{ color: deltaTone(s) }">{{ deltaText(s) }}</span>

          <span v-if="s.status === 'ok'" class="mono t-2xs dim-3 acct__last">
            上月 {{ money(s.lastMonth, '') }}
          </span>

          <span v-if="s.error" class="mono t-2xs acct__err" :title="s.error">{{ s.error }}</span>

          <span class="mono t-2xs dim-3 acct__region">{{ s.region }}</span>
          <span class="acct__caret">{{ selected === s.accountId ? '▾' : '▸' }}</span>
        </div>
      </template>
    </SectionCard>

    <!-- 明细 -->
    <SectionCard v-if="selected" :title="`明细 · ${selectedAccount?.alias ?? ''}`"
                 :note="detail ? `${detail.start.slice(0, 10)} 至今 · ${detail.region}` : ''">
      <template #action>
        <div class="range">
          <button v-for="d in DAY_OPTIONS" :key="d" class="btn btn--xs"
                  :class="{ 'btn--primary': days === d }" @click="days = d">
            {{ d }} 天
          </button>
          <button class="btn btn--xs" :disabled="detailLoading" @click="loadDetail(true)">刷新</button>
        </div>
      </template>

      <SkeletonRows v-if="detailLoading && !detail" :rows="3" />

      <p v-else-if="detailError" class="detail-err t-xs">{{ detailError }}</p>

      <template v-else-if="detail">
        <!-- 缺权限：明细页也要给同一段策略，不能让人回上面找 -->
        <div v-if="detail.status === 'no_permission'" class="perm">
          <p class="t-xs perm__body">
            这个账号读不到用量数据。补上下面这条策略后刷新：
          </p>
          <CodeBlock copyable :code="POLICY" />
        </div>

        <div v-else-if="detail.status === 'error'" class="perm">
          <p class="t-xs perm__body" style="color: var(--danger)">{{ detail.error }}</p>
        </div>

        <template v-else>
          <!-- 有费用才画金额趋势图。全零的柱状图是一条贴地直线，
               不如直接说清楚「这段时间没有产生费用」 -->
          <div v-if="hasCost" class="chart">
            <div class="chart__head">
              <span class="t-2xs dim-3">每日费用 · {{ detail.currency }}</span>
              <span class="mono t-xs chart__total">
                合计 {{ money(detail.total, detail.currency) }}
              </span>
            </div>
            <div class="bars" role="img"
                 :aria-label="`${detail.days} 天每日费用趋势，峰值 ${money(peak, detail.currency)}`">
              <span v-for="(d, i) in series" :key="d.date" class="bar"
                    :style="{ height: barHeight(d.amount) + '%' }"
                    :title="`${d.date} · ${money(d.amount, detail.currency)}`"
                    :data-label="axisLabel(i)" />
            </div>
            <div class="axis mono t-2xs dim-3">
              <span>{{ series[0]?.date.slice(5) }}</span>
              <span>{{ series[series.length - 1]?.date.slice(5) }}</span>
            </div>
          </div>

          <div v-else class="free">
            <p class="free__title">这 {{ detail.days }} 天没有产生费用</p>
            <p class="free__sub t-xs">
              资源都在免费额度内。下面是实际用量——免费额度也是有上限的，
              这些数字才是该盯的。
            </p>
          </div>

          <!-- 有费用时按服务/区域拆金额；没费用时拆用量 -->
          <div class="splits">
            <div v-if="hasCost && serviceRows.length" class="split">
              <p class="split__head t-2xs dim-3">按服务</p>
              <div v-for="b in serviceRows" :key="b.key" class="brow">
                <span class="t-xs brow__k">{{ b.key }}</span>
                <span class="brow__track">
                  <span class="brow__fill" :style="{ width: share(serviceRows, b.amount) + '%' }" />
                </span>
                <span class="mono t-2xs brow__v">{{ money(b.amount, '') }}</span>
              </div>
            </div>

            <div v-if="hasCost && regionRows.length" class="split">
              <p class="split__head t-2xs dim-3">按区域</p>
              <div v-for="b in regionRows" :key="b.key" class="brow">
                <span class="mono t-xs brow__k">{{ b.key }}</span>
                <span class="brow__track">
                  <span class="brow__fill" :style="{ width: share(regionRows, b.amount) + '%' }" />
                </span>
                <span class="mono t-2xs brow__v">{{ money(b.amount, '') }}</span>
              </div>
            </div>

            <div v-if="usageRows.length" class="split" :class="{ 'split--wide': !hasCost }">
              <p class="split__head t-2xs dim-3">用量 · 按服务</p>
              <div v-for="b in usageRows" :key="b.key" class="brow">
                <span class="t-xs brow__k">{{ b.key }}</span>
                <span class="brow__track">
                  <span class="brow__fill brow__fill--usage"
                        :style="{ width: shareQty(usageRows, b.quantity) + '%' }" />
                </span>
                <span class="mono t-2xs brow__v">
                  {{ qty(b.quantity) }}<span class="brow__unit dim-3">{{ b.unit }}</span>
                </span>
              </div>
            </div>

            <p v-if="!hasCost && !usageRows.length" class="t-xs dim-3 splits__none">
              这段时间没有任何用量记录。新接入的账号可能要等几小时才有数据。
            </p>
          </div>

          <p class="t-2xs dim-3 detail__foot">
            {{ relativeTime(detail.fetchedAt) }}查询 · 后端缓存 30 分钟，点刷新可强制重查
          </p>
        </template>
      </template>
    </SectionCard>
  </div>
</template>

<style scoped>
.warn-box { margin-bottom: 16px; }
.page > .card + .card { margin-top: 16px; }

/* ---- 权限提示 ---- */
.perm { padding: 4px 0 14px; }
.perm__body { margin: 14px 16px 0; line-height: 1.8; color: var(--text-secondary); }
.perm__foot { margin: 0 16px; line-height: 1.7; }

/* ---- KPI ---- */
.kpis { display: flex; gap: 12px; flex-wrap: wrap; margin-bottom: 16px; }
.kpi {
  flex: 1 1 200px; min-width: 200px; padding: 14px 16px;
  display: flex; flex-direction: column; gap: 4px;
}
.kpi__v { font-size: 22px; font-weight: 600; line-height: 1.3; }
.kpi__meta { display: flex; gap: 10px; align-items: baseline; }

/* ---- 账号行 ---- */
.acct {
  position: relative; display: flex; align-items: center; gap: 12px;
  height: 48px; padding: 0 16px 0 17px;
  border-bottom: 1px solid var(--border-subtle); cursor: pointer;
  transition: background var(--dur-fast);
}
.acct:last-child { border-bottom: none; }
.acct:hover { background: var(--bg-hover); }
.acct:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
.acct.is-open { background: var(--bg-hover); }
.acct__bar { position: absolute; left: 0; top: 0; bottom: 0; width: 3px; }
.acct__spacer { flex: 1 1 auto; min-width: 8px; }
.acct__amt { font-size: 14px; font-weight: 600; }
.acct__state { font-weight: 600; }
.acct__delta { width: 76px; text-align: right; flex: 0 0 auto; }
.acct__last { width: 96px; text-align: right; flex: 0 0 auto; }
.acct__region { width: 108px; text-align: right; flex: 0 0 auto; }
.acct__err {
  color: var(--danger); max-width: 220px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.acct__caret { color: var(--text-tertiary); font-size: 11px; width: 12px; }

/* ---- 明细 ---- */
.range { display: flex; gap: 6px; }

.detail-err { color: var(--danger); padding: 16px; line-height: 1.7; }

.chart { padding: 16px; }
.chart__head { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: 12px; }
.chart__total { font-weight: 600; }

/* 柱子用 flex 均分，柱数从 7 到 90 都不用改结构 */
.bars {
  display: flex; align-items: flex-end; gap: 2px;
  height: 120px; padding-bottom: 1px; border-bottom: 1px solid var(--border-subtle);
}
.bar {
  flex: 1 1 0; min-width: 2px; border-radius: 1px 1px 0 0;
  background: var(--accent); opacity: 0.75;
  transition: opacity var(--dur-fast);
}
.bar:hover { opacity: 1; }
.axis { display: flex; justify-content: space-between; margin-top: 8px; }

/* ---- 免费态 ---- */
.free { padding: 22px 16px 8px; }
.free__title { margin: 0 0 6px; font-size: 14px; font-weight: 600; color: var(--success); }
.free__sub { margin: 0; color: var(--text-secondary); line-height: 1.8; max-width: 62ch; }

/* ---- 构成 ---- */
.splits {
  display: flex; gap: 28px; flex-wrap: wrap;
  padding: 16px; border-top: 1px solid var(--border-subtle); margin-top: 8px;
}
.split { flex: 1 1 260px; min-width: 260px; }
.split--wide { flex: 1 1 100%; }
.split__head { margin: 0 0 10px; }
.splits__none { margin: 0; line-height: 1.7; }

.brow { display: flex; align-items: center; gap: 10px; height: 26px; }
.brow__k {
  width: 96px; flex: 0 0 auto;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.brow__track {
  flex: 1 1 auto; height: 6px; border-radius: var(--radius-full);
  background: var(--bg-inset); overflow: hidden;
}
.brow__fill { display: block; height: 100%; background: var(--accent); border-radius: var(--radius-full); }
.brow__fill--usage { background: var(--success); }
.brow__v { width: 128px; text-align: right; flex: 0 0 auto; }
/* 单位靠 margin 与数字分开：模板里的空格会被 Vue 的 whitespace: condense
   吃掉，渲染成「2880OCPU Hours」 */
.brow__unit { margin-left: 4px; }

.detail__foot { padding: 0 16px 14px; margin: 0; }

@media (max-width: 900px) {
  .acct__last, .acct__region { display: none; }
}
</style>
