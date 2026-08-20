import type { Account } from '@/types'

/** 一个分组：account 为 null 表示"不分组"时的那个全量组。 */
export interface AccountGroup<T> {
  key: string
  account: Account | null
  rows: T[]
}

/**
 * 把带 accountId 的行按账号分组。
 *
 * enabled 为 false 时返回单个全量组，模板只需写一套循环——
 * 否则每张表都要写「分组时怎么渲染」和「不分组时怎么渲染」两遍。
 *
 * 分组顺序沿用 rows 的原始顺序（各页面已经按 账号 → 区域 → 名称 排过），
 * 不在这里重排：调用方排好的顺序是它自己的语义，这里再排一次只会打架。
 */
export function groupByAccount<T extends { accountId: string }>(
  rows: T[],
  enabled: boolean,
  accountById: (id: string) => Account
): AccountGroup<T>[] {
  if (!enabled) return [{ key: 'all', account: null, rows }]

  const byId = new Map<string, T[]>()
  for (const row of rows) {
    const list = byId.get(row.accountId)
    if (list) list.push(row)
    else byId.set(row.accountId, [row])
  }
  return [...byId.entries()].map(([id, list]) => ({
    key: id,
    account: accountById(id),
    rows: list
  }))
}
