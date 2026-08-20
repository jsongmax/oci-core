<script setup lang="ts">
const props = defineProps<{ tabs: string[]; active: string }>()
const emit = defineEmits<{ 'update:active': [string] }>()
</script>

<template>
  <div class="ptabs" role="tablist">
    <button v-for="t in props.tabs" :key="t" class="pill" role="tab"
            :aria-selected="t === props.active" :class="{ 'is-active': t === props.active }"
            @click="emit('update:active', t)">{{ t }}</button>
  </div>
</template>

<style scoped>
.ptabs { display: flex; gap: 6px; margin-bottom: 16px; flex-wrap: wrap; }
</style>
