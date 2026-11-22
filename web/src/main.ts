import './reset-browser'
import './ui/appearance'
import { createApp, defineComponent, h } from 'vue'
import { createRouter, createWebHistory, RouterView, useRoute, type RouteRecordRaw } from 'vue-router'
import App from './App.vue'
import AppProvider from './AppProvider.vue'
import { profileHome, profileIdPattern, routeProfileId } from './client/profileScope'
import ConversationPage from './pages/ConversationPage.vue'
import { i18n, initializeLanguage } from './i18n'
import { installAssetRecovery, showBootFailure } from './asset-recovery'
import './style.css'
const SettingsPage = () => import('./pages/SettingsPage.vue')
const SessionManagerPage = () => import('./pages/SessionManagerPage.vue')
const SchedulesPage = () => import('./pages/SchedulesPage.vue')
// The document, router and locale stay alive. Only the data-owning workspace
// is recreated, so streams, editors and revision cursors remain Profile-local.
const ProfileWorkspace = defineComponent({
  setup() {
    const route = useRoute()
    return () => {
      const profileId = routeProfileId(route.params)
      return h(App, { key: profileId, profileId })
    }
  },
})
const routes: RouteRecordRaw[] = [
  { path: '', name: 'home', component: ConversationPage },
  { path: 'settings', name: 'settings-index', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: 'settings/general', name: 'settings-general', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: 'settings/personalization', name: 'settings-personalization', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: 'settings/models', name: 'settings', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: 'settings/network', name: 'settings-network', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: 'settings/about', name: 'settings-about', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: 'settings/storage', name: 'settings-storage', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: 'settings/statistics', name: 'settings-statistics', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: 'settings/search', name: 'settings-search', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: 'settings/mcp', name: 'settings-mcp', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: 'settings/skills', name: 'settings-skills', components: { default: ConversationPage, overlay: SettingsPage }, meta: { workspaceOverlay: 'settings' } },
  { path: 'sessions', name: 'session-manager', components: { default: ConversationPage, overlay: SessionManagerPage }, meta: { workspaceOverlay: 'sessions' } },
  { path: 'schedules', name: 'schedules', components: { default: ConversationPage, overlay: SchedulesPage }, meta: { workspaceOverlay: 'schedules' } },
  { path: 'sessions/:id', name: 'session', component: ConversationPage },
  { path: ':pathMatch(.*)*', redirect: to => profileHome(routeProfileId(to.params)) },
]
const router = createRouter({ history: createWebHistory(), routes: [{
  path: `/:profilePrefix(profiles)?/:profileId(${profileIdPattern})?`,
  component: ProfileWorkspace,
  children: routes,
}] })
// Optional parent parameters let named navigation inherit the current Profile.
// Reject a partially matched or noncanonical prefix.
router.beforeEach(to => !(to.params.profilePrefix || to.params.profileId) || routeProfileId(to.params) !== 'default' || '/')
installAssetRecovery(router)
const Root = { render: () => h(AppProvider, null, { default: () => h(RouterView) }) }
initializeLanguage().then(() => createApp(Root).use(router).use(i18n).mount('#app')).catch(showBootFailure)
