import { createApp } from 'vue'
import { i18n, initializeLanguage } from '../../src/i18n'
import Controls from './Controls.vue'
import ConversationScroll from './ConversationScroll.vue'
import '../../src/style.css'

await initializeLanguage()
const fixture = new URLSearchParams(location.search).has('conversation-scroll') ? ConversationScroll : Controls
createApp(fixture).use(i18n).mount('#app')
