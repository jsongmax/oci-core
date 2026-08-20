<script setup lang="ts">
/** §10.1 加载态：骨架屏贴合真实内容，禁止用居中转圈代替 */
withDefaults(defineProps<{ rows?: number; height?: number }>(), { rows: 6, height: 56 })
</script>

<template>
  <div>
    <div v-for="n in rows" :key="n" class="row" :style="{ height: height + 'px' }">
      <span class="skeleton" style="width: 84px" />
      <span class="skeleton" style="width: 180px" />
      <span class="skeleton" style="width: 56px" />
      <span class="skeleton" style="width: 130px" />
      <span class="skeleton" style="width: 110px" />
      <span class="skeleton" style="flex: 1 1 auto; max-width: 90px" />
    </div>
  </div>
</template>

<style scoped>
.row {
  display: flex; align-items: center; gap: 16px; padding: 0 16px;
  border-bottom: 1px solid var(--border-subtle);
}
.skeleton { height: 12px; }
</style>
