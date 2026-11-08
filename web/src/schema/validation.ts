import type { ErrorObject } from 'ajv'

export type FormIssue = { path: string; message: string }
export function additionalErrors(issues: FormIssue[]): ErrorObject[] {
  return issues.map(issue => ({ keyword: 'business', instancePath: issue.path, schemaPath: '', params: {}, message: issue.message }))
}

export function validationMessage(error: ErrorObject, t: (key: string, values?: Record<string, string | number>) => string): string {
  const limit = error.params.limit
  switch (error.keyword) {
    case 'business': return error.message || t('configFieldInvalid')
    case 'required': return t('configFieldRequired')
    case 'type': return t(error.params.type === 'integer' ? 'configFieldInteger' : error.params.type === 'number' ? 'configFieldNumber' : 'configFieldInvalid')
    case 'minimum': return t('configFieldMinimum', { limit })
    case 'maximum': return t('configFieldMaximum', { limit })
    case 'minLength': return error.params.limit === 1 ? t('configFieldRequired') : t('configFieldMinLength', { limit })
    case 'maxLength': return t('configFieldMaxLength', { limit })
    case 'minItems': return t('configFieldMinItems', { limit })
    case 'maxItems': return t('configFieldMaxItems', { limit })
    case 'uniqueItems': return t('configFieldUniqueItems')
    case 'enum': case 'const': return t('configFieldOption')
    case 'if': return '' // The failing branch already supplies its specific field errors.
    default: return t('configFieldInvalid')
  }
}
