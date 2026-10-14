/** 配额/容量的语义色：≥90% warning，=100% danger（§6.7） */
export function usageTone(used: number, limit: number): string {
  if (!limit) return 'var(--text-tertiary)'
  const p = used / limit
  if (p >= 1) return 'var(--danger)'
  if (p >= 0.9) return 'var(--warning)'
  return 'var(--accent)'
}

export const pct = (used: number, limit: number) =>
  limit ? Math.min(100, Math.round((used / limit) * 100)) : 0

export const acctColor = (colorIndex: number) => `var(--acct-${colorIndex})`

export const shortRegion = (region: string) =>
  region.replace(/^(ap|us|eu|sa|ca)-/, '').replace(/-1$/, '')

export async function copy(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    const el = document.createElement('textarea')
    el.value = text
    document.body.appendChild(el)
    el.select()
    document.execCommand('copy')
    el.remove()
  }
}

/**
 * KPI count-up（§7 Tier 1.5），ease-decelerate 600ms。
 *
 * 后台标签页里 requestAnimationFrame 是冻结的，动画一帧都不会走，
 * 数字会一直停在 0——用户切回来看到的是"所有指标都是零"，
 * 而不是"动画还没开始"。所以页面不可见时直接落到终值。
 */
export function countUp(to: number, onTick: (v: number) => void, duration = 600): void {
  if (typeof document !== 'undefined' && document.hidden) {
    onTick(to)
    return
  }
  const start = performance.now()
  const step = (now: number) => {
    const p = Math.min(1, (now - start) / duration)
    onTick(Math.round(to * (1 - Math.pow(1 - p, 3))))
    if (p < 1) requestAnimationFrame(step)
  }
  requestAnimationFrame(step)
}

/**
 * 流量字节数，按 1024 进制换算。
 *
 * 用 1024 而不是 1000：OCI 的免费额度「10 TB」按 10240 GB 算，
 * 两边进制不一致时，快到上限的那几百 GB 会对不上。
 */
export function formatBytes(bytes: number): string {
  if (!bytes || bytes < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let i = 0
  let v = bytes
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toLocaleString('zh-CN', { maximumFractionDigits: v < 10 ? 2 : 1 })} ${units[i]}`
}

/** 以 GB 计的流量，复用 formatBytes 的单位选择。 */
export const formatGB = (gb: number) => formatBytes(gb * 1024 ** 3)
