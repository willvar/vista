import './reset-browser'
import './ui/appearance'
import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import ConversationPage from './pages/ConversationPage.vue'
import { i18n, initializeLanguage } from './i18n'
import { installAssetRecovery, showBootFailure } from './asset-recovery'
import './style.css'
const SettingsPage = () => import('./pages/SettingsPage.vue')
const SessionManagerPage = () => import('./pages/SessionManagerPage.vue')
const router = createRouter({ history: createWebHistory(), routes: [
  { path: '/', name: 'home', component: ConversationPage },
  { path: '/settings', name: 'settings-index', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: '/settings/general', name: 'settings-general', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: '/settings/personalization', name: 'settings-personalization', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: '/settings/models', name: 'settings', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: '/settings/about', name: 'settings-about', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: '/settings/storage', name: 'settings-storage', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: '/settings/statistics', name: 'settings-statistics', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: '/settings/search', name: 'settings-search', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: '/settings/mcp', name: 'settings-mcp', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: '/settings/skills', name: 'settings-skills', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: '/sessions', name: 'session-manager', components: { default: ConversationPage, overlay: SessionManagerPage }, meta: { workspaceOverlay: 'sessions' } },
  { path: '/sessions/:id', name: 'session', component: ConversationPage },
  { path: '/:pathMatch(.*)*', redirect: '/' },
] })
installAssetRecovery(router)
initializeLanguage().then(() => createApp(App).use(router).use(i18n).mount('#app')).catch(showBootFailure)
