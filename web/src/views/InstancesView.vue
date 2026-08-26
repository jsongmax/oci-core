<script setup lang="ts">
/** §4.4.1 实例列表：跨账号聚合表格 · 筛选 · 分组 · 密度 · 批量条 */
import { computed, nextTick, ref } from 'vue'
import { useStore, type StateFilter } from '@/store'
import { acctColor, copy } from '@/lib/format'
import { instanceUptime } from '@/lib/adapt'
import { MAX_CONCURRENT_PULSES, isTransitional } from '@/lib/lifecycle'
import type { Instance } from '@/types'
import MaskedText from '@/components/MaskedText.vue'
import AccountChip from '@/components/AccountChip.vue'
import StateBadge from '@/components/StateBadge.vue'
import EmptyState from '@/components/EmptyState.vue'
import InstanceRowActions from '@/components/InstanceRowActions.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import ColumnPicker from '@/components/ColumnPicker.vue'
import { useColumns, type ColumnDef } from '@/lib/columns'

const store = useStore()
const {
  state, accountById, filteredInstances, countByFilter,
  openDrawer, bulk, toggleSelection, syncNow, toast, toastError
} = store

/* ---------- 备注就地编辑 ---------- */

const editingNote = ref('')
const noteDraft = ref('')

function startNote(i: Instance) {
  editingNote.value = i.id
  noteDraft.value = i.note
  // 等输入框渲染出来再聚焦，否则 focus 落在还不存在的元素上。
  void nextTick(() => {
    const el = document.querySelector<HTMLInputElement>('.note-edit')
    el?.focus()
    el?.select()
  })
}

async function saveNote(i: Instance) {
  if (editingNote.value !== i.id) return
  const next = noteDraft.value.trim()
  editingNote.value = ''
  if (next === i.note) return

  // 先本地更新再发请求：备注是纯展示字段，乐观更新不会造成状态错乱，
  // 失败时回滚并提示。
  const before = i.note
  i.note = next
  try {
    const { instances: api } = await import('@/api')
    await api.setNote(i.id, next)
  } catch (err) {
    i.note = before
    toastError('保存备注失败', err)
  }
}

/** 复制公网 IP。整行是可点的，按钮必须 @click.stop 才不会顺手打开抽屉。 */
function copyIp(ip: string) {
  void copy(ip).then(() => toast({ tone: 'accent', title: '已复制公网 IP', body: ip }))
}

const collapsedGroups = ref<Set<string>>(new Set())

/**
 * 实例表的列。宽度就是原来写死在 .cols 里的那串，只是拆开成了每列一段。
 *
 * minViewport 沿用原先媒体查询的断点：运行时长 1600、引导卷与备注 1280、
 * 规格与区域 1024。区别是现在它和用户的勾选走同一条计算路径，
 * 不会出现「CSS 藏了一列但 grid 模板还留着那一格」导致整行错位。
 */
const INSTANCE_COLUMNS: ColumnDef[] = [
  { key: 'check',   label: '选择',     width: '40px',                 fixed: true },
  { key: 'state',   label: '状态',     width: '104px',                fixed: true },
  { key: 'name',    label: '名称',     width: 'minmax(180px, 1.5fr)', fixed: true },
  { key: 'account', label: '账号',     width: '78px' },
  { key: 'region',  label: '区域 · AD', width: '158px',               minViewport: 1024 },
  { key: 'shape',   label: '规格',     width: 'minmax(170px, 1.2fr)', minViewport: 1024 },
  { key: 'ip',      label: '公网 IP',  width: '132px' },
  { key: 'boot',    label: '引导卷',   width: '84px',                 minViewport: 1280 },
  { key: 'note',    label: '备注',     width: 'minmax(110px, 1fr)',   minViewport: 1280 },
  { key: 'uptime',  label: '运行时长', width: '88px',                 minViewport: 1600 },
  { key: 'actions', label: '操作',     width: '132px',                fixed: true }
]

const cols = useColumns('oci.cols.instances', INSTANCE_COLUMNS)

const filters: { key: StateFilter; label: string }[] = [
  { key: 'all', label: '全部' },
  { key: 'running', label: '运行中' },
  { key: 'stopped', label: '已停止' },
  { key: 'transition', label: '转换中' },
  { key: 'anomaly', label: '异常' },
  { key: 'terminated', label: '已终止' }
]

const rowHeight = computed(() => (state.density === 'comfy' ? 56 : 44))

/** 首次加载完成前显示骨架屏，而不是"暂无实例"的空态——那会误导用户。 */
const showSkeleton = computed(() => state.loading && state.instances.length === 0)

/** §7 性能红线：同屏脉冲行 ≤ 12，超出降级为静态徽章 */
const pulseBudget = computed(() => {
  const ids = filteredInstances.value.filter(i => isTransitional(i.state)).slice(0, MAX_CONCURRENT_PULSES)
  return new Set(ids.map(i => i.id))
})

interface Group { account?: ReturnType<typeof accountById>; rows: Instance[]; open: boolean }

const groups = computed<Group[]>(() => {
  if (!state.groupByAccount) return [{ rows: filteredInstances.value, open: true }]
  return state.accounts
    .map(a => ({
      account: a,
      rows: filteredInstances.value.filter(i => i.accountId === a.id),
      open: !collapsedGroups.value.has(a.id)
    }))
    .filter(g => g.rows.length > 0)
})

function toggleGroup(id: string) {
  const next = new Set(collapsedGroups.value)
  next.has(id) ? next.delete(id) : next.add(id)
  collapsedGroups.value = next
}

const subtitle = computed(() => {
  const scope = state.accountFilter.size === state.accounts.length
    ? `跨 ${state.accounts.length} 个账号聚合`
    : `${state.accountFilter.size} 个账号`
  return `${scope} · 共 ${filteredInstances.value.length} 台`
})

const bootTone = (i: Instance) =>
  i.bootGb / i.bootLimitGb >= 0.9 ? 'var(--warning)' : 'var(--text-primary)'
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">实例</h1>
        <p class="page-sub">{{ subtitle }}</p>
      </div>
      <div class="head-actions">
        <span v-if="!state.live" class="offline" title="实时连接已断开，状态可能不是最新的">离线</span>
        <button class="btn" :disabled="state.syncing" @click="syncNow()">
          {{ state.syncing ? '同步中…' : '同步' }}
        </button>
        <button class="btn btn--primary" @click="openDrawer({ kind: 'create-instance' })">创建实例</button>
      </div>
    </header>

    <p v-if="state.loadError" class="load-err">{{ state.loadError }}</p>

    <div class="toolbar">
      <button v-for="f in filters" :key="f.key" class="pill"
              :class="{ 'is-active': state.stateFilter === f.key }" @click="state.stateFilter = f.key">
        {{ f.label }}<span class="mono toolbar__count">{{ countByFilter(f.key) }}</span>
      </button>
      <span class="toolbar__spacer" />
      <button class="pill" :class="{ 'is-active': state.groupByAccount }"
              @click="state.groupByAccount = !state.groupByAccount">按账号分组</button>
      <button class="pill" @click="state.density = state.density === 'comfy' ? 'compact' : 'comfy'">
        {{ state.density === 'comfy' ? '舒适 56px' : '紧凑 44px' }}
      </button>
      <ColumnPicker :columns="cols" />
    </div>

    <div class="card table">
      <div class="table-head cols" :style="{ gridTemplateColumns: cols.template.value }">
        <span />
        <span>状态</span><span>名称</span>
        <span v-if="cols.has('account')">账号</span>
        <span v-if="cols.has('region')">区域 · AD</span>
        <span v-if="cols.has('shape')">规格</span>
        <span v-if="cols.has('ip')">公网 IP</span>
        <span v-if="cols.has('boot')">引导卷</span>
        <span v-if="cols.has('note')">备注</span>
        <span v-if="cols.has('uptime')">运行时长</span>
        <span class="table__right">操作</span>
      </div>

      <SkeletonRows v-if="showSkeleton" :rows="6" />

      <template v-for="(g, gi) in groups" :key="g.account?.id ?? gi">
        <button v-if="g.account" class="group" @click="toggleGroup(g.account.id)">
          <span class="group__bar" :style="{ background: acctColor(g.account.colorIndex) }" />
          <span class="group__caret">{{ g.open ? '▼' : '▶' }}</span>
          <span class="mono group__code" :style="{ color: acctColor(g.account.colorIndex) }">{{ g.account.code }}</span>
          <span class="t-xs">{{ g.account.alias }}</span>
          <span class="group__spacer" />
          <span class="mono t-2xs dim-3">
            {{ g.rows.length }} 台 · 运行 {{ g.rows.filter(i => i.state === 'RUNNING').length }} ·
            停止 {{ g.rows.filter(i => i.state === 'STOPPED').length }}
          </span>
        </button>

        <TransitionGroup v-if="g.open" name="rows">
          <div v-for="i in g.rows" :key="i.id" class="table-row cols"
               :class="{ 'is-selected': state.selection.has(i.id), 'is-dimmed': i.state === 'TERMINATING' || i.state === 'TERMINATED' }"
               :style="{ height: rowHeight + 'px', gridTemplateColumns: cols.template.value }">
            <span class="acct-bar" :style="{ background: acctColor(accountById(i.accountId).colorIndex) }" />
            <span v-if="state.selection.has(i.id)" class="sel-bar" />

            <label class="cell cell--check">
              <input type="checkbox" :checked="state.selection.has(i.id)" @change="toggleSelection(i.id)"
                     :aria-label="`选择 ${i.name}`" />
            </label>

            <span class="cell">
              <StateBadge :state="i.state" :anomaly="i.anomaly" :settled="!!i.settledAt"
                          :static-dot="isTransitional(i.state) && !pulseBudget.has(i.id)" />
            </span>

            <span class="cell cell--name">
              <button class="name" :class="{ 'is-terminated': i.state === 'TERMINATED' }"
                      @click="openDrawer({ kind: 'instance', id: i.id, tab: '概览' })">{{ i.name }}</button>
              <span class="mono t-2xs dim-3">ocid1…{{ i.ocidTail }}</span>
            </span>

            <span v-if="cols.has('account')" class="cell"><AccountChip :account="accountById(i.accountId)" /></span>
            <span v-if="cols.has('region')" class="cell mono t-xs dim">{{ i.region }} · {{ i.ad }}</span>

            <span v-if="cols.has('shape')" class="cell cell--shape">
              <span class="mono t-xs">{{ i.shape }}</span>
              <span class="mono t-2xs dim-3">{{ i.ocpu }} OCPU · {{ i.memGb }} GB</span>
            </span>

            <span v-if="cols.has('ip')" class="cell mono t-xs ip" :class="{ 'is-fresh': i.settledAt }">
              <span class="ip__v"><MaskedText :value="i.publicIp" kind="ip" /></span>
              <button v-if="i.publicIp && i.publicIp !== '—'" class="ip__copy" title="复制公网 IP"
                      @click.stop="copyIp(i.publicIp)">⧉</button>
            </span>

            <span v-if="cols.has('boot')" class="cell cell--boot">
              <span class="mono t-xs" :style="{ color: bootTone(i) }">{{ i.bootGb }} GB</span>
              <span class="mono t-2xs dim-3">VPU {{ i.vpu }}</span>
            </span>

            <span v-if="cols.has('note')" class="cell col-note">
              <input v-if="editingNote === i.id" ref="noteInput" class="note-edit" maxlength="500"
                     :value="noteDraft" @click.stop
                     @input="noteDraft = ($event.target as HTMLInputElement).value"
                     @keydown.enter.stop.prevent="saveNote(i)"
                     @keydown.esc.stop.prevent="editingNote = ''"
                     @blur="saveNote(i)" />
              <button v-else class="note-view" :class="{ 'is-empty': !i.note }"
                      :title="i.note || '点击添加备注'" @click.stop="startNote(i)">
                {{ i.note || '＋' }}
              </button>
            </span>
            <span v-if="cols.has('uptime')" class="cell mono t-xs dim col-uptime"
                  :title="instanceUptime(i).approx
                    ? '面板未观测到本次开机（首次同步时它已在运行），这里显示的是创建至今的时长'
                    : '自面板观测到本次开机起'">
              <template v-if="instanceUptime(i).approx">~</template>{{ instanceUptime(i).text }}
            </span>

            <span class="cell cell--actions"><InstanceRowActions :instance="i" /></span>

            <!-- 过渡期间的不定进度条（§7 Tier 1.2） -->
            <span v-if="isTransitional(i.state)" class="row-progress"><span /></span>
            <!-- instance-ready 扫光（§7 Tier 3.15） -->
            <span v-if="i.settledAt" class="row-sweep"><span /></span>
          </div>
        </TransitionGroup>
      </template>

      <EmptyState v-if="!filteredInstances.length" title="没有符合筛选条件的实例"
                  action-label="清除筛选" @action="state.stateFilter = 'all'" />
    </div>

    <!-- 批量操作条：批量终止不提供（§4.4.1） -->
    <Transition name="scrim">
      <div v-if="state.selection.size && state.options.allowBulk" class="bulk glass">
        <span class="t-sm"><span class="mono bulk__n">{{ state.selection.size }}</span> 台已选</span>
        <span class="bulk__sep" />
        <button class="btn btn--sm" @click="bulk('start')">批量开机</button>
        <button class="btn btn--sm" @click="bulk('stop')">批量关机</button>
        <button class="btn btn--sm" @click="bulk('restart')">批量重启</button>
        <span class="t-2xs dim-3">批量终止不提供，需逐台执行</span>
        <button class="bulk__close" @click="state.selection.clear()">✕</button>
      </div>
    </Transition>
  </div>
</template>

<style scoped>
.note-view {
  width: 100%; min-height: 24px; padding: 2px 6px; border: 0; border-radius: var(--radius-sm);
  background: transparent; color: var(--text-secondary);
  font-size: 11px; font-family: inherit; text-align: left; cursor: text;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.note-view:hover { background: var(--bg-inset); }
/* 空备注只留一个很淡的加号：一整列的占位符会比备注本身还抢眼。 */
.note-view.is-empty { color: var(--text-tertiary); opacity: 0.35; }
.table-row:hover .note-view.is-empty { opacity: 1; }
.note-edit {
  width: 100%; height: 26px; padding: 0 6px;
  border: 1px solid var(--accent); border-radius: var(--radius-sm);
  background: var(--bg-surface); color: var(--text-primary);
  font-size: 11px; font-family: inherit; outline: none;
}

.ip { display: flex; align-items: center; gap: 6px; }
.ip__v { overflow: hidden; text-overflow: ellipsis; }
.ip__copy {
  flex: 0 0 auto; width: 20px; height: 20px; padding: 0;
  border: 0; border-radius: var(--radius-sm); background: transparent;
  color: var(--text-tertiary); font-size: 12px; line-height: 1; cursor: pointer;
  /* 常驻会让这一列很吵，完全隐藏又没人知道它存在。默认压暗，悬停行时点亮。 */
  opacity: 0.35; transition: opacity var(--dur-fast) var(--ease-standard);
}
.table-row:hover .ip__copy { opacity: 1; }
.ip__copy:hover { background: var(--bg-hover); color: var(--text-primary); }
.ip__copy:focus-visible { opacity: 1; }

.toolbar { display: flex; align-items: center; gap: 8px; margin-bottom: 14px; flex-wrap: wrap; }
.toolbar__spacer { flex: 1 1 auto; }
.head-actions { display: flex; align-items: center; gap: 10px; }
.offline {
  font-size: 11px; padding: 3px 9px; border-radius: var(--radius-full);
  border: 1px solid var(--warning); background: var(--warning-soft); color: var(--warning);
}
.load-err {
  margin: 0 0 12px; padding: 10px 14px; border-radius: var(--radius-md);
  border: 1px solid var(--danger); background: var(--danger-soft);
  color: var(--danger); font-size: 12px; line-height: 18px;
}
.toolbar__count { font-size: 11px; opacity: 0.65; }

.table { overflow: hidden; }
/* grid-template-columns 由 cols.template 内联给出——列是可隐藏的，
   写死在这里就会和实际渲染的单元格数量对不上。 */
.cols { padding-left: 0; }
.table-head > span:first-child { padding-left: 14px; }
.table__right { text-align: right; padding-right: 14px; }

.cell { min-width: 0; padding-right: 12px; display: flex; align-items: center; gap: 8px; }
.cell--check { padding-left: 14px; justify-content: flex-start; }
.cell--check input { accent-color: var(--accent); width: 15px; height: 15px; cursor: pointer; }
.cell--name, .cell--shape, .cell--boot { flex-direction: column; align-items: flex-start; gap: 0; }
.cell--actions { justify-content: flex-end; padding-right: 12px; }

.name {
  border: 0; background: none; padding: 0; color: var(--text-primary);
  font-size: 13px; font-weight: 500; cursor: pointer; max-width: 100%;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.name:hover { color: var(--accent); }
.name.is-terminated { text-decoration: line-through; }

.is-selected { background: var(--accent-soft); }
.is-dimmed { opacity: 0.6; }
.sel-bar { position: absolute; left: 0; top: 0; bottom: 0; width: 3px; background: var(--accent); z-index: 2; }
.is-fresh { animation: fade-in var(--dur-slow) var(--ease-decelerate); }

.row-progress { position: absolute; left: 0; right: 0; bottom: 0; height: 2px; overflow: hidden; }
.row-progress > span { display: block; height: 2px; width: 40%; background: var(--accent); animation: indeterminate 1.4s var(--ease-standard) infinite; }
.row-sweep { position: absolute; inset: 0; overflow: hidden; pointer-events: none; }
.row-sweep > span {
  position: absolute; top: 0; bottom: 0; width: 38%;
  background: linear-gradient(90deg, transparent, var(--success-soft), transparent);
  animation: sweep 600ms var(--ease-decelerate);
}

.group {
  display: flex; align-items: center; gap: 10px; width: 100%; height: 36px; padding: 0 20px 0 0;
  border: 0; border-bottom: 1px solid var(--border-subtle); background: var(--bg-inset);
  color: var(--text-primary); cursor: pointer; text-align: left;
}
.group__bar { width: 3px; height: 36px; flex: 0 0 auto; }
.group__caret { font-size: 9px; color: var(--text-tertiary); }
.group__code { font-size: 11px; font-weight: 600; }
.group__spacer { flex: 1 1 auto; }

.rows-enter-active { animation: fade-in var(--dur-normal) var(--ease-decelerate); }
.rows-leave-active { animation: fade-in var(--dur-fast) var(--ease-accelerate) reverse; }

.bulk {
  position: fixed; left: 50%; bottom: 28px; transform: translateX(-50%); z-index: 40;
  display: flex; align-items: center; gap: 10px; padding: 10px 14px;
  border-radius: var(--radius-lg); box-shadow: var(--shadow-4);
  animation: rise var(--dur-fast) var(--ease-decelerate);
}
.bulk__n { font-weight: 600; }
.bulk__sep { width: 1px; height: 20px; background: var(--border-default); }
.bulk__close { border: 0; background: none; color: var(--text-tertiary); cursor: pointer; }

/* §9 响应式 */
/* 窄屏隐藏哪些列由 columns.ts 的 minViewport 决定，不再走媒体查询。
   两套规则并存时，CSS 藏掉一列而 grid 模板里那一格还在，整行就会错位。 */
</style>
