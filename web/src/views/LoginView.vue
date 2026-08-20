<script setup lang="ts">
/**
 * §4.1 登录：两步式，TOTP 六格自动跳格 + 粘贴分配 + 填满自动提交。
 *
 * 同一张卡片承载四个阶段：首次设置 → 登录 → 两步验证 → 绑定验证器。
 * 做成一个视图而不是四个路由，是因为它们共用同一套外壳与动画，
 * 拆开只会让"设置完自动进入绑定"这类衔接变得别扭。
 */
import { computed, nextTick, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useStore } from '@/store'
import { auth, ApiError, errorText } from '@/api'
import { copy } from '@/lib/format'

type Mode = 'setup' | 'login' | 'totp' | 'enroll'

const router = useRouter()
const { state, afterLogin, toast } = useStore()

const mode = ref<Mode>('login')
const username = ref('admin')
const password = ref('')
const passwordConfirm = ref('')
const digits = ref<string[]>(['', '', '', '', '', ''])
const boxes = ref<HTMLInputElement[]>([])
const error = ref('')
const busy = ref(false)
const shakeKey = ref(0)

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
  if (mode.value === 'totp') boxes.value[0]?.focus()
})

function fail(message: string) {
  error.value = message
  shakeKey.value++
}

function resetDigits() {
  digits.value = ['', '', '', '', '', '']
  nextTick(() => boxes.value[0]?.focus())
}

/* ---------- 首次设置 ---------- */

async function doSetup() {
  error.value = ''
  if (!username.value.trim()) { fail('请填写用户名'); return }
  if (password.value.length < 10) { fail('口令至少需要 10 个字符'); return }
  if (password.value !== passwordConfirm.value) { fail('两次输入的口令不一致'); return }

  busy.value = true
  try {
    await auth.setup(username.value.trim(), password.value)
    // 创建完账户直接登录，再引导绑定验证器——
    // 后端的 nextStep 明确要求走这一步。
    await auth.login(username.value.trim(), password.value)
    const setup = await auth.totpSetup()
    totpSecret.value = setup.secret
    totpUri.value = setup.uri
    mode.value = 'enroll'
    passwordConfirm.value = ''
    await nextTick()
    boxes.value[0]?.focus()
  } catch (err) {
    fail(errorText(err))
  } finally {
    busy.value = false
  }
}

/* ---------- 登录 ---------- */

async function doLogin() {
  error.value = ''
  if (!username.value || !password.value) { fail('用户名或口令不能为空'); return }

  busy.value = true
  try {
    const result = await auth.login(username.value.trim(), password.value)
    password.value = ''
    if (result.totpRequired) {
      mode.value = 'totp'
      await nextTick()
      boxes.value[0]?.focus()
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
  const code = digits.value.join('')
  if (code.length !== 6 || busy.value) return

  busy.value = true
  error.value = ''
  try {
    if (mode.value === 'enroll') {
      await auth.totpEnable(code)
      toast({ tone: 'success', title: '两步验证已启用' })
    } else {
      await auth.verifyTotp(code)
    }
    await enter()
  } catch (err) {
    const hint = err instanceof ApiError && err.code === 'code_reused'
      ? '该验证码已被使用，请等待验证器刷新出下一个'
      : errorText(err)
    fail(hint)
    resetDigits()
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
    title: '已跳过两步验证',
    body: '强烈建议稍后在「设置 → 账户安全」中完成绑定。'
  }, 9000)
  await enter()
}

/* ---------- 六格验证码输入 ---------- */

function onDigit(i: number, e: Event) {
  const el = e.target as HTMLInputElement
  const v = el.value.replace(/\D/g, '')
  if (v.length > 1) { onPaste(v, i); return }
  digits.value[i] = v
  error.value = ''
  if (v && i < 5) boxes.value[i + 1]?.focus()
  if (digits.value.every(d => d)) submitCode()
}

function onPaste(text: string, from = 0) {
  const chars = text.replace(/\D/g, '').slice(0, 6 - from).split('')
  chars.forEach((c, n) => { digits.value[from + n] = c })
  const next = Math.min(5, from + chars.length)
  boxes.value[next]?.focus()
  if (digits.value.every(d => d)) submitCode()
}

function onBackspace(i: number) {
  if (!digits.value[i] && i > 0) boxes.value[i - 1]?.focus()
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
        <p class="login__step-title">首次设置</p>
        <p class="login__step-sub">创建管理员账户。口令至少 10 个字符。</p>
        <div class="field">
          <label for="u">用户名</label>
          <input id="u" v-model="username" class="input mono" autocomplete="username" />
        </div>
        <div class="field">
          <label for="p">口令</label>
          <input id="p" v-model="password" type="password" class="input" autocomplete="new-password" />
        </div>
        <div class="field">
          <label for="p2">确认口令</label>
          <input id="p2" v-model="passwordConfirm" type="password" class="input" autocomplete="new-password" />
        </div>
        <p v-if="error" class="login__error" role="alert">{{ error }}</p>
        <button class="btn btn--primary login__submit" type="submit" :disabled="busy">
          {{ busy ? '创建中…' : '创建账户' }}
        </button>
      </form>

      <!-- 登录 -->
      <form v-else-if="mode === 'login'" class="login__form" @submit.prevent="doLogin">
        <div class="field">
          <label for="u2">用户名</label>
          <input id="u2" v-model="username" class="input mono" autocomplete="username" />
        </div>
        <div class="field">
          <label for="p3">口令</label>
          <input id="p3" v-model="password" type="password" class="input" autocomplete="current-password" />
        </div>
        <p v-if="error" class="login__error" role="alert">{{ error }}</p>
        <button class="btn btn--primary login__submit" type="submit" :disabled="busy">
          {{ busy ? '登录中…' : '登录' }}
        </button>
      </form>

      <!-- 绑定验证器 -->
      <div v-else-if="mode === 'enroll'" class="login__form">
        <p class="login__step-title">绑定两步验证</p>
        <p class="login__step-sub">
          在验证器应用中选择「手动输入密钥」，录入下方密钥后填写验证码。
        </p>

        <div class="login__secret">
          <code class="login__secret-value mono">{{ groupedSecret }}</code>
          <button class="btn login__copy" type="button" @click="copy(totpSecret)">复制</button>
        </div>
        <a class="login__uri mono" :href="totpUri">在本机验证器中打开</a>

        <div class="login__totp" @paste.prevent="onPaste(($event.clipboardData?.getData('text') ?? ''))">
          <input v-for="(d, i) in digits" :key="i" ref="boxes" :value="d" class="login__box mono"
                 inputmode="numeric" maxlength="1" :aria-label="`第 ${i + 1} 位`"
                 :class="{ 'is-error': error }"
                 @input="onDigit(i, $event)" @keydown.backspace="onBackspace(i)" />
        </div>
        <p v-if="error" class="login__error" role="alert">{{ error }}</p>
        <button class="btn login__skip" type="button" @click="skipEnroll">稍后再绑定</button>
      </div>

      <!-- 两步验证 -->
      <div v-else class="login__form">
        <p class="login__step-title">输入两步验证码</p>
        <p class="login__step-sub">来自验证器应用的 6 位数字，填满自动提交</p>
        <div class="login__totp" @paste.prevent="onPaste(($event.clipboardData?.getData('text') ?? ''))">
          <input v-for="(d, i) in digits" :key="i" ref="boxes" :value="d" class="login__box mono"
                 inputmode="numeric" maxlength="1" :aria-label="`第 ${i + 1} 位`"
                 :class="{ 'is-error': error }"
                 @input="onDigit(i, $event)" @keydown.backspace="onBackspace(i)" />
        </div>
        <p v-if="error" class="login__error" role="alert">{{ error }}</p>
      </div>

      <footer class="login__foot">
        <!-- 这里原来写着"本面板持有你的 OCI 密钥"。登录页对未认证的访客
             是公开可达的，等于主动告诉每个扫到它的人这台机器上有什么值得偷，
             属于白送的信息。该提醒的对象是部署者，位置应该在 README 与
             服务端启动日志里，不是登录页。 -->
        <span class="mono">{{ state.version || 'dev' }}</span>
      </footer>
    </div>
  </div>
</template>

<style scoped>
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
.login__uri { margin-top: -6px; font-size: 11px; color: var(--accent); }
.login__totp { display: flex; gap: 8px; }
.login__box {
  flex: 1 1 auto; width: 100%; height: 52px; text-align: center;
  border-radius: var(--radius-md); border: 1px solid var(--border-default); background: var(--bg-inset);
  color: var(--text-primary); font-size: 22px; font-weight: 600; outline: none;
}
.login__box:focus { border-color: var(--accent); box-shadow: var(--glow-accent); }
.login__box.is-error { border-color: var(--danger); }
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
