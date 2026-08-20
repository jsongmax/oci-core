<script setup lang="ts">
/** §6.1 AccountChip —— 全站最高频组件。身份色永不单独出现，始终带短代号 */
import { computed } from 'vue'
import type { Account } from '@/types'
import { acctColor } from '@/lib/format'
import { accountStatusText } from '@/lib/adapt'

const props = withDefaults(defineProps<{
  account: Account
  variant?: 'dot' | 'chip' | 'full'
}>(), { variant: 'chip' })

const color = computed(() => acctColor(props.account.colorIndex))
const abnormal = computed(() => props.account.status === 'error')
const tooltip = computed(() =>
  `${props.account.alias} · ocid1…${props.account.tenancyTail} · ${accountStatusText(props.account)}`
)
</script>

<template>
  <span class="chip" :class="`chip--${variant}`" :title="tooltip" :style="{ '--c': color }">
    <span class="chip__dot" />
    <span v-if="variant !== 'dot'" class="chip__code mono">{{ account.code }}</span>
    <span v-if="abnormal" class="chip__warn" aria-label="账号异常">⚠</span>
    <span v-if="variant === 'full'" class="chip__alias">{{ account.alias }}</span>
  </span>
</template>

<style scoped>
.chip {
  display: inline-flex; align-items: center; gap: 5px;
  padding: 2px 8px 2px 6px; border-radius: var(--radius-full); background: var(--bg-inset);
}
.chip--dot { padding: 0; background: none; }
.chip__dot { width: 6px; height: 6px; border-radius: var(--radius-full); background: var(--c); flex: 0 0 auto; }
.chip__code { font-size: 11px; line-height: 16px; font-weight: 600; color: var(--c); }
.chip__warn { color: var(--danger); font-size: 11px; }
.chip__alias { font-size: 12px; color: var(--text-primary); }
</style>
