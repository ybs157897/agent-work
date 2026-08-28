# Design QA — LanguageGUI Work Timeline

## Evidence

- Source visual truth:
  - `design-qa-assets/reference-codex-file-changes-card.png` — Codex final-answer file-change card with total files, per-file additions/deletions, undo and review controls, 1552×1012 px.
  - `design-qa-assets/reference-codex-diff-review-sidebar.png` — Codex right-side file review surface with a real per-file diff, 2376×3008 px.
  - `design-qa-assets/reference-file-change-summary.png` — selected compact change summary reference (`N 个文件已更改 +A −D`), 442×92 px.
  - `design-qa-assets/reference-zcode-live-output-loading.png` — latest user reference for paragraph output followed by a small gray loading spinner, 3112×1406 px.
  - `design-qa-assets/reference-zcode-segment-disclosures.png` — user-confirmed running hierarchy: thinking, interim Markdown and Run shell, 2662×2784 px.
  - `design-qa-assets/reference-zcode-success.png` — ZCode completed state, 3420×2958 px.
  - `design-qa-assets/reference-zcode-running.png` — ZCode expanded work state, 2506×2210 px.
- Browser-rendered implementation:
  - `design-qa-assets/2026-08-29-run-changes-card.png` — isolated four-file terminal Run showing `已编辑 4 个文件`, aggregate `+7 −3`, per-file stats and the disclosure for the fourth file, 1280×720 px.
  - `design-qa-assets/2026-08-29-run-changes-review-drawer.png` — the same Run with the right-side review drawer open and a real snapshot-derived unified diff selected, 1280×720 px.
  - `design-qa-assets/2026-08-29-write-change-summary.png` — target conversation with the Write phase rendered as `1 个文件已更改 · 24.8 KB` inside the existing 44 px activity row, 1360×1482 px.
  - `design-qa-assets/2026-08-29-full-final-and-reasoning-duration.png` — target 615-event conversation with the former 52-second reasoning gap corrected to `<1 秒` and Final Answer fully expanded, 1360×1482 px.
  - `design-qa-assets/2026-08-28-zcode-compact-tools-expanded.png` — real interrupted Run showing compact 44 px tool/thinking rows, ordered interim Markdown and one expanded six-call list, 1360×1482 px.
  - `design-qa-assets/work-timeline-corrected-expanded.jpg` — corrected multi-stage Run with collapsed thinking/tool rows and independent interim Markdown, 1500×1482 px.
  - `design-qa-assets/work-timeline-corrected-collapsed.jpg` — corrected terminal state: whole work process collapsed, final answer visible, 1500×1482 px.
  - `design-qa-assets/work-timeline-corrected-dark.jpg` — corrected disclosure hierarchy in dark theme, 1500×1482 px.
  - `design-qa-assets/work-timeline-corrected-narrow.jpg` — corrected hierarchy at 900×720 CSS px.
  - `design-qa-assets/work-timeline-corrected-compact.jpg` — corrected hierarchy at 720×720 CSS px.
  - `design-qa-assets/work-timeline-success.jpg` — completed Run collapsed above the final answer, 1280×720 px.
  - `design-qa-assets/work-timeline-failure.jpg` — failed Run expanded with reasoning, tool summary and error evidence, 1280×720 px.
  - `design-qa-assets/work-timeline-dark.jpg` — dark-theme failure state, 1280×720 px.
  - `design-qa-assets/work-timeline-narrow.jpg` — 900×720 CSS viewport, no horizontal overflow.
  - `design-qa-assets/work-timeline-compact.jpg` — 720×720 CSS viewport, no horizontal overflow.
- Combined full-view comparisons:
  - `design-qa-assets/2026-08-29-run-changes-card-comparison.png`
  - `design-qa-assets/2026-08-29-run-changes-review-comparison.png`
  - `design-qa-assets/2026-08-28-zcode-compact-tools-comparison.png`
  - `design-qa-assets/comparison-corrected-full.jpg`
  - `design-qa-assets/comparison-success.jpg`
  - `design-qa-assets/comparison-expanded.jpg`
- Combined focused comparisons:
  - `design-qa-assets/2026-08-29-write-change-summary-comparison.png` — source summary and the matching Workbench row crop in one 920×372 comparison.
  - `design-qa-assets/comparison-corrected-focused.jpg`
  - `design-qa-assets/comparison-success-focused.jpg`
  - `design-qa-assets/comparison-expanded-focused.jpg`
- Local route for the isolated file-change fixture: `http://127.0.0.1:5185/chat?agent=agent_01M14M4ZWKTSAP89WVZD7K35SY&c=wi_01M14M4ZWMTTDYY2VJEC2D3G62`

The corrected default capture used a 1500×1482 CSS viewport at devicePixelRatio 2; the Browser capture is normalized to 1500×1482 output pixels. Corrected responsive captures used 900×720 and 720×720 CSS/output pixel sizes. Source screenshots are product captures with unknown CSS viewport/density, so they were normalized by height only in the combined comparison files; layout and interaction hierarchy, not absolute pixel scale, is the fidelity target.

## Required Fidelity Surfaces

- Fonts and typography: passed. The implementation preserves the product's existing system sans/mono stack. Status copy, phase labels, tool summaries and final Markdown retain a readable hierarchy at desktop, 900 px and 720 px widths. No clipping or forced line breaks were introduced.
- Spacing and layout rhythm: passed. The Run summary is a 44 px semantic row; successful work collapses to one line directly above the final answer. Expanded reasoning, interim Markdown, tool batches and errors follow one vertical event order. LanguageGUI's existing 8 px rhythm, 12 px radius and centered reading rail are preserved.
- Colors and visual tokens: passed. All new surfaces, borders, text and status tones use repository semantic tokens. Light and dark captures preserve contrast; failure and interruption use icon plus text, not color alone.
- Image quality and asset fidelity: passed. The target contains no raster product imagery requiring generation. Existing product iconography is reused; no emoji, CSS illustration, placeholder art or custom inline SVG was introduced.
- Copy and content: passed. Visible labels are localized and state-specific: `工作中`, `已工作`, `运行失败`, `已中断`, `思考 · 持续了 X`, and compact tool summaries. The final assistant Markdown remains outside the work timeline.
- Interaction and accessibility: passed. Work and tool summaries are semantic buttons with `aria-expanded`; Run status changes use a short stable `role=status` announcement; the large streaming body is not a live region. Keyboard focus is visible, reduced-motion keeps state changes without animation dependency, and the 900/720 px captures have `scrollWidth === innerWidth`.

## Comparison History

### Iteration 8 — final-answer file changes and review passed

- The implementation follows ZCode's capture model instead of deriving chat history from the mutable Git worktree: every supported Write/Edit records bounded before/after text snapshots, repeated writes fold to the first before and last after, and a line-level LCS produces the aggregate and per-file `+A/−D` values.
- The terminal assistant turn now ends with one compact `已编辑 N 个文件` card. It shows total additions/deletions, the first three paths, a disclosure for the remaining files, and explicit `撤销` / `审核` actions without changing the established LanguageGUI reading rail.
- `审核` opens a 760 px right-side drawer with a file rail and snapshot-derived unified diff. Switching files updates the diff in place; empty/unavailable text diff has a visible fallback.
- `撤销` first validates every current file against the recorded final snapshot. Any external change rejects the entire command before a write; a successful command restores prior contents, removes files created by the Run, persists a replayable `file_changes.reverted` event and disables the control as `已撤销`.
- Browser acceptance used a real isolated API/SQLite fixture with four files and `+7 −3`. It verified the collapsed and expanded file list, right-side review, file switching, confirmation dialog, ready/reverted states and zero console errors.
- Visual comparison: passed. The source's hierarchy, neutral card, dense file rows, green/red signed counts and side-by-side review grammar are preserved. Existing product tokens, iconography and drawer primitives remain authoritative rather than copying Codex chrome literally.
- Automated verification passed: 67 Vitest files / 564 tests, TypeScript project build, ESLint, production Vite build, Go build/vet and race-enabled focused backend tests.

### Iteration 7 — Write/Edit change summary passed

- The selected source uses one compact line: neutral file-count copy followed by green additions and red deletions. The implementation preserves that hierarchy and maps the colored values to existing success/error tokens without introducing a second card, badge, or interaction.
- The real historical Write event exposes one file and 24,832 written bytes but no old snapshot, diff, additions or deletions. Its honest matching state is therefore `1 个文件已更改 · 24.8 KB`; line counts are deliberately omitted rather than shown as fabricated zeroes.
- When canonical `change_stats` or a verifiable unified diff supplies line facts, the same component renders `N 个文件已更改 +A −D`. Focused render tests verify the neutral/green/red spans; the real browser capture verifies the file/bytes fallback in the same 44 px row.
- Fonts and typography: passed. Existing system UI typography, tabular figures and 11 px compact-row scale are preserved.
- Spacing and layout rhythm: passed. No row-height or surrounding stage spacing changed; the summary remains one line with the existing overflow behavior.
- Colors and visual tokens: passed. Neutral copy uses secondary/tertiary text tokens; additions use status-success and deletions use status-error. `+`/`−` signs preserve meaning without color.
- Image quality and asset fidelity: passed. The target is UI text only and requires no raster or generated asset.
- Copy and content: passed with an intentional data constraint. The current Run shows bytes because those are the only reliable facts; raw `Wrote ... bytes` remains available only in the deeper trace detail.
- Interaction and accessibility: passed. The summary is static content inside the existing disclosure button; the button's accessible name includes the full change summary, status and duration.
- Fresh focused and full-suite verification passed: 66 Vitest files / 559 tests, TypeScript, ESLint, production Vite build, Go build/vet, gofmt and Kimi adapter race test.

### Iteration 6 — full Final Answer and reasoning-gap feedback passed

- User-requested Final Answer folding was removed completely. The target conversation rendered one 1,101-character final article through its last recommendation with zero `展开全文/收起全文` controls.
- Event evidence for `run_01M14GZP47M8XQ60M90CJX9R1P` showed the sixth reasoning phase streamed for 573ms, then produced no event for 52.396s before `tool.started`. The UI now records the final reasoning delta as `completedAt`, so the historical row reads `思考 · 持续了 <1 秒` instead of 52 seconds.
- A live reasoning phase that receives no delta for 700ms stops its streaming sweep and appends the small accessible output loader. A new delta restores streaming; running tools, pending approval and terminal Runs suppress the loader.
- Fresh-page browser acceptance found zero console errors, zero old `持续了 52 秒` rows, zero long-answer controls, the full final tail in the DOM, and zero terminal-state loading indicators.
- Automated verification passed: 66 Vitest files / 553 tests, TypeScript project build, ESLint and production Vite build.

### Iteration 5 — live density and paragraph streaming passed

- Browser measurements on a real 68-call Run found the first eight tool disclosures and all seven thinking disclosures were exactly 44 px high. No visible `工具执行` heading remains; the 14 px wrench icon, call count, current/latest summary, terminal state and chevron carry the compact row.
- Expanding a real six-call Execute group produced six ordered list rows with their individual durations and terminal states. Each later tool phase remains a separate disclosure because settled Markdown/reasoning is a hard grouping boundary.
- A live final draft is now projected as the ordinary outer assistant answer immediately, with a stable paragraph-block Markdown tree. Completed paragraphs stop reparsing while only the current safe block updates; open fences, math and callouts remain buffered.
- The output loader is a 15 px token-colored spinner with `role=status`, hidden while reasoning, a tool or approval is active and removed at every terminal state. The real interrupted Run showed zero stale loaders; render/state tests cover initial wait and post-paragraph wait.
- Unknown Run status is no longer enough to hide a received `tool.started`: an actually streaming thinking/answer, pending approval or running tool infers the temporary live presentation until the canonical status arrives.
- Browser comparison and console inspection passed; the source/implementation comparison is recorded above. Automated verification passed: 67 Vitest files / 552 tests, TypeScript project build, ESLint, production Vite build, Go build/vet, gofmt and the Kimi adapter race test.

### Iteration 4 — ZCode behavior alignment passed

- Real 1325-event conversation retained `11 段思考 · 82 次工具 · 9 段过程正文` and the external final answer.
- Reasoning ticker now uses the final non-empty source line, scrolls to its horizontal tail, exposes a two-sided mask, and keeps the collapsed body mounted for 300 ms before unmount. Browser timing check observed body counts `1 → 1 → 0` across expand, immediate collapse and 360 ms.
- ZCode grouping defaults split the real transcript into 14 semantic groups (Agent, Explore and Execute visible in the inspected sample) without changing the 82 underlying tool rows.
- The final Markdown measured `scrollHeight=10826`; its collapsed `clientHeight/max-height` was exactly 120 px. Expand restored full `clientHeight=10925`, and collapse returned to the height cap.
- Settings expose four accessible switches. `显示全部思考` defaults on; turning it off reduced the same Run from 11 visible reasoning rows to exactly 1, then the default was restored.
- Retryable failed Run `wi_01M0ZNR10TMJ5S1TP39DBZV4YD` rendered one composer-adjacent error Banner with detail/copy/Retry controls and zero transcript error rows; the Retry action was present but deliberately not invoked during read-only acceptance.
- The settings, reasoning, tool and final controls have accessible names and state; reduced-motion disables the new sweep/transition effects. Browser console contained zero error-level entries.
- Automated verification: 67 Vitest files / 535 tests, TypeScript build, ESLint and production Vite build passed; Kimi adapter race test plus Go build/vet passed.

### Iteration 3 — passed

- The corrected real conversation contains 19 independent thinking phases, 18 collapsed tool batches, two interim Markdown messages and one external final answer in strict event order.
- Every thinking phase now defaults to a 44 px collapsed disclosure row. Its one-line preview follows the horizontal tail while streaming; expanding reveals only that phase's complete reasoning.
- Every tool batch defaults to one collapsed row whose latest human-readable tool summary follows the horizontal tail. Expanding still reveals the existing ordered tool log and details.
- Interim assistant output renders as an independent `article[aria-label="过程正文"] > .chat-prose` sibling. It has no thinking border, background or label and uses the same Markdown typography as normal assistant output.
- On terminal success, the outer WorkTimeline collapses all thinking/interim/tool history into `已工作 X`; `article[aria-label="Atlas 的消息"]` remains visible as the single final answer.
- Browser interaction checks passed for thinking expand/collapse, tool expand/collapse, outer Run collapse, final-answer visibility, light/dark themes, 900 px and 720 px viewports. Page `scrollWidth === innerWidth` at both responsive widths; preview rows retained internal horizontal overflow for tail following.
- Browser console: zero error-level entries.
- Automated verification: TypeScript build, 65 Vitest files / 520 tests, ESLint and production Vite build all passed.

### Iteration 1 — blocked

- [P1] Terminal events depended on an asynchronous Run refetch, leaving a visible window where final text existed while the work header still said running.
- [P1] Missing Run status was interpreted as running, starting a timer and `aria-busy` state for historical/unknown data.
- [P2] The initial live-region scope included the entire streaming work body and could repeatedly announce long Markdown.
- [P2] Folded reasoning did not carry its first-delta timestamp, so long historical phases could not report a meaningful duration.

### Fixes

- `run.completed` and `run.failed` now patch the cached Run to `succeeded`/`failed` immediately and update the terminal timestamp before the asynchronous refresh.
- Unknown status now renders a static `工作过程` state; only the canonical active-state set is treated as running.
- Only a short, stable status string is announced. The ordered body is a normal list.
- Folded reasoning now attaches to its matching `tool.started` / `message.completed` boundary, retains `reasoning_folded_started_at` and phaseId, and no longer reappears as one late whole-Run block. Live and settled phases carry `phaseStartedAt`/`startedAt`.

### Iteration 2 — superseded

- Completed comparison shows the same key hierarchy as ZCode: one `已工作 X` summary, then the final answer outside it.
- Expanded comparison shows the same chronological grammar: phase label and reasoning, compact tool row, then failure evidence. The implementation intentionally keeps the LanguageGUI raised surface and semantic status colors instead of copying ZCode's plain white shell.
- Primary interactions tested in the browser: expand/collapse completed work, expand/collapse a nested tool batch, default-expanded failure evidence, final Markdown visibility, light/dark theme.
- Browser console: zero error-level entries. Existing React Router future-flag warnings are unrelated to this change.
- This iteration proved Run-level collapse but was superseded because it rendered thinking expanded and did not visually separate interim Markdown strongly enough.

## Open Questions

- The reference screenshot shows its sample thinking body expanded, while the user's explicit confirmed requirement says each thinking phase must default collapsed with a scrolling one-line preview. The implementation follows the explicit requirement; the reference is used for sibling hierarchy and typography rather than that default state.
- Exact `+A/−D` is available for new Kimi Write/Edit events whose text snapshots are captured. Historical Runs, shell-based edits, multi-file patches without one resolvable path, binary files and bounded-out large files intentionally remain unavailable rather than reporting fabricated counts.

## Follow-up Polish

- P3: ZCode uses a nearly borderless work log; LanguageGUI keeps a subtle raised card to remain consistent with the existing chat surface. This is an intentional product-system adaptation, not an actionable mismatch.

## Implementation Checklist

- [x] Running Run defaults expanded.
- [x] Successful Run defaults collapsed.
- [x] Failed/interrupted Run defaults expanded.
- [x] Tool batches default to a single collapsed command row.
- [x] Tool rows follow the latest human-readable summary while collapsed.
- [x] Every thinking phase defaults collapsed and follows the latest preview text.
- [x] Interim assistant output is an independent ordinary Markdown sibling, never part of a thinking disclosure.
- [x] Thinking, interim Markdown and tool batches preserve event order.
- [x] Long capped histories fold reasoning back to the matching phase boundary.
- [x] Final Markdown renders outside the work timeline.
- [x] Live final Markdown renders outside the work timeline paragraph by paragraph.
- [x] A small accessible loading icon follows the latest rendered paragraph while waiting.
- [x] Tool `started` appears immediately and expands to its ordered call list.
- [x] Tool and thinking disclosure rows share the same 44 px density.
- [x] Terminal status closes immediately without a stale running window.
- [x] Light, dark, 900 px and 720 px states verified.
- [x] No browser console errors.
- [x] Final answer can show a snapshot-derived edited-files card with aggregate and per-file line counts.
- [x] Review opens a right-side per-file unified-diff drawer.
- [x] Undo is guarded by whole-batch preflight, idempotency and a persisted reverted state.

final result: passed
