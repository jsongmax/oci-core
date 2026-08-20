/**
 * 永久免费（Always Free）额度。
 *
 * 与后端 internal/ociclient/freetier.go 一一对应，改一边必须改另一边——
 * 两份数字对不上会让界面和预设各说各话。
 *
 * **Oracle 会不打招呼地改这些数字。** 2026-06-15 起 Ampere A1 从
 * 4 OCPU / 24 GB 砍到 2 OCPU / 12 GB，没有公告、没有博客，只给用户发了
 * 邮件，并从 2026-08-18 起终止超出新限额的永久免费实例。所以：
 *
 * - 这些常量只用于提示文案，绝不用于判断"超没超"。
 *   真实上限一律以 /api/quota 返回的值为准——那是账号自己的数字。
 * - 看到下面的核对日期已经很久远，就该怀疑它。
 *
 * 核对日期：2026-08-18
 * 来源：https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm
 */
export const ALWAYS_FREE = {
  armOcpus: 2,
  armMemoryGB: 12,
  microInstances: 2,
  blockGB: 200,
  /** 上面这组数字的生效日，用于文案里说明"从哪天起"。 */
  since: '2026-06-15'
} as const

/** 永久免费 ARM 额度的一句话描述，例如「2 OCPU / 12 GB」。 */
export const armFreeText = `${ALWAYS_FREE.armOcpus} OCPU / ${ALWAYS_FREE.armMemoryGB} GB`
