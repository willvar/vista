import { catalogs, messages } from '../src/i18n/catalogs.ts'
import { validateCatalogs } from '../src/i18n/validate.ts'

const errors = validateCatalogs(catalogs)
if (errors.length) {
  console.error(errors.join('\n'))
  process.exitCode = 1
} else {
  console.log(`i18n: ${Object.keys(catalogs).length} catalogs shared by ${Object.keys(messages).length} regional locales, ${Object.keys(messages.en).length} keys; placeholders and syntax verified.`)
}
