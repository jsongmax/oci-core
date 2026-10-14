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

/**
 * 网络速率。输入是每秒字节数（OCI 流量指标取 rate 后的值），输出按比特、1000 进制，
 * 与运营商和带宽规格的写法一致：1 Mbps = 1,000,000 bit/s。
 */
export function formatBitRate(bytesPerSec: number): string {
  return formatBits((bytesPerSec || 0) * 8)
}

/**
 * 同上，但输入已经是每秒比特数。图表直接用比特作数据，坐标轴才能落在
 * 2 Mbps、4 Mbps 这样的整数刻度上；用字节作数据再换算，刻度会变成 6.4 Mbps。
 */
export function formatBits(bitsPerSec: number): string {
  const bits = bitsPerSec || 0
  if (bits <= 0) return '0 bps'
  const units = ['bps', 'Kbps', 'Mbps', 'Gbps']
  let i = 0
  let v = bits
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000
    i++
  }
  return `${v.toLocaleString('zh-CN', { maximumFractionDigits: v < 10 ? 2 : 1 })} ${units[i]}`
}

/** 百分比，保留一位小数。 */
export const formatPercent = (v: number) =>
  `${(v || 0).toLocaleString('zh-CN', { maximumFractionDigits: 1 })}%`

/**
 * 为一组字节数挑一个统一的显示单位。
 *
 * 图表要把数据先换算到这个单位再交给坐标轴：坐标轴按十进制找整数刻度，
 * 直接喂字节数，1024 进制换算后刻度会变成 2.75 GB、5.53 GB 这种数。
 */
export function byteScale(maxBytes: number): { factor: number; unit: string; format: (v: number) => string } {
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let i = 0
  while (i < units.length - 1 && maxBytes >= 1024 ** (i + 1)) i++
  const unit = units[i]
  return {
    factor: 1024 ** i,
    unit,
    format: (v: number) => `${(v || 0).toLocaleString('zh-CN', { maximumFractionDigits: 2 })} ${unit}`
  }
}
