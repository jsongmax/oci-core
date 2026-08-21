<script setup lang="ts">
/** §4.4.4 创建实例向导（520）：账号 → 区域·AD → 规格 → 镜像 → 网络 → 初始化 */
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import { shortAd } from '@/lib/adapt'
import { ALWAYS_FREE } from '@/lib/freetier'
import {
  launch as launchApi, hunt as huntApi, insights, network, errorText,
  type AvailabilityDomainDTO, type ImageDTO, type LaunchPresetDTO, type ShapeDTO,
  type SubnetDTO
} from '@/api'
import AppDrawer from '@/components/AppDrawer.vue'
import DrawerBody from '@/components/DrawerBody.vue'
import SectionCard from '@/components/SectionCard.vue'
import SwitchRow from '@/components/SwitchRow.vue'
import QuotaMeter from '@/components/QuotaMeter.vue'
import KeyValueList from '@/components/KeyValueList.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import CheckList from '@/components/CheckList.vue'

const { state, accountById, closeDrawer, launchInstance, ask, toast, toastError } = useStore()
const router = useRouter()

const STEPS = ['账号', '区域·AD', '规格', '镜像', '网络', '初始化']

/**
 * 立即创建 vs 容量守候。
 *
 * 共用同一套表单而不是再写一份：参数完全一致，两套表单必然会漂移——
 * 一边加了字段另一边忘了，结果是守候抢到的机器配置和预期不符，
 * 而且要等到抢到那一刻才暴露。
 */
const mode = ref<'now' | 'hunt'>('now')

const huntForm = reactive({
  intervalSeconds: 60,
  expiresInHours: 168,
  maxAttempts: 0,
  // 默认开。容量报告是只读接口，LaunchInstance 才是 Oracle 风控盯的那个——
  // 用一次只读换掉一次创建，是这里性价比最高的一处。
  precheckCapacity: true
})

/** 低于这个值就该警告。和后端 huntsvc.WarnIntervalSeconds 对齐。 */
const WARN_INTERVAL = 60
/**
 * 硬下限。开局用 30 兜底，挂载后以 /api/hunt 返回的值为准。
 *
 * 不写死是因为这个数只有后端说了算（huntsvc.MinIntervalSeconds）。
 * 前端另存一份，后端哪天抬到 60，这里会照旧放行 30，
 * 还会显示一句「提交后会被抬到 30 秒」——那时它是错的。
 */
const minInterval = ref(30)

const intervalTooFast = computed(() => huntForm.intervalSeconds < WARN_INTERVAL)

/**
 * 任务时长的快捷选项。
 *
 * 之前是个裸的小时输入框，于是出现了"577 小时"这种没人能读懂的值——
 * 用户想选的是"大概三周"，却得自己乘一遍。这类参数的自然单位是天和月，
 * 不是小时。自定义仍然保留：真要填 577 也随他。
 */
const DURATION_PRESETS = [
  { hours: 24, label: '1 天' },
  { hours: 72, label: '3 天' },
  { hours: 168, label: '1 周' },
  { hours: 336, label: '2 周' },
  { hours: 720, label: '1 个月' },
  { hours: 2160, label: '3 个月' }
]

const customDuration = ref(false)

/** 把小时数说成人话。确认摘要和自定义输入的回显都用它。 */
function durationText(hours: number): string {
  if (hours <= 0) return '—'
  const preset = DURATION_PRESETS.find(p => p.hours === hours)
  if (preset) return preset.label
  if (hours < 48) return `${hours} 小时`
  const days = Math.round(hours / 24)
  if (days < 60) return `${days} 天`
  return `${Math.round(days / 30)} 个月左右`
}

function pickDuration(hours: number) {
  huntForm.expiresInHours = hours
  customDuration.value = false
}

const intervalHint = computed(() => {
  const n = huntForm.intervalSeconds
  if (n < minInterval.value) return `低于硬下限，提交后会被抬到 ${minInterval.value} 秒`
  if (n < WARN_INTERVAL) return '偏激进，被 Oracle 限流的概率明显更高'
  if (n <= 180) return '推荐范围'
  return '较保守，命中率略低但几乎不会触发限流'
})
const step = ref(0)
const submitting = ref(false)

const usableAccounts = computed(() =>
  state.accounts.filter(a => a.status !== 'error' && a.status !== 'disabled')
)

const form = reactive({
  accountId: usableAccounts.value[0]?.id ?? state.accounts[0]?.id ?? '',
  region: '',
  ad: '',
  shape: 'VM.Standard.A1.Flex',
  // 跟着 ALWAYS_FREE 走，不写死。2026-06-15 起 ARM 从 4/24 砍到 2/12，
  // 默认给 4/24 等于把用户新建的机器直接送进 Oracle 的回收队列。
  ocpu: ALWAYS_FREE.armOcpus as number,
  memGb: ALWAYS_FREE.armMemoryGB as number,
  imageId: '',
  bootGb: 50,
  autoVcn: true,
  /** 空串表示「自动处理」，否则是用户选中的已有子网 */
  subnetId: '',
  publicIp: true,
  ipv6: false,
  name: '',
  sshKey: '',
  cloudInit: ''
})

/* ---------- 远端选项 ---------- */

const presets = ref<LaunchPresetDTO[]>([])
const ads = ref<AvailabilityDomainDTO[]>([])
const shapes = ref<ShapeDTO[]>([])
const images = ref<ImageDTO[]>([])
const subnets = ref<SubnetDTO[]>([])
const loadingSubnets = ref(false)
/** 上限为 0 表示「读不到」或「不设上限」，两种情况都不拦。 */
const quota = reactive({
  ocpuUsed: 0, ocpuLimit: 0,
  memUsed: 0, memLimit: 0,
  blockUsed: 0, blockLimit: 0
})

const loadingAds = ref(false)
const loadingImages = ref(false)
const optionError = ref('')

const account = computed(() => accountById(form.accountId))
const region = computed(() => form.region || account.value.regions[0] || '')

const selectedShape = computed(() => shapes.value.find(s => s.shape === form.shape))

/**
 * 当前可用域实际提供的规格名。
 *
 * 空集有两种含义——还没查到，或查询失败。两种情况下都**不做**可用性判断：
 * 误把所有预设标成不可用，比漏拦一次糟糕得多。
 */
const availableShapes = computed(() => new Set(shapes.value.map(s => s.shape)))

/**
 * 预设的规格在当前可用域不存在。
 *
 * 预设是一张静态表（后端 launchPresets），不知道任何区域的实际情况；
 * 而规格列表是实时查 OCI 的。两者不对一遍，用户就会在新区域点中
 * 「免费额度 AMD」——E2 是 2018 年那代硬件，新区域普遍没有部署——
 * 然后一路走到最后一步才被 Oracle 拒掉。
 *
 * 这跟第 4 步拦「子网禁公网 IP 却要分配公网 IP」是同一类判断：
 * 创建时必然失败的组合，不该等到点下去才说。
 */
function presetUnavailable(p: LaunchPresetDTO): boolean {
  if (availableShapes.value.size === 0) return false
  return !availableShapes.value.has(p.shape)
}

/** 已选规格在当前可用域不存在。选完规格再回头改区域 / AD 时最容易撞上。 */
const shapeUnavailable = computed(() =>
  !!form.shape && availableShapes.value.size > 0 && !availableShapes.value.has(form.shape))
const isFlexible = computed(() => selectedShape.value?.isFlexible ?? form.shape.includes('.Flex'))

/** 每 OCPU 的内存上限。取不到规格元数据时退回 A1.Flex 的 6 GB。 */
const maxPerOcpu = computed(() => selectedShape.value?.memoryOptions?.maxPerOcpuInGBs ?? 6)
const maxOcpu = computed(() => selectedShape.value?.ocpuOptions?.max ?? (isFlexible.value ? 4 : 1))
const maxMem = computed(() =>
  Math.min(form.ocpu * maxPerOcpu.value, selectedShape.value?.memoryOptions?.maxInGBs ?? 24)
)

const remainingOcpu = computed(() => Math.max(0, quota.ocpuLimit - quota.ocpuUsed))
const remainingMem = computed(() => Math.max(0, quota.memLimit - quota.memUsed))

/**
 * 引导卷滑块的上限。
 *
 * 原先写死 200 —— 那是**永久免费的块存储总额**，不是单卷上限，两个方向都不对：
 * 升级号被无理由地卡在 200 GB，免费号则是"几台加起来 200"，
 * 拿单卷 200 当上限根本没约束到总量。
 *
 * 现在按账号剩余的块存储配额算。读不到或不设上限时退回界面刻度上限——
 * OCI 单卷真实上限是 32 TB，做成滑块没法用。
 */
const SLIDER_MAX_BOOT_GB = 1024

const maxBootGb = computed(() => {
  if (!quota.blockLimit) return SLIDER_MAX_BOOT_GB
  const remaining = Math.max(0, quota.blockLimit - quota.blockUsed)
  // 至少给 50：Oracle 的引导卷下限就是 50，比它还小的滑块没有意义。
  return Math.max(50, Math.min(SLIDER_MAX_BOOT_GB, remaining))
})

/** 配额未知（limit 为 0）时不拦——总比因为读不到配额就完全用不了强。 */
const overQuota = computed(() => {
  if (!isFlexible.value || quota.ocpuLimit === 0) return false
  return form.ocpu > remainingOcpu.value || form.memGb > remainingMem.value
})

const canNext = computed(() => {
  if (step.value === 0) return !!form.accountId
  if (step.value === 1) return !!region.value && !!form.ad
  // 规格在当前可用域不存在时必然创建失败，和超配额一样在这里就拦住。
  if (step.value === 2) return !overQuota.value && !shapeUnavailable.value
  if (step.value === 3) return !!form.imageId
  // 子网禁公网 IP 却又要分配公网 IP，创建时必然失败，不如在这里就拦住。
  if (step.value === 4) return !subnetConflict.value
  if (step.value === 5) return form.name.trim().length > 0
  return true
})

/* ---------- 加载 ---------- */

/** 守候的间隔下限只有后端说了算，取回来覆盖兜底值。 */
async function loadHuntLimits() {
  try {
    const { limits } = await huntApi.list()
    if (limits?.minIntervalSeconds > 0) minInterval.value = limits.minIntervalSeconds
  } catch {
    // 取不到就用兜底的 30，提交时后端还会再夹一次。
  }
}

async function loadPresets() {
  try {
    presets.value = (await launchApi.presets()).presets
  } catch {
    // 预设只是快捷方式，读不到不影响手动配置。
  }
}

async function loadQuota() {
  if (!form.accountId) return
  try {
    const { quotas } = await insights.quota(form.accountId)
    const items = quotas[0]?.items ?? []
    // 按 key 取，不要匹配 name。
    //
    // name 是 Oracle 的限额名，会变；key 是后端给的稳定语义标识，
    // 存在的意义就是隔断这层耦合。这里原先匹配的是
    // 'standard-a1-core-count'，而后端返回的是
    // 'standard-a1-core-regional-count' —— 差一个 -regional，
    // 于是 limit 恒为 0，overQuota 在 limit 为 0 时直接放行，
    // 整个配额保护静默失效，界面上还一直显示"剩余 0 OCPU"。
    const find = (key: string) => items.find(i => i.key === key && i.known)
    const cores = find('ocpu')
    const mem = find('memory')
    const block = find('block')
    quota.ocpuUsed = cores?.used ?? 0
    quota.ocpuLimit = cores?.unlimited ? 0 : (cores?.limit ?? 0)
    quota.memUsed = mem?.used ?? 0
    quota.memLimit = mem?.unlimited ? 0 : (mem?.limit ?? 0)
    quota.blockUsed = block?.used ?? 0
    // unlimited 用 0 表示"不设上限"，与"读不到"共用同一条放行分支。
    quota.blockLimit = block?.unlimited ? 0 : (block?.limit ?? 0)
  } catch {
    quota.ocpuLimit = 0
    quota.memLimit = 0
    quota.blockLimit = 0
  }
}

async function loadRegionOptions() {
  if (!form.accountId || !region.value) return

  loadingAds.value = true
  optionError.value = ''
  try {
    // 先定可用域，再按可用域查规格——不能并发。
    //
    // 规格是**按可用域**而非按区域提供的：同一个区域里，E2.1.Micro 只存在于
    // 其中一个可用域（Oracle 对永久免费资源的明确限制）。只按区域查会把
    // 「区域里有、但这个 AD 没有」的规格也列进来，用户选了要到提交才失败。
    const adResult = await launchApi.availabilityDomains(form.accountId, region.value)
    ads.value = adResult.availabilityDomains
    if (!ads.value.some(a => a.name === form.ad)) {
      form.ad = ads.value[0]?.name ?? ''
    }
    await loadShapes()
  } catch (err) {
    optionError.value = errorText(err)
    ads.value = []
    shapes.value = []
    shapesKey = ''
  } finally {
    loadingAds.value = false
  }
}

/**
 * shapes 当前对应的 (账号, 区域, 可用域)。用来跳过重复查询。
 *
 * 需要它是因为有两条路都会触发加载：loadRegionOptions 定完 AD 后显式调一次，
 * 而它给 form.ad 赋值本身又会触发 watch。不去重的话每次换区域都会对 Oracle
 * 发两次一模一样的请求。
 */
let shapesKey = ''

/** 查当前可用域实际提供的规格。换 AD 要重查——各 AD 的硬件不一定一样。 */
async function loadShapes() {
  if (!form.accountId || !region.value) return
  const key = `${form.accountId}|${region.value}|${form.ad}`
  if (key === shapesKey) return
  shapesKey = key
  try {
    const { shapes: list } = await launchApi.shapes(
      form.accountId, region.value, form.ad || undefined)
    shapes.value = list
  } catch (err) {
    optionError.value = errorText(err)
    // 查不到就留空。下面的可用性判断会因此全部放行——
    // 拦不住总好过把所有预设都误标成不可用。
    shapes.value = []
    // 失败不该被缓存住：用户重试时要能真的再查一次。
    shapesKey = ''
  }
}

/**
 * 镜像必须按 shape 过滤：ARM 与 x86 的镜像不通用，
 * 不过滤会让用户选到一个根本起不来的镜像。
 */
async function loadImages() {
  if (!form.accountId || !region.value || !form.shape) return

  loadingImages.value = true
  try {
    const { images: list } = await launchApi.images(form.accountId, region.value, form.shape)
    images.value = list
    if (!list.some(i => i.id === form.imageId)) {
      // 默认选 Ubuntu：社区里绝大多数教程都基于它。
      const ubuntu = list.find(i => i.operatingSystem.toLowerCase().includes('ubuntu'))
      form.imageId = (ubuntu ?? list[0])?.id ?? ''
    }
  } catch (err) {
    optionError.value = errorText(err)
    images.value = []
  } finally {
    loadingImages.value = false
  }
}

/**
 * 列出该区域已有的子网，让用户可以直接复用。
 *
 * 之前这一步只有一个「自动创建」开关：关掉它就必须自己指定 subnetId，
 * 而界面根本没给指定的地方——等于这个开关只有开着才能用。
 */
async function loadSubnets() {
  if (!form.accountId || !region.value) return
  loadingSubnets.value = true
  try {
    const { subnets: list } = await network.subnets(form.accountId, region.value)
    subnets.value = (list ?? []).filter(sn => sn.lifecycleState === 'AVAILABLE')
    // 选中的子网如果已经不在了（换了区域或账号），退回自动处理。
    if (form.subnetId && !subnets.value.some(sn => sn.id === form.subnetId)) {
      pickSubnet('')
    }
  } catch {
    // 读不到子网不该挡住创建流程——自动处理那条路依然可用。
    subnets.value = []
  } finally {
    loadingSubnets.value = false
  }
}

function pickSubnet(id: string) {
  form.subnetId = id
  // 选了具体子网就不该再自动建网，两者是互斥的。
  form.autoVcn = id === ''
}

/** 选中的子网禁止公网 IP，却又勾了分配公网 IPv4——这个组合创建时必失败。 */
const subnetConflict = computed(() => {
  if (!form.subnetId || !form.publicIp) return ''
  const sn = subnets.value.find(x => x.id === form.subnetId)
  if (!sn?.prohibitPublicIpOnVnic) return ''
  return `子网 ${sn.displayName} 禁止分配公网 IP。请改选其他子网，或关掉「分配公网 IPv4」。`
})

function onOcpu(v: number) {
  form.ocpu = v
  if (form.memGb > v * maxPerOcpu.value) form.memGb = v * maxPerOcpu.value
}

function pickPreset(p: LaunchPresetDTO) {
  form.shape = p.shape
  form.ocpu = p.ocpus
  form.memGb = p.memoryInGbs
  form.bootGb = p.bootGb
  void loadImages()
}

const imageLabel = (im: ImageDTO) =>
  im.displayName || `${im.operatingSystem} ${im.operatingSystemVersion}`

/* ---------- 镜像分组与收敛 ---------- */

/** 当前选中的系统族，'' 表示全部。 */
const osFilter = ref('')
/** 同一版本只保留最新那次构建。 */
const latestOnly = ref(true)

/**
 * 去掉镜像名末尾的构建日期，得到"同一版本"的键。
 *
 *   Canonical-Ubuntu-24.04-2026.06.29-0  →  Canonical-Ubuntu-24.04
 *
 * 匹配不上就退回完整名字（自定义镜像不遵守这套命名），此时不会被合并，
 * 也就不会误伤。
 */
function imageFamily(im: ImageDTO): string {
  const name = im.displayName || ''
  const stripped = name.replace(/-\d{4}\.\d{2}\.\d{2}-\d+$/, '')
  return stripped || `${im.operatingSystem} ${im.operatingSystemVersion}`
}

/** 系统族筛选后的镜像。 */
const osFiltered = computed(() =>
  osFilter.value ? images.value.filter(i => i.operatingSystem === osFilter.value) : images.value
)

/** 最终列出的镜像。latestOnly 打开时同一版本只留最新一次构建。 */
const visibleImages = computed(() => {
  if (!latestOnly.value) return osFiltered.value
  const newest = new Map<string, ImageDTO>()
  for (const im of osFiltered.value) {
    const key = imageFamily(im)
    const prev = newest.get(key)
    if (!prev || new Date(im.timeCreated) > new Date(prev.timeCreated)) newest.set(key, im)
  }
  return [...newest.values()]
})

/** 被 latestOnly 折叠掉的旧构建数量，必须显式告诉用户。 */
const hiddenBuilds = computed(() => osFiltered.value.length - visibleImages.value.length)

/** 系统族筛选条，按镜像数量降序。 */
const osFamilies = computed(() => {
  const count = new Map<string, number>()
  for (const im of images.value) {
    count.set(im.operatingSystem, (count.get(im.operatingSystem) ?? 0) + 1)
  }
  return [...count.entries()]
    .sort((a, b) => b[1] - a[1])
    .map(([os, n]) => ({ os, n }))
})

const selectedImage = computed(() => images.value.find(i => i.id === form.imageId))

async function next() {
  if (!canNext.value || submitting.value) return
  if (step.value < STEPS.length - 1) {
    step.value++
    if (step.value === 3) void loadImages()
    if (step.value === 4) void loadSubnets()
    return
  }

  if (mode.value === 'hunt') {
    await submitHunt()
    return
  }

  submitting.value = true
  try {
    await launchInstance({
      accountId: form.accountId,
      region: region.value,
      availabilityDomain: form.ad,
      displayName: form.name.trim(),
      shape: form.shape,
      ocpus: form.ocpu,
      memoryInGbs: form.memGb,
      imageId: form.imageId,
      bootVolumeGb: form.bootGb,
      subnetId: form.subnetId || undefined,
      autoCreateNetwork: form.autoVcn,
      assignPublicIp: form.publicIp,
      enableIpv6: form.ipv6,
      sshPublicKey: form.sshKey.trim() || undefined,
      cloudInit: form.cloudInit.trim() || undefined
    })
  } finally {
    submitting.value = false
  }
}

/**
 * 建守候任务。
 *
 * 走 L2 确认而不是直接提交：这个任务会在无人看管下持续调用 Oracle，
 * 风险与"点一次创建"完全不是一个量级，必须让人看一眼再确认。
 * 间隔偏激进时确认框里要说清楚，而不是提交完再用 toast 补一句。
 */
async function submitHunt() {
  const risk = intervalTooFast.value
    ? `你设置的间隔是 ${huntForm.intervalSeconds} 秒，低于建议的 ${WARN_INTERVAL} 秒。`
      + '高频调用创建接口是 Oracle 明确不欢迎的行为，被限流甚至标记账号的概率明显更高。'
    : ''

  ask({
    level: 2,
    title: '建立容量守候任务',
    body: `任务会每 ${huntForm.intervalSeconds} 秒尝试创建一次 ${form.shape}，`
      + `直到抢到、或 ${durationText(huntForm.expiresInHours)}后到期。`
      + `期间会持续调用该账号的 Oracle 接口。${risk}`,
    okLabel: risk ? '我了解风险，仍然建立' : '建立任务',
    onConfirm: async () => {
      submitting.value = true
      try {
        const res = await huntApi.create({
          accountId: form.accountId,
          region: region.value,
          name: form.name.trim(),
          availabilityDomains: form.ad ? [form.ad] : undefined,
          intervalSeconds: huntForm.intervalSeconds,
          precheckCapacity: huntForm.precheckCapacity,
          maxAttempts: huntForm.maxAttempts || undefined,
          expiresInHours: huntForm.expiresInHours,
          displayName: form.name.trim(),
          shape: form.shape,
          ocpus: form.ocpu,
          memoryInGbs: form.memGb,
          imageId: form.imageId,
          bootVolumeGb: form.bootGb,
          subnetId: form.subnetId || undefined,
          autoCreateNetwork: form.autoVcn,
          assignPublicIp: form.publicIp,
          enableIpv6: form.ipv6,
          sshPublicKey: form.sshKey.trim() || undefined,
          cloudInit: form.cloudInit.trim() || undefined
        })
        closeDrawer()
        toast({ tone: 'accent', title: '守候任务已启动', body: res.notice }, 9000)
        void router.push('/hunt')
      } catch (err) {
        toastError('建立守候任务失败', err)
      } finally {
        submitting.value = false
      }
    }
  })
}

onMounted(async () => {
  await Promise.all([loadPresets(), loadHuntLimits()])
  if (!form.region) form.region = account.value.regions[0] ?? ''
  await Promise.all([loadRegionOptions(), loadQuota()])
  if (!form.name) {
    // 预填一个不会撞名的默认值，用户改起来也方便。
    form.name = `${account.value.code.toLowerCase()}-${Date.now().toString(36).slice(-4)}`
  }
})

watch(() => form.accountId, async () => {
  form.region = accountById(form.accountId).regions[0] ?? ''
  await Promise.all([loadRegionOptions(), loadQuota()])
})

watch(() => form.region, () => void loadRegionOptions())
// 换可用域要重查规格：各 AD 的硬件不一定一样，永久免费的 E2.1.Micro
// 更是只在其中一个 AD 上提供。
watch(() => form.ad, () => void loadShapes())
watch(() => form.shape, () => {
  if (step.value >= 3) void loadImages()
})
</script>

<template>
  <AppDrawer width="narrow" @close="closeDrawer()">
    <header class="head">
      <div class="head__row">
        <h2 class="head__title">创建实例</h2>
        <button class="head__close" aria-label="关闭" @click="closeDrawer()">✕</button>
      </div>
      <ol class="steps">
        <li v-for="(s, n) in STEPS" :key="s" :class="{ 'is-done': n <= step, 'is-current': n === step }">
          <span class="steps__bar" /><span class="steps__label">{{ s }}</span>
        </li>
      </ol>
    </header>

    <DrawerBody>
      <p v-if="optionError" class="err">{{ optionError }}</p>

      <template v-if="step === 0">
        <SectionCard title="选择账号" note="凭据失效或已禁用的账号不可选">
          <EmptyState v-if="state.accounts.length === 0" title="还没有接入任何 Oracle 账号" />
          <button v-for="a in state.accounts" :key="a.id" class="opt"
                  :class="{ 'is-picked': form.accountId === a.id }"
                  :disabled="a.status === 'error' || a.status === 'disabled'"
                  @click="form.accountId = a.id">
            <span class="opt__radio" />
            <span class="mono opt__code" :style="{ color: acctColor(a.colorIndex) }">{{ a.code }}</span>
            <span class="opt__text">
              <span class="opt__title">{{ a.alias }}</span>
              <span class="opt__sub">
                {{ a.status === 'error' ? '凭据失效，无法创建'
                  : a.status === 'disabled' ? '已禁用'
                  : a.quota.ocpuLimit
                    ? `剩余 ${a.quota.ocpuLimit - a.quota.ocpuUsed} OCPU / ${a.quota.memLimit - a.quota.memUsed} GB`
                    : '配额未知' }}
              </span>
            </span>
            <span class="mono opt__tag">{{ a.regions.length }} 区域</span>
          </button>
        </SectionCard>
      </template>

      <template v-else-if="step === 1">
        <SectionCard title="区域" note="仅显示该账号已订阅的区域">
          <button v-for="r in account.regions" :key="r" class="opt" :class="{ 'is-picked': region === r }"
                  @click="form.region = r">
            <span class="opt__radio" />
            <span class="opt__text"><span class="opt__title mono">{{ r }}</span></span>
            <span class="mono opt__tag">
              {{ state.instances.filter(i => i.accountId === account.id && i.region === r).length }} 台
            </span>
          </button>
        </SectionCard>

        <SectionCard title="可用域" note="容量按可用域独立计算，一个开不出可以换一个">
          <SkeletonRows v-if="loadingAds" :rows="3" />
          <EmptyState v-else-if="ads.length === 0" title="该区域没有可用域" />
          <button v-for="ad in ads" :key="ad.id" class="opt" :class="{ 'is-picked': form.ad === ad.name }"
                  @click="form.ad = ad.name">
            <span class="opt__radio" />
            <span class="opt__text">
              <span class="opt__title mono">{{ shortAd(ad.name) }}</span>
              <span class="opt__sub mono">{{ ad.name }}</span>
            </span>
          </button>
        </SectionCard>
      </template>

      <template v-else-if="step === 2">
        <SectionCard title="免费额度预设"
                     :note="form.ad ? `已按 ${shortAd(form.ad)} 实际提供的规格过滤` : ''">
          <button v-for="p in presets" :key="p.key" class="opt"
                  :class="{ 'is-picked': form.shape === p.shape && form.ocpu === p.ocpus && form.memGb === p.memoryInGbs }"
                  :disabled="presetUnavailable(p)"
                  :title="presetUnavailable(p) ? `${p.shape} 在 ${form.ad} 不提供` : ''"
                  @click="pickPreset(p)">
            <span class="opt__radio" />
            <span class="opt__text">
              <span class="opt__title">{{ p.label }}</span>
              <span class="opt__sub">{{ p.description }}</span>
            </span>
            <!-- 不可用要盖过「免费」标：那个标此刻是误导，
                 免费与否已经无所谓，根本开不出来 -->
            <span v-if="presetUnavailable(p)" class="opt__tag" style="color: var(--warning)">
              该可用域不提供
            </span>
            <span v-else-if="p.freeTier" class="opt__tag" style="color: var(--accent)">免费</span>
          </button>

          <!-- 规格不可用是永久性的，和「暂时没有容量」完全不同：
               等下去不会变，挂守候只是白白消耗创建请求 -->
          <p v-if="shapeUnavailable" class="shape-warn t-xs">
            <b>{{ form.shape }}</b> 在 <span class="mono">{{ form.ad }}</span> 不提供。
            这不是「暂时没有容量」——该可用域没有部署这种硬件，等待或反复重试都不会成功。
            请换一个规格，或回上一步换可用域。
          </p>
        </SectionCard>

        <SectionCard v-if="isFlexible" title="算力"
                     :note="`OCPU 与内存联动：每 OCPU 最多 ${maxPerOcpu} GB`">
          <div class="pad">
            <label class="slider">
              <span class="slider__head"><span>OCPU</span><span class="mono">{{ form.ocpu }}</span></span>
              <input type="range" min="1" :max="maxOcpu" step="1" :value="form.ocpu"
                     @input="onOcpu(Number(($event.target as HTMLInputElement).value))" />
            </label>
            <label class="slider">
              <span class="slider__head"><span>内存</span><span class="mono">{{ form.memGb }} GB</span></span>
              <input type="range" min="1" :max="maxMem" step="1" v-model.number="form.memGb" />
            </label>
            <label class="slider">
              <span class="slider__head"><span>引导卷</span><span class="mono">{{ form.bootGb }} GB</span></span>
              <input type="range" min="50" :max="maxBootGb" step="10" v-model.number="form.bootGb" />
              <span class="slider__hint">
                {{ quota.blockLimit
                  ? `账号块存储剩余 ${Math.max(0, quota.blockLimit - quota.blockUsed)} GB`
                  : '未读到块存储配额，提交时后端会再校验一次' }}
              </span>
            </label>

            <div v-if="quota.ocpuLimit" class="quota-box">
              <QuotaMeter label="ARM OCPU" :used="quota.ocpuUsed + form.ocpu" :limit="quota.ocpuLimit" />
              <QuotaMeter label="内存" :used="quota.memUsed + form.memGb" :limit="quota.memLimit" unit=" GB" />
            </div>
            <p v-else class="hint">未能读取该账号的配额，创建时后端会再校验一次。</p>

            <p v-if="overQuota" class="over">
              超出该账号剩余配额（{{ remainingOcpu }} OCPU / {{ remainingMem }} GB），无法进入下一步
            </p>
          </div>
        </SectionCard>

        <SectionCard v-else title="算力" note="该规格为固定配置">
          <div class="pad">
            <p class="hint mono">{{ form.shape }} · {{ form.ocpu }} OCPU · {{ form.memGb }} GB</p>
          </div>
        </SectionCard>
      </template>

      <template v-else-if="step === 3">
        <SectionCard title="镜像" :note="`已按 ${form.shape} 过滤，ARM 与 x86 镜像不通用`">
          <SkeletonRows v-if="loadingImages" :rows="4" />
          <EmptyState v-else-if="images.length === 0" title="该规格下没有可用镜像"
                      body="换一个规格或区域再试。" />
          <template v-else>
            <div class="imgbar">
              <div class="imgbar__chips">
                <button class="chip" :class="{ 'is-on': osFilter === '' }" @click="osFilter = ''">
                  全部 <span class="chip__n">{{ images.length }}</span>
                </button>
                <button v-for="f in osFamilies" :key="f.os" class="chip"
                        :class="{ 'is-on': osFilter === f.os }"
                        @click="osFilter = osFilter === f.os ? '' : f.os">
                  {{ f.os }} <span class="chip__n">{{ f.n }}</span>
                </button>
              </div>
              <label class="imgbar__latest">
                <input type="checkbox" v-model="latestOnly" />
                只看最新构建
                <span v-if="latestOnly && hiddenBuilds > 0" class="dim-3">已折叠 {{ hiddenBuilds }} 个旧构建</span>
              </label>
            </div>
            <EmptyState v-if="visibleImages.length === 0" title="该系统族下没有镜像"
                        body="换一个系统族，或取消「只看最新构建」。" />
            <button v-for="im in visibleImages" :key="im.id" class="opt"
                    :class="{ 'is-picked': form.imageId === im.id }" @click="form.imageId = im.id">
              <span class="opt__radio" />
              <span class="opt__text">
                <span class="opt__title">{{ imageLabel(im) }}</span>
                <span class="opt__sub">{{ im.operatingSystem }} {{ im.operatingSystemVersion }}</span>
              </span>
            </button>
          </template>
        </SectionCard>
      </template>

      <template v-else-if="step === 4">
        <SectionCard title="子网" note="不确定就用自动处理，无需理解 OCI 网络模型">
          <SkeletonRows v-if="loadingSubnets" :rows="2" />
          <template v-else>
            <button class="opt" :class="{ 'is-picked': form.subnetId === '' }" @click="pickSubnet('')">
              <span class="opt__radio" />
              <span class="opt__text">
                <span class="opt__title">自动处理</span>
                <span class="opt__sub">
                  有可用公网子网就复用；没有则创建 10.0.0.0/16 与网关、路由、子网
                </span>
              </span>
            </button>
            <button v-for="sn in subnets" :key="sn.id" class="opt"
                    :class="{ 'is-picked': form.subnetId === sn.id }" @click="pickSubnet(sn.id)">
              <span class="opt__radio" />
              <span class="opt__text">
                <span class="opt__title">{{ sn.displayName }}</span>
                <span class="opt__sub mono">
                  {{ sn.cidrBlock }} ·
                  {{ sn.availabilityDomain ? shortAd(sn.availabilityDomain) : '区域级' }} ·
                  {{ sn.prohibitPublicIpOnVnic ? '禁止公网 IP' : '允许公网 IP' }}
                </span>
              </span>
            </button>
            <p v-if="subnets.length === 0" class="pad t-xs dim-3">
              该区域还没有可用子网，将由「自动处理」创建。
            </p>
          </template>
        </SectionCard>

        <SectionCard title="地址">
          <SwitchRow v-model="form.publicIp" title="分配公网 IPv4"
                     sub="临时 IP，可在实例详情中更换" />
          <SwitchRow v-model="form.ipv6" title="启用 IPv6"
                     sub="为 VCN 与子网分配前缀并给实例配置 IPv6 地址" />
          <p v-if="subnetConflict" class="pad t-xs" style="color: var(--warning); line-height: 1.7">
            {{ subnetConflict }}
          </p>
        </SectionCard>
      </template>

      <template v-else>
        <SectionCard title="初始化">
          <div class="pad">
            <div class="field">
              <label for="iname">实例名称</label>
              <input id="iname" v-model="form.name" class="input mono" />
            </div>
            <div class="field">
              <label for="sshkey">SSH 公钥</label>
              <textarea id="sshkey" v-model="form.sshKey" class="textarea" style="height: 68px"
                        spellcheck="false" placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5…" />
              <p class="hint">不填就只能通过串行控制台登录。</p>
            </div>
            <div class="field">
              <label for="ci">cloud-init（可选）</label>
              <textarea id="ci" v-model="form.cloudInit" class="textarea" style="height: 140px"
                        spellcheck="false" placeholder="#cloud-config" />
            </div>
          </div>
        </SectionCard>

        <SectionCard title="创建方式">
          <div class="pad">
            <div class="modes">
              <button class="mode" :class="{ 'is-on': mode === 'now' }" @click="mode = 'now'">
                <span class="mode__t">立即创建</span>
                <span class="mode__s">现在提交一次。没有容量就直接失败。</span>
              </button>
              <button class="mode" :class="{ 'is-on': mode === 'hunt' }" @click="mode = 'hunt'">
                <span class="mode__t">容量守候</span>
                <span class="mode__s">后台反复尝试，直到抢到或到期。</span>
              </button>
            </div>
          </div>
        </SectionCard>

        <SectionCard v-if="mode === 'hunt'" title="守候参数"
                     note="任务会长期无人看管地运行，参数直接决定风险">
          <div class="pad">
            <div class="field">
              <label for="hi">尝试间隔（秒）</label>
              <input id="hi" v-model.number="huntForm.intervalSeconds" class="input mono"
                     type="number" :min="minInterval" max="3600" step="10" />
              <p class="hint" :style="{ color: intervalTooFast ? 'var(--warning)' : undefined }">
                {{ intervalHint }}
              </p>
            </div>

            <!-- 警告必须在提交之前就在眼前，而不是提交后用 toast 补一句 -->
            <CheckList v-if="intervalTooFast" :items="[
              { tone: 'warn',
                text: `${huntForm.intervalSeconds} 秒偏激进。高频调用创建接口易触发 429 限流（该账号全部任务会自动降速），严重时账号被标记。` },
              { tone: 'info',
                text: '容量释放后有数分钟的申领窗口，不是抢毫秒。间隔减半，命中率提升有限，请求量翻倍。' }
            ]" />

            <SwitchRow :model-value="huntForm.precheckCapacity"
                       title="先查容量再尝试"
                       sub="先查只读的容量报告，没货就跳过本轮，不发创建请求"
                       @update:model-value="v => huntForm.precheckCapacity = v" />

            <CheckList v-if="!huntForm.precheckCapacity" :items="[
              { tone: 'warn',
                text: '关掉后每轮都直接调创建接口，不论有没有容量。除非确认容量报告在该区域不准，否则建议开着。' }
            ]" />

            <div class="field">
              <label>任务时长</label>
              <div class="chips">
                <button v-for="d in DURATION_PRESETS" :key="d.hours" class="chip"
                        :class="{ 'is-on': !customDuration && huntForm.expiresInHours === d.hours }"
                        @click="pickDuration(d.hours)">{{ d.label }}</button>
                <button class="chip" :class="{ 'is-on': customDuration }"
                        @click="customDuration = true">自定义</button>
              </div>
              <input v-if="customDuration" v-model.number="huntForm.expiresInHours"
                     class="input mono chips__custom" type="number" min="1" max="8760" step="24"
                     aria-label="自定义任务时长（小时）" />
              <p class="hint">
                {{ customDuration ? `${huntForm.expiresInHours} 小时 ≈ ${durationText(huntForm.expiresInHours)} · ` : '' }}
                到期自动停止并推通知。默认 7 天，避免建完就忘。
              </p>
            </div>

            <div class="field">
              <label for="hm">最大尝试次数（0 = 不限）</label>
              <input id="hm" v-model.number="huntForm.maxAttempts" class="input mono"
                     type="number" min="0" max="100000" step="100" />
            </div>
          </div>
        </SectionCard>

        <SectionCard title="确认">
          <KeyValueList :items="[
            { k: '账号', v: `${account.alias} · ${account.code}`, tone: acctColor(account.colorIndex) },
            { k: '区域 · AD', v: `${region} · ${shortAd(form.ad)}`, mono: true },
            { k: '规格', v: form.shape, mono: true },
            { k: '算力', v: `${form.ocpu} OCPU · ${form.memGb} GB`, mono: true },
            { k: '镜像', v: selectedImage ? imageLabel(selectedImage) : '—' },
            { k: '引导卷', v: `${form.bootGb} GB`, mono: true },
            { k: '网络', v: `${form.autoVcn ? '自动创建/复用 VCN' : '使用现有 VCN'}${form.publicIp ? ' · 公网 IPv4' : ''}${form.ipv6 ? ' · IPv6' : ''}` },
            { k: '名称', v: form.name, mono: true },
            { k: '方式', v: mode === 'hunt'
              ? `容量守候 · 每 ${huntForm.intervalSeconds} 秒 · ${durationText(huntForm.expiresInHours)}后到期`
                + (huntForm.precheckCapacity ? ' · 先查容量' : ' · 不查容量直接尝试')
              : '立即创建' }
          ]" />
          <p class="free">免费额度内预估费用 $0.00 · 超出部分按 Oracle 定价计费</p>
        </SectionCard>
      </template>
    </DrawerBody>

    <footer class="foot">
      <button class="btn" :disabled="step === 0 || submitting" @click="step--">上一步</button>
      <span class="t-2xs dim-3">
        {{ step === 2 ? `滑块联动：每 OCPU 最多 ${maxPerOcpu} GB`
          : step === 5 ? (mode === 'hunt'
            ? '任务会立刻尝试第一次，之后按间隔重试'
            : '创建后会立即在列表中插入 PROVISIONING 行')
          : '' }}
      </span>
      <button class="btn btn--primary" :disabled="!canNext || submitting" @click="next()">
        {{ submitting ? '提交中…'
          : step === STEPS.length - 1 ? (mode === 'hunt' ? '建立守候任务' : '创建实例')
          : '下一步' }}
      </button>
    </footer>
  </AppDrawer>
</template>

<style scoped>
.imgbar {
  display: flex; flex-direction: column; gap: 10px;
  padding: 12px 16px; border-bottom: 1px solid var(--border-subtle);
}
.imgbar__chips { display: flex; flex-wrap: wrap; gap: 6px; }
.imgbar__latest {
  display: flex; align-items: center; gap: 8px;
  font-size: 11px; color: var(--text-secondary); cursor: pointer;
}
.chip {
  display: inline-flex; align-items: center; gap: 5px;
  padding: 3px 9px; border-radius: var(--radius-full);
  border: 1px solid var(--border-default); background: transparent;
  color: var(--text-secondary); font-size: 11px; cursor: pointer;
}
.chip:hover { background: var(--bg-hover); }
.chip.is-on { border-color: var(--accent); color: var(--accent); }
.chip__n { font-size: 10px; color: var(--text-tertiary); }
.chip.is-on .chip__n { color: var(--accent); }

.head { flex: 0 0 auto; padding: 18px 20px 14px; border-bottom: 1px solid var(--border-subtle); }
.head__row { display: flex; align-items: center; gap: 10px; }
.head__title { margin: 0; flex: 1 1 auto; font-size: 20px; line-height: 28px; font-weight: 600; }
.head__close { border: 0; background: none; color: var(--text-secondary); cursor: pointer; font-size: 14px; }
.steps { margin: 14px 0 0; padding: 0; list-style: none; display: flex; gap: 6px; }
.steps li { flex: 1 1 auto; display: flex; flex-direction: column; gap: 6px; }
.steps__bar { height: 3px; border-radius: var(--radius-full); background: var(--border-default); transition: background var(--dur-normal); }
.steps li.is-done .steps__bar { background: var(--accent); }
.steps__label { font-size: 11px; color: var(--text-tertiary); white-space: nowrap; }
.steps li.is-current .steps__label { color: var(--text-primary); }

.err {
  margin: 12px 16px 0; padding: 10px 12px; border-radius: var(--radius-md);
  border: 1px solid var(--danger); background: var(--danger-soft);
  color: var(--danger); font-size: 12px; line-height: 18px;
}
.hint { margin: 0; font-size: 11px; color: var(--text-tertiary); line-height: 16px; }

.opt {
  display: flex; align-items: center; gap: 11px; width: 100%; padding: 13px 16px;
  border: 0; border-bottom: 1px solid var(--border-subtle); background: transparent;
  color: var(--text-primary); cursor: pointer; text-align: left;
}
.opt:hover:not([disabled]) { background: var(--bg-hover); }
.opt.is-picked { background: var(--accent-soft); }
.opt[disabled] { opacity: 0.45; cursor: not-allowed; }
.opt__radio { width: 15px; height: 15px; flex: 0 0 auto; border-radius: var(--radius-full); border: 1px solid var(--border-strong); }
.opt.is-picked .opt__radio { border-color: var(--accent); background: var(--accent); }
.opt__code { font-size: 11px; font-weight: 600; padding: 2px 7px; border-radius: var(--radius-full); background: var(--bg-inset); }
.opt__text { flex: 1 1 auto; min-width: 0; display: flex; flex-direction: column; }
.opt__title { font-size: 13px; font-weight: 500; }
.opt__sub { font-size: 11px; color: var(--text-tertiary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.opt__tag { flex: 0 0 auto; font-size: 11px; color: var(--text-tertiary); }

.shape-warn {
  margin: 0; padding: 12px 16px; line-height: 1.8;
  color: var(--warning); border-top: 1px solid var(--border-subtle);
}

.pad { padding: 16px; display: flex; flex-direction: column; gap: 14px; }
.slider { display: flex; flex-direction: column; gap: 8px; }
.slider__head { display: flex; justify-content: space-between; font-size: 12px; color: var(--text-secondary); }
.slider__head .mono { color: var(--text-primary); }
input[type='range'] { width: 100%; accent-color: var(--accent); }
.quota-box { display: flex; flex-direction: column; gap: 8px; padding: 14px; border-radius: var(--radius-md); background: var(--bg-inset); }
.over { margin: 0; font-size: 12px; color: var(--danger); }
.free { margin: 14px 16px; padding: 12px 14px; border-radius: var(--radius-md); background: var(--success-soft); color: var(--success); font-size: 13px; font-weight: 600; }
.foot {
  flex: 0 0 auto; padding: 14px 20px; border-top: 1px solid var(--border-subtle);
  background: var(--bg-surface); display: flex; align-items: center; gap: 10px;
}
.foot > span { flex: 1 1 auto; }

/* ---- 创建方式 ---- */

.modes { display: flex; gap: 10px; flex-wrap: wrap; }

.mode {
  flex: 1 1 200px;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
  align-items: flex-start;
  text-align: left;
  padding: 11px 13px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  background: var(--bg-inset);
  cursor: pointer;
  transition: border-color var(--dur-fast), background var(--dur-fast);
}
.mode:hover { border-color: var(--border-strong); }
.mode.is-on { border-color: var(--accent); background: var(--bg-raised); }

.mode__t { font-size: var(--t-sm); font-weight: 600; }
.mode__s { font-size: var(--t-2xs); color: var(--text-3); line-height: 1.6; }


.chips { display: flex; flex-wrap: wrap; gap: 6px; }

.chip {
  padding: 5px 11px;
  font-size: var(--t-2xs);
  border: 1px solid var(--border-default);
  border-radius: 999px;
  background: var(--bg-inset);
  color: var(--text-2);
  cursor: pointer;
  transition: border-color var(--dur-fast), color var(--dur-fast);
}
.chip:hover { border-color: var(--border-strong); }
.chip.is-on { border-color: var(--accent); color: var(--accent); }

.chips__custom { margin-top: 8px; }

</style>
