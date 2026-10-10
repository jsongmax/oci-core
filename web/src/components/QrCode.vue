<script setup lang="ts">
/**
 * 二维码。目前只用于绑定两步验证：把 otpauth:// URI 画给验证器扫。
 *
 * 编码交给 qrcode-generator，绘制自己来：按模块拼一条 SVG path，不用库
 * 产出的 HTML 字符串，免得走 v-html。颜色固定深色模块 + 白底 + 四格静区，
 * 不跟随深色主题——反色二维码不少验证器扫不出来。
 */
import { computed } from 'vue'
import qrcode from 'qrcode-generator'

const props = withDefaults(defineProps<{ value: string; size?: number; label?: string }>(), {
  size: 168,
  label: '二维码'
})

/** QR 规范要求四个模块宽的静区。 */
const QUIET = 4

const qr = computed(() => {
  // 类型 0 让库按内容长度自动选版本；纠错 M 在体积与容错之间折中。
  const code = qrcode(0, 'M')
  code.addData(props.value)
  code.make()

  const n = code.getModuleCount()
  let d = ''
  for (let r = 0; r < n; r++) {
    for (let c = 0; c < n; c++) {
      if (code.isDark(r, c)) d += `M${c + QUIET} ${r + QUIET}h1v1h-1z`
    }
  }
  return { d, span: n + QUIET * 2 }
})
</script>

<template>
  <svg class="qr" :width="size" :height="size" :viewBox="`0 0 ${qr.span} ${qr.span}`"
       shape-rendering="crispEdges" role="img" :aria-label="label">
    <rect :width="qr.span" :height="qr.span" fill="#fff" />
    <path :d="qr.d" fill="#111" />
  </svg>
</template>

<style scoped>
.qr { display: block; border-radius: var(--radius-sm); }
</style>
