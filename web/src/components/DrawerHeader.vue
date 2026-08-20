<script setup lang="ts">
defineProps<{ color: string; code: string; title: string; sub: string }>()
const emit = defineEmits<{ close: [] }>()
</script>

<template>
  <header class="dh">
    <span class="dh__bar" :style="{ background: color }" />
    <div class="dh__body">
      <div class="dh__row">
        <span class="dh__code mono" :style="{ color }">{{ code }}</span>
        <h2 class="dh__title">{{ title }}</h2>
        <slot name="badge" />
        <button class="dh__close" aria-label="关闭" @click="emit('close')">✕</button>
      </div>
      <p class="dh__sub mono">{{ sub }}</p>
      <div class="dh__actions"><slot name="actions" /></div>
    </div>
  </header>
</template>

<style scoped>
.dh { flex: 0 0 auto; display: flex; border-bottom: 1px solid var(--border-subtle); }
.dh__bar { width: 4px; flex: 0 0 auto; }
.dh__body { flex: 1 1 auto; min-width: 0; padding: 18px 20px; }
.dh__row { display: flex; align-items: center; gap: 10px; }
.dh__code { font-size: 11px; font-weight: 600; padding: 2px 7px; border-radius: var(--radius-full); background: var(--bg-inset); }
.dh__title { margin: 0; font-size: 20px; line-height: 28px; font-weight: 600; flex: 1 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dh__close { border: 0; background: none; color: var(--text-secondary); cursor: pointer; font-size: 14px; }
.dh__sub { margin: 6px 0 0; font-size: 11px; color: var(--text-tertiary); }
.dh__actions { margin-top: 14px; display: flex; gap: 8px; flex-wrap: wrap; }
.dh__actions:empty { display: none; }
</style>
