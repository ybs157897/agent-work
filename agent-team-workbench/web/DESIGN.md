---
version: 2.0.0
name: agent-team-workbench-web
description: >
  A dense multi-agent control plane with the production Agent LanguageGUI
  reading language: neutral surfaces, clear sans-serif typography, compact
  controls, and a restrained blue action color.
theme:
  source: "production .chat-languagegui-skin palette"
  store: "src/stores/workbench-theme.store.ts"
  persistence-key: "chat:theme"
  modes: "light | dark"
colors:
  brand:
    primary: "var(--color-brand-primary)"
    accent: "var(--color-brand-accent)"
    muted: "var(--color-brand-muted)"
  surface:
    base: "var(--color-surface-base)"
    warm: "var(--color-surface-warm)"
    sunken: "var(--color-surface-sunken)"
    raised: "var(--color-surface-raised)"
    glass: "var(--color-surface-glass)"
  sidebar:
    base: "var(--color-sidebar)"
    hover: "var(--color-sidebar-hover)"
    border: "var(--color-sidebar-border)"
  text:
    primary: "var(--color-text-primary)"
    secondary: "var(--color-text-secondary)"
    tertiary: "var(--color-text-tertiary)"
    inverse: "var(--color-text-inverse)"
    on-sidebar: "var(--color-text-on-sidebar)"
    on-sidebar-active: "var(--color-text-on-sidebar-active)"
  border:
    subtle: "var(--color-border-subtle)"
    strong: "var(--color-border-strong)"
  status:
    success: "var(--color-status-success)"
    warning: "var(--color-status-warning)"
    error: "var(--color-status-error)"
    info: "var(--color-status-info)"
    standby: "var(--color-status-standby)"
  identity: "identity-1..8 only for avatars and ownership marks"
typography:
  family: "chironHeiHK, PingFang SC, Hiragino Sans GB, Microsoft YaHei, sans-serif"
  mono: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"
  display: "42px / 1.05 / 700"
  h1: "29px / 1.15 / 700"
  h2: "24px / 1.25 / 600"
  h3: "20px / 1.3 / 600"
  body-lg: "17px / 1.5 / 400"
  body: "14px / 1.55 / 400"
  caption: "12px / 1.4 / 400"
spacing:
  scale: "4px, 8px, 12px, 16px, 24px, 32px, 64px, 96px, 128px"
rounded:
  control: "6px"
  card: "8px"
  container: "12px"
  code-panel: "16px"
  pill: "9999px"
shadows:
  level-1: "card surface elevation"
  level-2: "interactive card elevation"
  level-3: "popover and dialog elevation"
  level-4: "top-level dialog elevation"
components:
  panel: "workbench-panel = semantic border + raised surface + level-1 shadow"
  button: "semantic variant, 32px minimum height, focus-visible brand ring"
  input: "control radius, strong border, raised surface, focus ring"
  status: "status color plus text and icon; never decorative"
  chat: "chat-languagegui-skin owns reading geometry; AgentOutput owns content rendering"
layout:
  shell: "fixed viewport, persistent 240px sidebar, content fills remaining width"
  narrow-shell: "208px sidebar at <=1023px and 160px at <=639px; labels remain rendered"
  page-shell: "1440px maximum content width with 48/40/32px horizontal margins"
  chat-reading: "920px centered transcript and composer reading rail"
  config-split: "256px configuration rail plus a scrollable main pane"
---

# DESIGN.md — agent-team-workbench frontend design source

## Overview

This is a multi-agent control plane. Its visual language follows the production
Agent正文: a quiet neutral canvas, readable sans-serif text, compact controls,
raised white or dark surfaces, and one restrained blue action color. The shell,
dashboard, configuration pages, task board, knowledge reader, and Chat share the
same semantic variables in both modes.

The active palette is defined in `src/index.css`. `tailwind.config.js` exposes
those variables as semantic utilities. This document describes the contract;
the effective values are the CSS variables and Tailwind mappings. A palette
must not be duplicated inside a page or component.

## Theme ownership

`src/stores/workbench-theme.store.ts` is the only appearance preference store.
It exports:

- `WorkbenchTheme`, the `'light' | 'dark'` type;
- `useWorkbenchThemeStore`, with `theme`, `setTheme(theme)`, and
  `toggleTheme()`;
- `WORKBENCH_THEME_KEY`, which remains `chat:theme` for continuity with the
  existing Chat preference.

The store reads only when `window` exists and catches both storage reads and
writes. An unavailable or invalid value resolves to `light`; the current
in-memory setting still works when persistence is blocked. `LayoutShell`
mounts `.workbench-theme[data-theme]` and synchronizes
`html[data-workbench-theme]`. The document attribute is required because
Modal and Drawer are portaled to `body`; a portaled surface must resolve the
same semantic variables as its originating page.

Chat consumes the same store. It may keep the `chat-languagegui-skin` class for
transcript geometry and the `data-theme` attribute for inspection, but it must
not own a second palette or a second local-storage read/write path.

## Colors

The light mode uses the production LanguageGUI palette:

- brand primary `217 91% 57%`, brand accent `217 76% 44%`, brand muted
  `217 86% 94%`;
- base `220 50% 97%`, sunken `220 42% 94%`, raised `0 0% 100%`, and sidebar
  `0 0% 100%`;
- primary text `222 48% 17%`, secondary text `218 25% 38%`, tertiary text
  `218 20% 46%`;
- subtle border `220 38% 91%` and strong border `220 34% 79%`.

Dark mode keeps the same hue relationships while changing luminance:

- brand primary `214 94% 68%`, brand accent `214 90% 61%`, brand muted
  `217 55% 21%`;
- base `222 28% 10%`, sunken `220 22% 16%`, raised `221 24% 13%`, and sidebar
  `221 24% 13%`;
- primary text `214 34% 92%`, secondary text `216 18% 73%`, tertiary text
  `216 15% 63%`;
- subtle border `220 18% 24%` and strong border `220 18% 34%`.

Success, warning, error, info, and standby only communicate actual state.
Identity colors are limited to avatar or ownership marks. No state color may
be used as a general accent, and no component may introduce a local palette.
Task, Agent, and Model pages inherit the global mode; `.plane-board` provides
board geometry only and does not overwrite semantic color channels.

Use `hsl(var(--color-...))` in CSS or the matching Tailwind semantic utility in
TS/TSX. Never add hexadecimal, `rgb()`, or standalone `hsl()` values to TS/TSX.

## Typography

All product text uses the sans-serif `font-zh` stack. Headings use the same
stack with size and spacing changes; no script or decorative font is used for
branding, headings, avatars, or long-form content. Body text stays at least
12px, with 14px as the default control and paragraph size. Long Agent output
uses 15–16px text and approximately 1.7 line height for reading comfort.

Use `font-mono` for code, IDs, timestamps, and tabular numeric data. Emphasis
comes from hierarchy, spacing, and a modest weight change before color.

## Layout and navigation

The shell is a fixed-height flex row. The primary sidebar is always rendered,
always expanded, independently scrollable, and contains both icon and label
for every navigation item. It has no collapse state, hover expansion, hamburger
button, full-screen mobile replacement, or automatic hiding. At narrow widths
the sidebar becomes compact, but labels remain in the DOM and visible area;
the content pane uses the remaining width and may stack its own controls.

The sidebar brand is a simple product mark and text label. Active navigation is
shown with a slim brand-color indicator, active text, and an icon. The
workspace selector and signed-in user stay in the sidebar so Chat does not lose
workspace context when its page header is absent.

`page-shell` gives standard pages a bounded reading width and consistent
vertical rhythm. Full-height pages (`/chat`, `/task-chat`, `/agents`, and
`/models`) own their internal scroll containers. `route-layout.ts` only decides
scroll and full-height boundaries; it does not select a visual skin.

## Chat and Agent output

`chat-languagegui-skin` applies the production Chat reading geometry. The
transcript and composer share a 920px rail, the user message is a compact
right-aligned surface, and Agent output is left-aligned, directly readable
Markdown. Chat and Task reuse `AgentOutput`; pages add only domain controls.

The run timeline remains chronological:

`thinking → assistant/activity → thinking → assistant/activity → final`.

Thinking, tool activity, approvals, and changes use compact semantic summary
rows. The final Agent answer stays outside the collapsed work timeline and is
never absorbed into a status card. Code, tables, KaTeX, Mermaid, content
blocks, citations, and safe links keep their existing rendering contracts.

Adjacent Markdown blocks retain paragraph spacing; paragraph resets must not
override the reading surface's vertical rhythm. A standalone, unfenced JSON
document that explicitly declares `languagegui/v1` is recovered through the
same validated LanguageGUI renderer, in its original position. Ordinary JSON,
unknown versions, and code examples keep their literal meaning. Recognized
incomplete output is buffered while streaming; invalid final output remains
available in a code panel. This is a display projection: stored messages and
copied source text remain unchanged, and canonical blocks retain precedence.

The global light/dark mode also controls syntax and Mermaid contrast. Streaming
uses the established throttled Markdown cadence and a single caret; reduced
motion removes rotation and shimmer while preserving state text.

## Pages and panels

Cards and panels use `workbench-panel` or the shared `ui/` components. A panel
has one semantic border and one shadow level. Interactive panels may rise to
level 2 on hover; popovers and dialogs use levels 3–4. Avoid stacking a heavy
shadow on an already strongly bordered surface.

The task board keeps its dense list and kanban geometry, status lanes, review
projection, and side-peek behavior. Agent and Model configuration pages keep
their split layout and independent scroll pane. Dashboard and Settings use
the Aceternity Bento primitive directly with semantic tokens; Aceternity is a
layout and motion primitive, not a second visual system.

Modal and Drawer are portaled to `body`, lock background scroll, trap focus,
restore focus, and expose an accessible name. A nested Modal sits above a
Drawer; only the top layer remains interactive and lower layers are inert and
`aria-hidden`. Task dialogs retain the same global mode through the document
theme attribute.

## Knowledge reader

`/knowledge` is read-only. It uses type navigation, compact result lists, a
wide Agent正文 reading surface, real Markdown headings as the table of
contents, navigable sources and relations, version links, and a handoff into
the shared Chat. There is no knowledge-specific palette, ingest form, or
second editor surface. The full interaction and data contract lives in
`docs/product/knowledge-reading.md`.

## Agent knowledge canvas

The Product Agent workspace places its knowledge reader in the center and the
existing Chat on the right. Document and graph views share the same knowledge
items, source links, versions, and `AgentOutput` renderer. React Flow is a
spatial reading layer, loaded only for the graph view; its surfaces, controls,
nodes, and edges resolve the global semantic tokens in both modes.

The primary workbench sidebar stays visible. The inner Agent/conversation rail
can be opened from the workspace toolbar. When the available content width is
too small for both document and Chat, explicit view buttons switch between
them without unmounting the active conversation. A selected excerpt is shown
as a removable, versioned reference above the existing composer; selecting it
does not send a message. Reading position and layout preferences are scoped
to the workspace and stable Agent ID.

The HTML interaction proposal is not a palette source. Paper colors, ink
ornaments, calligraphic type, and vermilion accents from early prototypes must
not be reintroduced. See `docs/product/product-agent-canvas.md` for the
ownership, permission, failure-recovery, and acceptance contract.

## Interaction and accessibility

- Every interactive control has default, hover, pressed or active, disabled,
  and `focus-visible` states. Focus uses the semantic brand ring with a visible
  offset.
- State is expressed with icon and text, never color alone. Error regions use
  `role="alert"`; live status uses `role="status"` and an appropriate label.
- The shell begins with a skip link targeting `#main-content`. Main content is
  keyboard focusable with `tabindex="-1"`.
- Empty states explain why the area is empty and provide the next action.
- Skeletons appear after 300ms for normal requests and match the target layout.
- Dialogs keep their lock through exit animations and honor Escape and reduced
  motion. Content panes retain independent scrolling at low viewport heights.

## Responsive behavior

Desktop is the primary support target. Standard pages reduce horizontal
margins and grid columns at 1024px and 640px. The primary sidebar remains
visible at every supported width using its compact widths. Chat's inner Agent
and conversation rail may stack its controls at narrow widths so the main
reading surface retains usable space; it must not hide the primary shell nav.
Code and table blocks scroll horizontally rather than forcing tiny text.

## Forbidden presentation patterns

The product has one unified LanguageGUI skin. Do not reintroduce decorative
texture images, landscape layers, script branding, stamp-like marks, neon or
strong 3D defaults, or a route-level override that forks the palette. Keep
functional CSS for Markdown, code, tools, status, and accessibility. Visual
changes belong in semantic tokens and this document, with a focused render
test for any changed interaction boundary.

## Verification

For visual or layout changes run the focused Vitest files for the changed
component and `pnpm tsc -b`; run `pnpm lint` when the changed files are covered
by the frontend gate. The token test must pass. Browser checks must visit both
theme modes, a narrow viewport, a portal dialog, and the fixed sidebar. Record
whether the check exercised a real server path or only a static render.
