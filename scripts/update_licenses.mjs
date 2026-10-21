import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdir, readFile, readdir, writeFile } from 'node:fs/promises'
import { dirname, isAbsolute, join, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('../', import.meta.url))
const frontendManifest = join(root, '.vista/build/frontend-license-packages.json')
const output = join(root, 'licenses/NOTICE')
const legalName = /^(?:licen[cs]e|copying|notice|copyright|third[-_]party)(?:$|[._-])/i
const sourceOrBinary = /\.(?:go|[cm]?[jt]sx?|py|rs|c|h|png|jpe?g|svg|woff2?|ttf|wasm|map)$/i
const ignoredDirectories = new Set(['.git', '.github', 'node_modules', 'testdata'])
const compare = (a, b) => a < b ? -1 : a > b ? 1 : 0
const normalizeText = text => text.replace(/\r\n/g, '\n').replace(/[ \t]+$/gm, '').trimEnd() + '\n'
// This npm release omitted LICENSE; its registry gitHead identifies this text.
const npmLicenseOverrides = {
  '@ant-design/icons-svg@4.6.0': {
    directory: 'ant-design-icons-svg',
    source: 'https://github.com/ant-design/ant-design-icons/blob/7f2516ac91226d2b41f93b35cb5197c8d94f7189/LICENSE',
  },
}

async function readJSON(path) {
  return JSON.parse(await readFile(path, 'utf8'))
}

async function writeChanged(path, content, check = false) {
  const previous = await readFile(path, 'utf8').catch(error => {
    if (error.code !== 'ENOENT') throw error
    return ''
  })
  if (previous === content) return
  if (check) throw new Error('License notices are stale. Run make licenses.')
  await mkdir(dirname(path), { recursive: true })
  await writeFile(path, content)
}

// Resolve from the actual module file, including nested npm dependencies.
export async function packageForModule(id, base = root) {
  const clean = id.split('?')[0]
  if (!isAbsolute(clean) || !clean.split(/[\\/]/).includes('node_modules')) return undefined
  for (let directory = dirname(clean); directory !== dirname(directory); directory = dirname(directory)) {
    const path = join(directory, 'package.json')
    const metadata = await readJSON(path).catch(error => {
      if (error.code !== 'ENOENT') throw error
      return undefined
    })
    if (!metadata?.name || !metadata.version) continue
    const location = relative(base, directory).split(sep).join('/')
    if (location.startsWith('../') || isAbsolute(location)) throw new Error(`Package is outside the repository: ${id}`)
    return { location, name: metadata.name, version: metadata.version }
  }
  throw new Error(`No npm package metadata found for ${id}`)
}

// Vite supplies the modules in its output chunks, rather than every package in
// node_modules. Build/test-only dependencies are not attributed as shipped code.
export function frontendLicensesPlugin() {
  return {
    name: 'vista-frontend-licenses',
    apply: 'build',
    async generateBundle(_options, bundle) {
      const chunks = Object.values(bundle).filter(item => item.type === 'chunk')
      const ids = new Set(chunks.flatMap(chunk => Object.keys(chunk.modules)))
      const packages = new Map()
      for (const id of [...ids].sort(compare)) {
        const entry = await packageForModule(id)
        if (entry) packages.set(entry.location, entry)
      }
      if (!packages.size) throw new Error('Frontend build did not report any dependency packages')
      const entries = [...packages.values()].sort((a, b) => compare(a.location, b.location))
      await writeChanged(frontendManifest, JSON.stringify(entries, null, 2) + '\n')
      for (const chunk of chunks) chunk.code = '/*! Vista and third-party licenses: /licenses */\n' + chunk.code
    },
  }
}

export async function licenseFiles(directory, recursive = false) {
  const files = []
  async function visit(current) {
    for (const entry of await readdir(current, { withFileTypes: true })) {
      const path = join(current, entry.name)
      if (entry.isFile() && legalName.test(entry.name) && !sourceOrBinary.test(entry.name)) {
        const text = normalizeText(await readFile(path, 'utf8'))
        if (!text.trim()) throw new Error(`Empty license or notice: ${path}`)
        files.push({ name: relative(directory, path).split(sep).join('/'), text })
      } else if (entry.isDirectory() && !ignoredDirectories.has(entry.name) && (recursive || /^(?:licenses?|third[-_]party)$/i.test(entry.name))) {
        await visit(path)
      }
    }
  }
  await visit(directory)
  return files.sort((a, b) => compare(a.name, b.name))
}

function go(args) {
  return execFileSync('go', args, { cwd: root, env: { ...process.env, GOWORK: 'off' }, encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 })
}

async function goNotices() {
  const requirements = JSON.parse(go(['mod', 'edit', '-json'])).Require
  const list = () => go(['list', '-m', '-f', '{{.Path}}\t{{.Version}}\t{{.Dir}}', ...requirements.map(item => item.Path)])
    .trim().split('\n').map(line => {
      const [name, version, directory] = line.trimEnd().split('\t')
      return { name, version, directory }
    })
  let modules = list()
  const missing = modules.filter(item => !item.directory)
  if (missing.length) {
    go(['mod', 'download', ...missing.map(item => `${item.name}@${item.version}`)])
    modules = list()
  }
  const entries = []
  for (const module of modules) {
    if (!module.directory) throw new Error(`Go module is unavailable: ${module.name}@${module.version}`)
    const files = await licenseFiles(module.directory, true)
    if (!files.length) throw new Error(`No license found for Go module ${module.name}@${module.version}`)
    entries.push({ name: module.name, version: module.version, scope: 'Go dependency', files })
  }
  const toolchain = JSON.parse(go(['env', '-json', 'GOROOT', 'GOVERSION']))
  const goLicense = await readFile(join(toolchain.GOROOT, 'LICENSE'), 'utf8').catch(error => {
    // Some distribution packages install Go's license outside GOROOT.
    if (error.code !== 'ENOENT') throw error
    return readFile(join(root, 'licenses/go/LICENSE'), 'utf8')
  })
  const files = [{ name: 'LICENSE', text: goLicense }]
  // Include the standard library's separately licensed components, not the
  // compiler's test fixtures or tools, which are not shipped with Vista.
  const stdlib = await licenseFiles(join(toolchain.GOROOT, 'src'), true)
  files.push(...stdlib.filter(item => !item.name.startsWith('cmd/') && !item.name.includes('/testdata/')))
  entries.push({ name: 'Go standard library', version: toolchain.GOVERSION, scope: 'Runtime', source: 'https://go.dev/LICENSE', files })
  return entries
}

async function npmNotices() {
  const lock = await readJSON(join(root, 'web/package-lock.json'))
  const packages = await readJSON(frontendManifest)
  // OpenCC's conversion data is used to generate the Chinese regional catalogs.
  packages.push({ location: 'web/node_modules/opencc-js', name: 'opencc-js', version: lock.packages['node_modules/opencc-js'].version, generated: true })
  const entries = []
  for (const item of packages) {
    if (!item.location.startsWith('web/node_modules/') || item.location.split('/').includes('..')) throw new Error(`Invalid npm package location: ${item.location}`)
    const locked = lock.packages[item.location.slice('web/'.length)]
    const directory = join(root, item.location)
    const metadata = await readJSON(join(directory, 'package.json'))
    if (!locked || locked.version !== item.version || metadata.name !== item.name || metadata.version !== item.version) {
      throw new Error(`npm dependency changed: ${item.name}; rebuild with make web`)
    }
    let files = await licenseFiles(directory)
    const override = npmLicenseOverrides[`${item.name}@${item.version}`]
    if (!files.length && override) files = await licenseFiles(join(root, 'licenses', override.directory))
    if (!files.length) throw new Error(`No license found for npm package ${item.name}@${item.version}`)
    entries.push({ name: item.name, version: item.version, scope: item.generated ? 'Generated regional catalogs' : 'Frontend dependency', license: metadata.license || locked.license, source: override?.source, files })
  }
  return entries
}

async function sourceNotices() {
  const catalog = await readJSON(join(root, 'internal/provider/omp_models.json'))
  return Promise.all([
    { name: 'Pi', version: 'b9ab2a6238d2c91cf0c6ebf836dfd05883f9db51', scope: 'Adapted context-overflow rules', directory: 'pi', source: 'https://github.com/badlogic/pi-mono' },
    { name: 'Oh My Pi', version: `overflow rules f1ac114d8d3438bee43010aee559bfe720ba3224; catalog blob ${catalog.revision}`, scope: 'Adapted context-overflow rules and bundled model catalog', directory: 'oh-my-pi', source: 'https://github.com/can1357/oh-my-pi' },
    { name: 'models.dev', version: 'runtime catalog', scope: 'Model metadata downloaded from models.dev or its mirrors', directory: 'models.dev', source: 'https://github.com/anomalyco/models.dev' },
  ].map(async item => ({ ...item, files: await licenseFiles(join(root, 'licenses', item.directory)) })))
}

export function renderNotices(entries, projectLicense) {
  const documents = new Map()
  const sections = [...entries].sort((a, b) => compare(`${a.scope}\n${a.name}\n${a.version}`, `${b.scope}\n${b.name}\n${b.version}`)).map(entry => {
    if (!entry.files.length) throw new Error(`No license documents for ${entry.name}`)
    const references = entry.files.map(file => {
      const text = normalizeText(file.text)
      const hash = createHash('sha256').update(text).digest('hex')
      documents.set(hash, text)
      return `  ${file.name}: ${hash}`
    })
    return `${entry.name}@${entry.version}\n  Use: ${entry.scope}\n${entry.source ? `  Source: ${entry.source}\n` : ''}${entry.license ? `  Declared license: ${entry.license}\n` : ''}${references.join('\n')}\n`
  })
  return 'VISTA LICENSE AND THIRD-PARTY NOTICES\n'
    + 'Generated by node scripts/update_licenses.mjs; do not edit.\n\n'
    + projectLicense.trimEnd() + '\n\n'
    + 'Third-party components retain their original licenses and copyrights.\n'
    + 'The inventory covers pinned Go dependencies, packages used by the frontend\n'
    + 'bundle, generated regional catalogs, and adapted code/model metadata.\n'
    + 'Some listed components or auxiliary notices may not be used by every target.\n'
    + 'Document texts normalize line endings and trailing whitespace.\n'
    + 'Identical documents are reproduced once and referenced by their SHA-256.\n\n'
    + 'COMPONENT INVENTORY\n\n' + sections.join('\n') + '\n'
    + 'LICENSE AND NOTICE DOCUMENTS\n\n'
    + [...documents].sort(([a], [b]) => compare(a, b)).map(([hash, text]) => `${'='.repeat(72)}\nSHA-256: ${hash}\n${'='.repeat(72)}\n\n${text}`).join('\n')
}

export async function updateLicenses({ check = false } = {}) {
  const entries = [...await sourceNotices(), ...await goNotices(), ...await npmNotices()]
  const content = renderNotices(entries, await readFile(join(root, 'LICENSE'), 'utf8'))
  await writeChanged(output, content, check)
  return { components: entries.length, bytes: Buffer.byteLength(content) }
}

if (import.meta.main) {
  try {
    if (process.argv.slice(2).some(argument => argument !== '--check')) throw new Error('Usage: node scripts/update_licenses.mjs [--check]')
    const result = await updateLicenses({ check: process.argv.includes('--check') })
    console.log(`License notices: ${result.components} components, ${result.bytes} bytes.`)
  } catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
