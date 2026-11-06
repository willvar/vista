export type DiffLine = { kind: 'hunk' | 'add' | 'remove' | 'context' | 'notice'; text: string; oldLine?: number; newLine?: number }

// Only interpret the unified format; file text remains escaped Vue text.
export function diffLines(unified: string): DiffLine[] {
  const result: DiffLine[] = []
  let oldLine = 0, newLine = 0, inHunk = false
  for (const text of unified.split('\n')) {
    const hunk = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(text)
    if (hunk) {
      oldLine = Number(hunk[1]); newLine = Number(hunk[2]); inHunk = true
      result.push({ kind: 'hunk', text })
    } else if (inHunk && text.startsWith('+')) result.push({ kind: 'add', text: text.slice(1), newLine: newLine++ })
    else if (inHunk && text.startsWith('-')) result.push({ kind: 'remove', text: text.slice(1), oldLine: oldLine++ })
    else if (inHunk && text.startsWith(' ')) result.push({ kind: 'context', text: text.slice(1), oldLine: oldLine++, newLine: newLine++ })
    else if (inHunk && text.startsWith('\\')) result.push({ kind: 'notice', text })
  }
  return result
}
