<script setup lang="ts">
/**
 * 打码显示 + 单独展开。
 *
 * 隐私模式开启时按类型打码，尾部带一个眼睛按钮，点一下只展开这一处，
 * 不影响别处——截图时想露出某一个值，不必把整页的保护都关掉。
 *
 * 复制永远复制**原文**。打码只是显示层的事；复制出来是星号的话，
 * 这功能就从隐私保护变成了故意添堵。
 */
import { computed, ref, watch } from 'vue'
import { useStore } from '@/store'
import { mask, type MaskKind } from '@/lib/mask'
import { copy } from '@/lib/format'

const props = withDefaults(defineProps<{
  value: string
  kind?: MaskKind
  /** 显示复制按钮，复制的是原文 */
  copyable?: boolean
  /** 强制不打码。用于本来就不敏感、但恰好走了同一个组件的值 */
  plain?: boolean
}>(), { kind: 'generic', copyable: false, plain: false })

const { state, toast } = useStore()

/** 本组件是否被单独展开过。全局关掉隐私模式时这个状态无关紧要。 */
const revealed = ref(false)

// 隐私模式被重新开启时收回展开。
//
// 那一刻通常就是「马上要截图」，此时若保留之前的展开，用户会以为
// 全盖住了而实际没有——这种错觉比不做打码更危险。
watch(() => state.revealEpoch, () => { revealed.value = false })

const hidden = computed(() =>
  !props.plain && state.privacyMode && !revealed.value && !!props.value)

const shown = computed(() =>
  hidden.value ? mask(props.value, props.kind) : props.value)

/** 打码后与原文一致说明这个值没什么可藏的（占位符、空值），按钮就别出现了。 */
const maskable = computed(() =>
  !props.plain && !!props.value && mask(props.value, props.kind) !== props.value)

async function onCopy() {
  await copy(props.value)
  toast({ tone: 'accent', title: '已复制到剪贴板' })
}
</script>

<template>
  <span class="mk">
    <span class="mk__v" :class="{ 'is-hidden': hidden }">{{ shown }}</span>

    <button v-if="maskable && state.privacyMode" class="mk__btn"
            :title="revealed ? '重新打码' : '显示原文'"
            :aria-label="revealed ? '重新打码' : '显示原文'"
            @click.stop="revealed = !revealed">{{ revealed ? '◡' : '◉' }}</button>

    <button v-if="copyable" class="mk__btn" title="复制原文" aria-label="复制原文"
            @click.stop="onCopy()">⧉</button>
  </span>
</template>

<style scoped>
.mk { display: inline-flex; align-items: center; gap: 5px; min-width: 0; }
.mk__v { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 打码态压暗一点，一眼能看出"这里被藏起来了"而不是"数据就长这样" */
.mk__v.is-hidden { color: var(--text-secondary); letter-spacing: 0.02em; }

.mk__btn {
  flex: 0 0 auto; border: 0; background: transparent; cursor: pointer;
  padding: 0 2px; line-height: 1; font-size: 11px;
  color: var(--text-tertiary); opacity: 0.55;
  transition: opacity var(--dur-fast), color var(--dur-fast);
}
.mk__btn:hover { opacity: 1; color: var(--accent); }
.mk__btn:focus-visible { outline: 1px solid var(--accent); outline-offset: 1px; opacity: 1; }
</style>
