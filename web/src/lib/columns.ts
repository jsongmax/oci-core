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
  /** 拖动调宽的下限（px），默认 60。设为 0 表示这一列不能拖 */
  minWidth?: number
}

/** 拖宽的上限。再宽只会把右边的列挤出表格，没有意义 */
export const MAX_COLUMN_WIDTH = 600
const DEFAULT_MIN_WIDTH = 60

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

function loadOrder(storageKey: string): string[] {
  try {
    const raw = localStorage.getItem(storageKey)
    const arr = raw ? JSON.parse(raw) : []
    return Array.isArray(arr) ? arr.filter(x => typeof x === 'string') : []
  } catch {
    return []
  }
}

function loadWidths(storageKey: string): Record<string, number> {
  try {
    const raw = localStorage.getItem(storageKey)
    const obj = raw ? JSON.parse(raw) : {}
    if (!obj || typeof obj !== 'object' || Array.isArray(obj)) return {}
    const out: Record<string, number> = {}
    for (const [k, v] of Object.entries(obj)) {
      if (typeof v === 'number' && Number.isFinite(v) && v > 0) out[k] = v
    }
    return out
  } catch {
    return {}
  }
}

function save(storageKey: string, value: unknown) {
  try {
    localStorage.setItem(storageKey, JSON.stringify(value))
  } catch {
    // 隐私模式下 localStorage 会抛。偏好丢了就丢了，不该影响使用。
  }
}

export interface ColumnState {
  /** 当前真正要渲染的列，顺序即显示顺序 */
  visible: ComputedRef<ColumnDef[]>
  /** 直接绑到 style.gridTemplateColumns */
  template: ComputedRef<string>
  /** 某列此刻是否渲染。单元格的 v-if 用它 */
  has: (key: string) => boolean
  /**
   * 某列在 visible 里的位置，绑到单元格的 CSS order。
   *
   * 模板里单元格的书写顺序是固定的，调列序不去改模板，而是靠 grid 的
   * order 让它们按用户的顺序排进 grid-template-columns 的各个轨道。
   */
  pos: (key: string) => number
  /** 用户可以切换的列（排除固定列），按定义顺序 */
  toggleable: ColumnDef[]
  /** 同上，但按用户调整后的顺序。列选择器按它排 */
  ordered: ComputedRef<ColumnDef[]>
  /** 用户是否勾选了这一列。注意与 has 不同：勾了但视口太窄仍然不渲染 */
  isOn: (key: string) => boolean
  toggle: (key: string) => void
  /** 把 key 挪到 target 所在的位置（拖拽与上移/下移共用） */
  moveTo: (key: string, target: string) => void
  /** 这一列能不能拖宽 */
  resizable: (key: string) => boolean
  /** 用户拖过的宽度（px），没拖过为 undefined */
  widthOf: (key: string) => number | undefined
  /**
   * 设定列宽，会夹到 [minWidth, MAX_COLUMN_WIDTH]。
   * 拖动过程中每帧都会调，persist=false 只改内存，松手时再落盘一次。
   */
  setWidth: (key: string, px: number, persist?: boolean) => void
  /** 恢复某一列的默认宽度（双击拖动条） */
  resetWidth: (key: string) => void
  /** 恢复默认：全部显示、默认顺序、默认列宽 */
  reset: () => void
  /** 被视口宽度压掉的列数，用来在选择器里解释"勾了怎么没出来" */
  suppressed: ComputedRef<ColumnDef[]>
}

/**
 * 建立一张表的列状态。storageKey 决定偏好存在哪个 localStorage 键下，
 * 列序存在 `${storageKey}.order`。
 */
export function useColumns(storageKey: string, defs: ColumnDef[]): ColumnState {
  const hidden = ref<Set<string>>(loadHidden(storageKey))
  const toggleable = defs.filter(c => !c.fixed)
  const order = ref<string[]>(loadOrder(storageKey + '.order'))
  const widths = ref<Record<string, number>>(loadWidths(storageKey + '.widths'))
  const byKey = new Map(defs.map(c => [c.key, c]))
  const minOf = (key: string) => byKey.get(key)?.minWidth ?? DEFAULT_MIN_WIDTH

  /**
   * 可调列按用户顺序排；存的顺序里没有的列（新加的列）按定义顺序补在后面，
   * 已经不存在的键直接忽略。
   */
  const ordered = computed(() => {
    const byKey = new Map(toggleable.map(c => [c.key, c]))
    const seen = new Set<string>()
    const out: ColumnDef[] = []
    for (const k of order.value) {
      const c = byKey.get(k)
      if (c && !seen.has(k)) { out.push(c); seen.add(k) }
    }
    for (const c of toggleable) if (!seen.has(c.key)) out.push(c)
    return out
  })

  /** 固定列留在定义里的位置（选择/状态/名称在前，操作在后），可调列依次填进其余位置。 */
  const arranged = computed(() => {
    let idx = 0
    return defs.map(c => (c.fixed ? c : ordered.value[idx++]))
  })

  const visible = computed(() =>
    arranged.value.filter(c =>
      c.fixed || (!hidden.value.has(c.key) && vw.value >= (c.minViewport ?? 0))))

  const suppressed = computed(() =>
    toggleable.filter(c => !hidden.value.has(c.key) && vw.value < (c.minViewport ?? 0)))

  const positions = computed(() => new Map(visible.value.map((c, i) => [c.key, i])))

  return {
    visible,
    // 拖过的列改成固定像素；没拖过的照旧用定义里的值，fr 列继续吃剩余空间。
    template: computed(() =>
      visible.value.map(c => (widths.value[c.key] ? `${widths.value[c.key]}px` : c.width)).join(' ')),
    has: (key: string) => positions.value.has(key),
    pos: (key: string) => positions.value.get(key) ?? 0,
    toggleable,
    ordered,
    isOn: (key: string) => !hidden.value.has(key),
    toggle(key: string) {
      const next = new Set(hidden.value)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      // 换一个 Set 而不是原地增删：Vue 的响应式追踪不到 Set 的原地修改。
      hidden.value = next
      save(storageKey, [...next])
    },
    moveTo(key: string, target: string) {
      if (key === target) return
      const keys = ordered.value.map(c => c.key)
      const from = keys.indexOf(key)
      const to = keys.indexOf(target)
      if (from < 0 || to < 0) return
      keys.splice(from, 1)
      keys.splice(to, 0, key)
      order.value = keys
      save(storageKey + '.order', keys)
    },
    resizable: (key: string) => minOf(key) > 0,
    widthOf: (key: string) => widths.value[key],
    setWidth(key: string, px: number, persist = true) {
      if (minOf(key) <= 0) return
      const w = Math.round(Math.min(MAX_COLUMN_WIDTH, Math.max(minOf(key), px)))
      if (widths.value[key] !== w) widths.value = { ...widths.value, [key]: w }
      if (persist) save(storageKey + '.widths', widths.value)
    },
    resetWidth(key: string) {
      if (!(key in widths.value)) return
      const { [key]: _, ...rest } = widths.value
      widths.value = rest
      save(storageKey + '.widths', rest)
    },
    reset() {
      hidden.value = new Set()
      order.value = []
      widths.value = {}
      save(storageKey, [])
      save(storageKey + '.order', [])
      save(storageKey + '.widths', {})
    },
    suppressed
  }
}
