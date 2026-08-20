<script setup lang="ts">
/**
 * 容量监控。
 *
 * 数据来自 Oracle 官方的容量报告接口——只读，不创建任何资源、不消耗配额。
 * 和"守候"（反复调创建接口去撞）是两个性质，所以这一页可以放心地定期刷。
 *
 * 页面上必须反复讲清楚一件事：报告说有容量 ≠ 一定能开。它反映的是宿主机池
 * 的整体状态，真正的分配还要看那一瞬间的争抢。把它写成"抢到了"会得到一个
 * 总是骗人的界面。
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import { relativeTime } from '@/lib/adapt'
import {
  capacity as capApi, errorText,
  type CapacityWatchDTO, type CapacityProbeResult
} from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import CheckList from '@/components/CheckList.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import AccountChip from '@/components/AccountChip.vue'

const { state, accountById, ask, toast, toastError } = useStore()

/* ---------- 监控项 ---------- */

const watches = ref<CapacityWatchDTO[]>([])
const probeInterval = ref(300)
const loading = ref(false)
const loadError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await capApi.list()
    watches.value = res.watches
    probeInterval.value = res.probeIntervalSeconds
  } catch (err) {
    loadError.value = errorText(err)
  } finally {
    loading.value = false
  }
}

// 后台每 5 分钟查一轮，页面 30 秒刷一次就够跟上了。
let timer = 0
onMounted(() => {
  void load()
  timer = window.setInterval(() => {
    if (!document.hidden) void load()
  }, 30_000)
})
onUnmounted(() => window.clearInterval(timer))

/* ---------- 手动查询 ---------- */

const usableAccounts = computed(() =>
  state.accounts.filter(a => a.status !== 'error' && a.status !== 'disabled'))

const form = ref({
  accountId: usableAccounts.value[0]?.id ?? '',
  shape: 'VM.Standard.A1.Flex',
  ocpus: 2,
  memoryGb: 12
})

const accountGroups = computed(() => [{
  label: '账号',
  options: usableAccounts.value.map(a => ({
    value: a.id, label: `${a.code} · ${a.alias}`, dot: acctColor(a.colorIndex)
  }))
}])

/** 只给这两个规格的快捷项：其余规格自己填，免费额度里也就这两种值得盯。 */
const shapeGroups = [{
  label: '规格',
  options: [
    { value: 'VM.Standard.A1.Flex', label: 'A1.Flex（ARM）' },
    { value: 'VM.Standard.E2.1.Micro', label: 'E2.1.Micro（AMD）' }
  ]
}]

/** Micro 是固定规格，带 ShapeConfig 会被 OCI 拒。 */
const isFlexible = computed(() => form.value.shape.includes('Flex'))

const probing = ref(false)
const probeResult = ref<CapacityProbeResult | null>(null)
const probeError = ref('')

async function probe() {
  if (!form.value.accountId) return
  probing.value = true
  probeError.value = ''
  probeResult.value = null
  try {
    probeResult.value = await capApi.probe({
      accountId: form.value.accountId,
      shape: form.value.shape,
      ocpus: isFlexible.value ? form.value.ocpus : undefined,
      memoryGb: isFlexible.value ? form.value.memoryGb : undefined
    })
  } catch (err) {
    probeError.value = errorText(err)
  } finally {
    probing.value = false
  }
}

/** 把手动查出来的某个 AD 加入持续监控。 */
async function watchThis(ad: string) {
  try {
    const res = await capApi.create({
      accountId: form.value.accountId,
      region: probeResult.value?.region,
      availabilityDomain: ad,
      shape: form.value.shape,
      ocpus: isFlexible.value ? form.value.ocpus : undefined,
      memoryGb: isFlexible.value ? form.value.memoryGb : undefined
    })
    toast({ tone: 'success', title: '已加入监控', body: res.notice })
    await load()
  } catch (err) {
    toastError('加入监控失败', err)
  }
}

const alreadyWatched = (ad: string) =>
  watches.value.some(w =>
    w.accountId === form.value.accountId &&
    w.availabilityDomain === ad &&
    w.shape === form.value.shape)

/* ---------- 监控项操作 ---------- */

async function toggle(w: CapacityWatchDTO) {
  try {
    const res = w.enabled ? await capApi.disable(w.id) : await capApi.enable(w.id)
    const i = watches.value.findIndex(x => x.id === w.id)
    if (i >= 0) watches.value[i] = res.watch
  } catch (err) {
    toastError('操作失败', err)
  }
}

function remove(w: CapacityWatchDTO) {
  ask({
    level: 2,
    title: `删除监控「${w.shape} · ${w.availabilityDomainShort}」`,
    body: '删除后不再查询这一项，也不会再收到它的容量变化通知。',
    okLabel: '删除',
    onConfirm: async () => {
      try {
        await capApi.remove(w.id)
        watches.value = watches.value.filter(x => x.id !== w.id)
        toast({ tone: 'success', title: '已删除' })
      } catch (err) {
        toastError('删除失败', err)
      }
    }
  })
}

/* ---------- 展示 ---------- */

const STATUS_TONE: Record<string, string> = {
  AVAILABLE: 'var(--success)',
  OUT_OF_HOST_CAPACITY: 'var(--text-3)',
  HARDWARE_NOT_SUPPORTED: 'var(--warning)'
}

const toneOf = (status: string) => STATUS_TONE[status] ?? 'var(--text-3)'

function checkedText(w: CapacityWatchDTO): string {
  if (!w.lastCheckedAt || w.lastCheckedAt.startsWith('1970')) return '尚未查询'
  // relativeTime 自己读全局时钟，不用再传时间进去
  return relativeTime(w.lastCheckedAt) + '查询'
}

const availableCount = computed(() =>
  watches.value.filter(w => w.lastStatus === 'AVAILABLE').length)
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">容量监控</h1>
        <p class="page-sub">
          Oracle 官方容量报告 · {{ watches.length }} 项监控，{{ availableCount }} 项有容量
        </p>
      </div>
      <div class="head-actions">
        <button class="btn" :disabled="loading" @click="load()">刷新</button>
      </div>
    </header>

    <p v-if="loadError" class="load-err">{{ loadError }}</p>

    <CheckList class="warn-box" :items="[
      { tone: 'info',
        text: 'Oracle 官方的只读接口，不创建资源、不消耗配额。' },
      { tone: 'warn',
        text: '显示有容量时创建仍可能失败——它反映宿主机池的整体状态，不是那一瞬间的分配结果。当作「值得一试」，不是「一定能开」。' },
      { tone: 'info',
        text: '只能查已订阅的区域。免费号的实例只能开在 home region，通常也订阅不了第二个区域。' }
    ]" />

    <!-- 手动查询放在最前：先让人点一下看看返回对不对，
         再决定要不要把持续监控打开 -->
    <SectionCard title="立即查询" note="查完可以把某个可用域加入持续监控">
      <div class="pad">
        <div class="probe">
          <SelectMenu v-model="form.accountId" :groups="accountGroups"
                      placeholder="选择账号" aria-label="账号" :min-width="200" />
          <SelectMenu v-model="form.shape" :groups="shapeGroups"
                      placeholder="选择规格" aria-label="规格" :min-width="200" />
          <template v-if="isFlexible">
            <label class="probe__num">
              <span class="t-2xs dim-3">OCPU</span>
              <input v-model.number="form.ocpus" class="input mono" type="number" min="1" max="64" />
            </label>
            <label class="probe__num">
              <span class="t-2xs dim-3">内存 GB</span>
              <input v-model.number="form.memoryGb" class="input mono" type="number" min="1" max="512" />
            </label>
          </template>
          <button class="btn btn--primary" :disabled="probing || !form.accountId"
                  @click="probe()">{{ probing ? '查询中…' : '查询' }}</button>
        </div>

        <p v-if="probeError" class="t-xs probe__err">{{ probeError }}</p>

        <div v-if="probeResult" class="results">
          <p class="t-2xs dim-3 results__head">
            {{ probeResult.region }} · {{ probeResult.shape }}
          </p>
          <div v-for="r in probeResult.results" :key="r.availabilityDomain" class="result">
            <span class="mono t-xs result__ad">{{ r.short }}</span>
            <span class="t-xs result__st" :style="{ color: toneOf(r.status) }">
              {{ r.statusText }}
            </span>
            <span v-if="r.availableCount > 0" class="mono t-2xs dim-3">
              可用 {{ r.availableCount }}
            </span>
            <span v-if="r.error" class="mono t-2xs result__err">{{ r.error }}</span>
            <span class="results__spacer" />
            <button v-if="!r.error" class="btn btn--xs"
                    :disabled="alreadyWatched(r.availabilityDomain)"
                    @click="watchThis(r.availabilityDomain)">
              {{ alreadyWatched(r.availabilityDomain) ? '已监控' : '加入监控' }}
            </button>
          </div>
        </div>
      </div>
    </SectionCard>

    <SectionCard title="持续监控"
                 :note="`每 ${Math.round(probeInterval / 60)} 分钟复查一次 · 状态变化时才推通知`">
      <SkeletonRows v-if="loading && watches.length === 0" :rows="3" />

      <EmptyState v-else-if="watches.length === 0" title="还没有监控项"
                  body="先在上面查一次，再把关心的可用域加入监控。有容量时推通知。" />

      <div v-for="w in watches" v-else :key="w.id" class="watch"
           :class="{ 'is-off': !w.enabled }">
        <span class="watch__bar" :style="{ background: acctColor(accountById(w.accountId).colorIndex) }" />

        <span class="watch__dot" :style="{ background: toneOf(w.lastStatus) }" />
        <span class="watch__status" :style="{ color: toneOf(w.lastStatus) }">
          {{ w.statusText }}
        </span>

        <AccountChip :account="accountById(w.accountId)" />
        <span class="mono t-xs watch__shape">{{ w.shape }}</span>
        <span v-if="w.ocpus > 0" class="mono t-2xs dim-3">{{ w.ocpus }}C / {{ w.memoryGb }}G</span>
        <span class="mono t-2xs dim-3">{{ w.region }} · {{ w.availabilityDomainShort }}</span>

        <span class="watch__spacer" />

        <span v-if="w.lastError" class="mono t-2xs watch__err" :title="w.lastError">
          {{ w.lastError }}
        </span>
        <span class="mono t-2xs dim-3">{{ checkedText(w) }}</span>

        <button class="btn btn--xs" @click="toggle(w)">{{ w.enabled ? '暂停' : '启用' }}</button>
        <button class="btn btn--xs btn--warning" @click="remove(w)">删除</button>
      </div>
    </SectionCard>
  </div>
</template>

<style scoped>
.warn-box { margin-bottom: 16px; }

/* .pad 是各视图各自定义的工具类，不是全局的。这里用了却没定义过，
   于是整块查询表单直接贴在卡片边框上——标签压着上边框，看着像渲染坏了。 */
.pad { padding: 16px 20px; }

.page > .card + .card { margin-top: 16px; }

.probe { display: flex; align-items: flex-end; gap: 12px; flex-wrap: wrap; }
.probe__num { display: flex; flex-direction: column; gap: 5px; }
.probe__num .input { width: 92px; }
.probe__err { color: var(--danger); margin-top: 12px; line-height: 1.7; }

/* 结果区和上面的表单之间要有明确的分隔，否则查询条件和查询结果糊成一片 */
.results { margin-top: 18px; padding-top: 4px; border-top: 1px solid var(--border-subtle); }
.results__head { padding: 12px 0 8px; }
.results__spacer { flex: 1 1 auto; min-width: 12px; }

.result {
  display: flex; align-items: center; gap: 12px;
  height: 42px; border-bottom: 1px solid var(--border-subtle);
}
.result:last-child { border-bottom: none; }
.result__ad { width: 56px; flex: 0 0 auto; font-weight: 600; }
.result__st { font-weight: 600; }
/* 错误原文可能很长，截断而不是撑破整行 */
.result__err {
  color: var(--danger); min-width: 0;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.watch {
  position: relative;
  display: flex; align-items: center; gap: 12px;
  height: 46px; padding: 0 16px 0 17px;
  border-bottom: 1px solid var(--border-subtle);
}
.watch:last-child { border-bottom: none; }
/* 停用的项压暗但不隐藏——藏起来用户会以为被删了 */
.watch.is-off { opacity: 0.5; }

.watch__bar { position: absolute; left: 0; top: 0; bottom: 0; width: 3px; }
.watch__dot { width: 7px; height: 7px; border-radius: var(--radius-full); flex: 0 0 auto; }
.watch__status { font-size: var(--t-2xs); font-weight: 600; width: 104px; flex: 0 0 auto; }
.watch__shape { font-weight: 600; }
.watch__spacer { flex: 1 1 auto; min-width: 8px; }
.watch__err {
  color: var(--danger); max-width: 260px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
</style>
