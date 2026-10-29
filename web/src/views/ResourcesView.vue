<script setup lang="ts">
/**
 * 资源：按账号盘点。
 *
 * 网络页、存储页是按资源类型跨账号聚合的，回答不了「这个账号的免费额度
 * 用了多少、哪些东西占着额度却没在用」。这一页反过来：先选账号，再把它
 * 名下各区域的卷、公网 IP、控制台连接、VCN 一次列全，并把闲置的单独拎出来。
 *
 * 目前只读。闲置资源的判定：卷未挂载、保留 IP 未绑定、控制台连接仍存在、
 * VCN 没有可用的互联网网关、用量超出永久免费参考额度。
 */
import { computed, onMounted, ref, watch } from 'vue'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import { dateText, relativeTime, shortAd } from '@/lib/adapt'
import { accounts as accountsApi, errorText, type AccountResourcesDTO, type ResourceVolumeDTO } from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import PageTabs from '@/components/PageTabs.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import QuotaMeter from '@/components/QuotaMeter.vue'
import MaskedText from '@/components/MaskedText.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'

const { state, accountById, openDrawer } = useStore()

/* ---------- 账号与区域 ---------- */

const ACCOUNT_KEY = 'oci.resources.account'
const usableAccounts = computed(() => state.accounts.filter(a => a.status !== 'disabled'))

function rememberedAccount(): string {
  try {
    const id = localStorage.getItem(ACCOUNT_KEY) ?? ''
    if (usableAccounts.value.some(a => a.id === id)) return id
  } catch { /* 隐私模式下 localStorage 会抛，退回第一个账号 */ }
  return usableAccounts.value[0]?.id ?? ''
}

const accountId = ref(rememberedAccount())
const region = ref('')
const account = computed(() => (accountId.value ? accountById(accountId.value) : undefined))

const accountGroups = computed(() => [{
  label: '',
  options: usableAccounts.value.map(a => ({
    value: a.id, label: `${a.code} · ${a.alias}`, dot: acctColor(a.colorIndex)
  }))
}])
const regionGroups = computed(() => [{
  label: '',
  options: [
    { value: '', label: '全部已订阅区域' },
    ...(account.value?.regions ?? []).map(r => ({ value: r, label: r }))
  ]
}])

/* ---------- 数据 ---------- */

const data = ref<AccountResourcesDTO | null>(null)
const loading = ref(false)
const loadError = ref('')
let seq = 0

async function load(refresh = false) {
  const id = accountId.value
  if (!id) return
  const mine = ++seq
  loading.value = true
  loadError.value = ''
  try {
    const res = await accountsApi.resources(id, region.value || undefined, refresh)
    if (mine !== seq) return
    data.value = res
  } catch (err) {
    if (mine !== seq) return
    data.value = null
    loadError.value = errorText(err)
  } finally {
    if (mine === seq) loading.value = false
  }
}

watch(accountId, id => {
  try { localStorage.setItem(ACCOUNT_KEY, id) } catch { /* 偏好丢了就丢了 */ }
  region.value = ''
  void load()
})
watch(region, () => void load())
onMounted(() => void load())

/* ---------- 汇总 ---------- */

const quotaItems = computed(() => data.value?.quota.items ?? [])

/** 永久免费参考额度对照。用量按所选区域的资源清单算，不是 Oracle 的计费口径。 */
const freeMeters = computed(() => {
  const d = data.value
  if (!d) return []
  return [
    { label: 'ARM OCPU', used: d.usage.armOcpus, limit: d.freeTier.armOcpus, unit: '' },
    { label: 'ARM 内存', used: d.usage.armMemoryGb, limit: d.freeTier.armMemoryGb, unit: ' GB' },
    { label: 'AMD 微型', used: d.usage.microInstances, limit: d.freeTier.microInstances, unit: ' 台' },
    { label: '存储合计', used: d.usage.storageGb, limit: d.freeTier.storageGb, unit: ' GB' }
  ]
})

interface Pending { key: string; tone: 'warn' | 'info'; title: string; detail: string; instanceId?: string }

/** 待处理：占着额度却没在用的，以及会让实例出不了网的配置。 */
const pending = computed<Pending[]>(() => {
  const d = data.value
  if (!d) return []
  const out: Pending[] = []

  for (const m of freeMeters.value) {
    if (m.used > m.limit) {
      out.push({
        key: 'free-' + m.label, tone: 'warn',
        title: `${m.label} 超出永久免费参考额度`,
        detail: `已用 ${m.used}${m.unit}，参考额度 ${m.limit}${m.unit}。免费号超出部分可能被 Oracle 回收，升级号超出部分开始计费。`
      })
    }
  }
  for (const v of d.volumes) {
    if (v.attachedTo.length > 0 || v.lifecycleState !== 'AVAILABLE') continue
    out.push({
      key: v.id, tone: 'warn',
      title: `${v.kind === 'boot' ? '引导卷' : '块存储'} ${v.displayName || '未命名'} 未挂载`,
      detail: `${v.sizeInGbs} GB · ${v.region} · ${shortAd(v.availabilityDomain)} · 创建于 ${dateText(v.timeCreated)}，仍计入存储额度`
    })
  }
  for (const ip of d.publicIps) {
    if (ip.lifetime !== 'RESERVED' || ip.assigned) continue
    out.push({
      key: ip.id, tone: 'warn',
      title: `保留 IP ${ipShown(ip.ipAddress)} 未绑定`,
      detail: `${ip.region}${ip.displayName ? ' · ' + ip.displayName : ''} · 未绑定的保留 IP 仍然占用配额`
    })
  }
  for (const c of d.consoleConnections) {
    out.push({
      key: c.id, tone: 'info',
      title: `控制台连接 · ${c.instance.name || '未知实例'}`,
      detail: `${c.region} · ${c.lifecycleState} · 一台实例同时只能有一条，用完建议删除`,
      instanceId: c.instance.instanceId
    })
  }
  for (const v of d.vcns) {
    if (v.internetGateway?.isEnabled) continue
    out.push({
      key: v.id, tone: 'info',
      title: `VCN ${v.displayName} ${v.internetGateway ? '的互联网网关已停用' : '没有互联网网关'}`,
      detail: `${v.region} · 其中的实例无法通过公网 IP 访问外网`
    })
  }
  return out
})

/** 待处理标题里的 IP 不经过 MaskedText，按隐私模式自己打码。 */
function ipShown(ip: string): string {
  if (!state.privacyMode) return ip
  const p = ip.split('.')
  return p.length === 4 ? `${p[0]}.${p[1]}.•.•` : ip
}

/* ---------- 分类 ---------- */

const TABS = ['计算', '存储', '公网 IP', '网络', '控制台连接']
const active = ref('存储')

const tabNote = computed(() => {
  const d = data.value
  if (!d) return ''
  switch (active.value) {
    case '计算': return `${d.instances.length} 台实例`
    case '存储': return `${d.volumes.length} 块卷 · 合计 ${d.usage.storageGb} GB`
    case '公网 IP': return `${d.publicIps.length} 个`
    case '网络': return `${d.vcns.length} 个 VCN`
    default: return `${d.consoleConnections.length} 条`
  }
})

function attachedText(v: ResourceVolumeDTO): string {
  return v.attachedTo.map(r => r.name || '未知实例').join('、')
}

const STATE_TEXT: Record<string, string> = {
  RUNNING: '运行中', STOPPED: '已停止', STARTING: '启动中', STOPPING: '停止中',
  PROVISIONING: '创建中', TERMINATING: '终止中'
}
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">资源</h1>
        <p class="page-sub">
          按账号盘点各区域的资源与额度用量
          <template v-if="data"> · 数据时间 {{ relativeTime(data.fetchedAt) }}</template>
        </p>
      </div>
      <div class="head-actions">
        <SelectMenu v-model="accountId" :groups="accountGroups" placeholder="选择账号"
                    aria-label="账号" :min-width="200" />
        <SelectMenu v-model="region" :groups="regionGroups" aria-label="区域" :min-width="180" />
        <button class="btn" :disabled="loading || !accountId" @click="load(true)">
          {{ loading ? '读取中…' : '刷新' }}
        </button>
      </div>
    </header>

    <EmptyState v-if="usableAccounts.length === 0" title="还没有可用的账号" body="先在账号页添加一个账号。" />

    <template v-else>
      <p v-if="loadError" class="warn">{{ loadError }}</p>
      <p v-if="data?.errors?.length" class="warn">
        部分资源没查到，下面的清单可能不完整：
        <template v-for="e in data.errors" :key="e.region + e.part">
          <br>{{ e.region }} · {{ e.part }}：{{ e.error }}
        </template>
      </p>

      <SkeletonRows v-if="loading && !data" :rows="6" />

      <template v-if="data">
        <div class="grid2">
          <SectionCard title="Oracle 限额" :note="`主区域 ${data.quota.region || '—'}`">
            <div class="meters">
              <template v-for="q in quotaItems" :key="q.key">
                <QuotaMeter v-if="q.known" :label="q.label" :used="q.used" :limit="q.limit" :unlimited="q.unlimited" />
                <div v-else class="meter-unknown">
                  <span class="t-xs dim">{{ q.label }}</span>
                  <span class="t-xs dim" :title="q.error">未知{{ q.error ? ' · ' + q.error : '' }}</span>
                </div>
              </template>
              <p v-if="data.quota.error" class="t-xs dim">{{ data.quota.error }}</p>
            </div>
          </SectionCard>

          <SectionCard title="永久免费参考额度" :note="`核对于 ${data.freeTier.checkedAt} · ${region || '全部区域'}合计`">
            <div class="meters">
              <QuotaMeter v-for="m in freeMeters" :key="m.label"
                          :label="m.label" :used="m.used" :limit="m.limit" :unit="m.unit" />
              <p class="t-2xs dim">
                参考额度来自 Oracle 文档，Oracle 改额度时不一定会公告；真实上限以左侧限额为准。
                停机的实例照样占用 OCPU 与内存额度。
              </p>
            </div>
          </SectionCard>
        </div>

        <SectionCard title="待处理" :note="pending.length ? `${pending.length} 项` : '没有发现闲置资源'" class="mt">
          <div v-if="pending.length === 0" class="ok-line t-xs">✓ 没有未挂载的卷、未绑定的保留 IP 与遗留的控制台连接</div>
          <div v-for="p in pending" :key="p.key" class="pend" :class="`pend--${p.tone}`">
            <span class="pend__mark">{{ p.tone === 'warn' ? '⚠' : '·' }}</span>
            <span class="pend__text">
              <span class="t-xs">{{ p.title }}</span>
              <span class="t-2xs dim">{{ p.detail }}</span>
            </span>
            <button v-if="p.instanceId" class="btn btn--xs"
                    @click="openDrawer({ kind: 'instance', id: p.instanceId, tab: '控制台' })">查看实例</button>
          </div>
        </SectionCard>

        <PageTabs class="mt" :tabs="TABS" v-model:active="active" />

        <SectionCard :title="active" :note="tabNote">
          <template v-if="active === '计算'">
            <EmptyState v-if="data.instances.length === 0" title="没有实例" />
            <template v-else>
              <div class="head cols-inst"><span>名称</span><span>区域</span><span>规格</span><span>配置</span><span>引导卷</span><span>状态</span></div>
              <div v-for="i in data.instances" :key="i.id" class="row cols-inst">
                <button class="link t-xs" @click="openDrawer({ kind: 'instance', id: i.id, tab: '概览' })">{{ i.name }}</button>
                <span class="mono t-xs dim">{{ i.region }}</span>
                <span class="mono t-xs">{{ i.shape }}</span>
                <span class="mono t-xs dim">{{ i.ocpus }} OCPU · {{ i.memoryGb }} GB</span>
                <span class="mono t-xs dim">{{ i.bootVolumeGb }} GB</span>
                <span class="t-xs">{{ STATE_TEXT[i.lifecycleState] ?? i.lifecycleState }}</span>
              </div>
            </template>
          </template>

          <template v-else-if="active === '存储'">
            <EmptyState v-if="data.volumes.length === 0" title="没有卷" />
            <template v-else>
              <div class="head cols-vol"><span>名称</span><span>类型</span><span>区域 · AD</span><span>容量</span><span>VPU</span><span>挂载到</span><span>创建</span></div>
              <div v-for="v in data.volumes" :key="v.id" class="row cols-vol">
                <span class="mono t-xs">{{ v.displayName || '未命名' }}</span>
                <span class="t-xs dim">{{ v.kind === 'boot' ? '引导卷' : '块存储' }}</span>
                <span class="mono t-xs dim">{{ v.region }} · {{ shortAd(v.availabilityDomain) }}</span>
                <span class="mono t-xs">{{ v.sizeInGbs }} GB</span>
                <span class="mono t-xs dim">{{ v.vpusPerGb }}</span>
                <span v-if="v.attachedTo.length" class="t-xs">{{ attachedText(v) }}</span>
                <span v-else class="t-xs tone-warn">未挂载</span>
                <span class="mono t-xs dim">{{ dateText(v.timeCreated) }}</span>
              </div>
            </template>
          </template>

          <template v-else-if="active === '公网 IP'">
            <EmptyState v-if="data.publicIps.length === 0" title="没有公网 IP" />
            <template v-else>
              <div class="head cols-ip"><span>地址</span><span>类型</span><span>区域</span><span>绑定到</span><span>状态</span></div>
              <div v-for="ip in data.publicIps" :key="ip.id" class="row cols-ip">
                <span class="mono t-xs"><MaskedText :value="ip.ipAddress" kind="ip" copyable /></span>
                <span class="t-xs" :class="{ dim: ip.lifetime !== 'RESERVED' }">{{ ip.lifetime === 'RESERVED' ? '保留' : '临时' }}</span>
                <span class="mono t-xs dim">{{ ip.region }}</span>
                <span v-if="ip.instance" class="t-xs">{{ ip.instance.name }}</span>
                <span v-else-if="ip.assigned" class="t-xs dim">已绑定（附属网卡或其他资源）</span>
                <span v-else class="t-xs tone-warn">未绑定</span>
                <span class="mono t-xs dim">{{ ip.lifecycleState }}</span>
              </div>
            </template>
          </template>

          <template v-else-if="active === '网络'">
            <EmptyState v-if="data.vcns.length === 0" title="没有 VCN" body="创建实例时可以自动建网。" />
            <template v-else>
              <div class="head cols-vcn"><span>VCN</span><span>区域</span><span>IPv4</span><span>IPv6</span><span>子网</span><span>互联网网关</span></div>
              <div v-for="v in data.vcns" :key="v.id" class="row cols-vcn">
                <span class="mono t-xs">{{ v.displayName }}</span>
                <span class="mono t-xs dim">{{ v.region }}</span>
                <span class="mono t-xs dim">{{ v.cidrBlocks.join(', ') || '—' }}</span>
                <span class="mono t-xs dim">{{ v.ipv6CidrBlocks.join(', ') || '未启用' }}</span>
                <span class="mono t-xs dim">{{ v.subnets }}</span>
                <span v-if="v.internetGateway?.isEnabled" class="t-xs">已启用</span>
                <span v-else class="t-xs tone-warn">{{ v.internetGateway ? '已停用' : '没有' }}</span>
              </div>
            </template>
          </template>

          <template v-else>
            <EmptyState v-if="data.consoleConnections.length === 0" title="没有控制台连接" />
            <template v-else>
              <div class="head cols-cc"><span>实例</span><span>区域</span><span>状态</span><span>公钥指纹</span></div>
              <div v-for="c in data.consoleConnections" :key="c.id" class="row cols-cc">
                <button class="link t-xs" @click="openDrawer({ kind: 'instance', id: c.instance.instanceId, tab: '控制台' })">
                  {{ c.instance.name || '未知实例' }}
                </button>
                <span class="mono t-xs dim">{{ c.region }}</span>
                <span class="mono t-xs">{{ c.lifecycleState }}</span>
                <span class="mono t-xs dim">{{ c.fingerprint || '—' }}</span>
              </div>
            </template>
          </template>
        </SectionCard>
      </template>
    </template>
  </div>
</template>

<style scoped>
.head-actions { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.warn {
  margin: 0 0 12px; padding: 10px 14px; border-radius: var(--radius-md);
  border: 1px solid var(--warning); background: var(--warning-soft);
  color: var(--warning); font-size: 12px; line-height: 18px;
}
.mt { margin-top: 16px; }
.grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
@media (max-width: 1023px) { .grid2 { grid-template-columns: 1fr; } }
.meters { padding: 14px 16px; display: flex; flex-direction: column; gap: 10px; }
/* QuotaMeter 默认的 80px 标签列放不下「块存储 · 免费额度 (GB)」这类后端给的长标签 */
.meters :deep(.meter) { grid-template-columns: 150px 1fr 130px; }
.meter-unknown { display: grid; grid-template-columns: 150px 1fr; gap: 10px; }
.meters p { margin: 4px 0 0; line-height: 16px; }

.ok-line { padding: 14px 16px; color: var(--success); }
.pend { display: flex; align-items: center; gap: 10px; padding: 10px 16px; border-bottom: 1px solid var(--border-subtle); }
.pend:last-child { border-bottom: 0; }
.pend__mark { width: 14px; flex: 0 0 auto; text-align: center; }
.pend--warn .pend__mark { color: var(--warning); }
.pend--info .pend__mark { color: var(--text-tertiary); }
.pend__text { display: flex; flex-direction: column; gap: 2px; min-width: 0; flex: 1 1 auto; }
.tone-warn { color: var(--warning); }

.head, .row { display: grid; align-items: center; gap: 12px; padding: 0 16px; }
.head { height: 34px; background: var(--bg-inset); border-bottom: 1px solid var(--border-subtle); font-size: 11px; color: var(--text-tertiary); }
.row { min-height: 42px; border-bottom: 1px solid var(--border-subtle); }
.row:hover { background: var(--bg-hover); }
.row > *, .head > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.link { border: 0; background: none; padding: 0; color: var(--text-primary); cursor: pointer; text-align: left; font-weight: 500; }
.link:hover { color: var(--accent); }
.cols-inst { grid-template-columns: minmax(150px, 1.3fr) 130px minmax(160px, 1.2fr) 140px 80px 80px; }
.cols-vol { grid-template-columns: minmax(150px, 1.3fr) 64px 160px 76px 48px minmax(110px, 1fr) 96px; }
.cols-ip { grid-template-columns: 170px 56px 130px minmax(140px, 1fr) 96px; }
.cols-vcn { grid-template-columns: minmax(140px, 1fr) 130px 150px minmax(120px, 1fr) 48px 90px; }
.cols-cc { grid-template-columns: minmax(150px, 1fr) 130px 90px minmax(160px, 1.4fr); }
</style>
