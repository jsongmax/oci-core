<script setup lang="ts">
/**
 * OCI 官方云端审计日志面板（设置 → 审计日志 → OCI 云端审计）。
 *
 * 严格纯只读，对接：
 *   - GET /api/accounts/{id}/regions
 *   - GET /api/accounts/{id}/compartments
 *   - GET /api/accounts/{id}/cloud-audit
 *
 * 安全红线：所有来自 OCI 的文本与 JSON 均采用纯文本插值渲染，绝不使用 v-html。
 */
import { computed, onMounted, ref, watch } from 'vue'
import { t, fmtDateTime } from '@/i18n'
import { useStore } from '@/store'
import {
  accounts as accountsApi,
  ApiError,
  errorText,
  type CloudAuditEventDTO,
  type CloudAuditResponseDTO,
  type CompartmentDTO
} from '@/api'
import { acctColor, copy } from '@/lib/format'
import { auditTarget } from '@/lib/audit'
import SectionCard from '@/components/SectionCard.vue'
import SelectMenu, { type SelectGroup } from '@/components/SelectMenu.vue'
import MaskedText from '@/components/MaskedText.vue'
import CodeBlock from '@/components/CodeBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import LoadingBar from '@/components/LoadingBar.vue'

const { state, accountById, openDrawer, toast } = useStore()

const ACCOUNT_KEY = 'oci.cloudAudit.account'
const AUDIT_POLICY = `Allow group 'OCI Core' to read audit-events in tenancy`

interface RegionOption {
  regionName: string
  isHomeRegion: boolean
}

const usableAccounts = computed(() => state.accounts.filter(a => a.status !== 'disabled'))

function initialAccountId(): string {
  try {
    const saved = localStorage.getItem(ACCOUNT_KEY) ?? ''
    if (usableAccounts.value.some(a => a.id === saved)) return saved
  } catch { /* 隐私模式下忽略 localStorage 异常 */ }
  return usableAccounts.value[0]?.id ?? ''
}

const accountId = ref(initialAccountId())
const selectedAccount = computed(() => (accountId.value ? accountById(accountId.value) : undefined))

const regions = ref<RegionOption[]>([])
const region = ref('')
const homeRegion = ref('')

const compartments = ref<CompartmentDTO[]>([])
const compartmentId = ref('')
const compartmentsPartial = ref(false)
const metaLoading = ref(false)

/* ---------- 时间窗 ---------- */

type WindowPreset = '1' | '6' | '24' | '168' | '336' | 'custom'
const PRESETS: WindowPreset[] = ['1', '6', '24', '168', '336', 'custom']
const windowPreset = ref<WindowPreset>('24')

function toLocalInputValue(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

const customStart = ref('')
const customEnd = ref('')
const customValidationError = ref('')

function seedCustomRange() {
  const now = new Date()
  const start = new Date(now.getTime() - 24 * 3600_000)
  customStart.value = toLocalInputValue(start)
  customEnd.value = toLocalInputValue(now)
  customValidationError.value = ''
}

/* ---------- 审计数据与分页 ---------- */

const events = ref<CloudAuditEventDTO[]>([])
const loading = ref(false)
const loadingMore = ref(false)
const loadError = ref<ApiError | Error | null>(null)
const nextPage = ref('')
const hasMore = ref(false)
const retentionDays = ref(365)
const maxSpanDays = ref(31)
const responseNotice = ref('')
const activeWindow = ref<{ startTime: string; endTime: string; region: string; compartmentId: string } | null>(null)

/** 翻页与竞态保护代次 */
let metaSeq = 0
let querySeq = 0

/* ---------- 本地筛选与展开 ---------- */

const keyword = ref('')
const onlyWrite = ref(false)
const onlyError = ref(false)
const descOrder = ref(false)
const expanded = ref<Set<string>>(new Set())

/* ---------- 下拉选项 ---------- */

const accountGroups = computed<SelectGroup[]>(() => [{
  label: '',
  options: usableAccounts.value.map(a => ({
    value: a.id,
    label: `${a.code} · ${a.alias}`,
    dot: acctColor(a.colorIndex)
  }))
}])

const regionGroups = computed<SelectGroup[]>(() => [{
  label: '',
  options: regions.value.map(r => ({
    value: r.regionName,
    label: r.isHomeRegion ? `${r.regionName} (${t.cloudAudit.homeTag})` : r.regionName
  }))
}])

const compartmentGroups = computed<SelectGroup[]>(() => [{
  label: '',
  options: compartments.value.map(c => ({
    value: c.id,
    label: c.isRoot ? `${c.name} (${t.cloudAudit.rootTag})` : c.name
  }))
}])

const isHomeRegionSelected = computed(() =>
  !homeRegion.value || !region.value || region.value === homeRegion.value
)

/* ---------- 加载账号元数据（区域 + 区间） ---------- */

async function loadAccountMeta(id: string) {
  const mine = ++metaSeq
  metaLoading.value = true
  regions.value = []
  compartments.value = []
  region.value = ''
  homeRegion.value = ''
  compartmentId.value = ''
  compartmentsPartial.value = false
  events.value = []
  nextPage.value = ''
  hasMore.value = false
  activeWindow.value = null
  loadError.value = null
  expanded.value = new Set()

  const acct = accountById(id)

  const [regRes, compRes] = await Promise.allSettled([
    accountsApi.regions(id),
    accountsApi.compartments(id)
  ])
  if (mine !== metaSeq) return

  if (regRes.status === 'fulfilled' && regRes.value.regions?.length) {
    // 主区域排最前，方便快速选择
    const sorted = [...regRes.value.regions].sort((a, b) => Number(b.isHomeRegion) - Number(a.isHomeRegion))
    regions.value = sorted
    const home = sorted.find(r => r.isHomeRegion)
    homeRegion.value = home?.regionName ?? ''
    region.value = home?.regionName ?? sorted[0].regionName
  } else {
    const fallback = (acct?.regions ?? []).map((name, idx) => ({ regionName: name, isHomeRegion: idx === 0 }))
    regions.value = fallback
    region.value = fallback[0]?.regionName ?? ''
    homeRegion.value = ''
  }

  if (compRes.status === 'fulfilled' && compRes.value.compartments?.length) {
    compartments.value = compRes.value.compartments
    compartmentsPartial.value = !!compRes.value.partial
    const root = compRes.value.compartments.find(c => c.isRoot)
    compartmentId.value = root?.id ?? compRes.value.compartments[0].id
  } else {
    compartments.value = []
    compartmentId.value = ''
    compartmentsPartial.value = false
  }

  metaLoading.value = false
  await loadEvents(false)
}

/* ---------- 校验自定义时间窗 ---------- */

function validateCustomRange(): { startIso: string; endIso: string } | null {
  customValidationError.value = ''
  if (!customStart.value || !customEnd.value) {
    customValidationError.value = t.cloudAudit.errTimeIncomplete
    return null
  }
  const start = new Date(customStart.value)
  const end = new Date(customEnd.value)
  if (Number.isNaN(start.getTime()) || Number.isNaN(end.getTime()) || start.getTime() >= end.getTime()) {
    customValidationError.value = t.cloudAudit.errTimeOrder
    return null
  }
  const spanMs = end.getTime() - start.getTime()
  if (spanMs > maxSpanDays.value * 86_400_000) {
    customValidationError.value = t.cloudAudit.errSpanLimit(maxSpanDays.value)
    return null
  }
  const oldestAllowed = Date.now() - retentionDays.value * 86_400_000
  if (start.getTime() < oldestAllowed) {
    customValidationError.value = t.cloudAudit.errRetentionLimit(retentionDays.value)
    return null
  }
  return { startIso: start.toISOString(), endIso: end.toISOString() }
}

/* ---------- 查询云端审计日志 ---------- */

async function loadEvents(append = false) {
  const id = accountId.value
  if (!id) return

  let query: {
    region?: string
    compartmentId?: string
    startTime?: string
    endTime?: string
    hours?: number
    page?: string
  }

  if (append) {
    if (!nextPage.value || !activeWindow.value) return
    // 契约要求：翻页传 page 时与首页响应中的 region/compartmentId/startTime/endTime 保持一致
    query = {
      region: activeWindow.value.region || undefined,
      compartmentId: activeWindow.value.compartmentId || undefined,
      startTime: activeWindow.value.startTime,
      endTime: activeWindow.value.endTime,
      page: nextPage.value
    }
  } else {
    if (windowPreset.value === 'custom') {
      const range = validateCustomRange()
      if (!range) return
      query = {
        region: region.value || undefined,
        compartmentId: compartmentId.value || undefined,
        startTime: range.startIso,
        endTime: range.endIso
      }
    } else {
      query = {
        region: region.value || undefined,
        compartmentId: compartmentId.value || undefined,
        hours: Number(windowPreset.value)
      }
    }
  }

  const mine = ++querySeq
  if (append) {
    loadingMore.value = true
  } else {
    loading.value = true
    loadError.value = null
  }

  try {
    const res: CloudAuditResponseDTO = await accountsApi.cloudAudit(id, query)
    if (mine !== querySeq) return

    retentionDays.value = res.retentionDays || 365
    maxSpanDays.value = res.maxSpanDays || 31
    responseNotice.value = res.notice || ''
    if (res.homeRegion) homeRegion.value = res.homeRegion
    if (!region.value && res.region) region.value = res.region

    activeWindow.value = {
      startTime: res.startTime,
      endTime: res.endTime,
      region: res.region,
      compartmentId: res.compartmentId
    }

    const incoming = res.events ?? []
    if (append) {
      const seen = new Set(events.value.map(e => e.eventId))
      const merged = [...events.value]
      for (const item of incoming) {
        if (!item.eventId || !seen.has(item.eventId)) {
          merged.push(item)
          if (item.eventId) seen.add(item.eventId)
        }
      }
      events.value = merged
    } else {
      events.value = incoming
      expanded.value = new Set()
    }

    nextPage.value = res.nextPage || ''
    hasMore.value = !!res.hasMore && !!res.nextPage
  } catch (err) {
    if (mine !== querySeq) return
    if (append) {
      toast({ tone: 'danger', title: t.cloudAudit.errTitle, body: errorText(err) })
    } else {
      events.value = []
      nextPage.value = ''
      hasMore.value = false
      loadError.value = err instanceof Error ? err : new Error(String(err))
    }
  } finally {
    if (mine === querySeq) {
      loading.value = false
      loadingMore.value = false
    }
  }
}

/* ---------- 交互回调 ---------- */

function onAccountChange(nextId: string) {
  if (!nextId || nextId === accountId.value) return
  accountId.value = nextId
  try { localStorage.setItem(ACCOUNT_KEY, nextId) } catch { /* 忽略 */ }
  void loadAccountMeta(nextId)
}

function onRegionChange(nextRegion: string) {
  if (nextRegion === region.value) return
  region.value = nextRegion
  void loadEvents(false)
}

function onCompartmentChange(nextComp: string) {
  if (nextComp === compartmentId.value) return
  compartmentId.value = nextComp
  void loadEvents(false)
}

function switchToHomeRegion() {
  if (!homeRegion.value || region.value === homeRegion.value) return
  region.value = homeRegion.value
  void loadEvents(false)
}

function pickPreset(p: WindowPreset) {
  windowPreset.value = p
  customValidationError.value = ''
  if (p === 'custom') {
    if (!customStart.value || !customEnd.value) seedCustomRange()
    return
  }
  void loadEvents(false)
}

function toggleExpand(eventId: string) {
  const next = new Set(expanded.value)
  if (next.has(eventId)) next.delete(eventId)
  else next.add(eventId)
  expanded.value = next
}

function clearLocalFilters() {
  keyword.value = ''
  onlyWrite.value = false
  onlyError.value = false
}

/* ---------- 派生数据与筛选 ---------- */

const hasLocalFilter = computed(() =>
  keyword.value.trim().length > 0 || onlyWrite.value || onlyError.value
)

const filteredEvents = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  const list = events.value.filter(ev => {
    if (onlyWrite.value && !ev.isWrite) return false
    if (onlyError.value && !ev.isError) return false
    if (!kw) return true
    const hay = [
      ev.eventName, ev.eventType, ev.source,
      ev.resourceName, ev.resourceId,
      ev.compartmentName, ev.compartmentId,
      ev.principalName, ev.principalId,
      ev.callerName, ev.callerId,
      ev.ipAddress, ev.path, ev.action,
      ev.status, ev.message, ev.userAgent
    ].join(' ').toLowerCase()
    return hay.includes(kw)
  })
  return descOrder.value ? [...list].reverse() : list
})

const PERM_OCI_CODES = new Set(['NotAuthorizedOrNotFound', 'NotAuthorized', 'NotFound'])

const isPermissionError = computed(() => {
  const err = loadError.value
  if (!(err instanceof ApiError)) return false
  if (err.ociCode && PERM_OCI_CODES.has(err.ociCode)) return true
  // 极端情况下 OCI 仅返回 403 且未携带 ociCode，后端 writeOCIErrorWithPermHint 会附带 read audit-events 策略建议
  return err.status === 403 && err.code === 'oci_error' && !err.ociCode && (err.advice ?? '').includes('audit-events')
})

const errorOciCode = computed(() =>
  loadError.value instanceof ApiError ? (loadError.value.ociCode || loadError.value.code) : ''
)

const errorAdviceText = computed(() =>
  loadError.value instanceof ApiError ? (loadError.value.advice || '') : ''
)

const errorMessageText = computed(() =>
  loadError.value ? loadError.value.message : ''
)

/* ---------- 格式化工具 ---------- */

function formatTime(iso: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : fmtDateTime(d).replace(/\//g, '-')
}

function resourceDisplay(ev: CloudAuditEventDTO): { text: string; isOcid: boolean; raw: string } {
  if (ev.resourceName) return { text: ev.resourceName, isOcid: false, raw: ev.resourceId || ev.resourceName }
  if (ev.resourceId) return { text: auditTarget(ev.resourceId), isOcid: true, raw: ev.resourceId }
  if (ev.compartmentName) return { text: ev.compartmentName, isOcid: false, raw: ev.compartmentId || ev.compartmentName }
  return { text: '—', isOcid: false, raw: '' }
}

function principalDisplay(ev: CloudAuditEventDTO): { value: string; kind: 'email' | 'ocid' | 'generic'; plain: boolean } {
  const raw = ev.principalName || ev.callerName || ev.principalId || ev.callerId || ''
  if (!raw) return { value: '—', kind: 'generic', plain: true }
  if (raw.includes('@')) return { value: raw, kind: 'email', plain: false }
  if (raw.startsWith('ocid1.')) return { value: raw, kind: 'ocid', plain: false }
  return { value: raw, kind: 'generic', plain: true }
}

function statusTone(ev: CloudAuditEventDTO): string {
  if (ev.isError || (ev.statusCode && ev.statusCode >= 400)) return 'var(--danger)'
  if (ev.statusCode && ev.statusCode >= 200 && ev.statusCode < 300) return 'var(--success)'
  return 'var(--text-secondary)'
}

function actionBadgeClass(action: string): string {
  const up = (action || '').toUpperCase()
  if (up === 'DELETE') return 'act--danger'
  if (up === 'POST' || up === 'PUT' || up === 'PATCH') return 'act--warn'
  return 'act--read'
}

function detailJson(ev: CloudAuditEventDTO): string {
  if (!ev.detail) return ''
  try {
    return JSON.stringify(ev.detail, null, 2)
  } catch {
    return ''
  }
}

async function copyCode(val: string) {
  if (!val) return
  await copy(val)
  toast({ tone: 'accent', title: t.common.copied })
}

/* ---------- 监听账号列表变化（首次刷新场景） ---------- */

watch(usableAccounts, list => {
  if (!accountId.value && list.length > 0) {
    accountId.value = list[0].id
    void loadAccountMeta(accountId.value)
  }
})

onMounted(() => {
  if (accountId.value) {
    void loadAccountMeta(accountId.value)
  }
})
</script>

<template>
  <SectionCard :title="t.cloudAudit.title"
               :note="activeWindow ? t.cloudAudit.summaryWindow(formatTime(activeWindow.startTime), formatTime(activeWindow.endTime)) : undefined">
    <template #action>
      <button class="btn btn--sm" :disabled="loading || metaLoading || !accountId" @click="loadEvents(false)">
        <span v-if="loading" class="btn-spin" aria-hidden="true" />{{ t.common.refresh }}
      </button>
    </template>

    <!-- 365 天保留期与主区域醒目提示 -->
    <div class="ca-banners">
      <div class="ca-banner ca-banner--info">
        <span class="ca-banner__icon" aria-hidden="true">ℹ</span>
        <div class="ca-banner__body">
          <strong>{{ t.cloudAudit.retentionBanner(retentionDays) }}</strong>
          <span class="ca-banner__sub">{{ t.cloudAudit.homeRegionHint }}</span>
        </div>
      </div>

      <div v-if="accountId && !isHomeRegionSelected && homeRegion" class="ca-banner ca-banner--warn">
        <span class="ca-banner__icon" aria-hidden="true">⚠</span>
        <span class="ca-banner__text">{{ t.cloudAudit.notHomeRegionTip(homeRegion) }}</span>
        <button class="btn btn--sm btn--warning" :disabled="loading" @click="switchToHomeRegion()">
          {{ t.cloudAudit.switchToHome }}
        </button>
      </div>
    </div>

    <!-- 无账号空态 -->
    <EmptyState v-if="usableAccounts.length === 0"
                :title="t.cloudAudit.noAccountsTitle"
                :sub="t.cloudAudit.noAccountsSub"
                :action-label="t.cloudAudit.goAccounts"
                @action="openDrawer({ kind: 'add-account' })" />

    <template v-else>
      <!-- 查询条件工具栏 -->
      <div class="ca-toolbar">
        <div class="ca-controls">
          <div class="ca-control">
            <span class="ca-control__label">{{ t.cloudAudit.accountLabel }}</span>
            <SelectMenu :model-value="accountId" :groups="accountGroups" :min-width="190"
                        :aria-label="t.cloudAudit.accountLabel"
                        @update:model-value="onAccountChange" />
          </div>

          <div class="ca-control">
            <span class="ca-control__label">{{ t.cloudAudit.regionLabel }}</span>
            <SelectMenu :model-value="region" :groups="regionGroups" :min-width="200"
                        :disabled="metaLoading || regions.length === 0"
                        :aria-label="t.cloudAudit.regionLabel"
                        @update:model-value="onRegionChange" />
          </div>

          <div v-if="compartments.length > 0" class="ca-control">
            <span class="ca-control__label">{{ t.cloudAudit.compartmentLabel }}</span>
            <SelectMenu :model-value="compartmentId" :groups="compartmentGroups" :min-width="190"
                        :disabled="metaLoading"
                        :aria-label="t.cloudAudit.compartmentLabel"
                        @update:model-value="onCompartmentChange" />
          </div>
        </div>

        <p v-if="compartmentsPartial" class="ca-partial t-2xs dim-3">
          {{ t.cloudAudit.partialCompartments }}
        </p>

        <!-- 时间窗快捷选择 -->
        <div class="ca-row-bar">
          <span class="ca-control__label">{{ t.cloudAudit.windowLabel }}</span>
          <div class="ca-presets" role="group" :aria-label="t.cloudAudit.windowLabel">
            <button v-for="p in PRESETS" :key="p" type="button" class="pill ca-pill"
                    :class="{ 'is-active': windowPreset === p }"
                    :disabled="loading && windowPreset !== p"
                    @click="pickPreset(p)">
              {{ t.cloudAudit.presets[p] }}
            </button>
          </div>
        </div>

        <!-- 自定义时间窗输入 -->
        <div v-if="windowPreset === 'custom'" class="ca-custom">
          <label class="ca-custom__field">
            <span class="t-2xs dim">{{ t.cloudAudit.startTime }}</span>
            <input v-model="customStart" type="datetime-local" class="input ca-dt mono" />
          </label>
          <span class="dim-3">~</span>
          <label class="ca-custom__field">
            <span class="t-2xs dim">{{ t.cloudAudit.endTime }}</span>
            <input v-model="customEnd" type="datetime-local" class="input ca-dt mono" />
          </label>
          <button class="btn btn--sm btn--primary" :disabled="loading" @click="loadEvents(false)">
            {{ t.cloudAudit.applyRange }}
          </button>
          <span v-if="customValidationError" class="ca-custom__err t-xs">{{ customValidationError }}</span>
        </div>

        <!-- 本地筛选与统计栏 -->
        <div class="ca-filters">
          <input v-model="keyword" type="search" class="input ca-search"
                 :placeholder="t.cloudAudit.searchPlaceholder" />
          <button type="button" class="pill ca-pill" :class="{ 'is-active': onlyWrite }"
                  @click="onlyWrite = !onlyWrite">
            {{ t.cloudAudit.onlyWrite }}
          </button>
          <button type="button" class="pill ca-pill" :class="{ 'is-active': onlyError }"
                  @click="onlyError = !onlyError">
            {{ t.cloudAudit.onlyError }}
          </button>
          <button type="button" class="btn btn--sm btn--ghost"
                  @click="descOrder = !descOrder">
            {{ descOrder ? t.cloudAudit.orderDesc : t.cloudAudit.orderAsc }}
          </button>
          <button v-if="hasLocalFilter" type="button" class="btn btn--sm btn--ghost"
                  @click="clearLocalFilters">
            {{ t.cloudAudit.clearFilters }}
          </button>
          <span v-if="!loading && !loadError" class="ca-count mono t-2xs dim-3">
            {{ t.cloudAudit.summaryLoaded(filteredEvents.length, events.length, hasLocalFilter) }}
          </span>
        </div>
      </div>

      <LoadingBar :active="(loading || metaLoading) && events.length > 0" />

      <!-- 首次加载骨架屏（§10.1 禁止用居中转圈代替） -->
      <div v-if="(loading || metaLoading) && events.length === 0" class="ca-skeleton">
        <p class="skeleton-hint">
          <span class="dot-pulse" aria-hidden="true" />
          <span>{{ t.cloudAudit.loadingHint(region || selectedAccount?.regions[0] || 'OCI') }}</span>
        </p>
        <SkeletonRows :rows="6" :height="48" />
      </div>

      <!-- 缺权限专属提示 -->
      <div v-else-if="isPermissionError" class="ca-error ca-error--perm">
        <div class="ca-error__head">
          <span class="ca-error__icon" aria-hidden="true">⚠</span>
          <strong>{{ t.cloudAudit.noPermTitle }}</strong>
          <button v-if="errorOciCode" type="button" class="ca-code mono" @click="copyCode(errorOciCode)">
            {{ errorOciCode }}
          </button>
        </div>
        <p class="t-xs dim">{{ t.cloudAudit.noPermBody }}</p>
        <p v-if="errorAdviceText" class="t-xs ca-error__advice">{{ errorAdviceText }}</p>
        <CodeBlock copyable :code="AUDIT_POLICY" />
        <p class="t-2xs dim-3">{{ t.cloudAudit.noPermLocation }}</p>
        <div class="ca-error__actions">
          <button class="btn btn--sm" @click="loadEvents(false)">{{ t.cloudAudit.retry }}</button>
        </div>
      </div>

      <!-- 一般错误态（保留原始错误码 + 重试按钮） -->
      <div v-else-if="loadError" class="ca-error">
        <div class="ca-error__head">
          <span class="ca-error__icon" aria-hidden="true">✕</span>
          <strong>{{ t.cloudAudit.errTitle }}</strong>
          <button v-if="errorOciCode" type="button" class="ca-code mono" @click="copyCode(errorOciCode)">
            {{ errorOciCode }}
          </button>
        </div>
        <p class="t-xs dim">{{ errorMessageText }}</p>
        <p v-if="errorAdviceText" class="t-xs ca-error__advice">{{ errorAdviceText }}</p>
        <div class="ca-error__actions">
          <button class="btn btn--sm" @click="loadEvents(false)">{{ t.cloudAudit.retry }}</button>
        </div>
      </div>

      <!-- 服务端返回空态 -->
      <EmptyState v-else-if="events.length === 0"
                  :title="t.cloudAudit.emptyTitle"
                  :sub="!isHomeRegionSelected && homeRegion ? t.cloudAudit.emptyNotHomeSub(homeRegion) : t.cloudAudit.emptySub"
                  :action-label="!isHomeRegionSelected && homeRegion ? t.cloudAudit.switchToHome : undefined"
                  @action="switchToHomeRegion" />

      <!-- 本地筛选无匹配空态 -->
      <EmptyState v-else-if="filteredEvents.length === 0"
                  :title="t.cloudAudit.emptyFilteredTitle"
                  :sub="t.cloudAudit.emptyFilteredSub"
                  :action-label="t.cloudAudit.clearFilters"
                  @action="clearLocalFilters" />

      <!-- 事件列表 -->
      <div v-else class="stale-able" :class="{ 'is-stale': loading }">
        <div class="ca-head ca-cols">
          <span aria-hidden="true" />
          <span>{{ t.cloudAudit.colTime }}</span>
          <span>{{ t.cloudAudit.colEvent }}</span>
          <span>{{ t.cloudAudit.colActionStatus }}</span>
          <span>{{ t.cloudAudit.colResource }}</span>
          <span>{{ t.cloudAudit.colPrincipal }}</span>
          <span class="ca-col-ip">{{ t.cloudAudit.colIp }}</span>
        </div>

        <div v-for="ev in filteredEvents" :key="ev.eventId" class="ca-entry"
             :class="{ 'is-open': expanded.has(ev.eventId), 'is-err': ev.isError }">
          <div role="button" tabindex="0" class="ca-row ca-cols"
               :aria-expanded="expanded.has(ev.eventId)"
               :aria-label="expanded.has(ev.eventId) ? t.cloudAudit.collapseRow : t.cloudAudit.expandRow"
               @click="toggleExpand(ev.eventId)"
               @keydown.enter.prevent="toggleExpand(ev.eventId)"
               @keydown.space.prevent="toggleExpand(ev.eventId)">
            <span class="acct-bar" :style="{ background: selectedAccount ? acctColor(selectedAccount.colorIndex) : 'var(--border-default)' }" />
            <span class="ca-chev" :class="{ 'is-open': expanded.has(ev.eventId) }" aria-hidden="true">›</span>
            <span class="mono t-xs dim">{{ formatTime(ev.eventTime) }}</span>
            <span class="ca-event">
              <span class="ca-event__main">
                <span class="ca-event__name mono">{{ ev.eventName || ev.eventType || '—' }}</span>
                <span v-if="ev.isWrite" class="ca-write">{{ t.cloudAudit.writeBadge }}</span>
              </span>
              <span class="ca-event__src t-2xs dim-3 mono">{{ ev.source || ev.eventType }}</span>
            </span>
            <span class="ca-act">
              <span class="ca-act__badge mono" :class="actionBadgeClass(ev.action)">{{ ev.action || '—' }}</span>
              <span class="mono t-xs ca-status" :style="{ color: statusTone(ev) }">{{ ev.status || (ev.statusCode ? String(ev.statusCode) : '—') }}</span>
            </span>
            <span class="ca-cell mono t-xs dim" :title="resourceDisplay(ev).raw">
              <MaskedText v-if="resourceDisplay(ev).isOcid" :value="resourceDisplay(ev).raw" kind="ocid" />
              <template v-else>{{ resourceDisplay(ev).text }}</template>
            </span>
            <span class="ca-cell t-xs dim" :class="{ mono: principalDisplay(ev).kind === 'ocid' }">
              <MaskedText :value="principalDisplay(ev).value"
                          :kind="principalDisplay(ev).kind"
                          :plain="principalDisplay(ev).plain" />
            </span>
            <span class="ca-cell ca-col-ip mono t-xs dim-3">
              <MaskedText :value="ev.ipAddress || '—'" kind="ip" />
            </span>
          </div>

          <!-- 展开详情面板（严格纯文本渲染，无 v-html） -->
          <div v-if="expanded.has(ev.eventId)" class="ca-detail">
            <div class="ca-detail__grid">
              <div class="ca-kv">
                <span class="ca-kv__k">{{ t.cloudAudit.detailEventType }}</span>
                <span class="ca-kv__v mono">{{ ev.eventType || '—' }}</span>
              </div>
              <div class="ca-kv">
                <span class="ca-kv__k">{{ t.cloudAudit.detailMethodPath }}</span>
                <span class="ca-kv__v mono">{{ [ev.action, ev.path].filter(Boolean).join(' ') || '—' }}</span>
              </div>
              <div class="ca-kv">
                <span class="ca-kv__k">{{ t.cloudAudit.detailStatus }}</span>
                <span class="ca-kv__v mono" :style="{ color: statusTone(ev) }">
                  {{ ev.status || (ev.statusCode ? String(ev.statusCode) : '—') }}
                </span>
              </div>
              <div class="ca-kv">
                <span class="ca-kv__k">{{ t.cloudAudit.detailResponseTime }}</span>
                <span class="ca-kv__v mono">{{ formatTime(ev.responseTime) }}</span>
              </div>
              <div class="ca-kv">
                <span class="ca-kv__k">{{ t.cloudAudit.detailResource }}</span>
                <span class="ca-kv__v">{{ ev.resourceName || '—' }}</span>
              </div>
              <div class="ca-kv">
                <span class="ca-kv__k">{{ t.cloudAudit.detailResourceId }}</span>
                <span class="ca-kv__v mono">
                  <MaskedText v-if="ev.resourceId" :value="ev.resourceId" kind="ocid" copyable />
                  <template v-else>—</template>
                </span>
              </div>
              <div class="ca-kv">
                <span class="ca-kv__k">{{ t.cloudAudit.detailCompartment }}</span>
                <span class="ca-kv__v">{{ ev.compartmentName || '—' }}</span>
              </div>
              <div class="ca-kv">
                <span class="ca-kv__k">{{ t.cloudAudit.detailCompartmentId }}</span>
                <span class="ca-kv__v mono">
                  <MaskedText v-if="ev.compartmentId" :value="ev.compartmentId" kind="ocid" copyable />
                  <template v-else>—</template>
                </span>
              </div>
              <div class="ca-kv">
                <span class="ca-kv__k">{{ t.cloudAudit.detailPrincipal }}</span>
                <span class="ca-kv__v">
                  <MaskedText v-if="ev.principalName"
                              :value="ev.principalName"
                              :kind="ev.principalName.includes('@') ? 'email' : 'generic'"
                              :plain="!ev.principalName.includes('@')"
                              copyable />
                  <template v-else>—</template>
                </span>
              </div>
              <div class="ca-kv">
                <span class="ca-kv__k">{{ t.cloudAudit.detailPrincipalId }}</span>
                <span class="ca-kv__v mono">
                  <MaskedText v-if="ev.principalId" :value="ev.principalId" kind="ocid" copyable />
                  <template v-else>—</template>
                </span>
              </div>
              <div v-if="ev.callerName || ev.callerId" class="ca-kv">
                <span class="ca-kv__k">{{ t.cloudAudit.detailCaller }}</span>
                <span class="ca-kv__v mono">
                  <MaskedText :value="ev.callerName || ev.callerId"
                              :kind="(ev.callerName || ev.callerId).startsWith('ocid1.') ? 'ocid' : 'generic'"
                              :plain="!(ev.callerName || ev.callerId).startsWith('ocid1.')"
                              copyable />
                </span>
              </div>
              <div class="ca-kv">
                <span class="ca-kv__k">{{ t.cloudAudit.detailIp }}</span>
                <span class="ca-kv__v mono">
                  <MaskedText :value="ev.ipAddress || '—'" kind="ip" :copyable="!!ev.ipAddress" />
                </span>
              </div>
              <div class="ca-kv">
                <span class="ca-kv__k">{{ t.cloudAudit.detailAuthType }}</span>
                <span class="ca-kv__v mono">{{ ev.authType || '—' }}</span>
              </div>
              <div v-if="ev.availabilityDomain" class="ca-kv">
                <span class="ca-kv__k">{{ t.cloudAudit.detailAd }}</span>
                <span class="ca-kv__v mono">{{ ev.availabilityDomain }}</span>
              </div>
              <div class="ca-kv">
                <span class="ca-kv__k">{{ t.cloudAudit.detailEventId }}</span>
                <span class="ca-kv__v mono">
                  <MaskedText :value="ev.eventId || '—'" plain :copyable="!!ev.eventId" />
                </span>
              </div>
              <div class="ca-kv">
                <span class="ca-kv__k">{{ t.cloudAudit.detailRequestId }}</span>
                <span class="ca-kv__v mono">
                  <MaskedText :value="ev.requestId || '—'" plain :copyable="!!ev.requestId" />
                </span>
              </div>
              <div v-if="ev.eventGroupingId" class="ca-kv ca-kv--wide">
                <span class="ca-kv__k">{{ t.cloudAudit.detailGroupingId }}</span>
                <span class="ca-kv__v mono">{{ ev.eventGroupingId }}</span>
              </div>
              <div class="ca-kv ca-kv--wide">
                <span class="ca-kv__k">{{ t.cloudAudit.detailUserAgent }}</span>
                <span class="ca-kv__v mono ca-wrap">{{ ev.userAgent || '—' }}</span>
              </div>
              <div v-if="ev.message" class="ca-kv ca-kv--wide">
                <span class="ca-kv__k">{{ t.cloudAudit.detailMessage }}</span>
                <span class="ca-kv__v ca-wrap" :class="{ 'ca-msg-err': ev.isError }">{{ ev.message }}</span>
              </div>
            </div>

            <div v-if="detailJson(ev)" class="ca-detail__json">
              <div class="ca-kv__k">{{ t.cloudAudit.detailRawJson }}</div>
              <CodeBlock copyable :code="detailJson(ev)" />
            </div>
          </div>
        </div>

        <!-- 分页底栏 -->
        <div class="ca-more">
          <button v-if="hasMore" class="btn btn--sm" :disabled="loadingMore || loading" @click="loadEvents(true)">
            <span v-if="loadingMore" class="btn-spin" aria-hidden="true" />
            {{ loadingMore ? t.cloudAudit.loadingMore : t.cloudAudit.loadMore }}
          </button>
          <span v-else class="t-2xs dim-3">{{ t.cloudAudit.noMore }}</span>
          <span v-if="responseNotice" class="ca-notice t-2xs dim-3">{{ responseNotice }}</span>
        </div>
      </div>
    </template>
  </SectionCard>
</template>

<style scoped>
.ca-banners {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px 16px 0;
}
.ca-banner {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 12px;
  border-radius: var(--radius-md);
  font-size: 12px;
  line-height: 18px;
}
.ca-banner--info {
  background: var(--info-soft);
  border: 1px solid color-mix(in srgb, var(--info) 30%, transparent);
  color: var(--text-primary);
}
.ca-banner--warn {
  background: var(--warning-soft);
  border: 1px solid color-mix(in srgb, var(--warning) 35%, transparent);
  color: var(--text-primary);
}
.ca-banner__icon { flex: 0 0 auto; font-size: 13px; color: var(--info); }
.ca-banner--warn .ca-banner__icon { color: var(--warning); }
.ca-banner__body { display: flex; flex-wrap: wrap; gap: 4px 10px; align-items: baseline; flex: 1 1 auto; }
.ca-banner__sub { font-size: 11px; color: var(--text-secondary); }
.ca-banner__text { flex: 1 1 auto; min-width: 0; }

.ca-toolbar {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px 16px 14px;
  border-bottom: 1px solid var(--border-subtle);
}
.ca-controls {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px 14px;
}
.ca-control {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
.ca-control__label {
  font-size: 11px;
  font-weight: 500;
  color: var(--text-tertiary);
  white-space: nowrap;
}
.ca-partial { margin: 0; }

.ca-row-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 10px;
}
.ca-presets { display: flex; flex-wrap: wrap; gap: 6px; }
.ca-pill { height: 28px; padding: 0 10px; font-size: 11px; }

.ca-custom {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  border-radius: var(--radius-md);
  background: var(--bg-inset);
  border: 1px solid var(--border-subtle);
}
.ca-custom__field {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.ca-dt { height: 30px; font-size: 12px; padding: 0 8px; }
.ca-custom__err { color: var(--danger); }

.ca-filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.ca-search {
  flex: 1 1 240px;
  min-width: 200px;
  max-width: 380px;
  height: 30px;
  font-size: 12px;
}
.ca-count { margin-left: auto; }

.ca-skeleton { padding: 14px 16px 8px; }

.ca-error {
  margin: 16px;
  padding: 14px 16px;
  border-radius: var(--radius-md);
  border: 1px solid color-mix(in srgb, var(--danger) 35%, transparent);
  background: var(--danger-soft);
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.ca-error--perm {
  border-color: color-mix(in srgb, var(--warning) 40%, transparent);
  background: var(--warning-soft);
}
.ca-error__head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}
.ca-error__icon { color: var(--danger); font-weight: 700; }
.ca-error--perm .ca-error__icon { color: var(--warning); }
.ca-code {
  margin-left: auto;
  padding: 2px 8px;
  border-radius: var(--radius-sm);
  border: 1px solid var(--border-default);
  background: var(--bg-surface);
  color: var(--text-primary);
  font-size: 11px;
  cursor: copy;
}
.ca-code:hover { border-color: var(--border-strong); }
.ca-error p { margin: 0; line-height: 1.6; }
.ca-error__advice { color: var(--text-primary); }
.ca-error :deep(.code) { margin: 4px 0; }
.ca-error__actions { display: flex; gap: 8px; margin-top: 2px; }

.ca-cols {
  display: grid;
  align-items: center;
  gap: 12px;
  padding: 0 18px 0 12px;
  grid-template-columns: 14px 146px minmax(180px, 1.35fr) 118px minmax(120px, 1fr) minmax(130px, 1fr) 118px;
}
.ca-head {
  height: 34px;
  background: var(--bg-inset);
  border-bottom: 1px solid var(--border-subtle);
  font-size: 11px;
  color: var(--text-tertiary);
}
.ca-entry { border-bottom: 1px solid var(--border-subtle); }
.ca-entry.is-err > .ca-row { background: color-mix(in srgb, var(--danger-soft) 55%, transparent); }
.ca-row {
  width: 100%;
  min-height: 48px;
  border: 0;
  background: transparent;
  color: inherit;
  text-align: left;
  cursor: pointer;
  position: relative;
  padding-top: 6px;
  padding-bottom: 6px;
  transition: background var(--dur-fast);
}
.ca-row:hover { background: var(--bg-hover); }
.ca-entry.is-open > .ca-row { background: var(--bg-hover); }
.ca-row:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }

.ca-chev {
  color: var(--text-tertiary);
  font-size: 13px;
  text-align: center;
  transition: transform var(--dur-fast);
}
.ca-chev.is-open { transform: rotate(90deg); color: var(--accent); }

.ca-event {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.ca-event__main {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.ca-event__name {
  font-size: 12px;
  font-weight: 500;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ca-event__src {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ca-write {
  flex: 0 0 auto;
  padding: 0 5px;
  border-radius: var(--radius-sm);
  font-size: 10px;
  line-height: 16px;
  font-weight: 600;
  color: var(--warning);
  background: var(--warning-soft);
}

.ca-act {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.ca-act__badge {
  font-size: 10px;
  line-height: 16px;
  padding: 1px 6px;
  border-radius: var(--radius-sm);
  font-weight: 600;
}
.act--read { color: var(--text-secondary); background: var(--bg-inset); border: 1px solid var(--border-subtle); }
.act--warn { color: var(--warning); background: var(--warning-soft); }
.act--danger { color: var(--danger); background: var(--danger-soft); }
.ca-status { font-weight: 600; }

.ca-cell {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ca-detail {
  padding: 14px 20px 16px 38px;
  background: var(--bg-inset);
  border-top: 1px solid var(--border-subtle);
}
.ca-detail__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px 20px;
}
.ca-kv {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.ca-kv--wide { grid-column: 1 / -1; }
.ca-kv__k { font-size: 11px; color: var(--text-tertiary); }
.ca-kv__v {
  font-size: 12px;
  color: var(--text-primary);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}
.ca-wrap { white-space: pre-wrap; word-break: break-word; overflow: visible; }
.ca-msg-err { color: var(--danger); font-weight: 500; }

.ca-detail__json { margin-top: 12px; }
.ca-detail__json :deep(.code) {
  margin: 6px 0 0;
  max-height: 320px;
  overflow: auto;
  background: var(--bg-surface);
}

.ca-more {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  padding: 14px 16px;
}
.ca-notice { text-align: center; }

@media (max-width: 1100px) {
  .ca-cols {
    grid-template-columns: 14px 136px minmax(150px, 1.3fr) 108px minmax(100px, 1fr) 110px;
  }
  .ca-col-ip {
    display: none;
  }
  .ca-detail__grid { grid-template-columns: 1fr; }
}
</style>
