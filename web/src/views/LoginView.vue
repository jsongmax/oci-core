<script setup lang="ts">
/**
 * §4.1 登录：两步式，TOTP 六格自动跳格 + 粘贴分配 + 填满自动提交。
 *
 * 同一张卡片承载四个阶段：首次设置 → 登录 → 两步验证 → 绑定验证器。
 * 做成一个视图而不是四个路由，是因为它们共用同一套外壳与动画，
 * 拆开只会让"设置完自动进入绑定"这类衔接变得别扭。
 */
import { t, locale, setLocale } from '@/i18n'
import { computed, nextTick, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useStore } from '@/store'
import { auth, ApiError, errorText } from '@/api'
import { copy } from '@/lib/format'
import QrCode from '@/components/QrCode.vue'

type Mode = 'setup' | 'login' | 'totp' | 'enroll'

const router = useRouter()
const { state, afterLogin, toast } = useStore()

const mode = ref<Mode>('login')
const username = ref('admin')
const password = ref('')
const passwordConfirm = ref('')
const totpCode = ref('')
const totpInput = ref<HTMLInputElement | null>(null)
const totpFocused = ref(false)
const error = ref('')
const busy = ref(false)
const shakeKey = ref(0)

/** 当前输入焦点所在的视觉格下标（0..5） */
const activeSlot = computed(() => Math.min(totpCode.value.length, 5))

/** 绑定验证器时后端下发的密钥与 otpauth URI */
const totpSecret = ref('')
const totpUri = ref('')

/** 密钥按 4 位分组，方便在验证器里手动录入 */
const groupedSecret = computed(() =>
  totpSecret.value.replace(/(.{4})/g, '$1 ').trim()
)

onMounted(async () => {
  if (state.authed === null) await useStore().loadStatus()
  mode.value = state.setupRequired ? 'setup' : state.totpRequired ? 'totp' : 'login'
  await nextTick()
  if (mode.value === 'totp') totpInput.value?.focus()
})

function fail(message: string) {
  error.value = message
  shakeKey.value++
}

function resetCode() {
  totpCode.value = ''
  if (totpInput.value) totpInput.value.value = ''
  nextTick(() => totpInput.value?.focus())
}

/* ---------- 首次设置 ---------- */

async function doSetup() {
  error.value = ''
  if (!username.value.trim()) { fail(t.loginPage.usernameRequired); return }
  if (password.value.length < 10) { fail(t.loginPage.passwordTooShort); return }
  if (password.value !== passwordConfirm.value) { fail(t.loginPage.passwordMismatch); return }

  busy.value = true
  try {
    await auth.setup(username.value.trim(), password.value)
    // 创建完账户直接登录，再引导绑定验证器——
    // 后端的 nextStep 明确要求走这一步。
    await auth.login(username.value.trim(), password.value)
    const setup = await auth.totpSetup()
    totpSecret.value = setup.secret
    totpUri.value = setup.uri
    totpCode.value = ''
    mode.value = 'enroll'
    passwordConfirm.value = ''
    await nextTick()
    totpInput.value?.focus()
  } catch (err) {
    fail(errorText(err))
  } finally {
    busy.value = false
  }
}

/* ---------- 登录 ---------- */

async function doLogin() {
  error.value = ''
  if (!username.value || !password.value) { fail(t.loginPage.credentialsRequired); return }

  busy.value = true
  try {
    const result = await auth.login(username.value.trim(), password.value)
    password.value = ''
    if (result.totpRequired) {
      totpCode.value = ''
      mode.value = 'totp'
      await nextTick()
      totpInput.value?.focus()
      return
    }
    await enter()
  } catch (err) {
    // 后端对"用户名不存在"和"口令错误"返回完全一致的响应（防枚举），
    // 因此这里也只能原样透传，不做任何区分。
    fail(errorText(err))
  } finally {
    busy.value = false
  }
}

/* ---------- 两步验证 ---------- */

async function submitCode() {
  const code = totpCode.value.replace(/\D/g, '').slice(0, 6)
  if (code.length !== 6 || busy.value) return

  busy.value = true
  error.value = ''
  try {
    if (mode.value === 'enroll') {
      await auth.totpEnable(code)
      toast({ tone: 'success', title: t.loginPage.totpEnabled })
    } else {
      await auth.verifyTotp(code)
    }
    await enter()
  } catch (err) {
    const hint = err instanceof ApiError && err.code === 'code_reused'
      ? t.loginPage.codeReused
      : errorText(err)
    fail(hint)
    resetCode()
  } finally {
    busy.value = false
  }
}

async function enter() {
  await afterLogin()
  router.push('/overview')
}

/** 跳过绑定。允许但要说清后果——面板持有全部租户的控制权。 */
async function skipEnroll() {
  toast({
    tone: 'warning',
    title: t.loginPage.skippedTitle,
    body: t.loginPage.skippedBody
  }, 9000)
  await enter()
}

/* ---------- 六格验证码输入（单真实 input 覆盖六格视觉，兼容 1Password 等自动填充） ---------- */

function setTotpCode(raw: string) {
  const cleaned = raw.replace(/\D/g, '').slice(0, 6)
  totpCode.value = cleaned
  if (totpInput.value && totpInput.value.value !== cleaned) {
    totpInput.value.value = cleaned
  }
  error.value = ''
  if (cleaned.length === 6) void submitCode()
}

function onTotpInput(e: Event) {
  const el = e.target as HTMLInputElement
  setTotpCode(el.value)
}

function onTotpPaste(e: ClipboardEvent) {
  setTotpCode(e.clipboardData?.getData('text') ?? '')
}
</script>

<template>
  <div class="login">
    <div class="login__grid" aria-hidden="true" />
    <div class="login__glow login__glow--a" aria-hidden="true" />
    <div class="login__glow login__glow--b" aria-hidden="true" />

    <div :key="shakeKey" class="login__card glass" :class="{ 'is-shaking': error }">
      <div class="login__brand"><span class="login__mark" /><span>OCI Core</span></div>

      <!-- 首次设置 -->
      <form v-if="mode === 'setup'" class="login__form" @submit.prevent="doSetup">
        <p class="login__step-title">{{ t.loginPage.setupTitle }}</p>
        <p class="login__step-sub">{{ t.loginPage.setupSub }}</p>
        <div class="field">
          <label for="u">{{ t.loginPage.username }}</label>
          <input id="u" v-model="username" class="input mono" autocomplete="username" />
        </div>
        <div class="field">
          <label for="p">{{ t.loginPage.password }}</label>
          <input id="p" v-model="password" type="password" class="input" autocomplete="new-password" />
        </div>
        <div class="field">
          <label for="p2">{{ t.loginPage.passwordConfirm }}</label>
          <input id="p2" v-model="passwordConfirm" type="password" class="input" autocomplete="new-password" />
        </div>
        <p v-if="error" class="login__error" role="alert">{{ error }}</p>
        <button class="btn btn--primary login__submit" type="submit" :disabled="busy">
          {{ busy ? t.loginPage.creating : t.loginPage.createAccount }}
        </button>
      </form>

      <!-- 登录 -->
      <form v-else-if="mode === 'login'" class="login__form" @submit.prevent="doLogin">
        <div class="field">
          <label for="u2">{{ t.loginPage.username }}</label>
          <input id="u2" v-model="username" class="input mono" autocomplete="username" />
        </div>
        <div class="field">
          <label for="p3">{{ t.loginPage.password }}</label>
          <input id="p3" v-model="password" type="password" class="input" autocomplete="current-password" />
        </div>
        <p v-if="error" class="login__error" role="alert">{{ error }}</p>
        <button class="btn btn--primary login__submit" type="submit" :disabled="busy">
          {{ busy ? t.loginPage.signingIn : t.loginPage.signIn }}
        </button>
      </form>

      <!-- 绑定验证器 -->
      <form v-else-if="mode === 'enroll'" class="login__form" @submit.prevent="submitCode">
        <p class="login__step-title">{{ t.loginPage.enrollTitle }}</p>
        <p class="login__step-sub">
          {{ t.loginPage.enrollSub }}
        </p>

        <QrCode v-if="totpUri" class="login__qr" :value="totpUri" :label="t.loginPage.enrollQr" />

        <div class="login__secret">
          <code class="login__secret-value mono">{{ groupedSecret }}</code>
          <button class="btn login__copy" type="button" @click="copy(totpSecret)">{{ t.common.copy }}</button>
        </div>
        <a class="login__uri mono" :href="totpUri">{{ t.loginPage.openInApp }}</a>

        <label class="sr-only" for="totp-code">{{ t.loginPage.totpTitle }}</label>
        <div class="login__totp">
          <div class="login__boxes" aria-hidden="true">
            <span v-for="i in 6" :key="i" class="login__box mono"
                  :class="{ 'is-active': totpFocused && activeSlot === i - 1, 'is-error': error }">
              {{ totpCode[i - 1] ?? '' }}
            </span>
          </div>
          <input id="totp-code" ref="totpInput" :value="totpCode" class="login__totp-input mono"
                 name="totp" type="text" inputmode="numeric" pattern="[0-9]*" maxlength="6"
                 autocomplete="one-time-code" spellcheck="false" autocapitalize="off"
                 @focus="totpFocused = true" @blur="totpFocused = false"
                 @input="onTotpInput" @paste.prevent="onTotpPaste" />
        </div>
        <p v-if="error" class="login__error" role="alert">{{ error }}</p>
        <button class="btn login__skip" type="button" @click="skipEnroll">{{ t.loginPage.enrollLater }}</button>
      </form>

      <!-- 两步验证 -->
      <form v-else class="login__form" @submit.prevent="submitCode">
        <p class="login__step-title">{{ t.loginPage.totpTitle }}</p>
        <p class="login__step-sub">{{ t.loginPage.totpSub }}</p>
        <label class="sr-only" for="totp-code">{{ t.loginPage.totpTitle }}</label>
        <div class="login__totp">
          <div class="login__boxes" aria-hidden="true">
            <span v-for="i in 6" :key="i" class="login__box mono"
                  :class="{ 'is-active': totpFocused && activeSlot === i - 1, 'is-error': error }">
              {{ totpCode[i - 1] ?? '' }}
            </span>
          </div>
          <input id="totp-code" ref="totpInput" :value="totpCode" class="login__totp-input mono"
                 name="totp" type="text" inputmode="numeric" pattern="[0-9]*" maxlength="6"
                 autocomplete="one-time-code" spellcheck="false" autocapitalize="off"
                 @focus="totpFocused = true" @blur="totpFocused = false"
                 @input="onTotpInput" @paste.prevent="onTotpPaste" />
        </div>
        <p v-if="error" class="login__error" role="alert">{{ error }}</p>
      </form>

      <footer class="login__foot">
        <!-- 这里原来写着"本面板持有你的 OCI 密钥"。登录页对未认证的访客
             是公开可达的，等于主动告诉每个扫到它的人这台机器上有什么值得偷，
             属于白送的信息。该提醒的对象是部署者，位置应该在 README 与
             服务端启动日志里，不是登录页。 -->
        <span class="mono">{{ state.version || 'dev' }}</span>
        <button type="button" class="login__lang" @click="setLocale(locale === 'zh-CN' ? 'en' : 'zh-CN')">
          {{ t.nav.switchLanguage }}
        </button>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.login__lang { margin-left: auto; border: 0; background: none; color: var(--text-tertiary); font-size: 11px; cursor: pointer; }
.login__lang:hover { color: var(--text-primary); }
.login { position: fixed; inset: 0; display: flex; align-items: center; justify-content: center; overflow: hidden; background: var(--bg-base); }
.login__grid {
  position: absolute; inset: 0; opacity: 0.55;
  background-image:
    linear-gradient(var(--border-subtle) 1px, transparent 1px),
    linear-gradient(90deg, var(--border-subtle) 1px, transparent 1px);
  background-size: 48px 48px;
}
.login__glow { position: absolute; border-radius: var(--radius-full); filter: blur(30px); }
.login__glow--a {
  top: -140px; left: 14%; width: 600px; height: 600px; opacity: 0.13;
  background: radial-gradient(circle, var(--accent) 0%, transparent 62%);
  animation: drift 60s ease-in-out infinite;
}
.login__glow--b {
  bottom: -200px; right: 10%; width: 560px; height: 560px; opacity: 0.12;
  background: radial-gradient(circle, var(--acct-6) 0%, transparent 62%);
  animation: drift 74s ease-in-out infinite reverse;
}
.login__card {
  position: relative; width: 400px; padding: 32px; border-radius: var(--radius-xl); box-shadow: var(--shadow-4);
}
.login__card.is-shaking { animation: shake var(--dur-slow) var(--ease-standard); }
.login__brand { display: flex; align-items: center; gap: 9px; margin-bottom: 26px; font-size: 16px; font-weight: 600; }
.login__mark { width: 22px; height: 22px; border-radius: var(--radius-sm); background: var(--accent); opacity: 0.9; }
.login__form { display: flex; flex-direction: column; gap: 14px; }
.login__submit { margin-top: 6px; height: 40px; }
.login__skip { margin-top: 2px; height: 34px; font-size: 12px; }
.login__step-title { margin: 0; font-size: 14px; font-weight: 500; }
.login__step-sub { margin: -8px 0 0; font-size: 12px; color: var(--text-secondary); line-height: 18px; }
.login__secret {
  display: flex; align-items: center; gap: 8px;
  padding: 10px 12px; border-radius: var(--radius-md);
  border: 1px solid var(--border-default); background: var(--bg-inset);
}
.login__secret-value { flex: 1 1 auto; font-size: 13px; letter-spacing: 0.06em; word-break: break-all; }
.login__copy { flex: none; height: 28px; font-size: 11px; }
.login__qr { align-self: center; }
.login__uri { margin-top: -6px; font-size: 11px; color: var(--accent); }
.sr-only {
  position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px;
  overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0;
}
.login__totp { position: relative; }
.login__boxes { display: flex; gap: 8px; }
.login__box {
  flex: 1 1 0; height: 52px; display: flex; align-items: center; justify-content: center;
  border-radius: var(--radius-md); border: 1px solid var(--border-default); background: var(--bg-inset);
  color: var(--text-primary); font-size: 22px; font-weight: 600;
  transition: border-color var(--dur-fast), box-shadow var(--dur-fast);
}
.login__box.is-active { border-color: var(--accent); box-shadow: var(--glow-accent); }
.login__box.is-error { border-color: var(--danger); }
/* 保持 opacity: 1 与完整宽高覆盖六格，避免 1Password 等密码管理器跳过隐藏输入框 */
.login__totp-input {
  position: absolute; inset: 0; width: 100%; height: 100%; opacity: 1;
  color: transparent; background: transparent; border: 0; outline: none; caret-color: transparent;
  font-size: 22px; letter-spacing: 0.5em;
}
.login__totp-input::selection { background: transparent; color: transparent; }
.login__error {
  margin: 0; padding: 9px 12px; border-radius: var(--radius-md);
  border: 1px solid var(--danger); background: var(--danger-soft); color: var(--danger);
  font-size: 12px; line-height: 18px; white-space: pre-line;
}
.login__foot {
  margin-top: 24px; padding-top: 14px; border-top: 1px solid var(--border-subtle);
  display: flex; justify-content: space-between; gap: 12px;
  font-size: 11px; line-height: 16px; color: var(--text-tertiary);
}
</style>
