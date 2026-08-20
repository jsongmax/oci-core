<script setup lang="ts">
/**
 * §4.6 存储：引导卷 / 块存储。
 *
 * 引导卷直接读实例缓存（同步时已带回容量与 VPU），块存储要按
 * （账号 × 区域）逐个拉——OCI 没有跨账号的卷列表接口。
 */
import { computed, onMounted, ref, watch } from 'vue'
import { useStore } from '@/store'
import { acctColor } from '@/lib/format'
import { storage, errorText, type BootVolumeDTO } from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import PageTabs from '@/components/PageTabs.vue'
import AccountChip from '@/components/AccountChip.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import AccountGroupRow from '@/components/AccountGroupRow.vue'
import { groupByAccount } from '@/lib/grouping'

const { state, accountById, openDrawer, visibleInstances } = useStore()
const TABS = ['引导卷', '块存储']
const active = ref('引导卷')

/** 引导卷来自实例缓存：同步时已经把容量与 VPU 一并带回来了。 */
const boots = computed(() => visibleInstances.value.filter(i => i.state !== 'TERMINATED'))
const totalGb = computed(() => boots.value.reduce((n, i) => n + i.bootGb, 0))

interface LocatedVolume { accountId: string; region: string; item: BootVolumeDTO }

const blocks = ref<LocatedVolume[]>([])

const grouped = <T extends { accountId: string }>(rows: T[]) =>
  groupByAccount(rows, state.groupByAccount, accountById)

const bootGroups = computed(() => grouped(boots.value))
const blockGroups = computed(() => grouped(blocks.value))
const loading = ref(false)
const loadError = ref('')

const targets = computed(() =>
  state.accounts
    .filter(a => state.accountFilter.has(a.id) && a.status !== 'disabled')
    // 顶栏区域筛选器同样要参与——和网络页一样，它以前在这里也是个摆设。
    .flatMap(a => a.regions
      .filter(r => state.regionFilter.size === 0 || state.regionFilter.has(r))
      .map(region => ({ accountId: a.id, region })))
)

async function loadBlocks() {
  if (targets.value.length === 0) {
    blocks.value = []
    return
  }

  loading.value = true
  loadError.value = ''
  const next: LocatedVolume[] = []
  const failures: string[] = []

  // 每批 4 个，避免在同一租户上堆太多并发触发限流。
  for (let i = 0; i < targets.value.length; i += 4) {
    await Promise.all(targets.value.slice(i, i + 4).map(async ({ accountId, region }) => {
      try {
        const { volumes } = await storage.volumes(accountId, region)
        // ?? [] 是必需的，不是防御性冗余：后端的空列表曾经序列化成 null，
        // 直接 .forEach 会抛 "Cannot read properties of null"，
        // 而且这个异常会被下面的 catch 抓住、伪装成"这个区域查询失败"。
        ;(volumes ?? []).forEach(item => next.push({ accountId, region, item }))
      } catch (err) {
        failures.push(`${accountById(accountId).alias} · ${region}：${errorText(err)}`)
      }
    }))
  }

  blocks.value = next
  loadError.value = failures.join('\n')
  loading.value = false
}

/** VPU 与预估性能的对应关系，来自 Oracle 的块存储性能档位说明。 */
function iopsHint(vpu: number): string {
  if (vpu === 0) return '低成本档'
  if (vpu >= 30) return '超高性能'
  if (vpu >= 20) return '约 25k IOPS'
  return '约 3k IOPS'
}

onMounted(loadBlocks)
watch(() => [active.value, [...state.accountFilter].join(','), [...state.regionFilter].join(',')].join('|'), () => {
  if (active.value === '块存储') void loadBlocks()
})
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">存储</h1>
        <p class="page-sub">
          跨账号聚合 · 引导卷 {{ boots.length }} · 块存储 {{ blocks.length }} · 引导卷合计 {{ totalGb }} GB
        </p>
      </div>
      <div class="st-actions">
        <button class="pill" :class="{ 'is-active': state.groupByAccount }"
                @click="state.groupByAccount = !state.groupByAccount">按账号分组</button>
        <button v-if="active === '块存储'" class="btn" :disabled="loading" @click="loadBlocks">
          {{ loading ? '加载中…' : '刷新' }}
        </button>
      </div>
    </header>

    <PageTabs :tabs="TABS" v-model:active="active" />

    <p v-if="loadError && active === '块存储'" class="warn">{{ loadError }}</p>

    <SectionCard v-if="active === '引导卷'" title="引导卷"
                 note="容量只能增不能减；扩容后需在实例内扩展分区与文件系统">
      <EmptyState v-if="boots.length === 0" title="所选账号下暂无实例"
                  body="引导卷随实例一同创建。" />
      <template v-else>
        <div class="head cols-boot">
          <span>实例</span><span>账号</span><span>区域</span><span>容量</span><span>VPU</span><span>预估性能</span><span />
        </div>
        <template v-for="g in bootGroups" :key="g.key">
        <AccountGroupRow v-if="g.account" :account="g.account" :count="g.rows.length" unit="个引导卷" />
        <div v-for="i in g.rows" :key="i.id" class="row cols-boot">
          <span class="acct-bar" :style="{ background: acctColor(accountById(i.accountId).colorIndex) }" />
          <span class="mono t-xs">{{ i.name }}</span>
          <AccountChip :account="accountById(i.accountId)" />
          <span class="mono t-xs dim">{{ i.region }}</span>
          <span class="mono t-xs">{{ i.bootGb }} GB</span>
          <span class="mono t-xs dim">VPU {{ i.vpu }}</span>
          <span class="mono t-xs dim">{{ iopsHint(i.vpu) }}</span>
          <button class="btn btn--sm" @click="openDrawer({ kind: 'instance', id: i.id, tab: '存储' })">
            管理
          </button>
        </div>
        </template>
      </template>
    </SectionCard>

    <SectionCard v-else title="块存储" note="独立于实例的数据盘">
      <SkeletonRows v-if="loading" :rows="3" />
      <EmptyState v-else-if="blocks.length === 0" title="所选账号下暂无块存储卷"
                  body="块存储需要在 Oracle 控制台创建后再挂载到实例。" />
      <template v-else>
        <div class="head cols-blk">
          <span>名称</span><span>账号</span><span>区域</span><span>容量</span><span>VPU</span><span>状态</span>
        </div>
        <template v-for="g in blockGroups" :key="g.key">
        <AccountGroupRow v-if="g.account" :account="g.account" :count="g.rows.length" unit="个卷" />
        <div v-for="b in g.rows" :key="b.item.id" class="row cols-blk">
          <span class="acct-bar" :style="{ background: acctColor(accountById(b.accountId).colorIndex) }" />
          <span class="mono t-xs">{{ b.item.displayName }}</span>
          <AccountChip :account="accountById(b.accountId)" />
          <span class="mono t-xs dim">{{ b.region }}</span>
          <span class="mono t-xs">{{ b.item.sizeInGBs }} GB</span>
          <span class="mono t-xs dim">VPU {{ b.item.vpusPerGB }}</span>
          <span class="t-xs" :class="{ dim: b.item.lifecycleState !== 'AVAILABLE' }">
            {{ b.item.lifecycleState }}
          </span>
        </div>
        </template>
      </template>
    </SectionCard>
  </div>
</template>

<style scoped>
.st-actions { display: flex; align-items: center; gap: 10px; }

.warn {
  margin: 0 0 12px; padding: 10px 14px; border-radius: var(--radius-md);
  border: 1px solid var(--warning); background: var(--warning-soft);
  color: var(--warning); font-size: 12px; line-height: 18px; white-space: pre-line;
}
.head, .row { display: grid; align-items: center; gap: 12px; padding: 0 20px 0 14px; }
.head { height: 34px; background: var(--bg-inset); border-bottom: 1px solid var(--border-subtle); font-size: 11px; color: var(--text-tertiary); }
.row { min-height: 46px; border-bottom: 1px solid var(--border-subtle); position: relative; }
.row:hover { background: var(--bg-hover); }
.row > span:not(.acct-bar), .head > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row .btn { justify-self: end; }
.cols-boot { grid-template-columns: minmax(170px, 1.4fr) 78px 140px 100px 84px 110px 90px; }
.cols-blk { grid-template-columns: minmax(170px, 1.4fr) 78px 140px 100px 84px minmax(120px, 1fr); }
</style>
