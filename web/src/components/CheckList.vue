<script setup lang="ts">
import type { CheckItem } from '@/types'

const props = defineProps<{ items: CheckItem[] }>()
const icon = { ok: '✔', warn: '⚠', fail: '✕', info: '·' }
const color = {
  ok: 'var(--success)', warn: 'var(--warning)', fail: 'var(--danger)', info: 'var(--text-tertiary)'
}
</script>

<template>
  <ul class="checks">
    <li v-for="(item, n) in props.items" :key="n" :style="{ '--c': color[item.tone] }">
      <span class="checks__icon">{{ icon[item.tone] }}</span>
      <span class="checks__text" :class="{ 'is-strong': item.tone !== 'ok' && item.tone !== 'info' }">{{ item.text }}</span>
    </li>
  </ul>
</template>

<style scoped>
.checks { margin: 0; padding: 12px 16px; list-style: none; display: flex; flex-direction: column; gap: 8px; }
li { display: flex; align-items: flex-start; gap: 9px; font-size: 12px; }
.checks__icon { flex: 0 0 auto; color: var(--c); }
.checks__text { color: var(--text-secondary); }
.checks__text.is-strong { color: var(--c); }
</style>
