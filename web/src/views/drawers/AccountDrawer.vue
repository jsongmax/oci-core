<script setup lang="ts">
/** §4.3.2 账号详情抽屉（720）：概览 / 区域订阅 / 配额 / 权限自检 / 密钥 */
import { t } from '@/i18n'
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import { passwordPolicy, type PasswordPolicyDTO } from '@/api'
import { mask } from '@/lib/mask'
import { accountAgeText, accountStatusText, trialDaysLeft } from '@/lib/adapt'
import { ALWAYS_FREE, armFreeText } from '@/lib/freetier'
import { accounts as accountsApi, type CheckStepDTO } from '@/api'
import type { CheckItem } from '@/types'
import AppDrawer from '@/components/AppDrawer.vue'
import DrawerHeader from '@/components/DrawerHeader.vue'
import DrawerTabs from '@/components/DrawerTabs.vue'
import DrawerBody from '@/components/DrawerBody.vue'
import SectionCard from '@/components/SectionCard.vue'
import KeyValueList from '@/components/KeyValueList.vue'
import CheckList from '@/components/CheckList.vue'
import CodeBlock from '@/components/CodeBlock.vue'
import QuotaMeter from '@/components/QuotaMeter.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import CredentialField from '@/components/CredentialField.vue'

const props = defineProps<{ id: string; tab?: string }>()
const router = useRouter()
const {
  state, closeDrawer, ask, toast, toastError, allRegions,
  loadAccounts, syncNow
} = useStore()

/** 重新校验的结果。逐项来自后端，不是前端猜的。 */
const checkSteps = ref<CheckStepDTO[]>([])
const checking = ref(false)
const checkAdvice = ref('')

async function recheck() {
  checking.value = true
  checkAdvice.value = ''
  try {
    const result = await accountsApi.check(props.id)
    checkSteps.value = result.steps
    checkAdvice.value = result.advice ?? ''
    await loadAccounts()
    toast({
      tone: result.ok ? 'success' : 'danger',
      title: result.ok ? t.acctDrawer.checkOk(account.value?.alias ?? '') : t.acctDrawer.checkFailed,
      body: result.ok
        ? t.acctDrawer.subscribed(result.regions?.length ?? 0)
        : [result.errorCode, result.errorText].filter(Boolean).join(' · ')
    }, result.ok ? 6000 : 9000)
  } catch (err) {
    toastError(t.acctDrawer.checkFailed, err)
  } finally {
    checking.value = false
  }
}

/* ---------- 密钥轮换 ---------- */

const rotateFingerprint = ref('')
const rotateKey = ref('')
const rotating = ref(false)
const canRotate = computed(() =>
  rotateFingerprint.value.trim().length > 0 && rotateKey.value.trim().length > 20)

/**
 * 指纹与私钥一起提交：后端会核对两者是否匹配，不匹配直接拒绝写入，
 * 错误信息里带着私钥的实际指纹。成功后私钥立刻从内存里清掉，再走一次校验。
 */
function rotateKeyNow() {
  const acc = account.value
  if (!acc) return
  ask({
    level: 2, title: t.acctDrawer.rotateTitle(acc.alias),
    body: t.acctDrawer.rotateBody,
    okLabel: t.acctDrawer.rotate,
    onConfirm: async () => {
      rotating.value = true
      try {
        await accountsApi.update(acc.id, {
          fingerprint: rotateFingerprint.value.trim(),
          privateKeyPem: rotateKey.value.trim()
        })
        rotateKey.value = ''
        rotateFingerprint.value = ''
        toast({ tone: 'success', title: t.acctDrawer.rotated, body: t.acctDrawer.rechecking })
        await recheck()
      } catch (err) {
        toastError(t.acctDrawer.rotateFailed, err)
      } finally {
        rotating.value = false
      }
    }
  })
}

/** 权限自检项：真实校验步骤 + 本工具需要的最小权限集说明。 */
const permissionChecks = computed<CheckItem[]>(() => {
  const items: CheckItem[] = checkSteps.value.map(s => ({
    tone: s.ok ? 'ok' : 'fail',
    text: s.detail ? `${s.label} · ${s.detail}` : s.label
  }))
  if (items.length === 0) {
    items.push({ tone: 'info', text: t.acctDrawer.clickRecheck })
  }
  if (checkAdvice.value) items.push({ tone: 'warn', text: checkAdvice.value })
  return items
})

const TABS = ['overview', 'regions', 'quota', 'permissions', 'signin', 'keys']

/* ---------- 登录策略（身份域密码有效期） ---------- */

const policy = ref<PasswordPolicyDTO | null>(null)
const policyNotice = ref('')
const policyLoading = ref(false)
const policySaving = ref(false)
const policyDays = ref(365)

async function loadPolicy() {
  if (!props.id) return
  policyLoading.value = true
  try {
    const res = await passwordPolicy.get(props.id)
    policy.value = res.policy
    policyNotice.value = res.notice
    if (res.policy.expiresAfterDays) policyDays.value = res.policy.expiresAfterDays
  } catch (err) {
    policy.value = null
    toastError(t.acctDrawer.policyLoadFailed, err)
  } finally {
    policyLoading.value = false
  }
}

/**
 * 修改有效期。
 *
 * disable 为真时尝试移除这个属性。Oracle 没有公开说明哪个值代表永不过期，
 * 所以后端改完会立即回读——这里显示的始终是服务端实际存下的值。
 * 提交成功不等于生效，两者必须分开讲。
 */
function applyPolicy(disable: boolean) {
  ask({
    level: 2,
    title: disable ? t.acctDrawer.disableExpiry : t.acctDrawer.setExpiry(policyDays.value),
    body: t.acctDrawer.policyBody(account.value?.alias ?? ''),
    okLabel: t.acctDrawer.confirmChange,
    onConfirm: async () => {
      policySaving.value = true
      try {
        const res = await passwordPolicy.set(props.id,
          disable ? { disable: true } : { days: policyDays.value })
        policy.value = res.policy
        policyNotice.value = res.notice
        toast({ tone: 'success', title: t.acctDrawer.changed, body: res.notice })
      } catch (err) {
        toastError(t.acctDrawer.changeFailed, err)
      } finally {
        policySaving.value = false
      }
    }
  })
}

/** 当前有效期的一句话。null 和 0 含义不同，不能混为一谈。 */
const expiryText = computed(() => {
  const p = policy.value
  if (!p?.supported) return '—'
  if (p.expiresAfterDays === null || p.expiresAfterDays === undefined) return t.acctDrawer.expiryUnset
  if (p.expiresAfterDays === 0) return t.acctDrawer.expiryZero
  return t.acctDrawer.days(p.expiresAfterDays)
})

/** 抽屉副标题里带着 OCID 与邮箱，跟着隐私模式一起打码。 */
const drawerSub = computed(() => {
  const a = account.value
  if (!a) return ''
  const tail = state.privacyMode ? mask(a.tenancyTail) : a.tenancyTail
  const mail = state.privacyMode ? mask(a.email, 'email') : a.email
  return `ocid1.tenancy…${tail} · ${mail}`
})
const active = ref(props.tab && TABS.includes(props.tab) ? props.tab : 'overview')

const account = computed(() => state.accounts.find(a => a.id === props.id))
const color = computed(() => (account.value ? acctColor(account.value.colorIndex) : 'var(--border-default)'))
const instances = computed(() => state.instances.filter(i => i.accountId === props.id && i.state !== 'TERMINATED'))

/**
 * 账号类型一行话。试用号带到期日——那天配额会掉回永久免费额度，
 * 超出的实例会被 Oracle 回收。
 */
const tierText = computed(() => {
  const a = account.value
  if (!a) return '—'
  if (a.tier === 'unknown') return t.acctDrawer.tierUnknown
  if (a.tier === 'paid') return t.acctDrawer.tierPaid
  const d = trialDaysLeft(a)
  if (d === null) return t.acctDrawer.tierTrial
  if (d < 0) return t.acctDrawer.tierTrialExpired
  return t.acctDrawer.tierTrialDays(d, armFreeText)
})

/**
 * 试用到期后会超出永久免费额度的部分。
 *
 * 试用号现在的 ARM 配额是试用期专属的（本机实测 16 OCPU / 96 GB）。
 * 到期那天降回永久免费额度，超出的实例会被 Oracle 回收——2026 年 6 月
 * 那次砍额度后，Oracle 就是这么处理的，从 8 月 18 日起开始终止。
 *
 * 只在真的超了的时候提示：没超的账号看到这行只会白紧张。
 */
const trialOverage = computed(() => {
  const a = account.value
  if (a?.tier !== 'trial') return ''
  const over = a.quota.ocpuUsed - ALWAYS_FREE.armOcpus
  if (over <= 0) return ''
  return t.acctDrawer.trialOverage(a.quota.ocpuUsed, over)
})

/**
 * 开户时间。与「接入本面板」分开列——一个用了两年的老号今天才接进来，
 * 两个数字差着两年，混在一起会让人以为这是个新号。
 */
const openedText = computed(() => {
  const a = account.value
  if (!a) return '—'
  const age = accountAgeText(a)
  if (!a.openedAt) return t.acctDrawer.openedUnknown
  return `${a.openedAt.slice(0, 10)}（${age}）`
})

const tierTone = computed(() => {
  const a = account.value
  if (a?.tier !== 'trial') return undefined
  const d = trialDaysLeft(a)
  if (d === null) return undefined
  if (d <= 0) return 'var(--danger)'
  if (d <= 7) return 'var(--warning)'
  return undefined
})

const statusTone = {
  ok: 'var(--success)', checking: 'var(--info)', error: 'var(--danger)', disabled: 'var(--neutral)'
}

const unsubscribed = computed(() =>
  allRegions.value.filter(r => !account.value?.regions.includes(r))
)

/** 已订阅但没查配额的区域。 */
const otherRegions = computed(() => {
  const a = account.value
  if (!a) return []
  const shown = a.quotaRegion || a.regions[0]
  return a.regions.filter(r => r !== shown)
})

function toggleDisabled() {
  const a = account.value
  if (!a) return
  const disabling = a.status !== 'disabled'
  ask({
    level: 2,
    title: t.acctDrawer.toggleTitle(disabling, a.alias),
    body: disabling
      ? t.acctDrawer.disableBody
      : t.acctDrawer.enableBody,
    okLabel: disabling ? t.acctDrawer.disable : t.acctDrawer.enable,
    onConfirm: async () => {
      try {
        await accountsApi.update(a.id, { enabled: !disabling })
        await loadAccounts()
        toast({
          tone: 'warning',
          title: t.acctDrawer.toggled(a.alias, disabling),
          body: t.acctDrawer.instancesUnder(instances.value.length)
        })
      } catch (err) {
        toastError(t.acctDrawer.actionFailed, err)
      }
    }
  })
}

function askDelete() {
  const a = account.value
  if (!a) return
  ask({
    level: 3,
    title: t.acctDrawer.deleteTitle(a.alias),
    body: t.acctDrawer.deleteBody,
    noun: a.alias, nounLabel: t.acctDrawer.aliasLabel,
    losses: [
      t.acctDrawer.lossKey,
      t.acctDrawer.lossView(instances.value.length),
      t.acctDrawer.lossAudit
    ],
    okLabel: t.acctDrawer.deletePermanently,
    onConfirm: async () => {
      try {
        // 服务端要求回传账号别名——前端确认框可以被绕过，这道校验不能。
        await accountsApi.remove(a.id, a.alias)
        state.accountFilter.delete(a.id)
        state.instances = state.instances.filter(i => i.accountId !== a.id)
        await loadAccounts()
        closeDrawer()
        toast({ tone: 'danger', title: t.acctDrawer.removed(a.alias), body: t.acctDrawer.cloudUntouched })
      } catch (err) {
        toastError(t.acctDrawer.deleteFailed, err)
      }
    }
  })
}
</script>

<template>
  <AppDrawer v-if="account" width="wide" @close="closeDrawer()">
    <DrawerHeader :color="color" :code="account.code" :title="account.alias"
                  :sub="drawerSub" @close="closeDrawer()">
      <template #badge>
        <span class="lamp" :style="{ '--c': statusTone[account.status] }">
          <span class="lamp__dot" :class="{ 'is-pulsing': account.status === 'checking' }" />
          {{ t.acctDrawer.status[account.status] }}
        </span>
      </template>
      <template #actions>
        <button class="btn btn--sm" :disabled="checking" @click="recheck()">
          {{ checking ? t.acctDrawer.checking : t.acctDrawer.recheck }}
        </button>
        <button class="btn btn--sm" :disabled="state.syncing" @click="syncNow(account.id)">
          {{ state.syncing ? t.acctDrawer.syncing : t.acctDrawer.syncInstances }}
        </button>
        <button class="btn btn--sm" @click="closeDrawer(); state.accountFilter = new Set([account.id]); router.push('/instances')">
          {{ t.acctDrawer.viewInstances }}
        </button>
      </template>
    </DrawerHeader>

    <DrawerTabs :tabs="TABS" :labels="t.acctDrawer.tabs" v-model:active="active" />

    <DrawerBody>
      <!-- 切标签时内容淡入淡出：外层按标签换 key，Transition 需要单一子元素 -->
      <Transition name="swap" mode="out-in">
      <div :key="active">
      <template v-if="active === 'overview'">
        <SectionCard :title="t.acctDrawer.tenancy">
          <KeyValueList :items="[
            { k: t.acctDrawer.kAlias, v: account.alias },
            { k: t.acctDrawer.kCode, v: account.code, mono: true, tone: color },
            { k: t.acctDrawer.kTenancy, v: `ocid1.tenancy.oc1..aaaaaaaa${account.tenancyTail}`, mono: true, copyable: true, secret: 'ocid' },
            { k: t.acctDrawer.kEmail, v: account.email, mono: true, copyable: true, secret: 'email' },
            { k: t.acctDrawer.kTier, v: tierText, tone: tierTone },
            { k: t.acctDrawer.kFingerprint, v: account.fingerprint, mono: true, copyable: true, secret: 'fingerprint' },
            { k: t.acctDrawer.kOpened, v: openedText },
            { k: t.acctDrawer.kJoined, v: account.createdAt, mono: true },
            { k: t.acctDrawer.kChecked, v: accountStatusText(account) }
          ]" />
        </SectionCard>
        <SectionCard :title="t.acctDrawer.resources">
          <KeyValueList :items="[
            { k: t.acctDrawer.kInstances, v: t.acctDrawer.vInstances(instances.length, instances.filter(i => i.state === 'RUNNING').length), mono: true },
            { k: t.acctDrawer.kRegions, v: t.acctDrawer.vRegions(account.regions.length), mono: true },
            { k: t.acctDrawer.kBootTotal, v: `${instances.reduce((n, i) => n + i.bootGb, 0)} GB`, mono: true }
          ]" />
        </SectionCard>
        <SectionCard v-if="account.status === 'error'" :title="t.acctDrawer.diagnosis" :note="t.acctDrawer.diagnosisNote">
          <CheckList :items="[
            { tone: 'fail', text: t.acctDrawer.diag401 },
            { tone: 'info', text: t.acctDrawer.diagCause }
          ]" />
          <CodeBlock copyable
            :code="'{\n  \'code\': \'NotAuthenticated\',\n  \'message\': \'The required information to complete authentication was not provided.\'\n}'" />
        </SectionCard>
        <SectionCard v-else :title="t.acctDrawer.connectivity">
          <CheckList :items="[{ tone: 'ok', text: `${t.acctDrawer.status[account.status]} · ${accountStatusText(account)}` }]" />
        </SectionCard>
      </template>

      <template v-else-if="active === 'regions'">
        <SectionCard :title="t.acctDrawer.subscribedTitle" :note="t.acctDrawer.regionsNote(account.regions.length)">
          <div v-for="r in account.regions" :key="r" class="rule">
            <span class="rule__bar" :style="{ background: color }" />
            <span class="mono t-xs">{{ r }}</span>
            <span class="rule__desc mono t-2xs dim-3">
              {{ t.acctDrawer.regionInstances(instances.filter(i => i.region === r).length) }}
            </span>
          </div>
        </SectionCard>
        <SectionCard :title="t.acctDrawer.available" :note="t.acctDrawer.availableNote">
          <div v-for="r in unsubscribed" :key="r" class="rule">
            <span class="rule__bar" style="background: var(--border-default)" />
            <span class="mono t-xs">{{ r }}</span>
            <button class="btn btn--sm rule__desc"
                    @click="account.regions.push(r); toast({ tone: 'success', title: t.acctDrawer.subscribedToast(account.alias, r), body: t.acctDrawer.subscribeEta })">
              {{ t.acctDrawer.subscribe }}
            </button>
          </div>
          <CheckList v-if="!unsubscribed.length" :items="[{ tone: 'ok', text: t.acctDrawer.allSubscribed }]" />
        </SectionCard>
      </template>

      <template v-else-if="active === 'quota'">
        <SectionCard
          :title="account.quotaRegion || account.regions[0] || t.acctDrawer.quota"
          :note="account.quotaRegion ? t.acctDrawer.quotaLive : t.acctDrawer.quotaLoading">
          <!-- 配额还没回来时画骨架：四条 0 / 0 的进度条会被当成「没有额度」 -->
          <div v-if="!account.quotaRegion" class="pad sk-quota" aria-busy="true">
            <div v-for="n in 4" :key="n" class="sk-quota__row">
              <span class="skeleton" style="width: 64px; height: 10px" />
              <span class="skeleton" style="height: 6px" />
              <span class="skeleton" style="width: 72px; height: 10px; justify-self: end" />
            </div>
          </div>
          <div v-else class="pad reveal">
            <QuotaMeter label="ARM OCPU" :used="account.quota.ocpuUsed" :limit="account.quota.ocpuLimit"
                        :unlimited="account.quota.unlimited.ocpu" />
            <QuotaMeter :label="t.acctDrawer.armMem" :used="account.quota.memUsed" :limit="account.quota.memLimit" unit=" GB"
                        :unlimited="account.quota.unlimited.mem" />
            <QuotaMeter :label="t.acctDrawer.amdMicro" :used="account.quota.microUsed" :limit="account.quota.microLimit" :unit="t.acctDrawer.unitInstances"
                        :unlimited="account.quota.unlimited.micro" />
            <QuotaMeter :label="t.acctDrawer.blockFree" :used="account.quota.blockUsed" :limit="account.quota.blockLimit" unit=" GB"
                        :unlimited="account.quota.unlimited.block" />
          </div>
        </SectionCard>
        <SectionCard v-if="trialOverage" :title="t.acctDrawer.trialRisk"
                     :note="t.acctDrawer.trialRiskNote(armFreeText, ALWAYS_FREE.since)">
          <div class="pad t-xs" style="color: var(--warning); line-height: 1.7">
            {{ trialOverage }}
          </div>
        </SectionCard>
        <SectionCard v-if="otherRegions.length" :title="t.acctDrawer.otherRegions" :note="t.acctDrawer.otherRegionsNote">
          <div class="pad t-xs" style="color: var(--text-secondary); line-height: 1.7">
            {{ otherRegions.join(t.acctDrawer.listSep) }}
            <br />
            {{ t.acctDrawer.otherRegionsBody }}
          </div>
        </SectionCard>
      </template>

      <template v-else-if="active === 'permissions'">
        <SectionCard :title="t.acctDrawer.credCheck" :note="t.acctDrawer.credCheckNote">
          <template #action>
            <button class="btn btn--sm" :disabled="checking" @click="recheck()">
              {{ checking ? t.acctDrawer.checking : t.acctDrawer.recheck }}
            </button>
          </template>
          <CheckList :items="permissionChecks" />
        </SectionCard>

        <SectionCard :title="t.acctDrawer.minPerms"
                     :note="t.acctDrawer.minPermsNote">
          <CheckList :items="[
            { tone: 'info', text: t.acctDrawer.permInspect },
            { tone: 'info', text: t.acctDrawer.permUse },
            { tone: 'info', text: t.acctDrawer.permManage },
            { tone: 'info', text: t.acctDrawer.permNet },
            { tone: 'info', text: t.acctDrawer.permVol },
            { tone: 'info', text: t.acctDrawer.permCap },
            { tone: 'info', text: t.acctDrawer.permRead }
          ]" />
        </SectionCard>

        <SectionCard :title="t.acctDrawer.policyExample" :note="t.acctDrawer.policyExampleNote">
          <CodeBlock copyable :code="`Allow group 'OCI Core' to manage instance-family in tenancy
Allow group 'OCI Core' to manage virtual-network-family in tenancy
Allow group 'OCI Core' to manage volume-family in tenancy
Allow group 'OCI Core' to manage compute-capacity-reports in tenancy
Allow group 'OCI Core' to read all-resources in tenancy`" />
        </SectionCard>
      </template>

      <template v-else-if="active === 'signin'">
        <SectionCard :title="t.acctDrawer.pwExpiry"
                     :note="policy?.domainName ? t.acctDrawer.domain(policy.domainName) : ''">
          <template #action>
            <button class="btn btn--sm" :disabled="policyLoading" @click="loadPolicy()">
              <span v-if="policyLoading" class="btn-spin" aria-hidden="true" />{{ policyLoading ? t.acctDrawer.readingShort : (policy ? t.acctDrawer.reread : t.acctDrawer.read) }}
            </button>
          </template>

          <SkeletonRows v-if="policyLoading && !policy" :rows="2" :height="40" />
          <CheckList v-else-if="!policy" :items="[
            { tone: 'info', text: t.acctDrawer.clickRead }
          ]" />

          <!-- 老租户根本没有身份域，也就没有密码过期这回事。
               这不是错误，说清楚比报错有用 -->
          <CheckList v-else-if="!policy.supported" :items="[
            { tone: policy.error ? 'fail' : 'info',
              text: policy.error
                ? t.acctDrawer.readFailed(policy.error)
                : t.acctDrawer.noDomain },
            { tone: 'info', text: t.acctDrawer.needAdmin }
          ]" />

          <template v-else>
            <KeyValueList :items="[
              { k: t.acctDrawer.kExpiry, v: expiryText, mono: true,
                tone: policy.expiresAfterDays ? 'var(--warning)' : 'var(--success)' },
              { k: t.acctDrawer.kWarn, v: policy.warnBeforeDays ? t.acctDrawer.days(policy.warnBeforeDays) : t.acctDrawer.notSet, mono: true },
              { k: t.acctDrawer.kMinLen, v: policy.minLength ? t.acctDrawer.chars(policy.minLength) : t.acctDrawer.notSet, mono: true },
              { k: t.acctDrawer.kPolicy, v: policy.policyName || '—', mono: true }
            ]" />

            <div class="pad">
              <CheckList :items="[
                { tone: 'info', text: policyNotice },
                { tone: 'warn',
                  text: t.acctDrawer.policyScope },
                { tone: 'info',
                  text: t.acctDrawer.policyNist }
              ]" />

              <div class="pol">
                <button class="btn btn--sm" :disabled="policySaving"
                        @click="applyPolicy(true)">{{ t.acctDrawer.tryDisable }}</button>
                <span class="pol__sep">{{ t.acctDrawer.or }}</span>
                <label class="pol__num">
                  <span class="t-2xs dim-3">{{ t.acctDrawer.setTo }}</span>
                  <input v-model.number="policyDays" class="input mono" type="number"
                         min="1" max="3650" step="30" />
                  <span class="t-2xs dim-3">{{ t.acctDrawer.daysUnit }}</span>
                </label>
                <button class="btn btn--sm btn--primary"
                        :disabled="policySaving || !policyDays"
                        @click="applyPolicy(false)">
                  {{ policySaving ? t.common.submitting : t.acctDrawer.apply }}
                </button>
              </div>
              <p class="t-2xs dim-3 pol__hint">
                {{ t.acctDrawer.disableHint }}
              </p>
            </div>
          </template>
        </SectionCard>
      </template>

      <template v-else>
        <SectionCard :title="t.acctDrawer.currentKey">
          <KeyValueList :items="[
            { k: t.acctDrawer.kFingerprint, v: account.fingerprint, mono: true, copyable: true, secret: 'fingerprint' },
            { k: t.acctDrawer.kAlgo, v: 'RSA 2048', mono: true },
            { k: t.acctDrawer.kStorage, v: t.acctDrawer.vStorage },
            { k: t.acctDrawer.kPrivate, v: t.acctDrawer.vPrivate, tone: 'var(--text-secondary)' }
          ]" />
        </SectionCard>
        <SectionCard :title="t.acctDrawer.rotateCard">
          <div class="pad">
            <CheckList :items="[{ tone: 'info', text: t.acctDrawer.rotateHint }]" />
            <div class="field">
              <label for="rotate-fp">{{ t.acctDrawer.newFingerprint }}</label>
              <input id="rotate-fp" v-model="rotateFingerprint" class="input mono"
                     placeholder="20:3b:97:13:…" spellcheck="false" autocomplete="off" />
            </div>
            <CredentialField v-model="rotateKey" :fingerprint="rotateFingerprint" />
            <div>
              <button class="btn btn--sm btn--primary" :disabled="!canRotate || rotating" @click="rotateKeyNow()">
                {{ rotating ? t.acctDrawer.rotating : t.acctDrawer.rotate }}
              </button>
            </div>
          </div>
        </SectionCard>
        <SectionCard :title="t.acctDrawer.danger">
          <div class="rule">
            <span class="rule__bar" style="background: var(--warning)" />
            <span class="t-xs">{{ account.status === 'disabled' ? t.acctDrawer.enableAccount : t.acctDrawer.disableAccount }}</span>
            <button class="btn btn--sm btn--warning rule__desc" @click="toggleDisabled()">
              {{ account.status === 'disabled' ? t.acctDrawer.enable : t.acctDrawer.disable }}
            </button>
          </div>
          <div class="rule">
            <span class="rule__bar" style="background: var(--danger)" />
            <span class="t-xs">{{ t.acctDrawer.deleteAccount }}</span>
            <button class="btn btn--sm btn--danger rule__desc" @click="askDelete()">{{ t.common.delete }}</button>
          </div>
        </SectionCard>
      </template>
      </div>
      </Transition>
    </DrawerBody>
  </AppDrawer>
</template>

<style scoped>
.sk-quota { display: flex; flex-direction: column; gap: 12px; }
.sk-quota__row { display: grid; grid-template-columns: 80px 1fr 108px; gap: 10px; align-items: center; height: 16px; }
.pol { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-top: 14px; }
.pol__sep { font-size: 11px; color: var(--text-tertiary); }
.pol__num { display: flex; align-items: center; gap: 6px; }
.pol__num .input { width: 84px; }
.pol__hint { margin: 10px 0 0; line-height: 1.8; }

.lamp { display: inline-flex; align-items: center; gap: 6px; padding: 3px 10px; border-radius: var(--radius-full); background: var(--bg-inset); color: var(--c); font-size: 12px; font-weight: 600; }
.lamp__dot { width: 6px; height: 6px; border-radius: var(--radius-full); background: var(--c); }
.lamp__dot.is-pulsing { animation: pulse 2s ease-in-out infinite; }
.rule { display: flex; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid var(--border-subtle); }
.rule__bar { width: 3px; height: 22px; flex: 0 0 auto; border-radius: var(--radius-full); }
.rule__desc { margin-left: auto; }
.pad { padding: 14px 16px; display: flex; flex-direction: column; gap: 8px; }
</style>
