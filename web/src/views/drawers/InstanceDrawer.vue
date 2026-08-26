<script setup lang="ts">
/** §4.4.3 实例详情抽屉（720）：概览 / 网络 / 存储 / 监控 / 控制台 / 操作记录 */
import { computed, onMounted, ref, watch } from 'vue'
import { useStore } from '@/store'
import { isTransitional } from '@/lib/lifecycle'
import { acctColor } from '@/lib/format'
import { relativeTime, instanceUptime } from '@/lib/adapt'
import {
  instances as instancesApi, insights, storage, errorText,
  type AuditEntryDTO, type InstanceDetailDTO, type MetricsDTO
} from '@/api'
import type * as D from '@/api/types'
import AppDrawer from '@/components/AppDrawer.vue'
import DrawerHeader from '@/components/DrawerHeader.vue'
import DrawerTabs from '@/components/DrawerTabs.vue'
import DrawerBody from '@/components/DrawerBody.vue'
import SectionCard from '@/components/SectionCard.vue'
import KeyValueList from '@/components/KeyValueList.vue'
import CheckList from '@/components/CheckList.vue'
import CodeBlock from '@/components/CodeBlock.vue'
import QuotaMeter from '@/components/QuotaMeter.vue'
import StateBadge from '@/components/StateBadge.vue'
import BootVolumeEditor from '@/components/BootVolumeEditor.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import TrafficChart from '@/components/TrafficChart.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'

const props = defineProps<{ id: string; tab?: string }>()
const {
  state, accountById, closeDrawer, ask, toast, toastError,
  start, stop, restart, terminate
} = useStore()

const TABS = ['概览', '网络', '存储', '监控', '控制台', '操作记录']
const active = ref(props.tab && TABS.includes(props.tab) ? props.tab : '概览')

const inst = computed(() => state.instances.find(i => i.id === props.id))

/** 运行时长。未观测到本次开机时退回"创建至今"，并把这件事说清楚。 */
const uptimeDetail = computed(() => {
  const i = inst.value
  if (!i) return '—'
  const u = instanceUptime(i)
  return u.approx && u.text !== '—' ? `${u.text}（创建至今，未观测到本次开机）` : u.text
})
const account = computed(() => (inst.value ? accountById(inst.value.accountId) : undefined))
const color = computed(() => (account.value ? acctColor(account.value.colorIndex) : 'var(--border-default)'))
const locked = computed(() => !!inst.value && isTransitional(inst.value.state))

/* ---------- 详情 ---------- */

const detail = ref<InstanceDetailDTO | null>(null)
const detailLoading = ref(false)
const detailError = ref('')

async function loadDetail() {
  detailLoading.value = true
  detailError.value = ''
  try {
    detail.value = await instancesApi.detail(props.id)
  } catch (err) {
    detailError.value = errorText(err)
  } finally {
    detailLoading.value = false
  }
}

const primaryVnic = computed(() =>
  detail.value?.vnics?.find(v => v.isPrimary) ?? detail.value?.vnics?.[0]
)

const cloudInit = computed(() => {
  const raw = detail.value?.metadata?.user_data
  if (!raw) return ''
  try {
    // 后端存的是 base64，这里解回来给用户看原文。
    return decodeURIComponent(escape(atob(raw)))
  } catch {
    return raw
  }
})

const sshKeys = computed(() => detail.value?.metadata?.ssh_authorized_keys ?? '')

/* ---------- 监控 ---------- */

const metrics = ref<MetricsDTO | null>(null)
const metricsLoading = ref(false)
const metricHours = ref(24)

async function loadMetrics() {
  metricsLoading.value = true
  try {
    metrics.value = await instancesApi.metrics(props.id, metricHours.value)
  } catch (err) {
    toastError('读取监控数据失败', err)
  } finally {
    metricsLoading.value = false
  }
}

const seriesOf = (name: string) =>
  metrics.value?.series.find(s => s.metric === name)?.datapoints?.map(d => d.value) ?? []

const metricLabels = computed<[string, string]>(() => {
  if (!metrics.value) return ['', '']
  const fmt = (iso: string) =>
    new Date(iso).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
  return [fmt(metrics.value.start), fmt(metrics.value.end)]
})

/** 流量指标是每秒字节数，换算成更好读的 Mbps。 */
const toMbps = (values: number[]) => values.map(v => Math.round((v * 8) / 1_000_000 * 100) / 100)

const noMetricData = computed(() =>
  !!metrics.value && metrics.value.series.every(s => (s.datapoints?.length ?? 0) === 0)
)

/* ---------- 控制台 ---------- */

const sshPublicKey = ref('')
const consoleCreating = ref(false)
const consoleResult = ref<{ serialConsoleCommand: string; vncConsoleCommand: string } | null>(null)

async function createConsole() {
  const key = sshPublicKey.value.trim()
  if (!key) {
    toast({ tone: 'warning', title: '请先填入 SSH 公钥' })
    return
  }
  // 本地先拦一次，省掉一次注定失败的往返。
  if (!key.startsWith('ssh-rsa')) {
    toast({
      tone: 'warning',
      title: 'Oracle 的串行控制台只接受 RSA 公钥',
      body: '不支持 ed25519 / ecdsa。另生成一把 RSA 即可，实例登录用的密钥不受影响。',
      command: 'ssh-keygen -t rsa -b 4096 -f ~/.ssh/oci_console'
    })
    return
  }
  consoleCreating.value = true
  try {
    const result = await instancesApi.console(props.id, key)
    consoleResult.value = result
    toast({ tone: 'success', title: '控制台连接已建立', body: result.notice })
  } catch (err) {
    toastError('建立控制台连接失败', err)
  } finally {
    consoleCreating.value = false
  }
}

/* ---------- 操作记录 ---------- */

const auditRows = ref<AuditEntryDTO[]>([])

async function loadAudit() {
  if (!inst.value) return
  try {
    const { entries } = await insights.audit({ accountId: inst.value.accountId, limit: 200 })
    // 后端按账号过滤，这里再按目标资源名收敛到这一台机器。
    auditRows.value = entries.filter(e => e.target === inst.value?.name)
  } catch {
    auditRows.value = []
  }
}

/* ---------- 存储 ---------- */

async function detachBoot() {
  // 注意用的是挂载关系 ID 而不是卷 ID——两者混用会直接 404。
  const attachmentId = detail.value?.bootVolumeAttachmentId
  const inst_ = inst.value
  if (!inst_ || !attachmentId) return

  ask({
    level: 2,
    title: `分离 ${inst_.name} 的引导卷`,
    body: '实例必须先关机。分离后该实例无法启动，直到重新挂载引导卷。这是「救援模式」的第一步。',
    okLabel: '分离引导卷',
    onConfirm: async () => {
      try {
        await storage.detachBootVolume(attachmentId, inst_.accountId, inst_.region)
        toast({ tone: 'warning', title: '引导卷已分离', body: '可以把它挂到另一台实例上修复' })
        await loadDetail()
      } catch (err) {
        toastError('分离引导卷失败', err)
      }
    }
  })
}

/* ---------- 救援模式 ---------- */

/**
 * 引导卷是否已经卸下来了。
 *
 * 只看「关机 + 没有挂载关系」，不能顺带要求 detail.bootVolume 有值：详情里的
 * 引导卷是拿缓存的 BootVolumeID 去查的，而那个 ID 本身来自挂载关系——卷一分离，
 * 同步就找不到它，ID 被清空，于是卷也查不出来。真分离了反而认不出来。
 */
const bootDetached = computed(() =>
  !!inst.value && inst.value.state === 'STOPPED' && !detail.value?.bootVolumeAttachmentId)

/* 分离之后卷「无主」了，只能从区域里的引导卷列表里把它找回来。 */
const looseVolumes = ref<D.BootVolumeDTO[]>([])
const looseLoading = ref(false)
const looseError = ref('')
const chosenVolume = ref('')

async function loadLooseVolumes() {
  const i = inst.value
  if (!i) return
  looseLoading.value = true
  looseError.value = ''
  try {
    const res = await storage.bootVolumes(i.accountId, i.region, i.adFull)
    looseVolumes.value = (res.bootVolumes ?? []).filter(v => v.lifecycleState === 'AVAILABLE')
    // 默认选中最像本机的那块：优先详情里已知的卷，其次按名字猜。
    // Oracle 建实例时把引导卷取成同名，绝大多数情况一猜一个准。
    const known = detail.value?.bootVolume?.id
    const byName = looseVolumes.value.find(v => v.displayName === i.name)
    chosenVolume.value = known || byName?.id || ''
  } catch (err) {
    // 不能静默吞掉：读失败和「这个可用域里确实没有卷」在界面上长得一模一样，
    // 都是一个空下拉框，人会以为卷丢了。
    looseVolumes.value = []
    looseError.value = errorText(err)
  } finally {
    looseLoading.value = false
  }
}

// 进入「已分离」状态时才去拉列表——正常实例没必要多打一次 OCI。
watch(bootDetached, on => { if (on) loadLooseVolumes() }, { immediate: true })

const looseVolumeGroups = computed(() => [{
  label: `${inst.value?.ad ?? ''} 的引导卷`,
  options: looseVolumes.value.map(v => ({
    value: v.id, label: `${v.displayName} · ${v.sizeInGBs} GB`
  }))
}])

/**
 * 能接收这块卷的实例。
 *
 * 三个硬条件缺一不可：同账号（跨账号看不见对方的资源）、同区域、
 * **同可用域**——卷不能跨 AD 挂载，这是最容易踩的一条，Oracle 只会回一句
 * 语焉不详的 400。另外目标机得是运行中的，否则挂上去也没法登进去改文件。
 */
const rescueTargets = computed(() => {
  const i = inst.value
  if (!i) return []
  return state.instances.filter(t =>
    t.id !== i.id &&
    t.accountId === i.accountId &&
    t.region === i.region &&
    t.adFull === i.adFull &&
    t.state === 'RUNNING')
})

const rescueTarget = ref('')
const rescueBusy = ref(false)

/**
 * 挂载后在救援机上要跑的命令。
 *
 * 写死 /dev/sdb 会坑人——救援机自己可能已经挂了别的盘，新盘未必是 sdb。
 * 所以第一条是 lsblk，让人自己确认；后面的命令都以 $DEV 变量表述。
 * 分区选 1 是因为 OCI 的 Ubuntu/OL 镜像根分区固定是第一个，
 * 14/15/16 分别是 BIOS boot、EFI 和 /boot，挂错了会找不到 home 目录。
 */
const rescueSteps = computed(() => [
  '# 1. 找到新挂上来的盘（看 SIZE 对得上、且没有挂载点的那块）',
  'lsblk',
  '',
  '# 2. 挂载它的根分区（把 sdb 换成上一步看到的名字）',
  'DEV=/dev/sdb',
  'sudo mkdir -p /mnt/rescue',
  'sudo mount ${DEV}1 /mnt/rescue',
  'ls /mnt/rescue/home        # 确认用户名：Ubuntu 是 ubuntu，Oracle Linux 是 opc',
  '',
  '# 3. 补上 SSH 公钥（USER 换成上一步看到的用户名）',
  'USER=ubuntu',
  'sudo mkdir -p /mnt/rescue/home/$USER/.ssh',
  "echo '你的公钥内容' | sudo tee -a /mnt/rescue/home/$USER/.ssh/authorized_keys",
  'sudo chmod 700 /mnt/rescue/home/$USER/.ssh',
  'sudo chmod 600 /mnt/rescue/home/$USER/.ssh/authorized_keys',
  '# 属主按盘里的 UID 设，不能用救援机的用户名——两台机器的 UID 未必一样',
  'sudo chown -R 1000:1000 /mnt/rescue/home/$USER/.ssh',
  '',
  '# 4. 卸载。不 umount 就分离，刚写的东西可能还在页缓存里没落盘',
  'sudo umount /mnt/rescue'
].join('\n'))

const rescueTargetGroups = computed(() => [{
  label: '同可用域的运行中实例',
  options: rescueTargets.value.map(t => ({
    value: t.id, label: `${t.name} · ${t.publicIp || '无公网 IP'}`
  }))
}])

/** 把本机的引导卷挂到选中的救援机上当数据盘。 */
async function attachToRescue() {
  const i = inst.value
  const volumeId = chosenVolume.value
  const target = rescueTargets.value.find(t => t.id === rescueTarget.value)
  if (!i || !volumeId || !target) return

  ask({
    level: 2,
    title: `把 ${i.name} 的系统盘挂到 ${target.name}`,
    body: `挂上去之后登录 ${target.name}，用 lsblk 找到新增的设备（通常是 /dev/sdb），`
      + '挂载它的根分区就能改里面的文件。改完记得先 umount 再回来分离。',
    okLabel: '挂为数据盘',
    onConfirm: async () => {
      rescueBusy.value = true
      try {
        const res = await storage.attachVolume(
          i.accountId, i.region, target.id, volumeId, `rescue-${i.name}`)
        toast({ tone: 'success', title: '已挂载到 ' + target.name, body: res.notice })
        await loadDetail()
      } catch (err) {
        toastError('挂载失败', err)
      } finally {
        rescueBusy.value = false
      }
    }
  })
}

/** 分离一块数据盘。救援收尾用，也用于「附加块存储」里的普通盘。 */
async function detachData(attachmentId: string, name: string) {
  const i = inst.value
  if (!i) return
  ask({
    level: 2,
    title: `分离 ${name}`,
    body: '分离前先在实例里 umount，否则卷上可能有没落盘的写入。',
    okLabel: '分离',
    onConfirm: async () => {
      try {
        const res = await storage.detachVolume(attachmentId, i.accountId, i.region)
        toast({ tone: 'success', title: '已分离', body: res.notice })
        await loadDetail()
      } catch (err) {
        toastError('分离失败', err)
      }
    }
  })
}

/** 把引导卷挂回自己，恢复可启动状态。 */
async function reattachBoot() {
  const i = inst.value
  const volumeId = chosenVolume.value
  if (!i || !volumeId) return
  ask({
    level: 2,
    title: `把引导卷挂回 ${i.name}`,
    body: '挂回之前必须先在救援机上分离这块卷——一块卷同一时刻只能挂在一处。挂回后即可开机。',
    okLabel: '挂回引导卷',
    onConfirm: async () => {
      rescueBusy.value = true
      try {
        await storage.attachBootVolume(i.accountId, i.region, i.id, volumeId)
        toast({ tone: 'success', title: '引导卷已挂回', body: '现在可以启动实例了' })
        await loadDetail()
      } catch (err) {
        toastError('挂回引导卷失败', err)
      } finally {
        rescueBusy.value = false
      }
    }
  })
}

function askTerminate() {
  const i = inst.value
  if (!i) return
  ask({
    level: 3, title: `终止实例 ${i.name}`,
    body: '该操作会立即销毁实例与其挂载的引导卷。',
    noun: i.name, nounLabel: '实例名称',
    losses: [
      '实例本身及其全部系统数据',
      `引导卷（${i.bootGb} GB）`,
      i.publicIp !== '—' ? `临时公网 IP ${i.publicIp}` : '该实例的网络配置'
    ],
    okLabel: '永久终止',
    onConfirm: () => { closeDrawer(); terminate(i.id) }
  })
}

onMounted(() => {
  void loadDetail()
  void loadAudit()
})

watch(active, tab => {
  if (tab === '监控' && !metrics.value) void loadMetrics()
  if (tab === '操作记录') void loadAudit()
})

watch(metricHours, () => void loadMetrics())
</script>

<template>
  <AppDrawer v-if="inst && account" width="wide" @close="closeDrawer()">
    <DrawerHeader :color="color" :code="account.code" :title="inst.name"
                  :sub="`${inst.region} · ${inst.shape} · ocid1…${inst.ocidTail}`"
                  @close="closeDrawer()">
      <template #badge>
        <StateBadge :state="inst.state" :settled="!!inst.settledAt" />
      </template>
      <template #actions>
        <button v-if="inst.state === 'STOPPED'" class="btn btn--sm"
                style="border-color: var(--success); color: var(--success)"
                :disabled="locked" @click="start(inst.id)">开机</button>
        <button v-else class="btn btn--sm" :disabled="locked" @click="stop(inst.id)">关机</button>
        <button class="btn btn--sm" :disabled="locked" @click="restart(inst.id)">软重启</button>
        <button class="btn btn--sm btn--danger" :disabled="locked || !state.options.allowTerminate"
                :title="state.options.allowTerminate ? '' : '已在设置中禁用'"
                @click="askTerminate()">终止</button>
      </template>
    </DrawerHeader>

    <DrawerTabs :tabs="TABS" v-model:active="active" />

    <DrawerBody>
      <p v-if="inst.lastError" class="err">{{ inst.lastError }}</p>
      <p v-if="detailError" class="err">{{ detailError }}</p>
      <p v-for="w in detail?.warnings ?? []" :key="w" class="warn">{{ w }}</p>

      <template v-if="active === '概览'">
        <SectionCard title="基本信息">
          <KeyValueList :items="[
            { k: 'OCID', v: inst.id, mono: true, copyable: true },
            { k: '状态', v: inst.state, mono: true },
            { k: '区域 · AD', v: `${inst.region} · ${inst.ad}`, mono: true },
            { k: '运行时长', v: uptimeDetail, mono: true },
            { k: '账号', v: `${account.alias} · ${account.code}`, tone: color }
          ]" />
        </SectionCard>

        <SectionCard title="计算配置">
          <KeyValueList :items="[
            { k: 'Shape', v: inst.shape, mono: true },
            { k: 'OCPU', v: String(inst.ocpu), mono: true },
            { k: '内存', v: `${inst.memGb} GB`, mono: true },
            { k: '引导卷', v: `${inst.bootGb} GB · VPU ${inst.vpu}`, mono: true }
          ]" />
        </SectionCard>

        <SectionCard v-if="sshKeys" title="SSH 公钥" note="创建时注入">
          <CodeBlock :code="sshKeys" />
        </SectionCard>

        <SectionCard v-if="cloudInit" title="cloud-init 元数据" note="创建时注入，不可修改">
          <CodeBlock :code="cloudInit" />
        </SectionCard>
      </template>

      <template v-else-if="active === '网络'">
        <SkeletonRows v-if="detailLoading" :rows="4" />
        <template v-else>
          <SectionCard title="IP 地址">
            <KeyValueList :items="[
              { k: '公网 IPv4', v: primaryVnic?.publicIp || '未分配',
                mono: true, copyable: !!primaryVnic?.publicIp, secret: 'ip' },
              { k: '类型', v: primaryVnic?.publicIpType === 'RESERVED' ? '保留 IP' : '临时 IP' },
              { k: '私网 IPv4', v: primaryVnic?.privateIp || '—', mono: true, copyable: true, secret: 'ip' },
              { k: 'IPv6', v: primaryVnic?.ipv6?.join(', ') || '未启用',
                mono: true, tone: 'var(--text-secondary)' }
            ]" />
          </SectionCard>

          <SectionCard title="VCN 与子网">
            <KeyValueList :items="[
              { k: 'VNIC', v: primaryVnic?.displayName || '—', mono: true },
              { k: 'VCN', v: primaryVnic?.vcnName || '—', mono: true },
              { k: '子网', v: primaryVnic?.subnetName || '—', mono: true },
              { k: 'MAC', v: primaryVnic?.macAddress || '—', mono: true }
            ]" />
          </SectionCard>

          <SectionCard v-if="(detail?.vnics?.length ?? 0) > 1" title="附属网卡">
            <div v-for="v in detail?.vnics?.filter(x => !x.isPrimary) ?? []" :key="v.vnicId" class="rule">
              <span class="rule__bar" :style="{ background: color }" />
              <span class="mono t-xs">{{ v.displayName }} · {{ v.privateIp }}</span>
              <span class="rule__desc mono t-2xs dim-3">nic {{ v.nicIndex }}</span>
            </div>
          </SectionCard>

          <SectionCard title="公网 IP 操作">
            <CheckList :items="[
              { tone: 'warn', text: '更换公网 IP 会中断当前 SSH 连接，原 IP 不可找回。入口在行内 ⋯ 菜单与网络页，需二次确认。' },
              { tone: 'info', text: '保留 IP 不支持这种「删了重建」的更换方式，删除即永久释放。' }
            ]" />
          </SectionCard>
        </template>
      </template>

      <template v-else-if="active === '存储'">
        <BootVolumeEditor :instance="inst" :account="account"
                          :boot-volume-id="detail?.bootVolume?.id" />

        <SectionCard title="附加块存储">
          <SkeletonRows v-if="detailLoading" :rows="2" />
          <template v-else-if="(detail?.blockVolumes?.length ?? 0) > 0">
            <div v-for="v in detail?.blockVolumes ?? []" :key="v.volumeId" class="rule">
              <span class="rule__bar" :style="{ background: color }" />
              <span class="mono t-xs">{{ v.displayName }} · {{ v.sizeInGbs }} GB · VPU {{ v.vpusPerGb }}</span>
              <span class="rule__desc mono t-2xs dim-3">{{ v.device || v.state }}</span>
              <button v-if="v.attachmentId" class="btn btn--xs btn--warning"
                      @click="detachData(v.attachmentId, v.displayName)">分离</button>
            </div>
          </template>
          <CheckList v-else :items="[{ tone: 'info', text: '未附加块存储' }]" />
        </SectionCard>

        <SectionCard title="救援模式" note="把系统盘卸下来挂到另一台机器上修">
          <!-- 第一步：引导卷还挂着 -->
          <div v-if="!bootDetached" class="pad">
            <CheckList :items="[
              { tone: inst.state === 'STOPPED' ? 'ok' : 'warn',
                text: inst.state === 'STOPPED'
                  ? '实例已关机，可以分离引导卷。'
                  : '分离引导卷需要先关机。' },
              { tone: 'info', text: '分离后该实例无法启动，直到重新挂载。适合补 SSH 公钥、修坏掉的 fstab。' }
            ]" />
            <button class="btn btn--sm btn--warning"
                    :disabled="inst.state !== 'STOPPED' || !detail?.bootVolumeAttachmentId"
                    @click="detachBoot()">分离引导卷</button>
          </div>

          <!-- 第二步：已分离，挂到救援机 -->
          <div v-else class="pad">
            <CheckList :items="[
              { tone: 'warn', text: '引导卷已分离，该实例现在无法启动。' },
              ...(rescueTargets.length
                ? [{ tone: 'info' as const,
                     text: `可挂载到 ${rescueTargets.length} 台同可用域（${inst.ad}）的运行中实例。` }]
                : [{ tone: 'warn' as const,
                     text: `${inst.region} 的 ${inst.ad} 里没有其它运行中的实例。卷不能跨可用域挂载——`
                       + '需要先在同一个可用域开一台能 SSH 的机器。' }])
            ]" />

            <!-- 卷分离之后就「无主」了，缓存里的 BootVolumeID 也被同步清掉，
                 所以得让人从本可用域的引导卷里认一下是哪块。默认按同名猜。 -->
            <div class="rescue">
              <span class="t-2xs dim-3">要救的卷</span>
              <SelectMenu v-model="chosenVolume" :groups="looseVolumeGroups"
                          :placeholder="looseLoading ? '读取中…' : '选择引导卷'"
                          aria-label="要救援的引导卷" />
              <button class="btn btn--xs" :disabled="looseLoading" @click="loadLooseVolumes()">重新读取</button>
            </div>
            <CheckList v-if="looseError"
                       :items="[{ tone: 'fail', text: '读取引导卷列表失败：' + looseError }]" />
            <CheckList v-else-if="!looseLoading && !looseVolumes.length"
                       :items="[{ tone: 'warn',
                                  text: `${inst.ad} 里没有读到任何引导卷。分离后的卷仍会留在原可用域，`
                                    + '若这里为空，多半是账号的区域或分区选择不对。' }]" />

            <div v-if="rescueTargets.length" class="rescue">
              <span class="t-2xs dim-3">挂到</span>
              <SelectMenu v-model="rescueTarget" :groups="rescueTargetGroups"
                          placeholder="选择救援机" aria-label="救援目标实例" />
              <button class="btn btn--sm btn--primary"
                      :disabled="!rescueTarget || !chosenVolume || rescueBusy"
                      @click="attachToRescue()">挂为数据盘</button>
            </div>

            <details class="rescue__how">
              <summary class="t-xs dim-2">挂上去之后怎么改</summary>
              <CodeBlock :code="rescueSteps" />
            </details>

            <div class="rescue__back">
              <span class="t-2xs dim-3">修完、并在救援机上分离之后：</span>
              <button class="btn btn--sm" :disabled="rescueBusy || !chosenVolume"
                      @click="reattachBoot()">挂回引导卷</button>
            </div>
          </div>
        </SectionCard>
      </template>

      <template v-else-if="active === '监控'">
        <SectionCard title="时间范围">
          <div class="pad pad--row">
            <div class="range">
              <button v-for="h in [6, 24, 72, 168]" :key="h" class="btn btn--sm"
                      :class="{ 'is-on': metricHours === h }" @click="metricHours = h">
                {{ h < 24 ? `${h} 小时` : `${h / 24} 天` }}
              </button>
            </div>
            <button class="btn btn--sm" :disabled="metricsLoading" @click="loadMetrics()">
              {{ metricsLoading ? '加载中…' : '刷新' }}
            </button>
          </div>
        </SectionCard>

        <CheckList v-if="noMetricData"
                   :items="[{ tone: 'warn', text: metrics?.notice ?? '暂无监控数据' }]" />

        <SectionCard title="出站流量" :note="`聚合方式 rate · 粒度 ${metrics?.resolution ?? '—'}`">
          <TrafficChart :values="toMbps(seriesOf('NetworksBytesOut'))"
                        :labels="metricLabels" unit=" Mbps"
                        empty-text="暂无流量数据" />
        </SectionCard>

        <SectionCard title="入站流量">
          <TrafficChart :values="toMbps(seriesOf('NetworksBytesIn'))"
                        :labels="metricLabels" unit=" Mbps"
                        empty-text="暂无流量数据" />
        </SectionCard>

        <SectionCard title="CPU 使用率">
          <TrafficChart :values="seriesOf('CpuUtilization')"
                        :labels="metricLabels" unit="%"
                        empty-text="暂无 CPU 数据" />
        </SectionCard>

        <SectionCard title="内存使用率">
          <TrafficChart :values="seriesOf('MemoryUtilization')"
                        :labels="metricLabels" unit="%"
                        empty-text="暂无内存数据" />
          <div class="pad">
            <QuotaMeter label="内存总量" :used="inst.memGb" :limit="inst.memGb" unit=" GB" />
          </div>
        </SectionCard>
      </template>

      <template v-else-if="active === '控制台'">
        <SectionCard title="串行控制台" note="不依赖网络配置，SSH 连不上时用它排障">
          <CheckList :items="[
            { tone: 'info', text: '改坏 fstab、防火墙把自己关在门外、忘了开 SSH —— 这些都能靠串行控制台救回来。' },
            { tone: 'info', text: 'Oracle 用你的 SSH 公钥鉴权，本工具不会代管你的私钥。' }
          ]" />
        </SectionCard>

        <SectionCard title="建立连接">
          <div class="pad">
            <div class="field">
              <label for="conkey">SSH 公钥（必须是 RSA）</label>
              <textarea id="conkey" v-model="sshPublicKey" class="textarea" style="height: 68px"
                        spellcheck="false" placeholder="ssh-rsa AAAAB3NzaC1yc2E…" />
              <p class="hint">
                Oracle 的串行控制台只接受 RSA，不支持 ed25519。没有的话生成一把：
                <code>ssh-keygen -t rsa -b 4096 -f ~/.ssh/oci_console</code>
              </p>
            </div>
            <button class="btn btn--sm" :disabled="consoleCreating" @click="createConsole()">
              {{ consoleCreating ? '建立中…' : '建立控制台连接' }}
            </button>
          </div>
        </SectionCard>

        <template v-if="consoleResult">
          <SectionCard title="串行控制台命令" note="在本机终端执行">
            <CodeBlock copyable :code="consoleResult.serialConsoleCommand" />
          </SectionCard>
          <SectionCard title="VNC 隧道命令" note="建好隧道后用 VNC 客户端连 localhost:5900">
            <CodeBlock copyable :code="consoleResult.vncConsoleCommand" />
          </SectionCard>
        </template>
      </template>

      <template v-else>
        <SectionCard title="本实例操作审计" :note="`${auditRows.length} 条`">
          <div v-for="r in auditRows" :key="r.id" class="rule">
            <span class="rule__bar"
                  :style="{ background: r.result === 'ok' ? 'var(--success)' : 'var(--danger)' }" />
            <span class="t-xs">{{ r.action }}{{ r.detail ? ` · ${r.detail}` : '' }}</span>
            <span class="rule__desc mono t-2xs dim-3">{{ relativeTime(r.createdAt) }}</span>
          </div>
          <CheckList v-if="auditRows.length === 0"
                     :items="[{ tone: 'info', text: '该实例暂无操作记录' }]" />
        </SectionCard>
      </template>
    </DrawerBody>
  </AppDrawer>
</template>

<style scoped>
.rule { display: flex; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid var(--border-subtle); }
.rule__bar { width: 3px; height: 22px; flex: 0 0 auto; border-radius: var(--radius-full); }
.rule__desc { margin-left: auto; text-align: right; white-space: nowrap; }
.pad { padding: 14px 16px; display: flex; flex-direction: column; gap: 12px; }
.pad--row { flex-direction: row; align-items: center; justify-content: space-between; }
.range { display: flex; gap: 6px; }
.range .btn.is-on { border-color: var(--accent); color: var(--accent); background: var(--accent-soft); }
.err, .warn {
  margin: 12px 16px 0; padding: 10px 12px; border-radius: var(--radius-md);
  font-size: 12px; line-height: 18px;
}
.err { border: 1px solid var(--danger); background: var(--danger-soft); color: var(--danger); }
.warn { border: 1px solid var(--warning); background: var(--warning-soft); color: var(--warning); }

/* ---- 救援模式 ---- */

.rescue {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-top: 10px;
}

.rescue__how {
  margin-top: 12px;
}

.rescue__how summary {
  cursor: pointer;
  user-select: none;
  padding: 4px 0;
}

.rescue__back {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px solid var(--border-subtle);
}

</style>
