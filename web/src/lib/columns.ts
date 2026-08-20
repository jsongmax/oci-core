import { computed, ref, readonly, type ComputedRef } from 'vue'

/**
 * 表格列的显示控制。
 *
 * 这里同时管两件原本分开的事：用户手动隐藏的列，和视口太窄时自动隐藏的列。
 * 合并的原因是 grid-template-columns 必须和实际渲染出来的单元格数量严格
 * 一致——差一个就整行错位。原先自动隐藏靠 CSS 媒体查询改 .cols 的模板，
 * 一旦再叠加用户的选择，两套规则各改各的，对不上就会串列。
 * 现在只有一个真相来源：visible()，模板和 v-if 都从它派生。
 */
export interface ColumnDef {
  key: string
  label: string
  /** grid-template-columns 里的一段 */
  width: string
  /** 固定列不可隐藏。名称、状态、操作属于这类——藏了表格就没法用了 */
  fixed?: boolean
  /** 视口窄于这个宽度时自动隐藏，与用户的选择取交集 */
  minViewport?: number
}

/* ---------- 视口宽度 ---------- */

const vw = ref(window.innerWidth)

// 单个模块级监听器，不是每个表格各挂一个。resize 触发极密集，
// 每个组件挂一份会让滚动条拖动时跑几十个回调。
let raf = 0
window.addEventListener('resize', () => {
  if (raf) return
  raf = window.requestAnimationFrame(() => {
    raf = 0
    vw.value = window.innerWidth
  })
})

export const viewportWidth = readonly(vw)

/* ---------- 持久化 ---------- */

function loadHidden(storageKey: string): Set<string> {
  try {
    const raw = localStorage.getItem(storageKey)
    if (!raw) return new Set()
    const arr = JSON.parse(raw)
    return Array.isArray(arr) ? new Set(arr.filter(x => typeof x === 'string')) : new Set()
  } catch {
    // 存的内容坏了就当没配过。为一个显示偏好抛异常不值得。
    return new Set()
  }
}

export interface ColumnState {
  /** 当前真正要渲染的列，顺序即显示顺序 */
  visible: ComputedRef<ColumnDef[]>
  /** 直接绑到 style.gridTemplateColumns */
  template: ComputedRef<string>
  /** 某列此刻是否渲染。单元格的 v-if 用它 */
  has: (key: string) => boolean
  /** 用户可以切换的列（排除固定列） */
  toggleable: ColumnDef[]
  /** 用户是否勾选了这一列。注意与 has 不同：勾了但视口太窄仍然不渲染 */
  isOn: (key: string) => boolean
  toggle: (key: string) => void
  reset: () => void
  /** 被视口宽度压掉的列数，用来在选择器里解释"勾了怎么没出来" */
  suppressed: ComputedRef<ColumnDef[]>
}

/**
 * 建立一张表的列状态。storageKey 决定偏好存在哪个 localStorage 键下。
 */
export function useColumns(storageKey: string, defs: ColumnDef[]): ColumnState {
  const hidden = ref<Set<string>>(loadHidden(storageKey))

  const persist = () => {
    try {
      localStorage.setItem(storageKey, JSON.stringify([...hidden.value]))
    } catch {
      // 隐私模式下 localStorage 会抛。偏好丢了就丢了，不该影响使用。
    }
  }

  const visible = computed(() =>
    defs.filter(c =>
      c.fixed || (!hidden.value.has(c.key) && vw.value >= (c.minViewport ?? 0))))

  const suppressed = computed(() =>
    defs.filter(c =>
      !c.fixed && !hidden.value.has(c.key) && vw.value < (c.minViewport ?? 0)))

  const visibleKeys = computed(() => new Set(visible.value.map(c => c.key)))

  return {
    visible,
    template: computed(() => visible.value.map(c => c.width).join(' ')),
    has: (key: string) => visibleKeys.value.has(key),
    toggleable: defs.filter(c => !c.fixed),
    isOn: (key: string) => !hidden.value.has(key),
    toggle(key: string) {
      const next = new Set(hidden.value)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      // 换一个 Set 而不是原地增删：Vue 的响应式追踪不到 Set 的原地修改。
      hidden.value = next
      persist()
    },
    reset() {
      hidden.value = new Set()
      persist()
    },
    suppressed
  }
}
