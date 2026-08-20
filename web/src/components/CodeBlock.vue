<script setup lang="ts">
import { copy } from '@/lib/format'
import { useStore } from '@/store'

const props = defineProps<{ code: string; copyable?: boolean }>()
const { toast } = useStore()
</script>

<template>
  <pre class="code mono" :class="{ 'is-copyable': copyable }"
       @click="copyable && copy(props.code).then(() => toast({ tone: 'accent', title: '已复制' }))">{{ code }}</pre>
</template>

<style scoped>
.code {
  margin: 14px 16px; padding: 12px; border-radius: var(--radius-md);
  background: var(--bg-inset); border: 1px solid var(--border-subtle);
  font-size: 11px; line-height: 18px; color: var(--text-secondary);
  white-space: pre-wrap; word-break: break-all;
}
.is-copyable { cursor: copy; }
.is-copyable:hover { border-color: var(--border-strong); }
</style>
