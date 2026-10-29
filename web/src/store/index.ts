import { computed, reactive, readonly, watch } from 'vue'
import type {
  Account, ConfirmRequest, DrawerRequest, Instance, LifecycleState, ToastPayload
} from '@/types'
import { LIFECYCLE, isTransitional } from '@/lib/lifecycle'
import { toAccount, toInstance, toQuota } from '@/lib/adapt'
import {
  accounts as accountsApi, auth as authApi, instances as instancesApi,
  insights, jobs as jobsApi, launch as launchApi, settings as settingsApi,
  ApiError, errorText, setDangerCodeProvider, type InstanceAction, type LaunchInput
} from '@/api'
import type { ChangeIpDTO, JobDTO, SyncReportDTO } from '@/api/types'
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
  /**
   * 隐私模式：邮箱、IP、OCID、指纹默认打码，点眼睛按钮单独展开。
   *
   * 默认开。这个面板日常要发截图求助、录演示，而屏幕上到处是能拼出
   * 「你的机器在哪、归谁」的信息。默认关的话，等你想起来要打码时，
   * 截图已经发出去了。
   *
   * 只影响显示，不影响复制——复制永远给原文，否则这功能就从隐私保护
   * 变成了故意添堵。
   */
  privacyMode: localStorage.getItem('oci.privacy') !== 'off',
  /**
   * 重新开启隐私模式时自增，MaskedText 据此把所有「单独展开」收回。
   *
   * 不收回的话会出一个很坏的错觉：你重新打开隐私模式——通常正是
   * 「马上要截图」那一刻——以为全盖住了，实际上之前点开过的那几条
   * 还露着原文。
   */
  revealEpoch: 0,
  /**
   * 最近一次网卡增减落定的实例。详情抽屉据此重拉网络信息——
   * 换成新对象才能被 watch 到，所以每次都整体替换。
   */
  networkChanged: { id: '', at: 0 },
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
  /** 后台任务，最新的在前。进度由 SSE 的 job.updated 推送 */
  jobs: [] as JobDTO[],

  drawer: null as DrawerRequest | null,
  confirm: null as ConfirmRequest | null,
  /** 危险操作复核：后端要验证码时由 HTTP 层发起，DangerTotpPrompt 负责展示 */
  dangerPrompt: null as { message: string, resolve: (code: string | null) => void } | null,
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

watch(() => state.privacyMode, v => {
  localStorage.setItem('oci.privacy', v ? 'on' : 'off')
  // 只在「开启」时收回，关闭时没必要动——那时本来就全是原文。
  if (v) state.revealEpoch++
})

watch(() => state.options.reduceMotion, r => {
  document.documentElement.dataset.motion = r ? 'reduced' : ''
  localStorage.setItem('oci.motion', r ? 'reduced' : '')
}, { immediate: true })

/* ---------- UI 原语 ---------- */

/**
 * 弹一条 toast，ms 毫秒后自动收起；ms 为 0 则一直留着，直到用户手动关掉
 * 或被下一条顶掉。
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
  if (ms <= 0) return
  toastTimer = window.setTimeout(() => {
    if (seq === toastSeq) state.toast = null
  }, ms)
}

/**
 * 更换公网 IP 成功后的统一收尾。行内菜单与网络页共用。
 *
 * 提示不自动消失：新 IP 是用户接下来要复制去连的东西，6 秒根本来不及。
 * 列表那一行同时标出原 IP，刷新列表也保留，直到页面重新加载。
 */
function markIpChanged(instanceId: string, oldIp: string, newIp: string): void {
  // 按 id 取当前对象：换 IP 在后台任务里跑，等它结束时列表可能已经整体刷新过，
  // 提交时拿到的那个对象早就不在 state.instances 里了。
  const inst = state.instances.find(i => i.id === instanceId)
  if (!inst) return
  inst.previousIp = oldIp
  inst.publicIp = newIp
  toast({
    tone: 'success', title: `${inst.name} 已更换公网 IP`,
    body: `${oldIp} → ${newIp}\n新旧 IP 已记入「设置 → 审计日志」。`,
    command: `ssh ${inst.sshUser}@${newIp}`
  }, 0)
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

/**
 * 弹出验证码框并等用户输入。同时只会有一个：新请求进来时先把旧的当作取消，
 * 不然旧请求的 Promise 永远等不到结果。
 */
function requestDangerCode(message: string): Promise<string | null> {
  state.dangerPrompt?.resolve(null)
  return new Promise(resolve => {
    state.dangerPrompt = {
      message,
      resolve: code => { state.dangerPrompt = null; resolve(code) }
    }
  })
}
setDangerCodeProvider(requestDangerCode)
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
  // 保留本地的瞬时 UI 态（busy / settledAt / previousIp），否则一次刷新会把
  // 正在播放的按钮 spinner 和落定高光全部抹掉。
  const localUi = new Map(state.instances.map(i =>
    [i.id, { busy: i.busy, settledAt: i.settledAt, previousIp: i.previousIp }]))
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

/** 拉任务列表。失败不打扰：任务列表是旁路信息，不影响主流程。 */
async function loadJobs(): Promise<void> {
  try {
    const { jobs } = await jobsApi.list()
    state.jobs = jobs
  } catch (err) {
    console.warn('加载任务列表失败', err)
  }
}

/** 加载全部数据。登录成功后与手动刷新时调用。 */
async function loadAll(): Promise<void> {
  state.loading = true
  state.loadError = ''
  void loadJobs()
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

/**
 * 触发一次后端同步。这会真的去调 OCI，可能要几十秒，所以放在后台任务里跑；
 * 结果提示由 finishJob 按任务种类给出。
 */
async function syncNow(accountId?: string): Promise<void> {
  if (state.syncing) return
  state.syncing = true
  try {
    const { job } = await instancesApi.sync(accountId)
    trackJob(job, { quiet: true })
  } catch (err) {
    state.syncing = false
    toastError('同步失败', err)
  }
}

/** 同步任务结束后的提示。已终止的单独说，不混进台数——界面各处都不显示它们。 */
function announceSync(job: JobDTO): void {
  const report = job.result as SyncReportDTO | undefined
  const failures = report?.errors ?? []
  if (failures.length > 0) {
    toast({
      tone: 'warning',
      title: `同步完成，${failures.length} 个区域失败`,
      body: failures.map(e => `${e.accountAlias} · ${e.region}：${e.ociCode || e.message}`).join('\n')
    }, 9000)
  } else {
    toast({ tone: 'success', title: '同步完成', body: job.summary })
  }
  // 同步可能改变账号的连通性状态与配额。
  void loadAccounts().catch(() => undefined)
}

/* ---------- 后台任务 ---------- */

const isJobDone = (j: JobDTO) =>
  j.status !== 'queued' && j.status !== 'running'

interface TrackOptions {
  /** 成功后的自定义收尾。给了它，默认的成功提示就不再弹 */
  onSuccess?: (job: JobDTO) => void
  /** 不弹「已转入后台」的提示（比如同步，顶栏本来就有进度） */
  quiet?: boolean
}

/** 提交时登记的收尾回调。只在提交它的这个标签页里有。 */
const jobCallbacks = new Map<string, TrackOptions>()

/** 把新/变化的任务并进列表，返回之前的那一份（没有则为 undefined）。 */
function upsertJob(job: JobDTO): JobDTO | undefined {
  const idx = state.jobs.findIndex(j => j.id === job.id)
  if (idx < 0) {
    state.jobs.unshift(job)
    return undefined
  }
  const prev = state.jobs[idx]
  // SSE 与 HTTP 响应谁先到不一定；已经是终态的不能被更早的快照覆盖回去。
  if (isJobDone(prev) && !isJobDone(job)) return prev
  state.jobs[idx] = job
  return prev
}

/**
 * 登记一个刚提交的任务。
 *
 * 事件可能比 HTTP 响应先到——任务很快就失败时尤其如此——所以这里也要检查
 * 是否已经结束，结束了就当场收尾。
 */
function trackJob(job: JobDTO, opts: TrackOptions = {}): void {
  jobCallbacks.set(job.id, opts)
  upsertJob(job)
  const current = state.jobs.find(j => j.id === job.id) ?? job
  if (isJobDone(current)) {
    finishJob(current)
    return
  }
  if (!opts.quiet) {
    toast({
      tone: 'accent',
      title: `已转入后台：${job.title}`,
      body: '可以继续做别的，完成后会提示你。',
      link: { label: '查看任务', to: '/jobs' }
    })
  }
}

/** 任务结束：按种类给出提示，并刷新受影响的数据。 */
function finishJob(job: JobDTO): void {
  const opts = jobCallbacks.get(job.id)
  jobCallbacks.delete(job.id)

  if (job.instanceId || job.kind.startsWith('instance.')) void refreshInstances()
  if (job.kind === 'instance.sync' && job.status !== 'succeeded') {
    // 失败时不会有 sync.finished 事件，按服务端的真实状态复位。
    void refreshInstances()
  }

  if (job.status !== 'succeeded') {
    if (job.status === 'canceled') {
      toast({ tone: 'info', title: `已取消：${job.title}` })
      return
    }
    toast({
      tone: 'danger', title: `${job.title} 失败`,
      body: job.error, link: { label: '查看详情', to: '/jobs' }
    }, 12000)
    return
  }

  if (opts?.onSuccess) {
    opts.onSuccess(job)
    return
  }
  switch (job.kind) {
    case 'instance.change_ip': {
      const r = job.result as ChangeIpDTO | undefined
      if (r && job.instanceId) markIpChanged(job.instanceId, r.oldIp, r.newIp)
      return
    }
    case 'instance.sync':
      announceSync(job)
      return
    // 这两类由实例事件自己报：就绪提示带 SSH 命令，终止提示来自 instance.removed。
    case 'instance.launch':
    case 'instance.terminate':
    // 网卡挂上/分离的提示来自 instance.network。
    case 'instance.attach_vnic':
    case 'instance.detach_vnic':
      return
  }
  toast({ tone: 'success', title: `${job.title} 完成`, body: job.summary })
}

/** 取消排队中的任务。 */
async function cancelJob(id: string): Promise<void> {
  try {
    await jobsApi.cancel(id)
  } catch (err) {
    toastError('取消失败', err)
  }
}

/** 从列表里移除一条已结束的任务。 */
async function removeJob(id: string): Promise<void> {
  try {
    await jobsApi.remove(id)
    state.jobs = state.jobs.filter(j => j.id !== id)
  } catch (err) {
    toastError('移除失败', err)
  }
}

/** 清空全部已结束的任务。 */
async function clearFinishedJobs(): Promise<void> {
  try {
    await jobsApi.clearFinished()
    state.jobs = state.jobs.filter(j => !isJobDone(j))
  } catch (err) {
    toastError('清理失败', err)
  }
}

/** 进行中（含排队）的任务数，侧栏徽标用。 */
const activeJobCount = computed(() => state.jobs.filter(j => !isJobDone(j)).length)

/** 某台实例上进行中的任务。详情抽屉据此提示「有操作在后台进行」。 */
const activeJobsFor = (instanceId: string) =>
  state.jobs.filter(j => j.instanceId === instanceId && !isJobDone(j))

/* ---------- 实时事件 ---------- */

let disconnect: (() => void) | null = null

function applyEvent(event: ServerEvent): void {
  switch (event.type) {
    case 'job.updated': {
      const job = event.data as JobDTO | undefined
      if (!job?.id) break
      const prev = upsertJob(job)
      // 只对「见过它没结束」的任务报结果：页面刚打开时加载到的旧任务早就结束了，
      // 不该再弹一遍。
      if (isJobDone(job) && prev && !isJobDone(prev)) finishJob(job)
      break
    }

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

    case 'instance.network': {
      // 网卡挂上或分离后 IP 变了，列表里的 +N 要跟着变；读的是本地缓存，不贵。
      void refreshInstances()
      state.networkChanged = { id: event.instanceId ?? '', at: Date.now() }
      const inst = state.instances.find(i => i.id === event.instanceId)
      const name = inst?.name ?? '实例'
      if (event.state === 'ATTACHED') toast({ tone: 'success', title: `${name} 的附属网卡已挂载` })
      else if (event.state === 'DETACHED') toast({ tone: 'info', title: `${name} 的附属网卡已分离` })
      else toast({ tone: 'danger', title: `${name} 的附属网卡挂载失败`, body: '请到 OCI 控制台查看工作请求的错误信息。' }, 9000)
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
        // 按 id 重新找：下面的 refreshInstances 会整体替换 state.instances，
        // 并把 settledAt 拷到新对象上。清旧对象的话新对象上的高光永远不消失，
        // 扫光动画结束后那条渐变就一直停在行首。
        const id = inst.id
        window.setTimeout(() => {
          const cur = state.instances.find(i => i.id === id)
          if (cur) cur.settledAt = undefined
        }, 1200)
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
      command: inst.publicIp !== '—' ? `ssh ${inst.sshUser}@${inst.publicIp}` : undefined
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
      if (reconnected) {
        void refreshInstances()
        void loadJobs()
      }
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
    // 状态由 instance.updated 推送：任务可能还在排队，这里不抢先改成 TERMINATING。
    const { job } = await instancesApi.terminate(id, inst.name, preserveBootVolume)
    trackJob(job)
  } catch (err) {
    inst.anomaly = true
    inst.lastError = errorText(err)
    toastError(`${inst.name} 终止失败`, err)
  } finally {
    inst.busy = false
  }
}

/**
 * 批量操作。交给后端的批量接口在后台任务里执行：并发度在服务端压到 3，
 * 任务会等每台都落定，结果在任务列表里逐台可查。
 */
async function bulk(action: 'start' | 'stop' | 'restart'): Promise<void> {
  const ids = [...state.selection]
  if (ids.length === 0) return
  const op: InstanceAction = action === 'start' ? 'START' : action === 'stop' ? 'SOFTSTOP' : 'SOFTRESET'
  try {
    const { job } = await instancesApi.bulk(ids, op)
    state.selection.clear()
    trackJob(job)
  } catch (err) {
    toastError('批量操作失败', err)
  }
}

/** 创建实例。成功受理返回任务 id，失败返回 null（表单保持打开）。 */
async function launchInstance(input: LaunchInput): Promise<string | null> {
  try {
    const { job } = await launchApi.create(input)
    closeDrawer()
    // 新实例那一行会在任务提交创建后由 instance.updated 带进列表。
    trackJob(job)
    return job.id
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
  state.jobs = []
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

    toast, toastError, dismissToast, ask, openDrawer, closeDrawer, markIpChanged,

    loadAll, loadAccounts, loadInstances, refreshInstances, loadStatus, syncNow,
    startLiveUpdates, stopLiveUpdates,

    trackJob, cancelJob, removeJob, clearFinishedJobs, loadJobs, activeJobCount, activeJobsFor,

    start, stop, restart, forceStop, forceRestart, terminate, bulk,
    launchInstance, dismissInstanceError,

    toggleAccountFilter, selectAllAccounts, toggleSelection,
    toggleRegionFilter, clearRegionFilter,
    updateOptions, afterLogin, logout, onSessionLost
  }
}

export const LIFECYCLE_META = readonly(LIFECYCLE)
