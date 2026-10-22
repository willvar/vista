import './reset-browser'
import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import ConversationPage from './pages/ConversationPage.vue'
import SettingsPage from './pages/SettingsPage.vue'
import SessionManagerPage from './pages/SessionManagerPage.vue'
import { i18n } from './i18n'
import './style.css'
const router = createRouter({ history: createWebHistory(), routes: [
  { path: '/', name: 'home', component: ConversationPage },
  { path: '/settings', name: 'settings-index', components: { default: ConversationPage, overlay: SettingsPage } },
  { path: '/settings/general', name: 'settings-general', components: { default: ConversationPage, overlay: SettingsPage } },
  { path: '/settings/personalization', name: 'settings-personalization', components: { default: ConversationPage, overlay: SettingsPage } },
  { path: '/settings/models', name: 'settings', components: { default: ConversationPage, overlay: SettingsPage } },
  { path: '/settings/about', name: 'settings-about', components: { default: ConversationPage, overlay: SettingsPage } },
  { path: '/settings/storage', name: 'settings-storage', components: { default: ConversationPage, overlay: SettingsPage } },
  { path: '/settings/search', name: 'settings-search', components: { default: ConversationPage, overlay: SettingsPage } },
  { path: '/settings/mcp', name: 'settings-mcp', components: { default: ConversationPage, overlay: SettingsPage } },
  { path: '/settings/skills', name: 'settings-skills', components: { default: ConversationPage, overlay: SettingsPage } },
  { path: '/sessions', name: 'session-manager', components: { default: ConversationPage, overlay: SessionManagerPage } },
  { path: '/sessions/:id', name: 'session', component: ConversationPage },
  { path: '/:pathMatch(.*)*', redirect: '/' },
] })
createApp(App).use(router).use(i18n).mount('#app')
