<script setup lang="ts">
/** §6.8 ShapeConfigurator + 引导卷扩容/VPU：滑块联动 + 配额校验 + 前置提示 */
import { computed, onMounted, ref, watch } from 'vue'
import type { Account, Instance } from '@/types'
import { useStore } from '@/store'
import { storage, launch as launchApi, type ShapeDTO } from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import CheckList from '@/components/CheckList.vue'
import QuotaMeter from '@/components/QuotaMeter.vue'

const props = defineProps<{
  instance: Instance
  account: Account
  /** 引导卷 OCID。缺失时说明还没同步到，扩容按钮需禁用 */
  bootVolumeId?: string
}>()

const { ask, toast, toastError, refreshInstances } = useStore()

const size = ref(props.instance.bootGb)
const vpu = ref(props.instance.vpu)
const ocpu = ref(props.instance.ocpu)
const mem = ref(props.instance.memGb)
const applyingVolume = ref(false)
const applyingShape = ref(false)

/**
 * 这个规格的元数据，来自 OCI 的 ListShapes。
 *
 * 之前这里靠字符串嗅探 `shape.includes('A1.Flex')` 判断"是不是弹性规格"，
 * 于是 E5.Flex / E4.Flex / E3.Flex 全被当成固定配置，滑块禁用、还给出
 * 一句错的「该规格为固定配置，无法调整」——那几种规格明明是弹性的。
 *
 * 创建向导早就在查这个接口了，改配置这边没用，同一个信息两套来源。
 */
const shapeMeta = ref<ShapeDTO | null>(null)

async function loadShapeMeta() {
  try {
    const { shapes } = await launchApi.shapes(
      props.instance.accountId, props.instance.region, props.instance.adFull || undefined)
    shapeMeta.value = shapes.find(s => s.shape === props.instance.shape) ?? null
  } catch {
    // 查不到就回落到按名字判断。宁可少给一点能力，也不要因为一次
    // 查询失败就把本来能改的实例锁死。
    shapeMeta.value = null
  }
}

onMounted(() => void loadShapeMeta())
watch(() => [props.instance.shape, props.instance.region], () => void loadShapeMeta())

/** 元数据取不到时按名字兜底：带 .Flex 的都是弹性规格，不止 A1。 */
const isFlexible = computed(() =>
  shapeMeta.value?.isFlexible ?? props.instance.shape.includes('.Flex'))

/** 每 OCPU 的内存上限。A1.Flex 是 6，E 系列要大得多，不能写死。 */
const maxPerOcpu = computed(() => shapeMeta.value?.memoryOptions?.maxPerOcpuInGBs ?? 6)

const maxOcpu = computed(() => {
  const shapeMax = shapeMeta.value?.ocpuOptions?.max ?? 4
  const q = props.account.quota
  if (q.unlimited.ocpu || !q.ocpuLimit) return shapeMax
  // 配额只剩这么多，滑块就不该拖得更高——拖上去也是提交时被拒。
  return Math.max(props.instance.ocpu, Math.min(shapeMax, q.ocpuLimit - q.ocpuUsed + props.instance.ocpu))
})

const maxMem = computed(() => {
  if (!isFlexible.value) return mem.value
  const byRatio = ocpu.value * maxPerOcpu.value
  const shapeCap = shapeMeta.value?.memoryOptions?.maxInGBs ?? byRatio
  const q = props.account.quota
  const byQuota = (q.unlimited.mem || !q.memLimit)
    ? Number.POSITIVE_INFINITY
    : q.memLimit - q.memUsed + props.instance.memGb
  return Math.max(props.instance.memGb, Math.min(byRatio, shapeCap, byQuota))
})
const running = computed(() => props.instance.state === 'RUNNING')
const stopped = computed(() => props.instance.state === 'STOPPED')

const transitional = computed(() => !running.value && !stopped.value)

/**
 * 剩余配额文案。
 *
 * 升级号的上限是 Oracle 的哨兵值（一亿），直接相减会显示成
 * "剩余 99999998 OCPU"——一串没人看得懂的数字。
 */
const quotaText = computed(() => {
  const q = props.account.quota
  if (overQuota.value) return '超出该账号剩余配额，无法提交'
  if (q.unlimited.ocpu) return `该账号 ARM 配额不限，当前已用 ${q.ocpuUsed} OCPU / ${q.memUsed} GB`
  if (!q.ocpuLimit) return '未能读取该账号的配额，提交时后端会再校验一次'
  return `账号剩余配额 ${q.ocpuLimit - q.ocpuUsed} OCPU / ${q.memLimit - q.memUsed} GB`
})

/**
 * 块存储性能估算。
 *
 * 之前这里是三个写死的档位，只看 VPU 不看容量——但 OCI 的性能是**按 GB 线性**的。
 * 结果：VPU≥10 恒显示 3,000 IOPS，那其实是 50 GB 时的值，扩到 200 GB 实际
 * 是 12,000，而界面数字纹丝不动。滑块能拖到 120 VPU，档位判断却只到 20，
 * 21–120 全部显示成 20 的值。
 *
 * Oracle 的公式（VPU ≥ 10 时线性）：
 *   IOPS/GB       = 1.5 × VPU + 45      单卷上限 2,500 × VPU
 *   吞吐 KB/s/GB  = 12  × VPU + 360     单卷上限 20 × VPU + 280（MB/s）
 *
 * VPU = 0（低成本档）不在这条线上，单列：2 IOPS/GB、上限 3,000；
 * 240 KB/s per GB、上限 480 MB/s。
 *
 * 来源：https://docs.oracle.com/en-us/iaas/Content/Block/Concepts/blockvolumeperformance.htm
 * 核对日期：2026-08-20
 */
function volumePerf(v: number, gb: number): { iops: number; mbps: number } {
  if (v <= 0) {
    return { iops: Math.min(2 * gb, 3000), mbps: Math.min(240 * gb / 1000, 480) }
  }
  return {
    iops: Math.min((1.5 * v + 45) * gb, 2500 * v),
    mbps: Math.min((12 * v + 360) * gb / 1000, 20 * v + 280)
  }
}

const perf = computed(() => volumePerf(vpu.value, size.value))
const iops = computed(() => Math.round(perf.value.iops).toLocaleString('en-US'))
const throughput = computed(() => `${Math.round(perf.value.mbps)} MB/s`)

/** 配额上限为 0 表示读不到，此时不拦——总比因为读不到就完全用不了强。 */
const overQuota = computed(() => {
  const q = props.account.quota
  // 升级号的上限是 Oracle 的哨兵值（一亿），谈不上"超额"。
  if (q.unlimited.ocpu) return false
  if (!q.ocpuLimit) return false
  const dOcpu = ocpu.value - props.instance.ocpu
  const dMem = mem.value - props.instance.memGb
  return q.ocpuUsed + dOcpu > q.ocpuLimit || q.memUsed + dMem > q.memLimit
})

const volumeChanged = computed(() =>
  size.value !== props.instance.bootGb || vpu.value !== props.instance.vpu
)
const shapeChanged = computed(() =>
  ocpu.value !== props.instance.ocpu || mem.value !== props.instance.memGb
)

// 实例被外部刷新（同步或 SSE 落定）后，滑块要跟着回到新值。
watch(() => [props.instance.bootGb, props.instance.vpu, props.instance.ocpu, props.instance.memGb], () => {
  size.value = props.instance.bootGb
  vpu.value = props.instance.vpu
  ocpu.value = props.instance.ocpu
  mem.value = props.instance.memGb
})

function onOcpu(v: number) {
  ocpu.value = v
  // 内存跟着 OCPU 的比例回夹。比例取自规格元数据，不再写死 A1 的 6:1。
  const cap = v * maxPerOcpu.value
  if (mem.value > cap) mem.value = cap
}

/**
 * 引导卷滑块上限。
 *
 * 原先写死 200 —— 那是永久免费的块存储**总额**，不是单卷上限。
 * 升级号被无理由地卡在 200 GB，免费号那边又管不住"几台加起来"。
 * 现在按账号剩余配额算；读不到或不设上限时退回界面刻度上限
 * （OCI 单卷真实上限 32 TB，做成滑块没法用）。
 */
const SLIDER_MAX_BOOT_GB = 1024

const maxBootGb = computed(() => {
  const q = props.account.quota
  if (q.unlimited.block || !q.blockLimit) return SLIDER_MAX_BOOT_GB
  const remaining = Math.max(0, q.blockLimit - q.blockUsed)
  // 当前容量本身已经占着配额，扩容能到的上限是"当前 + 剩余"。
  return Math.max(props.instance.bootGb,
    Math.min(SLIDER_MAX_BOOT_GB, props.instance.bootGb + remaining))
})

function applyVolume() {
  if (!props.bootVolumeId) return

  const grew = size.value > props.instance.bootGb
  ask({
    level: 2,
    title: `修改 ${props.instance.name} 的引导卷`,
    body: grew
      ? `从 ${props.instance.bootGb} GB 扩到 ${size.value} GB。容量只能增不能减，扩容后还需要在实例内扩展分区与文件系统才会生效。`
      : `将 VPU 从 ${props.instance.vpu} 调整为 ${vpu.value}。性能档位变更会在后台生效，无需重启。`,
    okLabel: '应用变更',
    onConfirm: async () => {
      applyingVolume.value = true
      try {
        const { notice } = await storage.updateBootVolume(
          props.bootVolumeId!, props.instance.accountId, props.instance.region,
          {
            sizeInGbs: size.value !== props.instance.bootGb ? size.value : undefined,
            vpusPerGb: vpu.value !== props.instance.vpu ? vpu.value : undefined
          }
        )
        await refreshInstances()
        toast({
          tone: 'success',
          title: `${props.instance.name} 的引导卷已更新`,
          body: notice || undefined,
          command: grew ? 'sudo /usr/libexec/oci-growfs -y' : undefined
        }, 10000)
      } catch (err) {
        toastError('修改引导卷失败', err)
        size.value = props.instance.bootGb
        vpu.value = props.instance.vpu
      } finally {
        applyingVolume.value = false
      }
    }
  })
}

function applyShape() {
  ask({
    level: 2,
    title: `修改 ${props.instance.name} 的配置`,
    body: `${props.instance.ocpu} OCPU / ${props.instance.memGb} GB → ${ocpu.value} OCPU / ${mem.value} GB。` +
      (running.value
        ? '实例正在运行，Oracle 会重启它一次来应用变更，期间服务中断。'
        : '实例已关机，下次开机时按新配置启动。'),
    okLabel: '应用配置',
    onConfirm: async () => {
      applyingShape.value = true
      try {
        const { instances } = await import('@/api')
        await instances.reshape(props.instance.id, ocpu.value, mem.value)
        await refreshInstances()
        toast({ tone: 'success', title: `${props.instance.name} 配置已更新` })
      } catch (err) {
        toastError('修改配置失败', err)
        ocpu.value = props.instance.ocpu
        mem.value = props.instance.memGb
      } finally {
        applyingShape.value = false
      }
    }
  })
}
</script>

<template>
  <SectionCard title="引导卷" :note="`${instance.bootGb} GB · VPU ${instance.vpu} · 容量只能增不能减`">
    <div class="pad">
      <label class="slider">
        <span class="slider__head"><span>容量</span><span class="mono">{{ size }} GB</span></span>
        <input type="range" :min="instance.bootGb" :max="maxBootGb" step="1" v-model.number="size" />
      </label>
      <label class="slider">
        <span class="slider__head"><span>VPU</span><span class="mono">{{ vpu }}</span></span>
        <input type="range" min="0" max="120" step="10" v-model.number="vpu" />
        <span class="slider__hint mono">预估 {{ iops }} IOPS · {{ throughput }}</span>
      </label>
      <QuotaMeter v-if="account.quota.blockLimit" label="块存储配额"
                  :used="account.quota.blockUsed + (size - instance.bootGb)"
                  :limit="account.quota.blockLimit" unit=" GB" />
      <CheckList v-if="!bootVolumeId"
                 :items="[{ tone: 'info', text: '尚未同步到该实例的引导卷信息，先执行一次同步再来。' }]" />
      <button class="btn btn--sm" :disabled="!volumeChanged || applyingVolume || !bootVolumeId"
              @click="applyVolume()">
        {{ applyingVolume ? '提交中…' : '应用引导卷变更' }}
      </button>
    </div>
  </SectionCard>

  <SectionCard title="修改配置" note="OCPU 与内存联动">
    <div class="pad">
      <label class="slider">
        <span class="slider__head"><span>OCPU</span><span class="mono">{{ ocpu }}</span></span>
        <input type="range" min="1" :max="maxOcpu" step="1" :value="ocpu"
               :disabled="!isFlexible"
               @input="onOcpu(Number(($event.target as HTMLInputElement).value))" />
      </label>
      <label class="slider">
        <span class="slider__head"><span>内存</span><span class="mono">{{ mem }} GB</span></span>
        <input type="range" min="1" :max="maxMem" step="1" v-model.number="mem" :disabled="!isFlexible" />
        <span class="slider__hint">{{ isFlexible
          ? `${instance.shape} 约束：每 OCPU 最多 ${maxPerOcpu} GB`
          : '该规格为固定配置，无法调整' }}</span>
      </label>
      <CheckList :items="[
        running
          ? { tone: 'warn', text: '实例正在运行，Oracle 会在应用变更时重启它一次。' }
          : stopped
            ? { tone: 'ok', text: '实例已关机，变更会在下次开机时生效。' }
            : { tone: 'info', text: '实例正在转换状态，等落定后再操作。' },
        { tone: overQuota ? 'fail' : 'info', text: quotaText }
      ]" />
      <button class="btn btn--sm"
              :disabled="!shapeChanged || transitional || overQuota || applyingShape || !isFlexible"
              @click="applyShape()">
        {{ applyingShape ? '提交中…' : '应用配置变更' }}
      </button>
    </div>
  </SectionCard>
</template>

<style scoped>
.pad { padding: 14px 16px; display: flex; flex-direction: column; gap: 14px; }
.slider { display: flex; flex-direction: column; gap: 8px; }
.slider__head { display: flex; justify-content: space-between; font-size: 12px; color: var(--text-secondary); }
.slider__head .mono { color: var(--text-primary); }
.slider__hint { font-size: 11px; color: var(--text-tertiary); }
input[type='range'] { width: 100%; accent-color: var(--accent); }
input[type='range']:disabled { opacity: 0.4; }
</style>
