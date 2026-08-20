<script setup lang="ts">
/**
 * 列显示选择器。
 *
 * 和 SelectMenu 一样 Teleport 到 body：所在的 toolbar 有 overflow 裁剪，
 * 面板留在原地会被切掉一半。定位用 fixed + 手算坐标，滚动和缩放时重算。
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import type { ColumnState } from '@/lib/columns'

const props = defineProps<{ columns: ColumnState }>()

const open = ref(false)
const triggerEl = ref<HTMLButtonElement>()
const panelEl = ref<HTMLElement>()
const pos = ref({ top: 0, left: 0 })

const PANEL_W = 210
const GAP = 6
const MARGIN = 8

const hiddenCount = computed(() =>
  props.columns.toggleable.filter(c => !props.columns.isOn(c.key)).length)

function place() {
  const t = triggerEl.value
  if (!t) return
  const r = t.getBoundingClientRect()
  // 右对齐触发器，再夹回视口内——这个按钮通常靠右，左对齐会溢出屏幕。
  let left = r.right - PANEL_W
  left = Math.max(MARGIN, Math.min(left, window.innerWidth - PANEL_W - MARGIN))
  pos.value = { top: r.bottom + GAP, left }
}

function toggleOpen() {
  open.value = !open.value
  if (open.value) nextTick(place)
}

function onScroll() {
  if (open.value) place()
}

window.addEventListener('scroll', onScroll, true)
window.addEventListener('resize', onScroll)
onBeforeUnmount(() => {
  window.removeEventListener('scroll', onScroll, true)
  window.removeEventListener('resize', onScroll)
})

watch(open, v => {
  if (!v) return
  const onKey = (e: KeyboardEvent) => {
    if (e.key === 'Escape') {
      open.value = false
      triggerEl.value?.focus()
    }
  }
  document.addEventListener('keydown', onKey)
  const stop = watch(open, nv => {
    if (!nv) {
      document.removeEventListener('keydown', onKey)
      stop()
    }
  })
})
</script>

<template>
  <button ref="triggerEl" class="pill" :class="{ 'is-active': open || hiddenCount > 0 }"
          :aria-expanded="open" aria-haspopup="true" @click="toggleOpen()">
    列<span v-if="hiddenCount > 0" class="mono cp__badge">−{{ hiddenCount }}</span>
  </button>

  <Teleport to="body">
    <template v-if="open">
      <div class="cp__scrim" @click="open = false" />
      <div ref="panelEl" class="cp__panel"
           :style="{ top: pos.top + 'px', left: pos.left + 'px', width: PANEL_W + 'px' }"
           role="group" aria-label="选择显示的列">
        <label v-for="c in columns.toggleable" :key="c.key" class="cp__row">
          <input type="checkbox" :checked="columns.isOn(c.key)" @change="columns.toggle(c.key)" />
          <span class="t-xs">{{ c.label }}</span>
        </label>

        <!-- 勾了却没出现的列必须解释，否则看起来就是开关失灵 -->
        <p v-if="columns.suppressed.value.length" class="cp__note t-2xs dim-3">
          {{ columns.suppressed.value.map(c => c.label).join('、') }}
          因窗口太窄暂时隐藏，拉宽即可显示。
        </p>

        <div class="cp__foot">
          <button class="btn btn--xs" @click="columns.reset()">全部显示</button>
        </div>
      </div>
    </template>
  </Teleport>
</template>

<style scoped>
.cp__badge { margin-left: 6px; opacity: 0.75; }

/* 层级与 SelectMenu 一致：压过抽屉（47），低于确认对话框（50） */
.cp__scrim { position: fixed; inset: 0; z-index: 48; }
.cp__panel {
  position: fixed; z-index: 49;
  max-height: 70vh; overflow-y: auto;
  /* --bg-raised 这个变量根本不存在。未定义的自定义属性会让整条声明失效，
     面板就变成全透明的——底下的表格直接透上来，看着像渲染坏了。
     和 SelectMenu 用同一个 token。 */
  background: var(--bg-elevated);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-3);
  padding: 6px;
}

.cp__row {
  display: flex; align-items: center; gap: 8px;
  padding: 7px 8px; border-radius: var(--radius-sm);
  cursor: pointer; user-select: none;
}
.cp__row:hover { background: var(--bg-surface); }
.cp__row input { accent-color: var(--accent); width: 14px; height: 14px; cursor: pointer; }

.cp__note {
  padding: 6px 8px 2px;
  line-height: 1.5;
  border-top: 1px solid var(--border-subtle);
  margin-top: 4px;
}

.cp__foot {
  display: flex; justify-content: flex-end;
  padding: 6px 4px 2px;
  border-top: 1px solid var(--border-subtle);
  margin-top: 4px;
}
</style>
