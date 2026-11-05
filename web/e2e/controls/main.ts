import { createApp } from 'vue'
import { i18n } from '../../src/i18n'
import Controls from './Controls.vue'
import '../../src/style.css'

createApp(Controls).use(i18n).mount('#app')
