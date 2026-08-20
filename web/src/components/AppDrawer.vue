<script setup lang="ts">
/** §5.5 抽屉宽度：窄 520 / 宽 720；1024–1279 收至 480（§9） */
import { onMounted, onUnmounted, ref } from 'vue'

const props = withDefaults(defineProps<{ width?: 'narrow' | 'wide' }>(), { width: 'wide' })
const emit = defineEmits<{ close: [] }>()
const root = ref<HTMLElement | null>(null)

/** 焦点陷阱 + Esc 关闭（§10.2） */
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') { emit('close'); return }
  if (e.key !== 'Tab' || !root.value) return
  const nodes = root.value.querySelectorAll<HTMLElement>(
    'a[href], button:not([disabled]), input, textarea, select, [tabindex]:not([tabindex="-1"])'
  )
  if (!nodes.length) return
  const first = nodes[0], last = nodes[nodes.length - 1]
  if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus() }
  if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus() }
}
onMounted(() => window.addEventListener('keydown', onKey))
onUnmounted(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <Transition name="scrim" appear>
    <div class="drawer__scrim" @click="emit('close')" />
  </Transition>
  <Transition name="drawer" appear>
    <aside ref="root" class="drawer" :class="`drawer--${props.width}`" role="dialog" aria-modal="true">
      <slot />
    </aside>
  </Transition>
</template>

<style scoped>
.drawer__scrim { position: fixed; inset: 0; z-index: 46; background: var(--scrim); }
.drawer {
  position: fixed; z-index: 47; top: 0; right: 0; bottom: 0; width: var(--drawer-wide);
  display: flex; flex-direction: column;
  background: var(--bg-elevated); border-left: 1px solid var(--glass-border); box-shadow: var(--shadow-4);
}
.drawer--narrow { width: var(--drawer-narrow); }
@media (max-width: 1279px) {
  .drawer, .drawer--narrow { width: 480px; }
}
@media (max-width: 767px) {
  .drawer, .drawer--narrow { width: 100%; }
}
</style>
