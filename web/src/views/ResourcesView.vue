<script setup lang="ts">
/**
 * 资源：按账号盘点。
 *
 * 网络页、存储页是按资源类型跨账号聚合的，回答不了「这个账号的免费额度
 * 用了多少、哪些东西占着额度却没在用」。这一页反过来：先选账号，再把它
 * 名下各区域的卷、公网 IP、控制台连接、VCN 一次列全，并把闲置的单独拎出来。
 *
 * 闲置资源的判定：卷未挂载、保留 IP 未绑定、控制台连接仍存在、VCN 没有可用的
 * 互联网网关、用量超出永久免费参考额度。前三类可以直接在这里清理。
 */
import { t } from '@/i18n'
import { computed, onMounted, ref, watch } from 'vue'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import { maskIP } from '@/lib/mask'
import { dateText, relativeTime, shortAd } from '@/lib/adapt'
import {
  accounts as accountsApi, errorText,
  type AccountResourcesDTO, type ResourceVolumeDTO, type ResourcePublicIpDTO, type ResourceConsoleDTO
} from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import PageTabs from '@/components/PageTabs.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import QuotaMeter from '@/components/QuotaMeter.vue'
import MaskedText from '@/components/MaskedText.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import LoadingBar from '@/components/LoadingBar.vue'

const { state, accountById, openDrawer, ask, toast, toastError } = useStore()

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
    { value: '', label: t.resources.allRegions },
    ...(account.value?.regions ?? []).map(r => ({ value: r, label: r }))
  ]
}])

/* ---------- 数据 ---------- */

const data = ref<AccountResourcesDTO | null>(null)
const loading = ref(false)
const loadError = ref('')
let seq = 0

/** 正在查几个区域。骨架屏上告诉用户为什么要等——多区域账号一次要发上百个调用。 */
const regionCount = computed(() => (region.value ? 1 : Math.max(1, account.value?.regions.length ?? 1)))

/**
 * 读取资源汇总。
 *
 * clear 为 true 时先清空旧数据：换账号时继续显示上一个账号的清单，
 * 用户会把它当成新账号的数据去删东西。换区域、刷新则保留旧数据、调暗显示，
 * 页面不会整块消失再出现。
 */
async function load(refresh = false, clear = false) {
  const id = accountId.value
  if (!id) return
  const mine = ++seq
  if (clear) data.value = null
  loading.value = true
  loadError.value = ''
  try {
    const res = await accountsApi.resources(id, region.value || undefined, refresh)
    if (mine !== seq) return
    data.value = withoutRemoved(res)
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
  void load(false, true)
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
    { key: 'ocpu', label: 'ARM OCPU', used: d.usage.armOcpus, limit: d.freeTier.armOcpus, unit: '' },
    { key: 'mem', label: t.resources.armMem, used: d.usage.armMemoryGb, limit: d.freeTier.armMemoryGb, unit: ' GB' },
    { key: 'micro', label: t.resources.amdMicro, used: d.usage.microInstances, limit: d.freeTier.microInstances, unit: t.resources.unitInstances },
    { key: 'storage', label: t.resources.storageTotal, used: d.usage.storageGb, limit: d.freeTier.storageGb, unit: ' GB' }
  ]
})

/** 超出免费额度的后果因账号类型而异，说清楚是哪一种，不要两种都写。 */
const overConsequence = computed(() =>
  data.value?.freeTier.tier === 'paid'
    ? t.resources.overPaid
    : t.resources.overFree)

type PendingAction =
  | { kind: 'volume'; item: ResourceVolumeDTO }
  | { kind: 'ip'; item: ResourcePublicIpDTO }
  | { kind: 'console'; item: ResourceConsoleDTO }

interface Pending {
  key: string; tone: 'warn' | 'info'; title: string; detail: string
  instanceId?: string; action?: PendingAction
}

/** 待处理：占着额度却没在用的，以及会让实例出不了网的配置。 */
const pending = computed<Pending[]>(() => {
  const d = data.value
  if (!d) return []
  const out: Pending[] = []

  for (const m of freeMeters.value) {
    if (m.used > m.limit) {
      out.push({
        key: 'free-' + m.key, tone: 'warn',
        title: t.resources.overTitle(m.label),
        detail: t.resources.overDetail(`${m.used}${m.unit}`, t.resources.tier[d.freeTier.tier] ?? '', `${m.limit}${m.unit}`, overConsequence.value)
      })
    }
  }
  for (const v of d.volumes) {
    if (v.attachedTo.length > 0 || v.lifecycleState !== 'AVAILABLE') continue
    out.push({
      key: v.id, tone: 'warn',
      title: t.resources.unattachedTitle(volumeKind(v), v.displayName || t.common.unnamed),
      detail: t.resources.unattachedDetail(v.sizeInGbs, v.region, shortAd(v.availabilityDomain), dateText(v.timeCreated)),
      action: { kind: 'volume', item: v }
    })
  }
  for (const ip of d.publicIps) {
    if (ip.lifetime !== 'RESERVED' || ip.assigned) continue
    out.push({
      key: ip.id, tone: 'warn',
      title: t.resources.ipUnboundTitle(ipShown(ip.ipAddress)),
      detail: t.resources.ipUnboundDetail(`${ip.region}${ip.displayName ? ' · ' + ip.displayName : ''}`),
      action: { kind: 'ip', item: ip }
    })
  }
  for (const c of d.consoleConnections) {
    out.push({
      key: c.id, tone: 'info',
      title: t.resources.consoleTitle(c.instance.name || t.resources.unknownInstance),
      detail: t.resources.consoleDetail(c.region, c.lifecycleState),
      instanceId: c.instance.instanceId,
      action: { kind: 'console', item: c }
    })
  }
  for (const v of d.vcns) {
    if (v.internetGateway?.isEnabled) continue
    out.push({
      key: v.id, tone: 'info',
      title: v.internetGateway ? t.resources.vcnGwDisabled(v.displayName) : t.resources.vcnNoGw(v.displayName),
      detail: t.resources.vcnDetail(v.region)
    })
  }
  return out
})

/** 待处理标题、确认框里的 IP 不经过 MaskedText，按隐私模式自己打码。 */
function ipShown(ip: string): string {
  return state.privacyMode ? maskIP(ip) : ip
}

const volumeKind = (v: ResourceVolumeDTO) => (v.kind === 'boot' ? t.resources.bootVolume : t.resources.blockVolume)

/* ---------- 清理操作 ---------- */

/**
 * 本次会话里已经删掉 / 释放的资源 OCID。
 *
 * OCI 的删除是异步的，刚删完的一小会儿里列表接口可能仍把它报成 ACTIVE。
 * 不挡住的话，它会随着刷新又出现、还带着删除按钮——用户以为没删掉，再点一次。
 * OCI 不再返回某个 OCID 时，就把它从这里清掉。
 */
const removed = new Set<string>()

function withoutRemoved(d: AccountResourcesDTO): AccountResourcesDTO {
  const present = new Set([...d.volumes, ...d.publicIps, ...d.consoleConnections].map(x => x.id))
  for (const id of [...removed]) if (!present.has(id)) removed.delete(id)
  if (removed.size === 0) return d
  return {
    ...d,
    volumes: d.volumes.filter(x => !removed.has(x.id)),
    publicIps: d.publicIps.filter(x => !removed.has(x.id)),
    consoleConnections: d.consoleConnections.filter(x => !removed.has(x.id))
  }
}

/** 正在处理的资源 OCID。同一时刻允许多条，各自的按钮各自转圈。 */
const acting = ref<Set<string>>(new Set())

async function run(id: string, label: string, failLabel: string, fn: () => Promise<string | void>) {
  acting.value = new Set(acting.value).add(id)
  try {
    const notice = await fn()
    toast({ tone: 'success', title: label, body: notice || undefined })
    // 先在本地拿掉，待处理里那一项立刻滑出；随后静默重拉对一遍账。
    removed.add(id)
    if (data.value) data.value = withoutRemoved(data.value)
    await load(true)
  } catch (err) {
    toastError(failLabel, err)
  } finally {
    const next = new Set(acting.value)
    next.delete(id)
    acting.value = next
  }
}

/** 删卷：L3，回传卷名。卷上的数据不可恢复。 */
function deleteVolume(v: ResourceVolumeDTO) {
  const name = v.displayName || v.id
  ask({
    level: 3,
    title: t.resources.deleteVolTitle(volumeKind(v), name),
    body: t.resources.deleteVolBody,
    noun: name, nounLabel: t.resources.volName,
    losses: [t.resources.volLoss(volumeKind(v), name, v.sizeInGbs)],
    okLabel: t.resources.deletePermanently,
    onConfirm: () => run(v.id, t.resources.volDeleted(volumeKind(v), name), t.resources.failed(t.resources.deleteVol), async () =>
      (await accountsApi.deleteVolume(accountId.value, v.id, v.region, v.kind, name)).notice)
  })
}

/** 释放保留 IP：L3，回传 IP 地址。释放后这个地址永久归还 Oracle。 */
function releaseIp(ip: ResourcePublicIpDTO) {
  ask({
    level: 3,
    title: t.resources.releaseTitle(ipShown(ip.ipAddress)),
    body: t.resources.releaseBody,
    noun: ip.ipAddress, nounLabel: t.resources.ipAddress,
    losses: [t.resources.ipLoss(ipShown(ip.ipAddress), ip.displayName ?? '')],
    okLabel: t.resources.release,
    onConfirm: () => run(ip.id, t.resources.released, t.resources.failed(t.resources.releaseIp), () =>
      accountsApi.releaseReservedIp(accountId.value, ip.id, ip.region, ip.ipAddress).then(() => undefined))
  })
}

/** 删控制台连接：L2。不丢任何数据，需要时可以随时重建。 */
function deleteConsole(c: ResourceConsoleDTO) {
  const name = c.instance.name || t.resources.unknownInstance
  ask({
    level: 2,
    title: t.resources.deleteConsoleTitle(name),
    body: t.resources.deleteConsoleBody,
    okLabel: t.resources.deleteConn,
    onConfirm: () => run(c.id, t.resources.consoleDeleted, t.resources.failed(t.resources.deleteConsole), () =>
      accountsApi.deleteConsoleConnection(accountId.value, c.id, c.region).then(() => undefined))
  })
}

function doAction(a: PendingAction) {
  if (a.kind === 'volume') deleteVolume(a.item)
  else if (a.kind === 'ip') releaseIp(a.item)
  else deleteConsole(a.item)
}

const actionLabel = (k: PendingAction['kind']) =>
  k === 'volume' ? t.common.delete : k === 'ip' ? t.resources.release : t.resources.deleteConn

/* ---------- 分类 ---------- */

const TABS = ['compute', 'storage', 'publicIp', 'network', 'console']
const active = ref('storage')

const tabNote = computed(() => {
  const d = data.value
  if (!d) return ''
  switch (active.value) {
    case 'compute': return t.resources.noteInstances(d.instances.length)
    case 'storage': return t.resources.noteVolumes(d.volumes.length, d.usage.storageGb)
    case 'publicIp': return t.resources.noteIps(d.publicIps.length)
    case 'network': return t.resources.noteVcns(d.vcns.length)
    default: return t.resources.noteConsoles(d.consoleConnections.length)
  }
})

function attachedText(v: ResourceVolumeDTO): string {
  return v.attachedTo.map(r => r.name || t.resources.unknownInstance).join(t.resources.listSep)
}

const stateText = (s: string) => (t.enums.lifecycle as Record<string, string>)[s] ?? s
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">{{ t.resources.title }}</h1>
        <p class="page-sub">
          {{ t.resources.subtitle }}
          <template v-if="data">{{ t.resources.dataTime(relativeTime(data.fetchedAt)) }}</template>
        </p>
      </div>
      <div class="head-actions">
        <SelectMenu v-model="accountId" :groups="accountGroups" :placeholder="t.resources.pickAccount"
                    :aria-label="t.resources.account" :min-width="200" />
        <SelectMenu v-model="region" :groups="regionGroups" :aria-label="t.resources.region" :min-width="180" />
        <button class="btn" :disabled="loading || !accountId" @click="load(true)">
          <span v-if="loading" class="btn-spin" aria-hidden="true" />{{ loading ? t.resources.loadingShort : t.common.refresh }}
        </button>
      </div>
    </header>
    <LoadingBar :active="loading && !!data" />

    <EmptyState v-if="usableAccounts.length === 0" :title="t.resources.noAccounts" :body="t.resources.noAccountsBody" />

    <template v-else>
      <p v-if="loadError" class="warn">{{ loadError }}</p>

      <!-- 首次加载 / 换账号：按真实布局画骨架，数据到了原地替换，页面不跳 -->
      <div v-if="loading && !data" class="sk" aria-busy="true">
        <p class="skeleton-hint">
          <span class="dot-pulse" aria-hidden="true" />
          {{ t.resources.loadingHint(regionCount) }}
        </p>
        <div class="grid2">
          <div v-for="c in 2" :key="c" class="card sk-card">
            <div class="sk-card__head"><span class="skeleton" style="width: 96px; height: 14px" /></div>
            <div class="meters">
              <div v-for="n in 4" :key="n" class="sk-meter">
                <span class="skeleton" style="width: 72px" />
                <span class="skeleton" style="height: 6px" />
                <span class="skeleton" style="width: 64px; justify-self: end" />
              </div>
            </div>
          </div>
        </div>
        <div class="card sk-card mt">
          <div class="sk-card__head"><span class="skeleton" style="width: 64px; height: 14px" /></div>
          <div v-for="n in 3" :key="n" class="sk-pend">
            <span class="skeleton" style="width: 14px; height: 14px" />
            <span class="sk-pend__text">
              <span class="skeleton" :style="{ width: 160 + n * 40 + 'px' }" />
              <span class="skeleton" :style="{ width: 280 + n * 30 + 'px', height: 10 + 'px' }" />
            </span>
          </div>
        </div>
        <div class="sk-tabs mt">
          <span v-for="n in 5" :key="n" class="skeleton" style="width: 64px; height: 28px; border-radius: 999px" />
        </div>
        <div class="card sk-card">
          <SkeletonRows :rows="4" :height="42" />
        </div>
      </div>

      <!-- 换区域、刷新时保留旧数据并调暗，页头下方的进度条在走 -->
      <div v-if="data" class="stale-able" :class="{ 'is-stale': loading }">

        <p v-if="data.errors?.length" class="warn reveal">
          {{ t.resources.partial }}
          <template v-for="e in data.errors" :key="e.region + e.part">
            <br>{{ e.region }} · {{ e.part }}：{{ e.error }}
          </template>
        </p>

        <div class="grid2">
          <SectionCard class="reveal" style="--i: 0" :title="t.resources.limitsTitle" :note="t.resources.homeRegion(data.quota.region || '—')">
            <div class="meters">
              <template v-for="q in quotaItems" :key="q.key">
                <QuotaMeter v-if="q.known" :label="q.label" :used="q.used" :limit="q.limit" :unlimited="q.unlimited" />
                <div v-else class="meter-unknown">
                  <span class="t-xs dim">{{ q.label }}</span>
                  <span class="t-xs dim" :title="q.error">{{ t.resources.unknown }}{{ q.error ? ' · ' + q.error : '' }}</span>
                </div>
              </template>
              <p v-if="data.quota.error" class="t-xs dim">{{ data.quota.error }}</p>
            </div>
          </SectionCard>

          <SectionCard class="reveal" style="--i: 1" :title="t.resources.freeTitle"
                       :note="t.resources.freeNote(t.resources.tier[data.freeTier.tier] ?? '', data.freeTier.checkedAt, region || t.resources.allRegionsShort)">
            <div class="meters">
              <QuotaMeter v-for="m in freeMeters" :key="m.key" full-ok
                          :label="m.label" :used="m.used" :limit="m.limit" :unit="m.unit" />
              <p class="t-2xs dim">
                <template v-if="data.freeTier.tier === 'paid'">{{ t.resources.freePaid }}</template>
                <template v-else-if="data.freeTier.tier === 'free'">{{ t.resources.freeFree }}</template>
                <template v-else>{{ t.resources.freeUnknown }}</template>
                {{ t.resources.freeCaveat }}
              </p>
            </div>
          </SectionCard>
        </div>

        <SectionCard class="mt reveal" style="--i: 2" :title="t.resources.pendingTitle"
                     :note="pending.length ? t.resources.pendingNote(pending.length) : t.resources.noIdle">
          <div v-if="pending.length === 0" class="ok-line t-xs">{{ t.resources.allClean }}</div>
          <TransitionGroup name="pend" tag="div">
            <div v-for="p in pending" :key="p.key" class="pend" :class="`pend--${p.tone}`">
              <span class="pend__mark">{{ p.tone === 'warn' ? '⚠' : '·' }}</span>
              <span class="pend__text">
                <span class="t-xs">{{ p.title }}</span>
                <span class="t-2xs dim">{{ p.detail }}</span>
              </span>
              <span class="pend__acts">
                <button v-if="p.instanceId" class="btn btn--xs"
                        @click="openDrawer({ kind: 'instance', id: p.instanceId, tab: 'console' })">{{ t.common.viewInstance }}</button>
                <button v-if="p.action" class="btn btn--xs btn--warning" :disabled="acting.has(p.key)"
                        @click="doAction(p.action)">
                  <span v-if="acting.has(p.key)" class="btn-spin" aria-hidden="true" />{{ actionLabel(p.action.kind) }}
                </button>
              </span>
            </div>
          </TransitionGroup>
        </SectionCard>

        <PageTabs class="mt reveal" style="--i: 3" :tabs="TABS" :labels="t.resources.tabs" v-model:active="active" />

        <SectionCard class="reveal" style="--i: 3" :title="t.resources.tabs[active]" :note="tabNote">
          <Transition name="swap" mode="out-in">
            <div :key="active">
              <template v-if="active === 'compute'">
                <EmptyState v-if="data.instances.length === 0" :title="t.resources.noInstances" />
                <template v-else>
                  <div class="head cols-inst"><span>{{ t.resources.colName }}</span><span>{{ t.resources.colRegion }}</span><span>{{ t.resources.colShape }}</span><span>{{ t.resources.colConfig }}</span><span>{{ t.resources.colBoot }}</span><span>{{ t.resources.colState }}</span></div>
                  <div v-for="i in data.instances" :key="i.id" class="row cols-inst">
                    <button class="link t-xs" @click="openDrawer({ kind: 'instance', id: i.id, tab: 'overview' })">{{ i.name }}</button>
                    <span class="mono t-xs dim">{{ i.region }}</span>
                    <span class="mono t-xs">{{ i.shape }}</span>
                    <span class="mono t-xs dim">{{ i.ocpus }} OCPU · {{ i.memoryGb }} GB</span>
                    <span class="mono t-xs dim">{{ i.bootVolumeGb }} GB</span>
                    <span class="t-xs">{{ stateText(i.lifecycleState) }}</span>
                  </div>
                </template>
              </template>

              <template v-else-if="active === 'storage'">
                <EmptyState v-if="data.volumes.length === 0" :title="t.resources.noVolumes" />
                <template v-else>
                  <div class="head cols-vol"><span>{{ t.resources.colName }}</span><span>{{ t.resources.colType }}</span><span>{{ t.resources.colRegionAd }}</span><span>{{ t.resources.colSize }}</span><span>VPU</span><span>{{ t.resources.colAttached }}</span><span>{{ t.resources.colCreated }}</span><span /></div>
                  <div v-for="v in data.volumes" :key="v.id" class="row cols-vol">
                    <span class="mono t-xs">{{ v.displayName || t.common.unnamed }}</span>
                    <span class="t-xs dim">{{ volumeKind(v) }}</span>
                    <span class="mono t-xs dim">{{ v.region }} · {{ shortAd(v.availabilityDomain) }}</span>
                    <span class="mono t-xs">{{ v.sizeInGbs }} GB</span>
                    <span class="mono t-xs dim">{{ v.vpusPerGb }}</span>
                    <span v-if="v.attachedTo.length" class="t-xs">{{ attachedText(v) }}</span>
                    <span v-else class="t-xs tone-warn">{{ t.resources.unattached }}</span>
                    <span class="mono t-xs dim">{{ dateText(v.timeCreated) }}</span>
                    <span class="row__act">
                      <button v-if="!v.attachedTo.length && v.lifecycleState === 'AVAILABLE'" class="btn btn--xs btn--warning"
                              :disabled="acting.has(v.id)" @click="deleteVolume(v)">
                        <span v-if="acting.has(v.id)" class="btn-spin" aria-hidden="true" />{{ t.common.delete }}
                      </button>
                    </span>
                  </div>
                </template>
              </template>

              <template v-else-if="active === 'publicIp'">
                <EmptyState v-if="data.publicIps.length === 0" :title="t.resources.noIps" />
                <template v-else>
                  <div class="head cols-ip"><span>{{ t.resources.colAddress }}</span><span>{{ t.resources.colType }}</span><span>{{ t.resources.colRegion }}</span><span>{{ t.resources.colBoundTo }}</span><span>{{ t.resources.colState }}</span><span /></div>
                  <div v-for="ip in data.publicIps" :key="ip.id" class="row cols-ip">
                    <span class="mono t-xs"><MaskedText :value="ip.ipAddress" kind="ip" copyable /></span>
                    <span class="t-xs" :class="{ dim: ip.lifetime !== 'RESERVED' }">{{ ip.lifetime === 'RESERVED' ? t.resources.reserved : t.resources.ephemeral }}</span>
                    <span class="mono t-xs dim">{{ ip.region }}</span>
                    <span v-if="ip.instance" class="t-xs">{{ ip.instance.name }}</span>
                    <span v-else-if="ip.assigned" class="t-xs dim">{{ t.resources.boundOther }}</span>
                    <span v-else class="t-xs tone-warn">{{ t.resources.unbound }}</span>
                    <span class="mono t-xs dim">{{ ip.lifecycleState }}</span>
                    <span class="row__act">
                      <button v-if="ip.lifetime === 'RESERVED' && !ip.assigned" class="btn btn--xs btn--warning"
                              :disabled="acting.has(ip.id)" @click="releaseIp(ip)">
                        <span v-if="acting.has(ip.id)" class="btn-spin" aria-hidden="true" />{{ t.resources.release }}
                      </button>
                    </span>
                  </div>
                </template>
              </template>

              <template v-else-if="active === 'network'">
                <EmptyState v-if="data.vcns.length === 0" :title="t.resources.noVcns" :body="t.resources.noVcnsBody" />
                <template v-else>
                  <div class="head cols-vcn"><span>VCN</span><span>{{ t.resources.colRegion }}</span><span>IPv4</span><span>IPv6</span><span>{{ t.resources.colSubnets }}</span><span>{{ t.resources.colIgw }}</span></div>
                  <div v-for="v in data.vcns" :key="v.id" class="row cols-vcn">
                    <span class="mono t-xs">{{ v.displayName }}</span>
                    <span class="mono t-xs dim">{{ v.region }}</span>
                    <span class="mono t-xs dim">{{ v.cidrBlocks.join(', ') || '—' }}</span>
                    <span class="mono t-xs dim">{{ v.ipv6CidrBlocks.join(', ') || t.resources.notEnabled }}</span>
                    <span class="mono t-xs dim">{{ v.subnets }}</span>
                    <span v-if="v.internetGateway?.isEnabled" class="t-xs">{{ t.resources.enabled }}</span>
                    <span v-else class="t-xs tone-warn">{{ v.internetGateway ? t.resources.gwDisabled : t.resources.gwNone }}</span>
                  </div>
                </template>
              </template>

              <template v-else>
                <EmptyState v-if="data.consoleConnections.length === 0" :title="t.resources.noConsoles" />
                <template v-else>
                  <div class="head cols-cc"><span>{{ t.resources.colInstance }}</span><span>{{ t.resources.colRegion }}</span><span>{{ t.resources.colState }}</span><span>{{ t.resources.colFingerprint }}</span><span /></div>
                  <div v-for="c in data.consoleConnections" :key="c.id" class="row cols-cc">
                    <button class="link t-xs" @click="openDrawer({ kind: 'instance', id: c.instance.instanceId, tab: 'console' })">
                      {{ c.instance.name || t.resources.unknownInstance }}
                    </button>
                    <span class="mono t-xs dim">{{ c.region }}</span>
                    <span class="mono t-xs">{{ c.lifecycleState }}</span>
                    <span class="mono t-xs dim">{{ c.fingerprint || '—' }}</span>
                    <span class="row__act">
                      <button class="btn btn--xs" :disabled="acting.has(c.id)" @click="deleteConsole(c)">
                        <span v-if="acting.has(c.id)" class="btn-spin" aria-hidden="true" />{{ t.common.delete }}
                      </button>
                    </span>
                  </div>
                </template>
              </template>
            </div>
          </Transition>
        </SectionCard>
      </div>
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
/* 两侧列随卡片宽度伸缩、数值按内容定宽，进度条吃剩下的；窄屏时不至于被挤成一个点 */
.meters :deep(.meter) { grid-template-columns: minmax(84px, 150px) minmax(60px, 1fr) max-content; }
.meter-unknown { display: grid; grid-template-columns: minmax(84px, 150px) 1fr; gap: 10px; }
.meters p { margin: 4px 0 0; line-height: 16px; }

/* ---------- 加载 ---------- */

/* 骨架：形状与真实内容一致，数据到了原地替换 */
.sk-card { overflow: hidden; }
.sk-card__head { height: 46px; display: flex; align-items: center; padding: 0 16px; border-bottom: 1px solid var(--border-subtle); }
.sk-meter { display: grid; grid-template-columns: minmax(84px, 150px) minmax(60px, 1fr) 90px; gap: 10px; align-items: center; height: 18px; }
.sk-meter .skeleton { height: 10px; }
.sk-pend { display: flex; align-items: center; gap: 10px; padding: 12px 16px; border-bottom: 1px solid var(--border-subtle); }
.sk-pend:last-child { border-bottom: 0; }
.sk-pend__text { display: flex; flex-direction: column; gap: 6px; }
.sk-pend__text .skeleton { height: 12px; }
.sk-tabs { display: flex; gap: 6px; margin-bottom: 16px; }

/* 待处理里的一项被清理掉：向左淡出，其余的平滑补位 */
.pend-leave-active { transition: opacity var(--dur-normal), transform var(--dur-normal); }
.pend-leave-to { opacity: 0; transform: translateX(-12px); }
.pend-move { transition: transform var(--dur-normal) var(--ease-standard); }

.ok-line { padding: 14px 16px; color: var(--success); }
.pend { display: flex; align-items: center; gap: 10px; padding: 10px 16px; border-bottom: 1px solid var(--border-subtle); }
.pend:last-child { border-bottom: 0; }
.pend__mark { width: 14px; flex: 0 0 auto; text-align: center; }
.pend--warn .pend__mark { color: var(--warning); }
.pend--info .pend__mark { color: var(--text-tertiary); }
.pend__text { display: flex; flex-direction: column; gap: 2px; min-width: 0; flex: 1 1 auto; }
.pend__acts { display: flex; gap: 6px; flex: 0 0 auto; }
.tone-warn { color: var(--warning); }

.head, .row { display: grid; align-items: center; gap: 12px; padding: 0 16px; }
.head { height: 34px; background: var(--bg-inset); border-bottom: 1px solid var(--border-subtle); font-size: 11px; color: var(--text-tertiary); }
.row { min-height: 42px; border-bottom: 1px solid var(--border-subtle); }
.row:hover { background: var(--bg-hover); }
.row > *, .head > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row__act { justify-self: end; }
.link { border: 0; background: none; padding: 0; color: var(--text-primary); cursor: pointer; text-align: left; font-weight: 500; }
.link:hover { color: var(--accent); }
.cols-inst { grid-template-columns: minmax(150px, 1.3fr) 130px minmax(160px, 1.2fr) 140px 80px 80px; }
.cols-vol { grid-template-columns: minmax(150px, 1.3fr) 64px 160px 76px 48px minmax(110px, 1fr) 96px 64px; }
.cols-ip { grid-template-columns: 170px 56px 130px minmax(140px, 1fr) 96px 64px; }
.cols-vcn { grid-template-columns: minmax(140px, 1fr) 130px 150px minmax(120px, 1fr) 48px 90px; }
.cols-cc { grid-template-columns: minmax(150px, 1fr) 130px 90px minmax(160px, 1.4fr) 64px; }
</style>
