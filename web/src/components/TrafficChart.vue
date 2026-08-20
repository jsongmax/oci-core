<script setup lang="ts">
/** §7 Tier 2.13 图表渐变流动：面积渐变 + 描边高光 */
import { computed, useId } from 'vue'

const props = withDefaults(defineProps<{
  /** 真实数据点。为空时显示空态而不是画一条假曲线 */
  values?: number[]
  /** 坐标轴两端的时间标签 */
  labels?: [string, string]
  unit?: string
  /** 数据为空时的说明文字 */
  emptyText?: string
}>(), {
  values: () => [],
  labels: () => ['', ''],
  unit: '',
  emptyText: '暂无监控数据'
})

/** 渐变 id 必须全局唯一，同页面多张图会互相覆盖。 */
const gradientId = useId()

const hasData = computed(() => props.values.length >= 2)

/** 归一化到 0–100，留 8% 上边距免得峰值贴顶。 */
const normalized = computed(() => {
  const max = Math.max(...props.values, 0)
  if (max <= 0) return props.values.map(() => 0)
  return props.values.map(v => (v / max) * 92)
})

const peak = computed(() => Math.max(...props.values, 0))

const path = computed(() => {
  const points = normalized.value
  if (points.length < 2) return ''
  const w = 100 / (points.length - 1)
  return points
    .map((v, n) => `${n === 0 ? 'M' : 'L'}${(n * w).toFixed(2)},${(100 - v).toFixed(2)}`)
    .join(' ')
})

const area = computed(() => (path.value ? `${path.value} L100,100 L0,100 Z` : ''))
</script>

<template>
  <div class="chart">
    <template v-if="hasData">
      <svg viewBox="0 0 100 100" preserveAspectRatio="none" role="img"
           :aria-label="`趋势图，峰值 ${peak}${unit}`">
        <defs>
          <linearGradient :id="gradientId" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="var(--accent)" stop-opacity="0.35" />
            <stop offset="100%" stop-color="var(--accent)" stop-opacity="0" />
          </linearGradient>
        </defs>
        <path :d="area" :fill="`url(#${gradientId})`" />
        <path :d="path" fill="none" stroke="var(--accent)" stroke-width="0.8"
              vector-effect="non-scaling-stroke" />
      </svg>
      <div class="chart__axis mono">
        <span>{{ labels[0] }}</span><span>{{ labels[1] }}</span>
      </div>
    </template>

    <p v-else class="chart__empty">{{ emptyText }}</p>
  </div>
</template>

<style scoped>
.chart { padding: 16px; }
svg { width: 100%; height: 120px; display: block; }
.chart__axis { margin-top: 8px; display: flex; justify-content: space-between; font-size: 11px; color: var(--text-tertiary); }
.chart__empty {
  margin: 0; height: 120px; display: flex; align-items: center; justify-content: center;
  font-size: 12px; color: var(--text-tertiary);
}
</style>
