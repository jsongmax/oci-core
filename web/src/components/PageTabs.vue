<script setup lang="ts">
/** tabs 是标签的稳定 ID；labels 给出按当前语言显示的名字，缺省时直接显示 ID。 */
const props = defineProps<{ tabs: string[]; active: string; labels?: Record<string, string> }>()
const emit = defineEmits<{ 'update:active': [string] }>()
</script>

<template>
  <div class="ptabs" role="tablist">
    <button v-for="t in props.tabs" :key="t" class="pill" role="tab"
            :aria-selected="t === props.active" :class="{ 'is-active': t === props.active }"
            @click="emit('update:active', t)">{{ props.labels?.[t] ?? t }}</button>
  </div>
</template>

<style scoped>
.ptabs { display: flex; gap: 6px; margin-bottom: 16px; flex-wrap: wrap; }
</style>
