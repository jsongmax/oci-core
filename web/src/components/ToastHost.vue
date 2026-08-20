<script setup lang="ts">
import { computed, ref } from 'vue'
import { useStore } from '@/store'
import { copy } from '@/lib/format'

const { state, dismissToast } = useStore()
const copied = ref(false)
const tone = computed(() => {
  const map = {
    success: 'var(--success)', warning: 'var(--warning)', danger: 'var(--danger)',
    info: 'var(--info)', accent: 'var(--accent)'
  }
  return state.toast ? map[state.toast.tone] : 'var(--accent)'
})

async function onCopy() {
  if (!state.toast?.command) return
  await copy(state.toast.command)
  copied.value = true
}
</script>

<template>
  <Transition name="toast">
    <div v-if="state.toast" class="toast glass" :style="{ '--c': tone }" role="status" aria-live="polite">
      <div class="toast__head">
        <span class="toast__dot" />
        <strong class="toast__title">{{ state.toast.title }}</strong>
        <button class="toast__close" aria-label="关闭" @click="dismissToast()">✕</button>
      </div>
      <p v-if="state.toast.body" class="toast__body">{{ state.toast.body }}</p>
      <button v-if="state.toast.command" class="toast__cmd" @click="onCopy">
        <span class="mono">{{ state.toast.command }}</span>
        <span class="toast__cmd-action">{{ copied ? '已复制' : '复制' }}</span>
      </button>
    </div>
  </Transition>
</template>

<style scoped>
.toast {
  position: fixed; right: 24px; bottom: 24px; z-index: 56; width: 356px; padding: 14px 16px;
  border-radius: var(--radius-lg); border-color: var(--c); box-shadow: var(--shadow-4);
}

/*
 * 用 transition 而不是 animation 做进出场。
 *
 * 之前 .toast 上挂了一个无条件的 `animation: rise`，而它的 scoped 选择器
 * （.toast[data-v-x]）特异性高于公共的 .scrim-leave-active，于是离场时元素的
 * animation-name 仍然是 rise —— 那是个早就播完的入场动画，animationend 永不
 * 再触发，Vue 就一直等着，元素永远留在 DOM 里。看上去像"toast 不会自动消失"。
 *
 * transition 没有这个坑：属性值一变就必然产生 transitionend。
 */
.toast-enter-active,
.toast-leave-active {
  transition: opacity var(--dur-normal) var(--ease-decelerate),
              transform var(--dur-normal) var(--ease-decelerate);
}
.toast-enter-from,
.toast-leave-to {
  opacity: 0;
  transform: translateY(8px);
}
.toast__head { display: flex; align-items: center; gap: 9px; }
.toast__dot { width: 7px; height: 7px; border-radius: var(--radius-full); background: var(--c); }
.toast__title { flex: 1 1 auto; font-size: 13px; font-weight: 600; }
.toast__close { border: 0; background: none; color: var(--text-tertiary); cursor: pointer; font-size: 12px; }
.toast__body { margin: 6px 0 0; font-size: 12px; color: var(--text-secondary); }
.toast__cmd {
  margin-top: 10px; width: 100%; display: flex; align-items: center; gap: 8px; padding: 8px 10px;
  border-radius: var(--radius-sm); border: 1px solid var(--border-subtle);
  background: var(--bg-inset); color: var(--text-primary); cursor: pointer;
}
.toast__cmd:hover { border-color: var(--border-strong); }
.toast__cmd > span:first-child { flex: 1 1 auto; font-size: 11px; text-align: left; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.toast__cmd-action { font-size: 11px; color: var(--accent); }
</style>
