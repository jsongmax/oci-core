<script setup lang="ts">
/**
 * 按天的柱状图。
 *
 * 柱子用 flex 均分，7 到 90 根都不用改结构。柱高按峰值归一化，留 8% 上边距；
 * 有值但极小的柱子保底 1.5%，免得「这天用了一点」和「这天是零」看起来一样。
 */
import { computed } from 'vue'

const props = withDefaults(defineProps<{
  /** date 为 YYYY-MM-DD */
  items: { date: string; value: number }[]
  /** 悬停提示里的数值格式 */
  format?: (v: number) => string
  ariaLabel?: string
  height?: number
}>(), {
  format: (v: number) => String(v),
  ariaLabel: '',
  height: 96
})

const peak = computed(() => props.items.reduce((m, d) => Math.max(m, d.value), 0))

function barHeight(v: number): number {
  if (!v || peak.value <= 0) return 0
  return Math.max(1.5, (v / peak.value) * 92)
}
</script>

<template>
  <div>
    <div class="bars" role="img" :aria-label="ariaLabel" :style="{ height: height + 'px' }">
      <span v-for="d in items" :key="d.date" class="bar"
            :style="{ height: barHeight(d.value) + '%' }"
            :title="`${d.date} · ${format(d.value)}`" />
    </div>
    <div class="axis mono t-2xs dim-3">
      <span>{{ items[0]?.date.slice(5) }}</span>
      <span>{{ items[items.length - 1]?.date.slice(5) }}</span>
    </div>
  </div>
</template>

<style scoped>
.bars {
  display: flex; align-items: flex-end; gap: 2px;
  padding-bottom: 1px; border-bottom: 1px solid var(--border-subtle);
}
.bar {
  flex: 1 1 0; min-width: 2px; border-radius: 1px 1px 0 0;
  background: var(--accent); opacity: 0.75;
  transition: opacity var(--dur-fast);
}
.bar:hover { opacity: 1; }
.axis { display: flex; justify-content: space-between; margin-top: 8px; }
</style>
