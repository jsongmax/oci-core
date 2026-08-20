<script setup lang="ts">
/**
 * 容量守候（抢机）。
 *
 * 这个页面必须把"为什么还没抢到"讲清楚。任务在后台跑，用户唯一的信息来源
 * 就是这张表——不显示尝试次数、上次的错误和下次的时间，它就是个黑盒，
 * 用户只能反复问"到底在不在跑"，然后去点重启。
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import { shortAd } from '@/lib/adapt'
import { now } from '@/lib/clock'
import { hunt as huntApi, errorText, type HuntTaskDTO, type HuntLimitsDTO } from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import CheckList from '@/components/CheckList.vue'
import AccountChip from '@/components/AccountChip.vue'

const { accountById, openDrawer, ask, toast, toastError } = useStore()

const tasks = ref<HuntTaskDTO[]>([])
const limits = ref<HuntLimitsDTO | null>(null)
const loading = ref(false)
const loadError = ref('')

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

const STATE_LABEL: Record<string, string> = {
  running: '运行中', paused: '已暂停',
  succeeded: '已抢到', failed: '已停止', pending: '待启动'
}

const STATE_TONE: Record<string, string> = {
  running: 'var(--accent)', paused: 'var(--warning)',
  succeeded: 'var(--success)', failed: 'var(--danger)', pending: 'var(--text-3)'
}

/** 错误分类映射成人话。原始分类名同时保留在 title 里，方便搜索。 */
const CLASS_LABEL: Record<string, string> = {
  OutOfCapacity: '该可用域暂时没有容量',
  Throttled: '被 Oracle 限流，已降速',
  QuotaExceeded: '配额已满',
  AuthFailed: '凭据校验失败',
  NotAuthorized: '无权限或资源不存在',
  BadRequest: '参数有误',
  Transient: '网络抖动',
  Conflict: '资源状态冲突',
  Expired: '任务已到期',
  Succeeded: '成功',
  Unknown: '未识别的错误'
}

const classLabel = (c: string) => CLASS_LABEL[c] ?? c

/** 距下次尝试还有多久。读全局时钟，所以会自己走。 */
function countdown(task: HuntTaskDTO): string {
  if (task.state !== 'running') return '—'
  const ms = new Date(task.nextAt).getTime() - now.value
  if (Number.isNaN(ms)) return '—'
  if (ms <= 0) return '即将尝试'
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s} 秒后`
  const m = Math.floor(s / 60)
  return m < 60 ? `${m} 分 ${s % 60} 秒后` : `${Math.floor(m / 60)} 小时 ${m % 60} 分后`
}

function expiryText(task: HuntTaskDTO): string {
  const ms = new Date(task.expiresAt).getTime() - now.value
  if (Number.isNaN(ms) || ms <= 0) return '已到期'
  const h = Math.round(ms / 3_600_000)
  return h < 24 ? `${h} 小时后到期` : `${Math.round(h / 24)} 天后到期`
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
    toastError('操作失败', err)
  }
}

function remove(task: HuntTaskDTO) {
  ask({
    level: 2,
    title: `删除守候任务「${task.name}」`,
    body: '任务立即停止并从列表消失。已抢到的实例不受影响，要销毁请去实例页终止。',
    okLabel: '删除任务',
    onConfirm: async () => {
      try {
        const res = await huntApi.remove(task.id)
        tasks.value = tasks.value.filter(t => t.id !== task.id)
        toast({ tone: 'success', title: '任务已删除', body: res.notice })
      } catch (err) {
        toastError('删除失败', err)
      }
    }
  })
}

function openInstance(id: string) {
  openDrawer({ kind: 'instance', id, tab: '概览' })
}
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">容量守候</h1>
        <p class="page-sub">
          反复尝试创建实例直到成功 ·
          {{ tasks.length }} 个任务，{{ running }} 个运行中
        </p>
      </div>
      <div class="head-actions">
        <button class="btn" :disabled="loading" @click="load()">刷新</button>
        <button class="btn btn--primary" @click="openDrawer({ kind: 'create-instance' })">
          新建任务
        </button>
      </div>
    </header>

    <p v-if="loadError" class="load-err">{{ loadError }}</p>

    <!-- 风险必须在页面上，而不是只写在文档里 -->
    <CheckList class="warn-box" :items="[
      { tone: 'warn',
        text: `高频调用创建接口轻则限流，重则账号被标记。间隔硬下限 ${limits?.minIntervalSeconds ?? 30} 秒，不可放开。` },
      { tone: 'info',
        text: '容量释放后有数分钟的申领窗口，不是抢毫秒。间隔压到极限，命中率提升有限而请求量成倍。' },
      { tone: 'info',
        text: '每个账号只允许一个任务。容量是账号级共享的，并行不会更快，只会让请求量翻倍。' }
    ]" />

    <SectionCard title="任务" :note="`每 15 秒自动刷新`">
      <SkeletonRows v-if="loading && tasks.length === 0" :rows="3" />

      <EmptyState v-else-if="tasks.length === 0" title="还没有守候任务"
                  body="任务会在后台反复尝试创建实例，抢到时推通知。"
                  action-label="新建任务"
                  @action="openDrawer({ kind: 'create-instance' })" />

      <div v-for="t in tasks" v-else :key="t.id" class="task">
        <span class="task__bar" :style="{ background: acctColor(accountById(t.accountId).colorIndex) }" />

        <div class="task__main">
          <div class="task__line1">
            <span class="task__state" :style="{ color: STATE_TONE[t.state] }">
              ● {{ STATE_LABEL[t.state] ?? t.state }}
            </span>
            <span class="task__name">{{ t.name }}</span>
            <AccountChip :account="accountById(t.accountId)" />
            <span class="mono t-2xs dim-3">{{ shapeText(t) }}</span>
            <span class="mono t-2xs dim-3">{{ t.region }}</span>
          </div>

          <div class="task__line2">
            <span class="mono t-2xs dim-3">已尝试 {{ t.attempts }} 次</span>
            <span v-if="t.lastAd" class="mono t-2xs dim-3">当前 {{ shortAd(t.lastAd) }}</span>
            <span class="mono t-2xs dim-3">每 {{ t.intervalSeconds }} 秒</span>
            <!-- 开没开预检直接决定这个任务发多少创建请求，必须能一眼看到 -->
            <span class="mono t-2xs" :style="{ color: t.precheckCapacity ? 'var(--success)' : 'var(--warning)' }"
                  :title="t.precheckCapacity
                    ? '每轮先查只读的容量报告，说没货就跳过、不发创建请求'
                    : '每轮直接调创建接口，不管有没有容量'">
              {{ t.precheckCapacity ? '先查容量' : '不查容量' }}
            </span>
            <span v-if="t.state === 'running'" class="mono t-2xs dim-3">{{ expiryText(t) }}</span>
          </div>

          <!-- 上次为什么没成，是这张表最重要的一列 -->
          <div v-if="t.lastError" class="task__err" :title="t.lastClass">
            <span class="t-2xs" :style="{ color: t.state === 'failed' ? 'var(--danger)' : 'var(--text-3)' }">
              {{ classLabel(t.lastClass) }}
            </span>
            <span class="mono t-2xs dim-3 task__errmsg">{{ t.lastError }}</span>
          </div>
        </div>

        <div class="task__right">
          <span v-if="t.state === 'running'" class="mono t-2xs dim">{{ countdown(t) }}</span>

          <button v-if="t.state === 'succeeded' && t.instanceId" class="btn btn--xs btn--primary"
                  @click="openInstance(t.instanceId)">查看实例</button>

          <button v-if="t.state === 'running' || t.state === 'paused'" class="btn btn--xs"
                  @click="toggle(t)">
            {{ t.state === 'running' ? '暂停' : '恢复' }}
          </button>

          <button class="btn btn--xs btn--warning" @click="remove(t)">删除</button>
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
