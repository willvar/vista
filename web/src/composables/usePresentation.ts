import { useI18n } from 'vue-i18n'
import { computed, type Ref } from 'vue'
import type { Message, ModelCatalog, ModelInfo, ModelResultView, StreamEvent } from '../client/types'
import { formatDateTime } from '../ui/datetime.ts'

export function displayModelName(model: Pick<ModelInfo, 'id' | 'modelName'>) {
  return model.modelName?.trim() || model.id
}

export function catalogModelName(id: string, models: readonly ModelInfo[], provider?: string) {
  const matches = models.filter(model => model.id === id && (!provider || model.provider === provider || model.providerId === provider))
  return displayModelName(matches.find(model => model.modelName?.trim()) || { id })
}

type ModelIdentity = { key?: string; provider?: string; id?: string }

export function historicalModelInfo(identity: ModelIdentity, models: readonly ModelInfo[]): ModelInfo | undefined {
  if (!identity.id) return undefined
  const matches = models.filter(model => (!identity.key || model.key === identity.key)
    && model.id === identity.id
    && (!identity.provider || model.provider === identity.provider || model.providerId === identity.provider))
  if ((identity.key || identity.provider) && matches.length === 1) return matches[0]
  // A removed connection must keep its recorded identity, even when another
  // connection now serves the same model ID.
  return { key: identity.key || '', provider: identity.provider || '', id: identity.id }
}

export function messageModelIdentity(message: Message, modelByRun: Record<string, ModelInfo>): ModelIdentity {
  const actual = modelByRun[message.runId]
  const compatible = actual && (!message.modelKey || !actual.key || message.modelKey === actual.key)
    && (!message.modelId || message.modelId === actual.id)
    && (!message.provider || message.provider === actual.provider || message.provider === actual.providerId)
  return {
    key: message.modelKey || (compatible ? actual.key : undefined),
    provider: message.provider || (compatible ? actual.provider : undefined),
    id: message.modelId || (compatible ? actual.id : undefined),
  }
}

export function modelSearchText(model: ModelInfo, label: string) {
  return [label, displayModelName(model), model.id, model.name, model.providerId, model.provider].filter(Boolean).join(' ')
}

/** Stable provider/connection grouping shared by the composer and settings. */
export function modelPickerOptions(models: readonly ModelInfo[], locale: string, providerLabel: (model: ModelInfo) => string, fullLabel: (model: ModelInfo) => string) {
  const compare = new Intl.Collator(locale, { numeric: true, sensitivity: 'base' }).compare
  const groups = new Map<string, { key: string; label: string; models: ModelInfo[] }>()
  for (const model of models) {
    if (model.key === 'demo') continue
    const key = model.connectionId || JSON.stringify([model.name, model.providerId || model.provider, model.baseUrl])
    if (!groups.has(key)) groups.set(key, { key, label: providerLabel(model), models: [] })
    groups.get(key)!.models.push(model)
  }
  return [...groups.values()]
    .sort((a, b) => compare(a.label, b.label) || compare(a.key, b.key))
    .map(group => ({ key: group.key, label: group.label, options: group.models
      .sort((a, b) => compare(displayModelName(a), displayModelName(b)) || compare(a.key, b.key))
      .map(model => {
        const label = fullLabel(model)
        return { label: displayModelName(model), fullLabel: label, searchText: modelSearchText(model, label), value: model.key }
      }),
    }))
}

export function reasoningEffortSuffix(effort: string | undefined, t: (key: string, params?: Record<string, unknown>) => string, te: (key: string) => boolean) {
  const value = effort?.trim()
  if (!value || value.toLowerCase() === 'none') return ''
  const key = 'reasoning' + value.charAt(0).toUpperCase() + value.slice(1)
  return ` · ${te(key) ? t(key) : value}`
}

export function messageReasoningEffort(message: Message, modelByRun: Record<string, ModelInfo>, results: readonly ModelResultView[] = message.modelResults || []) {
  const result = results.filter(item => item.runId === message.runId && !item.depth
    && (!message.outputTurn || item.turn === message.outputTurn)
    && (!message.modelId || item.id === message.modelId)
    && (!message.provider || item.provider === message.provider)).sort((a, b) => a.sequence - b.sequence).at(-1)
  if (result?.reasoningEffort !== undefined) return result.reasoningEffort
  const fromEvent = modelByRun[message.runId]
  if (!fromEvent || message.modelId && fromEvent.id !== message.modelId || message.provider && fromEvent.provider !== message.provider && fromEvent.providerId !== message.provider
    || message.modelKey && fromEvent.key && fromEvent.key !== message.modelKey) return undefined
  return fromEvent.reasoningEffort
}

export function usePresentation(catalog: Ref<ModelCatalog>) {
  const { t, te, locale } = useI18n({ useScope: 'global' })
  const localTime = (utc: string) => formatDateTime(utc, locale.value)
  function statusLabel(status: string) {
    const key = 'status' + status.replace(/(^|_)([a-z])/g, (_, __: string, letter: string) => letter.toUpperCase())
    return te(key) ? t(key) : status
  }
  function endpointHost(baseUrl: string) { try { return new URL(baseUrl).host } catch { return baseUrl } }
  function modelNameById(id: string, provider?: string) { return catalogModelName(id, catalog.value.models, provider) }
  function formatModelWithReasoning(label: string, effort?: string) { return label + reasoningEffortSuffix(effort, t, te) }
  function modelLabel(model: ModelInfo, effort?: string) {
    const provider = model.providerId || model.provider
    const source = model.name || (provider === 'opencode' ? 'Zen' : provider === 'opencode-go' ? 'Zen Go'
      : (model.provider === 'openai-chat' || model.provider === 'openai-responses') && (!model.providerId || model.providerId === 'openai')
        ? (model.baseUrl ? `${t('custom')} (${endpointHost(model.baseUrl)})` : t('custom'))
        : model.providerId || model.provider || t('demo'))
    return formatModelWithReasoning(`${source} · ${displayModelName(model)}`, effort)
  }
  function settingProviderName(model: Pick<ModelInfo, 'name' | 'providerId' | 'provider'>) {
    return model.name || (model.providerId === 'opencode' ? 'OpenCode Zen' : model.providerId === 'opencode-go' ? 'OpenCode Zen Go'
      : model.provider === 'openai-chat' || model.provider === 'openai-responses' ? t('customProvider') : model.providerId || model.provider)
  }
  const modelChoices = computed(() => modelPickerOptions(catalog.value.models, locale.value, settingProviderName, modelLabel))
  function historicalModelLabel(identity: ModelIdentity) {
    const model = historicalModelInfo(identity, catalog.value.models)
    return model ? model.provider || model.name ? modelLabel(model) : displayModelName(model) : ''
  }
  function messageModel(message: Message, modelByRun: Record<string, ModelInfo>, results?: readonly ModelResultView[]) {
    const label = historicalModelLabel(messageModelIdentity(message, modelByRun))
    const reasoning = reasoningEffortSuffix(messageReasoningEffort(message, modelByRun, results), t, te)
    return label ? label + reasoning : t('modelUnavailable')
  }
  function activityLabel(event: StreamEvent) {
    if (event.kind === 'tool') return t('activityTool', { name: event.data?.tool?.name || t('tool'), status: statusLabel(event.data?.status || 'unknown'), summary: event.data?.tool?.summary || '' })
    if (event.kind === 'run_finished') return t('activityRunFinished', { status: statusLabel(event.data?.status || 'finished') })
    if (event.kind === 'context_notice') return t('contextCompacted')
    return ''
  }
  return { localTime, statusLabel, modelLabel, historicalModelLabel, settingProviderName, messageModel, activityLabel, modelNameById, modelSearchText, modelChoices, formatModelWithReasoning }
}
