<script setup lang="ts">
/**
 * 替换引导卷（重装系统）。
 *
 * 对应 Oracle 控制台的 Replace boot volume：实例、OCID、A1 容量都保留，只换系统盘。
 * 在 A1 紧张的区域里，这比「终止再重建」安全得多——删掉的机器很可能再也抢不回来。
 */
import { t } from '@/i18n'
import { computed, ref, watch } from 'vue'
import type { Instance } from '@/types'
import { useStore } from '@/store'
import { instances as instancesApi, launch as launchApi, type ImageDTO } from '@/api'
import SectionCard from '@/components/SectionCard.vue'
import CheckList from '@/components/CheckList.vue'
import SwitchRow from '@/components/SwitchRow.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import { ALWAYS_FREE } from '@/lib/freetier'
import { LIFECYCLE } from '@/lib/lifecycle'

const props = defineProps<{ instance: Instance }>()

const { ask, toastError, trackJob } = useStore()

/** Oracle 对引导卷的最小容量要求，与后端 instancesvc.MinBootVolumeGB 一致。 */
const MIN_BOOT_GB = 50

const open = ref(false)
const loading = ref(false)
const loadError = ref('')
const submitting = ref(false)
const images = ref<ImageDTO[]>([])

const imageId = ref('')
const sizeGb = ref(Math.max(props.instance.bootGb || 0, MIN_BOOT_GB))
const preserveOld = ref(true)
const sshKey = ref('')
const cloudInit = ref('')

const osFilter = ref('')

const stable = computed(() => props.instance.state === 'RUNNING' || props.instance.state === 'STOPPED')

/** 换一台实例时表单全部作废，避免把上一台选的镜像提交到这一台。 */
watch(() => props.instance.id, () => {
  open.value = false
  images.value = []
  imageId.value = ''
  sizeGb.value = Math.max(props.instance.bootGb || 0, MIN_BOOT_GB)
  sshKey.value = ''
  cloudInit.value = ''
  osFilter.value = ''
})

async function loadImages() {
  const key = props.instance.id
  loading.value = true
  loadError.value = ''
  try {
    // 按规格过滤：ARM 与 x86 镜像不通用，选错了新盘一样起不来。
    const { images: list } = await launchApi.images(
      props.instance.accountId, props.instance.region, props.instance.shape)
    if (key !== props.instance.id) return
    images.value = list
    if (!list.some(i => i.id === imageId.value)) {
      const ubuntu = list.find(i => i.operatingSystem.includes('Ubuntu'))
      imageId.value = (ubuntu ?? list[0])?.id ?? ''
    }
  } catch (err) {
    if (key !== props.instance.id) return
    images.value = []
    loadError.value = err instanceof Error ? err.message : String(err)
  } finally {
    if (key === props.instance.id) loading.value = false
  }
}

function toggle() {
  open.value = !open.value
  if (open.value && images.value.length === 0) void loadImages()
}

/** 去掉镜像名末尾的构建日期，同一版本只留最新一次构建。 */
const family = (im: ImageDTO) =>
  (im.displayName || '').replace(/-\d{4}\.\d{2}\.\d{2}-\d+$/, '') ||
  `${im.operatingSystem} ${im.operatingSystemVersion}`

const osFamilies = computed(() => {
  const count = new Map<string, number>()
  for (const im of images.value) count.set(im.operatingSystem, (count.get(im.operatingSystem) ?? 0) + 1)
  return [...count.entries()].sort((a, b) => b[1] - a[1]).map(([os, n]) => ({ os, n }))
})

const visibleImages = computed(() => {
  const newest = new Map<string, ImageDTO>()
  for (const im of images.value) {
    if (osFilter.value && im.operatingSystem !== osFilter.value) continue
    const k = family(im)
    const prev = newest.get(k)
    if (!prev || new Date(im.timeCreated) > new Date(prev.timeCreated)) newest.set(k, im)
  }
  return [...newest.values()]
})

const selected = computed(() => images.value.find(i => i.id === imageId.value))
const imageLabel = (im: ImageDTO) => im.displayName || `${im.operatingSystem} ${im.operatingSystemVersion}`

const sizeValid = computed(() => Number.isInteger(sizeGb.value) && sizeGb.value >= MIN_BOOT_GB)
const canSubmit = computed(() => stable.value && !!imageId.value && sizeValid.value && !submitting.value)

const checks = computed(() => {
  const items: { tone: 'ok' | 'warn' | 'info' | 'fail'; text: string }[] = []
  if (!stable.value) {
    items.push({ tone: 'fail', text: t.bootVol.replaceTransitional(LIFECYCLE[props.instance.state].label) })
  } else if (props.instance.state === 'RUNNING') {
    items.push({ tone: 'warn', text: t.bootVol.replaceRunning })
  }
  items.push({ tone: 'warn', text: t.bootVol.replaceFresh })
  items.push(preserveOld.value
    ? { tone: 'info', text: t.bootVol.replaceKeepOld(ALWAYS_FREE.blockGB) }
    : { tone: 'warn', text: t.bootVol.replaceDeleteOld })
  items.push({ tone: 'info', text: t.bootVol.replaceKeeps })
  return items
})

function submit() {
  if (!canSubmit.value || !selected.value) return
  const name = props.instance.name
  const label = imageLabel(selected.value)
  const losses = [t.bootVol.lossSystem]
  if (!preserveOld.value) losses.push(t.bootVol.lossOldVolume)

  ask({
    level: 3,
    title: t.bootVol.replaceConfirmTitle(name),
    body: t.bootVol.replaceConfirmBody(label, sizeGb.value),
    noun: name,
    nounLabel: t.rowActions.instanceName,
    losses,
    okLabel: t.bootVol.replaceButton,
    onConfirm: async () => {
      submitting.value = true
      try {
        const { job } = await instancesApi.replaceBootVolume(props.instance.id, {
          imageId: imageId.value,
          bootVolumeGb: sizeGb.value,
          preserveBootVolume: preserveOld.value,
          sshPublicKey: sshKey.value.trim() || undefined,
          cloudInit: cloudInit.value.trim() || undefined,
          confirm: name
        })
        trackJob(job)
        open.value = false
      } catch (err) {
        toastError(t.bootVol.replaceFailed, err)
      } finally {
        submitting.value = false
      }
    }
  })
}
</script>

<template>
  <SectionCard :title="t.bootVol.replaceTitle" :note="t.bootVol.replaceNote">
    <div v-if="!open" class="pad">
      <p class="t-xs dim-3 lead">
        {{ t.bootVol.replaceLead }}
      </p>
      <button class="btn btn--sm btn--warning" @click="toggle()">{{ t.bootVol.pickSystem }}</button>
    </div>

    <template v-else>
      <SkeletonRows v-if="loading" :rows="3" />
      <div v-else-if="loadError" class="pad">
        <CheckList :items="[{ tone: 'fail', text: t.bootVol.imagesFailed(loadError) }]" />
        <button class="btn btn--sm" @click="loadImages()">{{ t.common.retry }}</button>
      </div>
      <p v-else-if="images.length === 0" class="pad t-xs dim-3">{{ t.bootVol.noImages }}</p>
      <template v-else>
        <div class="imgbar">
          <button class="chip" :class="{ 'is-on': osFilter === '' }" @click="osFilter = ''">
            {{ t.common.all }} <span class="chip__n">{{ images.length }}</span>
          </button>
          <button v-for="f in osFamilies" :key="f.os" class="chip" :class="{ 'is-on': osFilter === f.os }"
                  @click="osFilter = osFilter === f.os ? '' : f.os">
            {{ f.os }} <span class="chip__n">{{ f.n }}</span>
          </button>
        </div>
        <div class="imglist">
          <button v-for="im in visibleImages" :key="im.id" class="opt"
                  :class="{ 'is-picked': imageId === im.id }" @click="imageId = im.id">
            <span class="opt__radio" />
            <span class="opt__text">
              <span class="opt__title">{{ imageLabel(im) }}</span>
              <span class="opt__sub">{{ im.operatingSystem }} {{ im.operatingSystemVersion }}</span>
            </span>
          </button>
        </div>
      </template>

      <div class="pad">
        <div class="field">
          <label for="rbv-size">{{ t.bootVol.bootSizeGb }}</label>
          <input id="rbv-size" v-model.number="sizeGb" type="number" :min="MIN_BOOT_GB" step="1" class="input mono" />
          <p v-if="!sizeValid" class="hint hint--bad">{{ t.bootVol.minGb(MIN_BOOT_GB) }}</p>
        </div>
        <div class="field">
          <label for="rbv-ssh">{{ t.bootVol.sshKey }}</label>
          <textarea id="rbv-ssh" v-model="sshKey" class="textarea" style="height: 68px"
                    spellcheck="false" placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5…" />
          <p class="hint">{{ t.bootVol.sshKeepHint }}</p>
        </div>
        <div class="field">
          <label for="rbv-ci">{{ t.bootVol.cloudInit }}</label>
          <textarea id="rbv-ci" v-model="cloudInit" class="textarea" style="height: 90px"
                    spellcheck="false" placeholder="#cloud-config" />
        </div>
      </div>
      <SwitchRow v-model="preserveOld" :title="t.bootVol.keepOld" :sub="t.bootVol.keepOldSub" />
      <div class="pad">
        <CheckList :items="checks" />
        <div class="actions">
          <button class="btn btn--sm btn--danger" :disabled="!canSubmit" @click="submit()">
            {{ submitting ? t.common.submitting : t.bootVol.replaceButton }}
          </button>
          <button class="btn btn--sm" :disabled="submitting" @click="toggle()">{{ t.common.cancel }}</button>
        </div>
      </div>
    </template>
  </SectionCard>
</template>

<style scoped>
.pad { padding: 14px 16px; display: flex; flex-direction: column; gap: 14px; }
.lead { margin: 0; line-height: 1.7; }
.imgbar {
  display: flex; flex-wrap: wrap; gap: 6px;
  padding: 12px 16px; border-bottom: 1px solid var(--border-subtle);
}
.chip {
  display: inline-flex; align-items: center; gap: 5px;
  padding: 3px 9px; border-radius: var(--radius-full);
  border: 1px solid var(--border-default); background: transparent;
  color: var(--text-secondary); font-size: 11px; cursor: pointer;
}
.chip:hover { background: var(--bg-hover); }
.chip.is-on { border-color: var(--accent); color: var(--accent); }
.chip__n { font-size: 10px; color: var(--text-tertiary); }
.chip.is-on .chip__n { color: var(--accent); }
.imglist { max-height: 280px; overflow-y: auto; }
.opt {
  display: flex; align-items: center; gap: 11px; width: 100%; padding: 11px 16px;
  border: 0; border-bottom: 1px solid var(--border-subtle); background: transparent;
  color: var(--text-primary); cursor: pointer; text-align: left;
}
.opt:hover { background: var(--bg-hover); }
.opt.is-picked { background: var(--accent-soft); }
.opt__radio { width: 15px; height: 15px; flex: 0 0 auto; border-radius: var(--radius-full); border: 1px solid var(--border-strong); }
.opt.is-picked .opt__radio { border-color: var(--accent); background: var(--accent); }
.opt__text { flex: 1 1 auto; min-width: 0; display: flex; flex-direction: column; }
.opt__title { font-size: 13px; font-weight: 500; }
.opt__sub { font-size: 11px; color: var(--text-tertiary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.hint { margin: 0; font-size: 11px; color: var(--text-tertiary); line-height: 16px; }
.hint--bad { color: var(--danger); }
.actions { display: flex; gap: 8px; }
</style>
