import type * as Monaco from 'monaco-editor'
import type { JavaLspSession } from './session'

/** Map LSP CompletionItemKind → Monaco CompletionItemKind (subset). */
function monacoKind(
  monaco: typeof Monaco,
  kind?: number,
): Monaco.languages.CompletionItemKind {
  const K = monaco.languages.CompletionItemKind
  switch (kind) {
    case 2:
      return K.Method
    case 3:
      return K.Function
    case 4:
      return K.Constructor
    case 5:
      return K.Field
    case 6:
      return K.Variable
    case 7:
      return K.Class
    case 8:
      return K.Interface
    case 9:
      return K.Module
    case 10:
      return K.Property
    case 14:
      return K.Keyword
    case 17:
      return K.File
    case 1:
      return K.Text
    default:
      return K.Method
  }
}

/**
 * Register Monaco completion for Java that forwards to jdtls
 * (typing `obj.` → member methods).
 */
export function registerJavaLspCompletion(
  monaco: typeof Monaco,
  getSession: () => JavaLspSession | null,
  getPath: () => string | null,
  getModel: () => Monaco.editor.ITextModel | null,
): Monaco.IDisposable {
  return monaco.languages.registerCompletionItemProvider('java', {
    triggerCharacters: ['.', '@'],
    provideCompletionItems: async (model, position, context, token) => {
      const session = getSession()
      const path = getPath()
      if (!session || !path || !path.endsWith('.java')) {
        return { suggestions: [] }
      }
      if (model !== getModel() || token.isCancellationRequested) return { suggestions: [] }
      const version = model.getVersionId()
      try {
        await session.didChange(path, model.getValue())
        if (getModel() !== model || getPath() !== path || token.isCancellationRequested) return { suggestions: [] }
        const trigger =
          context.triggerKind === monaco.languages.CompletionTriggerKind.TriggerCharacter
            ? context.triggerCharacter
            : undefined
        const items = await session.completion(
          path,
          { line: position.lineNumber - 1, character: position.column - 1 },
          trigger,
        )
        if (token.isCancellationRequested || getPath() !== path || getSession() !== session ||
          getModel() !== model || model.isDisposed() || model.getVersionId() !== version) {
          return { suggestions: [] }
        }
        const word = model.getWordUntilPosition(position)
        const range: Monaco.IRange = {
          startLineNumber: position.lineNumber,
          endLineNumber: position.lineNumber,
          startColumn: word.startColumn,
          endColumn: word.endColumn,
        }
        return {
          suggestions: items.map((it, i) => ({
            label: it.label,
            kind: monacoKind(monaco, it.kind),
            detail: it.detail,
            documentation: it.documentation,
            insertText: it.insertText ?? it.label,
            filterText: it.filterText ?? it.label,
            sortText: it.sortText ?? String(i).padStart(5, '0'),
            range,
          })),
        }
      } catch {
        return { suggestions: [] }
      }
    },
  })
}
