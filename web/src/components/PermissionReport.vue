<script setup lang="ts">
/**
 * 权限自检报告：按功能列出可用 / 缺失 / 无法判断。
 *
 * 缺失项直接展开修复所需的策略语句——这是用户唯一需要动手的地方；
 * 每项所需的权限明细与依据收在「详情」里，供核对。
 */
import { t } from '@/i18n'
import { computed, ref } from 'vue'
import { relativeTime } from '@/lib/adapt'
import type { PermItemDTO, PermReportDTO, PermStatusDTO } from '@/api'
import CheckList from '@/components/CheckList.vue'
import CodeBlock from '@/components/CodeBlock.vue'
import type { CheckItem } from '@/types'

const props = defineProps<{
  report: PermReportDTO | null
  loading?: boolean
  /** 没有报告且不在加载时显示的提示 */
  emptyText: string
}>()

const ICON: Record<PermStatusDTO, string> = { ok: '✔', missing: '✕', unknown: '?' }
const CATEGORIES = ['compute', 'network', 'storage', 'account'] as const

const open = ref<Set<string>>(new Set())
function toggle(key: string) {
  const next = new Set(open.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  open.value = next
}

const groups = computed(() => {
  const items = props.report?.items ?? []
  return CATEGORIES
    .map(c => ({ key: c, items: items.filter(i => i.category === c) }))
    .filter(g => g.items.length > 0)
})

const sourceNotes = computed<CheckItem[]>(() => {
  const r = props.report
  if (!r) return []
  // 有具体说明时只列说明；概括句与它们意思重复。
  const notes = r.policyNote ?? []
  const out: CheckItem[] = notes.length
    ? notes.map(n => ({ tone: 'warn' as const, text: n }))
    : [{ tone: r.policy === 'ok' ? 'info' : 'warn', text: t.perm.policy[r.policy] }]
  if (r.groups.length) out.push({ tone: 'info', text: t.perm.groups(r.groups.join(', ')) })
  return out
})

/**
 * 读不到策略、也没有探测覆盖时，每一项的原因都是同一句话，顶部已经说过了，不再逐项重复。
 */
function showReason(item: PermItemDTO): boolean {
  if (!item.reason || item.status !== 'unknown') return false
  return !(props.report?.policy === 'unavailable' && !item.needs.some(n => n.source === 'probe'))
}

function basis(n: PermItemDTO['needs'][number]): string {
  if (n.source === 'probe') return n.reason || t.perm.sourceProbe(n.via ?? '')
  if (n.source === 'policy' && n.via) return n.via
  return n.reason || t.perm.sourceNone
}
</script>

<template>
  <div v-if="!report" class="perm-empty">
    <CheckList :items="[{ tone: 'info', text: loading ? t.perm.running : emptyText }]" />
  </div>

  <div v-else class="perm" :class="{ 'is-stale': loading }">
    <div class="perm__summary">
      <span class="pill pill--ok">{{ report.summary.ok }} {{ t.perm.status.ok }}</span>
      <span class="pill pill--missing">{{ report.summary.missing }} {{ t.perm.status.missing }}</span>
      <span class="pill pill--unknown">{{ report.summary.unknown }} {{ t.perm.status.unknown }}</span>
      <span class="perm__when t-2xs dim-3">{{ t.perm.checkedAt(relativeTime(report.checkedAt)) }}</span>
    </div>

    <CheckList :items="sourceNotes" />

    <section v-for="g in groups" :key="g.key" class="perm__group">
      <h4 class="perm__cat">{{ t.perm.category[g.key] }}</h4>
      <div v-for="item in g.items" :key="item.key" class="perm__item" :class="`is-${item.status}`">
        <div class="perm__row">
          <span class="perm__icon" aria-hidden="true">{{ ICON[item.status] }}</span>
          <span class="perm__label">{{ item.label }}</span>
          <span class="perm__status">{{ t.perm.status[item.status] }}</span>
          <button class="btn btn--sm perm__toggle" :aria-expanded="open.has(item.key)"
                  @click="toggle(item.key)">{{ open.has(item.key) ? t.perm.hide : t.perm.details }}</button>
        </div>
        <p v-if="showReason(item)" class="perm__reason">{{ item.reason }}</p>

        <div v-if="item.status === 'missing' && item.fix?.length" class="perm__fix">
          <p class="perm__fix-title">{{ t.perm.fix }}</p>
          <CodeBlock copyable :code="item.fix.join('\n')" />
        </div>

        <table v-if="open.has(item.key)" class="perm__needs">
          <thead>
            <tr><th>{{ t.perm.colPerm }}</th><th>{{ t.perm.colRequire }}</th><th>{{ t.perm.colSource }}</th></tr>
          </thead>
          <tbody>
            <tr v-for="n in item.needs" :key="n.perm" :class="`is-${n.status}`">
              <td class="mono"><span class="perm__icon">{{ ICON[n.status] }}</span> {{ n.perm }}</td>
              <td class="mono">{{ n.verb }} {{ n.type }}</td>
              <td class="perm__basis" :class="{ mono: n.source === 'policy' && n.via }">{{ basis(n) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <p v-if="report.summary.missing" class="perm__foot t-2xs dim">{{ t.perm.fixLocation }}</p>
    <p class="perm__foot t-2xs dim-3">{{ t.perm.disclaimer }}</p>
  </div>
</template>

<style scoped>
.perm { padding: 4px 0 12px; transition: opacity 0.2s; }
.perm.is-stale { opacity: 0.55; }
.perm__summary { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; padding: 12px 16px 0; }
.pill { font-size: 11px; line-height: 18px; padding: 0 8px; border-radius: var(--radius-full); font-weight: 500; }
.pill--ok { color: var(--success); background: var(--success-soft); }
.pill--missing { color: var(--danger); background: var(--danger-soft); }
.pill--unknown { color: var(--text-secondary); background: var(--bg-inset); }
.perm__when { margin-left: auto; }

.perm__group { padding: 0 16px; }
.perm__cat { margin: 12px 0 6px; font-size: 11px; font-weight: 600; color: var(--text-tertiary); text-transform: uppercase; letter-spacing: 0.04em; }
.perm__item { border-top: 1px solid var(--border-subtle); padding: 7px 0; }
.perm__row { display: flex; align-items: center; gap: 9px; font-size: 12px; }
.perm__icon { flex: 0 0 auto; width: 12px; text-align: center; color: var(--c); font-weight: 600; }
.perm__label { flex: 1 1 auto; min-width: 0; color: var(--text-primary); }
.perm__status { flex: 0 0 auto; font-size: 11px; color: var(--c); }
.perm__toggle { flex: 0 0 auto; }
.is-ok { --c: var(--success); }
.is-missing { --c: var(--danger); }
.is-unknown { --c: var(--text-tertiary); }
.perm__item.is-missing .perm__label { color: var(--danger); font-weight: 500; }

.perm__reason { margin: 4px 0 0 21px; font-size: 11px; color: var(--text-secondary); }
.perm__fix { margin: 6px 0 0 21px; }
.perm__fix-title { margin: 0; font-size: 11px; color: var(--text-secondary); }
.perm__fix :deep(.code) { margin: 6px 0 0; }

.perm__needs { width: calc(100% - 21px); margin: 8px 0 2px 21px; border-collapse: collapse; font-size: 11px; table-layout: fixed; }
.perm__needs th { text-align: left; font-weight: 500; color: var(--text-tertiary); padding: 3px 6px 3px 0; }
.perm__needs td { padding: 3px 6px 3px 0; color: var(--text-secondary); vertical-align: top; word-break: break-word; }
.perm__needs th:nth-child(1) { width: 38%; }
.perm__needs th:nth-child(2) { width: 27%; }
.perm__needs tr.is-missing td { color: var(--danger); }
.perm__basis { font-size: 11px; }

.perm__foot { margin: 10px 16px 0; }

@media (max-width: 560px) {
  .perm__needs th:nth-child(2), .perm__needs td:nth-child(2) { display: none; }
  .perm__needs th:nth-child(1) { width: 50%; }
}
</style>
