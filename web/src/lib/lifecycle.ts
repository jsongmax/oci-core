import type { LifecycleState } from '@/types'

interface StateMeta {
  label: string
  /** CSS 变量名 */
  color: string
  /** 实心灯 = 稳定运行/过渡中，空心 = 已停止/已终止（§10.2 第二重编码） */
  solid: boolean
  /** 过渡态：唯一允许带循环动画的状态（§6.2） */
  transitional: boolean
}

export const LIFECYCLE: Record<LifecycleState, StateMeta> = {
  RUNNING:      { label: '运行中', color: 'var(--success)',    solid: true,  transitional: false },
  STOPPED:      { label: '已停止', color: 'var(--neutral)',    solid: false, transitional: false },
  PROVISIONING: { label: '创建中', color: 'var(--info)',       solid: true,  transitional: true },
  STARTING:     { label: '启动中', color: 'var(--accent)',     solid: true,  transitional: true },
  STOPPING:     { label: '关机中', color: 'var(--accent)',     solid: true,  transitional: true },
  TERMINATING:  { label: '终止中', color: 'var(--warning)',    solid: true,  transitional: true },
  TERMINATED:   { label: '已终止', color: 'var(--text-tertiary)', solid: false, transitional: false }
}

export const isTransitional = (s: LifecycleState) => LIFECYCLE[s].transitional

/** 同屏脉冲行上限（§7 性能红线），超出用静态徽章 + 计数汇总 */
export const MAX_CONCURRENT_PULSES = 12
