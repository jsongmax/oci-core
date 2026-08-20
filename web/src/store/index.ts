import { computed, reactive, readonly, watch } from 'vue'
import type {
  Account, ConfirmRequest, DrawerRequest, Instance, LifecycleState, ToastPayload
} from '@/types'
import { LIFECYCLE, isTransitional } from '@/lib/lifecycle'
import { toAccount, toInstance, toQuota } from '@/lib/adapt'
import {
  accounts as accountsApi, auth as authApi, instances as instancesApi,
  insights, launch as launchApi, settings as settingsApi,
  ApiError, errorText, type InstanceAction, type LaunchInput
} from '@/api'
import { connectEvents, type ServerEvent } from '@/api/events'

export type Density = 'comfy' | 'compact'
export type StateFilter = 'all' | 'running' | 'stopped' | 'transition' | 'anomaly' | 'terminated'

interface Options {
  /** §6.5：可全局禁用终止实例。与后端 /api/settings 同步 */
  allowTerminate: boolean
  allowBulk: boolean
  /** 危险操作是否需要重新验证 TOTP */
  reauthForDanger: boolean
  confirmForceRestart: boolean
  reduceMotion: boolean
  syncIntervalMinutes: number
  checkIntervalHours: number
  /** 审计日志保留天数，0 为永久保留 */
  auditRetentionDays: number
}

const state = reactive({
  /** 未完成 /api/status 探测前为 null，路由据此决定落地页 */
  authed: null as boolean | null,
  setupRequired: false,
  totpRequired: false,
  username: '',
  version: '',

  loading: true,
  /** 首次加载失败的原因。非空时页面显示错误态而不是空列表 */
  loadError: '',
  syncing: false,
  lastSync: '',
  /** SSE 连接是否正常。断开时顶栏提示，用户才知道状态可能不是最新的 */
  live: false,

  theme: (localStorage.getItem('oci.theme') as 'dark' | 'light') ?? 'dark',
  accentName: localStorage.getItem('oci.accent') ?? 'cyan',
  sidebarCollapsed: false,
  density: (localStorage.getItem('oci.density') as Density) ?? 'comfy',
  // 分组偏好横跨实例、网络、存储三页，是"这个人习惯怎么看"，
  // 不该每次刷新都退回默认——和账号页的卡片/列表偏好同理。
  groupByAccount: localStorage.getItem('oci.groupByAccount') === '1',
  stateFilter: 'all' as StateFilter,
  /** 账号筛选器：选中的账号 id 集合，跨页面保持（§3.2） */
  accountFilter: new Set<string>(),
  /** 区域筛选器。为空表示不限——与账号筛选器"空=全选"的语义一致 */
  regionFilter: new Set<string>(),
  selection: new Set<string>(),

  accounts: [] as Account[],
  instances: [] as Instance[],

  drawer: null as DrawerRequest | null,
  confirm: null as ConfirmRequest | null,
  toast: null as ToastPayload | null,
  paletteOpen: false,

  options: {
    allowTerminate: true,
    allowBulk: true,
    reauthForDanger: false,
    confirmForceRestart: true,
    reduceMotion: localStorage.getItem('oci.motion') === 'reduced',
    syncIntervalMinutes: 5,
    checkIntervalHours: 6,
    auditRetentionDays: 0
  } as Options
})

/* ---------- 派生 ---------- */

/**
 * 按 id 取账号。缓存尚未加载完时返回一个占位对象——
 * 视图里大量 `accountById(x).code` 的直接取值，返回 undefined 会到处炸。
 */
const PLACEHOLDER_ACCOUNT: Account = {
  id: '', alias: '—', code: '···', colorIndex: 8, tenancyTail: '',
  regions: [], status: 'checking', lastCheckedAt: null, statusMessage: '', tier: 'unknown', trialEndsAt: null, openedAt: null, email: '', fingerprint: '',
  quota: {
    ocpuUsed: 0, ocpuLimit: 0, memUsed: 0, memLimit: 0,
    blockUsed: 0, blockLimit: 0, microUsed: 0, microLimit: 0,
    unlimited: { ocpu: false, mem: false, block: false, micro: false }
  },
  quotaRegion: '',
  createdAt: ''
}

const accountById = (id: string): Account =>
  state.accounts.find(a => a.id === id) ?? PLACEHOLDER_ACCOUNT

const visibleInstances = computed(() =>
  state.instances.filter(i =>
    // 筛选器为空表示"尚未初始化"或"不限"，此时不过滤而不是显示一片空白。
    (state.accountFilter.size === 0 || state.accountFilter.has(i.accountId)) &&
    (state.regionFilter.size === 0 || state.regionFilter.has(i.region))
  )
)

const matchesFilter = (i: Instance, f: StateFilter) => {
  switch (f) {
    case 'running': return i.state === 'RUNNING'
    case 'stopped': return i.state === 'STOPPED'
    case 'transition': return isTransitional(i.state)
    case 'anomaly': return !!i.anomaly
    case 'terminated': return i.state === 'TERMINATED'
    default: return i.state !== 'TERMINATED'
  }
}

const filteredInstances = computed(() =>
  visibleInstances.value.filter(i => matchesFilter(i, state.stateFilter))
)

const transitioning = computed(() => state.instances.filter(i => isTransitional(i.state)))

/**
 * 所有账号订阅过的区域并集，用于筛选器与下拉。
 *
 * 数据源是账号的订阅列表而非硬编码的全量区域表：只展示用户真正
 * 能用的区域，否则筛选器里会塞进三十多个永远筛不出东西的选项。
 */
const allRegions = computed(() => {
  const set = new Set<string>()
  for (const account of state.accounts) {
    for (const region of account.regions) set.add(region)
  }
  return [...set].sort()
})

const countByFilter = (f: StateFilter) =>
  visibleInstances.value.filter(i => matchesFilter(i, f)).length

/* ---------- 主题 ---------- */

watch(() => state.theme, t => {
  document.documentElement.dataset.theme = t
  localStorage.setItem('oci.theme', t)
}, { immediate: true })

watch(() => state.accentName, a => {
  document.documentElement.dataset.accent = a
  localStorage.setItem('oci.accent', a)
}, { immediate: true })

watch(() => state.density, d => localStorage.setItem('oci.density', d))
watch(() => state.groupByAccount, g => localStorage.setItem('oci.groupByAccount', g ? '1' : ''))

watch(() => state.options.reduceMotion, r => {
  document.documentElement.dataset.motion = r ? 'reduced' : ''
  localStorage.setItem('oci.motion', r ? 'reduced' : '')
}, { immediate: true })

/* ---------- UI 原语 ---------- */

/**
 * 弹一条 toast，ms 毫秒后自动收起。
 *
 * 不能用 `state.toast === payload` 判断"还是不是同一条"：state 是 reactive()，
 * 赋进去的对象会被深度包成 Proxy，读回来跟原始对象永不相等，定时器于是形同
 * 虚设——toast 会一直挂到用户手动点掉。改用单调递增的序号，并在弹新的之前
 * 清掉上一个定时器，避免旧定时器把新 toast 提前收走。
 */
let toastSeq = 0
let toastTimer = 0

function toast(payload: ToastPayload, ms = 6000) {
  window.clearTimeout(toastTimer)
  const seq = ++toastSeq
  state.toast = payload
  toastTimer = window.setTimeout(() => {
    if (seq === toastSeq) state.toast = null
  }, ms)
}

/** 手动关闭。同时作废待触发的定时器。 */
function dismissToast() {
  window.clearTimeout(toastTimer)
  toastSeq++
  state.toast = null
}

/** 把异常渲染成一条 danger toast，并附上后端给的处理建议。 */
function toastError(title: string, err: unknown) {
  const advice = err instanceof ApiError && err.advice ? err.advice : undefined
  toast({ tone: 'danger', title, body: advice ? `${errorText(err)}\n${advice}` : errorText(err) }, 9000)
}

function ask(request: ConfirmRequest) { state.confirm = request }
function openDrawer(request: DrawerRequest) { state.drawer = request }
function closeDrawer() { state.drawer = null }

/* ---------- 数据加载 ---------- */

async function loadStatus(): Promise<void> {
  try {
    const status = await authApi.status()
    state.setupRequired = status.setupRequired
    state.totpRequired = status.totpRequired
    state.authed = status.authenticated
    state.username = status.username ?? ''
    state.version = status.version
  } catch (err) {
    state.authed = false
    state.loadError = errorText(err)
  }
}

/**
 * 拉账号列表。配额不参与等待——它是装饰，不是主体。
 *
 * 这里原本 await 了配额。账号列表本身几毫秒就回来了，配额却要向
 * Oracle 发八个跨洋请求；一旦某个区域的限额接口不通，整个"保存账号"
 * 按钮就会一直转圈，用户看到的是"保存卡住了"，而实际上账号早就存好了。
 * 现在列表先渲染，配额自己追上来。
 */
async function loadAccounts(): Promise<void> {
  const { accounts: dtos } = await accountsApi.list()
  state.accounts = dtos.map(dto => toAccount(dto, undefined))

  // 首次加载时默认全选，之后保留用户的选择，但要剔除已删除的账号。
  const known = new Set(state.accounts.map(a => a.id))
  if (state.accountFilter.size === 0) {
    state.accounts.forEach(a => state.accountFilter.add(a.id))
  } else {
    for (const id of [...state.accountFilter]) {
      if (!known.has(id)) state.accountFilter.delete(id)
    }
  }

  void loadQuota()
}

/** 把配额并进已渲染的账号里。失败就保持"未知"，不打扰用户。 */
async function loadQuota(): Promise<void> {
  let quotas
  try {
    ;({ quotas } = await insights.quota())
  } catch {
    return
  }
  const byAccount = new Map(quotas.map(q => [q.accountId, q]))
  for (const acc of state.accounts) {
    const dto = byAccount.get(acc.id)
    if (dto) {
      acc.quota = toQuota(dto)
      acc.quotaRegion = dto.region
    }
  }
}

async function loadInstances(): Promise<void> {
  const { instances: dtos, sync } = await instancesApi.list()
  // 保留本地的瞬时 UI 态（busy / settledAt），否则一次刷新会把
  // 正在播放的按钮 spinner 和落定高光全部抹掉。
  const localUi = new Map(state.instances.map(i => [i.id, { busy: i.busy, settledAt: i.settledAt }]))
  state.instances = dtos.map(dto => {
    const view = toInstance(dto)
    const ui = localUi.get(view.id)
    if (ui) Object.assign(view, ui)
    return view
  })
  state.syncing = sync.syncing
  state.lastSync = sync.lastSync
}

async function loadSettings(): Promise<void> {
  try {
    const s = await settingsApi.get()
    state.options.allowTerminate = s.allowTerminate
    state.options.allowBulk = s.allowBulkActions
    state.options.reauthForDanger = s.requireTotpForDanger
    state.options.syncIntervalMinutes = s.syncIntervalMinutes
    state.options.checkIntervalHours = s.checkIntervalHours
    state.options.auditRetentionDays = s.auditRetentionDays
  } catch {
    // 设置读不到就用默认值，不该阻断整个应用。
  }
}

/** 加载全部数据。登录成功后与手动刷新时调用。 */
async function loadAll(): Promise<void> {
  state.loading = true
  state.loadError = ''
  try {
    await Promise.all([loadAccounts(), loadInstances(), loadSettings()])
  } catch (err) {
    state.loadError = errorText(err)
  } finally {
    state.loading = false
  }
}

/** 只刷新实例列表。SSE 重连或操作后调用。 */
async function refreshInstances(): Promise<void> {
  try {
    await loadInstances()
  } catch (err) {
    // 后台刷新失败不弹 toast：用户没有主动触发，打扰他没有意义。
    console.warn('刷新实例列表失败', err)
  }
}

/** 触发一次后端同步。这会真的去调 OCI，可能要几十秒。 */
async function syncNow(accountId?: string): Promise<void> {
  if (state.syncing) return
  state.syncing = true
  try {
    const report = await instancesApi.sync(accountId)
    await loadInstances()

    const failures = report.errors ?? []
    if (failures.length > 0) {
      toast({
        tone: 'warning',
        title: `同步完成，${failures.length} 个区域失败`,
        body: failures.map(e => `${e.accountAlias} · ${e.region}：${e.ociCode || e.message}`).join('\n')
      }, 9000)
    } else {
      toast({
        tone: 'success',
        title: '同步完成',
        // 已终止的单独说，不混进台数——界面各处都不显示它们，
        // 混在一起会出现「同步说 14 台、列表只有 13 台」这种自相矛盾。
        body: `${report.instances} 台实例 · ${report.regions} 个区域 · ${(report.durationMs / 1000).toFixed(1)}s`
          + (report.terminated ? `（另有 ${report.terminated} 台已终止）` : '')
      })
    }
    // 同步可能改变账号的连通性状态与配额。
    await loadAccounts().catch(() => undefined)
  } catch (err) {
    toastError('同步失败', err)
  } finally {
    state.syncing = false
  }
}

/* ---------- 实时事件 ---------- */

let disconnect: (() => void) | null = null

function applyEvent(event: ServerEvent): void {
  switch (event.type) {
    case 'sync.started':
      state.syncing = true
      break

    case 'sync.finished':
      state.syncing = false
      state.lastSync = event.at
      void refreshInstances()
      break

    case 'account.status':
      void loadAccounts().catch(() => undefined)
      if (event.message) {
        const acct = state.accounts.find(a => a.id === event.accountId)
        toast({
          tone: 'danger',
          title: `账号 ${acct?.alias ?? ''} 凭据校验失败`,
          body: event.message
        }, 9000)
      }
      break

    case 'instance.removed': {
      const idx = state.instances.findIndex(i => i.id === event.instanceId)
      if (idx >= 0) {
        const [gone] = state.instances.splice(idx, 1)
        toast({ tone: 'info', title: `${gone.name} 已终止` })
      }
      break
    }

    case 'instance.error': {
      const inst = state.instances.find(i => i.id === event.instanceId)
      if (inst) {
        inst.busy = false
        inst.anomaly = true
        inst.lastError = event.message
        toast({ tone: 'danger', title: `${inst.name} 操作失败`, body: event.message }, 9000)
      }
      break
    }

    case 'instance.updated': {
      const inst = state.instances.find(i => i.id === event.instanceId)
      if (!inst) {
        // 新建的实例第一次出现在事件里，拉一次全量把它接进来。
        void refreshInstances()
        return
      }
      const previous = inst.state
      const next = (event.state as LifecycleState) || inst.state
      inst.state = next
      inst.busy = false

      // 从过渡态落到稳定态才算"落定"，触发 instance-ready 高光。
      if (isTransitional(previous) && !isTransitional(next)) {
        inst.anomaly = false
        inst.lastError = undefined
        inst.settledAt = Date.now()
        window.setTimeout(() => { inst.settledAt = undefined }, 1200)
        announceSettled(inst, previous, next)
        // 落定时公网 IP 等字段可能变了，补一次全量。
        void refreshInstances()
      }
      break
    }
  }
}

function announceSettled(inst: Instance, from: LifecycleState, to: LifecycleState): void {
  if (from === 'PROVISIONING' && to === 'RUNNING') {
    toast({
      tone: 'success',
      title: `${inst.name} 已就绪`,
      body: inst.publicIp !== '—' ? `公网 IP ${inst.publicIp}` : undefined,
      command: inst.publicIp !== '—' ? `ssh ubuntu@${inst.publicIp}` : undefined
    })
    return
  }
  toast({
    tone: to === 'RUNNING' ? 'success' : 'info',
    title: `${inst.name} ${LIFECYCLE[to].label}`
  })
}

/** 建立事件流。登录后调用一次即可。 */
function startLiveUpdates(): void {
  if (disconnect) return
  disconnect = connectEvents({
    onEvent: applyEvent,
    onOpen: (reconnected) => {
      state.live = true
      // 后端不补发历史事件，重连后必须重新拉全量。
      if (reconnected) void refreshInstances()
    },
    onError: () => { state.live = false }
  })
}

function stopLiveUpdates(): void {
  disconnect?.()
  disconnect = null
  state.live = false
}

/* ---------- 生命周期操作 ---------- */

/**
 * §6.3 LifecycleTransition —— 诚实的过渡态。
 *
 * 后端返回的一定是过渡态而非终态，落定由 SSE 推送。
 * 这里只负责：立刻把按钮转成 loading、写入后端给的过渡态、失败时可见地回滚。
 */
async function runAction(id: string, action: InstanceAction, force = false): Promise<void> {
  const inst = state.instances.find(i => i.id === id)
  if (!inst) return

  const previous = inst.state
  inst.busy = true
  inst.anomaly = false
  inst.lastError = undefined

  try {
    const updated = await instancesApi.action(id, action, force)
    inst.state = updated.lifecycleState
  } catch (err) {
    // 失败必须可见地回滚，不能静默恢复。
    inst.state = previous
    inst.anomaly = true
    inst.lastError = errorText(err)
    toastError(`${inst.name} 操作失败`, err)
  } finally {
    // 后端受理后按钮 loading 结束，行内进度条接管（§6.3 t=0.4s）。
    inst.busy = false
  }
}

const start = (id: string) => runAction(id, 'START')
const stop = (id: string) => runAction(id, 'SOFTSTOP')
const restart = (id: string) => runAction(id, 'SOFTRESET')
const forceStop = (id: string) => runAction(id, 'STOP', true)
const forceRestart = (id: string) => runAction(id, 'RESET', true)

/** 终止实例。确认串由 ConfirmDialog 收集，这里按实例名回传给后端。 */
async function terminate(id: string, preserveBootVolume = false): Promise<void> {
  const inst = state.instances.find(i => i.id === id)
  if (!inst) return

  inst.busy = true
  try {
    await instancesApi.terminate(id, inst.name, preserveBootVolume)
    inst.state = 'TERMINATING'
  } catch (err) {
    inst.anomaly = true
    inst.lastError = errorText(err)
    toastError(`${inst.name} 终止失败`, err)
  } finally {
    inst.busy = false
  }
}

/** 批量操作。逐台串行提交，避免同租户并发过多触发 OCI 限流。 */
async function bulk(action: 'start' | 'stop' | 'restart'): Promise<void> {
  const ids = [...state.selection]
  state.selection.clear()

  const fn = action === 'start' ? start : action === 'stop' ? stop : restart
  for (const id of ids) {
    await fn(id)
  }
  toast({ tone: 'info', title: `已对 ${ids.length} 台实例提交${action === 'start' ? '开机' : action === 'stop' ? '关机' : '重启'}` })
}

async function launchInstance(input: LaunchInput): Promise<string | null> {
  try {
    const result = await launchApi.create(input)
    closeDrawer()

    // 立刻把新实例插到列表最前，用户马上能看到 PROVISIONING 那一行。
    state.instances.unshift(toInstance(result.instance))
    toast({
      tone: 'accent',
      title: `${result.instance.displayName} 正在创建`,
      body: result.notice ?? '通常需要 1–3 分钟，就绪后会通知你。'
    }, 8000)
    return result.instance.id
  } catch (err) {
    toastError('创建实例失败', err)
    return null
  }
}

/** 清除某行的错误提示。用户看过之后主动关掉。 */
async function dismissInstanceError(id: string): Promise<void> {
  const inst = state.instances.find(i => i.id === id)
  if (inst) {
    inst.anomaly = false
    inst.lastError = undefined
  }
  await instancesApi.dismissError(id).catch(() => undefined)
}

/* ---------- 筛选与选择 ---------- */

function toggleAccountFilter(id: string) {
  if (state.accountFilter.has(id)) state.accountFilter.delete(id)
  else state.accountFilter.add(id)
  // 一个都不选等于什么都看不到，没有意义——退回全选。
  if (state.accountFilter.size === 0) selectAllAccounts()
}

function selectAllAccounts() {
  state.accounts.forEach(a => state.accountFilter.add(a.id))
}

function toggleSelection(id: string) {
  if (state.selection.has(id)) state.selection.delete(id)
  else state.selection.add(id)
}

function toggleRegionFilter(region: string) {
  if (state.regionFilter.has(region)) state.regionFilter.delete(region)
  else state.regionFilter.add(region)
}

/** 清空区域筛选即"全部区域"。 */
function clearRegionFilter() {
  state.regionFilter.clear()
}

/** 下一个未被占用的身份色（§5.2：8 个之后循环复用，短代号保证唯一） */
function nextColorIndex(): number {
  const used = new Set(state.accounts.map(a => a.colorIndex))
  for (let n = 1; n <= 8; n++) if (!used.has(n as never)) return n
  return (state.accounts.length % 8) + 1
}

/* ---------- 设置 ---------- */

async function updateOptions(patch: {
  allowTerminate?: boolean
  allowBulkActions?: boolean
  requireTotpForDanger?: boolean
  syncIntervalMinutes?: number
  checkIntervalHours?: number
  auditRetentionDays?: number
}): Promise<void> {
  try {
    const { settings: s, notice } = await settingsApi.update(patch)
    state.options.allowTerminate = s.allowTerminate
    state.options.allowBulk = s.allowBulkActions
    state.options.reauthForDanger = s.requireTotpForDanger
    state.options.syncIntervalMinutes = s.syncIntervalMinutes
    state.options.checkIntervalHours = s.checkIntervalHours
    // 这一行漏掉过：接口写成功了、下次刷新也对，但当前页面上的下拉框
    // 还显示旧值——看起来就是"点了没反应"，而 toast 又说保存成功。
    state.options.auditRetentionDays = s.auditRetentionDays
    if (patch.syncIntervalMinutes !== undefined && notice) {
      toast({ tone: 'info', title: '设置已保存', body: notice })
    } else {
      toast({ tone: 'success', title: '设置已保存' })
    }
  } catch (err) {
    toastError('保存设置失败', err)
    await loadSettings()
  }
}

/* ---------- 会话 ---------- */

async function afterLogin(): Promise<void> {
  state.authed = true
  // 必须重新拉一次 /api/status：用户名只在那里返回。
  // 漏掉它的话 state.username 一直是空串，右上角头像菜单会显示"未登录"——
  // 人明明已经登进来了。
  await loadStatus()
  await loadAll()
  startLiveUpdates()
}

async function logout(): Promise<void> {
  stopLiveUpdates()
  await authApi.logout().catch(() => undefined)
  state.authed = false
  state.accounts = []
  state.instances = []
  state.accountFilter.clear()
  state.selection.clear()
  state.username = ''
  // 同样要重新同步状态。setupRequired 是首屏加载时取的：全新部署那次是
  // true，完成首次设置后没人把它改回 false，于是登出后登录页按 setupRequired
  // 渲染成了"首次设置"表单——刷新一下才恢复正常。
  await loadStatus()
}

/** 会话失效时清理本地状态，由 401 广播触发。 */
function onSessionLost(): void {
  stopLiveUpdates()
  state.authed = false
  state.username = ''
}

export function useStore() {
  return {
    state,
    options: state.options,
    accountById,
    visibleInstances,
    filteredInstances,
    transitioning,
    allRegions,
    countByFilter,
    nextColorIndex,

    toast, toastError, dismissToast, ask, openDrawer, closeDrawer,

    loadAll, loadAccounts, loadInstances, refreshInstances, loadStatus, syncNow,
    startLiveUpdates, stopLiveUpdates,

    start, stop, restart, forceStop, forceRestart, terminate, bulk,
    launchInstance, dismissInstanceError,

    toggleAccountFilter, selectAllAccounts, toggleSelection,
    toggleRegionFilter, clearRegionFilter,
    updateOptions, afterLogin, logout, onSessionLost
  }
}

export const LIFECYCLE_META = readonly(LIFECYCLE)
