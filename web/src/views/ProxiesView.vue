<script setup lang="ts">
/**
 * 代理池。
 *
 * 目的是网络隔离：给每个 Oracle 账号配一条独立出口，避免所有账号的 API
 * 调用从同一个 IP 出去。这一页要反复讲清楚两件事，否则它会变成一种
 * 虚假的安全感：
 *
 *  - 代理只换 IP，不换身份。每个请求都带该账号的密钥签名，Oracle 始终
 *    知道是哪个租户在调。隔离的只有「源 IP」这一个维度。
 *  - 共用出口比不用代理更糟。两个账号配同一条代理，等于主动把它们绑在
 *    一个 IP 上，凭空制造一个本来不存在的关联信号。所以重复绑定是硬拒绝。
 */
import { t } from '@/i18n'
import { computed, onMounted, ref, watch } from 'vue'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import { relativeTime } from '@/lib/adapt'
import {
  proxies as proxyApi, errorText,
  type ProxyDTO, type ProxyImportResultDTO, type ProxyStatusDTO
} from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import LoadingBar from '@/components/LoadingBar.vue'
import CheckList from '@/components/CheckList.vue'
import AccountChip from '@/components/AccountChip.vue'
import MaskedText from '@/components/MaskedText.vue'
import SelectMenu from '@/components/SelectMenu.vue'

const { state, accountById, ask, toast, toastError } = useStore()

const list = ref<ProxyDTO[]>([])
const bindings = ref<Record<string, string>>({})
const notice = ref('')
const loading = ref(false)
const loadError = ref('')
const checking = ref(false)

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
    const res = await proxyApi.list()
    list.value = res.proxies ?? []
    bindings.value = res.bindings ?? {}
    notice.value = res.notice
  } catch (err) {
    loadError.value = errorText(err)
  } finally {
    loading.value = false
  }
}

onMounted(() => void load())

/* ---------- 导入 ---------- */

const importText = ref('')
const preview = ref<ProxyImportResultDTO | null>(null)

// 改了文本，旧预览就不再代表将要导入的内容——「确认导入 N 条」的 N 会对不上。
watch(importText, () => { preview.value = null })
const importing = ref(false)


/** 先干跑一遍让用户核对。代理列表动辄十几行、格式又乱，直接写库会让人蒙。 */
async function parsePreview() {
  if (!importText.value.trim()) return
  importing.value = true
  const text = importText.value
  try {
    const res = await proxyApi.import(text, true)
    // 解析期间文本又被改了：这份预览已经过期，丢掉。
    if (text === importText.value) preview.value = res
  } catch (err) {
    toastError(t.addAcctProxy.px.parseFailed, err)
  } finally {
    importing.value = false
  }
}

async function confirmImport() {
  importing.value = true
  try {
    const res = await proxyApi.import(importText.value, false)
    toast({
      tone: res.failed > 0 ? 'warning' : 'success',
      title: t.addAcctProxy.px.imported(res.ok),
      body: [
        res.skipped ? t.addAcctProxy.px.skipped(res.skipped) : '',
        res.failed ? t.addAcctProxy.px.failedN(res.failed) : ''
      ].filter(Boolean).join(' · ') || undefined
    })
    importText.value = ''
    preview.value = null
    await load()
  } catch (err) {
    toastError(t.addAcctProxy.px.importFailed, err)
  } finally {
    importing.value = false
  }
}

/* ---------- 检测 ---------- */

async function check(id?: string) {
  checking.value = true
  try {
    const { results } = await proxyApi.check(id)
    const ok = results.filter(r => r.status === 'ok').length
    toast({
      tone: ok === results.length ? 'success' : 'warning',
      title: t.addAcctProxy.px.checked(ok, results.length)
    })
    await load()
  } catch (err) {
    toastError(t.addAcctProxy.px.checkFailed, err)
  } finally {
    checking.value = false
  }
}

/* ---------- 绑定 ---------- */

/** proxyId -> accountId，用来在代理行上显示"绑给了谁"。 */
const boundTo = computed(() => {
  const m: Record<string, string> = {}
  for (const [accId, proxyId] of Object.entries(bindings.value)) m[proxyId] = accId
  return m
})

/** 只列还没被别人占用的代理。共用是硬拒绝，选项里就不该出现。 */
function optionsFor(accountId: string) {
  const free = list.value.filter(p => {
    const owner = boundTo.value[p.id]
    return p.enabled && (!owner || owner === accountId)
  })
  return [{
    label: t.addAcctProxy.px.proxy,
    options: [
      { value: '', label: t.addAcctProxy.px.directLocal },
      ...free.map(p => ({
        value: p.id,
        label: `${p.label || display(p)}${p.lastStatus === 'fail' ? t.addAcctProxy.px.down : ''}`,
        dot: TONE[p.lastStatus]
      }))
    ]
  }]
}

async function bind(accountId: string, proxyId: string) {
  try {
    await proxyApi.bind(accountId, proxyId)
    toast({ tone: 'success', title: proxyId ? t.addAcctProxy.px.bound : t.addAcctProxy.px.unbound })
    await load()
  } catch (err) {
    toastError(t.addAcctProxy.px.bindFailed, err)
    await load()
  }
}

function remove(p: ProxyDTO) {
  ask({
    level: 2,
    title: t.addAcctProxy.px.deleteTitle(p.label || display(p)),
    body: t.addAcctProxy.px.deleteBody,
    okLabel: t.common.delete,
    onConfirm: async () => {
      try {
        await proxyApi.remove(p.id)
        toast({ tone: 'success', title: t.addAcctProxy.px.deleted })
        await load()
      } catch (err) {
        toastError(t.addAcctProxy.px.deleteFailed, err)
      }
    }
  })
}

async function toggle(p: ProxyDTO) {
  try {
    await proxyApi.update(p.id, { enabled: !p.enabled })
    await load()
  } catch (err) {
    toastError(t.addAcctProxy.px.actionFailed, err)
  }
}

/* ---------- 展示 ---------- */

const TONE: Record<ProxyStatusDTO, string> = {
  ok: 'var(--success)',
  fail: 'var(--danger)',
  unknown: 'var(--text-tertiary)'
}


const display = (p: ProxyDTO) =>
  `${p.scheme}://${p.username ? p.username + ':****@' : ''}${p.host}:${p.port}`

function checkedText(p: ProxyDTO): string {
  if (!p.lastCheckedAt || p.lastCheckedAt.startsWith('1970')) return t.addAcctProxy.px.neverChecked
  return t.addAcctProxy.px.checkedAt(relativeTime(p.lastCheckedAt))
}

/**
 * 失败但曾经通过——值得单独说一句。
 *
 * 「刚才挂了但十分钟前还好好的」和「从来没通过」是两种完全不同的处置：
 * 前者多半是代理商抽风，等等或重试即可；后者说明配置就是错的。
 */
function everWorked(p: ProxyDTO): boolean {
  return p.lastStatus === 'fail' && !!p.lastOkAt && !p.lastOkAt.startsWith('1970')
}

const usableAccounts = computed(() =>
  state.accounts.filter(a => a.status !== 'disabled'))

const okCount = computed(() => list.value.filter(p => p.lastStatus === 'ok').length)
const boundCount = computed(() => Object.keys(bindings.value).length)
const directCount = computed(() => usableAccounts.value.length - boundCount.value)
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">{{ t.addAcctProxy.px.title }}</h1>
        <p class="page-sub">
          {{ t.addAcctProxy.px.subtitle(list.length, okCount, boundCount, directCount) }}
        </p>
      </div>
      <div class="head-actions">
        <button class="btn" :disabled="checking || list.length === 0" @click="check()">
          {{ checking ? t.addAcctProxy.px.checkingAll : t.addAcctProxy.px.checkAll }}
        </button>
        <button class="btn" :disabled="refreshing" @click="refresh()">
          <span v-if="refreshing" class="btn-spin" aria-hidden="true" />{{ t.common.refresh }}
        </button>
      </div>
    </header>
    <LoadingBar :active="refreshing" />

    <p v-if="loadError" class="load-err">{{ loadError }}</p>

    <CheckList class="warn-box" :items="[
      { tone: 'info', text: notice || t.addAcctProxy.px.noteIdentity },
      { tone: 'warn',
        text: t.addAcctProxy.px.noteOnePerAccount },
      { tone: 'info',
        text: t.addAcctProxy.px.noteCheck },
      { tone: 'warn',
        text: t.addAcctProxy.px.noteSameIp }
    ]" />

    <!-- 导入 -->
    <SectionCard :title="t.addAcctProxy.px.importTitle" :note="t.addAcctProxy.px.importNote">
      <div class="pad">
        <!-- 用 .textarea 而不是 .input：.input 是固定 36px 高的单行控件，
             套在 textarea 上会把它压成一条缝，rows 属性完全不起作用。 -->
        <textarea v-model="importText" class="textarea imp__box"
                  style="height: 116px"
                  :placeholder="t.addAcctProxy.px.placeholder" spellcheck="false" />
        <div class="imp__actions">
          <button class="btn" :disabled="importing || !importText.trim()"
                  @click="parsePreview()">{{ importing ? t.addAcctProxy.px.parsing : t.addAcctProxy.px.preview }}</button>
          <button v-if="preview" class="btn btn--primary"
                  :disabled="importing || preview.ok === 0"
                  @click="confirmImport()">{{ t.addAcctProxy.px.confirmImport(preview.ok) }}</button>
          <span v-if="preview" class="t-2xs dim-3">
            {{ t.addAcctProxy.px.previewStats(preview.ok, preview.failed) }}
          </span>
        </div>

        <!-- 逐行结果。失败行必须留在原地并带行号，只报个总数等于让人自己找 -->
        <div v-if="preview?.rows?.length" class="imp__rows">
          <div v-for="row in preview.rows" :key="row.line" class="imp__row">
            <span class="mono t-2xs dim-3 imp__line">{{ row.line }}</span>
            <span class="mono t-xs imp__addr" :class="{ 'is-bad': row.error }">{{ row.masked }}</span>
            <span v-if="row.label" class="t-2xs dim-3">{{ row.label }}</span>
            <span class="imp__spacer" />
            <span v-if="row.error" class="t-2xs imp__err">{{ row.error }}</span>
            <span v-else-if="row.skipped" class="t-2xs dim-3">{{ t.addAcctProxy.px.existsSkip }}</span>
            <span v-else class="t-2xs" style="color: var(--success)">{{ t.addAcctProxy.px.importable }}</span>
          </div>
        </div>
      </div>
    </SectionCard>

    <!-- 代理列表 -->
    <SectionCard :title="t.addAcctProxy.px.poolTitle" :note="t.addAcctProxy.px.poolNote(list.length)" class="stale-able" :class="{ 'is-stale': refreshing && list.length > 0 }">
      <SkeletonRows v-if="loading && list.length === 0" :rows="3" />

      <EmptyState v-else-if="list.length === 0" icon="⇄" :title="t.addAcctProxy.px.poolEmpty"
                  :sub="t.addAcctProxy.px.poolEmptySub" />

      <div v-for="(p, n) in list" v-else :key="p.id" class="px reveal" :class="{ 'is-off': !p.enabled }"
           :style="{ '--i': Math.min(n, 8) }">
        <span class="px__dot" :style="{ background: TONE[p.lastStatus] }" />
        <span class="px__status" :style="{ color: TONE[p.lastStatus] }">
          {{ t.addAcctProxy.px.status[p.lastStatus] }}
        </span>

        <span class="px__label">{{ p.label || '—' }}</span>
        <span class="mono t-xs px__addr"><MaskedText :value="display(p)" kind="hostport" /></span>

        <span v-if="p.lastStatus === 'ok'" class="mono t-2xs dim-3 px__lat">
          {{ p.lastLatencyMs }}ms
        </span>
        <span v-else class="px__lat" />

        <span v-if="boundTo[p.id]" class="px__bound">
          <AccountChip :account="accountById(boundTo[p.id])" />
        </span>
        <span v-else class="t-2xs dim-3 px__bound">{{ t.addAcctProxy.px.unboundTag }}</span>

        <span class="px__spacer" />

        <span v-if="p.lastError" class="mono t-2xs px__err" :title="p.lastError">
          {{ p.lastError }}
        </span>
        <span v-if="everWorked(p)" class="t-2xs dim-3" :title="t.addAcctProxy.px.lastOk(p.lastOkAt)">
          {{ t.addAcctProxy.px.stillWorked(relativeTime(p.lastOkAt)) }}
        </span>
        <span class="mono t-2xs dim-3 px__when">{{ checkedText(p) }}</span>

        <button class="btn btn--xs" :disabled="checking" @click="check(p.id)">{{ t.addAcctProxy.px.check }}</button>
        <button class="btn btn--xs" @click="toggle(p)">{{ p.enabled ? t.addAcctProxy.px.disable : t.addAcctProxy.px.enable }}</button>
        <button class="btn btn--xs btn--warning" @click="remove(p)">{{ t.common.delete }}</button>
      </div>
    </SectionCard>

    <!-- 分配矩阵 -->
    <SectionCard :title="t.addAcctProxy.px.assignTitle"
                 :note="t.addAcctProxy.px.assignNote">
      <EmptyState v-if="usableAccounts.length === 0" icon="⬡" :title="t.addAcctProxy.px.noAccounts"
                  :sub="t.addAcctProxy.px.noAccountsSub" />

      <div v-for="a in usableAccounts" v-else :key="a.id" class="asg">
        <span class="asg__bar" :style="{ background: acctColor(a.colorIndex) }" />
        <AccountChip :account="a" variant="full" />

        <span class="asg__spacer" />

        <span v-if="bindings[a.id]" class="mono t-2xs dim-3 asg__cur">
          {{ display(list.find(p => p.id === bindings[a.id])!) }}
        </span>
        <span v-else class="t-2xs dim-3 asg__cur">{{ t.addAcctProxy.px.directShort }}</span>

        <SelectMenu :model-value="bindings[a.id] ?? ''" :groups="optionsFor(a.id)"
                    :placeholder="t.addAcctProxy.px.pickProxy" :aria-label="t.addAcctProxy.px.proxyOf(a.alias)" :min-width="240"
                    @update:model-value="v => bind(a.id, String(v))" />
      </div>
    </SectionCard>
  </div>
</template>

<style scoped>
.warn-box { margin-bottom: 16px; }
.page > .card + .card { margin-top: 16px; }
.pad { padding: 16px 20px; }

/* ---- 导入 ---- */
/* resize 覆盖 .textarea 的 none：代理列表长短差别很大，
   拖高一点比在小窗口里滚动舒服。 */
.imp__box { width: 100%; resize: vertical; }
.imp__actions { display: flex; align-items: center; gap: 12px; margin-top: 12px; }
.imp__rows { margin-top: 16px; border-top: 1px solid var(--border-subtle); }
.imp__row {
  display: flex; align-items: center; gap: 10px;
  height: 30px; border-bottom: 1px solid var(--border-subtle);
}
.imp__row:last-child { border-bottom: none; }
.imp__line { width: 28px; text-align: right; flex: 0 0 auto; }
.imp__addr { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.imp__addr.is-bad { color: var(--danger); }
.imp__spacer { flex: 1 1 auto; min-width: 8px; }
.imp__err { color: var(--danger); }

/* ---- 代理行 ---- */
.px {
  display: flex; align-items: center; gap: 12px;
  height: 46px; padding: 0 16px;
  border-bottom: 1px solid var(--border-subtle);
}
.px:last-child { border-bottom: none; }
/* 停用的压暗但不隐藏——藏起来用户会以为被删了 */
.px.is-off { opacity: 0.5; }
.px__dot { width: 7px; height: 7px; border-radius: var(--radius-full); flex: 0 0 auto; }
.px__status { font-size: var(--t-2xs); font-weight: 600; width: 44px; flex: 0 0 auto; }
.px__label {
  width: 110px; flex: 0 0 auto; font-size: 13px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.px__addr {
  width: 210px; flex: 0 0 auto;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.px__lat { width: 56px; text-align: right; flex: 0 0 auto; }
.px__bound { width: 108px; flex: 0 0 auto; }
.px__spacer { flex: 1 1 auto; min-width: 8px; }
.px__err {
  color: var(--danger); max-width: 240px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.px__when { width: 96px; text-align: right; flex: 0 0 auto; }

/* ---- 分配 ---- */
.asg {
  position: relative; display: flex; align-items: center; gap: 12px;
  height: 52px; padding: 0 16px 0 17px;
  border-bottom: 1px solid var(--border-subtle);
}
.asg:last-child { border-bottom: none; }
.asg__bar { position: absolute; left: 0; top: 0; bottom: 0; width: 3px; }
.asg__spacer { flex: 1 1 auto; min-width: 8px; }
.asg__cur {
  width: 220px; text-align: right; flex: 0 0 auto;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

@media (max-width: 1100px) {
  .px__label, .px__when { display: none; }
  .asg__cur { display: none; }
}
</style>
