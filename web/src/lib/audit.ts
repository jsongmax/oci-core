/**
 * 审计日志的显示规则。总览的「最近操作」与设置里的审计日志共用。
 *
 * 以前两页各写一份动作名对照表，新加的动作往往只登记了一边，另一边就显示成
 * account_check_auto 这样的原始键名。动作名统一登记在 i18n/messages/enums.ts。
 */
import { t } from '@/i18n'

/** 动作名按当前语言显示。未登记的动作原样显示，不至于变成空白。 */
export function actionLabel(action: string): string {
  return t.enums.audit[action] ?? (action.startsWith('instance_bulk_') ? t.enums.auditBulk : action)
}

/**
 * 系统自己做的例行动作。审计日志里照常保留，但不进总览的「最近操作」——
 * 每个账号几小时一次的凭据复查会把用户真正做过的事挤出那 8 条。
 */
const ROUTINE = new Set(['account_check_auto'])
export const isRoutineAction = (action: string) => ROUTINE.has(action)

/**
 * 操作对象的显示文本。OCID 只留尾巴：一整串既挤坏布局，也不该原样摆在总览上
 * （旧版本把控制台连接的 OCID 当作对象记了下来，这里一并兜住）。
 */
export function auditTarget(target: string): string {
  if (/^ocid1\./.test(target)) return 'ocid1…' + target.slice(-6)
  return target
}
