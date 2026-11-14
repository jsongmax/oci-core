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
import { t } from '@/i18n'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
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
import LoadingBar from '@/components/LoadingBar.vue'
import CheckList from '@/components/CheckList.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import AccountChip from '@/components/AccountChip.vue'

const { state, accountById, ask, toast, toastError } = useStore()

/* ---------- 监控项 ---------- */

const watches = ref<CapacityWatchDTO[]>([])
const probeInterval = ref(300)
const loading = ref(false)
const loadError = ref('')

/**
 * 手动刷新。只有点「刷新」才调暗列表、走进度条；定时轮询和操作后的重拉
 * 保持安静，否则每隔十几秒整页闪一下。
 */
const refreshing = ref(false)
async function refresh() {
  refreshing.value = true
  try { await load() } finally { refreshing.value = false }
}

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
  label: t.capNotify.cap.account,
  options: usableAccounts.value.map(a => ({
    value: a.id, label: `${a.code} · ${a.alias}`, dot: acctColor(a.colorIndex)
  }))
}])

/** 只给这两个规格的快捷项：其余规格自己填，免费额度里也就这两种值得盯。 */
const shapeGroups = computed(() => [{
  label: t.capNotify.cap.shape,
  options: [
    { value: 'VM.Standard.A1.Flex', label: 'A1.Flex（ARM）' },
    { value: 'VM.Standard.E2.1.Micro', label: 'E2.1.Micro（AMD）' }
  ]
}])

/** Micro 是固定规格，带 ShapeConfig 会被 OCI 拒。 */
const isFlexible = computed(() => form.value.shape.includes('Flex'))

const probing = ref(false)
const probeResult = ref<CapacityProbeResult | null>(null)
const probeError = ref('')

/**
 * 查出这份结果时用的参数。
 *
 * 结果里的区域和可用域只属于当时那个账号。查完再把下拉框切到别的账号，
 * 「加入监控」如果读的是表单当前值，就会建出一个「B 账号 + A 的可用域」的
 * 任务——它会一直拿别人租户的可用域去轮询 Oracle，次次失败。
 */
const probedWith = ref<{ accountId: string; shape: string; ocpus?: number; memoryGb?: number } | null>(null)

/** 查询代次。查询途中换了账号或规格，回来的结果直接作废。 */
let probeSeq = 0

async function probe() {
  if (!form.value.accountId) return
  const params = {
    accountId: form.value.accountId,
    shape: form.value.shape,
    ocpus: isFlexible.value ? form.value.ocpus : undefined,
    memoryGb: isFlexible.value ? form.value.memoryGb : undefined
  }
  probing.value = true
  probeError.value = ''
  probeResult.value = null
  probedWith.value = null
  const seq = ++probeSeq
  try {
    const res = await capApi.probe(params)
    if (seq !== probeSeq) return
    probeResult.value = res
    probedWith.value = params
  } catch (err) {
    if (seq === probeSeq) probeError.value = errorText(err)
  } finally {
    if (seq === probeSeq) probing.value = false
  }
}

// 换了账号或规格，旧结果就不再对应表单：收起它，在途的查询也作废，
// 避免误点「加入监控」。
watch(() => [form.value.accountId, form.value.shape], () => {
  probeSeq++
  probing.value = false
  probeResult.value = null
  probedWith.value = null
  probeError.value = ''
})

/** 把手动查出来的某个 AD 加入持续监控。 */
async function watchThis(ad: string) {
  const p = probedWith.value
  if (!p || !probeResult.value) return
  try {
    const res = await capApi.create({
      accountId: p.accountId,
      region: probeResult.value.region,
      availabilityDomain: ad,
      shape: p.shape,
      ocpus: p.ocpus,
      memoryGb: p.memoryGb
    })
    toast({ tone: 'success', title: t.capNotify.cap.added, body: res.notice })
    await load()
  } catch (err) {
    toastError(t.capNotify.cap.addFailed, err)
  }
}

const alreadyWatched = (ad: string) =>
  !!probedWith.value && watches.value.some(w =>
    w.accountId === probedWith.value!.accountId &&
    w.availabilityDomain === ad &&
    w.shape === probedWith.value!.shape)

/* ---------- 监控项操作 ---------- */

async function toggle(w: CapacityWatchDTO) {
  try {
    const res = w.enabled ? await capApi.disable(w.id) : await capApi.enable(w.id)
    const i = watches.value.findIndex(x => x.id === w.id)
    if (i >= 0) watches.value[i] = res.watch
  } catch (err) {
    toastError(t.capNotify.cap.actionFailed, err)
  }
}

function remove(w: CapacityWatchDTO) {
  ask({
    level: 2,
    title: t.capNotify.cap.deleteTitle(`${w.shape} · ${w.availabilityDomainShort}`),
    body: t.capNotify.cap.deleteBody,
    okLabel: t.common.delete,
    onConfirm: async () => {
      try {
        await capApi.remove(w.id)
        watches.value = watches.value.filter(x => x.id !== w.id)
        toast({ tone: 'success', title: t.capNotify.cap.deleted })
      } catch (err) {
        toastError(t.capNotify.cap.deleteFailed, err)
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
  if (!w.lastCheckedAt || w.lastCheckedAt.startsWith('1970')) return t.capNotify.cap.neverChecked
  // relativeTime 自己读全局时钟，不用再传时间进去
  return t.capNotify.cap.checked(relativeTime(w.lastCheckedAt))
}

const availableCount = computed(() =>
  watches.value.filter(w => w.lastStatus === 'AVAILABLE').length)
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">{{ t.capNotify.cap.title }}</h1>
        <p class="page-sub">
          {{ t.capNotify.cap.subtitle(watches.length, availableCount) }}
        </p>
      </div>
      <div class="head-actions">
        <button class="btn" :disabled="refreshing" @click="refresh()">
          <span v-if="refreshing" class="btn-spin" aria-hidden="true" />{{ t.common.refresh }}
        </button>
      </div>
    </header>
    <LoadingBar :active="refreshing" />

    <p v-if="loadError" class="load-err">{{ loadError }}</p>

    <CheckList class="warn-box" :items="[
      { tone: 'info',
        text: t.capNotify.cap.noteReadonly },
      { tone: 'warn',
        text: t.capNotify.cap.noteMayFail },
      { tone: 'info',
        text: t.capNotify.cap.noteSubscribed }
    ]" />

    <!-- 手动查询放在最前：先让人点一下看看返回对不对，
         再决定要不要把持续监控打开 -->
    <SectionCard :title="t.capNotify.cap.probeTitle" :note="t.capNotify.cap.probeNote">
      <div class="pad">
        <div class="probe">
          <SelectMenu v-model="form.accountId" :groups="accountGroups"
                      :placeholder="t.capNotify.cap.pickAccount" :aria-label="t.capNotify.cap.account" :min-width="200" />
          <SelectMenu v-model="form.shape" :groups="shapeGroups"
                      :placeholder="t.capNotify.cap.pickShape" :aria-label="t.capNotify.cap.shape" :min-width="200" />
          <template v-if="isFlexible">
            <label class="probe__num">
              <span class="t-2xs dim-3">OCPU</span>
              <input v-model.number="form.ocpus" class="input mono" type="number" min="1" max="64" />
            </label>
            <label class="probe__num">
              <span class="t-2xs dim-3">{{ t.capNotify.cap.memGb }}</span>
              <input v-model.number="form.memoryGb" class="input mono" type="number" min="1" max="512" />
            </label>
          </template>
          <button class="btn btn--primary" :disabled="probing || !form.accountId"
                  @click="probe()">{{ probing ? t.capNotify.cap.probing : t.capNotify.cap.probe }}</button>
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
              {{ t.capNotify.cap.available(r.availableCount) }}
            </span>
            <span v-if="r.error" class="mono t-2xs result__err">{{ r.error }}</span>
            <span class="results__spacer" />
            <button v-if="!r.error" class="btn btn--xs"
                    :disabled="alreadyWatched(r.availabilityDomain)"
                    @click="watchThis(r.availabilityDomain)">
              {{ alreadyWatched(r.availabilityDomain) ? t.capNotify.cap.watched : t.capNotify.cap.watch }}
            </button>
          </div>
        </div>
      </div>
    </SectionCard>

    <SectionCard :title="t.capNotify.cap.watchTitle" class="stale-able" :class="{ 'is-stale': refreshing && watches.length > 0 }"
                 :note="t.capNotify.cap.watchNote(Math.round(probeInterval / 60))">
      <SkeletonRows v-if="loading && watches.length === 0" :rows="3" />

      <EmptyState v-else-if="watches.length === 0" :title="t.capNotify.cap.emptyTitle"
                  :body="t.capNotify.cap.emptyBody" />

      <div v-for="(w, n) in watches" v-else :key="w.id" class="watch reveal" :style="{ '--i': Math.min(n, 8) }"
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

        <button class="btn btn--xs" @click="toggle(w)">{{ w.enabled ? t.capNotify.cap.pause : t.capNotify.cap.resume }}</button>
        <button class="btn btn--xs btn--warning" @click="remove(w)">{{ t.common.delete }}</button>
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
