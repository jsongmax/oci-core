<script setup lang="ts">
/**
 * OCI Core 的标记。
 *
 * 原 logo 有云形、OCI 三个字母、三个圆点和一行中文副标题。整套搬进 18px
 * 的侧栏或 16px 的标签页只会糊成一团——字母永远是最先垮掉的部分。
 * 这里只留三个在小尺寸仍然立得住的元素：云的轮廓、橙色的 O 环、
 * 以及 C 里那两三个点（它们正好也对应"多个实例"）。
 *
 * 深色部分用 currentColor，而不是原 logo 的深藏青：那个颜色放在深色侧栏上
 * 几乎看不见。橙色是品牌识别的主体，固定不变；深色部分交给主题决定，
 * 两种主题下都能保证对比度。
 */
withDefaults(defineProps<{ size?: number }>(), { size: 18 })

// 每个实例一个渐变 id：同一页出现多次时（侧栏 + 登录页）不会互相覆盖。
const gid = `ocicore-${Math.random().toString(36).slice(2, 9)}`
</script>

<template>
  <svg :width="size" :height="size" viewBox="0 0 32 32" fill="none"
       role="img" aria-label="OCI Core">
    <defs>
      <linearGradient :id="gid" x1="4" y1="14" x2="28" y2="8" gradientUnits="userSpaceOnUse">
        <stop offset="0" stop-color="#F04E23" />
        <stop offset="1" stop-color="currentColor" />
      </linearGradient>
    </defs>

    <!-- 云的上沿。只画轮廓、不闭合，下半留给 O 与圆点 -->
    <path d="M4.5 14.5 A5.5 5.5 0 0 1 10.5 6.6 A7 7 0 0 1 23.5 8.2 A5 5 0 0 1 27.5 14.5"
          :stroke="`url(#${gid})`" stroke-width="2.2" stroke-linecap="round" />

    <!-- O：品牌橙，不随主题变 -->
    <circle cx="11" cy="21" r="5.5" stroke="#F04E23" stroke-width="2.8" />

    <!-- C 里的圆点 -->
    <circle cx="21" cy="21" r="1.9" fill="#F04E23" />
    <circle cx="26.5" cy="21" r="1.9" fill="currentColor" opacity="0.6" />
  </svg>
</template>
