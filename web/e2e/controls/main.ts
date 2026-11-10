import { createApp } from 'vue'
import { i18n, initializeLanguage } from '../../src/i18n'
import Controls from './Controls.vue'
import '../../src/style.css'

await initializeLanguage()
createApp(Controls).use(i18n).mount('#app')
