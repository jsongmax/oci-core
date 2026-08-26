<script setup lang="ts">
/** §4.8 设置：账户安全 / 操作策略 / 外观 / 审计日志 / 关于 */
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useStore } from '@/store'
import MaskedText from '@/components/MaskedText.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import { acctColor, copy } from '@/lib/format'
import { auth, insights, http, type AuditEntryDTO } from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import PageTabs from '@/components/PageTabs.vue'
import SwitchRow from '@/components/SwitchRow.vue'
import KeyValueList from '@/components/KeyValueList.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'

const router = useRouter()
const { state, ask, toast, toastError, updateOptions, logout } = useStore()
const TABS = ['账户安全', '操作策略', '外观', '审计日志', '关于']
const active = ref('账户安全')

/* ---------- 账户安全 ---------- */

const currentPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const changingPassword = ref(false)
const totpEnabled = ref(false)

async function loadMe() {
  try {
    const me = await auth.me()
    totpEnabled.value = me.totpEnabled
  } catch {
    // 读不到就按未启用显示，用户点绑定时会再走一次真实流程。
  }
}

/* ---------- 两步验证的绑定与关闭 ---------- */

const totpBusy = ref(false)
const totpSecret = ref('')
const totpUri = ref('')
const enrollCode = ref('')
const disablePassword = ref('')
const disableCode = ref('')
const showDisable = ref(false)

/** 密钥按 4 位分组，方便在验证器里手动录入。 */
const groupedSecret = computed(() => totpSecret.value.replace(/(.{4})/g, '$1 ').trim())

async function startEnroll() {
  totpBusy.value = true
  try {
    const setup = await auth.totpSetup()
    totpSecret.value = setup.secret
    totpUri.value = setup.uri
    enrollCode.value = ''
  } catch (err) {
    toastError('获取绑定密钥失败', err)
  } finally {
    totpBusy.value = false
  }
}

async function confirmEnroll() {
  if (enrollCode.value.trim().length !== 6) {
    toast({ tone: 'danger', title: '请输入 6 位验证码' })
    return
  }
  totpBusy.value = true
  try {
    await auth.totpEnable(enrollCode.value.trim())
    totpSecret.value = ''
    totpUri.value = ''
    enrollCode.value = ''
    await loadMe()
    toast({ tone: 'success', title: '两步验证已启用' })
  } catch (err) {
    toastError('绑定失败', err)
  } finally {
    totpBusy.value = false
  }
}

function askDisable() {
  ask({
    level: 2,
    title: '关闭两步验证',
    body: '关闭后仅凭口令即可登录本面板。这个面板持有你全部 Oracle 租户的控制权，'
      + '一旦口令泄露就再没有第二道防线。确认要关闭吗？',
    okLabel: '继续关闭',
    onConfirm: () => { showDisable.value = true }
  })
}

async function confirmDisable() {
  if (!disablePassword.value || disableCode.value.trim().length !== 6) {
    toast({ tone: 'danger', title: '需要填写口令和 6 位验证码' })
    return
  }
  totpBusy.value = true
  try {
    const result = await auth.totpDisable(disablePassword.value, disableCode.value.trim())
    showDisable.value = false
    disablePassword.value = ''
    disableCode.value = ''
    await loadMe()
    toast({ tone: 'warning', title: '两步验证已关闭', body: result.message }, 9000)
  } catch (err) {
    toastError('关闭失败', err)
  } finally {
    totpBusy.value = false
  }
}

async function changePassword() {
  if (newPassword.value.length < 10) {
    toast({ tone: 'danger', title: '新口令至少需要 10 个字符' })
    return
  }
  if (newPassword.value !== confirmPassword.value) {
    toast({ tone: 'danger', title: '两次输入的新口令不一致' })
    return
  }

  changingPassword.value = true
  try {
    const result = await auth.changePassword(currentPassword.value, newPassword.value)
    toast({ tone: 'success', title: '口令已更新', body: result.message })
    // 后端会顺带清空所有会话，这里必须跟着跳登录页。
    await logout()
    router.push('/login')
  } catch (err) {
    toastError('修改口令失败', err)
  } finally {
    changingPassword.value = false
    currentPassword.value = ''
    newPassword.value = ''
    confirmPassword.value = ''
  }
}

function revokeAll() {
  ask({
    level: 2,
    title: '强制所有会话下线',
    body: '包括当前这个浏览器在内的全部登录都会立即失效，需要重新登录。怀疑凭据泄露时应当这么做。',
    okLabel: '全部下线',
    onConfirm: async () => {
      try {
        await http.post('/api/auth/sessions/revoke-all')
        toast({ tone: 'warning', title: '所有会话已下线' })
        await logout()
        router.push('/login')
      } catch (err) {
        toastError('操作失败', err)
      }
    }
  })
}

/* ---------- 外观 ---------- */

const accents = [
  { key: 'cyan', label: 'cyan（默认）', hex: '#22D3EE', note: '与八色身份色板色相距离最大' },
  { key: 'sky', label: 'sky', hex: '#38BDF8', note: '偏蓝，弱化终端感' },
  { key: 'emerald', label: 'emerald', hex: '#34D399', note: '与 success 绿接近，不推荐' },
  { key: 'violet', label: 'violet', hex: '#A78BFA', note: '与 acct-6 冲突，不推荐' }
]

/* ---------- 审计日志 ---------- */

const audit = ref<AuditEntryDTO[]>([])
const auditLoading = ref(false)
const auditMore = ref(false)
const auditTotal = ref<number | null>(null)

const AUDIT_PAGE = 100

/**
 * 读取一页审计记录。append 为真时追加到已有列表后面。
 *
 * 翻页用游标（beforeId）而不是 OFFSET：审计表持续写入，OFFSET 会在翻页
 * 途中因为新记录插到最前面而漏掉条目——翻第二页时原本第 100 条已被挤到
 * 第 101 的位置，于是整条被跳过。id 是自增主键，天然单调，不受写入影响。
 */
async function loadAudit(append = false) {
  auditLoading.value = true
  try {
    const beforeId = append && audit.value.length
      ? audit.value[audit.value.length - 1].id
      : undefined
    const res = await insights.audit({ limit: AUDIT_PAGE, beforeId })
    audit.value = append ? [...audit.value, ...res.entries] : res.entries
    auditMore.value = res.hasMore
    if (res.total !== undefined) auditTotal.value = res.total
  } catch (err) {
    toastError('读取审计日志失败', err)
  } finally {
    auditLoading.value = false
  }
}

const codeOf = (id: string) => (id ? state.accounts.find(a => a.id === id)?.code ?? '—' : '—')
const colorOf = (id: string) => {
  const a = id ? state.accounts.find(x => x.id === id) : undefined
  return a ? acctColor(a.colorIndex) : 'var(--border-default)'
}

/** 动作名映射成中文。未登记的动作原样显示，不至于变成空白。 */
const ACTION_LABEL: Record<string, string> = {
  login: '登录', logout: '登出', setup: '首次设置',
  totp_verify: '两步验证', totp_enable: '启用两步验证',
  change_password: '修改口令', sessions_revoke_all: '强制会话下线',
  account_create: '添加账号', account_update: '修改账号',
  account_rotate_key: '轮换密钥', account_delete: '删除账号',
  instance_start: '开机', instance_softstop: '关机', instance_stop: '强制关机',
  instance_softreset: '重启', instance_reset: '强制重启',
  instance_rename: '重命名', instance_reshape: '改配置',
  instance_terminate: '终止实例', instance_launch: '创建实例',
  instance_change_ip: '更换公网 IP', instance_enable_ipv6: '启用 IPv6',
  boot_volume_update: '修改引导卷', boot_volume_detach: '分离引导卷',
  boot_volume_attach: '挂载引导卷', volume_update: '修改块存储',
  security_list_update: '修改安全规则', network_ensure: '自动建网',
  console_create: '建立控制台连接', settings_update: '修改设置',
  channel_create: '添加通知渠道', channel_update: '修改通知渠道',
  channel_delete: '删除通知渠道'
}

const actionLabel = (action: string) =>
  ACTION_LABEL[action] ?? (action.startsWith('instance_bulk_') ? '批量操作' : action)

const auditTime = (iso: string) => {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString('zh-CN', { hour12: false }).replace(/\//g, '-')
}

/**
 * 导出 CSV。走后端的全量导出接口。
 *
 * 原先是把前端已加载的那一页拼成文件——分页之后那只是最新的 100 条，
 * 而按钮上写的是「导出 CSV」。一个悄悄少掉大部分记录的导出比没有更糟。
 */
function exportCsv() {
  if (audit.value.length === 0) {
    toast({ tone: 'warning', title: '没有可导出的记录' })
    return
  }
  // 同源、带 Cookie 的普通下载，交给浏览器处理。BOM 与转义都在后端做了。
  window.location.href = '/api/audit/export'
}

/* ---------- 关于 ---------- */

const aboutItems = computed(() => [
  { k: '版本', v: state.version || 'dev', mono: true },
  { k: '前端', v: 'Vue 3 + TypeScript · 产物 embed 进单二进制' },
  // 只写引擎，不写路径：路径对使用者没有意义，而且是部署细节——
  // 容器里的 /app/data 和宿主机上的卷不是一回事，写出来反而误导。
  { k: '数据库', v: 'SQLite', mono: true },
  { k: '同步间隔', v: `每 ${state.options.syncIntervalMinutes} 分钟全量同步一次，状态变化走 SSE 实时推送` },
  {
    k: '凭据复查',
    v: state.options.checkIntervalHours > 0
      ? `每 ${state.options.checkIntervalHours} 小时自动重跑一次连通性校验`
      : '已关闭——卡片上的校验时间只反映最后一次手动校验'
  },
  { k: '实时连接', v: state.live ? '正常' : '已断开', tone: state.live ? 'var(--success)' : 'var(--warning)' }
])

/** 顶部副标题。总数由后端给，不等于已加载条数——那会让人以为只有这么多。 */
const auditNote = computed(() => {
  const loaded = audit.value.length
  if (auditTotal.value === null) return `已加载 ${loaded} 条 · 只增不改`
  if (auditTotal.value <= loaded) return `共 ${loaded} 条 · 只增不改`
  return `已加载 ${loaded} / 共 ${auditTotal.value} 条 · 只增不改`
})

const retentionGroups = [{
  label: '',
  options: [
    { value: '0', label: '永久保留' },
    { value: '30', label: '30 天' },
    { value: '90', label: '90 天' },
    { value: '180', label: '180 天' },
    { value: '365', label: '一年' }
  ]
}]

/**
 * 改保留期限要过确认。
 *
 * 这是唯一一个会让审计记录消失的开关，而且下一次清理跑起来就是不可逆的。
 * 从"永久"改成有限期时尤其要说清楚会删掉什么。
 */
function confirmRetention(days: number) {
  if (days === state.options.auditRetentionDays) return
  if (days === 0) {
    updateOptions({ auditRetentionDays: 0 })
    return
  }
  ask({
    level: 2,
    title: `审计日志只保留 ${days} 天`,
    body: `超过 ${days} 天的审计记录会在下一次清理时被永久删除，无法恢复。`
      + '如果需要长期留存，先用「导出 CSV」把现有记录存下来。',
    okLabel: `设为 ${days} 天`,
    onConfirm: async () => { await updateOptions({ auditRetentionDays: days }) }
  })
}

const checkIntervalGroups = [{
  label: '',
  options: [
    { value: '0', label: '关闭' },
    { value: '1', label: '每小时' },
    { value: '6', label: '每 6 小时' },
    { value: '12', label: '每 12 小时' },
    { value: '24', label: '每天' },
    { value: '72', label: '每 3 天' },
    { value: '168', label: '每周' }
  ]
}]

/**
 * 全量同步间隔。
 *
 * 最短给到 2 分钟，和后端 UpdateSettings 的下限一致——那边才是真正的关口，
 * 这里只是不把做不到的选项摆出来。一轮同步要对每个（账号 × 区域）发一组
 * 请求，五个账号就是几十个调用；实时性本来就由 SSE 保证，全量只是兜底对账。
 */
const syncIntervalGroups = [{
  label: '',
  options: [
    { value: '2', label: '每 2 分钟' },
    { value: '5', label: '每 5 分钟' },
    { value: '10', label: '每 10 分钟' },
    { value: '30', label: '每 30 分钟' },
    { value: '60', label: '每小时' },
    { value: '360', label: '每 6 小时' },
    { value: '1440', label: '每天' }
  ]
}]

onMounted(() => {
  void loadMe()
  void loadAudit()
})

watch(active, tab => { if (tab === '审计日志') void loadAudit() })
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">设置</h1>
        <p class="page-sub">安全 · 操作策略 · 外观 · 审计</p>
      </div>
    </header>

    <PageTabs :tabs="TABS" v-model:active="active" />

    <template v-if="active === '账户安全'">
      <SectionCard title="两步验证">
        <div class="pad">
          <p class="t-sm">
            当前状态：
            <strong :style="{ color: totpEnabled ? 'var(--success)' : 'var(--warning)' }">
              {{ totpEnabled ? '已启用' : '未启用' }}
            </strong>
          </p>

          <!-- 已启用：提供关闭入口 -->
          <template v-if="totpEnabled">
            <template v-if="!showDisable">
              <p class="t-xs dim">登录时需要口令与验证器上的 6 位数字。</p>
              <button class="btn btn--sm btn--danger" @click="askDisable()">关闭两步验证</button>
            </template>

            <div v-else class="sub-form">
              <p class="t-xs dim">关闭需要同时验证口令与一次有效验证码。</p>
              <div class="field">
                <label for="dp">当前口令</label>
                <input id="dp" v-model="disablePassword" type="password" class="input"
                       autocomplete="current-password" />
              </div>
              <div class="field">
                <label for="dc">验证码</label>
                <input id="dc" v-model="disableCode" class="input mono" maxlength="6"
                       inputmode="numeric" placeholder="6 位数字" />
              </div>
              <div class="sub-form__foot">
                <button class="btn btn--sm" @click="showDisable = false">取消</button>
                <button class="btn btn--sm btn--danger" :disabled="totpBusy" @click="confirmDisable()">
                  {{ totpBusy ? '提交中…' : '确认关闭' }}
                </button>
              </div>
            </div>
          </template>

          <!-- 未启用：就地绑定，不必退出登录 -->
          <template v-else>
            <p class="t-xs dim">这个面板持有你全部 Oracle 租户的控制权，强烈建议启用。</p>

            <button v-if="!totpSecret" class="btn btn--sm" :disabled="totpBusy" @click="startEnroll()">
              {{ totpBusy ? '生成中…' : '开始绑定' }}
            </button>

            <div v-else class="sub-form">
              <p class="t-xs dim">
                在验证器应用中选择「手动输入密钥」，录入下方密钥后填写它显示的验证码。
              </p>
              <div class="secret">
                <code class="secret__value mono">{{ groupedSecret }}</code>
                <button class="btn btn--sm" @click="copy(totpSecret)">复制</button>
              </div>
              <a class="t-xs" :href="totpUri" style="color: var(--accent)">在本机验证器中打开</a>
              <div class="field">
                <label for="ec">验证码</label>
                <input id="ec" v-model="enrollCode" class="input mono" maxlength="6"
                       inputmode="numeric" placeholder="6 位数字" />
              </div>
              <div class="sub-form__foot">
                <button class="btn btn--sm" @click="totpSecret = ''">取消</button>
                <button class="btn btn--sm btn--primary" :disabled="totpBusy" @click="confirmEnroll()">
                  {{ totpBusy ? '提交中…' : '完成绑定' }}
                </button>
              </div>
            </div>
          </template>
        </div>
      </SectionCard>

      <SectionCard title="修改口令" note="修改后所有会话会立即失效" class="mt">
        <form class="pad form" @submit.prevent="changePassword">
          <div class="field">
            <label for="cur">当前口令</label>
            <input id="cur" v-model="currentPassword" type="password" class="input" autocomplete="current-password" />
          </div>
          <div class="field">
            <label for="np">新口令</label>
            <input id="np" v-model="newPassword" type="password" class="input" autocomplete="new-password" />
          </div>
          <div class="field">
            <label for="np2">确认新口令</label>
            <input id="np2" v-model="confirmPassword" type="password" class="input" autocomplete="new-password" />
          </div>
          <button class="btn btn--primary" type="submit" :disabled="changingPassword">
            {{ changingPassword ? '提交中…' : '修改口令' }}
          </button>
        </form>
      </SectionCard>

      <SectionCard title="会话" note="怀疑凭据泄露时的第一件事" class="mt">
        <div class="pad">
          <p class="t-xs dim">
            会话有效期 12 小时，每次请求滑动续期。下线操作会作用于包括当前浏览器在内的全部登录。
          </p>
          <button class="btn btn--danger mt-10" @click="revokeAll">强制所有会话下线</button>
        </div>
      </SectionCard>
    </template>

    <SectionCard v-else-if="active === '操作策略'" title="危险操作"
                 note="服务端强制执行——前端按钮可以被绕过，这些开关不能">
      <SwitchRow :model-value="state.options.allowTerminate" title="允许终止实例"
                 sub="关闭后终止接口直接返回 403，⋯ 菜单中的入口灰显"
                 @update:model-value="v => updateOptions({ allowTerminate: v })" />
      <SwitchRow :model-value="state.options.allowBulk" title="允许批量操作"
                 sub="关闭后行首复选框与批量操作条隐藏，批量接口同时拒绝"
                 @update:model-value="v => updateOptions({ allowBulkActions: v })" />
      <SwitchRow :model-value="state.options.reauthForDanger" title="危险操作需重新验证 TOTP"
                 sub="L2 / L3 确认框追加一次 6 位验证码校验"
                 @update:model-value="v => updateOptions({ requireTotpForDanger: v })" />
      <SwitchRow v-model="state.options.confirmForceRestart" title="强制重启需要二次确认"
                 sub="按 L2 门槛处理。仅前端行为，不影响接口" />
    </SectionCard>

    <SectionCard v-if="active === '操作策略'" title="后台同步"
                 note="全量对账的频率。状态变化走 SSE 实时推送，这里只是兜底">
      <div class="chk">
        <div class="chk__text">
          <div class="chk__title">同步间隔</div>
          <div class="chk__sub">
            一轮会对每个「账号 × 区域」发一组请求，账号多时是几十个调用。调密了只消耗配额，实时性不会更好。
          </div>
        </div>
        <SelectMenu class="chk__select" :model-value="String(state.options.syncIntervalMinutes)"
                    :groups="syncIntervalGroups" :min-width="128" aria-label="后台同步间隔"
                    @update:model-value="v => updateOptions({ syncIntervalMinutes: Number(v) })" />
      </div>
    </SectionCard>

    <SectionCard v-if="active === '操作策略'" title="自动校验凭据"
                 note="凭据会在面板不知情的情况下失效——密钥轮换、IAM 用户被删、账号被封">
      <div class="chk">
        <div class="chk__text">
          <div class="chk__title">复查间隔</div>
          <div class="chk__sub">
            每个账号每轮只发两个只读请求。状态变化时才推通知。
          </div>
        </div>
        <SelectMenu class="chk__select" :model-value="String(state.options.checkIntervalHours)"
                    :groups="checkIntervalGroups" :min-width="128" aria-label="凭据复查间隔"
                    @update:model-value="v => updateOptions({ checkIntervalHours: Number(v) })" />
      </div>
    </SectionCard>

    <SectionCard v-if="active === '操作策略'" title="审计日志保留"
                 note="默认永久保留——审计记录删掉就再也找不回来了">
      <div class="chk">
        <div class="chk__text">
          <div class="chk__title">保留期限</div>
          <div class="chk__sub">
            超期记录每天清理一次。按几十条／天算，一年不过几 MB——存储不构成开启的理由。
          </div>
        </div>
        <SelectMenu class="chk__select" :model-value="String(state.options.auditRetentionDays)"
                    :groups="retentionGroups" :min-width="128" aria-label="审计日志保留期限"
                    @update:model-value="v => confirmRetention(Number(v))" />
      </div>
    </SectionCard>

    <template v-else-if="active === '外观'">
      <SectionCard title="主题与密度">
        <SwitchRow :model-value="state.theme === 'dark'" title="深色主题" sub="暗色是默认，Light 完整对等"
                   :value="state.theme === 'dark' ? 'Dark' : 'Light'"
                   @update:model-value="state.theme = state.theme === 'dark' ? 'light' : 'dark'" />
        <SwitchRow :model-value="state.density === 'compact'" title="紧凑密度" sub="实例表行高 56px / 44px"
                   :value="state.density === 'comfy' ? '56px' : '44px'"
                   @update:model-value="state.density = state.density === 'comfy' ? 'compact' : 'comfy'" />
        <SwitchRow v-model="state.options.reduceMotion" title="减弱动效" sub="停止所有循环动画，过渡时长归零" />
      </SectionCard>
      <SectionCard title="强调色" note="账号身份色不受影响" class="mt">
        <button v-for="a in accents" :key="a.key" class="row cols-accent accent"
                :class="{ 'is-picked': state.accentName === a.key }" @click="state.accentName = a.key">
          <span class="acct-bar" :style="{ background: a.hex }" />
          <span class="t-xs">{{ a.label }}</span>
          <span class="mono t-xs" :style="{ color: a.hex }">{{ a.hex }}</span>
          <span class="t-xs dim">{{ a.note }}</span>
          <span class="t-xs" :style="{ color: 'var(--accent)' }">{{ state.accentName === a.key ? '当前' : '' }}</span>
        </button>
      </SectionCard>
    </template>

    <SectionCard v-else-if="active === '审计日志'" title="审计日志" :note="auditNote">
      <template #action>
        <button class="btn btn--sm" :disabled="auditLoading" @click="loadAudit(false)">刷新</button>
        <button class="btn btn--sm" @click="exportCsv">导出 CSV</button>
      </template>

      <SkeletonRows v-if="auditLoading" :rows="6" />
      <EmptyState v-else-if="audit.length === 0" title="暂无审计记录"
                  body="所有账号与实例操作都会记录在这里。" />
      <template v-else>
        <div class="head cols-audit">
          <span>时间</span><span>动作</span><span>账号</span><span>目标资源</span><span>详情</span><span>来源 IP</span><span>结果</span>
        </div>
        <div v-for="r in audit" :key="r.id" class="row cols-audit">
          <span class="acct-bar" :style="{ background: colorOf(r.accountId) }" />
          <span class="mono t-xs dim">{{ auditTime(r.createdAt) }}</span>
          <span class="t-xs">{{ actionLabel(r.action) }}</span>
          <span class="mono t-xs" :style="{ color: colorOf(r.accountId), fontWeight: 600 }">{{ codeOf(r.accountId) }}</span>
          <span class="mono t-xs dim">{{ r.target || '—' }}</span>
          <span class="t-xs dim-3">{{ r.detail || '—' }}</span>
          <span class="mono t-xs dim-3"><MaskedText :value="r.ip || '—'" kind="ip" /></span>
          <span class="t-xs" :style="{ color: r.result === 'ok' ? 'var(--success)' : 'var(--danger)', fontWeight: 600 }">
            {{ r.result === 'ok' ? '成功' : '失败' }}
          </span>
        </div>

        <div class="more">
          <button v-if="auditMore" class="btn btn--sm" :disabled="auditLoading"
                  @click="loadAudit(true)">
            {{ auditLoading ? '加载中…' : `加载更早的 ${AUDIT_PAGE} 条` }}
          </button>
          <span v-else class="t-2xs dim-3">已经到最早的一条了</span>
        </div>
      </template>
    </SectionCard>

    <SectionCard v-else title="关于">
      <KeyValueList :items="aboutItems" />
    </SectionCard>
  </div>
</template>

<style scoped>
.chk { display: flex; align-items: center; gap: 16px; padding: 14px 16px; }
.chk__text { flex: 1 1 auto; min-width: 0; }
.chk__title { font-size: 13px; font-weight: 600; }
.chk__sub { margin-top: 4px; font-size: 11px; color: var(--text-secondary); line-height: 1.6; }
.chk__select { flex: 0 0 auto; width: 128px; }

.mt { margin-top: 16px; }

/* 同一个标签页里相邻的卡片一律留白。
   原先靠每张卡各自加 .mt，漏一张就和上一张贴死——而漏了不会报错，
   只是看着挤，加卡片的人也不会想到还有这么个约定。 */
.page > .card + .card { margin-top: 16px; }
.mt-6 { margin-top: 6px; }
.mt-10 { margin-top: 10px; }
.pad { padding: 16px 20px; }
.form { display: flex; flex-direction: column; gap: 14px; max-width: 360px; }
.sub-form {
  display: flex; flex-direction: column; gap: 12px; max-width: 360px;
  padding: 14px; border-radius: var(--radius-md);
  border: 1px solid var(--border-subtle); background: var(--bg-inset);
}
.sub-form__foot { display: flex; gap: 8px; }
.secret {
  display: flex; align-items: center; gap: 8px;
  padding: 9px 11px; border-radius: var(--radius-sm);
  border: 1px solid var(--border-default); background: var(--bg-surface);
}
.secret__value { flex: 1 1 auto; font-size: 12px; letter-spacing: 0.06em; word-break: break-all; }
.head, .row { display: grid; align-items: center; gap: 12px; padding: 0 20px 0 14px; }
.head { height: 34px; background: var(--bg-inset); border-bottom: 1px solid var(--border-subtle); font-size: 11px; color: var(--text-tertiary); }
.row { min-height: 46px; border-bottom: 1px solid var(--border-subtle); position: relative; }
.row:hover { background: var(--bg-hover); }
.row > span:not(.acct-bar), .head > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row .btn { justify-self: end; }
.accent { width: 100%; border: 0; border-bottom: 1px solid var(--border-subtle); background: transparent; color: inherit; cursor: pointer; text-align: left; }
.accent.is-picked { background: var(--accent-soft); }
.cols-accent { grid-template-columns: 150px 110px 1fr 60px; }
.cols-audit { grid-template-columns: 150px 110px 70px minmax(130px, 1fr) minmax(120px, 1fr) 110px 70px; }

.more {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 14px 0 4px;
}

</style>
