<script setup lang="ts">
/**
 * 行内操作按钮用的线框图标。
 *
 * 原先用字符凑：关机是 ⏸（暂停），复制 SSH 是 ⌘（Mac 的 Command 键），
 * 都不表达本义。项目没有图标库，为四个图标引一个不值得，直接内联 SVG。
 * 描边用 currentColor，颜色跟随按钮。
 */
defineProps<{ name: 'power' | 'restart' | 'terminal' }>()
</script>

<template>
  <svg class="ai" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"
       stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <template v-if="name === 'power'">
      <path d="M8 2v6" />
      <path d="M4.5 4.2a5 5 0 1 0 7 0" />
    </template>
    <template v-else-if="name === 'restart'">
      <path d="M13 8a5 5 0 1 1-1.5-3.6" />
      <path d="M13 2.5v3h-3" />
    </template>
    <template v-else>
      <rect x="1.75" y="2.75" width="12.5" height="10.5" rx="1.5" />
      <path d="M4.5 6.5 6.5 8.25 4.5 10" />
      <path d="M8 10h3.5" />
    </template>
  </svg>
</template>

<style scoped>
.ai { width: 14px; height: 14px; display: block; }
</style>
