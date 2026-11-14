<script setup lang="ts">
/** §4.8 设置：账户安全 / 操作策略 / 外观 / 审计日志 / 关于 */
import { t, fmtDateTime, locale, setLocale, LOCALES, type Locale } from '@/i18n'
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useStore } from '@/store'
import MaskedText from '@/components/MaskedText.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import { acctColor, copy } from '@/lib/format'
import { auth, insights, http, type AuditEntryDTO } from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import PageTabs from '@/components/PageTabs.vue'
import SwitchRow from '@/components/SwitchRow.vue'
import KeyValueList from '@/components/KeyValueList.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import QrCode from '@/components/QrCode.vue'
import { actionLabel, auditTarget } from '@/lib/audit'

const router = useRouter()
const { state, ask, toast, toastError, updateOptions, logout } = useStore()
const TABS = ['security', 'policy', 'appearance', 'audit', 'about']
const active = ref('security')

/* ---------- 账户安全 ---------- */

const currentPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const changingPassword = ref(false)
const totpEnabled = ref(false)

async function loadMe() {
  try {
    const me = await auth.me()
    totpEnabled.value = me.totpEnabled
  } catch {
    // 读不到就按未启用显示，用户点绑定时会再走一次真实流程。
  }
}

/* ---------- 两步验证的绑定与关闭 ---------- */

const totpBusy = ref(false)
const totpSecret = ref('')
const totpUri = ref('')
const enrollCode = ref('')
const disablePassword = ref('')
const disableCode = ref('')
const showDisable = ref(false)

/** 密钥按 4 位分组，方便在验证器里手动录入。 */
const groupedSecret = computed(() => totpSecret.value.replace(/(.{4})/g, '$1 ').trim())

async function startEnroll() {
  totpBusy.value = true
  try {
    const setup = await auth.totpSetup()
    totpSecret.value = setup.secret
    totpUri.value = setup.uri
    enrollCode.value = ''
  } catch (err) {
    toastError(t.settingsPage.secretFailed, err)
  } finally {
    totpBusy.value = false
  }
}

async function confirmEnroll() {
  if (enrollCode.value.trim().length !== 6) {
    toast({ tone: 'danger', title: t.settingsPage.need6 })
    return
  }
  totpBusy.value = true
  try {
    await auth.totpEnable(enrollCode.value.trim())
    totpSecret.value = ''
    totpUri.value = ''
    enrollCode.value = ''
    await loadMe()
    toast({ tone: 'success', title: t.settingsPage.totpEnabled })
  } catch (err) {
    toastError(t.settingsPage.bindFailed, err)
  } finally {
    totpBusy.value = false
  }
}

function askDisable() {
  ask({
    level: 2,
    title: t.settingsPage.disableTitle,
    body: t.settingsPage.disableBody,
    okLabel: t.settingsPage.continueDisable,
    onConfirm: () => { showDisable.value = true }
  })
}

async function confirmDisable() {
  if (!disablePassword.value || disableCode.value.trim().length !== 6) {
    toast({ tone: 'danger', title: t.settingsPage.needPwAndCode })
    return
  }
  totpBusy.value = true
  try {
    const result = await auth.totpDisable(disablePassword.value, disableCode.value.trim())
    showDisable.value = false
    disablePassword.value = ''
    disableCode.value = ''
    await loadMe()
    toast({ tone: 'warning', title: t.settingsPage.totpDisabled, body: result.message }, 9000)
  } catch (err) {
    toastError(t.settingsPage.disableFailed, err)
  } finally {
    totpBusy.value = false
  }
}

async function changePassword() {
  if (newPassword.value.length < 10) {
    toast({ tone: 'danger', title: t.settingsPage.pwTooShort })
    return
  }
  if (newPassword.value !== confirmPassword.value) {
    toast({ tone: 'danger', title: t.settingsPage.pwMismatch })
    return
  }

  changingPassword.value = true
  try {
    const result = await auth.changePassword(currentPassword.value, newPassword.value)
    toast({ tone: 'success', title: t.settingsPage.pwUpdated, body: result.message })
    // 后端会顺带清空所有会话，这里必须跟着跳登录页。
    await logout()
    router.push('/login')
  } catch (err) {
    toastError(t.settingsPage.pwFailed, err)
  } finally {
    changingPassword.value = false
    currentPassword.value = ''
    newPassword.value = ''
    confirmPassword.value = ''
  }
}

function revokeAll() {
  ask({
    level: 2,
    title: t.settingsPage.revokeTitle,
    body: t.settingsPage.revokeBody,
    okLabel: t.settingsPage.revokeAll,
    onConfirm: async () => {
      try {
        await http.post('/api/auth/sessions/revoke-all')
        toast({ tone: 'warning', title: t.settingsPage.revoked })
        await logout()
        router.push('/login')
      } catch (err) {
        toastError(t.settingsPage.actionFailed, err)
      }
    }
  })
}

/* ---------- 外观 ---------- */

const accents = computed(() => [
  { key: 'cyan', label: t.settingsPage.accentDefault, hex: '#22D3EE', note: t.settingsPage.accentCyanNote },
  { key: 'sky', label: 'sky', hex: '#38BDF8', note: t.settingsPage.accentSkyNote },
  { key: 'emerald', label: 'emerald', hex: '#34D399', note: t.settingsPage.accentEmeraldNote },
  { key: 'violet', label: 'violet', hex: '#A78BFA', note: t.settingsPage.accentVioletNote }
])

/* ---------- 语言 ---------- */

const languageGroups = [{ label: '', options: LOCALES.map(l => ({ value: l.value, label: l.label })) }]

/* ---------- 审计日志 ---------- */

const audit = ref<AuditEntryDTO[]>([])
const auditLoading = ref(false)
const auditMore = ref(false)
const auditTotal = ref<number | null>(null)

const AUDIT_PAGE = 100

/**
 * 请求代次。「加载更早」在途时切回这个页签会触发一次刷新，
 * 两者交错回来会把旧的下一页拼到新列表后面，出现重复或断层。
 */
let auditSeq = 0

/**
 * 读取一页审计记录。append 为真时追加到已有列表后面。
 *
 * 翻页用游标（beforeId）而不是 OFFSET：审计表持续写入，OFFSET 会在翻页
 * 途中因为新记录插到最前面而漏掉条目——翻第二页时原本第 100 条已被挤到
 * 第 101 的位置，于是整条被跳过。id 是自增主键，天然单调，不受写入影响。
 */
async function loadAudit(append = false) {
  const seq = ++auditSeq
  auditLoading.value = true
  try {
    const beforeId = append && audit.value.length
      ? audit.value[audit.value.length - 1].id
      : undefined
    const res = await insights.audit({ limit: AUDIT_PAGE, beforeId })
    if (seq !== auditSeq) return
    audit.value = append ? [...audit.value, ...res.entries] : res.entries
    auditMore.value = res.hasMore
    if (res.total !== undefined) auditTotal.value = res.total
  } catch (err) {
    if (seq === auditSeq) toastError(t.settingsPage.auditFailed, err)
  } finally {
    if (seq === auditSeq) auditLoading.value = false
  }
}

const codeOf = (id: string) => (id ? state.accounts.find(a => a.id === id)?.code ?? '—' : '—')
const colorOf = (id: string) => {
  const a = id ? state.accounts.find(x => x.id === id) : undefined
  return a ? acctColor(a.colorIndex) : 'var(--border-default)'
}

const auditTime = (iso: string) => {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '—' : fmtDateTime(d).replace(/\//g, '-')
}

/**
 * 导出 CSV。走后端的全量导出接口。
 *
 * 原先是把前端已加载的那一页拼成文件——分页之后那只是最新的 100 条，
 * 而按钮上写的是「导出 CSV」。一个悄悄少掉大部分记录的导出比没有更糟。
 */
function exportCsv() {
  if (audit.value.length === 0) {
    toast({ tone: 'warning', title: t.settingsPage.nothingToExport })
    return
  }
  // 同源、带 Cookie 的普通下载，交给浏览器处理。BOM 与转义都在后端做了。
  // 演示站没有后端，导出不了文件。
  if (import.meta.env.VITE_DEMO === 'true') {
    toast({ tone: 'info', title: t.settingsPage.demoNoExport, body: t.settingsPage.demoNoExportBody })
    return
  }
  window.location.href = '/api/audit/export'
}

/* ---------- 关于 ---------- */

const aboutItems = computed(() => [
  { k: t.settingsPage.kVersion, v: state.version || 'dev', mono: true },
  { k: t.settingsPage.kHome, v: 'github.com/jsongmax/oci-core', href: 'https://github.com/jsongmax/oci-core', mono: true },
  { k: t.settingsPage.kFrontend, v: t.settingsPage.vFrontend },
  // 只写引擎，不写路径：路径对使用者没有意义，而且是部署细节——
  // 容器里的 /app/data 和宿主机上的卷不是一回事，写出来反而误导。
  { k: t.settingsPage.kDb, v: 'SQLite', mono: true },
  { k: t.settingsPage.kSync, v: t.settingsPage.vSync(state.options.syncIntervalMinutes) },
  {
    k: t.settingsPage.kCheck,
    v: state.options.checkIntervalHours > 0
      ? t.settingsPage.vCheck(state.options.checkIntervalHours)
      : t.settingsPage.vCheckOff
  },
  { k: t.settingsPage.kLive, v: state.live ? t.settingsPage.vLiveOk : t.settingsPage.vLiveDown, tone: state.live ? 'var(--success)' : 'var(--warning)' },
  // 与 README 的「关于开发方式」保持一致，改一处要改另一处。
  { k: t.settingsPage.kDev, v: t.settingsPage.vDev }
])

/** 顶部副标题。总数由后端给，不等于已加载条数——那会让人以为只有这么多。 */
const auditNote = computed(() => {
  const loaded = audit.value.length
  if (auditTotal.value === null) return t.settingsPage.auditLoaded(loaded)
  if (auditTotal.value <= loaded) return t.settingsPage.auditAll(loaded)
  return t.settingsPage.auditPartial(loaded, auditTotal.value)
})

const retentionGroups = computed(() => [{
  label: '',
  options: [
    { value: '0', label: t.settingsPage.keepForever },
    { value: '30', label: t.settingsPage.nDays(30) },
    { value: '90', label: t.settingsPage.nDays(90) },
    { value: '180', label: t.settingsPage.nDays(180) },
    { value: '365', label: t.settingsPage.oneYear }
  ]
}])

/**
 * 改保留期限要过确认。
 *
 * 这是唯一一个会让审计记录消失的开关，而且下一次清理跑起来就是不可逆的。
 * 从"永久"改成有限期时尤其要说清楚会删掉什么。
 */
function confirmRetention(days: number) {
  if (days === state.options.auditRetentionDays) return
  if (days === 0) {
    updateOptions({ auditRetentionDays: 0 })
    return
  }
  ask({
    level: 2,
    title: t.settingsPage.retentionTitle(days),
    body: t.settingsPage.retentionBody(days),
    okLabel: t.settingsPage.setDays(days),
    onConfirm: async () => { await updateOptions({ auditRetentionDays: days }) }
  })
}

const checkIntervalGroups = computed(() => [{
  label: '',
  options: [
    { value: '0', label: t.settingsPage.off },
    { value: '1', label: t.settingsPage.hourly },
    { value: '6', label: t.settingsPage.everyNHours(6) },
    { value: '12', label: t.settingsPage.everyNHours(12) },
    { value: '24', label: t.settingsPage.daily },
    { value: '72', label: t.settingsPage.every3Days },
    { value: '168', label: t.settingsPage.weekly }
  ]
}])

/**
 * 全量同步间隔。
 *
 * 最短给到 2 分钟，和后端 UpdateSettings 的下限一致——那边才是真正的关口，
 * 这里只是不把做不到的选项摆出来。一轮同步要对每个（账号 × 区域）发一组
 * 请求，五个账号就是几十个调用；实时性本来就由 SSE 保证，全量只是兜底对账。
 */
const syncIntervalGroups = computed(() => [{
  label: '',
  options: [
    { value: '2', label: t.settingsPage.everyNMin(2) },
    { value: '5', label: t.settingsPage.everyNMin(5) },
    { value: '10', label: t.settingsPage.everyNMin(10) },
    { value: '30', label: t.settingsPage.everyNMin(30) },
    { value: '60', label: t.settingsPage.hourly },
    { value: '360', label: t.settingsPage.everyNHours(6) },
    { value: '1440', label: t.settingsPage.daily }
  ]
}])

onMounted(() => {
  void loadMe()
  void loadAudit()
})

watch(active, tab => { if (tab === 'audit') void loadAudit() })
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">{{ t.settingsPage.title }}</h1>
        <p class="page-sub">{{ t.settingsPage.subtitle }}</p>
      </div>
    </header>

    <PageTabs :tabs="TABS" :labels="t.settingsPage.tabs" v-model:active="active" />

    <!-- 各标签的条件链彼此独立，没法逐个过渡；外层按标签换 key，切换时整块淡入淡出 -->
    <Transition name="swap" mode="out-in">
    <div :key="active">

    <template v-if="active === 'security'">
      <SectionCard :title="t.settingsPage.totp">
        <div class="pad">
          <p class="t-sm">
            {{ t.settingsPage.currentStatus }}
            <strong :style="{ color: totpEnabled ? 'var(--success)' : 'var(--warning)' }">
              {{ totpEnabled ? t.settingsPage.enabled : t.settingsPage.notEnabled }}
            </strong>
          </p>

          <!-- 已启用：提供关闭入口 -->
          <template v-if="totpEnabled">
            <template v-if="!showDisable">
              <p class="t-xs dim">{{ t.settingsPage.totpOnHint }}</p>
              <button class="btn btn--sm btn--danger" @click="askDisable()">{{ t.settingsPage.disableTitle }}</button>
            </template>

            <div v-else class="sub-form">
              <p class="t-xs dim">{{ t.settingsPage.disableHint }}</p>
              <div class="field">
                <label for="dp">{{ t.settingsPage.currentPw }}</label>
                <input id="dp" v-model="disablePassword" type="password" class="input"
                       autocomplete="current-password" />
              </div>
              <div class="field">
                <label for="dc">{{ t.settingsPage.code }}</label>
                <input id="dc" v-model="disableCode" class="input mono" maxlength="6"
                       inputmode="numeric" :placeholder="t.settingsPage.sixDigits" />
              </div>
              <div class="sub-form__foot">
                <button class="btn btn--sm" @click="showDisable = false">{{ t.common.cancel }}</button>
                <button class="btn btn--sm btn--danger" :disabled="totpBusy" @click="confirmDisable()">
                  {{ totpBusy ? t.common.submitting : t.settingsPage.confirmDisable }}
                </button>
              </div>
            </div>
          </template>

          <!-- 未启用：就地绑定，不必退出登录 -->
          <template v-else>
            <p class="t-xs dim">{{ t.settingsPage.totpOffHint }}</p>

            <button v-if="!totpSecret" class="btn btn--sm" :disabled="totpBusy" @click="startEnroll()">
              {{ totpBusy ? t.settingsPage.generating : t.settingsPage.startEnroll }}
            </button>

            <div v-else class="sub-form">
              <p class="t-xs dim">
                {{ t.settingsPage.enrollHint }}
              </p>
              <QrCode v-if="totpUri" :value="totpUri" :label="t.settingsPage.enrollQr" />
              <div class="secret">
                <code class="secret__value mono">{{ groupedSecret }}</code>
                <button class="btn btn--sm" @click="copy(totpSecret)">{{ t.common.copy }}</button>
              </div>
              <a class="t-xs" :href="totpUri" style="color: var(--accent)">{{ t.settingsPage.openInApp }}</a>
              <div class="field">
                <label for="ec">{{ t.settingsPage.code }}</label>
                <input id="ec" v-model="enrollCode" class="input mono" maxlength="6"
                       inputmode="numeric" :placeholder="t.settingsPage.sixDigits" />
              </div>
              <div class="sub-form__foot">
                <button class="btn btn--sm" @click="totpSecret = ''">{{ t.common.cancel }}</button>
                <button class="btn btn--sm btn--primary" :disabled="totpBusy" @click="confirmEnroll()">
                  {{ totpBusy ? t.common.submitting : t.settingsPage.finishEnroll }}
                </button>
              </div>
            </div>
          </template>
        </div>
      </SectionCard>

      <SectionCard :title="t.settingsPage.changePw" :note="t.settingsPage.changePwNote" class="mt">
        <form class="pad form" @submit.prevent="changePassword">
          <div class="field">
            <label for="cur">{{ t.settingsPage.currentPw }}</label>
            <input id="cur" v-model="currentPassword" type="password" class="input" autocomplete="current-password" />
          </div>
          <div class="field">
            <label for="np">{{ t.settingsPage.newPw }}</label>
            <input id="np" v-model="newPassword" type="password" class="input" autocomplete="new-password" />
          </div>
          <div class="field">
            <label for="np2">{{ t.settingsPage.confirmNewPw }}</label>
            <input id="np2" v-model="confirmPassword" type="password" class="input" autocomplete="new-password" />
          </div>
          <button class="btn btn--primary" type="submit" :disabled="changingPassword">
            {{ changingPassword ? t.common.submitting : t.settingsPage.changePw }}
          </button>
        </form>
      </SectionCard>

      <SectionCard :title="t.settingsPage.sessions" :note="t.settingsPage.sessionsNote" class="mt">
        <div class="pad">
          <p class="t-xs dim">
            {{ t.settingsPage.sessionsHint }}
          </p>
          <button class="btn btn--danger mt-10" @click="revokeAll">{{ t.settingsPage.revokeButton }}</button>
        </div>
      </SectionCard>
    </template>

    <SectionCard v-else-if="active === 'policy'" :title="t.settingsPage.dangerOps"
                 :note="t.settingsPage.dangerOpsNote">
      <SwitchRow :model-value="state.options.allowTerminate" :title="t.settingsPage.allowTerminate"
                 :sub="t.settingsPage.allowTerminateSub"
                 @update:model-value="v => updateOptions({ allowTerminate: v })" />
      <SwitchRow :model-value="state.options.allowBulk" :title="t.settingsPage.allowBulk"
                 :sub="t.settingsPage.allowBulkSub"
                 @update:model-value="v => updateOptions({ allowBulkActions: v })" />
      <SwitchRow :model-value="state.options.reauthForDanger" :title="t.settingsPage.reauth"
                 :sub="t.settingsPage.reauthSub"
                 @update:model-value="v => updateOptions({ requireTotpForDanger: v })" />
      <SwitchRow v-model="state.options.confirmForceRestart" :title="t.settingsPage.confirmForce"
                 :sub="t.settingsPage.confirmForceSub" />
    </SectionCard>

    <SectionCard v-if="active === 'policy'" :title="t.settingsPage.privacy"
                 :note="t.settingsPage.privacyNote" class="mt">
      <SwitchRow v-model="state.privacyMode" :title="t.settingsPage.privacyMask"
                 :sub="t.settingsPage.privacyMaskSub" />
      <div class="pad privacy-note">
        <p class="t-xs dim-3">
          {{ t.settingsPage.privacyKeep }}<b>{{ t.settingsPage.privacyCopy }}</b>{{ t.settingsPage.privacyReveal }}
        </p>
        <p class="t-xs dim-3">
          {{ t.settingsPage.privacyBoundary }}
        </p>
      </div>
    </SectionCard>

    <SectionCard v-if="active === 'policy'" :title="t.settingsPage.bgSync"
                 :note="t.settingsPage.bgSyncNote">
      <div class="chk">
        <div class="chk__text">
          <div class="chk__title">{{ t.settingsPage.syncInterval }}</div>
          <div class="chk__sub">
            {{ t.settingsPage.syncIntervalSub }}
          </div>
        </div>
        <SelectMenu class="chk__select" :model-value="String(state.options.syncIntervalMinutes)"
                    :groups="syncIntervalGroups" :min-width="128" :aria-label="t.settingsPage.syncIntervalLabel"
                    @update:model-value="v => updateOptions({ syncIntervalMinutes: Number(v) })" />
      </div>
    </SectionCard>

    <SectionCard v-if="active === 'policy'" :title="t.settingsPage.autoCheck"
                 :note="t.settingsPage.autoCheckNote">
      <div class="chk">
        <div class="chk__text">
          <div class="chk__title">{{ t.settingsPage.checkInterval }}</div>
          <div class="chk__sub">
            {{ t.settingsPage.checkIntervalSub }}
          </div>
        </div>
        <SelectMenu class="chk__select" :model-value="String(state.options.checkIntervalHours)"
                    :groups="checkIntervalGroups" :min-width="128" :aria-label="t.settingsPage.checkIntervalLabel"
                    @update:model-value="v => updateOptions({ checkIntervalHours: Number(v) })" />
      </div>
    </SectionCard>

    <SectionCard v-if="active === 'policy'" :title="t.settingsPage.retention"
                 :note="t.settingsPage.retentionNote">
      <div class="chk">
        <div class="chk__text">
          <div class="chk__title">{{ t.settingsPage.retentionPeriod }}</div>
          <div class="chk__sub">
            {{ t.settingsPage.retentionSub }}
          </div>
        </div>
        <SelectMenu class="chk__select" :model-value="String(state.options.auditRetentionDays)"
                    :groups="retentionGroups" :min-width="128" :aria-label="t.settingsPage.retentionLabel"
                    @update:model-value="v => confirmRetention(Number(v))" />
      </div>
    </SectionCard>

    <template v-else-if="active === 'appearance'">
      <SectionCard :title="t.settingsPage.language" :note="t.settingsPage.languageNote">
        <div class="chk">
          <div class="chk__text">
            <div class="chk__title">{{ t.settingsPage.uiLanguage }}</div>
            <div class="chk__sub">{{ t.settingsPage.uiLanguageSub }}</div>
          </div>
          <SelectMenu class="chk__select" :model-value="locale" :groups="languageGroups" :min-width="128"
                      :aria-label="t.settingsPage.uiLanguage" @update:model-value="v => setLocale(v as Locale)" />
        </div>
        <div class="chk">
          <div class="chk__text">
            <div class="chk__title">{{ t.settingsPage.notifyLanguage }}</div>
            <div class="chk__sub">{{ t.settingsPage.notifyLanguageSub }}</div>
          </div>
          <SelectMenu class="chk__select" :model-value="state.options.notifyLanguage" :groups="languageGroups"
                      :min-width="128" :aria-label="t.settingsPage.notifyLanguage"
                      @update:model-value="v => updateOptions({ language: String(v) })" />
        </div>
      </SectionCard>
      <SectionCard :title="t.settingsPage.themeDensity" class="mt">
        <SwitchRow :model-value="state.theme === 'dark'" :title="t.settingsPage.darkTheme" :sub="t.settingsPage.darkThemeSub"
                   :value="state.theme === 'dark' ? 'Dark' : 'Light'"
                   @update:model-value="state.theme = state.theme === 'dark' ? 'light' : 'dark'" />
        <SwitchRow :model-value="state.density === 'compact'" :title="t.settingsPage.compact" :sub="t.settingsPage.compactSub"
                   :value="state.density === 'comfy' ? '56px' : '44px'"
                   @update:model-value="state.density = state.density === 'comfy' ? 'compact' : 'comfy'" />
        <SwitchRow v-model="state.options.reduceMotion" :title="t.settingsPage.reduceMotion" :sub="t.settingsPage.reduceMotionSub" />
      </SectionCard>
      <SectionCard :title="t.settingsPage.accent" :note="t.settingsPage.accentNote" class="mt">
        <button v-for="a in accents" :key="a.key" class="row cols-accent accent"
                :class="{ 'is-picked': state.accentName === a.key }" @click="state.accentName = a.key">
          <span class="acct-bar" :style="{ background: a.hex }" />
          <span class="t-xs">{{ a.label }}</span>
          <span class="mono t-xs" :style="{ color: a.hex }">{{ a.hex }}</span>
          <span class="t-xs dim">{{ a.note }}</span>
          <span class="t-xs" :style="{ color: 'var(--accent)' }">{{ state.accentName === a.key ? t.settingsPage.current : '' }}</span>
        </button>
      </SectionCard>
    </template>

    <SectionCard v-else-if="active === 'audit'" :title="t.settingsPage.audit" :note="auditNote">
      <template #action>
        <button class="btn btn--sm" :disabled="auditLoading" @click="loadAudit(false)">{{ t.common.refresh }}</button>
        <button class="btn btn--sm" @click="exportCsv">{{ t.settingsPage.exportCsv }}</button>
      </template>

      <SkeletonRows v-if="auditLoading && audit.length === 0" :rows="6" />
      <EmptyState v-else-if="audit.length === 0" :title="t.settingsPage.noAudit"
                  :body="t.settingsPage.noAuditBody" />
      <template v-else>
        <div class="head cols-audit">
          <span>{{ t.settingsPage.colTime }}</span><span>{{ t.settingsPage.colAction }}</span><span>{{ t.settingsPage.colAccount }}</span><span>{{ t.settingsPage.colTarget }}</span><span>{{ t.settingsPage.colDetail }}</span><span>{{ t.settingsPage.colIp }}</span><span>{{ t.settingsPage.colResult }}</span>
        </div>
        <div v-for="r in audit" :key="r.id" class="row cols-audit">
          <span class="acct-bar" :style="{ background: colorOf(r.accountId) }" />
          <span class="mono t-xs dim">{{ auditTime(r.createdAt) }}</span>
          <span class="t-xs">{{ actionLabel(r.action) }}</span>
          <span class="mono t-xs" :style="{ color: colorOf(r.accountId), fontWeight: 600 }">{{ codeOf(r.accountId) }}</span>
          <span class="mono t-xs dim" :title="r.target">{{ r.target ? auditTarget(r.target) : '—' }}</span>
          <span class="t-xs dim-3">{{ r.detail || '—' }}</span>
          <span class="mono t-xs dim-3"><MaskedText :value="r.ip || '—'" kind="ip" /></span>
          <span class="t-xs" :style="{ color: r.result === 'ok' ? 'var(--success)' : 'var(--danger)', fontWeight: 600 }">
            {{ r.result === 'ok' ? t.settingsPage.ok : t.settingsPage.failed }}
          </span>
        </div>

        <div class="more">
          <button v-if="auditMore" class="btn btn--sm" :disabled="auditLoading"
                  @click="loadAudit(true)">
            <span v-if="auditLoading" class="btn-spin" aria-hidden="true" />{{ auditLoading ? t.settingsPage.loadingShort : t.settingsPage.loadEarlier(AUDIT_PAGE) }}
          </button>
          <span v-else class="t-2xs dim-3">{{ t.settingsPage.earliest }}</span>
        </div>
      </template>
    </SectionCard>

    <SectionCard v-else :title="t.settingsPage.about">
      <KeyValueList :items="aboutItems" />
    </SectionCard>
    </div>
    </Transition>
  </div>
</template>

<style scoped>
.privacy-note { padding-top: 4px; }
.privacy-note p { margin: 0 0 8px; line-height: 1.8; }
.privacy-note p:last-child { margin-bottom: 0; }

.chk { display: flex; align-items: center; gap: 16px; padding: 14px 16px; }
.chk__text { flex: 1 1 auto; min-width: 0; }
.chk__title { font-size: 13px; font-weight: 600; }
.chk__sub { margin-top: 4px; font-size: 11px; color: var(--text-secondary); line-height: 1.6; }
.chk__select { flex: 0 0 auto; width: 128px; }

.mt { margin-top: 16px; }

/* 同一个标签页里相邻的卡片一律留白。
   原先靠每张卡各自加 .mt，漏一张就和上一张贴死——而漏了不会报错，
   只是看着挤，加卡片的人也不会想到还有这么个约定。 */
.page > .card + .card { margin-top: 16px; }
.mt-6 { margin-top: 6px; }
.mt-10 { margin-top: 10px; }
.pad { padding: 16px 20px; }
.form { display: flex; flex-direction: column; gap: 14px; max-width: 360px; }
.sub-form {
  display: flex; flex-direction: column; gap: 12px; max-width: 360px;
  padding: 14px; border-radius: var(--radius-md);
  border: 1px solid var(--border-subtle); background: var(--bg-inset);
}
.sub-form__foot { display: flex; gap: 8px; }
.secret {
  display: flex; align-items: center; gap: 8px;
  padding: 9px 11px; border-radius: var(--radius-sm);
  border: 1px solid var(--border-default); background: var(--bg-surface);
}
.secret__value { flex: 1 1 auto; font-size: 12px; letter-spacing: 0.06em; word-break: break-all; }
.head, .row { display: grid; align-items: center; gap: 12px; padding: 0 20px 0 14px; }
.head { height: 34px; background: var(--bg-inset); border-bottom: 1px solid var(--border-subtle); font-size: 11px; color: var(--text-tertiary); }
.row { min-height: 46px; border-bottom: 1px solid var(--border-subtle); position: relative; }
.row:hover { background: var(--bg-hover); }
.row > span:not(.acct-bar), .head > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row .btn { justify-self: end; }
.accent { width: 100%; border: 0; border-bottom: 1px solid var(--border-subtle); background: transparent; color: inherit; cursor: pointer; text-align: left; }
.accent.is-picked { background: var(--accent-soft); }
.cols-accent { grid-template-columns: 150px 110px 1fr 60px; }
.cols-audit { grid-template-columns: 150px 110px 70px minmax(130px, 1fr) minmax(120px, 1fr) 110px 70px; }

.more {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 14px 0 4px;
}

</style>
