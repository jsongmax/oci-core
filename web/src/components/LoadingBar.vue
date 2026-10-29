<script setup lang="ts">
/**
 * 页头下方的不定进度条：刷新时告诉用户「在读」，旧数据照常显示。
 *
 * 永远占 2px 的位置、只切透明度——条子出现和消失时页面不跟着上下跳。
 */
defineProps<{ active: boolean }>()
</script>

<template>
  <div class="lbar" :class="{ 'is-on': active }" aria-hidden="true"><span /></div>
</template>

<style scoped>
.lbar {
  height: 2px; margin: -14px 0 12px; overflow: hidden; border-radius: 2px;
  opacity: 0; transition: opacity var(--dur-normal) var(--ease-standard);
}
.lbar.is-on { opacity: 1; }
.lbar > span { display: block; height: 2px; width: 40%; background: var(--accent); transform: translateX(-100%); }
.lbar.is-on > span { animation: indeterminate 1.4s var(--ease-standard) infinite; }
</style>
