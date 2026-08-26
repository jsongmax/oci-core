<script setup lang="ts">
/** §4.3 账号管理：卡片网格 + 四种连通性状态 */
import { computed, ref, watch } from 'vue'
import { useStore } from '@/store'
import { acctColor, pct, usageTone } from '@/lib/format'
import { accountAgeText, accountStatusText, trialDaysLeft } from '@/lib/adapt'
import type { Account } from '@/types'
import MaskedText from '@/components/MaskedText.vue'
import EmptyState from '@/components/EmptyState.vue'

const { state, openDrawer } = useStore()

const tierLabel: Record<Account['tier'], string> = {
  trial: '试用号',
  paid: '升级号',
  // 读不到订阅信息时不猜。写"升级号"会让用户以为资源是稳的,
  // 写"试用号"又会平白吓人一跳。
  unknown: '类型未知'
}

/**
 * 试用到期提示。
 *
 * 试用期内的配额远高于永久免费额度,到期那天会被打回永久免费额度,
 * 超出的实例会被 Oracle 回收。这行字就是提前把那一天摆在用户眼前。
 */
function trialText(a: Account): string {
  const d = trialDaysLeft(a)
  if (d === null) return ''
  if (d < 0) return '试用已到期'
  if (d === 0) return '试用今日到期'
  return `试用 ${d} 天后到期`
}

function trialTone(a: Account): string {
  const d = trialDaysLeft(a)
  if (d === null) return 'var(--text-secondary)'
  if (d <= 0) return 'var(--danger)'
  if (d <= 7) return 'var(--warning)'
  return 'var(--text-secondary)'
}

const lampTone: Record<Account['status'], string> = {
  ok: 'var(--success)', checking: 'var(--info)', error: 'var(--danger)', disabled: 'var(--neutral)'
}
const checkIcon: Record<Account['status'], string> = { ok: '✔', checking: '◍', error: '✕', disabled: '○' }

const summary = computed(() => {
  const err = state.accounts.filter(a => a.status === 'error').length
  const off = state.accounts.filter(a => a.status === 'disabled').length
  return `${state.accounts.length} 个 Oracle 租户 · ${err} 个认证失败 · ${off} 个已禁用`
})

const instancesOf = (id: string) => state.instances.filter(i => i.accountId === id && i.state !== 'TERMINATED')

/**
 * 卡片 / 列表两种视图。
 *
 * 卡片适合三五个账号，一眼看清每个的状态；账号多起来之后，
 * 一屏放不下几张卡，横向比较（谁的配额快满了、谁的试用先到期）
 * 反而更难。列表按行对齐，扫起来快得多。
 *
 * 存进 localStorage：视图偏好是"这个人习惯怎么看"，
 * 不该每次刷新都退回默认。
 */
type AccountView = 'card' | 'list'
const view = ref<AccountView>(
  (localStorage.getItem('oci.accountView') as AccountView) ?? 'card'
)
watch(view, v => localStorage.setItem('oci.accountView', v))
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">账号</h1>
        <p class="page-sub">{{ summary }}</p>
      </div>
      <div class="head-actions">
        <div class="segmented" role="group" aria-label="视图切换">
          <button class="segmented__btn" :class="{ 'is-on': view === 'card' }"
                  :aria-pressed="view === 'card'" @click="view = 'card'">▦ 卡片</button>
          <button class="segmented__btn" :class="{ 'is-on': view === 'list' }"
                  :aria-pressed="view === 'list'" @click="view = 'list'">☰ 列表</button>
        </div>
        <button class="btn btn--primary" @click="openDrawer({ kind: 'add-account' })">添加账号</button>
      </div>
    </header>

    <EmptyState v-if="!state.accounts.length" icon="⬡" title="还没有接入任何 Oracle 账号"
                sub="接入后即可统一管理所有实例" action-label="添加第一个账号"
                @action="openDrawer({ kind: 'add-account' })" />

    <div v-else-if="view === 'card'" class="grid">
      <article v-for="a in state.accounts" :key="a.id" class="card acct"
               :class="{ 'is-error': a.status === 'error', 'is-disabled': a.status === 'disabled' }"
               :style="{ '--c': acctColor(a.colorIndex) }"
               tabindex="0" @click="openDrawer({ kind: 'account', id: a.id, tab: '概览' })"
               @keydown.enter="openDrawer({ kind: 'account', id: a.id, tab: '概览' })">
        <span class="acct__bar" />
        <div class="acct__body">
          <p v-if="a.status === 'error'" class="acct__errbar">凭据失效 · 该账号下的资源暂不可操作</p>

          <header class="acct__head">
            <span class="acct__lamp" :style="{ '--l': lampTone[a.status] }"
                  :class="{ 'is-solid': a.status !== 'disabled', 'is-pulsing': a.status === 'checking' || a.status === 'error' }" />
            <h2 class="acct__alias">{{ a.alias }}</h2>
            <span class="acct__code mono">{{ a.code }}</span>
            <button class="acct__menu" aria-label="账号菜单"
                    @click.stop="openDrawer({ kind: 'account', id: a.id, tab: '密钥' })">⋯</button>
          </header>

          <p class="acct__mail"><MaskedText :value="a.email" kind="email" /></p>
          <p class="acct__ocid mono">ocid1.tenancy…<MaskedText :value="a.tenancyTail" /></p>

          <div class="acct__tier">
            <span class="acct__tag" :class="`is-${a.tier}`">{{ tierLabel[a.tier] }}</span>
            <span v-if="accountAgeText(a)" class="acct__age">{{ accountAgeText(a) }}</span>
            <span v-if="trialText(a)" class="acct__trial" :style="{ color: trialTone(a) }">
              {{ trialText(a) }}
            </span>
          </div>

          <div class="acct__regions">
            <span v-for="r in a.regions.slice(0, 2)" :key="r" class="acct__region mono">{{ r }}</span>
            <span v-if="a.regions.length > 2" class="acct__region mono">+{{ a.regions.length - 2 }}</span>
          </div>

          <!-- 正在校验时状态区骨架屏 -->
          <div v-if="a.status === 'checking'" class="acct__stats">
            <span class="skeleton" style="width: 56px; height: 32px" />
            <span class="skeleton" style="width: 56px; height: 32px" />
            <span class="skeleton" style="width: 56px; height: 32px" />
          </div>
          <div v-else class="acct__stats">
            <div><p class="acct__stat-label">实例</p><p class="acct__stat mono">{{ instancesOf(a.id).length }}</p></div>
            <div><p class="acct__stat-label">运行</p>
              <p class="acct__stat mono" style="color: var(--success)">{{ instancesOf(a.id).filter(i => i.state === 'RUNNING').length }}</p></div>
            <div><p class="acct__stat-label">停止</p>
              <p class="acct__stat mono dim">{{ instancesOf(a.id).filter(i => i.state === 'STOPPED').length }}</p></div>
          </div>

          <div class="acct__quota">
            <span class="t-2xs dim">ARM 配额</span>
            <span class="acct__track">
              <span v-if="!a.quota.unlimited.ocpu" class="acct__fill" :style="{
                width: pct(a.quota.ocpuUsed, a.quota.ocpuLimit) + '%',
                background: usageTone(a.quota.ocpuUsed, a.quota.ocpuLimit)
              }" />
            </span>
            <span class="mono t-2xs acct__quota-val"
                  :style="{ color: a.quota.unlimited.ocpu ? 'var(--text-secondary)' : usageTone(a.quota.ocpuUsed, a.quota.ocpuLimit) }">
              <template v-if="a.quota.unlimited.ocpu">{{ a.quota.ocpuUsed }} OCPU · 不限</template>
              <template v-else>{{ a.quota.ocpuUsed }} / {{ a.quota.ocpuLimit }} OCPU</template>
            </span>
          </div>

          <footer class="acct__foot" :style="{ color: lampTone[a.status] }">
            <span>{{ checkIcon[a.status] }}</span><span>{{ accountStatusText(a) }}</span>
          </footer>
        </div>
      </article>
    </div>

    <div v-else class="card table">
      <div class="table-head acols">
        <span>账号</span><span>类型 · 账龄</span><span class="col-region">区域</span>
        <span class="col-count">实例</span><span class="col-quota">ARM 配额</span>
        <span>最后校验</span><span></span>
      </div>
      <div v-for="a in state.accounts" :key="a.id" class="table-row acols"
           :class="{ 'is-error': a.status === 'error', 'is-disabled': a.status === 'disabled' }"
           :style="{ '--c': acctColor(a.colorIndex) }"
           tabindex="0" @click="openDrawer({ kind: 'account', id: a.id, tab: '概览' })"
           @keydown.enter="openDrawer({ kind: 'account', id: a.id, tab: '概览' })">
        <span class="acct-bar" :style="{ background: acctColor(a.colorIndex) }" />

        <span class="cell lcell">
          <span class="lcell__top">
            <span class="acct__lamp" :style="{ '--l': lampTone[a.status] }"
                  :class="{ 'is-solid': a.status !== 'disabled', 'is-pulsing': a.status === 'checking' || a.status === 'error' }" />
            <span class="lcell__alias">{{ a.alias }}</span>
            <span class="acct__code mono">{{ a.code }}</span>
          </span>
          <span class="lcell__sub"><MaskedText :value="a.email" kind="email" /></span>
        </span>

        <span class="cell lcell">
          <span class="lcell__top">
            <span class="acct__tag" :class="`is-${a.tier}`">{{ tierLabel[a.tier] }}</span>
            <span v-if="trialText(a)" class="acct__trial" :style="{ color: trialTone(a) }">{{ trialText(a) }}</span>
          </span>
          <span class="lcell__sub">{{ accountAgeText(a) || '开户时间未知' }}</span>
        </span>

        <span class="cell mono t-xs dim col-region">
          {{ a.regions.slice(0, 2).join(' · ') }}<template v-if="a.regions.length > 2"> +{{ a.regions.length - 2 }}</template>
        </span>

        <span class="cell mono t-xs col-count">
          {{ instancesOf(a.id).length }}
          <span class="dim-3">/ 运行 {{ instancesOf(a.id).filter(i => i.state === 'RUNNING').length }}</span>
        </span>

        <span class="cell lquota col-quota">
          <span class="acct__track">
            <span v-if="!a.quota.unlimited.ocpu" class="acct__fill" :style="{
              width: pct(a.quota.ocpuUsed, a.quota.ocpuLimit) + '%',
              background: usageTone(a.quota.ocpuUsed, a.quota.ocpuLimit)
            }" />
          </span>
          <span class="mono t-2xs acct__quota-val"
                :style="{ color: a.quota.unlimited.ocpu ? 'var(--text-secondary)' : usageTone(a.quota.ocpuUsed, a.quota.ocpuLimit) }">
            <template v-if="a.quota.unlimited.ocpu">{{ a.quota.ocpuUsed }} · 不限</template>
            <template v-else>{{ a.quota.ocpuUsed }} / {{ a.quota.ocpuLimit }}</template>
          </span>
        </span>

        <span class="cell t-xs" :style="{ color: lampTone[a.status] }">
          {{ checkIcon[a.status] }} {{ accountStatusText(a) }}
        </span>

        <span class="cell">
          <button class="acct__menu" aria-label="账号菜单"
                  @click.stop="openDrawer({ kind: 'account', id: a.id, tab: '密钥' })">⋯</button>
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.head-actions { display: flex; align-items: center; gap: 10px; }

.segmented { display: inline-flex; border: 1px solid var(--border-default); border-radius: var(--radius-md); overflow: hidden; }
.segmented__btn {
  padding: 6px 12px; border: 0; background: transparent;
  color: var(--text-secondary); font-size: 12px; cursor: pointer;
}
.segmented__btn + .segmented__btn { border-left: 1px solid var(--border-default); }
.segmented__btn:hover { background: var(--bg-hover); }
.segmented__btn.is-on { background: var(--bg-inset); color: var(--accent); }

/* 列表视图。列宽与实例表同源，保持两张表的视觉节奏一致。 */
.acols {
  display: grid; align-items: center;
  grid-template-columns: minmax(220px, 1.5fr) minmax(150px, 1fr) 130px 110px 150px minmax(140px, 1fr) 44px;
  gap: 12px; padding-left: 14px;
}
/* .cell 定义在 InstancesView 的 scoped 块里，这边拿不到。不补这一条，
   状态灯会退化成 inline 元素——width / height 全部失效，8px 的圆点被压成
   一根 2×18 的竖条，正好贴在左边的身份色条上，看起来像"红条中间有点绿"。 */
.cell { min-width: 0; padding-right: 12px; display: flex; align-items: center; gap: 8px; }
.table-row.acols { position: relative; height: 56px; cursor: pointer; }
.table-row.acols:hover { background: var(--bg-hover); }
.table-row.acols.is-error { background: color-mix(in srgb, var(--danger) 5%, transparent); }
.table-row.acols.is-disabled { opacity: 0.55; }
.acols .acct-bar { position: absolute; left: 0; top: 0; bottom: 0; width: 3px; }

/* align-items 必须显式写 flex-start：.cell 给的是 center，
   在纵向 flex 里那是"水平居中"，整块内容会被推到列中间。 */
.lcell { display: flex; flex-direction: column; align-items: flex-start; gap: 3px; min-width: 0; }
.lcell__top { display: flex; align-items: center; gap: 8px; min-width: 0; }
.lcell__alias { font-size: 13px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.lcell__sub { font-size: 11px; color: var(--text-tertiary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.lquota { display: flex; align-items: center; gap: 8px; }
.lquota .acct__track { flex: 1 1 auto; }

.acct__age { font-size: 10px; color: var(--text-tertiary); }
/* 不换行。升级号的上限曾经是一亿，"2 / 100000000 OCPU" 把这一行挤成三行，
   把卡片底部整个撑开。现在已经不显示那串数字了，但这条约束要留着：
   任何一个意外的长值都不该把卡片布局带歪。 */
.acct__quota-val { white-space: nowrap; flex: 0 0 auto; }

/* 窄屏收列。
 *
 * 卡片 .card 带 overflow: hidden（裁圆角用的），列放不下时不会出滚动条，
 * 而是被静默切掉——实测 1024px 下最后一列（⋯ 菜单）被切掉 307px，
 * 界面上看不出任何痕迹。所以必须主动收列，不能指望它自己挤。
 * 收的顺序按信息密度：先去区域，再去实例计数与配额条。 */
@media (max-width: 1279px) {
  .col-region { display: none; }
  .acols { grid-template-columns: minmax(210px, 1.5fr) minmax(150px, 1fr) 110px 150px minmax(140px, 1fr) 44px; }
}
@media (max-width: 1023px) {
  .col-quota, .col-count { display: none; }
  .acols { grid-template-columns: minmax(190px, 1.6fr) minmax(140px, 1fr) minmax(130px, 1fr) 44px; }
}

.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(340px, 1fr)); gap: 16px; }
.acct { display: flex; overflow: hidden; cursor: pointer; transition: border-color var(--dur-fast); }
.acct:hover { border-color: var(--border-strong); }
.acct:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.acct.is-error { border-color: var(--danger); filter: saturate(0.72); }
.acct.is-disabled { opacity: 0.55; }
.acct__bar { width: 4px; flex: 0 0 auto; background: var(--c); }
.acct__body { flex: 1 1 auto; min-width: 0; padding: 20px; }
.acct__errbar {
  margin: -20px -20px 14px; padding: 7px 20px; background: var(--danger-soft);
  border-bottom: 1px solid var(--danger); color: var(--danger); font-size: 11px; font-weight: 600;
}
.acct__head { display: flex; align-items: center; gap: 9px; }
.acct__lamp {
  position: relative; width: 8px; height: 8px; flex: 0 0 auto;
  border-radius: var(--radius-full); border: 1.5px solid var(--l);
}
.acct__lamp.is-solid { background: var(--l); }
.acct__lamp.is-pulsing::after {
  content: ''; position: absolute; inset: -1.5px; border-radius: var(--radius-full);
  background: var(--l); animation: ring 2s ease-out infinite;
}
.acct__alias { margin: 0; flex: 1 1 auto; min-width: 0; font-size: 16px; line-height: 24px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.acct__code { font-size: 11px; font-weight: 600; color: var(--c); padding: 2px 7px; border-radius: var(--radius-full); background: var(--bg-inset); }
.acct__menu { border: 0; background: none; color: var(--text-secondary); cursor: pointer; font-size: 13px; }
.acct__mail { font-size: 11px; color: var(--text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.acct__tier { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.acct__tag {
  padding: 2px 8px; border-radius: var(--radius-full);
  font-size: 10px; font-weight: 600; letter-spacing: .02em;
  border: 1px solid var(--border-default); color: var(--text-secondary);
}
.acct__tag.is-trial { border-color: color-mix(in srgb, var(--warning) 45%, transparent); color: var(--warning); }
.acct__tag.is-paid { border-color: color-mix(in srgb, var(--success) 45%, transparent); color: var(--success); }
.acct__trial { font-size: 10px; }
.acct__ocid { margin: 6px 0 0; font-size: 11px; color: var(--text-tertiary); }
.acct__regions { margin-top: 14px; display: flex; gap: 6px; flex-wrap: wrap; }
.acct__region {
  padding: 2px 8px; border-radius: var(--radius-full); border: 1px solid var(--border-subtle);
  background: var(--bg-inset); font-size: 11px; color: var(--text-secondary);
}
.acct__stats { margin-top: 16px; display: flex; gap: 20px; }
.acct__stat-label { margin: 0; font-size: 11px; color: var(--text-tertiary); }
.acct__stat { margin: 0; font-size: 16px; font-weight: 600; }
.acct__quota { margin-top: 14px; display: grid; grid-template-columns: 66px 1fr 82px; gap: 10px; align-items: center; }
.acct__track { height: 6px; border-radius: var(--radius-full); background: var(--bg-inset); overflow: hidden; }
.acct__fill { display: block; height: 6px; border-radius: var(--radius-full); }
.acct__quota .mono { text-align: right; }
.acct__foot {
  margin-top: 14px; padding-top: 12px; border-top: 1px solid var(--border-subtle);
  display: flex; align-items: center; gap: 7px; font-size: 12px;
}
</style>
