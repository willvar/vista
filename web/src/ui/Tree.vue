<script setup lang="ts">
import { computed } from 'vue'
import { TreeViewRoot, TreeViewTree, createTreeCollection } from '@ark-ui/vue/tree-view'
import TreeBranch from './TreeBranch.vue'
import type { TreeNode } from './types'

const props = defineProps<{ nodes: TreeNode[]; value?: string; label: string }>()
const emit = defineEmits<{ 'update:value': [value: string] }>()
const collection = computed(() => createTreeCollection<TreeNode>({
  rootNode: { value: '', label: '', children: props.nodes },
  nodeToValue: node => node.value,
  nodeToString: node => node.label,
}))
</script>

<template>
  <TreeViewRoot class="ui-tree" :collection="collection" :selected-value="value ? [value] : []" selection-mode="single">
    <TreeViewTree :aria-label="label">
      <TreeBranch v-for="(node, index) in nodes" :key="node.value" :node="node" :index-path="[index]" @activate="emit('update:value', $event)" />
    </TreeViewTree>
  </TreeViewRoot>
</template>

<style>
.ui-tree{min-width:0;color:var(--text-secondary);font-size:var(--font-size-body)}
.ui-tree [data-part="tree"]{outline:none}
.ui-tree [data-part="item"],.ui-tree [data-part="branch-control"]{display:flex;align-items:center;gap:.45rem;min-height:var(--control-height);padding:.35rem .55rem;border-radius:7px;cursor:pointer;overflow-wrap:anywhere}
.ui-tree [data-part="item"]:hover,.ui-tree [data-part="branch-control"]:hover{background:var(--surface-subtle)}
.ui-tree [data-part="item"][data-selected]{background:var(--accent-soft);color:var(--text)}
.ui-tree [data-part="item"]:focus-visible,.ui-tree [data-part="branch-control"]:focus-visible{outline:2px solid var(--accent);outline-offset:-2px}
.ui-tree [data-part="branch-content"]{padding-inline-start:1rem}
.ui-tree [data-part="branch-indicator"]{display:inline-flex;flex:none;transition:transform .16s}
.ui-tree [data-part="branch-indicator"][data-state="open"]{transform:rotate(90deg)}
.ui-tree svg{flex:none}.ui-tree [data-part="item-text"],.ui-tree [data-part="branch-text"]{min-width:0}
.ui-tree-modified{margin-inline-start:auto;color:var(--accent)}
@media(prefers-reduced-motion:reduce){.ui-tree [data-part="branch-indicator"]{transition:none}}
</style>
