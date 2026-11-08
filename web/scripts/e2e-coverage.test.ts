import assert from 'node:assert/strict'
import test from 'node:test'
import { browserScope, browserSelection, localBrowserArgumentsValid, responsiveGrep } from './e2e-coverage.ts'

const files = ['compaction.spec.ts', 'conversation-sync.spec.ts', 'execution-flow.spec.ts', 'session-flow.spec.ts', 'session-manager.spec.ts', 'statistics.spec.ts', 'usage.spec.ts']
test('local coverage includes consumers, staged/new specs and core smoke', () => {
  const scope = browserScope(['web/src/state/conversation.ts', 'web/e2e/session-flow.spec.ts'], files)
  assert.equal(scope.full, false)
  assert.deepEqual(scope.files, ['compaction.spec.ts', 'conversation-sync.spec.ts', 'execution-flow.spec.ts', 'session-flow.spec.ts', 'usage.spec.ts'])
  const selection = new RegExp(browserSelection(scope.files)!)
  assert.ok(selection.test('desktop onboarding.spec.ts connect @smoke'))
  assert.ok(selection.test('desktop compaction.spec.ts scoped summary usage'))
  assert.equal(selection.test('desktop statistics.spec.ts unrelated totals'), false)
})
test('shared or unknown source changes conservatively select the whole browser suite', () => {
  for (const path of ['web/src/ui/Select.vue', 'web/src/style.css', 'internal/server/routes.go', 'Makefile', 'web/playwright.config.ts', 'web/e2e/fixtures.ts', 'web/scripts/e2e-coverage.ts']) {
    assert.deepEqual(browserScope([path], files), { full: true, files: [...files].sort() }, path)
  }
  assert.equal(browserSelection(files, true), undefined)
})
test('documentation and unit-only changes keep smoke without inventing affected browser tests', () => {
  const scope = browserScope(['AGENTS.md', 'docs/testing.md', 'internal/store/display_messages_test.go', 'web/src/state/conversation.test.ts'], files)
  assert.deepEqual(scope, { full: false, files: [] })
  assert.equal(browserSelection(scope.files), '@smoke')
})
test('regenerated frontend bundles follow source coverage and remain conservative without source', () => {
  const assets = ['internal/server/assets/app.js', 'internal/server/assets/vista-web.css']
  const source = ['web/src/state/conversation.ts']
  assert.deepEqual(browserScope([...source, ...assets], files), browserScope(source, files))
  assert.equal(browserScope(assets, files).full, true)
  assert.equal(browserScope([...assets, 'web/src/state/conversation.test.ts'], files).full, true)
})
test('responsive coverage retains touch/layout checks while business recovery runs once on desktop', () => {
  for (const title of ['mobile-portrait layout.spec.ts composer fits', 'mobile-landscape execution-flow.spec.ts new runs clear the composer checklist', 'mobile-portrait i18n.spec.ts UI language de translated settings @responsive']) assert.ok(responsiveGrep.test(title), title)
  for (const title of ['mobile-portrait state-sync.spec.ts workspace initializes once', 'mobile-landscape model-retry.spec.ts stopping during the wait', 'mobile-portrait i18n.spec.ts browser detection, searchable override']) assert.equal(responsiveGrep.test(title), false, title)
})

test('local runner accepts execution options but cannot filter out required coverage', () => {
  assert.ok(localBrowserArgumentsValid(['--list', '--workers=2', '--max-failures=1', '--timeout=60000', '--reporter=json']))
  for (const argument of ['--grep=@smoke', '-g', '--project=desktop', '--shard=1/2', '--last-failed', '--only-changed', '--config=other.ts', 'compaction.spec.ts']) {
    assert.equal(localBrowserArgumentsValid([argument]), false, argument)
  }
})
