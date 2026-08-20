<script setup lang="ts">
/**
 * 跨账号列表的分组标题行。
 *
 * 网络页四张表、存储页两张表都要用同一个样子。抄六遍的结果一定是六份
 * 慢慢跑偏的样式，所以做成一个组件。
 */
import { acctColor } from '@/lib/format'
import type { Account } from '@/types'

defineProps<{ account: Account; count: number; unit: string }>()
</script>

<template>
  <div class="agr">
    <span class="agr__dot" :style="{ background: acctColor(account.colorIndex) }" />
    <span class="mono" :style="{ color: acctColor(account.colorIndex) }">{{ account.code }}</span>
    <span class="dim t-xs">{{ account.alias }}</span>
    <span class="dim-3 t-2xs">{{ count }} {{ unit }}</span>
  </div>
</template>

<style scoped>
.agr {
  display: flex; align-items: center; gap: 8px;
  padding: 8px 16px; background: var(--bg-inset);
  border-bottom: 1px solid var(--border-subtle);
  font-size: 12px; font-weight: 600;
}
.agr__dot { width: 6px; height: 6px; border-radius: var(--radius-full); flex: 0 0 auto; }
</style>
