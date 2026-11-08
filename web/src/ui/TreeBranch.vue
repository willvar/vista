<script setup lang="ts">
import { TreeView } from '@ark-ui/vue/tree-view'
import { FileOutlined, FolderOutlined, RightOutlined } from '@antdv-next/icons'
import type { TreeNode } from './types'
defineProps<{ node: TreeNode; indexPath: number[] }>()
const emit = defineEmits<{ activate: [value: string] }>()
</script>

<template>
  <TreeView.NodeProvider :node="node" :index-path="indexPath">
    <TreeView.Branch v-if="node.children">
      <TreeView.BranchControl :data-directory-path="node.value">
        <TreeView.BranchIndicator><RightOutlined aria-hidden="true" /></TreeView.BranchIndicator><FolderOutlined aria-hidden="true" />
        <TreeView.BranchText>{{ node.label }}</TreeView.BranchText>
      </TreeView.BranchControl>
      <TreeView.BranchContent>
        <TreeBranch v-for="(child, index) in node.children" :key="child.value" :node="child" :index-path="[...indexPath, index]" @activate="emit('activate', $event)" />
      </TreeView.BranchContent>
    </TreeView.Branch>
    <TreeView.Item v-else :data-file-path="node.value" @click="emit('activate', node.value)" @keydown.enter.stop.prevent="emit('activate', node.value)" @keydown.space.stop.prevent="emit('activate', node.value)">
      <FileOutlined aria-hidden="true" /><TreeView.ItemText>{{ node.label }}</TreeView.ItemText><span v-if="node.modified" class="ui-tree-modified" aria-hidden="true">●</span>
    </TreeView.Item>
  </TreeView.NodeProvider>
</template>
