<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import AccountChip from './AccountChip.vue'

const route = useRoute()
const router = useRouter()
const {
  state, transitioning, allRegions,
  toggleAccountFilter, selectAllAccounts,
  toggleRegionFilter, clearRegionFilter, logout
} = useStore()

const popOpen = ref(false)
const regionOpen = ref(false)
const userOpen = ref(false)

const crumb = computed(() => (route.meta.title as string) ?? '')
const allSelected = computed(() => state.accountFilter.size === state.accounts.length)
const filterLabel = computed(() =>
  allSelected.value ? '全部账号' : `${state.accountFilter.size} 个账号`
)
const dots = computed(() =>
  state.accounts.filter(a => state.accountFilter.has(a.id)).map(a => acctColor(a.colorIndex))
)

/** 区域筛选：空集即"全部区域"。 */
const regionLabel = computed(() =>
  state.regionFilter.size === 0 ? '全部区域' : `${state.regionFilter.size} 个区域`
)

const initial = computed(() => (state.username || 'A').charAt(0).toUpperCase())

async function doLogout() {
  userOpen.value = false
  await logout()
  router.push('/login')
}
</script>

<template>
  <header class="top glass">
    <nav class="top__crumbs" aria-label="面包屑">
      <span class="dim">OCI Core</span>
      <span class="dim-3">/</span>
      <span class="top__here">{{ crumb }}</span>
    </nav>

    <button class="top__search" @click="state.paletteOpen = true">
      <span>⌕</span>
      <span class="top__search-label">搜索实例 · OCID · IP</span>
      <span class="top__kbd mono">⌘K</span>
    </button>

    <span class="top__spacer" />

    <div class="top__filter-wrap">
      <button class="top__control" :class="{ 'is-active': !allSelected }" @click="popOpen = !popOpen"
              aria-haspopup="listbox" :aria-expanded="popOpen">
        <span class="top__dots">
          <span v-for="c in dots" :key="c" class="top__dot" :style="{ background: c }" />
        </span>
        <span>{{ filterLabel }}</span>
        <span class="dim-3">▾</span>
      </button>

      <div v-if="popOpen" class="top__scrim" @click="popOpen = false" />
      <div v-if="popOpen" class="top__pop" role="listbox">
        <button class="top__pop-all" @click="selectAllAccounts()">全选 <span class="mono">{{ state.accounts.length }}</span></button>
        <hr />
        <button v-for="a in state.accounts" :key="a.id" class="top__pop-row"
                role="option" :aria-selected="state.accountFilter.has(a.id)"
                @click="toggleAccountFilter(a.id)">
          <span class="top__check" :class="{ 'is-on': state.accountFilter.has(a.id) }">✓</span>
          <AccountChip :account="a" variant="full" />
        </button>
      </div>
    </div>

    <div class="top__filter-wrap">
      <button class="top__control" :class="{ 'is-active': state.regionFilter.size > 0 }"
              @click="regionOpen = !regionOpen" aria-haspopup="listbox" :aria-expanded="regionOpen">
        <span>{{ regionLabel }}</span><span class="dim-3">▾</span>
      </button>

      <div v-if="regionOpen" class="top__scrim" @click="regionOpen = false" />
      <div v-if="regionOpen" class="top__pop" role="listbox">
        <button class="top__pop-all" @click="clearRegionFilter()">
          全部区域 <span class="mono">{{ allRegions.length }}</span>
        </button>
        <hr />
        <button v-for="r in allRegions" :key="r" class="top__pop-row"
                role="option" :aria-selected="state.regionFilter.has(r)"
                @click="toggleRegionFilter(r)">
          <span class="top__check" :class="{ 'is-on': state.regionFilter.has(r) }">✓</span>
          <span class="mono t-xs">{{ r }}</span>
        </button>
      </div>
    </div>

    <button v-if="transitioning.length" class="top__transitions" @click="router.push('/instances')">
      <span class="top__pulse"><span /></span>
      <span class="mono">{{ transitioning.length }}</span>
      <span>台正在转换</span>
    </button>

    <span class="top__sep" />

    <span class="top__sep" />

    <div class="top__filter-wrap">
      <button class="top__avatar" :title="state.username" @click="userOpen = !userOpen">
        {{ initial }}
      </button>
      <div v-if="userOpen" class="top__scrim" @click="userOpen = false" />
      <div v-if="userOpen" class="top__pop top__pop--user">
        <p class="top__user">{{ state.username || '未登录' }}</p>
        <hr />
        <button class="top__pop-row" @click="userOpen = false; router.push('/settings')">设置</button>
        <button class="top__pop-row" @click="doLogout()">退出登录</button>
      </div>
    </div>
  </header>
</template>

<style scoped>
.top {
  height: var(--topbar-h); flex: 0 0 auto; display: flex; align-items: center; gap: 12px;
  padding: 0 20px; border: 0; border-bottom: 1px solid var(--border-subtle);
  position: sticky; top: 0; z-index: 20;
}
.top__crumbs { display: flex; align-items: center; gap: 8px; font-size: 13px; min-width: 0; }
.top__here { color: var(--text-primary); font-weight: 500; white-space: nowrap; }
.top__spacer { flex: 1 1 auto; }

.top__search {
  display: flex; align-items: center; gap: 10px; width: 250px; height: 32px; padding: 0 10px;
  border-radius: var(--radius-md); border: 1px solid var(--border-default);
  background: var(--bg-inset); color: var(--text-tertiary); font-size: 12px; cursor: pointer;
}
.top__search:hover { border-color: var(--border-strong); }
.top__search-label { flex: 1 1 auto; text-align: left; }
.top__kbd { font-size: 11px; padding: 1px 5px; border-radius: var(--radius-sm); border: 1px solid var(--border-default); }

.top__control {
  display: flex; align-items: center; gap: 8px; height: 32px; padding: 0 10px;
  border-radius: var(--radius-md); border: 1px solid var(--border-default);
  background: var(--bg-inset); color: var(--text-primary); font-size: 12px; cursor: pointer; white-space: nowrap;
}
.top__control:hover { border-color: var(--border-strong); }
.top__control.is-active { border-color: var(--accent); }
.top__dots { display: flex; gap: 3px; }
.top__dot { width: 6px; height: 6px; border-radius: var(--radius-full); }

.top__filter-wrap { position: relative; }
.top__scrim { position: fixed; inset: 0; z-index: 30; }
.top__pop {
  position: absolute; z-index: 31; top: 38px; right: 0; width: 268px; padding: 6px;
  border-radius: var(--radius-md); border: 1px solid var(--border-default);
  background: var(--bg-elevated); box-shadow: var(--shadow-3);
  animation: rise var(--dur-fast) var(--ease-decelerate);
}
.top__pop hr { border: 0; border-top: 1px solid var(--border-subtle); margin: 4px 0; }
.top__pop-all, .top__pop-row {
  display: flex; align-items: center; gap: 9px; width: 100%; height: 34px; padding: 0 10px;
  border: 0; border-radius: var(--radius-sm); background: transparent; color: var(--text-primary);
  font-size: 12px; cursor: pointer; text-align: left;
}
.top__pop-all { height: 30px; color: var(--accent); justify-content: space-between; }
.top__pop-row:hover, .top__pop-all:hover { background: var(--bg-hover); }
.top__check {
  width: 14px; height: 14px; flex: 0 0 auto; display: flex; align-items: center; justify-content: center;
  border-radius: 4px; border: 1px solid var(--border-strong); color: transparent; font-size: 9px;
}
.top__check.is-on { background: var(--accent); border-color: var(--accent); color: var(--bg-base); }

.top__transitions {
  display: flex; align-items: center; gap: 8px; height: 32px; padding: 0 11px;
  border-radius: var(--radius-full); border: 1px solid var(--accent);
  background: var(--accent-soft); color: var(--accent);
  font-size: 12px; font-weight: 500; cursor: pointer; white-space: nowrap;
}
.top__pulse { position: relative; width: 6px; height: 6px; }
.top__pulse::before, .top__pulse > span {
  content: ''; position: absolute; inset: 0; border-radius: var(--radius-full); background: var(--accent);
}
.top__pulse::before { animation: pulse 2s ease-in-out infinite; }
.top__pulse > span { animation: ring 2s ease-out infinite; }

.top__sep { width: 1px; height: 22px; background: var(--border-subtle); }
.top__pop--user { min-width: 168px; }
.top__user { margin: 0; padding: 8px 12px; font-size: 12px; color: var(--text-secondary); }
.top__avatar {
  width: 28px; height: 28px; flex: 0 0 auto; border: 0; border-radius: var(--radius-full);
  background: var(--acct-5); color: #fff; font-size: 11px; font-weight: 600;
  display: flex; align-items: center; justify-content: center; cursor: pointer;
  padding: 0;
}
</style>
