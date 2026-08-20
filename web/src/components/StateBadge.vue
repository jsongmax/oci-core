<script setup lang="ts">
/** §6.2 StateBadge —— 六态。只有过渡态带动画；文字标签始终存在（§10.2） */
import { computed } from 'vue'
import type { LifecycleState } from '@/types'
import { LIFECYCLE } from '@/lib/lifecycle'

const props = withDefaults(defineProps<{
  state: LifecycleState
  anomaly?: boolean
  /** 刚落定 → ease-spring 弹性回弹 */
  settled?: boolean
  /** 同屏脉冲超限时降级为静态灯（§7 性能红线） */
  staticDot?: boolean
}>(), { anomaly: false, settled: false, staticDot: false })

const meta = computed(() => LIFECYCLE[props.state])
const pulsing = computed(() => meta.value.transitional && !props.staticDot)
</script>

<template>
  <span class="badge" :class="{ 'is-settled': settled }" :style="{ '--c': meta.color }">
    <span class="badge__lamp" :class="{ 'is-solid': meta.solid, 'is-pulsing': pulsing }">
      <span v-if="pulsing" class="badge__ring" />
    </span>
    <span class="badge__label">{{ meta.label }}</span>
    <span v-if="anomaly" class="badge__warn" title="非用户操作导致的状态变化">⚠</span>
  </span>
</template>

<style scoped>
.badge { display: inline-flex; align-items: center; gap: 8px; min-width: 0; }
.badge__lamp {
  position: relative; width: 7px; height: 7px; flex: 0 0 auto;
  border-radius: var(--radius-full); border: 1.5px solid var(--c); background: transparent;
}
.badge__lamp.is-solid { background: var(--c); }
.badge__lamp.is-pulsing { animation: pulse 2s ease-in-out infinite; }
.badge__ring {
  position: absolute; inset: -1.5px; border-radius: var(--radius-full);
  background: var(--c); animation: ring 2s ease-out infinite;
}
.badge__label { font-size: 12px; font-weight: 500; color: var(--c); white-space: nowrap; }
.is-settled .badge__label { animation: pop var(--dur-normal) var(--ease-spring); }
.badge__warn { color: var(--warning); font-size: 11px; }
</style>
