<script setup lang="ts">
import { TreeViewBranchIndicator, TreeViewBranchControl, TreeViewBranchContent, TreeViewNodeProvider, TreeViewBranchText, TreeViewItemText, TreeViewBranch, TreeViewItem } from '@ark-ui/vue/tree-view'
import { IconFile, IconFolder, IconChevronRight } from '@tabler/icons-vue'
import type { TreeNode } from './types'
defineProps<{ node: TreeNode; indexPath: number[] }>()
const emit = defineEmits<{ activate: [value: string] }>()
</script>

<template>
  <TreeViewNodeProvider :node="node" :index-path="indexPath">
    <TreeViewBranch v-if="node.children">
      <TreeViewBranchControl :data-directory-path="node.value">
        <TreeViewBranchIndicator><IconChevronRight aria-hidden="true" /></TreeViewBranchIndicator><IconFolder aria-hidden="true" />
        <TreeViewBranchText>{{ node.label }}</TreeViewBranchText>
      </TreeViewBranchControl>
      <TreeViewBranchContent>
        <TreeBranch v-for="(child, index) in node.children" :key="child.value" :node="child" :index-path="[...indexPath, index]" @activate="emit('activate', $event)" />
      </TreeViewBranchContent>
    </TreeViewBranch>
    <TreeViewItem v-else :data-file-path="node.value" @click="emit('activate', node.value)" @keydown.enter.stop.prevent="emit('activate', node.value)" @keydown.space.stop.prevent="emit('activate', node.value)">
      <IconFile aria-hidden="true" /><TreeViewItemText>{{ node.label }}</TreeViewItemText><span v-if="node.modified" class="ui-tree-modified" aria-hidden="true">●</span>
    </TreeViewItem>
  </TreeViewNodeProvider>
</template>
