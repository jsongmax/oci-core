<script setup lang="ts">
/** §6.6 CredentialField —— 私钥输入：失焦立即打码，无显示/导出入口 */
import { computed, ref } from 'vue'

const props = defineProps<{ modelValue: string; fingerprint?: string; existing?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()

const focused = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
const fileError = ref('')
const fileName = ref('')

const filled = computed(() => props.modelValue.trim().length > 20)
const shown = computed(() => (focused.value ? props.modelValue : filled.value ? '•'.repeat(24) : ''))

/**
 * 读取本地 .pem 文件。
 *
 * 文件只在浏览器内存里读一次就丢，不上传原文件——私钥走的还是
 * 保存账号那一条路径（后端加密落库），这里只是替用户省掉打开
 * 文本编辑器再复制粘贴的麻烦。
 */
async function onFile(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  // 清掉 value，否则连续选同一个文件不会再触发 change。
  input.value = ''
  if (!file) return

  fileError.value = ''
  fileName.value = ''

  // 私钥文件只有几 KB。明显超标的多半是选错了文件。
  if (file.size > 64 * 1024) {
    fileError.value = `文件有 ${Math.round(file.size / 1024)} KB，不像是私钥，请确认选对了文件`
    return
  }

  const text = (await file.text()).trim()

  if (!text.includes('-----BEGIN')) {
    fileError.value = '文件里没有找到 PEM 区块，请选择 .pem 私钥文件'
    return
  }
  if (text.includes('PUBLIC KEY')) {
    fileError.value = '这是公钥文件。需要的是私钥（oci_api_key.pem）'
    return
  }
  if (text.includes('ENCRYPTED')) {
    fileError.value = '该私钥带口令，本工具不支持。请先解密：' +
      'openssl rsa -in oci_api_key.pem -out oci_api_key_plain.pem'
    return
  }

  fileName.value = file.name
  emit('update:modelValue', text)
}
</script>

<template>
  <div class="field">
    <div class="cred__label">
      <label for="pem">API 私钥（PEM）</label>
      <button type="button" class="cred__upload" @click="fileInput?.click()">
        从文件读取
      </button>
      <input ref="fileInput" type="file" class="cred__file"
             accept=".pem,.key,application/x-pem-file,text/plain"
             @change="onFile" />
    </div>

    <textarea id="pem" class="textarea cred" :class="{ 'is-filled': filled && !focused }"
              :style="{ height: focused ? '140px' : '44px' }"
              :value="shown"
              :placeholder="existing ? '留空则不修改' : '-----BEGIN PRIVATE KEY-----'"
              spellcheck="false" autocomplete="off"
              @focus="focused = true" @blur="focused = false"
              @input="emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)" />

    <span v-if="fileError" class="cred__note is-err">{{ fileError }}</span>
    <span v-else class="cred__note" :class="{ 'is-ok': filled }">
      {{ filled
        ? `已加密存储 · 指纹 ${fingerprint || '待校验'}${fileName ? ` · 来自 ${fileName}` : ''}`
        : '失焦后立即打码，界面上不存在显示或导出入口' }}
    </span>
  </div>
</template>

<style scoped>
.cred { transition: height var(--dur-fast) var(--ease-standard); }
.cred.is-filled { border-color: var(--success); }
.cred__label { display: flex; align-items: center; gap: 8px; }
.cred__label > label { flex: 1 1 auto; font-size: 12px; color: var(--text-secondary); }
.cred__upload {
  flex: none; height: 24px; padding: 0 10px; border-radius: var(--radius-sm);
  border: 1px solid var(--border-default); background: transparent;
  color: var(--accent); font-size: 11px; cursor: pointer;
}
.cred__upload:hover { border-color: var(--accent); background: var(--accent-soft); }
.cred__file { display: none; }
.cred__note { font-size: 11px; color: var(--text-tertiary); line-height: 16px; }
.cred__note.is-ok { color: var(--success); }
.cred__note.is-err { color: var(--danger); }
</style>
