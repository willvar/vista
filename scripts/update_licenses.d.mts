export function frontendLicensesPlugin(): {
  name: string
  apply: 'build'
  generateBundle(options: unknown, bundle: Record<string,
    { type: 'chunk'; code: string; modules: Record<string, unknown> } | { type: 'asset' }
  >): Promise<void>
}
