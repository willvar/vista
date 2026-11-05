<script setup lang="ts">
import { ref } from 'vue'
import AppProvider from '../../src/AppProvider.vue'
import { Alert, Button, Checkbox, Dropdown, Empty, Input, InputNumber, InputPassword, Modal, Pagination, Popover, Radio, Select, Slider, Spin, Switch, Tabs, TextArea, Tooltip } from '../../src/ui'
import Bubble from '../../src/ui/Bubble.vue'
import CopyAction from '../../src/ui/CopyAction.vue'
import Sender from '../../src/ui/Sender.vue'
import Think from '../../src/ui/Think.vue'

const submits = ref(0)
const clicks = ref(0)
const text = ref('abc')
const area = ref('one\ntwo')
const autoSize = ref(true)
const secret = ref('secret')
const number = ref<number | null>(3)
const fraction = ref<number | null>(0.5)
const checked = ref(false)
const mixed = ref(true)
const changes = ref(0)
const radio = ref('a')
const switched = ref(false)
const selected = ref<string | number | (string | number)[]>()
const searchSelected = ref<string | number | (string | number)[]>()
const tags = ref<string | number | (string | number)[]>(['Alpha'])
const options = [{ value: 'a', label: 'Alpha' }, { value: 'b', label: 'Beta', disabled: true }, { value: 'g', label: 'Gamma' }]
const slider = ref(2)
const tab = ref('one')
const page = ref(1)
const menu = ref('')
const dialog = ref(false)
const busy = ref(false)
const cancelled = ref(0)
const confirmed = ref(0)
const message = ref('Draft')
const keys = ref(0)
const composerLevel = ref(2)
</script>

<template>
  <AppProvider><main class="controls-fixture">
    <section data-testid="buttons">
      <h1>Buttons</h1>
      <form @submit.prevent="submits++">
        <Button @click="clicks++">Ordinary</Button>
        <Button type="submit">Submit</Button>
        <Button disabled @click="clicks++">Disabled</Button>
        <Button loading @click="clicks++">Loading</Button>
        <Button href="#destination" @click="clicks++">Link</Button>
        <Button href="#destination" disabled @click="clicks++">Disabled link</Button>
        <Button href="#destination" loading @click="clicks++">Loading link</Button>
      </form>
      <output data-testid="clicks">{{ clicks }}</output><output data-testid="submits">{{ submits }}</output>
    </section>
    <section data-testid="text-inputs">
      <h2>Text</h2>
      <Input id="contract-input" v-model:value="text" allow-clear maxlength="8" aria-label="Text" />
      <Input value="Prefixed" aria-label="Prefixed text"><template #prefix><span>Prefix</span></template></Input>
      <Input value="Read only" readonly allow-clear aria-label="Read only text" />
      <Input value="Disabled" disabled allow-clear aria-label="Disabled text" />
      <Input value="Error" status="error" aria-label="Error text" />
      <InputPassword v-model:value="secret" maxlength="12" aria-label="Secret" />
      <InputPassword value="locked" disabled aria-label="Disabled secret" />
      <TextArea :value="area" :rows="5" aria-label="Fixed rows" />
      <TextArea v-model:value="area" :auto-size="autoSize ? { minRows: 2, maxRows: 4 } : false" aria-label="Autosize" />
      <Button @click="autoSize = !autoSize">Toggle autosize</Button>
      <TextArea value="Read only" readonly aria-label="Read only area" />
      <TextArea value="Disabled" disabled aria-label="Disabled area" />
    </section>
    <section data-testid="numbers">
      <h2>Numbers</h2>
      <InputNumber id="contract-number" v-model:value="number" :min="0" :max="8" :precision="0" aria-label="Integer" />
      <InputNumber v-model:value="fraction" :min="1 / 1024" :max="2" :step="1" aria-label="Fraction" />
      <InputNumber :value="3" :min="0" :max="8" disabled aria-label="Disabled number" />
      <output data-testid="number">{{ number === null ? 'empty' : number }}</output>
      <output data-testid="fraction">{{ fraction === null ? 'empty' : fraction }}</output>
    </section>
    <section data-testid="toggles">
      <h2>Toggles</h2>
      <Checkbox :checked="checked" :indeterminate="mixed" @update:checked="checked = $event; mixed = false" @change="changes++">Mixed checkbox</Checkbox>
      <Checkbox disabled>Disabled checkbox</Checkbox>
      <Checkbox :checked="false" @update:checked="changes++">Unacknowledged checkbox</Checkbox>
      <Radio name="contract-radio" :checked="radio === 'a'" @update:checked="radio = 'a'">First radio</Radio>
      <Radio name="contract-radio" :checked="radio === 'b'" @update:checked="radio = 'b'">Second radio</Radio>
      <Radio name="contract-radio" disabled>Disabled radio</Radio>
      <Switch v-model:checked="switched" aria-label="Switch" />
      <Switch disabled aria-label="Disabled switch" />
      <Switch loading aria-label="Loading switch" />
      <Switch :checked="false" aria-label="Unacknowledged switch" @update:checked="changes++" />
      <output data-testid="changes">{{ changes }}</output>
    </section>
    <section data-testid="selects">
      <h2>Selects</h2>
      <Select v-model:value="selected" :options="options" placeholder="Choose an option" allow-clear aria-label="Options" />
      <Select v-model:value="searchSelected" :options="options" placeholder="Search options" show-search aria-label="Search options" />
      <Select v-model:value="tags" mode="tags" :options="options" allow-clear aria-label="Tags" />
      <Select value="a" :options="options" allow-clear disabled aria-label="Disabled options" />
    </section>
    <section data-testid="slider">
      <h2>Slider</h2>
      <div data-testid="drag-origin" tabindex="0">Start an unrelated drag here</div>
      <Slider v-model:value="slider" :min="0" :max="4" dots :tooltip="{ formatter: value => `Level ${value}` }" aria-label-for-handle="Level" />
      <Slider :value="2" :min="0" :max="4" disabled aria-label-for-handle="Disabled level" />
    </section>
    <section data-testid="navigation">
      <h2>Navigation</h2>
      <Tabs v-model:active-key="tab" :items="[{ value: 'one', label: 'One' }, { value: 'two', label: 'Two' }]"><p>{{ tab }} panel</p></Tabs>
      <Pagination v-model:current="page" :total="35" :page-size="10" />
      <Pagination :current="2" :total="35" :page-size="10" disabled />
      <Dropdown :trigger="['click']" :menu="{ items: [{ key: 'enabled', label: 'Enabled item' }, { key: 'disabled', label: 'Disabled item', disabled: true }], onClick: item => menu = item.key }"><Button>Menu</Button></Dropdown>
      <output data-testid="menu">{{ menu }}</output>
    </section>
    <section data-testid="overlays">
      <h2>Overlays</h2>
      <Tooltip title="Tooltip content"><Button>Tooltip trigger</Button></Tooltip>
      <Popover trigger="click"><Button>Click popover</Button><template #content><Button>Inside click popover</Button></template></Popover>
      <Popover :trigger="['hover', 'click']"><Button>Hover popover</Button><template #content><Button>Inside hover popover</Button></template></Popover>
      <Button @click="dialog = true">Open dialog</Button>
      <Modal v-model:open="dialog" title="Control dialog" :closable="!busy" :keyboard="!busy" :mask-closable="!busy" :confirm-loading="busy" :cancel-button-props="{ disabled: busy }" destroy-on-hidden @cancel="cancelled++" @ok="confirmed++">
        <Input aria-label="Dialog input" />
        <Button @click="busy = !busy">Toggle busy</Button>
      </Modal>
      <output data-testid="cancelled">{{ cancelled }}</output><output data-testid="confirmed">{{ confirmed }}</output>
    </section>
    <section data-testid="messages">
      <h2>Messages</h2>
      <Sender v-model:value="message" :auto-size="{ minRows: 1, maxRows: 3 }" aria-label="Message" :on-key-down="() => keys++"><template #footer><Button>Sender footer</Button><Slider v-model:value="composerLevel" :min="0" :max="4" aria-label-for-handle="Composer level" /></template></Sender>
      <Sender value="Disabled" disabled aria-label="Disabled message" />
      <output data-testid="keys">{{ keys }}</output>
      <Bubble content="Message body"><template #header>Message header</template><template #footer>Message footer</template></Bubble>
      <Think title="Reasoning"><p>Hidden reasoning</p></Think>
      <CopyAction text="Complete copied message" />
    </section>
    <section data-testid="feedback">
      <h2>Feedback</h2>
      <Alert v-for="type in (['info', 'success', 'warning', 'error'] as const)" :key="type" :type="type" :message="type" :description="`${type} details`" show-icon />
      <Empty description="Nothing here"><Button>Empty action</Button></Empty>
      <Spin />
    </section>
  </main></AppProvider>
</template>

<style scoped>
.controls-fixture { width: min(640px, 100%); margin: 0 auto; padding: 16px; }
section { display: grid; gap: 12px; padding: 16px 0; border-bottom: 1px solid var(--border); }
form { display: flex; flex-wrap: wrap; gap: 8px; }
output { display: block; }
.ui-slider { margin: 12px; }
</style>
