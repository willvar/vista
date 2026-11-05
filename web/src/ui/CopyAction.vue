<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { CheckOutlined, CopyOutlined } from '@antdv-next/icons'
import { useI18n } from 'vue-i18n'
import Tooltip from './Tooltip.vue'
const props = defineProps<{ text: string; fullText?: () => Promise<string> }>()
const { t } = useI18n()
const copied = ref(false)
const busy = ref(false)
const failure = ref('')
let timer: ReturnType<typeof setTimeout> | undefined
async function copy() {
  if (busy.value) return
  busy.value = true
  failure.value = ''
  try {
    const text = props.fullText ? await props.fullText() : props.text
    if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(text)
    else {
      const previous = document.activeElement as HTMLElement | null
      const input = document.createElement('textarea')
      input.value = text
      input.style.cssText = 'position:fixed;inset:0 auto auto -9999px;opacity:0'
      document.body.append(input)
      input.select()
      try { if (!document.execCommand('copy')) throw new Error(t('copyReply')) }
      finally { input.remove(); previous?.focus({ preventScroll: true }) }
    }
    copied.value = true
    clearTimeout(timer)
    timer = setTimeout(() => { copied.value = false }, 2000)
  } catch (error) { failure.value = String(error) }
  finally { busy.value = false }
}
onBeforeUnmount(() => clearTimeout(timer))
</script>

<template>
  <Tooltip :title="failure || t(copied ? 'copiedReply' : 'copyReply')"><button type="button" class="reply-action reply-copy" :aria-label="t('copyReply')" :disabled="busy" :aria-busy="busy || undefined" @click="copy"><CheckOutlined v-if="copied" /><CopyOutlined v-else /><span class="visually-hidden" aria-live="polite">{{ failure || (copied ? t('copiedReply') : '') }}</span></button></Tooltip>
</template>
