import type { LifecycleState } from '@/types'
import { t } from '@/i18n'

interface StateMeta {
  /** 按当前语言取，读的时候才求值 */
  readonly label: string
  /** CSS 变量名 */
  color: string
  /** 实心灯 = 稳定运行/过渡中，空心 = 已停止/已终止（§10.2 第二重编码） */
  solid: boolean
  /** 过渡态：唯一允许带循环动画的状态（§6.2） */
  transitional: boolean
}

export const LIFECYCLE: Record<LifecycleState, StateMeta> = {
  RUNNING:      { get label() { return t.enums.lifecycle.RUNNING }, color: 'var(--success)',    solid: true,  transitional: false },
  STOPPED:      { get label() { return t.enums.lifecycle.STOPPED }, color: 'var(--neutral)',    solid: false, transitional: false },
  PROVISIONING: { get label() { return t.enums.lifecycle.PROVISIONING }, color: 'var(--info)',       solid: true,  transitional: true },
  STARTING:     { get label() { return t.enums.lifecycle.STARTING }, color: 'var(--accent)',     solid: true,  transitional: true },
  STOPPING:     { get label() { return t.enums.lifecycle.STOPPING }, color: 'var(--accent)',     solid: true,  transitional: true },
  TERMINATING:  { get label() { return t.enums.lifecycle.TERMINATING }, color: 'var(--warning)',    solid: true,  transitional: true },
  TERMINATED:   { get label() { return t.enums.lifecycle.TERMINATED }, color: 'var(--text-tertiary)', solid: false, transitional: false }
}

export const isTransitional = (s: LifecycleState) => LIFECYCLE[s].transitional

/** 同屏脉冲行上限（§7 性能红线），超出用静态徽章 + 计数汇总 */
export const MAX_CONCURRENT_PULSES = 12
