<script setup lang="ts">
/**
 * 演示站顶部的常驻提示。只在 VITE_DEMO 构建里加载。
 *
 * 必须一直看得见：演示站上的账号、IP、账单都是编的，截图被转出去时
 * 也要能一眼看出这不是谁的真实环境。
 */
import { computed } from 'vue'
import { locale } from '@/i18n'

const repo = 'https://github.com/jsongmax/oci-core'
// 只有演示站用到这三句，不进主字典。
const text = computed(() => locale.value === 'en'
  ? { tag: 'Demo', body: 'All data is simulated and no Oracle account is connected; actions live only in browser memory and reset on reload.', link: 'Deploy your own →' }
  : { tag: '演示站', body: '所有数据均为模拟，不连接任何 Oracle 账号；操作只在浏览器内存里生效，刷新页面即恢复。', link: '部署自己的 →' })
</script>

<template>
  <div class="demo-banner" role="note">
    <span class="demo-banner__tag">{{ text.tag }}</span>
    <span class="demo-banner__text">{{ text.body }}</span>
    <a class="demo-banner__link" :href="repo" target="_blank" rel="noopener noreferrer">{{ text.link }}</a>
  </div>
</template>

<style scoped>
.demo-banner {
  flex: 0 0 auto; display: flex; align-items: center; gap: 10px; min-height: 30px; padding: 4px 16px;
  background: var(--accent-soft); border-bottom: 1px solid var(--accent); color: var(--text-primary); font-size: 12px;
}
.demo-banner__tag {
  flex: 0 0 auto; padding: 1px 8px; border-radius: var(--radius-full);
  background: var(--accent); color: var(--bg-base); font-weight: 600; font-size: 11px;
}
.demo-banner__text { flex: 1 1 auto; min-width: 0; color: var(--text-secondary); }
.demo-banner__link { flex: 0 0 auto; color: var(--accent); text-decoration: none; font-weight: 500; }
.demo-banner__link:hover { text-decoration: underline; }
@media (max-width: 767px) { .demo-banner__text { display: none; } }
</style>
