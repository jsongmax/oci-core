import { createRouter, createWebHashHistory } from 'vue-router'
import { watch } from 'vue'
import { useStore } from '@/store'
import { locale, t } from '@/i18n'

/** hash 路由：Go 单二进制内嵌静态资源时无需服务端 rewrite */
const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', redirect: '/overview' },
    { path: '/login', name: 'login', component: () => import('@/views/LoginView.vue'), meta: { bare: true } },
    { path: '/overview', name: 'overview', component: () => import('@/views/OverviewView.vue') },
    { path: '/accounts', name: 'accounts', component: () => import('@/views/AccountsView.vue') },
    { path: '/instances', name: 'instances', component: () => import('@/views/InstancesView.vue') },
    { path: '/network', name: 'network', component: () => import('@/views/NetworkView.vue') },
    { path: '/jobs', name: 'jobs', component: () => import('@/views/JobsView.vue') },
    { path: '/hunt', name: 'hunt', component: () => import('@/views/HuntView.vue') },
    { path: '/capacity', name: 'capacity', component: () => import('@/views/CapacityView.vue') },
    { path: '/storage', name: 'storage', component: () => import('@/views/StorageView.vue') },
    { path: '/resources', name: 'resources', component: () => import('@/views/ResourcesView.vue') },
    { path: '/billing', name: 'billing', component: () => import('@/views/BillingView.vue') },
    { path: '/proxies', name: 'proxies', component: () => import('@/views/ProxiesView.vue') },
    { path: '/notifications', name: 'notifications', component: () => import('@/views/NotificationsView.vue') },
    { path: '/settings', name: 'settings', component: () => import('@/views/SettingsView.vue') },
    // 兜底。移除移动端路由（/m/*）后，旧书签会匹配不到任何路由，
    // 渲染出一个只有外壳、内容区空白的页面——看起来像界面崩了。
    { path: '/:pathMatch(.*)*', redirect: '/overview' }
  ]
})

/**
 * 首次进入时探测会话状态，之后仅做拦截。
 *
 * 探测放在守卫里而不是 main.ts：这样刷新任意深层路由都能正确落位，
 * 而不是先闪一下登录页再跳回来。
 */
let probed = false

router.beforeEach(async (to) => {
  const store = useStore()

  if (!probed) {
    probed = true
    await store.loadStatus()
    if (store.state.authed) {
      // 不 await：让页面先渲染出骨架屏，数据到了再填。
      void store.loadAll()
      store.startLiveUpdates()
    }
  }

  // 已登录还去登录页就直接送回总览，否则会再看一遍登录表单。
  if (to.name === 'login') {
    return store.state.authed ? { name: 'overview' } : true
  }
  if (store.state.authed) return true

  return { name: 'login' }
})

/** 页面名取自路由名，按当前语言显示。 */
export function pageTitle(name: unknown): string {
  return typeof name === 'string' ? (t.nav.pages[name] ?? '') : ''
}

function applyTitle(): void {
  const title = pageTitle(router.currentRoute.value.name)
  document.title = title ? `${title} · OCI Core` : 'OCI Core'
}

router.afterEach(() => applyTitle())
// 切换语言时标签页标题也要跟着变，不必等下一次跳转。
watch(locale, () => applyTitle())

export default router
