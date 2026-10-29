<script setup lang="ts">
/** §6.7 QuotaMeter —— ≥90% warning，=100% danger 并标注已满 */
import { computed } from 'vue'
import { pct, usageTone } from '@/lib/format'

const props = defineProps<{
  label: string
  used: number
  limit: number
  unit?: string
  /** 没有实际上限。进度条会被画成永远 0%，不如不画 */
  unlimited?: boolean
}>()

const tone = computed(() => usageTone(props.used, props.limit))
const width = computed(() => pct(props.used, props.limit) + '%')
const full = computed(() => props.limit > 0 && props.used === props.limit)
/**
 * 超出多少。「已满」只适合恰好用满；用量超过上限时（升级号超出免费额度、
 * 创建表单里预估的用量）写「已满」会让人以为还差一点，实际已经超了。
 */
const over = computed(() =>
  props.limit > 0 && props.used > props.limit ? Math.round((props.used - props.limit) * 10) / 10 : 0)
</script>

<template>
  <div class="meter" :style="{ '--c': tone }">
    <span class="meter__label">{{ label }}</span>
    <template v-if="unlimited">
      <!-- 上限是 Oracle 的哨兵值（一亿），画成进度条永远贴着 0%，
           显示出来也只是一长串数字。直接说"不限"。 -->
      <span class="meter__track meter__track--none" />
      <span class="meter__value mono">{{ used }}{{ unit ?? '' }} · 不限</span>
    </template>
    <template v-else>
      <span class="meter__track"><span class="meter__fill" :style="{ width }" /></span>
      <span class="meter__value mono">{{ used }} / {{ limit }}{{ unit ?? '' }}<template v-if="over"> 超出 {{ over }}</template><template v-else-if="full"> 已满</template></span>
    </template>
  </div>
</template>

<style scoped>
.meter { display: grid; grid-template-columns: 80px 1fr 108px; gap: 10px; align-items: center; }
.meter__label { font-size: 11px; color: var(--text-secondary); }
.meter__track--none { background: transparent; border-top: 1px dashed var(--border-default); height: 0; }
.meter__track { height: 6px; border-radius: var(--radius-full); background: var(--bg-inset); overflow: hidden; }
.meter__fill {
  display: block; height: 6px; border-radius: var(--radius-full);
  background: var(--c); transition: width var(--dur-slow) var(--ease-standard);
}
.meter__value { font-size: 11px; color: var(--c); text-align: right; }
</style>
