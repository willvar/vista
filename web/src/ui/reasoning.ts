export function reasoningChoiceLabel(value: string, t: (key: string, params?: Record<string, unknown>) => string): string {
  if (value.startsWith('budget:')) return t('reasoningBudget', { tokens: value.slice(7) })
  return t('reasoning' + value.charAt(0).toUpperCase() + value.slice(1))
}
