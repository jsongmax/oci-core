<script setup lang="ts">
/** §4.4.2 行内操作安全分级：L1 直接 / L2 确认 / L3 输名确认（藏在 ⋯ 底部） */
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useStore } from '@/store'
import { isTransitional } from '@/lib/lifecycle'
import type { Instance } from '@/types'
import { copy } from '@/lib/format'

const props = defineProps<{ instance: Instance }>()
const {
  state, options, ask, toast, toastError, openDrawer,
  start, stop, restart, terminate
} = useStore()

const menuOpen = ref(false)
const menuEl = ref<HTMLElement>()
const dotsEl = ref<HTMLButtonElement>()

/**
 * 菜单挂到 body 上，用 fixed 定位。
 *
 * 原来是 absolute 定位在行内。表格卡片 .card.table 为了裁圆角用了
 * overflow: hidden，于是菜单被切掉一半——实测 6 项只露出 2 项，
 * 下面的强制重启、更换 IP、分离引导卷、终止实例全部看不见。而且切得
 * 干干净净，看起来像"这个菜单本来就只有两项"，不像出了问题。
 *
 * 任何祖先加一句 overflow 都会重现这个 bug，所以不去改卡片的 overflow，
 * 而是让菜单彻底离开那棵子树。
 */
const pos = reactive({ top: 0, right: 0 })
const MENU_GAP = 6
const VIEWPORT_MARGIN = 8

function place() {
  const btn = dotsEl.value
  const menu = menuEl.value
  if (!btn || !menu) return
  const r = btn.getBoundingClientRect()
  const h = menu.offsetHeight

  let top = r.bottom + MENU_GAP
  // 列表末尾的行，菜单往下放会掉出视口，翻到按钮上方。
  if (top + h > window.innerHeight - VIEWPORT_MARGIN) {
    top = Math.max(VIEWPORT_MARGIN, r.top - MENU_GAP - h)
  }
  pos.top = top
  pos.right = Math.max(VIEWPORT_MARGIN, window.innerWidth - r.right)
}

async function toggleMenu() {
  if (menuOpen.value) {
    menuOpen.value = false
    return
  }
  menuOpen.value = true
  // 要等菜单渲染出来才量得到高度，否则翻转判断永远用 0。
  await nextTick()
  place()
}

// 脱离了文档流就不会跟着行走了，滚动和缩放时得自己跟上。
// capture 是必须的：真正滚动的是 .shell__content，不是 window。
function onViewportChange() {
  if (menuOpen.value) place()
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') menuOpen.value = false
}

watch(menuOpen, open => {
  if (open) {
    window.addEventListener('scroll', onViewportChange, true)
    window.addEventListener('resize', onViewportChange)
    window.addEventListener('keydown', onKeydown)
  } else {
    window.removeEventListener('scroll', onViewportChange, true)
    window.removeEventListener('resize', onViewportChange)
    window.removeEventListener('keydown', onKeydown)
  }
})

onBeforeUnmount(() => {
  window.removeEventListener('scroll', onViewportChange, true)
  window.removeEventListener('resize', onViewportChange)
  window.removeEventListener('keydown', onKeydown)
})
const busy = computed(() => !!props.instance.busy)
/** 过渡期间禁用该行所有操作按钮（§6.3 硬规则 2） */
const locked = computed(() => isTransitional(props.instance.state))

function forceRestart() {
  const i = props.instance
  // 强制重启走后端的 RESET（带 force），与软重启是两个不同的操作。
  const run = () => useStore().forceRestart(i.id)
  if (!options.confirmForceRestart) return run()
  ask({
    level: 2, title: `强制重启 ${i.name}`,
    body: '相当于直接断电重启，未落盘的数据会丢失，文件系统可能需要自检。',
    okLabel: '强制重启', onConfirm: run
  })
}

function changeIp() {
  const i = props.instance
  ask({
    level: 2, title: `更换 ${i.name} 的公网 IP`,
    body: `更换后原 IP ${i.publicIp} 不可找回，当前 SSH 连接将中断，DNS 记录需要手动更新。`,
    okLabel: '更换 IP',
    onConfirm: async () => {
      try {
        const { instances } = await import('@/api')
        const result = await instances.changeIp(i.id)
        i.publicIp = result.newIp
        toast({
          tone: 'success', title: `${i.name} 已更换公网 IP`,
          body: `${result.oldIp} → ${result.newIp}`,
          command: `ssh ubuntu@${result.newIp}`
        })
      } catch (err) {
        toastError('更换 IP 失败', err)
      }
    }
  })
}

/**
 * 分离引导卷需要挂载关系的 OCID，行内拿不到——那是详情接口才返回的。
 * 因此这里只负责把用户送到详情抽屉的存储页，真正的操作在那里执行。
 */
function detachBoot() {
  menuOpen.value = false
  openDrawer({ kind: 'instance', id: props.instance.id, tab: '存储' })
}

function askTerminate() {
  const i = props.instance
  ask({
    level: 3, title: `终止实例 ${i.name}`,
    body: '该操作会立即销毁实例与其挂载的引导卷。',
    noun: i.name, nounLabel: '实例名称',
    losses: [
      '实例本身及其全部系统数据',
      `${i.name}-boot（${i.bootGb} GB 引导卷）`,
      `临时公网 IP ${i.publicIp}`
    ],
    okLabel: '永久终止',
    onConfirm: () => terminate(i.id)
  })
}
</script>

<template>
  <div class="acts">
    <button v-if="instance.state === 'STOPPED'" class="icon-btn" :disabled="locked"
            title="开机" style="color: var(--success)" @click="start(instance.id)">
      <span :class="{ 'is-spinning': busy }">▶</span>
    </button>
    <button v-else class="icon-btn" :disabled="locked" title="关机" @click="stop(instance.id)">
      <span :class="{ 'is-spinning': busy }">⏸</span>
    </button>
    <button class="icon-btn" :disabled="locked" title="软重启" @click="restart(instance.id)">↻</button>
    <button class="icon-btn" title="复制 SSH 命令"
            @click="copy(`ssh ubuntu@${instance.publicIp}`).then(() => toast({ tone: 'accent', title: '已复制 SSH 命令', body: `ssh ubuntu@${instance.publicIp}` }))">⌘</button>

    <button ref="dotsEl" class="icon-btn" aria-haspopup="menu" aria-label="更多操作"
            :aria-expanded="menuOpen" @click="toggleMenu()">⋯</button>

    <Teleport to="body">
    <div v-if="menuOpen" class="acts__scrim" @click="menuOpen = false" />
    <div v-if="menuOpen" ref="menuEl" class="acts__menu" role="menu"
         :style="{ top: `${pos.top}px`, right: `${pos.right}px` }">
      <button role="menuitem" @click="menuOpen = false; openDrawer({ kind: 'instance', id: instance.id, tab: '概览' })">
        查看详情
      </button>
      <button role="menuitem" @click="menuOpen = false; openDrawer({ kind: 'instance', id: instance.id, tab: '存储' })">
        修改配置<span v-if="instance.state === 'RUNNING'" class="acts__hint">会重启</span>
      </button>
      <hr />
      <button role="menuitem" class="is-warning" :disabled="locked" @click="menuOpen = false; forceRestart()">
        强制重启<span class="acts__hint">L2</span>
      </button>
      <button role="menuitem" class="is-warning" :disabled="locked" @click="menuOpen = false; changeIp()">
        更换公网 IP<span class="acts__hint">L2</span>
      </button>
      <button role="menuitem" class="is-warning" :disabled="locked" @click="menuOpen = false; detachBoot()">
        分离引导卷<span class="acts__hint">L2</span>
      </button>
      <hr />
      <!-- L3：红色文字，菜单最底部，可在设置中全局禁用 -->
      <button v-if="state.options.allowTerminate" role="menuitem" class="is-danger"
              :disabled="locked" @click="menuOpen = false; askTerminate()">
        终止实例<span class="acts__hint">L3</span>
      </button>
      <button v-else role="menuitem" class="is-disabled" disabled>
        终止实例<span class="acts__hint">已在设置中禁用</span>
      </button>
    </div>
    </Teleport>
  </div>
</template>

<style scoped>
.acts { position: relative; display: flex; align-items: center; gap: 4px; }
.is-spinning { display: inline-block; animation: spin 700ms linear infinite; }

.acts__scrim { position: fixed; inset: 0; z-index: 44; }
.acts__menu {
  /* fixed + teleport：绝对定位会被表格卡片的 overflow: hidden 裁掉。 */
  position: fixed; z-index: 45; width: 228px; padding: 6px;
  border-radius: var(--radius-md); border: 1px solid var(--border-default);
  background: var(--bg-elevated); box-shadow: var(--shadow-3);
  animation: rise var(--dur-fast) var(--ease-decelerate);
}
.acts__menu button {
  display: flex; align-items: center; justify-content: space-between; gap: 10px;
  width: 100%; height: 32px; padding: 0 10px; border: 0; border-radius: var(--radius-sm);
  background: transparent; color: var(--text-primary); font-size: 13px; cursor: pointer; text-align: left;
}
.acts__menu button:hover:not([disabled]) { background: var(--bg-hover); }
.acts__menu button[disabled] { opacity: 0.5; cursor: not-allowed; }
.acts__menu hr { border: 0; border-top: 1px solid var(--border-subtle); margin: 4px 0; }
.acts__menu .is-warning { color: var(--warning); }
.acts__menu .is-danger { color: var(--danger); }
.acts__hint { font-size: 11px; color: var(--text-tertiary); }
</style>
