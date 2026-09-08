import type { editor, IDisposable } from 'monaco-editor'
import { IdeaColors } from './ideaLight'

/**
 * Heuristic IntelliJ-like Java coloring without jdtls:
 * fields → purple, annotations → olive, method decls → teal.
 * True semantic tokens arrive with P2 jdtls.
 */
const FIELD_DECL =
  /(?:^|\n)[ \t]*(?:(?:public|protected|private|static|final|volatile|transient|synchronized|native|abstract|default|strictfp)\s+)+([\w.$]+(?:\s*<[^;{]+>)?(?:\s*\[\s*\])*)\s+(\w+)\s*(?:=|;)/g

const THIS_FIELD = /\b(?:this|super)\.(\w+)\b/g
const ANNOTATION = /@[\w.]+/g
const METHOD_DECL =
  /(?:^|\n)[ \t]*(?:(?:public|protected|private|static|final|synchronized|native|abstract|default|strictfp)\s+)+(?:<[^>]+>\s+)?[\w.$]+(?:\s*<[^;(]+>)?(?:\s*\[\s*\])*\s+(\w+)\s*\(/g

function offsetToPos(model: editor.ITextModel, offset: number): { line: number; col: number } {
  const pos = model.getPositionAt(offset)
  return { line: pos.lineNumber, col: pos.column }
}

function isInStringOrComment(text: string, index: number): boolean {
  const lineStart = text.lastIndexOf('\n', index - 1) + 1
  const prefix = text.slice(lineStart, index)
  if (prefix.includes('//')) return true
  let inStr = false
  let strCh = ''
  for (let i = 0; i < prefix.length; i++) {
    const c = prefix[i]
    if (!inStr && c === '/' && prefix[i + 1] === '*') return true
    if (!inStr && (c === '"' || c === "'")) {
      inStr = true
      strCh = c
      continue
    }
    if (inStr && c === '\\') {
      i++
      continue
    }
    if (inStr && c === strCh) inStr = false
  }
  return inStr
}

export function applyJavaIdeaDecorations(
  monaco: typeof import('monaco-editor'),
  editorInstance: editor.IStandaloneCodeEditor,
  model: editor.ITextModel,
): IDisposable {
  let decorationIds: string[] = []

  const paint = () => {
    const text = model.getValue()
    if (text.length > 400_000) {
      decorationIds = editorInstance.deltaDecorations(decorationIds, [])
      return
    }

    const decs: editor.IModelDeltaDecoration[] = []

    const push = (index: number, length: number, cls: string) => {
      if (length <= 0 || isInStringOrComment(text, index)) return
      const start = offsetToPos(model, index)
      const end = offsetToPos(model, index + length)
      decs.push({
        range: {
          startLineNumber: start.line,
          startColumn: start.col,
          endLineNumber: end.line,
          endColumn: end.col,
        },
        options: {
          inlineClassName: cls,
          stickiness: monaco.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges,
        },
      })
    }

    FIELD_DECL.lastIndex = 0
    for (let m = FIELD_DECL.exec(text); m; m = FIELD_DECL.exec(text)) {
      const name = m[2]
      if (name === 'class' || name === 'interface' || name === 'enum' || name === 'record') continue
      const nameIndex = m.index + m[0].lastIndexOf(name)
      push(nameIndex, name.length, 'idea-tok-field')
    }

    THIS_FIELD.lastIndex = 0
    for (let m = THIS_FIELD.exec(text); m; m = THIS_FIELD.exec(text)) {
      const name = m[1]
      const nameIndex = m.index + m[0].indexOf(name)
      push(nameIndex, name.length, 'idea-tok-field')
    }

    ANNOTATION.lastIndex = 0
    for (let m = ANNOTATION.exec(text); m; m = ANNOTATION.exec(text)) {
      push(m.index, m[0].length, 'idea-tok-annotation')
    }

    METHOD_DECL.lastIndex = 0
    for (let m = METHOD_DECL.exec(text); m; m = METHOD_DECL.exec(text)) {
      const name = m[1]
      if (name === 'if' || name === 'for' || name === 'while' || name === 'switch' || name === 'catch') {
        continue
      }
      const nameIndex = m.index + m[0].lastIndexOf(name)
      push(nameIndex, name.length, 'idea-tok-method')
    }

    decorationIds = editorInstance.deltaDecorations(decorationIds, decs)
  }

  paint()
  const sub = model.onDidChangeContent(() => paint())

  return {
    dispose() {
      sub.dispose()
      decorationIds = editorInstance.deltaDecorations(decorationIds, [])
    },
  }
}

export function ensureJavaDecorationStyles(): void {
  const id = 'idea-java-tok-styles'
  if (document.getElementById(id)) return
  const style = document.createElement('style')
  style.id = id
  style.textContent = `
.idea-tok-field { color: ${IdeaColors.instanceField} !important; }
.idea-tok-annotation { color: ${IdeaColors.metadata} !important; }
.idea-tok-method { color: ${IdeaColors.functionDecl} !important; }
`
  document.head.appendChild(style)
}
