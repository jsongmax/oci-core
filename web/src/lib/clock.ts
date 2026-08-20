import { ref, readonly } from 'vue'

/**
 * 全局时钟。相对时间（"2 分钟前"）读它而不是 Date.now()。
 *
 * 相对时间字符串一旦算出来就不会自己变。页面开着不动，"2 分钟前"
 * 能挂一整天——用户看到的是一个停摆的时钟，却以为那是当前状态。
 * 把当前时刻做成响应式的，所有相对时间就会随之重算。
 *
 * 30 秒一跳：相对时间的最小刻度是分钟，半分钟的误差看不出来，
 * 而更密的定时器纯属浪费。
 */
const TICK_MS = 30_000

const nowMs = ref(Date.now())

/** 页面不可见时不跳——后台标签页里没人在看，醒着只是白耗电。 */
function tick(): void {
  if (!document.hidden) nowMs.value = Date.now()
}

window.setInterval(tick, TICK_MS)

// 切回前台时立刻补一次。否则标签页在后台放了两小时，
// 回来第一眼看到的还是两小时前那批数字。
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) nowMs.value = Date.now()
})

export const now = readonly(nowMs)
