export function frontendLicensesPlugin(): {
  name: string
  apply: 'build'
  config(): { build: { rolldownOptions: { output: { postBanner: string } } } }
  generateBundle(options: unknown, bundle: Record<string,
    { type: 'chunk'; code: string; modules: Record<string, unknown> } | { type: 'asset' }
  >): Promise<void>
}
