<script setup lang="ts">
/** §6.8 ShapeConfigurator + 引导卷扩容/VPU：滑块联动 + 配额校验 + 前置提示 */
import { computed, ref, watch } from 'vue'
import type { Account, Instance } from '@/types'
import { useStore } from '@/store'
import { storage } from '@/api'
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

const isArm = computed(() => props.instance.shape.includes('A1.Flex'))
const maxMem = computed(() => (isArm.value ? ocpu.value * 6 : mem.value))
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

const iops = computed(() => (vpu.value >= 20 ? '25,000' : vpu.value >= 10 ? '3,000' : '600'))
const throughput = computed(() => (vpu.value >= 20 ? '480 MB/s' : vpu.value >= 10 ? '160 MB/s' : '30 MB/s'))

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
  if (mem.value > v * 6) mem.value = v * 6
}

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
        <input type="range" :min="instance.bootGb" max="200" step="1" v-model.number="size" />
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
        <input type="range" min="1" :max="isArm ? 4 : 1" step="1" :value="ocpu"
               :disabled="!isArm"
               @input="onOcpu(Number(($event.target as HTMLInputElement).value))" />
      </label>
      <label class="slider">
        <span class="slider__head"><span>内存</span><span class="mono">{{ mem }} GB</span></span>
        <input type="range" min="1" :max="maxMem" step="1" v-model.number="mem" :disabled="!isArm" />
        <span class="slider__hint">{{ isArm ? 'A1.Flex 约束：每 OCPU 最多 6 GB' : '该规格为固定配置，无法调整' }}</span>
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
              :disabled="!shapeChanged || transitional || overQuota || applyingShape || !isArm"
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
