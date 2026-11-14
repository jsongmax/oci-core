<script setup lang="ts">
/**
 * 危险操作复核：设置里打开「危险操作需重新验证 TOTP」后，后端对 L2 / L3
 * 接口要求一次验证码。HTTP 层收到要求时经 store 打开这里，输完自动重发原请求。
 */
import { t } from '@/i18n'
import { computed, nextTick, ref, watch } from 'vue'
import { useStore } from '@/store'

const { state } = useStore()
const code = ref('')
const input = ref<HTMLInputElement | null>(null)

const req = computed(() => state.dangerPrompt)
const valid = computed(() => /^\d{6}$/.test(code.value))

watch(req, async (r) => {
  code.value = ''
  if (r) {
    await nextTick()
    input.value?.focus()
  }
})

function cancel() { req.value?.resolve(null) }
function submit() {
  if (!req.value || !valid.value) return
  req.value.resolve(code.value)
}
</script>

<template>
  <Transition name="scrim">
    <div v-if="req" class="scrim" role="dialog" aria-modal="true" aria-labelledby="danger-totp-title"
         @keydown.esc="cancel">
      <form class="dlg" @submit.prevent="submit">
        <h2 id="danger-totp-title" class="dlg__title">{{ t.ui.totpTitle }}</h2>
        <p class="dlg__body">{{ t.ui.totpBody(req.message) }}</p>
        <label class="dlg__label" for="danger-totp-code">{{ t.ui.totpCode }}</label>
        <input id="danger-totp-code" ref="input" v-model="code" class="input mono dlg__input"
               maxlength="6" inputmode="numeric" autocomplete="one-time-code" :placeholder="t.ui.totpPlaceholder" />
        <footer class="dlg__foot">
          <button type="button" class="btn" @click="cancel">{{ t.common.cancel }}</button>
          <button type="submit" class="btn btn--primary" :disabled="!valid">{{ t.ui.confirm }}</button>
        </footer>
      </form>
    </div>
  </Transition>
</template>

<style scoped>
.scrim {
  position: fixed; inset: 0; z-index: 60; display: flex; align-items: center; justify-content: center;
  background: var(--scrim); backdrop-filter: blur(3px);
}
.dlg {
  width: 400px; max-width: calc(100vw - 32px); padding: 24px; border-radius: var(--radius-xl);
  border: 1px solid var(--border-default); background: var(--bg-elevated);
  box-shadow: var(--shadow-4); animation: pop var(--dur-normal) var(--ease-spring);
}
.dlg__title { margin: 0; font-size: 20px; line-height: 28px; font-weight: 600; }
.dlg__body { margin: 10px 0 0; font-size: 13px; color: var(--text-secondary); }
.dlg__label { display: block; margin-top: 14px; font-size: 12px; color: var(--text-secondary); }
.dlg__input { width: 100%; margin-top: 6px; height: 38px; letter-spacing: 0.2em; }
.dlg__foot { margin-top: 20px; display: flex; justify-content: flex-end; gap: 10px; }
</style>
