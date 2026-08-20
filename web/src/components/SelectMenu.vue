<script setup lang="ts">
/**
 * 自绘下拉。
 *
 * 存在的理由只有一个：原生 <select> 展开后的那张列表是浏览器画的，CSS 碰不到。
 * 收起态可以用 appearance: none 收拾干净，一展开还是系统样式——深色主题下
 * 尤其突兀，弹出来一片浅色。
 *
 * 弹层 Teleport 到 body 并用 fixed 定位：它的宿主 SectionCard 是
 * overflow: hidden，绝对定位会被整齐地裁掉，而且裁得看不出痕迹
 * （这个坑本项目已经踩过一次，见 InstanceRowActions）。
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'

export interface SelectOption {
  value: string
  label: string
  /** 可选的前置圆点颜色，用来带上账号身份色 */
  dot?: string
}
export interface SelectGroup {
  label: string
  options: SelectOption[]
}

const props = withDefaults(defineProps<{
  modelValue: string
  groups: SelectGroup[]
  placeholder?: string
  disabled?: boolean
  /** 触发器最小宽度 */
  minWidth?: number
  /** 无障碍名称。触发器是 button 而不是 select，<label for> 挂不上去。 */
  ariaLabel?: string
}>(), { placeholder: '请选择', disabled: false, minWidth: 240, ariaLabel: '' })

const emit = defineEmits<{ 'update:modelValue': [string] }>()

const open = ref(false)
const triggerEl = ref<HTMLButtonElement>()
const panelEl = ref<HTMLElement>()
const pos = ref({ top: 0, left: 0, width: 0 })

/** 摊平后的可选项，供键盘上下移动用。 */
const flat = computed(() => props.groups.flatMap(g => g.options))
const selected = computed(() => flat.value.find(o => o.value === props.modelValue))
const activeIndex = ref(-1)

const GAP = 6
const MARGIN = 8

function place() {
  const t = triggerEl.value
  const p = panelEl.value
  if (!t || !p) return
  const r = t.getBoundingClientRect()
  const h = p.offsetHeight

  let top = r.bottom + GAP
  // 下面放不下就翻到上方。列表长的时候这一步很关键，
  // 否则选项会掉出视口，用户只能看到前几条。
  if (top + h > window.innerHeight - MARGIN) {
    top = Math.max(MARGIN, r.top - GAP - h)
  }
  pos.value = { top, left: r.left, width: r.width }
}

async function toggle() {
  if (props.disabled) return
  if (open.value) {
    open.value = false
    return
  }
  open.value = true
  activeIndex.value = Math.max(0, flat.value.findIndex(o => o.value === props.modelValue))
  await nextTick()
  place()
}

function pick(value: string) {
  emit('update:modelValue', value)
  open.value = false
  triggerEl.value?.focus()
}

function onKeydown(e: KeyboardEvent) {
  if (!open.value) return
  if (e.key === 'Escape') {
    open.value = false
    triggerEl.value?.focus()
    return
  }
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault()
    const step = e.key === 'ArrowDown' ? 1 : -1
    const n = flat.value.length
    if (n === 0) return
    activeIndex.value = (activeIndex.value + step + n) % n
    return
  }
  if (e.key === 'Enter' || e.key === ' ') {
    e.preventDefault()
    const opt = flat.value[activeIndex.value]
    if (opt) pick(opt.value)
  }
}

// 弹层脱离了文档流，滚动与缩放时得自己跟上。
// capture 是必须的：真正滚动的是内容区，不是 window。
function onViewportChange() {
  if (open.value) place()
}

watch(open, isOpen => {
  const fn = isOpen ? window.addEventListener : window.removeEventListener
  fn('scroll', onViewportChange, true)
  fn('resize', onViewportChange)
  fn('keydown', onKeydown as EventListener)
})

onBeforeUnmount(() => {
  window.removeEventListener('scroll', onViewportChange, true)
  window.removeEventListener('resize', onViewportChange)
  window.removeEventListener('keydown', onKeydown as EventListener)
})
</script>

<template>
  <button ref="triggerEl" type="button" class="sm__trigger" :class="{ 'is-open': open }"
          :style="{ minWidth: `${minWidth}px` }" :disabled="disabled"
          :aria-label="ariaLabel || undefined"
          aria-haspopup="listbox" :aria-expanded="open" @click="toggle()">
    <span v-if="selected?.dot" class="sm__dot" :style="{ background: selected.dot }" />
    <span class="sm__label" :class="{ 'is-placeholder': !selected }">
      {{ selected?.label ?? placeholder }}
    </span>
    <span class="sm__caret" aria-hidden="true">▾</span>
  </button>

  <Teleport to="body">
    <div v-if="open" class="sm__scrim" @click="open = false" />
    <div v-if="open" ref="panelEl" class="sm__panel" role="listbox"
         :style="{ top: `${pos.top}px`, left: `${pos.left}px`, minWidth: `${pos.width}px` }">
      <template v-for="g in groups" :key="g.label">
        <div v-if="g.label" class="sm__group">{{ g.label }}</div>
        <button v-for="o in g.options" :key="o.value" type="button" class="sm__option"
                role="option" :aria-selected="o.value === modelValue"
                :class="{ 'is-selected': o.value === modelValue,
                          'is-active': flat[activeIndex]?.value === o.value }"
                @click="pick(o.value)" @mouseenter="activeIndex = flat.indexOf(o)">
          <span v-if="o.dot" class="sm__dot" :style="{ background: o.dot }" />
          <span class="sm__option-label">{{ o.label }}</span>
          <span v-if="o.value === modelValue" class="sm__check">✓</span>
        </button>
      </template>
      <p v-if="flat.length === 0" class="sm__empty">没有可选项</p>
    </div>
  </Teleport>
</template>

<style scoped>
.sm__trigger {
  display: inline-flex; align-items: center; gap: 8px;
  height: 32px; padding: 0 10px; max-width: 100%;
  border: 1px solid var(--border-default); border-radius: var(--radius-md);
  background: var(--bg-surface); color: var(--text-primary);
  font-size: 12px; font-family: inherit; cursor: pointer;
  transition: border-color var(--dur-fast);
}
.sm__trigger:hover:not([disabled]) { border-color: var(--border-strong); }
.sm__trigger.is-open { border-color: var(--accent); }
.sm__trigger[disabled] { opacity: 0.5; cursor: not-allowed; }

.sm__dot { width: 7px; height: 7px; border-radius: var(--radius-full); flex: 0 0 auto; }
.sm__label { flex: 1 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; text-align: left; }
.sm__label.is-placeholder { color: var(--text-tertiary); }
.sm__caret { flex: 0 0 auto; font-size: 10px; color: var(--text-tertiary); }

/* 层级要压过抽屉（47）。面板 Teleport 到 body，抽屉也是 fixed，
   低于它就会被整个盖住——面板确实展开了，只是看不见，点一下还会被自己的
   遮罩关掉，表现出来就是「下拉框点不动」。
   上限是确认对话框（50）：弹确认时该由对话框盖住下拉，不能反过来。 */
.sm__scrim { position: fixed; inset: 0; z-index: 48; }
.sm__panel {
  position: fixed; z-index: 49; max-height: 320px; overflow-y: auto;
  padding: 6px; border-radius: var(--radius-md);
  border: 1px solid var(--border-default); background: var(--bg-elevated);
  box-shadow: var(--shadow-3);
}
.sm__group {
  padding: 6px 10px 4px; font-size: 10px; font-weight: 600;
  color: var(--text-tertiary); letter-spacing: 0.03em;
}
.sm__option {
  display: flex; align-items: center; gap: 8px; width: 100%;
  min-height: 32px; padding: 0 10px; border: 0; border-radius: var(--radius-sm);
  background: transparent; color: var(--text-primary);
  font-size: 12px; font-family: inherit; text-align: left; cursor: pointer;
}
.sm__option.is-active { background: var(--bg-hover); }
.sm__option.is-selected { color: var(--accent); }
.sm__option-label { flex: 1 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sm__check { flex: 0 0 auto; font-size: 11px; }
.sm__empty { padding: 12px 10px; font-size: 12px; color: var(--text-tertiary); }
</style>
