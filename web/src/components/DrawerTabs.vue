<script setup lang="ts">
/** tabs 是标签的稳定 ID；labels 给出按当前语言显示的名字，缺省时直接显示 ID。 */
const props = defineProps<{ tabs: string[]; active: string; labels?: Record<string, string> }>()
const emit = defineEmits<{ 'update:active': [string] }>()
</script>

<template>
  <div class="tabs" role="tablist">
    <button v-for="t in props.tabs" :key="t" class="tabs__tab" role="tab"
            :aria-selected="t === props.active" :class="{ 'is-active': t === props.active }"
            @click="emit('update:active', t)">{{ props.labels?.[t] ?? t }}</button>
  </div>
</template>

<style scoped>
.tabs {
  flex: 0 0 auto; display: flex; gap: 2px; padding: 0 20px;
  border-bottom: 1px solid var(--border-subtle); background: var(--bg-surface); overflow-x: auto;
}
.tabs__tab {
  height: 40px; padding: 0 12px; border: 0; border-bottom: 2px solid transparent;
  background: none; color: var(--text-secondary); font-size: 13px; cursor: pointer; white-space: nowrap;
}
.tabs__tab:hover { color: var(--text-primary); }
.tabs__tab.is-active { color: var(--text-primary); font-weight: 600; border-bottom-color: var(--accent); }
</style>
