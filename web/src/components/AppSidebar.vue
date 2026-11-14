<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useStore } from '@/store'
import BrandMark from '@/components/BrandMark.vue'
import { locale, setLocale, t } from '@/i18n'

const route = useRoute()
const { state, visibleInstances, activeJobCount } = useStore()

const items = computed(() => [
  { to: '/overview', icon: '▦', label: t.nav.side.overview },
  { to: '/accounts', icon: '⬡', label: t.nav.side.accounts, badge: state.accounts.length },
  { to: '/instances', icon: '▤', label: t.nav.side.instances, badge: visibleInstances.value.filter(i => i.state !== 'TERMINATED').length },
  { to: '/network', icon: '⌗', label: t.nav.side.network },
  { to: '/storage', icon: '◱', label: t.nav.side.storage },
  // 按账号盘点：网络、存储两页按资源类型跨账号聚合，这一页反过来按账号看。
  { to: '/resources', icon: '▣', label: t.nav.side.resources },
  // 徽标只数进行中的：已结束的任务不需要注意力。
  { to: '/jobs', icon: '◷', label: t.nav.side.jobs, badge: activeJobCount.value, live: activeJobCount.value > 0 },
  { to: '/capacity', icon: '◇', label: t.nav.side.capacity },
  { to: '/billing', icon: '¤', label: t.nav.side.billing },
  { to: '/hunt', icon: '◈', label: t.nav.side.hunt },
  { to: '/proxies', icon: '⇄', label: t.nav.side.proxies },
  { to: '/notifications', icon: '✎', label: t.nav.side.notifications },
  { to: '/settings', icon: '⚙', label: t.nav.side.settings }
])

/** 磁吸指示器（§7 Tier 2.11）：只动 transform */
const activeIndex = computed(() => Math.max(0, items.value.findIndex(i => route.path.startsWith(i.to))))
</script>

<template>
  <nav class="side" :class="{ 'is-collapsed': state.sidebarCollapsed }" :aria-label="t.nav.mainNav">
    <div class="side__brand">
      <BrandMark class="side__mark" :size="18" />
      <span class="side__name">OCI Core</span>
    </div>

    <div class="side__nav">
      <span class="side__indicator" :style="{ transform: `translateY(${activeIndex * 40}px)` }" />
      <RouterLink v-for="item in items" :key="item.to" :to="item.to" class="side__item">
        <span class="side__icon">{{ item.icon }}</span>
        <span class="side__label">{{ item.label }}</span>
        <span v-if="item.badge" class="side__badge mono" :class="{ 'is-live': item.live }">{{ item.badge }}</span>
      </RouterLink>
    </div>

    <div class="side__foot">
      <button class="side__item side__item--btn" @click="state.theme = state.theme === 'dark' ? 'light' : 'dark'">
        <span class="side__icon">{{ state.theme === 'dark' ? '☾' : '☀' }}</span>
        <span class="side__label">{{ state.theme === 'dark' ? t.nav.toLight : t.nav.toDark }}</span>
      </button>
      <button class="side__item side__item--btn" @click="state.sidebarCollapsed = !state.sidebarCollapsed">
        <span class="side__icon">{{ state.sidebarCollapsed ? '»' : '«' }}</span>
        <span class="side__label">{{ state.sidebarCollapsed ? t.nav.expand : t.nav.collapse }}</span>
      </button>
      <button class="side__item side__item--btn" :title="t.common.language"
              @click="setLocale(locale === 'zh-CN' ? 'en' : 'zh-CN')">
        <span class="side__icon">{{ locale === 'zh-CN' ? 'EN' : '中' }}</span>
        <span class="side__label">{{ t.nav.switchLanguage }}</span>
      </button>
    </div>
  </nav>
</template>

<style scoped>
.side {
  width: var(--sidebar-w); flex: 0 0 auto; display: flex; flex-direction: column;
  background: var(--bg-surface); border-right: 1px solid var(--border-subtle);
  transition: width var(--dur-normal) var(--ease-standard);
}
.side.is-collapsed { width: var(--sidebar-w-collapsed); }
.side.is-collapsed .side__label, .side.is-collapsed .side__badge, .side.is-collapsed .side__name { opacity: 0; }

.side__brand {
  height: var(--topbar-h); display: flex; align-items: center; gap: 9px; padding: 0 18px;
  border-bottom: 1px solid var(--border-subtle); overflow: hidden;
}
.side__mark { flex: 0 0 auto; color: var(--accent); }
.side__name { font-size: 14px; font-weight: 600; white-space: nowrap; transition: opacity var(--dur-fast); }

.side__nav { position: relative; flex: 1 1 auto; padding: 10px 8px; overflow: hidden; }
.side__indicator {
  position: absolute; left: 8px; top: 18px; width: 3px; height: 22px;
  border-radius: var(--radius-full); background: var(--accent);
  transition: transform 320ms var(--ease-spring);
}
.side__item {
  display: flex; align-items: center; gap: 12px; height: 38px; padding: 0 12px; margin-bottom: 2px;
  border: 0; width: 100%; border-radius: var(--radius-md); background: transparent;
  color: var(--text-secondary); font-size: 13px; cursor: pointer; text-align: left;
  transition: background var(--dur-fast), color var(--dur-fast);
}
.side__item:hover { background: var(--bg-hover); color: var(--text-primary); }
.side__item:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
.side__item.router-link-active { background: var(--bg-hover); color: var(--text-primary); font-weight: 600; }
.side__icon { width: 18px; flex: 0 0 auto; text-align: center; font-size: 14px; }
.side__label { flex: 1 1 auto; white-space: nowrap; transition: opacity var(--dur-fast); }
.side__badge {
  min-width: 20px; height: 18px; padding: 0 6px; display: flex; align-items: center; justify-content: center;
  border-radius: var(--radius-full); background: var(--bg-inset); border: 1px solid var(--border-subtle);
  color: var(--text-secondary); font-size: 11px; font-weight: 500; transition: opacity var(--dur-fast);
}
.side__badge.is-live { border-color: var(--accent); color: var(--accent); }
.side__foot {
  padding: 10px 8px; border-top: 1px solid var(--border-subtle);
  display: flex; flex-direction: column; gap: 2px;
}
.side__item--btn { height: 34px; }

@media (max-width: 1279px) {
  .side { width: var(--sidebar-w-collapsed); }
  .side .side__label, .side .side__badge, .side .side__name { opacity: 0; }
}
</style>
