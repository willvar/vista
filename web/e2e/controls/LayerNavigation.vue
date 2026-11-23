<script setup lang="ts">
import { ref } from 'vue'
import { useRoute } from 'vue-router'
import { useRouteLayer } from '../../src/composables/routeLayers'

const route = useRoute()
const drawer = useRouteLayer('drawer', to => to.query.screen === 'resources')
const preview = useRouteLayer('preview', to => to.query.screen === 'resources')
const draft = ref('')
let delayedClose: (() => void) | undefined
function closeTwice() { preview.close(); preview.close() }
</script>

<template>
  <main v-if="route.query.screen === 'resources'">
    <h1>Resources</h1>
    <label>Draft <input v-model="draft"></label>
    <button @click="drawer.open('attachments')">Choose attachments</button>
    <section v-if="drawer.current.value !== undefined" role="dialog" aria-label="Attachments">
      <button @click="preview.open('cover')">Preview image</button>
      <button @click="delayedClose = drawer.captureClose()">Remember current drawer</button>
      <button @click="delayedClose?.()">Complete old request</button>
      <button @click="drawer.close()">Close drawer</button>
    </section>
    <section v-if="preview.current.value !== undefined" role="dialog" aria-label="Preview">
      <button @click="closeTwice">Double close</button>
    </section>
  </main>
  <main v-else>
    <h1>Home</h1>
    <RouterLink :to="{ query: { 'layer-navigation': null, screen: 'resources' } }">Resources</RouterLink>
  </main>
</template>
