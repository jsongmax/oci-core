/**
 * 界面多语言。
 *
 * 没有用 vue-i18n：字典按功能区拆成普通的 TS 对象，中英文放在同一个文件里，
 * 英文字典的类型就是 `typeof zh`——漏翻一个键、参数签名对不上，vue-tsc 直接报错，
 * 而不是等到运行时在界面上露出一个 key。带参数的文案写成函数，复数等语法差异
 * 也在函数里处理，不需要再学一套插值语法。
 *
 * 用法：模板与脚本里都直接读 `t.area.key`。`t` 是个代理，读取时才按当前语言取值，
 * 所以在渲染函数、computed 里读到的文案会随语言切换自动更新。
 * **不要**在模块顶层把 `t.xxx` 存进常量——那样只会拿到加载那一刻的语言。
 */
import { ref } from 'vue'
import { messages, type Messages } from './messages'

export type Locale = 'zh-CN' | 'en'

export const LOCALES: { value: Locale; label: string }[] = [
  { value: 'zh-CN', label: '简体中文' },
  { value: 'en', label: 'English' }
]

const STORAGE_KEY = 'ocicore.locale'

const isLocale = (v: unknown): v is Locale => v === 'zh-CN' || v === 'en'

/**
 * 首次打开时的语言：记住的选择 → 浏览器语言（中文系一律简体中文）→ 英文。
 * 存储可能被禁用（隐私模式），读写都要兜住。
 */
function detect(): Locale {
  try {
    const saved = localStorage.getItem(STORAGE_KEY)
    if (isLocale(saved)) return saved
  } catch { /* 存储不可用就按浏览器语言 */ }
  const langs = typeof navigator !== 'undefined' ? (navigator.languages ?? [navigator.language]) : []
  return langs.some(l => l?.toLowerCase().startsWith('zh')) ? 'zh-CN' : 'en'
}

export const locale = ref<Locale>(detect())

function applyDocument(l: Locale): void {
  if (typeof document === 'undefined') return
  document.documentElement.lang = l
}
applyDocument(locale.value)

export function setLocale(l: Locale): void {
  locale.value = l
  applyDocument(l)
  try { localStorage.setItem(STORAGE_KEY, l) } catch { /* 只是记不住，不影响使用 */ }
}

/** 当前语言的字典。见文件头的用法说明。 */
export const t: Messages = new Proxy({} as Messages, {
  get: (_target, key) => (messages[locale.value] as Record<string | symbol, unknown>)[key]
})

/** Intl 系列 API 用的语言标签。 */
export const intlLocale = (): string => locale.value

/** 数字的本地化写法，如 1,234.5。 */
export const fmtNumber = (v: number, opts?: Intl.NumberFormatOptions): string =>
  v.toLocaleString(intlLocale(), opts)

/** 日期时间的本地化写法，24 小时制。 */
export const fmtDateTime = (d: Date | string | number, opts?: Intl.DateTimeFormatOptions): string =>
  new Date(d).toLocaleString(intlLocale(), { hour12: false, ...opts })

export type { Messages }
