<script setup lang="ts">
/**
 * 代理池。
 *
 * 目的是网络隔离：给每个 Oracle 账号配一条独立出口，避免所有账号的 API
 * 调用从同一个 IP 出去。这一页要反复讲清楚两件事，否则它会变成一种
 * 虚假的安全感：
 *
 *  - 代理只换 IP，不换身份。每个请求都带该账号的密钥签名，Oracle 始终
 *    知道是哪个租户在调。隔离的只有「源 IP」这一个维度。
 *  - 共用出口比不用代理更糟。两个账号配同一条代理，等于主动把它们绑在
 *    一个 IP 上，凭空制造一个本来不存在的关联信号。所以重复绑定是硬拒绝。
 */
import { computed, onMounted, ref } from 'vue'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import { relativeTime } from '@/lib/adapt'
import {
  proxies as proxyApi, errorText,
  type ProxyDTO, type ProxyImportResultDTO, type ProxyStatusDTO
} from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import CheckList from '@/components/CheckList.vue'
import AccountChip from '@/components/AccountChip.vue'
import SelectMenu from '@/components/SelectMenu.vue'

const { state, accountById, ask, toast, toastError } = useStore()

const list = ref<ProxyDTO[]>([])
const bindings = ref<Record<string, string>>({})
const notice = ref('')
const loading = ref(false)
const loadError = ref('')
const checking = ref(false)

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await proxyApi.list()
    list.value = res.proxies ?? []
    bindings.value = res.bindings ?? {}
    notice.value = res.notice
  } catch (err) {
    loadError.value = errorText(err)
  } finally {
    loading.value = false
  }
}

onMounted(() => void load())

/* ---------- 导入 ---------- */

const importText = ref('')
const preview = ref<ProxyImportResultDTO | null>(null)
const importing = ref(false)

const PLACEHOLDER = `1.2.3.4:8080
1.2.3.4:8080:user:pass
user:pass@1.2.3.4:8080
socks5://user:pass@1.2.3.4:1080  # 香港节点`

/** 先干跑一遍让用户核对。代理列表动辄十几行、格式又乱，直接写库会让人蒙。 */
async function parsePreview() {
  if (!importText.value.trim()) return
  importing.value = true
  try {
    preview.value = await proxyApi.import(importText.value, true)
  } catch (err) {
    toastError('解析失败', err)
  } finally {
    importing.value = false
  }
}

async function confirmImport() {
  importing.value = true
  try {
    const res = await proxyApi.import(importText.value, false)
    toast({
      tone: res.failed > 0 ? 'warning' : 'success',
      title: `导入完成：成功 ${res.ok} 条`,
      body: [
        res.skipped ? `跳过 ${res.skipped} 条（已存在）` : '',
        res.failed ? `失败 ${res.failed} 条` : ''
      ].filter(Boolean).join(' · ') || undefined
    })
    importText.value = ''
    preview.value = null
    await load()
  } catch (err) {
    toastError('导入失败', err)
  } finally {
    importing.value = false
  }
}

/* ---------- 检测 ---------- */

async function check(id?: string) {
  checking.value = true
  try {
    const { results } = await proxyApi.check(id)
    const ok = results.filter(r => r.status === 'ok').length
    toast({
      tone: ok === results.length ? 'success' : 'warning',
      title: `检测完成：${ok} / ${results.length} 条可用`
    })
    await load()
  } catch (err) {
    toastError('检测失败', err)
  } finally {
    checking.value = false
  }
}

/* ---------- 绑定 ---------- */

/** proxyId -> accountId，用来在代理行上显示"绑给了谁"。 */
const boundTo = computed(() => {
  const m: Record<string, string> = {}
  for (const [accId, proxyId] of Object.entries(bindings.value)) m[proxyId] = accId
  return m
})

/** 只列还没被别人占用的代理。共用是硬拒绝，选项里就不该出现。 */
function optionsFor(accountId: string) {
  const free = list.value.filter(p => {
    const owner = boundTo.value[p.id]
    return p.enabled && (!owner || owner === accountId)
  })
  return [{
    label: '代理',
    options: [
      { value: '', label: '本机直连（不走代理）' },
      ...free.map(p => ({
        value: p.id,
        label: `${p.label || display(p)}${p.lastStatus === 'fail' ? ' · 不通' : ''}`,
        dot: TONE[p.lastStatus]
      }))
    ]
  }]
}

async function bind(accountId: string, proxyId: string) {
  try {
    await proxyApi.bind(accountId, proxyId)
    toast({ tone: 'success', title: proxyId ? '已绑定' : '已解绑，恢复本机直连' })
    await load()
  } catch (err) {
    toastError('绑定失败', err)
    await load()
  }
}

function remove(p: ProxyDTO) {
  ask({
    level: 2,
    title: `删除代理「${p.label || display(p)}」`,
    body: '删除后这条代理从池子里消失。仍被账号绑定的代理无法删除，请先解绑。',
    okLabel: '删除',
    onConfirm: async () => {
      try {
        await proxyApi.remove(p.id)
        toast({ tone: 'success', title: '已删除' })
        await load()
      } catch (err) {
        toastError('删除失败', err)
      }
    }
  })
}

async function toggle(p: ProxyDTO) {
  try {
    await proxyApi.update(p.id, { enabled: !p.enabled })
    await load()
  } catch (err) {
    toastError('操作失败', err)
  }
}

/* ---------- 展示 ---------- */

const TONE: Record<ProxyStatusDTO, string> = {
  ok: 'var(--success)',
  fail: 'var(--danger)',
  unknown: 'var(--text-tertiary)'
}

const STATUS_TEXT: Record<ProxyStatusDTO, string> = {
  ok: '可用', fail: '不通', unknown: '未检测'
}

const display = (p: ProxyDTO) =>
  `${p.scheme}://${p.username ? p.username + ':****@' : ''}${p.host}:${p.port}`

function checkedText(p: ProxyDTO): string {
  if (!p.lastCheckedAt || p.lastCheckedAt.startsWith('1970')) return '尚未检测'
  return relativeTime(p.lastCheckedAt) + '检测'
}

/**
 * 失败但曾经通过——值得单独说一句。
 *
 * 「刚才挂了但十分钟前还好好的」和「从来没通过」是两种完全不同的处置：
 * 前者多半是代理商抽风，等等或重试即可；后者说明配置就是错的。
 */
function everWorked(p: ProxyDTO): boolean {
  return p.lastStatus === 'fail' && !!p.lastOkAt && !p.lastOkAt.startsWith('1970')
}

const usableAccounts = computed(() =>
  state.accounts.filter(a => a.status !== 'disabled'))

const okCount = computed(() => list.value.filter(p => p.lastStatus === 'ok').length)
const boundCount = computed(() => Object.keys(bindings.value).length)
const directCount = computed(() => usableAccounts.value.length - boundCount.value)
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">代理</h1>
        <p class="page-sub">
          {{ list.length }} 条代理，{{ okCount }} 条可用 ·
          {{ boundCount }} 个账号走代理，{{ directCount }} 个本机直连
        </p>
      </div>
      <div class="head-actions">
        <button class="btn" :disabled="checking || list.length === 0" @click="check()">
          {{ checking ? '检测中…' : '全部检测' }}
        </button>
        <button class="btn" :disabled="loading" @click="load()">刷新</button>
      </div>
    </header>

    <p v-if="loadError" class="load-err">{{ loadError }}</p>

    <CheckList class="warn-box" :items="[
      { tone: 'info', text: notice || '代理只改变出口 IP，不改变身份——每个请求仍带该账号的密钥签名。' },
      { tone: 'warn',
        text: '一条代理只能绑一个账号。两个账号共用同一出口，等于把它们绑在同一个 IP 上，反而制造了本来不存在的关联信号——那与网络隔离的目的正好相反。' },
      { tone: 'info',
        text: '检测走的是「通过该代理访问 OCI 端点」，不经任何第三方、不消耗配额、不产生费用。按该代理所绑账号的 home region 测——同一条代理连不同区域的延迟能差好几倍。' },
      { tone: 'warn',
        text: '检测不了「不同代理但同一出口 IP」。那需要把代理列表发给第三方回显服务，与本工具不向第三方上报数据的原则冲突。代理商给的多条 IP 是否真的独立，需要你自己验一次。' }
    ]" />

    <!-- 导入 -->
    <SectionCard title="导入代理" note="每行一条，认得下面几种常见格式">
      <div class="pad">
        <textarea v-model="importText" class="input imp__box mono" rows="5"
                  :placeholder="PLACEHOLDER" spellcheck="false" />
        <div class="imp__actions">
          <button class="btn" :disabled="importing || !importText.trim()"
                  @click="parsePreview()">{{ importing ? '解析中…' : '解析预览' }}</button>
          <button v-if="preview" class="btn btn--primary"
                  :disabled="importing || preview.ok === 0"
                  @click="confirmImport()">确认导入 {{ preview.ok }} 条</button>
          <span v-if="preview" class="t-2xs dim-3">
            解析成功 {{ preview.ok }} · 失败 {{ preview.failed }}
          </span>
        </div>

        <!-- 逐行结果。失败行必须留在原地并带行号，只报个总数等于让人自己找 -->
        <div v-if="preview?.rows?.length" class="imp__rows">
          <div v-for="row in preview.rows" :key="row.line" class="imp__row">
            <span class="mono t-2xs dim-3 imp__line">{{ row.line }}</span>
            <span class="mono t-xs imp__addr" :class="{ 'is-bad': row.error }">{{ row.masked }}</span>
            <span v-if="row.label" class="t-2xs dim-3">{{ row.label }}</span>
            <span class="imp__spacer" />
            <span v-if="row.error" class="t-2xs imp__err">{{ row.error }}</span>
            <span v-else-if="row.skipped" class="t-2xs dim-3">已存在，跳过</span>
            <span v-else class="t-2xs" style="color: var(--success)">可导入</span>
          </div>
        </div>
      </div>
    </SectionCard>

    <!-- 代理列表 -->
    <SectionCard title="代理池" :note="`${list.length} 条`">
      <SkeletonRows v-if="loading && list.length === 0" :rows="3" />

      <EmptyState v-else-if="list.length === 0" icon="⇄" title="代理池是空的"
                  sub="在上面粘贴代理列表导入。不导入的话所有账号都走本机直连。" />

      <div v-for="p in list" v-else :key="p.id" class="px" :class="{ 'is-off': !p.enabled }">
        <span class="px__dot" :style="{ background: TONE[p.lastStatus] }" />
        <span class="px__status" :style="{ color: TONE[p.lastStatus] }">
          {{ STATUS_TEXT[p.lastStatus] }}
        </span>

        <span class="px__label">{{ p.label || '—' }}</span>
        <span class="mono t-xs px__addr">{{ display(p) }}</span>

        <span v-if="p.lastStatus === 'ok'" class="mono t-2xs dim-3 px__lat">
          {{ p.lastLatencyMs }}ms
        </span>
        <span v-else class="px__lat" />

        <span v-if="boundTo[p.id]" class="px__bound">
          <AccountChip :account="accountById(boundTo[p.id])" />
        </span>
        <span v-else class="t-2xs dim-3 px__bound">未绑定</span>

        <span class="px__spacer" />

        <span v-if="p.lastError" class="mono t-2xs px__err" :title="p.lastError">
          {{ p.lastError }}
        </span>
        <span v-if="everWorked(p)" class="t-2xs dim-3" :title="`最近一次成功：${p.lastOkAt}`">
          {{ relativeTime(p.lastOkAt) }}还是通的
        </span>
        <span class="mono t-2xs dim-3 px__when">{{ checkedText(p) }}</span>

        <button class="btn btn--xs" :disabled="checking" @click="check(p.id)">检测</button>
        <button class="btn btn--xs" @click="toggle(p)">{{ p.enabled ? '停用' : '启用' }}</button>
        <button class="btn btn--xs btn--warning" @click="remove(p)">删除</button>
      </div>
    </SectionCard>

    <!-- 分配矩阵 -->
    <SectionCard title="账号分配"
                 note="一屏看全谁走代理、谁直连。改动立刻生效，下一次调用就换路">
      <EmptyState v-if="usableAccounts.length === 0" icon="⬡" title="还没有账号"
                  sub="先在账号页接入 Oracle 账号。" />

      <div v-for="a in usableAccounts" v-else :key="a.id" class="asg">
        <span class="asg__bar" :style="{ background: acctColor(a.colorIndex) }" />
        <AccountChip :account="a" variant="full" />

        <span class="asg__spacer" />

        <span v-if="bindings[a.id]" class="mono t-2xs dim-3 asg__cur">
          {{ display(list.find(p => p.id === bindings[a.id])!) }}
        </span>
        <span v-else class="t-2xs dim-3 asg__cur">本机直连</span>

        <SelectMenu :model-value="bindings[a.id] ?? ''" :groups="optionsFor(a.id)"
                    placeholder="选择代理" :aria-label="`${a.alias} 的代理`" :min-width="240"
                    @update:model-value="v => bind(a.id, String(v))" />
      </div>
    </SectionCard>
  </div>
</template>

<style scoped>
.warn-box { margin-bottom: 16px; }
.page > .card + .card { margin-top: 16px; }
.pad { padding: 16px 20px; }

/* ---- 导入 ---- */
.imp__box { width: 100%; resize: vertical; line-height: 1.7; }
.imp__actions { display: flex; align-items: center; gap: 12px; margin-top: 12px; }
.imp__rows { margin-top: 16px; border-top: 1px solid var(--border-subtle); }
.imp__row {
  display: flex; align-items: center; gap: 10px;
  height: 30px; border-bottom: 1px solid var(--border-subtle);
}
.imp__row:last-child { border-bottom: none; }
.imp__line { width: 28px; text-align: right; flex: 0 0 auto; }
.imp__addr { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.imp__addr.is-bad { color: var(--danger); }
.imp__spacer { flex: 1 1 auto; min-width: 8px; }
.imp__err { color: var(--danger); }

/* ---- 代理行 ---- */
.px {
  display: flex; align-items: center; gap: 12px;
  height: 46px; padding: 0 16px;
  border-bottom: 1px solid var(--border-subtle);
}
.px:last-child { border-bottom: none; }
/* 停用的压暗但不隐藏——藏起来用户会以为被删了 */
.px.is-off { opacity: 0.5; }
.px__dot { width: 7px; height: 7px; border-radius: var(--radius-full); flex: 0 0 auto; }
.px__status { font-size: var(--t-2xs); font-weight: 600; width: 44px; flex: 0 0 auto; }
.px__label {
  width: 110px; flex: 0 0 auto; font-size: 13px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.px__addr {
  width: 210px; flex: 0 0 auto;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.px__lat { width: 56px; text-align: right; flex: 0 0 auto; }
.px__bound { width: 108px; flex: 0 0 auto; }
.px__spacer { flex: 1 1 auto; min-width: 8px; }
.px__err {
  color: var(--danger); max-width: 240px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.px__when { width: 96px; text-align: right; flex: 0 0 auto; }

/* ---- 分配 ---- */
.asg {
  position: relative; display: flex; align-items: center; gap: 12px;
  height: 52px; padding: 0 16px 0 17px;
  border-bottom: 1px solid var(--border-subtle);
}
.asg:last-child { border-bottom: none; }
.asg__bar { position: absolute; left: 0; top: 0; bottom: 0; width: 3px; }
.asg__spacer { flex: 1 1 auto; min-width: 8px; }
.asg__cur {
  width: 220px; text-align: right; flex: 0 0 auto;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

@media (max-width: 1100px) {
  .px__label, .px__when { display: none; }
  .asg__cur { display: none; }
}
</style>
