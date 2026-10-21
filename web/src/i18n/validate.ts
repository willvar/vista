import { baseCompile } from '@intlify/message-compiler'

function placeholders(message: string) {
  return [...new Set([...message.matchAll(/\{\s*([\w]+)\s*\}/g)].map(match => match[1]))].sort().join(',')
}

export function validateCatalogs(catalogs: Record<string, Record<string, string>>) {
  const errors: string[] = []
  const reference = catalogs.en
  for (const [locale, catalog] of Object.entries(catalogs)) {
    for (const key of Object.keys(reference)) {
      if (!(key in catalog)) errors.push(`${locale}.${key}: missing translation`)
    }
    for (const [key, value] of Object.entries(catalog)) {
      if (!(key in reference)) errors.push(`${locale}.${key}: unknown key`)
      if (typeof value !== 'string' || !value.trim()) {
        errors.push(`${locale}.${key}: empty translation`)
        continue
      }
      baseCompile(value, { onError: error => { errors.push(`${locale}.${key}: ${error.message}`) } })
      if (key in reference && placeholders(value) !== placeholders(reference[key])) {
        errors.push(`${locale}.${key}: expected placeholders {${placeholders(reference[key])}}, got {${placeholders(value)}}`)
      }
    }
  }
  return errors
}
