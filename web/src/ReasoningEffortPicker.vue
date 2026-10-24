<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import { Select as ASelect, Slider as ASlider } from 'antdv-next'

const props = defineProps<{ options: { label: string; value: string }[]; disabled?: boolean }>()
const value = defineModel<string>('value', { required: true })
const { t } = useI18n({ useScope: 'global' })
const selectId = useId()
const smallViewport = window.matchMedia('(max-width: 830px)')
const smallScreen = ref(smallViewport.matches)
function updateSmallScreen() { smallScreen.value = smallViewport.matches }
onMounted(() => smallViewport.addEventListener('change', updateSmallScreen))
onUnmounted(() => smallViewport.removeEventListener('change', updateSmallScreen))
const orderedOptions = computed(() => [
  ...props.options.filter(option => option.value === 'none'),
  ...props.options.filter(option => option.value !== 'none'),
])
const selectedIndex = computed({
  get: () => Math.max(0, orderedOptions.value.findIndex(option => option.value === value.value)),
  set: (index: number) => {
    if (!props.disabled && orderedOptions.value[index]) value.value = orderedOptions.value[index]!.value
  },
})
const formatIndex = (index?: number) => orderedOptions.value[index ?? selectedIndex.value]?.label || ''
</script>

<template>
  <div class="reasoning-control" role="group" :data-effort="value" :aria-label="t('reasoningEffort')" :aria-disabled="disabled || undefined">
    <template v-if="smallScreen && options.length > 1">
      <label :for="selectId" class="visually-hidden">{{ t('reasoningEffort') }}</label>
      <a-select :id="selectId" v-model:value="value" class="reasoning-select sender-picker"
        variant="borderless" :options="orderedOptions" :styles="{ content: { color: 'var(--effort-color)' } }" :disabled="disabled" :aria-label="t('reasoningEffort')" />
    </template>
    <template v-else>
      <a-slider v-if="options.length > 1" v-model:value="selectedIndex" class="reasoning-slider"
        :min="0" :max="options.length - 1" :step="1" dots :disabled="disabled"
        :tooltip="{ formatter: formatIndex }" :aria-label-for-handle="t('reasoningEffort')"
        :aria-value-text-formatter-for-handle="formatIndex" />
      <span class="reasoning-current"><Transition name="reasoning-label"><span :key="value">{{ formatIndex() }}</span></Transition></span>
    </template>
  </div>
</template>

<style scoped>
.reasoning-control{--effort-purple:light-dark(#9333ea,#d8b4fe);--effort-color:var(--accent);--effort-glow:transparent;display:inline-flex;align-items:center;gap:8px;flex:none;min-width:0;color:var(--text-secondary);height:32px}
.reasoning-control[data-effort=none]{--effort-color:var(--muted)}
.reasoning-control[data-effort=off]{--effort-color:var(--muted)}
.reasoning-control[data-effort^="budget:"] .reasoning-select{width:9rem}
.reasoning-control[data-effort=medium]{--effort-color:color-mix(in srgb,var(--accent),var(--effort-purple) 25%)}
.reasoning-control[data-effort=high]{--effort-color:color-mix(in srgb,var(--accent),var(--effort-purple) 50%)}
.reasoning-control[data-effort=xhigh]{--effort-color:color-mix(in srgb,var(--accent),var(--effort-purple) 75%)}
.reasoning-control[data-effort=max]{--effort-color:var(--effort-purple)}
.reasoning-control:is([data-effort=high],[data-effort=xhigh],[data-effort=max]){--effort-glow:color-mix(in srgb,var(--effort-color) 28%,transparent)}
.reasoning-slider{width:96px;margin:10px 6px}
.reasoning-select{width:6.5rem}
.reasoning-control .reasoning-slider :deep(.ant-slider-track){background:var(--effort-color);box-shadow:0 0 8px var(--effort-glow);transition:width .22s cubic-bezier(.22,1,.36,1),background-color .18s ease,box-shadow .22s ease}
.reasoning-control .reasoning-slider :deep(.ant-slider-handle){transition:left .22s cubic-bezier(.22,1,.36,1)}
.reasoning-control .reasoning-slider :deep(.ant-slider-handle::after){box-shadow:0 0 0 2px var(--effort-color),0 0 12px 2px var(--effort-glow);transition:inset-inline-start .18s ease,inset-block-start .18s ease,width .18s ease,height .18s ease,box-shadow .22s ease,outline .18s ease}
.reasoning-control .reasoning-slider :deep(.ant-slider-handle:is(:hover,:active,:focus)::after){box-shadow:0 0 0 3px var(--effort-color),0 0 14px 3px var(--effort-glow);outline:5px solid color-mix(in srgb,var(--effort-color) 15%,transparent)}
.reasoning-control .reasoning-slider :deep(.ant-slider-dot-active){border-color:var(--effort-color);transition:border-color .18s ease}
.reasoning-control .reasoning-slider.ant-slider-lock :deep(.ant-slider-track){transition:background-color .18s ease,box-shadow .22s ease}
.reasoning-control .reasoning-slider.ant-slider-lock :deep(.ant-slider-handle){transition:none}
.reasoning-current{display:grid;overflow:hidden;color:var(--effort-color);font-size:14px;line-height:20px;white-space:nowrap;transition:color .18s ease}
.reasoning-current>span{grid-area:1/1}
.reasoning-label-enter-active{transition:opacity .18s ease,transform .22s cubic-bezier(.22,1,.36,1)}
.reasoning-label-leave-active{transition:opacity .1s ease,transform .14s ease}
.reasoning-label-enter-from{opacity:0;transform:translateY(5px)}
.reasoning-label-leave-to{opacity:0;transform:translateY(-5px)}
.reasoning-control[aria-disabled=true]{--effort-color:var(--muted);--effort-glow:transparent}
@media(prefers-reduced-motion:reduce){.reasoning-control,.reasoning-control :deep(*),.reasoning-control :deep(*::before),.reasoning-control :deep(*::after){transition:none!important}}
</style>
