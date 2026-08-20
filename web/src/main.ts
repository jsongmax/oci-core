import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import { useStore } from './store'
import { AUTH_REQUIRED_EVENT } from './api'
import './styles/base.css'

const app = createApp(App)

/**
 * 会话失效的统一出口。
 *
 * HTTP 层在收到 401 时广播事件而不是直接跳转——路由不该被硬编码进
 * 网络层。这里是唯一决定"去哪"的地方。
 */
window.addEventListener(AUTH_REQUIRED_EVENT, () => {
  const { state, onSessionLost } = useStore()
  if (state.authed === false) return
  onSessionLost()
  if (router.currentRoute.value.name !== 'login') {
    router.push('/login')
  }
})

app.use(router).mount('#app')
