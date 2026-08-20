<script setup lang="ts">
/** §6.5 危险操作三级门槛：L2 确认（取消为主按钮），L3 输名确认 */
import { computed, ref, watch } from 'vue'
import { useStore } from '@/store'

const { state } = useStore()
const typed = ref('')

watch(() => state.confirm, () => { typed.value = '' })

const req = computed(() => state.confirm)
const isL3 = computed(() => req.value?.level === 3)
const canConfirm = computed(() => {
  if (!req.value) return false
  if (req.value.level === 2) return true
  return typed.value.trim() === req.value.noun
})

function close() { state.confirm = null }
function commit() {
  if (!req.value || !canConfirm.value) return
  const fn = req.value.onConfirm
  close()
  fn()
}
</script>

<template>
  <Transition name="scrim">
    <div v-if="req" class="scrim" role="dialog" aria-modal="true" @keydown.esc="close">
      <div class="dlg" :class="{ 'dlg--l3': isL3 }">
        <h2 class="dlg__title">{{ req.title }}</h2>
        <p class="dlg__body">{{ req.body }}</p>

        <template v-if="req.level === 3">
          <div class="dlg__losses">
            <p class="dlg__losses-head">以下内容将被永久删除，无法恢复：</p>
            <ul>
              <li v-for="l in req.losses" :key="l"><span>·</span><span class="mono">{{ l }}</span></li>
            </ul>
          </div>
          <label class="dlg__label" for="confirm-noun">
            请输入{{ req.nounLabel }} <span class="mono dlg__noun">{{ req.noun }}</span> 以确认：
          </label>
          <input id="confirm-noun" v-model="typed" class="input mono dlg__input"
                 :class="{ 'is-match': canConfirm }" :placeholder="req.noun" autocomplete="off" />
        </template>

        <footer class="dlg__foot">
          <!-- L2：取消是主按钮；L3 确认按钮不得是默认焦点（§10.2） -->
          <button class="btn" :class="{ 'btn--primary': req.level === 2 }" @click="close">取消</button>
          <button class="btn" :class="isL3 ? 'btn--danger-solid' : 'btn--warning'"
                  :disabled="!canConfirm" @click="commit">{{ req.okLabel }}</button>
        </footer>
      </div>
    </div>
  </Transition>
</template>

<style scoped>
.scrim {
  position: fixed; inset: 0; z-index: 50; display: flex; align-items: center; justify-content: center;
  background: var(--scrim); backdrop-filter: blur(3px);
}
.dlg {
  width: 480px; padding: 24px; border-radius: var(--radius-xl);
  border: 1px solid var(--border-default); background: var(--bg-elevated);
  box-shadow: var(--shadow-4); animation: pop var(--dur-normal) var(--ease-spring);
}
.dlg--l3 { border-color: var(--danger); box-shadow: var(--shadow-4), var(--glow-danger); }
.dlg__title { margin: 0; font-size: 20px; line-height: 28px; font-weight: 600; }
.dlg--l3 .dlg__title { color: var(--danger); }
.dlg__body { margin: 10px 0 0; font-size: 13px; color: var(--text-secondary); }
.dlg__losses {
  margin-top: 14px; padding: 14px; border-radius: var(--radius-md);
  background: var(--bg-inset); border: 1px solid var(--border-subtle);
}
.dlg__losses-head { margin: 0 0 8px; font-size: 12px; color: var(--text-secondary); }
.dlg__losses ul { margin: 0; padding: 0; list-style: none; display: flex; flex-direction: column; gap: 5px; }
.dlg__losses li { display: flex; gap: 8px; font-size: 12px; }
.dlg__losses li > span:first-child { color: var(--danger); }
.dlg__label { display: block; margin-top: 14px; font-size: 12px; color: var(--text-secondary); }
.dlg__noun { color: var(--text-primary); }
.dlg__input { width: 100%; margin-top: 6px; height: 38px; }
.dlg__input.is-match { border-color: var(--success); }
.dlg__foot { margin-top: 20px; display: flex; justify-content: flex-end; gap: 10px; }
</style>
