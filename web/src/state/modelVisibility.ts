import type { ModelCatalog, ModelInfo } from '../client/types'

export function visibleModels(catalog: ModelCatalog): ModelInfo[] {
  return catalog.models.filter(model => catalog.showUnrecognizedModels || model.recognized === true)
}

// Every restored or default choice follows the current visibility preference.
export function initialModelKey(catalog: ModelCatalog, preferred: readonly (string | undefined)[] = []): string {
  const models = visibleModels(catalog)
  const retained = preferred.find(key => key && models.some(model => model.key === key))
  if (retained) return retained
  return models.find(model => model.key === catalog.default)?.key || models[0]?.key || ''
}
