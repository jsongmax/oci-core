<script setup lang="ts">
/**
 * 监控图表（ECharts）。
 *
 * 原先的 TrafficChart 只有一条无刻度的曲线，看不出量级，也读不出某个时刻的值。
 * 这里补齐读图必需的部分：带单位的纵轴与网格、时间轴、悬停十字线（一次列出
 * 所有序列在该时刻的值）、多序列时的图例。
 *
 * 配色取 dataviz 参考调色板的前两个分类色（蓝、橙），亮暗主题各用一档，
 * 两色在色觉异常下也分得开。颜色只随序列身份走：出站永远是蓝，入站永远是橙。
 *
 * ECharts 按需引入，体积只算折线、柱状和用到的组件。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts/core'
import { LineChart, BarChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { EChartsCoreOption } from 'echarts/core'

echarts.use([LineChart, BarChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer])

export interface MetricSeries {
  name: string
  /** 时间轴：[毫秒时间戳, 值]；类目轴：与 categories 等长的值 */
  data: [number, number][] | number[]
}

const props = withDefaults(defineProps<{
  series: MetricSeries[]
  type?: 'line' | 'bar'
  /** 类目轴的标签；给了就用类目轴，否则用时间轴 */
  categories?: string[]
  /** 纵轴与悬停读数的格式化 */
  format?: (v: number) => string
  /** 纵轴固定上限，百分比类指标传 100 */
  yMax?: number
  height?: number
  emptyText?: string
}>(), {
  type: 'line',
  categories: undefined,
  format: (v: number) => v.toLocaleString('zh-CN', { maximumFractionDigits: 2 }),
  yMax: undefined,
  height: 200,
  emptyText: '暂无数据'
})

/** 分类色：dataviz 参考调色板 slot 1（蓝）、slot 2（橙）。 */
const SERIES_COLORS = {
  light: ['#2a78d6', '#eb6834'],
  dark: ['#3987e5', '#d95926']
}

const hasData = computed(() => props.series.some(s => s.data.length > 0))

const el = ref<HTMLDivElement>()
let chart: echarts.ECharts | null = null
let resizeObs: ResizeObserver | null = null
let themeObs: MutationObserver | null = null

function isDark(): boolean {
  const t = document.documentElement.getAttribute('data-theme')
  if (t) return t === 'dark'
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false
}

function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

function timeLabel(ms: number, withDate: boolean): string {
  const d = new Date(ms)
  const hm = d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false })
  if (!withDate) return hm
  return `${d.getMonth() + 1}/${d.getDate()} ${hm}`
}

function buildOption(): EChartsCoreOption {
  const dark = isDark()
  const colors = dark ? SERIES_COLORS.dark : SERIES_COLORS.light
  const textSecondary = cssVar('--text-secondary')
  const textTertiary = cssVar('--text-tertiary')
  const textPrimary = cssVar('--text-primary')
  const border = cssVar('--border-subtle')
  const borderDefault = cssVar('--border-default')
  const surface = cssVar('--bg-elevated')
  const mono = cssVar('--font-mono')
  const fmt = props.format

  const isCategory = !!props.categories
  // 跨度超过一天时，时间轴标签带上日期，否则 00:00 之后的点看不出是哪天。
  let spanMs = 0
  if (!isCategory) {
    const ts = props.series.flatMap(s => (s.data as [number, number][]).map(p => p[0]))
    if (ts.length) spanMs = Math.max(...ts) - Math.min(...ts)
  }
  const withDate = spanMs > 20 * 3600 * 1000
  const multi = props.series.length > 1

  return {
    animation: false,
    color: colors,
    textStyle: { fontFamily: cssVar('--font-ui') },
    grid: { left: 8, right: 16, top: multi ? 36 : 16, bottom: 8, containLabel: true },
    legend: multi
      ? {
          top: 0, left: 0, icon: 'roundRect', itemWidth: 12, itemHeight: 4,
          textStyle: { color: textSecondary, fontSize: 12 }
        }
      : undefined,
    tooltip: {
      trigger: 'axis',
      backgroundColor: surface,
      borderColor: borderDefault,
      borderWidth: 1,
      padding: [8, 10],
      textStyle: { color: textPrimary, fontSize: 12 },
      axisPointer: {
        type: props.type === 'bar' ? 'shadow' : 'line',
        lineStyle: { color: textTertiary, type: 'dashed' },
        shadowStyle: { color: dark ? 'rgba(255,255,255,0.04)' : 'rgba(0,0,0,0.04)' }
      },
      formatter: (params: any) => {
        const list = Array.isArray(params) ? params : [params]
        if (!list.length) return ''
        const head = isCategory
          ? String(list[0].axisValueLabel ?? list[0].name)
          : timeLabel(Number(list[0].axisValue), true)
        // 值在前、标签在后；文字用正文色，只有小色块带序列身份。
        const rows = list.map((p: any) => {
          const v = Array.isArray(p.value) ? p.value[1] : p.value
          return `<div style="display:flex;align-items:center;gap:8px;margin-top:4px">` +
            `<span style="width:8px;height:8px;border-radius:2px;background:${p.color}"></span>` +
            `<span style="font-family:${mono};font-weight:600">${fmt(Number(v) || 0)}</span>` +
            `<span style="color:${textSecondary}">${p.seriesName}</span></div>`
        }).join('')
        return `<div style="color:${textSecondary};font-size:11px">${head}</div>${rows}`
      }
    },
    xAxis: isCategory
      ? {
          type: 'category',
          data: props.categories,
          axisLine: { lineStyle: { color: borderDefault } },
          axisTick: { show: false },
          axisLabel: { color: textTertiary, fontSize: 11, fontFamily: mono, hideOverlap: true }
        }
      : {
          type: 'time',
          axisLine: { lineStyle: { color: borderDefault } },
          axisTick: { show: false },
          splitLine: { show: false },
          axisLabel: {
            color: textTertiary, fontSize: 11, fontFamily: mono, hideOverlap: true,
            formatter: (v: number) => timeLabel(v, withDate)
          }
        },
    yAxis: {
      type: 'value',
      min: 0,
      max: props.yMax,
      // 固定上限时按四等分取刻度（0/25/50/75/100），否则 ECharts 会给出 0/30/60/90/100 这种末段不等距的刻度。
      interval: props.yMax ? props.yMax / 4 : undefined,
      splitNumber: 4,
      axisLabel: { color: textTertiary, fontSize: 11, fontFamily: mono, formatter: (v: number) => fmt(v) },
      splitLine: { lineStyle: { color: border } }
    },
    series: props.series.map((s, i) => props.type === 'bar'
      ? {
          name: s.name,
          type: 'bar',
          data: s.data,
          barMaxWidth: 14,
          barGap: '15%',
          itemStyle: { borderRadius: [3, 3, 0, 0] }
        }
      : {
          name: s.name,
          type: 'line',
          data: s.data,
          showSymbol: false,
          symbolSize: 6,
          lineStyle: { width: 2 },
          // 只给第一条序列铺一层很淡的面积，两条都铺会互相糊住。
          areaStyle: i === 0 ? { opacity: 0.08 } : undefined,
          emphasis: { disabled: true }
        })
  }
}

function render() {
  if (!el.value || !hasData.value) return
  if (!chart) chart = echarts.init(el.value)
  chart.setOption(buildOption(), true)
}

onMounted(() => {
  render()
  resizeObs = new ResizeObserver(() => chart?.resize())
  if (el.value) resizeObs.observe(el.value)
  // 主题切换只改 <html data-theme>，颜色要重新从 CSS 变量里读一次。
  themeObs = new MutationObserver(render)
  themeObs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
})

watch(() => [props.series, props.categories, props.yMax], () => {
  if (!hasData.value) {
    chart?.dispose()
    chart = null
    return
  }
  // 从空态切回有数据时，容器是新挂上的，要重新 init。
  requestAnimationFrame(render)
}, { deep: true })

onBeforeUnmount(() => {
  resizeObs?.disconnect()
  themeObs?.disconnect()
  chart?.dispose()
  chart = null
})
</script>

<template>
  <div class="mchart">
    <div v-show="hasData" ref="el" :style="{ height: height + 'px' }" />
    <p v-if="!hasData" class="mchart__empty" :style="{ height: height + 'px' }">{{ emptyText }}</p>
  </div>
</template>

<style scoped>
.mchart { padding: 14px 16px 12px; }
.mchart__empty {
  margin: 0; display: flex; align-items: center; justify-content: center;
  font-size: 12px; color: var(--text-tertiary);
}
</style>
