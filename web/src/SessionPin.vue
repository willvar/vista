<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { IconPinFilled, IconPin } from '@tabler/icons-vue'
import { Button as VButton, Tooltip as VTooltip } from './ui'
import type { Session } from './client/types'
import { useWorkspace } from './composables/useWorkspace'

const props = defineProps<{ session: Session }>()
const { t } = useI18n({ useScope: 'global' })
const { client, busy, error } = useWorkspace()
const saving = ref(false)
async function toggle() {
  if (busy.value || saving.value) return
  saving.value = true
  try {
    await client.request(`/sessions/${props.session.id}`, { method: 'PATCH', body: JSON.stringify({ pinned: !props.session.pinned }) })
  } catch (cause) { error.value = String(cause) }
  finally { saving.value = false }
}
</script>

<template>
  <v-tooltip :title="t(session.pinned ? 'unpinSession' : 'pinSession')">
    <v-button variant="text" class="icon-button session-pin" :class="{ 'session-pinned': session.pinned }"
      :aria-label="t(session.pinned ? 'unpinSession' : 'pinSession')" :aria-pressed="!!session.pinned"
      :disabled="busy || saving" @click.stop="toggle" @keydown.enter.stop @keydown.space.stop>
      <IconPinFilled v-if="session.pinned" /><IconPin v-else />
    </v-button>
  </v-tooltip>
</template>
