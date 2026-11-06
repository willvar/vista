import { test } from 'node:test'
import assert from 'node:assert/strict'
import { diffLines } from './fileDiff.ts'
import { projectToolOutput } from './runTrace.ts'

test('file diffs preserve hunk coordinates, escaped content and missing newlines', () => {
  const unified = '--- source\n+++ source\n@@ -2,2 +2,2 @@\n same\n-old\n+++ <script>alert(1)</script>\n\\ No newline at end of file\n@@ -40 +41 @@\n-old tail\n+new tail\n'
  assert.deepEqual(diffLines(unified), [
    { kind: 'hunk', text: '@@ -2,2 +2,2 @@' },
    { kind: 'context', text: 'same', oldLine: 2, newLine: 2 },
    { kind: 'remove', text: 'old', oldLine: 3 },
    { kind: 'add', text: '++ <script>alert(1)</script>', newLine: 3 },
    { kind: 'notice', text: '\\ No newline at end of file' },
    { kind: 'hunk', text: '@@ -40 +41 @@' },
    { kind: 'remove', text: 'old tail', oldLine: 40 },
    { kind: 'add', text: 'new tail', newLine: 41 },
  ])
})

test('only write tools project saved diffs; historical metadata stays compatible', () => {
  const diff = { path: '/source.ts', created: true, unified: '@@ -0,0 +1 @@\n+hello\n' }
  const output = JSON.stringify({ path: diff.path, sha256: 'hash', diff })
  assert.deepEqual(projectToolOutput(output, 'main/child/write_file').diff, { ...diff, omitted: undefined })
  assert.equal(projectToolOutput(output, 'mcp_call_tool').diff, undefined)
  assert.equal(projectToolOutput('{"path":"/source.ts","sha256":"hash"}', 'write_file').body, '')
  assert.equal(projectToolOutput(JSON.stringify({ diff: { ...diff, omitted: 'too_large' } }), 'write_file').diff?.omitted, 'too_large')
  assert.equal(projectToolOutput(JSON.stringify({ diff: { ...diff, omitted: 'unavailable' } }), 'write_file').diff?.omitted, 'unavailable')
})
