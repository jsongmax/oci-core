<script setup lang="ts">
const props = defineProps<{ title: string; sub?: string; value?: string; modelValue: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [boolean] }>()
</script>

<template>
  <button type="button" class="swrow" role="switch" :aria-checked="modelValue"
          @click="emit('update:modelValue', !props.modelValue)">
    <span class="swrow__text">
      <span class="swrow__title">{{ title }}</span>
      <span v-if="sub" class="swrow__sub">{{ sub }}</span>
    </span>
    <span v-if="value" class="swrow__value mono">{{ value }}</span>
    <span class="track" :class="{ 'is-on': modelValue }"><span class="knob" /></span>
  </button>
</template>

<style scoped>
/* 根元素的类名必须够独特。
   Vue 的 scoped CSS 会把**父组件**的 scope ID 也打到子组件根元素上，
   所以父视图里任何一条 .row 规则都会命中这里，而两边特异性相同
   （都是 .row[data-v-xxx]），后加载的那份赢。SettingsView 给审计表格
   写了 .row { display: grid }，于是这个开关整个竖了起来——没有报错，
   只是布局塌了。共享组件的根类名不能用 row / item / card 这类通用词。 */
.swrow {
  display: flex; align-items: center; gap: 14px; width: 100%; padding: 14px 20px;
  border: 0; border-bottom: 1px solid var(--border-subtle); background: transparent;
  color: inherit; text-align: left; cursor: pointer;
}
.swrow:hover { background: var(--bg-hover); }
.swrow:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
.swrow__text { flex: 1 1 auto; min-width: 0; display: flex; flex-direction: column; }
.swrow__title { font-size: 13px; font-weight: 500; }
.swrow__sub { font-size: 12px; color: var(--text-tertiary); }
.swrow__value { font-size: 12px; color: var(--text-secondary); }
.track {
  width: 36px; height: 20px; flex: 0 0 auto; border-radius: var(--radius-full);
  background: var(--border-strong); position: relative; transition: background var(--dur-fast);
}
.track.is-on { background: var(--accent); }
.knob {
  position: absolute; top: 2px; left: 2px; width: 16px; height: 16px;
  border-radius: var(--radius-full); background: #fff;
  transition: transform var(--dur-fast) var(--ease-standard);
}
.track.is-on .knob { transform: translateX(16px); }
</style>
