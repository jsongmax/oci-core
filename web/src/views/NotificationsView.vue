<script setup lang="ts">
/**
 * §4.7 通知：渠道卡片 + 事件订阅矩阵。
 *
 * 渠道的配置字段由后端下发（kinds），前端据此动态渲染表单——
 * 这样后端新增一种渠道时前端不用跟着改。
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { useStore } from '@/store'
import SelectMenu from '@/components/SelectMenu.vue'
import {
  notifications as notifyApi,
  type ChannelDTO, type ChannelKindDTO, type NotifyEventDTO
} from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import EmptyState from '@/components/EmptyState.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'

const { toast, toastError, ask } = useStore()

const channels = ref<ChannelDTO[]>([])
const kinds = ref<ChannelKindDTO[]>([])

/** 渠道类型下拉。只有一档，label 留空则不渲染分组标题。 */
const kindGroups = computed(() => [{
  label: '',
  options: kinds.value.map(k => ({ value: k.kind, label: k.label }))
}])
const events = ref<NotifyEventDTO[]>([])
const loading = ref(true)
const testing = ref<string | null>(null)

/** 新建/编辑表单。editingId 为 null 表示新建。 */
const editingId = ref<string | null>(null)
const formOpen = ref(false)
const saving = ref(false)
const form = reactive({
  kind: 'telegram',
  name: '',
  config: {} as Record<string, string>,
  events: [] as string[]
})

const currentKind = computed(() => kinds.value.find(k => k.kind === form.kind))

async function load() {
  loading.value = true
  try {
    const [list, evt] = await Promise.all([notifyApi.list(), notifyApi.events()])
    channels.value = list.channels
    kinds.value = list.kinds
    events.value = evt.events
    if (!kinds.value.some(k => k.kind === form.kind)) {
      form.kind = kinds.value[0]?.kind ?? ''
    }
  } catch (err) {
    toastError('读取通知渠道失败', err)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  form.kind = kinds.value[0]?.kind ?? ''
  form.name = ''
  form.config = {}
  form.events = events.value.map(e => e.key)
  formOpen.value = true
}

function openEdit(ch: ChannelDTO) {
  editingId.value = ch.id
  form.kind = ch.kind
  form.name = ch.name
  // 直接回填后端给的（已打码）配置。提交时若某字段仍是打码值，
  // 后端会保留原值，所以原样回填是安全的。
  form.config = { ...ch.config }
  form.events = [...ch.events]
  formOpen.value = true
}

async function save() {
  if (!form.name.trim()) {
    toast({ tone: 'danger', title: '请填写渠道名称' })
    return
  }
  saving.value = true
  try {
    if (editingId.value) {
      await notifyApi.update(editingId.value, {
        name: form.name.trim(), config: form.config, events: form.events
      })
    } else {
      await notifyApi.create({
        kind: form.kind, name: form.name.trim(), config: form.config, events: form.events
      })
    }
    formOpen.value = false
    await load()
    toast({ tone: 'success', title: editingId.value ? '渠道已更新' : '渠道已添加' })
  } catch (err) {
    toastError('保存渠道失败', err)
  } finally {
    saving.value = false
  }
}

async function toggleEnabled(ch: ChannelDTO) {
  try {
    await notifyApi.update(ch.id, { enabled: !ch.enabled })
    await load()
  } catch (err) {
    toastError('操作失败', err)
  }
}

function removeChannel(ch: ChannelDTO) {
  ask({
    level: 2,
    title: `删除渠道 ${ch.name}`,
    body: '删除后订阅该渠道的事件将不再推送。',
    okLabel: '删除',
    onConfirm: async () => {
      try {
        await notifyApi.remove(ch.id)
        await load()
        toast({ tone: 'warning', title: `${ch.name} 已删除` })
      } catch (err) {
        toastError('删除失败', err)
      }
    }
  })
}

/** 测试发送始终返回 200，成功与否看 ok 字段。 */
async function sendTest(ch: ChannelDTO) {
  testing.value = ch.id
  try {
    const result = await notifyApi.test(ch.id)
    if (result.ok) {
      toast({
        tone: 'success', title: `${ch.name} 测试已发送`,
        body: '请检查该渠道是否收到「OCI Core 测试通知」'
      })
    } else {
      toast({ tone: 'danger', title: `${ch.name} 测试失败`, body: result.error }, 9000)
    }
    await load()
  } catch (err) {
    toastError('测试失败', err)
  } finally {
    testing.value = null
  }
}

/** 订阅矩阵：行=事件，列=已配置的渠道。 */
const enabledChannels = computed(() => channels.value.filter(c => c.enabled))

const subscribed = (ch: ChannelDTO, eventKey: string) => ch.events.includes(eventKey)

async function toggleSubscription(ch: ChannelDTO, eventKey: string) {
  const next = subscribed(ch, eventKey)
    ? ch.events.filter(e => e !== eventKey)
    : [...ch.events, eventKey]
  try {
    await notifyApi.update(ch.id, { events: next })
    ch.events = next
  } catch (err) {
    toastError('保存订阅失败', err)
  }
}

const kindLabel = (kind: string) => kinds.value.find(k => k.kind === kind)?.label ?? kind

/** 渠道配置的一行摘要，机密字段已由后端打码。 */
function configSummary(ch: ChannelDTO): string {
  const parts = Object.entries(ch.config)
    .filter(([, v]) => v)
    .map(([k, v]) => `${k}=${v}`)
  return parts.length ? parts.join(' · ') : '未配置'
}

onMounted(load)
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="page-title">通知</h1>
        <p class="page-sub">事件推送渠道与订阅矩阵</p>
      </div>
      <button class="btn btn--primary" @click="openCreate()">添加渠道</button>
    </header>

    <SectionCard title="渠道" note="机密字段在服务端打码，不会回传完整值">
      <SkeletonRows v-if="loading" :rows="3" />
      <EmptyState v-else-if="channels.length === 0" title="还没有配置任何通知渠道"
                  body="配置后，实例就绪、账号凭据失效、危险操作等事件会推送到这里。" />
      <div v-else class="cards">
        <article v-for="ch in channels" :key="ch.id" class="chcard" :class="{ 'is-on': ch.enabled }">
          <header class="chcard__head">
            <h3 class="chcard__title">{{ ch.name }}</h3>
            <span class="chcard__kind">{{ kindLabel(ch.kind) }}</span>
            <button class="track" role="switch" :aria-checked="ch.enabled"
                    :class="{ 'is-on': ch.enabled }" @click="toggleEnabled(ch)">
              <span class="knob" />
            </button>
          </header>

          <p class="chcard__config mono">{{ configSummary(ch) }}</p>
          <p v-if="ch.lastError" class="chcard__err">{{ ch.lastError }}</p>

          <div class="chcard__foot">
            <button class="btn btn--sm" :disabled="testing === ch.id" @click="sendTest(ch)">
              {{ testing === ch.id ? '发送中…' : '发送测试' }}
            </button>
            <button class="btn btn--sm" @click="openEdit(ch)">编辑</button>
            <button class="btn btn--sm btn--danger" @click="removeChannel(ch)">删除</button>
            <span class="t-2xs chcard__state" :style="{
              color: ch.lastError ? 'var(--danger)' : ch.enabled ? 'var(--success)' : 'var(--text-tertiary)'
            }">
              {{ ch.lastError ? '上次发送失败' : ch.enabled ? '已启用' : '已关闭' }}
            </span>
          </div>
        </article>
      </div>
    </SectionCard>

    <SectionCard v-if="formOpen" :title="editingId ? '编辑渠道' : '添加渠道'" class="mt">
      <div class="form">
        <div v-if="!editingId" class="field">
          <span class="t-xs dim">类型</span>
          <SelectMenu v-model="form.kind" :groups="kindGroups" :min-width="200"
                      aria-label="渠道类型" />
        </div>

        <div class="field">
          <label for="cname">名称</label>
          <input id="cname" v-model="form.name" class="input" placeholder="例如：我的 Telegram" />
        </div>

        <div v-for="f in currentKind?.fields ?? []" :key="f.key" class="field">
          <label :for="`cfg-${f.key}`">
            {{ f.label }}<span v-if="f.required" class="req">*</span>
          </label>
          <input :id="`cfg-${f.key}`" v-model="form.config[f.key]" class="input mono"
                 :type="f.secret && !editingId ? 'password' : 'text'" :placeholder="f.hint" />
          <p v-if="f.hint" class="hint">{{ f.hint }}</p>
        </div>

        <div class="field">
          <label>订阅事件</label>
          <div class="evt-list">
            <label v-for="e in events" :key="e.key" class="evt">
              <input type="checkbox" :value="e.key" v-model="form.events" />
              <span>
                <span class="evt__label">{{ e.label }}</span>
                <span class="evt__desc">{{ e.description }}</span>
              </span>
            </label>
          </div>
        </div>

        <div class="form__foot">
          <button class="btn" @click="formOpen = false">取消</button>
          <button class="btn btn--primary" :disabled="saving" @click="save()">
            {{ saving ? '保存中…' : '保存' }}
          </button>
        </div>
      </div>
    </SectionCard>

    <SectionCard v-if="enabledChannels.length > 0" title="事件订阅" note="行为事件，列为渠道" class="mt">
      <div class="matrix" :style="{ gridTemplateColumns: `1fr repeat(${enabledChannels.length}, 88px)` }">
        <span />
        <span v-for="c in enabledChannels" :key="c.id" class="matrix__col">{{ c.name }}</span>
        <template v-for="e in events" :key="e.key">
          <span class="matrix__label" :title="e.description">{{ e.label }}</span>
          <span v-for="c in enabledChannels" :key="c.id" class="matrix__cell">
            <button class="check" :class="{ 'is-on': subscribed(c, e.key) }"
                    role="checkbox" :aria-checked="subscribed(c, e.key)"
                    :aria-label="`${e.label} → ${c.name}`"
                    @click="toggleSubscription(c, e.key)">✓</button>
          </span>
        </template>
      </div>
    </SectionCard>
  </div>
</template>

<style scoped>
.mt { margin-top: 16px; }
.cards { padding: 16px 20px; display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 14px; }
.chcard { padding: 16px; border-radius: var(--radius-md); border: 1px solid var(--border-subtle); background: var(--bg-inset); }
.chcard.is-on { border-color: var(--border-strong); }
.chcard__head { display: flex; align-items: center; gap: 10px; }
.chcard__title { margin: 0; flex: 1 1 auto; font-size: 14px; font-weight: 600; }
.chcard__kind { font-size: 11px; color: var(--text-tertiary); }
.chcard__config { margin: 8px 0 0; font-size: 11px; color: var(--text-tertiary); word-break: break-all; }
.chcard__err { margin: 8px 0 0; font-size: 11px; color: var(--danger); word-break: break-all; }
.chcard__foot { margin-top: 12px; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.chcard__state { margin-left: auto; }

.track { width: 36px; height: 20px; flex: 0 0 auto; border: 0; border-radius: var(--radius-full); background: var(--border-strong); position: relative; cursor: pointer; }
.track.is-on { background: var(--accent); }
.knob { position: absolute; top: 2px; left: 2px; width: 16px; height: 16px; border-radius: var(--radius-full); background: #fff; transition: transform var(--dur-fast) var(--ease-standard); }
.track.is-on .knob { transform: translateX(16px); }

.form { padding: 16px 20px; display: flex; flex-direction: column; gap: 14px; max-width: 520px; }
.form__foot { display: flex; gap: 10px; justify-content: flex-end; }
.req { color: var(--danger); margin-left: 3px; }
.hint { margin: 4px 0 0; font-size: 11px; color: var(--text-tertiary); }
.evt-list { display: flex; flex-direction: column; gap: 8px; }
.evt { display: flex; align-items: flex-start; gap: 9px; cursor: pointer; }
.evt__label { display: block; font-size: 13px; }
.evt__desc { display: block; font-size: 11px; color: var(--text-tertiary); }

.matrix { padding: 16px 20px; display: grid; gap: 4px; align-items: center; }
.matrix__col { font-size: 11px; color: var(--text-tertiary); text-align: center; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.matrix__label { font-size: 13px; padding: 7px 0; }
.matrix__cell { display: flex; justify-content: center; padding: 7px 0; }
.check {
  width: 17px; height: 17px; display: flex; align-items: center; justify-content: center;
  border-radius: 5px; border: 1px solid var(--border-strong); background: transparent;
  color: transparent; font-size: 10px; cursor: pointer;
}
.check.is-on { background: var(--accent); border-color: var(--accent); color: var(--bg-base); }
</style>
