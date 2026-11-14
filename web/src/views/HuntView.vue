<script setup lang="ts">
/**
 * 容量守候（抢机）。
 *
 * 这个页面必须把"为什么还没抢到"讲清楚。任务在后台跑，用户唯一的信息来源
 * 就是这张表——不显示尝试次数、上次的错误和下次的时间，它就是个黑盒，
 * 用户只能反复问"到底在不在跑"，然后去点重启。
 */
import { t } from '@/i18n'
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import { shortAd } from '@/lib/adapt'
import { now } from '@/lib/clock'
import { hunt as huntApi, errorText, type HuntTaskDTO, type HuntLimitsDTO } from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import LoadingBar from '@/components/LoadingBar.vue'
import CheckList from '@/components/CheckList.vue'
import AccountChip from '@/components/AccountChip.vue'

const { accountById, openDrawer, ask, toast, toastError } = useStore()

const tasks = ref<HuntTaskDTO[]>([])
const limits = ref<HuntLimitsDTO | null>(null)
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
    const res = await huntApi.list()
    tasks.value = res.tasks
    limits.value = res.limits
  } catch (err) {
    loadError.value = errorText(err)
  } finally {
    loading.value = false
  }
}

// 任务在后台推进，页面不刷新就永远停在打开那一刻的数字。
// 15 秒一轮：调度器 tick 是 10 秒，比它慢一点足够，再密只是白打接口。
let timer = 0
onMounted(() => {
  void load()
  timer = window.setInterval(() => {
    if (!document.hidden) void load()
  }, 15_000)
})
onUnmounted(() => window.clearInterval(timer))

/* ---------- 展示 ---------- */


const STATE_TONE: Record<string, string> = {
  running: 'var(--accent)', paused: 'var(--warning)',
  succeeded: 'var(--success)', failed: 'var(--danger)', pending: 'var(--text-3)'
}

/** 错误分类映射成人话。原始分类名同时保留在 title 里，方便搜索。 */
const classLabel = (c: string) => t.overviewHunt.hunt.cls[c] ?? c

/** 距下次尝试还有多久。读全局时钟，所以会自己走。 */
function countdown(task: HuntTaskDTO): string {
  if (task.state !== 'running') return '—'
  const ms = new Date(task.nextAt).getTime() - now.value
  if (Number.isNaN(ms)) return '—'
  if (ms <= 0) return t.overviewHunt.hunt.soon
  const s = Math.round(ms / 1000)
  if (s < 60) return t.overviewHunt.hunt.inSec(s)
  const m = Math.floor(s / 60)
  return m < 60 ? t.overviewHunt.hunt.inMinSec(m, s % 60) : t.overviewHunt.hunt.inHourMin(Math.floor(m / 60), m % 60)
}

function expiryText(task: HuntTaskDTO): string {
  const ms = new Date(task.expiresAt).getTime() - now.value
  if (Number.isNaN(ms) || ms <= 0) return t.overviewHunt.hunt.expired
  const h = Math.round(ms / 3_600_000)
  return h < 24 ? t.overviewHunt.hunt.expiresHours(h) : t.overviewHunt.hunt.expiresDays(Math.round(h / 24))
}

const shapeText = (t: HuntTaskDTO) =>
  t.ocpus > 0 ? `${t.shape} · ${t.ocpus} OCPU / ${t.memoryGb} GB` : t.shape

const running = computed(() => tasks.value.filter(t => t.state === 'running').length)

/* ---------- 操作 ---------- */

async function toggle(task: HuntTaskDTO) {
  try {
    const res = task.state === 'running'
      ? await huntApi.pause(task.id)
      : await huntApi.resume(task.id)
    const i = tasks.value.findIndex(t => t.id === task.id)
    if (i >= 0) tasks.value[i] = res.task
  } catch (err) {
    toastError(t.overviewHunt.hunt.actionFailed, err)
  }
}

function remove(task: HuntTaskDTO) {
  ask({
    level: 2,
    title: t.overviewHunt.hunt.deleteTitle(task.name),
    body: t.overviewHunt.hunt.deleteBody,
    okLabel: t.overviewHunt.hunt.deleteTask,
    onConfirm: async () => {
      try {
        const res = await huntApi.remove(task.id)
        tasks.value = tasks.value.filter(t => t.id !== task.id)
        toast({ tone: 'success', title: t.overviewHunt.hunt.deleted, body: res.notice })
      } catch (err) {
        toastError(t.overviewHunt.hunt.deleteFailed, err)
      }
    }
  })
}

function openInstance(id: string) {
  openDrawer({ kind: 'instance', id, tab: 'overview' })
}
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">{{ t.overviewHunt.hunt.title }}</h1>
        <p class="page-sub">
          {{ t.overviewHunt.hunt.subtitle(tasks.length, running) }}
        </p>
      </div>
      <div class="head-actions">
        <button class="btn" :disabled="refreshing" @click="refresh()">
          <span v-if="refreshing" class="btn-spin" aria-hidden="true" />{{ t.common.refresh }}
        </button>
        <button class="btn btn--primary" @click="openDrawer({ kind: 'create-instance' })">
          {{ t.overviewHunt.hunt.newTask }}
        </button>
      </div>
    </header>
    <LoadingBar :active="refreshing" />

    <p v-if="loadError" class="load-err">{{ loadError }}</p>

    <!-- 风险必须在页面上，而不是只写在文档里 -->
    <CheckList class="warn-box" :items="[
      { tone: 'warn',
        text: t.overviewHunt.hunt.noteThrottle(limits?.minIntervalSeconds ?? 30) },
      { tone: 'info',
        text: t.overviewHunt.hunt.noteWindow },
      { tone: 'info',
        text: t.overviewHunt.hunt.noteOnePerAccount }
    ]" />

    <SectionCard :title="t.overviewHunt.hunt.tasks" :note="t.overviewHunt.hunt.autoRefresh" class="stale-able" :class="{ 'is-stale': refreshing && tasks.length > 0 }">
      <SkeletonRows v-if="loading && tasks.length === 0" :rows="3" />

      <EmptyState v-else-if="tasks.length === 0" :title="t.overviewHunt.hunt.emptyTitle"
                  :body="t.overviewHunt.hunt.emptyBody"
                  :action-label="t.overviewHunt.hunt.newTask"
                  @action="openDrawer({ kind: 'create-instance' })" />

      <div v-for="(tk, n) in tasks" v-else :key="tk.id" class="task reveal" :style="{ '--i': Math.min(n, 8) }">
        <span class="task__bar" :style="{ background: acctColor(accountById(tk.accountId).colorIndex) }" />

        <div class="task__main">
          <div class="task__line1">
            <span class="task__state" :style="{ color: STATE_TONE[tk.state] }">
              ● {{ t.overviewHunt.hunt.state[tk.state] ?? tk.state }}
            </span>
            <span class="task__name">{{ tk.name }}</span>
            <AccountChip :account="accountById(tk.accountId)" />
            <span class="mono t-2xs dim-3">{{ shapeText(tk) }}</span>
            <span class="mono t-2xs dim-3">{{ tk.region }}</span>
          </div>

          <div class="task__line2">
            <span class="mono t-2xs dim-3">{{ t.overviewHunt.hunt.attempts(tk.attempts) }}</span>
            <span v-if="tk.lastAd" class="mono t-2xs dim-3">{{ t.overviewHunt.hunt.current(shortAd(tk.lastAd)) }}</span>
            <span class="mono t-2xs dim-3">{{ t.overviewHunt.hunt.every(tk.intervalSeconds) }}</span>
            <!-- 开没开预检直接决定这个任务发多少创建请求，必须能一眼看到 -->
            <span class="mono t-2xs" :style="{ color: tk.precheckCapacity ? 'var(--success)' : 'var(--warning)' }"
                  :title="tk.precheckCapacity
                    ? t.overviewHunt.hunt.precheckOn
                    : t.overviewHunt.hunt.precheckOff">
              {{ tk.precheckCapacity ? t.overviewHunt.hunt.precheck : t.overviewHunt.hunt.noPrecheck }}
            </span>
            <span v-if="tk.state === 'running'" class="mono t-2xs dim-3">{{ expiryText(tk) }}</span>
          </div>

          <!-- 上次为什么没成，是这张表最重要的一列 -->
          <div v-if="tk.lastError" class="task__err" :title="tk.lastClass">
            <span class="t-2xs" :style="{ color: tk.state === 'failed' ? 'var(--danger)' : 'var(--text-3)' }">
              {{ classLabel(tk.lastClass) }}
            </span>
            <span class="mono t-2xs dim-3 task__errmsg">{{ tk.lastError }}</span>
          </div>
        </div>

        <div class="task__right">
          <span v-if="tk.state === 'running'" class="mono t-2xs dim">{{ countdown(tk) }}</span>

          <button v-if="tk.state === 'succeeded' && tk.instanceId" class="btn btn--xs btn--primary"
                  @click="openInstance(tk.instanceId)">{{ t.common.viewInstance }}</button>

          <button v-if="tk.state === 'running' || tk.state === 'paused'" class="btn btn--xs"
                  @click="toggle(tk)">
            {{ tk.state === 'running' ? t.overviewHunt.hunt.pause : t.overviewHunt.hunt.resume }}
          </button>

          <button class="btn btn--xs btn--warning" @click="remove(tk)">{{ t.common.delete }}</button>
        </div>
      </div>
    </SectionCard>
  </div>
</template>

<style scoped>
.warn-box { margin-bottom: 14px; }

.task {
  position: relative;
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 12px 14px 12px 17px;
  border-bottom: 1px solid var(--border-subtle);
}
.task:last-child { border-bottom: none; }

.task__bar { position: absolute; left: 0; top: 0; bottom: 0; width: 3px; }

.task__main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 5px; }

.task__line1 { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.task__line2 { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }

.task__state { font-size: var(--t-2xs); font-weight: 600; white-space: nowrap; }
.task__name { font-size: var(--t-sm); font-weight: 600; }

.task__err { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
/* 错误原文可能很长，截断而不是撑破整行；完整内容在 title 上 */
.task__errmsg { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.task__right {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
  padding-top: 2px;
}
</style>
