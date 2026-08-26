<script setup lang="ts">
/** §3.3 全局搜索：跨账号搜实例名 / OCID / IP / 账号别名，结果带身份色条 */
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useStore } from '@/store'
import MaskedText from '@/components/MaskedText.vue'
import { acctColor } from '@/lib/format'

const router = useRouter()
const { state, accountById, openDrawer } = useStore()
const query = ref('')
const input = ref<HTMLInputElement | null>(null)

function onKey(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
    e.preventDefault()
    state.paletteOpen = true
  }
  if (e.key === 'Escape') state.paletteOpen = false
}
onMounted(() => window.addEventListener('keydown', onKey))
onUnmounted(() => window.removeEventListener('keydown', onKey))
watch(() => state.paletteOpen, open => {
  if (open) requestAnimationFrame(() => input.value?.focus())
  else query.value = ''
})

const q = computed(() => query.value.trim().toLowerCase())
const instanceHits = computed(() => {
  const list = state.instances.filter(i =>
    // 用完整 OCID 而不是 ocidTail：搜索框上写着"OCID"，粘贴一整条
    // ocid1.instance.oc1... 却搜不到——尾巴装不下整条 OCID。
    !q.value || `${i.name}${i.publicIp}${i.privateIp}${i.id}`.toLowerCase().includes(q.value))
  return list.slice(0, 6)
})
const accountHits = computed(() => {
  const list = state.accounts.filter(a =>
    !q.value || `${a.alias}${a.code}${a.email}${a.tenancyTail}`.toLowerCase().includes(q.value))
  return list.slice(0, 4)
})

function goInstance(id: string) {
  state.paletteOpen = false
  router.push('/instances').then(() => openDrawer({ kind: 'instance', id }))
}
function goAccount(id: string) {
  state.paletteOpen = false
  router.push('/accounts').then(() => openDrawer({ kind: 'account', id }))
}
</script>

<template>
  <Transition name="scrim">
    <div v-if="state.paletteOpen" class="scrim" @click.self="state.paletteOpen = false">
      <div class="pal" role="dialog" aria-modal="true" aria-label="全局搜索">
        <div class="pal__head">
          <span class="dim-3">⌕</span>
          <input ref="input" v-model="query" class="pal__input" placeholder="搜索实例名 · OCID · IP · 账号别名" />
          <span class="pal__kbd mono">ESC</span>
        </div>
        <div class="pal__body">
          <template v-if="instanceHits.length">
            <p class="pal__group">实例</p>
            <button v-for="i in instanceHits" :key="i.id" class="pal__row" @click="goInstance(i.id)">
              <span class="pal__bar" :style="{ background: acctColor(accountById(i.accountId).colorIndex) }" />
              <span class="pal__title">{{ i.name }}</span>
              <span class="pal__sub mono"><MaskedText :value="i.publicIp" kind="ip" /></span>
              <span class="pal__code mono" :style="{ color: acctColor(accountById(i.accountId).colorIndex) }">
                {{ accountById(i.accountId).code }}
              </span>
            </button>
          </template>
          <template v-if="accountHits.length">
            <p class="pal__group">账号</p>
            <button v-for="a in accountHits" :key="a.id" class="pal__row" @click="goAccount(a.id)">
              <span class="pal__bar" :style="{ background: acctColor(a.colorIndex) }" />
              <span class="pal__title">{{ a.alias }}</span>
              <span class="pal__sub mono">ocid1…<MaskedText :value="a.tenancyTail" /></span>
              <span class="pal__code mono" :style="{ color: acctColor(a.colorIndex) }">{{ a.code }}</span>
            </button>
          </template>
          <p v-if="!instanceHits.length && !accountHits.length" class="pal__empty">
            没有匹配的实例或账号
          </p>
        </div>
      </div>
    </div>
  </Transition>
</template>

<style scoped>
.scrim {
  position: fixed; inset: 0; z-index: 52; display: flex; align-items: flex-start; justify-content: center;
  padding-top: 13vh; background: var(--scrim); backdrop-filter: blur(3px);
}
.pal {
  width: 620px; border-radius: var(--radius-xl); border: 1px solid var(--glass-border);
  background: var(--bg-elevated); box-shadow: var(--shadow-4); overflow: hidden;
  animation: rise 200ms var(--ease-decelerate);
}
.pal__head { display: flex; align-items: center; gap: 10px; padding: 0 16px; height: 52px; border-bottom: 1px solid var(--border-subtle); }
.pal__input { flex: 1 1 auto; height: 32px; border: 0; background: transparent; color: var(--text-primary); font-size: 14px; outline: none; }
.pal__kbd { font-size: 11px; color: var(--text-tertiary); padding: 1px 5px; border: 1px solid var(--border-default); border-radius: var(--radius-sm); }
.pal__body { max-height: 52vh; overflow-y: auto; padding: 8px; }
.pal__group { margin: 8px 10px 4px; font-size: 11px; color: var(--text-tertiary); font-weight: 500; }
.pal__row {
  display: flex; align-items: center; gap: 12px; width: 100%; height: 40px; padding: 0 10px;
  border: 0; border-radius: var(--radius-sm); background: transparent; color: var(--text-primary); cursor: pointer;
}
.pal__row:hover { background: var(--bg-hover); }
.pal__bar { width: 3px; height: 22px; border-radius: var(--radius-full); flex: 0 0 auto; }
.pal__title { font-size: 13px; }
.pal__sub { font-size: 11px; color: var(--text-tertiary); flex: 1 1 auto; text-align: left; }
.pal__code { font-size: 11px; font-weight: 600; }
.pal__empty { padding: 24px 10px; text-align: center; font-size: 12px; color: var(--text-tertiary); }
</style>
