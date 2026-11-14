<script setup lang="ts">
/** §4.3.1 添加账号抽屉（520）：整段粘贴 OCI config 自动解析 + 逐项校验 */
import { t } from '@/i18n'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import { mask } from '@/lib/mask'
import { accounts as accountsApi, proxies as proxiesApi, errorText, type CheckStepDTO, type ProxyDTO } from '@/api'
import type { AccountColorIndex, CheckItem } from '@/types'
import AppDrawer from '@/components/AppDrawer.vue'
import DrawerBody from '@/components/DrawerBody.vue'
import SectionCard from '@/components/SectionCard.vue'
import CredentialField from '@/components/CredentialField.vue'
import CheckList from '@/components/CheckList.vue'
import SelectMenu from '@/components/SelectMenu.vue'

const { state, closeDrawer, toast, toastError, nextColorIndex, loadAccounts, syncNow } = useStore()

const form = reactive({
  paste: '',
  parsed: false,
  tenancy: '',
  user: '',
  fingerprint: '',
  region: '',
  key: '',
  alias: '',
  code: '',
  colorIndex: nextColorIndex() as AccountColorIndex,
  /** 网络出口：代理 id；DIRECT 表示明确选了直连；空串表示还没选 */
  egress: ''
})

/* ---------- 网络出口 ---------- */

/*
 * 出口必须在测试连接之前就定下来。测试连接是这个租户第一次被访问，
 * 用本机 IP 直连一次，之后再绑代理也抹不掉这次记录——这正是代理要避免的
 * 关联信号。所以代理池里有空闲代理时，不选就不让测试、不让保存。
 */
const DIRECT = '__direct__'
const router = useRouter()
const proxyList = ref<ProxyDTO[]>([])
const boundProxyIds = ref<Set<string>>(new Set())
const proxiesLoaded = ref(false)

/** 可选的代理：启用的、还没绑给别的账号的。一条代理只能绑一个账号。 */
const freeProxies = computed(() =>
  proxyList.value.filter(p => p.enabled && !boundProxyIds.value.has(p.id)))

function proxyLabel(p: ProxyDTO): string {
  const addr = `${p.host}:${p.port}`
  const shown = state.privacyMode ? mask(addr, 'hostport') : addr
  const status = p.lastStatus === 'ok' ? `${p.lastLatencyMs} ms`
    : p.lastStatus === 'fail' ? t.addAcctProxy.add.lastFail : t.addAcctProxy.add.unchecked
  return `${p.label ? p.label + ' · ' : ''}${shown} · ${status}`
}

const egressGroups = computed(() => [
  { label: t.addAcctProxy.add.proxyPool, options: freeProxies.value.map(p => ({ value: p.id, label: proxyLabel(p) })) },
  { label: '', options: [{ value: DIRECT, label: t.addAcctProxy.add.direct }] }
])

async function loadProxies() {
  try {
    const list = await proxiesApi.list()
    proxyList.value = list.proxies ?? []
    boundProxyIds.value = new Set(Object.values(list.bindings ?? {}))
  } catch {
    // 读不到代理池就当它是空的，下面会按"没有可用代理"处理。
    proxyList.value = []
  } finally {
    proxiesLoaded.value = true
    // 没有可用代理时只能直连，不必逼用户多点一下；有的话留空，让用户自己选。
    if (!freeProxies.value.length && !form.egress) form.egress = DIRECT
  }
}
onMounted(() => void loadProxies())

/** 换了出口，之前那次测试走的是另一条路，结果作废。 */
watch(() => form.egress, () => {
  steps.value = []
  testError.value = ''
  testAdvice.value = ''
})

const proxyId = computed(() => (form.egress && form.egress !== DIRECT ? form.egress : undefined))

function goProxies() {
  closeDrawer()
  void router.push('/proxies')
}

const testing = ref(false)
const saving = ref(false)
const steps = ref<CheckStepDTO[]>([])
const testError = ref('')
const testAdvice = ref('')

/**
 * 一个交互消除 80% 的配置错误：整段粘贴 → 自动填充。
 *
 * 解析交给后端而不是前端正则：后端要处理 BOM、冒号分隔、
 * 缺少 [DEFAULT] 头、多 profile 等一堆边角情况，两边各写一遍
 * 只会让它们慢慢跑偏。
 */
async function parseConfig() {
  try {
    const { profiles } = await accountsApi.parseConfig(form.paste)
    const p = profiles[0]

    form.tenancy = p.tenancyOcid
    form.user = p.userOcid
    form.fingerprint = p.fingerprint
    form.region = p.region
    form.parsed = true
    if (!form.code && p.suggestedCode) form.code = p.suggestedCode

    const found = ['user', 'fingerprint', 'tenancy', 'region']
      .filter(k => !(p.missing ?? []).includes(k))

    if (p.hasPassPhrase) {
      toast({
        tone: 'warning',
        title: t.addAcctProxy.add.passphraseTitle,
        body: t.addAcctProxy.add.passphraseBody
      }, 12000)
      return
    }

    toast({
      tone: p.complete ? 'success' : 'warning',
      title: t.addAcctProxy.add.parsed(found.length),
      body: p.complete ? found.join(' · ') : t.addAcctProxy.add.stillMissing((p.missing ?? []).join(t.addAcctProxy.add.listSep))
    })
  } catch (err) {
    toastError(t.addAcctProxy.add.parseFailed, err)
  }
}

const takenColors = computed(() => new Set(state.accounts.map(a => a.colorIndex)))
const codeTaken = computed(() =>
  state.accounts.some(a => a.code.toUpperCase() === form.code.toUpperCase())
)

const canTest = computed(() =>
  !!form.tenancy && !!form.user && !!form.fingerprint && form.key.trim().length > 20 && !!form.egress
)

const canSave = computed(() =>
  canTest.value && !!form.alias.trim() &&
  form.code.length >= 2 && form.code.length <= 4 && !codeTaken.value
)

/** 把后端的逐项校验结果映射成 CheckList 的条目。 */
const checks = computed<CheckItem[]>(() => {
  if (testing.value && steps.value.length === 0) {
    return [{ tone: 'info', text: t.addAcctProxy.add.checking }]
  }
  if (steps.value.length === 0) {
    return [{ tone: 'info', text: form.egress
      ? t.addAcctProxy.add.clickTest
      : t.addAcctProxy.add.pickEgressFirst }]
  }

  const items: CheckItem[] = steps.value.map(s => ({
    tone: s.ok ? 'ok' : 'fail',
    text: s.detail ? `${s.label} · ${s.detail}` : s.label
  }))
  if (testError.value) {
    items.push({ tone: 'fail', text: testError.value })
  }
  if (testAdvice.value) {
    items.push({ tone: 'warn', text: testAdvice.value })
  }
  return items
})

async function runTest() {
  testing.value = true
  steps.value = []
  testError.value = ''
  testAdvice.value = ''

  try {
    const result = await accountsApi.checkDraft({
      tenancyOcid: form.tenancy,
      userOcid: form.user,
      fingerprint: form.fingerprint,
      privateKeyPem: form.key,
      region: form.region,
      proxyId: proxyId.value
    })
    steps.value = result.steps
    if (!result.ok) {
      testError.value = [result.errorCode, result.errorText].filter(Boolean).join(' · ')
      testAdvice.value = result.advice ?? ''
    } else if (result.advice) {
      testAdvice.value = result.advice
    }
    // 校验顺带把区域订阅带回来了，用主区域补全默认区域。
    if (result.ok && result.homeRegion && !form.region) form.region = result.homeRegion
  } catch (err) {
    testError.value = errorText(err)
  } finally {
    testing.value = false
  }
}

async function save() {
  if (!canSave.value || saving.value) return

  saving.value = true
  try {
    const { account } = await accountsApi.create({
      alias: form.alias.trim(),
      code: form.code.toUpperCase(),
      colorIndex: form.colorIndex,
      tenancyOcid: form.tenancy,
      userOcid: form.user,
      fingerprint: form.fingerprint,
      privateKeyPem: form.key,
      defaultRegion: form.region,
      proxyId: proxyId.value
    })

    await loadAccounts()
    state.accountFilter.add(account.id)
    closeDrawer()
    toast({
      tone: 'success',
      title: t.addAcctProxy.add.connected(account.alias),
      body: t.addAcctProxy.add.connectedBody(account.code)
    })
    // 新账号刚接进来，立刻拉一次它的实例。
    void syncNow(account.id)
  } catch (err) {
    toastError(t.addAcctProxy.add.saveFailed, err)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <AppDrawer width="narrow" @close="closeDrawer()">
    <header class="head">
      <h2 class="head__title">{{ t.addAcctProxy.add.title }}</h2>
      <button class="head__close" :aria-label="t.common.close" @click="closeDrawer()">✕</button>
    </header>

    <DrawerBody>
      <SectionCard :title="t.addAcctProxy.add.pasteTitle" :note="t.addAcctProxy.add.pasteNote">
        <div class="pad">
          <textarea v-model="form.paste" class="textarea paste" spellcheck="false"
                    placeholder="[DEFAULT]&#10;user=ocid1.user.oc1..aaaa…&#10;fingerprint=20:3b:97:13:…&#10;tenancy=ocid1.tenancy.oc1..aaaa…&#10;region=ap-tokyo-1&#10;key_file=/path/to/key.pem" />
          <div class="row">
            <button class="btn btn--sm" style="border-color: var(--accent); color: var(--accent)"
                    :disabled="!form.paste.trim()" @click="parseConfig()">{{ t.addAcctProxy.add.parseFill }}</button>
          </div>
        </div>
      </SectionCard>

      <SectionCard :title="t.addAcctProxy.add.credTitle" :note="t.addAcctProxy.add.credNote">
        <div class="pad">
          <div v-for="fld in [
            { key: 'tenancy', label: t.addAcctProxy.add.tenancy, ph: 'ocid1.tenancy.oc1..' },
            { key: 'user', label: t.addAcctProxy.add.user, ph: 'ocid1.user.oc1..' },
            { key: 'fingerprint', label: t.addAcctProxy.add.fingerprint, ph: '20:3b:97:13:…' },
            { key: 'region', label: t.addAcctProxy.add.region, ph: 'ap-tokyo-1' }
          ]" :key="fld.key" class="field">
            <label>
              {{ fld.label }}
              <span v-if="form.parsed && (form as any)[fld.key]" class="ok-tag">{{ t.addAcctProxy.add.recognized }}</span>
            </label>
            <input v-model="(form as any)[fld.key]" class="input mono"
                   :class="{ 'is-ok': form.parsed && (form as any)[fld.key] }" :placeholder="fld.ph" />
          </div>
          <CredentialField v-model="form.key" :fingerprint="form.fingerprint" />
        </div>
      </SectionCard>

      <SectionCard :title="t.addAcctProxy.add.identityTitle" :note="t.addAcctProxy.add.identityNote">
        <div class="pad">
          <div class="identity">
            <div class="field">
              <label>{{ t.addAcctProxy.add.alias }}</label>
              <input v-model="form.alias" class="input" :placeholder="t.addAcctProxy.add.aliasPlaceholder" />
            </div>
            <div class="field">
              <label>{{ t.addAcctProxy.add.code }}</label>
              <input v-model="form.code" class="input mono code-input" maxlength="4" placeholder="TYO"
                     :style="{ color: acctColor(form.colorIndex) }"
                     @input="form.code = form.code.toUpperCase()" />
            </div>
          </div>
          <p v-if="codeTaken" class="warn-note">{{ t.addAcctProxy.add.codeTaken }}</p>
          <p v-else-if="form.code && (form.code.length < 2)" class="warn-note">{{ t.addAcctProxy.add.codeLength }}</p>

          <div class="field">
            <label>{{ t.addAcctProxy.add.color }}</label>
            <div class="swatches">
              <button v-for="n in 8" :key="n" class="swatch"
                      :class="{ 'is-picked': form.colorIndex === n, 'is-taken': takenColors.has(n as AccountColorIndex) }"
                      :style="{ background: acctColor(n) }"
                      :aria-label="t.addAcctProxy.add.colorN(n, takenColors.has(n as AccountColorIndex))"
                      @click="form.colorIndex = n as AccountColorIndex" />
            </div>
          </div>

          <div class="preview">
            <span class="preview__bar" :style="{ background: acctColor(form.colorIndex) }" />
            <span class="mono preview__code" :style="{ color: acctColor(form.colorIndex) }">{{ form.code || '???' }}</span>
            <span class="t-xs dim">{{ form.alias || t.addAcctProxy.add.unnamedAccount }}</span>
            <span class="preview__hint t-2xs dim-3">{{ t.addAcctProxy.add.previewHint }}</span>
          </div>
        </div>
      </SectionCard>

      <SectionCard :title="t.addAcctProxy.add.egressTitle" :note="t.addAcctProxy.add.egressNote">
        <div class="pad">
          <SelectMenu v-model="form.egress" :groups="egressGroups"
                      :placeholder="proxiesLoaded ? t.addAcctProxy.add.pickEgress : t.addAcctProxy.add.loadingPool"
                      :disabled="!proxiesLoaded" :min-width="300" :aria-label="t.addAcctProxy.add.egressTitle" />
          <CheckList v-if="proxiesLoaded && !freeProxies.length" :items="[{ tone: 'warn',
            text: proxyList.length
              ? t.addAcctProxy.add.noFreeProxies
              : t.addAcctProxy.add.emptyPool }]" />
          <CheckList v-else-if="form.egress === DIRECT" :items="[{ tone: 'warn',
            text: t.addAcctProxy.add.directWarn }]" />
          <button v-if="proxiesLoaded && !freeProxies.length" class="btn btn--sm egress__link"
                  @click="goProxies()">{{ t.addAcctProxy.add.goProxies }}</button>
        </div>
      </SectionCard>

      <SectionCard :title="t.addAcctProxy.add.testTitle" :note="t.addAcctProxy.add.testNote">
        <template #action>
          <button class="btn btn--sm" style="border-color: var(--accent); color: var(--accent)"
                  :disabled="testing || !canTest" @click="runTest()">
            {{ testing ? t.addAcctProxy.add.checking : steps.length ? t.addAcctProxy.add.retest : t.addAcctProxy.add.test }}
          </button>
        </template>
        <CheckList :items="checks" />
      </SectionCard>
    </DrawerBody>

    <footer class="foot">
      <span class="t-2xs dim-3">{{ t.addAcctProxy.add.keyNote }}</span>
      <button class="btn" @click="closeDrawer()">{{ t.common.cancel }}</button>
      <button class="btn btn--primary" :disabled="!canSave || saving" @click="save()">
        {{ saving ? t.common.saving : t.addAcctProxy.add.saveAccount }}
      </button>
    </footer>
  </AppDrawer>
</template>

<style scoped>
.head { flex: 0 0 auto; display: flex; align-items: center; gap: 10px; padding: 18px 20px; border-bottom: 1px solid var(--border-subtle); }
.head__title { margin: 0; flex: 1 1 auto; font-size: 20px; line-height: 28px; font-weight: 600; }
.head__close { border: 0; background: none; color: var(--text-secondary); cursor: pointer; font-size: 14px; }
.pad { padding: 16px; display: flex; flex-direction: column; gap: 12px; }
.paste { height: 108px; }
.row { display: flex; gap: 8px; }
.egress__link { align-self: flex-start; }
.ok-tag { margin-left: 6px; font-size: 11px; color: var(--success); }
.input.is-ok { border-color: var(--success); background: var(--success-soft); }
.identity { display: grid; grid-template-columns: 1fr 104px; gap: 10px; }
.code-input { font-weight: 600; text-transform: uppercase; }
.warn-note { margin: 0; font-size: 11px; color: var(--warning); }
.swatches { display: flex; gap: 8px; }
.swatch { width: 28px; height: 28px; border: 0; border-radius: var(--radius-full); cursor: pointer; opacity: 0.8; }
.swatch.is-taken { opacity: 0.4; }
.swatch.is-picked { opacity: 1; box-shadow: 0 0 0 2px var(--bg-elevated), 0 0 0 4px var(--accent); }
.preview { display: flex; align-items: center; gap: 9px; padding: 10px 12px; border-radius: var(--radius-md); background: var(--bg-inset); }
.preview__bar { width: 3px; height: 22px; }
.preview__code { font-size: 11px; font-weight: 600; }
.preview__hint { margin-left: auto; }
.foot {
  flex: 0 0 auto; padding: 14px 20px; border-top: 1px solid var(--border-subtle);
  background: var(--bg-surface); display: flex; align-items: center; gap: 10px;
}
.foot > span { flex: 1 1 auto; }
</style>
