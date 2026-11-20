import { createAjv, isVisible, RuleEffect, type ControlElement, type JsonSchema7 as JsonSchema } from '@jsonforms/core'
import type { QuestionAnswer, QuestionPrompt } from '@vista/client'

export type QuestionDraft = { options: string[]; custom: boolean; text: string }
export type QuestionData = Record<string, QuestionDraft>
export const emptyQuestionDraft = (): QuestionDraft => ({ options: [], custom: false, text: '' })
export const optionValue = (question: QuestionPrompt, index: number) => question.id ? question.options[index]!.id! : question.options[index]!.label

// Only this adapter creates schemas; models never supply layouts or validators.
export function questionDefinition(questions: QuestionPrompt[]) {
  const ajv = createAjv({ useDefaults: false, coerceTypes: false, removeAdditional: false, strictNumbers: true, logger: false })
  const conditions: JsonSchema[][] = []
  const properties: Record<string, JsonSchema> = {}
  const requirements: JsonSchema[] = []
  const entries = questions.map((prompt, index) => {
    // Internal paths do not depend on model IDs or translated display labels.
    const key = `q${index}`
    const values = prompt.options.map((_, at) => optionValue(prompt, at))
    const base: JsonSchema = {
      type: 'object', additionalProperties: false, required: ['options', 'custom', 'text'],
      properties: {
        options: { type: 'array', uniqueItems: true, items: values.length ? { type: 'string', enum: values } : { type: 'string' }, ...(!values.length ? { maxItems: 0 } : !prompt.multiple ? { maxItems: 1 } : {}) },
        custom: { type: 'boolean', ...(prompt.custom === false ? { const: false } : {}) },
        text: { type: 'string' },
      },
    }
    const answerSchema: JsonSchema = {
      ...base,
      allOf: [{
        if: { required: ['custom'], properties: { custom: { const: true } } },
        then: { properties: { text: { type: 'string', pattern: '\\S' }, ...(!prompt.multiple ? { options: { maxItems: 0 } } : {}) } },
        else: { properties: { options: { minItems: 1 } } },
      }],
    }
    let matches: JsonSchema[] = []
    if (prompt.when) {
      const parentIndex = questions.findIndex(question => question.id === prompt.when!.questionId)
      // Backend validation guarantees earlier single-choice references. Fail closed
      // for malformed recovered payloads rather than exposing an unrelated branch.
      if (parentIndex < 0 || parentIndex >= index || questions[parentIndex]!.multiple) {
        matches = [{ not: {} }]
      } else {
        matches = [...conditions[parentIndex]!, {
          type: 'object', required: [`q${parentIndex}`], properties: {
            [`q${parentIndex}`]: {
              type: 'object', required: ['options', 'custom'], properties: {
                options: { const: [prompt.when.optionId] }, custom: { const: false },
              },
            },
          },
        }]
      }
    }
    conditions.push(matches)
    const condition: JsonSchema = matches.length ? { allOf: matches } : {}
    properties[key] = base
    requirements.push({ if: condition, then: { required: [key], properties: { [key]: answerSchema } } })
    const control: ControlElement = {
      type: 'Control', scope: `#/properties/${key}`,
      options: { question: prompt, questionIndex: index, hasDependents: !!prompt.id && questions.some(question => question.when?.questionId === prompt.id) },
      ...(prompt.when ? { rule: { effect: RuleEffect.SHOW, condition: { scope: '#', schema: condition, failWhenUndefined: true } } } : {}),
    }
    return { key, index, prompt, control, validate: ajv.compile(answerSchema) }
  })
  const schema: JsonSchema = { type: 'object', properties, required: entries.map(entry => entry.key), additionalProperties: false, allOf: requirements }
  const active = (data: QuestionData) => entries.filter(entry => isVisible(entry.control, data, '', ajv, undefined))
  function normalize(value: Record<string, unknown>): QuestionData {
    const data: QuestionData = Object.fromEntries(entries.map(entry => {
      const raw = value[entry.key]
      const candidate = raw && typeof raw === 'object' && !Array.isArray(raw) ? raw as Partial<QuestionDraft> : {}
      const allowed = entry.prompt.options.map((_, at) => optionValue(entry.prompt, at))
      const custom = entry.prompt.custom !== false && candidate.custom === true
      const options = Array.isArray(candidate.options) ? [...new Set(candidate.options.filter(option => typeof option === 'string' && allowed.includes(option)))] : []
      return [entry.key, {
        options: entry.prompt.multiple ? options : custom ? [] : options.slice(0, 1),
        custom, text: typeof candidate.text === 'string' ? candidate.text : '',
      }]
    }))
    // Rules include ancestor conditions. Clearing in question order also prevents
    // stale descendant answers from activating another hidden descendant.
    for (const entry of entries) if (!isVisible(entry.control, data, '', ajv, undefined)) data[entry.key] = emptyQuestionDraft()
    return data
  }
  function answers(data: QuestionData): QuestionAnswer[] {
    return active(data).map(entry => {
      const draft = data[entry.key] || emptyQuestionDraft()
      return {
        ...(entry.prompt.id ? { questionId: entry.prompt.id } : {}), options: draft.options,
        ...(draft.custom && draft.text.trim() ? { text: draft.text.trim() } : {}),
      }
    })
  }
  return { entries, schema, active, normalize, answers, empty: () => normalize({}) }
}
