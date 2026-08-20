<script setup lang="ts">
import { copy } from '@/lib/format'
import { useStore } from '@/store'
import type { KvItem } from '@/types'

const props = defineProps<{ items: KvItem[] }>()
const { toast } = useStore()

async function onCopy(item: KvItem) {
  if (!item.copyable) return
  await copy(item.v)
  toast({ tone: 'accent', title: '已复制到剪贴板', body: item.v })
}
</script>

<template>
  <dl class="kv">
    <template v-for="item in props.items" :key="item.k">
      <dt>{{ item.k }}</dt>
      <dd :class="{ mono: item.mono, 'is-copyable': item.copyable }"
          :style="{ color: item.tone ?? 'var(--text-primary)' }"
          @click="onCopy(item)">{{ item.v }}</dd>
    </template>
  </dl>
</template>

<style scoped>
.kv { margin: 0; padding: 6px 16px 10px; display: grid; grid-template-columns: 116px 1fr; }
dt, dd {
  margin: 0; padding: 7px 0; border-bottom: 1px solid var(--border-subtle);
  font-size: 12px; align-self: baseline; word-break: break-all;
}
dt { color: var(--text-secondary); }
dd.is-copyable { cursor: copy; }
dd.is-copyable:hover { color: var(--accent); }
</style>
