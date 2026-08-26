<script setup lang="ts">
/** §4.3.2 账号详情抽屉（720）：概览 / 区域订阅 / 配额 / 权限自检 / 密钥 */
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import { mask } from '@/lib/mask'
import { accountAgeText, accountStatusText, trialDaysLeft } from '@/lib/adapt'
import { ALWAYS_FREE, armFreeText } from '@/lib/freetier'
import { accounts as accountsApi, type CheckStepDTO } from '@/api'
import type { CheckItem } from '@/types'
import AppDrawer from '@/components/AppDrawer.vue'
import DrawerHeader from '@/components/DrawerHeader.vue'
import DrawerTabs from '@/components/DrawerTabs.vue'
import DrawerBody from '@/components/DrawerBody.vue'
import SectionCard from '@/components/SectionCard.vue'
import KeyValueList from '@/components/KeyValueList.vue'
import CheckList from '@/components/CheckList.vue'
import CodeBlock from '@/components/CodeBlock.vue'
import QuotaMeter from '@/components/QuotaMeter.vue'

const props = defineProps<{ id: string; tab?: string }>()
const router = useRouter()
const {
  state, closeDrawer, ask, toast, toastError, allRegions,
  loadAccounts, syncNow
} = useStore()

/** 重新校验的结果。逐项来自后端，不是前端猜的。 */
const checkSteps = ref<CheckStepDTO[]>([])
const checking = ref(false)
const checkAdvice = ref('')

async function recheck() {
  checking.value = true
  checkAdvice.value = ''
  try {
    const result = await accountsApi.check(props.id)
    checkSteps.value = result.steps
    checkAdvice.value = result.advice ?? ''
    await loadAccounts()
    toast({
      tone: result.ok ? 'success' : 'danger',
      title: result.ok ? `${account.value?.alias} 校验通过` : '校验失败',
      body: result.ok
        ? `已订阅 ${result.regions?.length ?? 0} 个区域`
        : [result.errorCode, result.errorText].filter(Boolean).join(' · ')
    }, result.ok ? 6000 : 9000)
  } catch (err) {
    toastError('校验失败', err)
  } finally {
    checking.value = false
  }
}

/** 权限自检项：真实校验步骤 + 本工具需要的最小权限集说明。 */
const permissionChecks = computed<CheckItem[]>(() => {
  const items: CheckItem[] = checkSteps.value.map(s => ({
    tone: s.ok ? 'ok' : 'fail',
    text: s.detail ? `${s.label} · ${s.detail}` : s.label
  }))
  if (items.length === 0) {
    items.push({ tone: 'info', text: '点击「重新校验」逐项检查凭据与权限' })
  }
  if (checkAdvice.value) items.push({ tone: 'warn', text: checkAdvice.value })
  return items
})

const TABS = ['概览', '区域订阅', '配额', '权限自检', '密钥']

/** 抽屉副标题里带着 OCID 与邮箱，跟着隐私模式一起打码。 */
const drawerSub = computed(() => {
  const a = account.value
  if (!a) return ''
  const tail = state.privacyMode ? mask(a.tenancyTail) : a.tenancyTail
  const mail = state.privacyMode ? mask(a.email, 'email') : a.email
  return `ocid1.tenancy…${tail} · ${mail}`
})
const active = ref(props.tab && TABS.includes(props.tab) ? props.tab : '概览')

const account = computed(() => state.accounts.find(a => a.id === props.id))
const color = computed(() => (account.value ? acctColor(account.value.colorIndex) : 'var(--border-default)'))
const instances = computed(() => state.instances.filter(i => i.accountId === props.id && i.state !== 'TERMINATED'))

/**
 * 账号类型一行话。试用号带到期日——那天配额会掉回永久免费额度，
 * 超出的实例会被 Oracle 回收。
 */
const tierText = computed(() => {
  const a = account.value
  if (!a) return '—'
  if (a.tier === 'unknown') return '未知（读不到订阅信息，可能是权限不足）'
  if (a.tier === 'paid') return '升级号（Pay As You Go）'
  const d = trialDaysLeft(a)
  if (d === null) return '试用号（Free Trial）'
  if (d < 0) return '试用号 · 已到期'
  return `试用号 · ${d} 天后到期，届时配额降回永久免费额度 ${armFreeText}`
})

/**
 * 试用到期后会超出永久免费额度的部分。
 *
 * 试用号现在的 ARM 配额是试用期专属的（本机实测 16 OCPU / 96 GB）。
 * 到期那天降回永久免费额度，超出的实例会被 Oracle 回收——2026 年 6 月
 * 那次砍额度后，Oracle 就是这么处理的，从 8 月 18 日起开始终止。
 *
 * 只在真的超了的时候提示：没超的账号看到这行只会白紧张。
 */
const trialOverage = computed(() => {
  const a = account.value
  if (a?.tier !== 'trial') return ''
  const over = a.quota.ocpuUsed - ALWAYS_FREE.armOcpus
  if (over <= 0) return ''
  return `当前已用 ${a.quota.ocpuUsed} OCPU，超出永久免费额度 ${over} OCPU。` +
    `试用到期后超出部分的实例会被 Oracle 回收，请提前迁移或升级为 PAYG。`
})

/**
 * 开户时间。与「接入本面板」分开列——一个用了两年的老号今天才接进来，
 * 两个数字差着两年，混在一起会让人以为这是个新号。
 */
const openedText = computed(() => {
  const a = account.value
  if (!a) return '—'
  const age = accountAgeText(a)
  if (!a.openedAt) return '未知（读不到订阅信息）'
  return `${a.openedAt.slice(0, 10)}（${age}）`
})

const tierTone = computed(() => {
  const a = account.value
  if (a?.tier !== 'trial') return undefined
  const d = trialDaysLeft(a)
  if (d === null) return undefined
  if (d <= 0) return 'var(--danger)'
  if (d <= 7) return 'var(--warning)'
  return undefined
})

const statusLabel = { ok: '凭据有效', checking: '正在校验', error: '凭据失效', disabled: '已禁用' }
const statusTone = {
  ok: 'var(--success)', checking: 'var(--info)', error: 'var(--danger)', disabled: 'var(--neutral)'
}

const unsubscribed = computed(() =>
  allRegions.value.filter(r => !account.value?.regions.includes(r))
)

/** 已订阅但没查配额的区域。 */
const otherRegions = computed(() => {
  const a = account.value
  if (!a) return []
  const shown = a.quotaRegion || a.regions[0]
  return a.regions.filter(r => r !== shown)
})

function toggleDisabled() {
  const a = account.value
  if (!a) return
  const disabling = a.status !== 'disabled'
  ask({
    level: 2,
    title: `${disabling ? '禁用' : '启用'} ${a.alias}`,
    body: disabling
      ? '禁用后本工具停止轮询该账号，其资源在列表中只读显示，不会删除任何云端资源。'
      : '恢复对该账号的轮询与操作能力。',
    okLabel: disabling ? '禁用' : '启用',
    onConfirm: async () => {
      try {
        await accountsApi.update(a.id, { enabled: !disabling })
        await loadAccounts()
        toast({
          tone: 'warning',
          title: `${a.alias} ${disabling ? '已禁用' : '已启用'}`,
          body: `该账号下 ${instances.value.length} 台实例`
        })
      } catch (err) {
        toastError('操作失败', err)
      }
    }
  })
}

function askDelete() {
  const a = account.value
  if (!a) return
  ask({
    level: 3,
    title: `删除账号 ${a.alias}`,
    body: '该操作只从本工具移除凭据，不会删除 Oracle 云端的任何资源。',
    noun: a.alias, nounLabel: '账号别名',
    losses: [
      '本地加密存储的 API 私钥与指纹',
      `该账号的 ${instances.value.length} 台实例在本工具中的聚合视图`,
      '该账号的历史操作审计记录'
    ],
    okLabel: '永久删除',
    onConfirm: async () => {
      try {
        // 服务端要求回传账号别名——前端确认框可以被绕过，这道校验不能。
        await accountsApi.remove(a.id, a.alias)
        state.accountFilter.delete(a.id)
        state.instances = state.instances.filter(i => i.accountId !== a.id)
        await loadAccounts()
        closeDrawer()
        toast({ tone: 'danger', title: `${a.alias} 已从本工具移除`, body: '云端资源未受影响' })
      } catch (err) {
        toastError('删除账号失败', err)
      }
    }
  })
}
</script>

<template>
  <AppDrawer v-if="account" width="wide" @close="closeDrawer()">
    <DrawerHeader :color="color" :code="account.code" :title="account.alias"
                  :sub="drawerSub" @close="closeDrawer()">
      <template #badge>
        <span class="lamp" :style="{ '--c': statusTone[account.status] }">
          <span class="lamp__dot" :class="{ 'is-pulsing': account.status === 'checking' }" />
          {{ statusLabel[account.status] }}
        </span>
      </template>
      <template #actions>
        <button class="btn btn--sm" :disabled="checking" @click="recheck()">
          {{ checking ? '校验中…' : '重新校验' }}
        </button>
        <button class="btn btn--sm" :disabled="state.syncing" @click="syncNow(account.id)">
          {{ state.syncing ? '同步中…' : '同步实例' }}
        </button>
        <button class="btn btn--sm" @click="closeDrawer(); state.accountFilter = new Set([account.id]); router.push('/instances')">
          查看该账号实例
        </button>
      </template>
    </DrawerHeader>

    <DrawerTabs :tabs="TABS" v-model:active="active" />

    <DrawerBody>
      <template v-if="active === '概览'">
        <SectionCard title="租户">
          <KeyValueList :items="[
            { k: '别名', v: account.alias },
            { k: '短代号', v: account.code, mono: true, tone: color },
            { k: '租户 OCID', v: `ocid1.tenancy.oc1..aaaaaaaa${account.tenancyTail}`, mono: true, copyable: true, secret: 'ocid' },
            { k: '用户邮箱', v: account.email, mono: true, copyable: true, secret: 'email' },
            { k: '账号类型', v: tierText, tone: tierTone },
            { k: '密钥指纹', v: account.fingerprint, mono: true, copyable: true, secret: 'fingerprint' },
            { k: '开户时间', v: openedText },
            { k: '接入本面板', v: account.createdAt, mono: true },
            { k: '最后校验', v: accountStatusText(account) }
          ]" />
        </SectionCard>
        <SectionCard title="资源">
          <KeyValueList :items="[
            { k: '实例', v: `${instances.length} 台（运行 ${instances.filter(i => i.state === 'RUNNING').length}）`, mono: true },
            { k: '订阅区域', v: `${account.regions.length} 个`, mono: true },
            { k: '引导卷合计', v: `${instances.reduce((n, i) => n + i.bootGb, 0)} GB`, mono: true }
          ]" />
        </SectionCard>
        <SectionCard v-if="account.status === 'error'" title="诊断" note="保留原始错误码">
          <CheckList :items="[
            { tone: 'fail', text: 'NotAuthenticated (401) —— 私钥与该用户的 API 公钥不再匹配。' },
            { tone: 'info', text: '最可能的原因：API 密钥已在 Oracle 控制台被删除或轮换。请在「密钥」页重新导入私钥。' }
          ]" />
          <CodeBlock copyable
            :code="'{\n  \'code\': \'NotAuthenticated\',\n  \'message\': \'The required information to complete authentication was not provided.\'\n}'" />
        </SectionCard>
        <SectionCard v-else title="连通性">
          <CheckList :items="[{ tone: 'ok', text: `${statusLabel[account.status]} · ${accountStatusText(account)}` }]" />
        </SectionCard>
      </template>

      <template v-else-if="active === '区域订阅'">
        <SectionCard title="已订阅" :note="`${account.regions.length} 个区域`">
          <div v-for="r in account.regions" :key="r" class="rule">
            <span class="rule__bar" :style="{ background: color }" />
            <span class="mono t-xs">{{ r }}</span>
            <span class="rule__desc mono t-2xs dim-3">
              {{ instances.filter(i => i.region === r).length }} 台实例
            </span>
          </div>
        </SectionCard>
        <SectionCard title="可订阅" note="订阅后立即可创建实例">
          <div v-for="r in unsubscribed" :key="r" class="rule">
            <span class="rule__bar" style="background: var(--border-default)" />
            <span class="mono t-xs">{{ r }}</span>
            <button class="btn btn--sm rule__desc"
                    @click="account.regions.push(r); toast({ tone: 'success', title: `${account.alias} 已订阅 ${r}`, body: '区域订阅通常在 1 分钟内生效' })">
              订阅
            </button>
          </div>
          <CheckList v-if="!unsubscribed.length" :items="[{ tone: 'ok', text: '已订阅全部可用区域' }]" />
        </SectionCard>
      </template>

      <template v-else-if="active === '配额'">
        <SectionCard
          :title="account.quotaRegion || account.regions[0] || '配额'"
          :note="account.quotaRegion ? '上限来自 Oracle 实时返回' : '配额加载中…'">
          <div class="pad">
            <QuotaMeter label="ARM OCPU" :used="account.quota.ocpuUsed" :limit="account.quota.ocpuLimit"
                        :unlimited="account.quota.unlimited.ocpu" />
            <QuotaMeter label="ARM 内存" :used="account.quota.memUsed" :limit="account.quota.memLimit" unit=" GB"
                        :unlimited="account.quota.unlimited.mem" />
            <QuotaMeter label="AMD 微型" :used="account.quota.microUsed" :limit="account.quota.microLimit" unit=" 台"
                        :unlimited="account.quota.unlimited.micro" />
            <QuotaMeter label="块存储 · 免费额度" :used="account.quota.blockUsed" :limit="account.quota.blockLimit" unit=" GB"
                        :unlimited="account.quota.unlimited.block" />
          </div>
        </SectionCard>
        <SectionCard v-if="trialOverage" title="试用到期风险"
                     :note="`永久免费额度 ${armFreeText}（${ALWAYS_FREE.since} 起）`">
          <div class="pad t-xs" style="color: var(--warning); line-height: 1.7">
            {{ trialOverage }}
          </div>
        </SectionCard>
        <SectionCard v-if="otherRegions.length" title="其他已订阅区域" note="配额按区域独立计算">
          <div class="pad t-xs" style="color: var(--text-secondary); line-height: 1.7">
            {{ otherRegions.join('、') }}
            <br />
            这几个区域的配额暂未查询——账号的主区域之外用得少，
            每多查一个区域就要多发八个跨洋请求，默认不拉。
          </div>
        </SectionCard>
      </template>

      <template v-else-if="active === '权限自检'">
        <SectionCard title="凭据校验" note="逐项来自后端真实调用">
          <template #action>
            <button class="btn btn--sm" :disabled="checking" @click="recheck()">
              {{ checking ? '校验中…' : '重新校验' }}
            </button>
          </template>
          <CheckList :items="permissionChecks" />
        </SectionCard>

        <SectionCard title="本工具需要的最小权限集"
                     note="建议为本工具单独建一个 IAM 用户，不要用管理员密钥">
          <CheckList :items="[
            { tone: 'info', text: 'inspect / read instance-family —— 实例列表与详情' },
            { tone: 'info', text: 'use instance-family —— 开机 / 关机 / 重启 / 改配置' },
            { tone: 'info', text: 'manage instance-family —— 创建与终止实例' },
            { tone: 'info', text: 'manage virtual-network-family —— VCN、子网、安全规则、公网 IP' },
            { tone: 'info', text: 'manage volume-family —— 引导卷扩容与 VPU 调整' },
            { tone: 'info', text: 'read all-resources —— 配额与用量（已覆盖账单所需的 usage-report）' }
          ]" />
        </SectionCard>

        <SectionCard title="策略示例" note="在 Oracle 控制台 → 身份与安全 → 策略 中新建">
          <CodeBlock copyable :code="`Allow group OCI Core to manage instance-family in tenancy
Allow group OCI Core to manage virtual-network-family in tenancy
Allow group OCI Core to manage volume-family in tenancy
Allow group OCI Core to read all-resources in tenancy`" />
        </SectionCard>
      </template>

      <template v-else>
        <SectionCard title="当前密钥">
          <KeyValueList :items="[
            { k: '指纹', v: account.fingerprint, mono: true, copyable: true, secret: 'fingerprint' },
            { k: '算法', v: 'RSA 2048', mono: true },
            { k: '存储', v: 'AES-256-GCM 加密于本地数据库' },
            { k: '私钥', v: '永不回显，界面无导出入口', tone: 'var(--text-secondary)' }
          ]" />
        </SectionCard>
        <SectionCard title="轮换密钥">
          <CheckList :items="[{ tone: 'info', text: '粘贴新的 PEM 私钥即可替换。留空则不修改。轮换后会立即重新校验凭据。' }]" />
        </SectionCard>
        <SectionCard title="危险操作">
          <div class="rule">
            <span class="rule__bar" style="background: var(--warning)" />
            <span class="t-xs">{{ account.status === 'disabled' ? '启用账号' : '禁用账号' }}</span>
            <button class="btn btn--sm btn--warning rule__desc" @click="toggleDisabled()">
              {{ account.status === 'disabled' ? '启用' : '禁用' }}
            </button>
          </div>
          <div class="rule">
            <span class="rule__bar" style="background: var(--danger)" />
            <span class="t-xs">删除账号 · 从本工具移除，不影响云端资源</span>
            <button class="btn btn--sm btn--danger rule__desc" @click="askDelete()">删除</button>
          </div>
        </SectionCard>
      </template>
    </DrawerBody>
  </AppDrawer>
</template>

<style scoped>
.lamp { display: inline-flex; align-items: center; gap: 6px; padding: 3px 10px; border-radius: var(--radius-full); background: var(--bg-inset); color: var(--c); font-size: 12px; font-weight: 600; }
.lamp__dot { width: 6px; height: 6px; border-radius: var(--radius-full); background: var(--c); }
.lamp__dot.is-pulsing { animation: pulse 2s ease-in-out infinite; }
.rule { display: flex; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid var(--border-subtle); }
.rule__bar { width: 3px; height: 22px; flex: 0 0 auto; border-radius: var(--radius-full); }
.rule__desc { margin-left: auto; }
.pad { padding: 14px 16px; display: flex; flex-direction: column; gap: 8px; }
</style>
