<script setup lang="ts">
/** §4.3.1 添加账号抽屉（520）：整段粘贴 OCI config 自动解析 + 逐项校验 */
import { computed, reactive, ref } from 'vue'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import { accounts as accountsApi, errorText, type CheckStepDTO } from '@/api'
import type { AccountColorIndex, CheckItem } from '@/types'
import AppDrawer from '@/components/AppDrawer.vue'
import DrawerBody from '@/components/DrawerBody.vue'
import SectionCard from '@/components/SectionCard.vue'
import CredentialField from '@/components/CredentialField.vue'
import CheckList from '@/components/CheckList.vue'

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
  colorIndex: nextColorIndex() as AccountColorIndex
})

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
        title: '该私钥带有口令',
        body: '本工具不支持带口令的私钥，请先解密：\nopenssl rsa -in oci_api_key.pem -out oci_api_key_plain.pem'
      }, 12000)
      return
    }

    toast({
      tone: p.complete ? 'success' : 'warning',
      title: `已解析 ${found.length} 个字段`,
      body: p.complete ? found.join(' · ') : `仍缺少：${(p.missing ?? []).join('、')}`
    })
  } catch (err) {
    toastError('解析失败', err)
  }
}

const takenColors = computed(() => new Set(state.accounts.map(a => a.colorIndex)))
const codeTaken = computed(() =>
  state.accounts.some(a => a.code.toUpperCase() === form.code.toUpperCase())
)

const canTest = computed(() =>
  !!form.tenancy && !!form.user && !!form.fingerprint && form.key.trim().length > 20
)

const canSave = computed(() =>
  canTest.value && !!form.alias.trim() &&
  form.code.length >= 2 && form.code.length <= 4 && !codeTaken.value
)

/** 把后端的逐项校验结果映射成 CheckList 的条目。 */
const checks = computed<CheckItem[]>(() => {
  if (testing.value && steps.value.length === 0) {
    return [{ tone: 'info', text: '校验中…' }]
  }
  if (steps.value.length === 0) {
    return [{ tone: 'info', text: '点击「测试连接」逐项校验凭据与权限' }]
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
      region: form.region
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
      defaultRegion: form.region
    })

    await loadAccounts()
    state.accountFilter.add(account.id)
    closeDrawer()
    toast({
      tone: 'success',
      title: `${account.alias} 已接入`,
      body: `身份色与短代号 ${account.code} 已绑定，正在同步实例列表`
    })
    // 新账号刚接进来，立刻拉一次它的实例。
    void syncNow(account.id)
  } catch (err) {
    toastError('保存账号失败', err)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <AppDrawer width="narrow" @close="closeDrawer()">
    <header class="head">
      <h2 class="head__title">添加 Oracle 账号</h2>
      <button class="head__close" aria-label="关闭" @click="closeDrawer()">✕</button>
    </header>

    <DrawerBody>
      <SectionCard title="粘贴 OCI config" note="从 Oracle 控制台下载的 config 整段粘贴">
        <div class="pad">
          <textarea v-model="form.paste" class="textarea paste" spellcheck="false"
                    placeholder="[DEFAULT]&#10;user=ocid1.user.oc1..aaaa…&#10;fingerprint=20:3b:97:13:…&#10;tenancy=ocid1.tenancy.oc1..aaaa…&#10;region=ap-tokyo-1&#10;key_file=/path/to/key.pem" />
          <div class="row">
            <button class="btn btn--sm" style="border-color: var(--accent); color: var(--accent)"
                    :disabled="!form.paste.trim()" @click="parseConfig()">解析并填充</button>
          </div>
        </div>
      </SectionCard>

      <SectionCard title="凭据字段" note="已识别项高亮">
        <div class="pad">
          <div v-for="fld in [
            { key: 'tenancy', label: '租户 OCID', ph: 'ocid1.tenancy.oc1..' },
            { key: 'user', label: '用户 OCID', ph: 'ocid1.user.oc1..' },
            { key: 'fingerprint', label: '密钥指纹', ph: '20:3b:97:13:…' },
            { key: 'region', label: '默认区域', ph: 'ap-tokyo-1' }
          ]" :key="fld.key" class="field">
            <label>
              {{ fld.label }}
              <span v-if="form.parsed && (form as any)[fld.key]" class="ok-tag">✔ 已识别</span>
            </label>
            <input v-model="(form as any)[fld.key]" class="input mono"
                   :class="{ 'is-ok': form.parsed && (form as any)[fld.key] }" :placeholder="fld.ph" />
          </div>
          <CredentialField v-model="form.key" :fingerprint="form.fingerprint" />
        </div>
      </SectionCard>

      <SectionCard title="账号身份" note="别名 + 短代号 + 身份色">
        <div class="pad">
          <div class="identity">
            <div class="field">
              <label>别名</label>
              <input v-model="form.alias" class="input" placeholder="例如：东京主号" />
            </div>
            <div class="field">
              <label>短代号</label>
              <input v-model="form.code" class="input mono code-input" maxlength="4" placeholder="TYO"
                     :style="{ color: acctColor(form.colorIndex) }"
                     @input="form.code = form.code.toUpperCase()" />
            </div>
          </div>
          <p v-if="codeTaken" class="warn-note">该短代号已被占用，请换一个（代号必须唯一）</p>
          <p v-else-if="form.code && (form.code.length < 2)" class="warn-note">代号需要 2–4 个字符</p>

          <div class="field">
            <label>身份色 · 已自动分配未被占用的一色</label>
            <div class="swatches">
              <button v-for="n in 8" :key="n" class="swatch"
                      :class="{ 'is-picked': form.colorIndex === n, 'is-taken': takenColors.has(n as AccountColorIndex) }"
                      :style="{ background: acctColor(n) }"
                      :aria-label="`身份色 ${n}${takenColors.has(n as AccountColorIndex) ? '（已被占用）' : ''}`"
                      @click="form.colorIndex = n as AccountColorIndex" />
            </div>
          </div>

          <div class="preview">
            <span class="preview__bar" :style="{ background: acctColor(form.colorIndex) }" />
            <span class="mono preview__code" :style="{ color: acctColor(form.colorIndex) }">{{ form.code || '???' }}</span>
            <span class="t-xs dim">{{ form.alias || '未命名账号' }}</span>
            <span class="preview__hint t-2xs dim-3">列表中的样子</span>
          </div>
        </div>
      </SectionCard>

      <SectionCard title="连接校验" note="逐项亮起">
        <template #action>
          <button class="btn btn--sm" style="border-color: var(--accent); color: var(--accent)"
                  :disabled="testing || !canTest" @click="runTest()">
            {{ testing ? '校验中…' : steps.length ? '重新测试' : '测试连接' }}
          </button>
        </template>
        <CheckList :items="checks" />
      </SectionCard>
    </DrawerBody>

    <footer class="foot">
      <span class="t-2xs dim-3">私钥以 AES-256-GCM 加密后写入本地数据库，界面永不回显</span>
      <button class="btn" @click="closeDrawer()">取消</button>
      <button class="btn btn--primary" :disabled="!canSave || saving" @click="save()">
        {{ saving ? '保存中…' : '保存账号' }}
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
