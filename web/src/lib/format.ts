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
