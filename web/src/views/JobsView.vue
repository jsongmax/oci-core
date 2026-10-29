<script setup lang="ts">
/**
 * 后台任务列表。
 *
 * 创建实例、换 IP、改配置这类操作要几十秒到几分钟，提交后就转到后台执行，
 * 界面不再干等。这里集中看它们的进度、每一步做了什么、失败原因。
 * 数据全部来自 store：首屏加载一次，之后靠 SSE 的 job.updated 实时更新。
 */
import { computed, ref } from 'vue'
import { useStore } from '@/store'
import { relativeTime } from '@/lib/adapt'
import { now } from '@/lib/clock'
import type { JobDTO, JobStatus } from '@/api/types'
import SectionCard from '@/components/SectionCard.vue'
import EmptyState from '@/components/EmptyState.vue'
import AccountChip from '@/components/AccountChip.vue'

const {
  state, accountById, openDrawer, cancelJob, removeJob, clearFinishedJobs, loadJobs, activeJobCount, ask
} = useStore()

type Filter = 'all' | 'active' | 'failed' | 'done'
const filter = ref<Filter>('all')

const STATUS_LABEL: Record<JobStatus, string> = {
  queued: '排队中', running: '进行中', succeeded: '已完成',
  failed: '失败', canceled: '已取消', interrupted: '已中断'
}
const STATUS_TONE: Record<JobStatus, string> = {
  queued: 'var(--text-tertiary)', running: 'var(--accent)', succeeded: 'var(--success)',
  failed: 'var(--danger)', canceled: 'var(--text-tertiary)', interrupted: 'var(--warning)'
}
const KIND_ICON: Record<string, string> = {
  'instance.launch': '＋', 'instance.change_ip': '⇄', 'instance.reshape': '⇕',
  'instance.terminate': '✕', 'instance.enable_ipv6': '⁶', 'instance.remove_ipv6': '⁶', 'instance.attach_vnic': '⌁',
  'instance.detach_vnic': '⌁', 'instance.bulk': '▤', 'instance.sync': '↻',
  'network.ensure': '⌗', 'storage.boot_volume': '◱', 'storage.volume': '◱'
}

const isActive = (j: JobDTO) => j.status === 'queued' || j.status === 'running'

const counts = computed(() => ({
  all: state.jobs.length,
  active: activeJobCount.value,
  failed: state.jobs.filter(j => j.status === 'failed' || j.status === 'interrupted').length,
  done: state.jobs.filter(j => j.status === 'succeeded').length
}))

const list = computed(() => state.jobs.filter(j => {
  switch (filter.value) {
    case 'active': return isActive(j)
    case 'failed': return j.status === 'failed' || j.status === 'interrupted'
    case 'done': return j.status === 'succeeded'
    default: return true
  }
}))

const finishedCount = computed(() => state.jobs.length - activeJobCount.value)

/** 展开的任务。进行中的默认展开——那正是用户点进来想看的。 */
const expanded = ref(new Set<string>())
const collapsed = ref(new Set<string>())
function isOpen(j: JobDTO): boolean {
  if (isActive(j)) return !collapsed.value.has(j.id)
  return expanded.value.has(j.id)
}
function toggle(j: JobDTO): void {
  const set = isActive(j) ? collapsed.value : expanded.value
  if (set.has(j.id)) set.delete(j.id)
  else set.add(j.id)
}

/** 已用时长。进行中的读全局时钟，结束的用结束时刻。 */
function duration(j: JobDTO): string {
  if (!j.startedAt) return ''
  const end = j.finishedAt ? Date.parse(j.finishedAt) : Math.max(now.value, Date.now())
  const sec = Math.max(0, Math.round((end - Date.parse(j.startedAt)) / 1000))
  if (sec < 60) return `${sec} 秒`
  const min = Math.floor(sec / 60)
  return `${min} 分 ${sec % 60} 秒`
}

function stepTime(iso: string): string {
  const d = new Date(iso)
  return d.toLocaleTimeString('zh-CN', { hour12: false })
}

function canOpenInstance(j: JobDTO): boolean {
  return !!j.instanceId && state.instances.some(i => i.id === j.instanceId)
}

function askClear() {
  ask({
    level: 2,
    title: '清除已结束的任务',
    body: `将从列表中移除 ${finishedCount.value} 条已结束的任务。操作本身已记在审计日志里，不受影响。`,
    okLabel: '清除',
    onConfirm: () => clearFinishedJobs()
  })
}
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">任务</h1>
        <p class="page-sub">
          长耗时操作在后台执行，关掉页面也会跑完 ·
          {{ counts.active }} 个进行中，共 {{ counts.all }} 条
        </p>
      </div>
      <div class="head-actions">
        <button class="btn" @click="loadJobs()">刷新</button>
        <button class="btn" :disabled="finishedCount === 0" @click="askClear()">清除已结束</button>
      </div>
    </header>

    <div class="filters" role="tablist">
      <button v-for="f in ([
        ['all', '全部'], ['active', '进行中'], ['failed', '失败'], ['done', '已完成']
      ] as [Filter, string][])" :key="f[0]" role="tab"
              class="filter" :class="{ 'is-on': filter === f[0] }" :aria-selected="filter === f[0]"
              @click="filter = f[0]">
        {{ f[1] }} <span class="mono filter__n">{{ counts[f[0]] }}</span>
      </button>
    </div>

    <SectionCard title="任务列表" note="最多保留最近 300 条已结束的任务">
      <EmptyState v-if="list.length === 0" icon="◷"
                  :title="state.jobs.length === 0 ? '还没有后台任务' : '这个分类下没有任务'"
                  sub="创建实例、更换公网 IP、修改配置等操作提交后会出现在这里。" />

      <div v-for="j in list" :key="j.id" class="job" :class="{ 'is-active': isActive(j) }">
        <button class="job__head" :aria-expanded="isOpen(j)" @click="toggle(j)">
          <span class="job__icon" :style="{ color: STATUS_TONE[j.status] }">
            <span v-if="j.status === 'running'" class="job__spin" />
            <template v-else>{{ KIND_ICON[j.kind] ?? '◷' }}</template>
          </span>

          <span class="job__main">
            <span class="job__line1">
              <span class="job__title">{{ j.title }}</span>
              <AccountChip v-if="j.accountId" :account="accountById(j.accountId)" />
            </span>
            <span class="job__line2">
              <span class="t-2xs job__status" :style="{ color: STATUS_TONE[j.status] }">
                ● {{ STATUS_LABEL[j.status] }}
              </span>
              <span class="mono t-2xs dim-3">{{ relativeTime(j.createdAt) }}提交</span>
              <span v-if="j.startedAt" class="mono t-2xs dim-3">用时 {{ duration(j) }}</span>
              <span v-if="j.summary && j.status === 'succeeded'" class="t-2xs job__summary">{{ j.summary }}</span>
              <span v-if="j.error" class="t-2xs job__error" :title="j.error">{{ j.error }}</span>
            </span>
          </span>

          <span class="job__right">
            <span v-if="isActive(j)" class="mono t-2xs dim">{{ j.progress }}%</span>
            <span class="job__chev" :class="{ 'is-open': isOpen(j) }">›</span>
          </span>
        </button>

        <div v-if="isActive(j)" class="job__bar" :class="{ 'is-queued': j.status === 'queued' }">
          <span :style="{ width: `${Math.max(j.progress, 3)}%` }" />
        </div>

        <div v-if="isOpen(j)" class="job__body">
          <ol v-if="j.steps.length" class="steps">
            <li v-for="(s, idx) in j.steps" :key="idx" class="step">
              <span class="mono t-2xs dim-3 step__at">{{ stepTime(s.at) }}</span>
              <span class="t-xs step__text">{{ s.text }}</span>
            </li>
          </ol>
          <p v-else class="t-xs dim-3 job__empty">
            {{ j.status === 'queued' ? '等待前面的任务完成（同一台实例上的操作按顺序执行）' : '还没有进度记录' }}
          </p>

          <p v-if="j.error" class="job__errbox">{{ j.error }}</p>
          <p v-if="j.status === 'interrupted'" class="t-xs dim-3">
            请求可能已经发到 Oracle，请到对应页面确认实际状态，再决定是否重做。
          </p>

          <div class="job__actions">
            <button v-if="canOpenInstance(j)" class="btn btn--sm"
                    @click="openDrawer({ kind: 'instance', id: j.instanceId! })">查看实例</button>
            <button v-if="j.status === 'queued'" class="btn btn--sm" @click="cancelJob(j.id)">取消</button>
            <button v-if="!isActive(j)" class="btn btn--sm btn--ghost" @click="removeJob(j.id)">移除</button>
          </div>
        </div>
      </div>
    </SectionCard>
  </div>
</template>

<style scoped>
.filters { display: flex; gap: 6px; margin-bottom: 14px; flex-wrap: wrap; }
.filter {
  height: 30px; padding: 0 12px; border-radius: var(--radius-full);
  border: 1px solid var(--border-subtle); background: transparent;
  color: var(--text-secondary); font-size: 12px; cursor: pointer;
  display: inline-flex; align-items: center; gap: 6px;
}
.filter:hover { border-color: var(--border-strong); color: var(--text-primary); }
.filter.is-on { border-color: var(--accent); color: var(--text-primary); background: var(--bg-hover); }
.filter__n { font-size: 11px; color: var(--text-tertiary); }

.job { border-bottom: 1px solid var(--border-subtle); }
.job:last-child { border-bottom: none; }

.job__head {
  width: 100%; display: flex; align-items: flex-start; gap: 12px; padding: 12px 16px;
  border: 0; background: transparent; color: inherit; text-align: left; cursor: pointer;
}
.job__head:hover { background: var(--bg-hover); }
.job__head:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }

.job__icon {
  width: 26px; height: 26px; flex: 0 0 auto; display: flex; align-items: center; justify-content: center;
  border-radius: var(--radius-md); border: 1px solid var(--border-subtle); font-size: 13px;
}
.job__spin {
  width: 12px; height: 12px; border-radius: var(--radius-full);
  border: 2px solid var(--accent); border-right-color: transparent;
  animation: spin 0.9s linear infinite;
}
@keyframes spin { to { transform: rotate(360deg); } }

.job__main { flex: 1 1 auto; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
.job__line1 { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.job__line2 { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; min-width: 0; }
.job__title { font-size: var(--t-sm, 13px); font-weight: 600; }
.job__status { font-weight: 600; white-space: nowrap; }
.job__summary { color: var(--text-secondary); }
/* 错误原文可能很长，截断而不是撑破整行；展开后有完整内容 */
.job__error {
  color: var(--danger); overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  max-width: 100%; min-width: 0; flex: 1 1 200px;
}

.job__right { display: flex; align-items: center; gap: 10px; flex: 0 0 auto; padding-top: 4px; }
.job__chev { color: var(--text-tertiary); transition: transform var(--dur-fast); font-size: 14px; }
.job__chev.is-open { transform: rotate(90deg); }

.job__bar { height: 3px; margin: 0 16px 10px 54px; background: var(--bg-inset); border-radius: var(--radius-full); overflow: hidden; }
.job__bar > span {
  display: block; height: 100%; background: var(--accent); border-radius: inherit;
  transition: width 600ms var(--ease-standard);
}
.job__bar.is-queued > span { background: var(--text-tertiary); }

.job__body { padding: 0 16px 14px 54px; display: flex; flex-direction: column; gap: 10px; }
.steps {
  margin: 0; padding: 10px 12px; list-style: none; display: flex; flex-direction: column; gap: 4px;
  border-radius: var(--radius-md); background: var(--bg-inset); border: 1px solid var(--border-subtle);
}
.step { display: flex; gap: 10px; align-items: baseline; }
.step__at { flex: 0 0 auto; }
.step__text { white-space: pre-line; word-break: break-word; }
.job__empty { margin: 0; }
.job__errbox {
  margin: 0; padding: 10px 12px; border-radius: var(--radius-md);
  border: 1px solid var(--danger); color: var(--danger); font-size: 12px; white-space: pre-line; word-break: break-word;
}
.job__actions { display: flex; gap: 8px; }

@media (max-width: 640px) {
  .job__bar { margin-left: 16px; }
  .job__body { padding-left: 16px; }
}
</style>
