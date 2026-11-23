import { createApp } from 'vue'
import { createRouter, createWebHistory, RouterView } from 'vue-router'
import { i18n, initializeLanguage } from '../../src/i18n'
import Controls from './Controls.vue'
import LayerNavigation from './LayerNavigation.vue'
import ConversationScroll from './ConversationScroll.vue'
import '../../src/style.css'

await initializeLanguage()
const params = new URLSearchParams(location.search)
if (params.has('layer-navigation')) {
  const router = createRouter({ history: createWebHistory(), routes: [{ path: '/', component: LayerNavigation }] })
  createApp(RouterView).use(router).use(i18n).mount('#app')
} else {
  createApp(params.has('conversation-scroll') ? ConversationScroll : Controls).use(i18n).mount('#app')
}
