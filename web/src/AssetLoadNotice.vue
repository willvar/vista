<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { assetLoadFailed } from './asset-recovery'
import Button from './ui/Button.vue'
import Modal from './ui/Modal.vue'

const { t } = useI18n()
const confirming = ref(false)
function reload() { window.location.reload() }
</script>

<template>
  <div v-if="assetLoadFailed" class="error state-sync-error asset-load-notice" role="alert">
    <span>{{ t('frontendLoadFailed') }}</span>
    <Button @click="confirming = true">{{ t('frontendReload') }}</Button>
    <Button @click="assetLoadFailed = false">{{ t('close') }}</Button>
  </div>
  <Modal v-model:open="confirming" :title="t('frontendReload')" :ok-text="t('frontendReload')" @ok="reload">
    <p>{{ t('frontendReloadConfirm') }}</p>
  </Modal>
</template>
