import type { Format } from '../api/client'

// Very small, dependency-free tokenizer used only for editor highlighting.
// It never affects the underlying text: each line is rendered as spans.
interface Tok {
  cls: string
  text: string
}

const span = (cls: string, text: string): Tok => ({ cls, text })

function highlightJSONLine(line: string): Tok[] {
  const toks: Tok[] = []
  const re = /("(?:\\.|[^"\\])*")(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d+)?([eE][+-]?\d+)?|([{}[\],])/g
  let last = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(line))) {
    if (m.index > last) toks.push(span('tok-punct', line.slice(last, m.index)))
    if (m[1] !== undefined) {
      toks.push(span(m[2] ? 'tok-key' : 'tok-str', m[1]))
      if (m[2]) toks.push(span('tok-punct', m[2]))
    } else if (m[3]) toks.push(span('tok-bool', m[3]))
    else if (m[4] !== undefined || /^-?\d/.test(m[0])) toks.push(span('tok-num', m[0]))
    else toks.push(span('tok-punct', m[0]))
    last = re.lastIndex
  }
  if (last < line.length) toks.push(span('', line.slice(last)))
  return toks
}

function highlightYAMLLine(line: string): Tok[] {
  const trimmed = line.trimStart()
  if (trimmed.startsWith('#')) return [span('tok-comment', line)]
  const toks: Tok[] = []
  const re = /^(\s*)([^:]+)(:\s?)(.*)$/
  const m = line.match(re)
  if (!m) return [span('', line)]
  toks.push(span('', m[1]))
  toks.push(span('tok-key', m[2]))
  toks.push(span('tok-punct', m[3]))
  let rest = m[4]
  if (/^(true|false)$/.test(rest)) toks.push(span('tok-bool', rest))
  else if (/^-?\d+(\.\d+)?$/.test(rest)) toks.push(span('tok-num', rest))
  else if (rest.startsWith('"') || rest.startsWith("'")) toks.push(span('tok-str', rest))
  else if (rest.startsWith('#')) toks.push(span('tok-comment', rest))
  else toks.push(span('', rest))
  return toks
}

function highlightPropsLine(line: string): Tok[] {
  const trimmed = line.trimStart()
  if (trimmed.startsWith('#') || trimmed.startsWith('!')) return [span('tok-comment', line)]
  const idx = line.search(/[=:\s]/)
  if (idx < 0) return [span('', line)]
  return [
    span('tok-key', line.slice(0, idx)),
    span('tok-punct', line[idx]),
    span('tok-str', line.slice(idx + 1)),
  ]
}

function highlightTOMLLine(line: string): Tok[] {
  const trimmed = line.trim()
  if (trimmed.startsWith('#')) return [span('tok-comment', line)]
  if (trimmed.startsWith('[')) return [span('tok-key', line)]
  const eq = line.indexOf('=')
  if (eq < 0) return [span('', line)]
  const key = line.slice(0, eq)
  const val = line.slice(eq)
  const vm = val.match(/^=\s*(.*)$/)
  const rest = vm ? vm[1] : ''
  let valTok: Tok
  if (/^(true|false)$/.test(rest)) valTok = span('tok-bool', '= ' + rest)
  else if (/^-?\d/.test(rest)) valTok = span('tok-num', '= ' + rest)
  else if (rest.startsWith('"') || rest.startsWith("'")) valTok = span('tok-str', '= ' + rest)
  else valTok = span('', '= ' + rest)
  return [span('tok-key', key), valTok]
}

export function highlightLine(format: Format, line: string): Tok[] {
  switch (format) {
    case 'json':
      return highlightJSONLine(line)
    case 'yaml':
      return highlightYAMLLine(line)
    case 'properties':
      return highlightPropsLine(line)
    case 'toml':
      return highlightTOMLLine(line)
  }
}
