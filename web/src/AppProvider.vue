<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import enUS from 'antdv-next/locale/en_US'
import enGB from 'antdv-next/locale/en_GB'
import zhCN from 'antdv-next/locale/zh_CN'
import zhTW from 'antdv-next/locale/zh_TW'
import zhHK from 'antdv-next/locale/zh_HK'
import esES from 'antdv-next/locale/es_ES'
import deDE from 'antdv-next/locale/de_DE'
import jaJP from 'antdv-next/locale/ja_JP'
import koKR from 'antdv-next/locale/ko_KR'
import frFR from 'antdv-next/locale/fr_FR'
import ptBR from 'antdv-next/locale/pt_BR'
import ptPT from 'antdv-next/locale/pt_PT'
import ruRU from 'antdv-next/locale/ru_RU'
import XProvider from '@antdv-next/x/dist/x-provider/index'
import { languageInfo, type Catalog } from './i18n/languages.ts'
import { vistaTheme } from './ui/theme'

const { locale, t } = useI18n({ useScope: 'global' })
const componentLocales = {
  en: enUS, 'en-GB': enGB, 'zh-CN': zhCN, 'zh-TW': zhTW, 'zh-HK': zhHK, 'zh-MO': zhHK, 'zh-SG': zhCN, 'zh-MY': zhCN,
  es: esES, 'es-419': esES, de: deDE, 'de-CH': deDE, ja: jaJP, ko: koKR, fr: frFR, 'pt-BR': ptBR, 'pt-PT': ptPT, ru: ruRU,
} satisfies Record<Catalog, typeof enUS>
// X only ships English and Simplified Chinese. Supply its labels from the same
// catalog as the app so switching language updates both component libraries.
const componentLocale = computed(() => ({
  ...componentLocales[languageInfo(locale.value).catalog],
  locale: locale.value,
  global: { ...componentLocales[languageInfo(locale.value).catalog].global, close: t('close') },
  Conversations: { create: t('newConversation') },
  Sender: { stopLoading: t('cancelExecution'), speechRecording: t('speechRecording') },
  Actions: {
    feedbackLike: t('helpfulReply'), feedbackDislike: t('unhelpfulReply'),
    audio: t('playAudio'), audioRunning: t('audioPlaying'), audioError: t('audioError'), audioLoading: t('audioLoading'),
  },
  Bubble: { editableOk: t('feedbackInvitationAccept'), editableCancel: t('cancel') },
  Mermaid: { zoomIn: t('zoomIn'), zoomOut: t('zoomOut'), zoomReset: t('reset'), download: t('download'), code: t('code'), image: t('image') },
  Folder: { selectFile: t('selectFile'), loadError: t('fileLoadFailed'), loadFailed: t('fileLoadFailed'), noService: t('fileServiceUnavailable') },
}))
</script>

<template>
  <XProvider :theme="vistaTheme" :locale="componentLocale"><slot /></XProvider>
</template>
