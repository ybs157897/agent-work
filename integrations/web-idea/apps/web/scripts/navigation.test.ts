import assert from 'node:assert/strict'
import test from 'node:test'
import { fetchEmbeddedBootstrap, embeddedBootstrapUrl } from '../src/api/bootstrap.ts'
import { GatewayClient } from '../src/api/client.ts'
import { NavHistory, placeFromClick } from '../src/lsp/navHistory.ts'
import { javaLspSettings } from '../src/lsp/settings.ts'

test('return from a symbol jump restores the exact source caret, not the next column', () => {
  const history = new NavHistory()
  const caller = placeFromClick('Application.java', 5, 33)
  const target = placeFromClick('GreetingService.java', 4, 18)
  history.recordLeave(caller)
  assert.deepEqual(history.goBack(target), {
    path: 'Application.java', reveal: { line: 6, column: 34, endLine: 6, endColumn: 34 },
  })
  assert.deepEqual(history.goForward(caller), target)
})

test('long reading sessions retain at most 100 navigation positions', () => {
  const history = new NavHistory()
  for (let i = 0; i < 130; i++) history.recordLeave(placeFromClick(`File${i}.java`, i, 0))
  const visited: string[] = []
  while (history.canBack()) visited.push(history.goBack(null)!.path)
  assert.equal(visited.length, 100)
  assert.equal(visited[0], 'File129.java')
  assert.equal(visited.at(-1), 'File30.java')
})

test('new navigation invalidates forward entries even when the leave position is repeated', () => {
  const history = new NavHistory()
  const caller = placeFromClick('A.java', 1, 1)
  history.recordLeave(caller)
  history.recordLeave(placeFromClick('B.java', 2, 2))
  history.goBack(placeFromClick('C.java', 3, 3))
  assert.equal(history.canForward(), true)
  history.recordLeave(caller)
  assert.equal(history.canForward(), false)
})

import { registerJavaLspCompletion } from '../src/lsp/monacoCompletion.ts'

function completionHarness() {
  let provider: any
  let path = 'src/Example.java'
  const model = {
    uri: { path: '/1', scheme: 'inmemory' },
    getValue: () => 'service.',
    getVersionId: () => 1,
    isDisposed: () => false,
    getWordUntilPosition: () => ({ startColumn: 2, endColumn: 2 }),
  }
  const session = { didChange: async () => {}, completion: async () => [{ label: 'greet', kind: 2 }] }
  const monaco = { languages: {
    CompletionItemKind: { Method: 1 }, CompletionTriggerKind: { TriggerCharacter: 1 },
    registerCompletionItemProvider: (_: string, value: any) => { provider = value; return { dispose() {} } },
  } }
  registerJavaLspCompletion(monaco as any, () => session as any, () => path, () => model as any)
  return { model, session, setPath: (value: string) => { path = value },
    complete: () => provider.provideCompletionItems(model, { lineNumber: 2, column: 2 }, { triggerKind: 1, triggerCharacter: '.' }, { isCancellationRequested: false }) }
}

test('Java completion accepts the active reused inmemory model', async () => {
  const harness = completionHarness()
  assert.equal((await harness.complete()).suggestions[0].label, 'greet')
})

test('completion arriving after a file switch is discarded', async () => {
  const harness = completionHarness()
  let finish!: (items: Array<{ label: string; kind: number }>) => void
  harness.session.completion = () => new Promise((resolve) => { finish = resolve })
  const request = harness.complete()
  await Promise.resolve()
  harness.setPath('src/Other.java')
  finish([{ label: 'wrongFile', kind: 2 }])
  assert.deepEqual((await request).suggestions, [])
})


test('member completion synchronizes the current buffer before querying jdtls', async () => {
  const harness = completionHarness()
  const calls: string[] = []
  harness.session.didChange = async () => { calls.push('didChange') }
  harness.session.completion = async () => { calls.push('completion'); return [] }
  await harness.complete()
  assert.deepEqual(calls, ['didChange', 'completion'])
})

test('embedded bootstrap resolves the sibling endpoint and keeps the gateway same-origin', async () => {
  const href = 'https://workbench.test/api/v1/code-workspaces/cw_1/view/'
  assert.equal(embeddedBootstrapUrl(href), 'https://workbench.test/api/v1/code-workspaces/cw_1/bootstrap')
  let requestURL = ''
  let credentials = ''
  const bootstrap = await fetchEmbeddedBootstrap(href, async (input, init) => {
    requestURL = String(input)
    credentials = String(init?.credentials)
    return new Response(JSON.stringify({
      gateway_base_url: '/api/v1/code-workspaces/cw_1/gateway',
      workspace: { id: 'upstream-1', status: 'browsing', lsp_enabled: true },
      read_only: true,
      embedded: true,
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  })
  assert.equal(requestURL, 'https://workbench.test/api/v1/code-workspaces/cw_1/bootstrap')
  assert.equal(credentials, 'same-origin')
  assert.equal(bootstrap.gateway_base_url, 'https://workbench.test/api/v1/code-workspaces/cw_1/gateway')
  assert.equal(bootstrap.read_only, true)
})

test('GatewayClient omits Authorization for an embedded same-origin session', async () => {
  const originalFetch = globalThis.fetch
  let authorization: string | null = null
  globalThis.fetch = async (_input, init) => {
    authorization = new Headers(init?.headers).get('Authorization')
    return new Response(JSON.stringify({ build: 'none', jdk: {} }), { status: 200 })
  }
  try {
    await new GatewayClient('/api/v1/code-workspaces/cw_1/gateway', '').project('upstream-1')
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.equal(authorization, null)
})

test('read-only Java settings keep project metadata and autobuild out of the checkout', () => {
  const readOnly = javaLspSettings(true)
  assert.equal(readOnly.java.autobuild.enabled, false)
  assert.equal(readOnly.java.import.generatesMetadataFilesAtProjectRoot, false)
  assert.equal(readOnly.java.configuration.updateBuildConfiguration, 'disabled')

  const editable = javaLspSettings(false)
  assert.equal(editable.java.autobuild.enabled, true)
  assert.equal(editable.java.import.generatesMetadataFilesAtProjectRoot, true)
  assert.equal(editable.java.configuration.updateBuildConfiguration, 'automatic')
})
