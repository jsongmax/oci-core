<script setup lang="ts">
/**
 * 添加账号页的「如何创建 API 密钥并配置权限」说明，默认折叠。
 * 策略语句与 internal/permsvc/catalog.go 中各功能所需的权限一致；组名可改，语句随之更新。
 */
import { t } from '@/i18n'
import { computed, ref } from 'vue'
import CheckList from '@/components/CheckList.vue'
import CodeBlock from '@/components/CodeBlock.vue'

const open = ref(false)
const group = ref('OCI Core')

/** 组名按官方示例加单引号；带身份域时写成 'Domain'/'Name'。 */
const subject = computed(() => {
  const raw = group.value.trim()
  if (!raw) return 'group <group-name>'
  const q = (s: string) => `'${s.trim()}'`
  const slash = raw.indexOf('/')
  return slash > 0 ? `group ${q(raw.slice(0, slash))}/${q(raw.slice(slash + 1))}` : `group ${q(raw)}`
})

const GRANTS = [
  'manage instance-family',
  'manage virtual-network-family',
  'manage volume-family',
  'manage compute-capacity-reports',
  'read metrics',
  'inspect limits',
  'read resource-availability',
  'read usage-report',
  'inspect policies',
  'inspect users',
  'inspect groups'
]

const policy = computed(() => GRANTS.map(g => `Allow ${subject.value} to ${g} in tenancy`).join('\n'))
const notes = computed(() => t.perm.setup.notes.map(text => ({ tone: 'warn' as const, text })))
</script>

<template>
  <section class="card guide">
    <button class="guide__head" :aria-expanded="open" @click="open = !open">
      <span class="guide__chev" :class="{ 'is-open': open }" aria-hidden="true">›</span>
      <span class="guide__title">{{ t.perm.setup.title }}</span>
      <span class="guide__note">{{ t.perm.setup.note }}</span>
      <span class="guide__toggle">{{ open ? t.perm.setup.hide : t.perm.setup.show }}</span>
    </button>

    <div v-if="open" class="guide__body">
      <p class="guide__p">{{ t.perm.setup.intro }}</p>

      <h4 class="guide__h">{{ t.perm.setup.stepsTitle }}</h4>
      <ol class="guide__steps">
        <li v-for="(s, i) in t.perm.setup.steps" :key="i">{{ s }}</li>
      </ol>

      <label class="guide__field">
        <span class="guide__label">{{ t.perm.setup.groupLabel }}</span>
        <input v-model="group" class="input mono" spellcheck="false" placeholder="OCI Core" />
        <span class="guide__hint">{{ t.perm.setup.groupHint }}</span>
      </label>

      <h4 class="guide__h">{{ t.perm.setup.policyTitle }}</h4>
      <CodeBlock copyable :code="policy" />

      <h4 class="guide__h">{{ t.perm.setup.purposeTitle }}</h4>
      <table class="guide__table">
        <thead><tr><th>{{ t.perm.setup.colStatement }}</th><th>{{ t.perm.setup.colPurpose }}</th></tr></thead>
        <tbody>
          <tr v-for="[st, use] in t.perm.setup.purposes" :key="st">
            <td class="mono">{{ st }}</td>
            <td>{{ use }}</td>
          </tr>
        </tbody>
      </table>

      <CheckList :items="notes" />
      <p class="guide__foot">{{ t.perm.fixLocation }}</p>
    </div>
  </section>
</template>

<style scoped>
.guide { overflow: hidden; }
.guide__head {
  width: 100%; display: flex; align-items: center; gap: 10px; padding: 13px 16px;
  border: 0; background: none; cursor: pointer; text-align: left; color: inherit; font: inherit;
}
.guide__head:hover .guide__toggle { color: var(--accent); }
.guide__chev { flex: 0 0 auto; width: 10px; color: var(--text-tertiary); transition: transform 0.15s; }
.guide__chev.is-open { transform: rotate(90deg); }
.guide__title { flex: 0 1 auto; font-size: 13px; font-weight: 600; }
.guide__note { flex: 1 1 auto; min-width: 0; font-size: 11px; color: var(--text-tertiary); }
.guide__toggle { flex: 0 0 auto; font-size: 11px; color: var(--text-secondary); }

.guide__body { border-top: 1px solid var(--border-subtle); padding: 12px 0 14px; }
.guide__p, .guide__steps, .guide__h, .guide__field, .guide__table, .guide__foot { margin-left: 16px; margin-right: 16px; }
.guide__p { margin-top: 0; margin-bottom: 0; font-size: 12px; line-height: 19px; color: var(--text-secondary); }
.guide__h { margin-top: 14px; margin-bottom: 6px; font-size: 11px; font-weight: 600; color: var(--text-tertiary); }
.guide__steps { margin-top: 0; margin-bottom: 0; padding-left: 18px; font-size: 12px; line-height: 19px; color: var(--text-secondary); }
.guide__steps li + li { margin-top: 4px; }
.guide__field { display: flex; flex-direction: column; gap: 5px; margin-top: 14px; }
.guide__label { font-size: 11px; font-weight: 600; color: var(--text-tertiary); }
.guide__hint { font-size: 11px; color: var(--text-tertiary); }
.guide__body :deep(.code) { margin: 0 16px; }
.guide__table { width: calc(100% - 32px); border-collapse: collapse; font-size: 11px; table-layout: fixed; }
.guide__table th { text-align: left; font-weight: 500; color: var(--text-tertiary); padding: 3px 8px 3px 0; }
.guide__table td { padding: 5px 8px 5px 0; color: var(--text-secondary); vertical-align: top; word-break: break-word; border-top: 1px solid var(--border-subtle); }
.guide__table th:first-child { width: 46%; }
.guide__body > :deep(.checklist), .guide__body > :deep(ul) { margin-top: 10px; }
.guide__foot { margin-top: 10px; margin-bottom: 0; font-size: 11px; color: var(--text-tertiary); }
</style>
