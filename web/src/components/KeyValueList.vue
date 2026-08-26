<script setup lang="ts">
import { copy } from '@/lib/format'
import { useStore } from '@/store'
import type { KvItem } from '@/types'
import MaskedText from '@/components/MaskedText.vue'

const props = defineProps<{ items: KvItem[] }>()
const { toast } = useStore()

/**
 * 复制的永远是**原文**，不是打过码的显示值。
 *
 * 详情抽屉里的 OCID、IP 复制出来就是要拿去用的。给一串星号等于把
 * 隐私功能变成了故意添堵。
 */
async function onCopy(item: KvItem) {
  if (!item.copyable) return
  await copy(item.v)
  toast({ tone: 'accent', title: '已复制到剪贴板' })
}
</script>

<template>
  <dl class="kv">
    <template v-for="item in props.items" :key="item.k">
      <dt>{{ item.k }}</dt>
      <!-- 带 secret 的走 MaskedText：它自己管打码与展开，
           复制也由它处理，所以这里不再挂 click -->
      <dd v-if="item.secret" :class="{ mono: item.mono }"
          :style="{ color: item.tone ?? 'var(--text-primary)' }">
        <MaskedText :value="item.v" :kind="item.secret" :copyable="item.copyable" />
      </dd>
      <dd v-else :class="{ mono: item.mono, 'is-copyable': item.copyable }"
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
