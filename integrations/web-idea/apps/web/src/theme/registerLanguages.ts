import type * as Monaco from 'monaco-editor'
import { ideaDarkTheme, ideaLightTheme } from './ideaLight'

let registered = false

/** Register IDEA-like languages + theme once per Monaco instance. */
export function registerIdeaEditor(monaco: typeof Monaco): void {
	monaco.editor.defineTheme('idea-light', ideaLightTheme)
	monaco.editor.defineTheme('idea-dark', ideaDarkTheme)
  if (registered) return
  registered = true

  monaco.languages.register({ id: 'properties', extensions: ['.properties'], aliases: ['Properties'] })
  monaco.languages.setMonarchTokensProvider('properties', {
    defaultToken: '',
    tokenizer: {
      root: [
        [/[ \t\r\n]+/, ''],
        [/[#!].*$/, 'comment.properties'],
        [
          /([^\\:=\s](?:\\.|[^\\:=\s])*)([ \t]*)([:=])/,
          ['key.properties', '', 'separator.properties'],
        ],
        [/([^\\:=\s](?:\\.|[^\\:=\s])*)(.*)$/, ['key.properties', 'value.properties']],
        [/./, 'value.properties'],
      ],
    },
  })
  monaco.languages.setLanguageConfiguration('properties', {
    comments: { lineComment: '#' },
    autoClosingPairs: [],
  })

  // Keep JSON syntax highlighting without loading Monaco's JSON language
  // service worker. Navigation and diagnostics remain the gateway/jdtls job.
  monaco.languages.register({ id: 'json', extensions: ['.json'], aliases: ['JSON', 'json'] })
  monaco.languages.setMonarchTokensProvider('json', {
    defaultToken: '',
    tokenizer: {
      root: [
        [/[{}\[\]]/, 'delimiter.bracket'],
        [/[,:]/, 'delimiter'],
        [/'(?:[^'\\]|\\.)*'/, 'string.invalid'],
        [/"(?:[^\\"]|\\.)*"/, 'string'],
        [/-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/, 'number'],
        [/\b(?:true|false)\b/, 'keyword'],
        [/\bnull\b/, 'keyword'],
        [/[ \t\r\n]+/, 'white'],
        [/./, 'invalid'],
      ],
    },
  })
}

export function languageForPath(path: string | null): string {
  if (!path) return 'plaintext'
  const base = path.slice(path.lastIndexOf('/') + 1).toLowerCase()
  const ext = base.includes('.') ? base.slice(base.lastIndexOf('.') + 1) : ''
  switch (ext) {
    case 'java':
      return 'java'
    case 'kt':
    case 'kts':
      return 'kotlin'
    case 'xml':
      return 'xml'
    case 'gradle':
      return 'groovy'
    case 'json':
      return 'json'
    case 'md':
      return 'markdown'
    case 'yml':
    case 'yaml':
      return 'yaml'
    case 'properties':
      return 'properties'
    case 'ts':
    case 'tsx':
      return 'typescript'
    case 'js':
    case 'jsx':
      return 'javascript'
    case 'html':
    case 'htm':
      return 'html'
    case 'css':
      return 'css'
    case 'sh':
    case 'bash':
      return 'shell'
    case 'sql':
      return 'sql'
    case 'toml':
      return 'ini'
    case 'conf':
    case 'cfg':
    case 'ini':
      return 'ini'
    default:
      if (base === 'dockerfile') return 'dockerfile'
      if (base.startsWith('application') && (base.endsWith('.yml') || base.endsWith('.yaml'))) {
        return 'yaml'
      }
      return 'plaintext'
  }
}
