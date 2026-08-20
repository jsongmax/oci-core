<script setup lang="ts">
defineProps<{ title: string; note?: string }>()
</script>

<template>
  <section class="card sec">
    <header class="sec__head">
      <h3 class="sec__title">{{ title }}</h3>
      <span v-if="note" class="sec__note">{{ note }}</span>
      <slot name="action" />
    </header>
    <slot />
  </section>
</template>

<style scoped>
.sec { overflow: hidden; }
.sec__head {
  display: flex; align-items: center; gap: 10px; padding: 13px 16px;
  border-bottom: 1px solid var(--border-subtle);
}
.sec__title { margin: 0; font-size: 13px; font-weight: 600; }
.sec__note { flex: 1 1 auto; font-size: 11px; color: var(--text-tertiary); }
</style>
