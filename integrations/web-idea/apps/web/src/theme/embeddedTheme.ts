export type EmbeddedTheme = 'light' | 'dark'

type ThemeMessage = {
	type: 'atw-code-theme'
	theme: EmbeddedTheme
	tokens?: Record<string, string>
}

const semanticAliases: Record<string, string> = {
	'--color-surface-base': '--ij-bg',
	'--color-surface-raised': '--ij-surface',
	'--color-surface-sunken': '--ij-tool',
	'--color-sidebar': '--ij-tool',
	'--color-sidebar-hover': '--ij-hover',
	'--color-sidebar-border': '--ij-border',
	'--color-border-subtle': '--ij-border-soft',
	'--color-border-strong': '--ij-border',
	'--color-text-primary': '--ij-text',
	'--color-text-secondary': '--ij-text-muted',
	'--color-text-tertiary': '--ij-text-muted',
	'--color-brand-primary': '--ij-focus',
	'--color-status-error': '--ij-danger',
}

function isThemeMessage(value: unknown): value is ThemeMessage {
	if (typeof value !== 'object' || value === null) return false
	const message = value as { type?: unknown; theme?: unknown; tokens?: unknown }
	if (message.type !== 'atw-code-theme' || (message.theme !== 'light' && message.theme !== 'dark')) return false
	if (message.tokens === undefined) return true
	if (typeof message.tokens !== 'object' || message.tokens === null || Array.isArray(message.tokens)) return false
	return Object.entries(message.tokens).every(([key, token]) =>
		/^--[a-zA-Z0-9_-]{1,96}$/.test(key) && typeof token === 'string' && token.length <= 256 && !/[;{}<>]/.test(token),
	)
}

function cssColor(value: string): string {
	const trimmed = value.trim()
	// The host design system sends HSL channels (for use with hsl(var(...))).
	if (/^(?:\d+(?:\.\d+)?%?\s+){2}\d+(?:\.\d+)?%?(?:\s*\/\s*[\d.]+%?)?$/.test(trimmed)) {
		return `hsl(${trimmed})`
	}
	return trimmed
}

function applyTheme(message: ThemeMessage): void {
	const root = document.documentElement
	root.dataset.theme = message.theme
	root.style.colorScheme = message.theme
	for (const [key, value] of Object.entries(message.tokens ?? {})) {
		root.style.setProperty(key, value.trim())
		const alias = semanticAliases[key]
		if (alias) root.style.setProperty(alias, cssColor(value))
	}
	window.dispatchEvent(new CustomEvent<EmbeddedTheme>('atw-code-theme', { detail: message.theme }))
}

/** Accept only same-origin messages sent by the embedding workbench window. */
export function installEmbeddedThemeBridge(): () => void {
	if (window.parent === window) return () => {}
	const parentWindow = window.parent
	const parentOrigin = window.location.origin
	const onMessage = (event: MessageEvent<unknown>) => {
		if (event.origin !== parentOrigin || event.source !== parentWindow || !isThemeMessage(event.data)) return
		applyTheme(event.data)
	}
	window.addEventListener('message', onMessage)
	return () => window.removeEventListener('message', onMessage)
}
