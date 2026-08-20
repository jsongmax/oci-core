<script setup lang="ts">
/** §10.1 空态：图标 + 一句话 + 明确主行动 */
defineProps<{ icon?: string; title: string; sub?: string; actionLabel?: string }>()
const emit = defineEmits<{ action: [] }>()
</script>

<template>
  <div class="empty">
    <div class="empty__icon">{{ icon ?? '▤' }}</div>
    <p class="empty__title">{{ title }}</p>
    <p v-if="sub" class="empty__sub">{{ sub }}</p>
    <button v-if="actionLabel" class="btn" @click="emit('action')">{{ actionLabel }}</button>
  </div>
</template>

<style scoped>
.empty { padding: 56px 20px; display: flex; flex-direction: column; align-items: center; gap: 8px; }
.empty__icon {
  width: 40px; height: 40px; border-radius: var(--radius-md);
  border: 1px solid var(--border-default); display: flex; align-items: center; justify-content: center;
  color: var(--text-tertiary);
}
.empty__title { margin: 0; font-size: 14px; font-weight: 500; }
.empty__sub { margin: 0; font-size: 12px; color: var(--text-secondary); }
.empty .btn { margin-top: 6px; }
</style>
