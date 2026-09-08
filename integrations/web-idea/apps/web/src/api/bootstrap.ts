import type { Workspace } from './client'

export type EmbeddedBootstrap = {
  gateway_base_url: string
  workspace: Workspace
  read_only: true
  embedded: true
}

/** Resolve the host's sibling bootstrap endpoint from the iframe view URL. */
export function embeddedBootstrapUrl(href: string): string {
  return new URL('../bootstrap', href).toString()
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function parseBootstrap(value: unknown, currentURL: URL): EmbeddedBootstrap {
  if (!isRecord(value) || value.read_only !== true || value.embedded !== true) {
    throw new Error('embedded code workspace bootstrap is invalid')
  }
  if (typeof value.gateway_base_url !== 'string' || value.gateway_base_url.trim() === '') {
    throw new Error('embedded code workspace gateway is missing')
  }
  const gatewayURL = new URL(value.gateway_base_url, currentURL)
  if (gatewayURL.origin !== currentURL.origin || gatewayURL.username || gatewayURL.password || gatewayURL.search || gatewayURL.hash) {
    throw new Error('embedded code workspace gateway must be same-origin')
  }
  if (!isRecord(value.workspace) || typeof value.workspace.id !== 'string' || value.workspace.id === '') {
    throw new Error('embedded code workspace is missing')
  }
  return {
    gateway_base_url: gatewayURL.toString().replace(/\/$/, ''),
    workspace: value.workspace as Workspace,
    read_only: true,
    embedded: true,
  }
}

export async function fetchEmbeddedBootstrap(
  href: string = window.location.href,
  fetcher: typeof fetch = fetch,
): Promise<EmbeddedBootstrap> {
  const currentURL = new URL(href)
  const response = await fetcher(embeddedBootstrapUrl(href), {
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
  })
  if (!response.ok) {
    throw new Error(`embedded code workspace bootstrap failed (${response.status})`)
  }
  let body: unknown
  try {
    body = await response.json()
  } catch {
    throw new Error('embedded code workspace bootstrap was not JSON')
  }
  return parseBootstrap(body, currentURL)
}
