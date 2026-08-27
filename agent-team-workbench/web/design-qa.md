# Production Chat LanguageGUI QA

## Comparison target

- Source visual truth: `/tmp/languagegui-reference.zQuUb3/multi-prompt.png` and `/tmp/languagegui-reference.zQuUb3/figma-dashboard.png`.
- Rendered implementation: `/tmp/languagegui-reference.zQuUb3/chat-workflow-pass2.png`.
- Focused implementation crop: `/tmp/languagegui-reference.zQuUb3/chat-workflow-pass2-focus.png`.
- Combined comparison input: `/tmp/languagegui-reference.zQuUb3/chat-workflow-comparison-pass2.png`.
- Route: `http://localhost:5180/chat?agent=agent_01M0MS9FSV89X5M5ACJXVC05A7&c=wi_01M0T0W6JNQ4X4J2JMHK44XGQE`.
- State: stored successful run with a real four-step `run.plan_updated` snapshot; three completed steps and one active step.
- Browser viewport: 1500 × 1482 CSS px; `devicePixelRatio = 2`.
- Full screenshot: 1500 × 1482 px, normalized by the browser capture to CSS-pixel dimensions.
- Focus crop: 960 × 390 px. Source reference: 816 × 857 px. The combined comparison preserves each image's aspect ratio rather than stretching either component.

## Full-view comparison evidence

- The workflow remains on the same 920 px reading rail as transcript and composer, so the new surface does not create page-level horizontal overflow or detach the execution state from the input path.
- The LanguageGUI white card, cool canvas, thin border, soft shadow and blue/semantic status accents remain consistent with the accepted production Chat skin.
- Four Action cards fit above the composer; longer workflows scroll inside the bounded list rather than growing the persistent bottom region indefinitely.
- The implementation intentionally retains the Workbench shell, real conversation data and runtime status instead of copying the Figma demo shell.

## Focused comparison evidence

- The combined comparison input places the official multi-prompt reference and the rendered Workbench workflow in one view. Both use ordered, independent Action cards, compact badges, generous white content surfaces and a visible connector between steps.
- The Workbench projection is deliberately denser because it is a persistent execution summary rather than a workflow editor. It replaces Figma's model/settings/delete/add controls with real completed/current/pending status pills.
- The active Action is visible on initial render even when the list overflows. DOM order remains Action 1 → Action 4 and the current item carries `aria-current="step"`.

## Required fidelity surfaces

- Fonts and typography: passed. Kicker, badges and status use the existing 12 px caption token; action text uses the 14 px body token with natural wrapping. No role/model header was reintroduced.
- Spacing and layout rhythm: passed. Goal/proposal/actions share one 12 px-radius container; Action cards use the existing 8 px card radius and 8 px vertical rhythm; the centered connector follows the card stack.
- Colors and visual tokens: passed. All product styles use semantic surface, border, brand and status tokens. Completed/current/pending states also include icon and text, so color is not the only signal.
- Image quality and asset fidelity: passed. The production component is code-native UI and uses the installed Lucide icon set; no raster placeholder, handcrafted SVG, emoji or CSS illustration replaced a visible source asset.
- Copy and content: passed. Labels distinguish「当前目标」「方案草稿」「本轮执行计划」and use real stored step text. Unavailable editing commands are not shown as fake controls.
- Accessibility: passed. The workflow is a named section, execution order is an `ol`, the current step uses `aria-current`, goal progress is a named progressbar, proposal disclosure exposes `aria-expanded`/`aria-controls`, and every status has visible text.
- Responsiveness: passed for the repository's supported desktop surface. The workflow stays single-column and bounds long content; no new sub-1024 contract was introduced.

## Comparison history

### Pass 1

- P1: a real stored Plan did not render because the generic 500-event timeline cap discarded the latest `run.plan_updated` snapshot before Chat projection.
  - Fix: preserve the latest Plan and Goal state frames while still capping delta-heavy timeline tails at 500 entries.
  - Post-fix evidence: the real stored conversation renders one workflow with four Action cards.
- P2: the active fourth Action began below the bounded list viewport, so the most important current state was not initially visible.
  - Fix: increase the bounded list height and scroll the active step into the nearest visible position after state updates.
  - Post-fix evidence: Action 4 bounds are inside the list bounds (`1270.20–1338.99` vs `1050.80–1338.80`, sub-pixel edge only) and the screenshot visibly includes its「进行中」pill.

### Pass 2

- No remaining actionable P0, P1 or P2 visual findings.
- Accepted adaptations: read-only status controls instead of editor controls; compact persistent geometry instead of full-page workflow-builder cards; Workbench Chinese copy instead of reference lorem ipsum.

## Interaction and runtime evidence

- Real history load produced one `.chat-workflow` and four `.chat-workflow-action-card` elements.
- Visible text reports `3/4 已完成`, three completed steps and one current step.
- Legal empty Plan frames remove the old Plan snapshot; malformed non-empty frames retain the last valid snapshot.
- Goal clear events cannot revive an older `/goal` command.
- Browser console contains no errors. Only the two pre-existing React Router v7 future-flag warnings remain.
- Full frontend evidence: 57 test files / 425 tests passed, ESLint passed, TypeScript project build passed, and the production Vite build passed.

## Follow-up polish

- P3: when a real conversation containing both Goal and Plan-mode proposal becomes available, capture those live states in addition to the current static-render coverage.

## ContentBlock v1 comparison target

- Official source visuals:
  - `/tmp/languagegui-reference.zQuUb3/current-dashboard.png`
  - `/tmp/languagegui-reference.zQuUb3/chart-detail.png`
  - `/tmp/languagegui-reference.zQuUb3/top-5-gdp.png`
  - `/tmp/languagegui-reference.zQuUb3/widgets-overview.png`
  - `/tmp/languagegui-reference.zQuUb3/drag-drop-files.png`
- Rendered implementation captures:
  - `/tmp/languagegui-reference.zQuUb3/content-blocks-pass1-metric.png`
  - `/tmp/languagegui-reference.zQuUb3/content-blocks-pass1-mid.png`
  - `/tmp/languagegui-reference.zQuUb3/content-blocks-pass1-bottom.png`
  - `/tmp/languagegui-reference.zQuUb3/content-blocks-pass2-event.png`
- Combined comparison input: `/tmp/languagegui-reference.zQuUb3/content-blocks-comparison-pass2.png`.
- Route/state: `http://localhost:5180/languagegui`, seeded assistant output containing one `languagegui/v1` document with metric, bar chart, table, file and event blocks.
- Viewport: 1265 × 712 CSS px and normalized screenshot pixels; source images retain their original aspect ratio in the combined comparison.

### ContentBlock full-view and focused evidence

- MetricBlock preserves the official large-value hierarchy while generalizing it to an auto-fit metric grid; delta text uses explicit positive/warning wording in addition to semantic color.
- ChartBlock reproduces the official white plotting card, blue series, grid, axes, tooltip and source footer. It adds a keyboard-operable native details disclosure containing the exact data table.
- TableBlock uses a real table with column scopes, semantic alignment and bounded horizontal overflow. FileBlock and EventBlock reuse the same shell instead of introducing domain-specific dashboards.
- Event date/time copy was tightened after the first capture: same-day ranges show the date once, the end time once and a separate timezone badge.
- All five blocks use the shared 920 px reading surface and semantic tokens; unsafe URLs produce metadata-only cards rather than empty or executable actions.

### ContentBlock comparison history

- P1: importing Recharts eagerly increased the main application chunk from about 881 kB to 1.30 MB.
  - Fix: lazy-load ChartBlock and its chart runtime behind a stable loading surface.
  - Post-fix evidence: the final main chunk is about 908 kB after all navigation/theme/template work; Recharts remains isolated in a 350 kB chart chunk and loads only when a chart block is present.
- P2: the first EventBlock repeated the full date and timezone on both ends of a same-day range.
  - Fix: render the start date/time once, then only the end time, with timezone in its own badge.
- P2: canonical blocks and the same fenced JSON could render twice.
  - Fix: canonical blocks are authoritative and the matching closed LanguageGUI fence is removed from visible Markdown before rendering.
- No remaining actionable P0, P1 or P2 findings after the second pass.

### ContentBlock interaction and safety evidence

- Browser DOM reports all five types in order: `metric`, `chart`, `table`, `file`, `event`.
- Chart「查看数据表」changes the native details state from closed to open and exposes the table.
- Browser console has no errors; only the pre-existing React Router v7 future-flag warnings remain.
- Parser caps blocks, metrics, rows/columns, chart series/points and files; rejects unknown versions/types, non-finite chart values, executable URLs, DOM props and oversized JSON.
- Invalid fenced JSON falls back to the normal CodeBlock; streaming fences stay source text until settled; a bad block does not remove valid siblings or surrounding Markdown.
- Chat Run requests declare `output_contract=languagegui/v1`; backend tests prove the protocol is appended to the system prompt snapshot while user instruction remains unchanged.

## PromptBox comparison target

- Official source visuals:
  - `/tmp/languagegui-reference.zQuUb3/prompt-boxes.png`
  - `/tmp/languagegui-reference.zQuUb3/how-can-help.png`
  - `/tmp/languagegui-reference.zQuUb3/drag-drop-files.png`
- Rendered implementation:
  - `/tmp/languagegui-reference.zQuUb3/prompt-box-pass2-focus.png`
  - `/tmp/languagegui-reference.zQuUb3/prompt-box-attachment-pass1-focus.png`
  - `/tmp/languagegui-reference.zQuUb3/prompt-box-apps-pass2-focus.png`
- Combined comparison input: `/tmp/languagegui-reference.zQuUb3/prompt-box-comparison-pass1.png`.
- Route/state: production Chat with the real completed-run workflow above the composer; default, local attachment and Library/Apps-open states.
- Viewport: 1500 × 1482 CSS px and normalized screenshot pixels; focused crops preserve the 920 px composer width.

### PromptBox evidence and comparison history

- Default state matches the official expanded PromptBox hierarchy: rounded white shell, inset multiline input, compact attachment/image/mic/apps toolbar and a high-weight blue send action.
- Existing queue, usage, stop/send and Enter/Shift+Enter behavior remain connected to the real Chat store. Running runs change the send label to「加入队列」instead of hiding the queue behavior.
- Attachment state uses a real hidden file input plus keyboard-operable button, bounded file chips, explicit remove actions and an inline explanation. The composer remains fully visible with the extra rows.
- Library/Apps uses one lightweight popover, three useful prompt templates, a truthful「LanguageGUI v1 已启用」row and a neutral「外部 Apps 尚未配置」row.
- P2: the first Apps styling colored「尚未配置」like a success state.
  - Fix: default App status copy is tertiary; only the explicit enabled class uses success.
- P2: pending attachments could have survived a conversation switch if the same PromptBox instance remained mounted.
  - Fix: key PromptBox by conversation ID so local files and object URLs are disposed at the conversation boundary.
- Intentional capability boundary: local attachments disable send because no upload/user-content-part API exists. No file bytes, base64 or local paths enter Run JSON; voice is enabled only when native speech-to-text exists and writes transcript into the ordinary draft.
- No remaining actionable P0, P1 or P2 visual findings after the second pass.

### PromptBox interaction and safety evidence

- Prompt Library opens with `aria-expanded=true`, exposes all three templates and writes the selected prompt into the controlled textarea; Escape closes it and restores `aria-expanded=false`.
- Shift+Enter creates a second line without creating a Run.
- A generated non-sensitive `.txt` fixture appears as a 110 B pending chip; send becomes disabled and the Runtime limitation is visible. Removing it restores the normal state.
- A generated `.svg` fixture is rejected with「不支持此文件类型」and creates no attachment chip.
- File validation caps count, per-file bytes and total bytes; rejects empty, executable/HTML/SVG/unknown files and deduplicates stable file fingerprints.
- Microphone permission was not triggered during QA. Unsupported browsers render an explicitly disabled voice control; the supported path has error recovery and releases recognition on unmount.

## Extended content and navigation target

- Official source visuals:
  - `/tmp/languagegui-reference.zQuUb3/figma-dashboard.png` and `/tmp/languagegui-reference.zQuUb3/overview.png` for map/media/search composition.
  - `/tmp/languagegui-reference.zQuUb3/sidebars.png` and `/tmp/languagegui-reference.zQuUb3/screens.png` for Chats/Library/Apps navigation.
- Rendered implementation:
  - `/tmp/languagegui-reference.zQuUb3/content-blocks-extended-pass1.png`
  - `/tmp/languagegui-reference.zQuUb3/content-blocks-extended-pass2.png`
  - `/tmp/languagegui-reference.zQuUb3/chat-sidebar-chat-focus.png`
  - `/tmp/languagegui-reference.zQuUb3/chat-sidebar-library-focus.png`
  - `/tmp/languagegui-reference.zQuUb3/chat-sidebar-apps-focus.png`
- Combined sidebar comparison: `/tmp/languagegui-reference.zQuUb3/sidebar-comparison-pass1.png`.

### Extended content evidence

- `image` reuses a real repository asset with required alt text and caption; `audio` uses native controls, metadata preloading and no autoplay; both are bounded multi-item blocks.
- `map` shows a truthful location/coordinate card and only renders static imagery or an external action when a safe URL is present. It does not fabricate a map canvas or use arbitrary iframe/embed content.
- `search` renders ordered results with safe links, source labels and escaped plain-text snippets. HTML-like text remains text.
- Parser and output-contract prompt now cover image/audio/map/search in addition to metric/table/chart/file/event; unknown or invalid media siblings are dropped without hiding surrounding prose.

### Sidebar/navigation evidence and history

- Chats now has explicit Chats/Library/Apps navigation, local title search, conversation count, new-chat action and Pinned/History groups while retaining Agent ownership and real run status.
- Search narrowed 23 conversations to the one matching「协议验收」and restored the full set after clearing.
- Pin/unpin moved the selected conversation into/out of the Pinned region and was restored after QA. Pin IDs are scoped per Agent in local storage.
- Sidebar Library exposes the same three templates as PromptBox; selecting one opens a new unsent conversation, focuses the composer and preserves the user-editable prompt.
- Sidebar Apps reports only real state: LanguageGUI v1 enabled, external Apps unconfigured, and a link to Agent tool-policy configuration.
- No actionable P0/P1/P2 mismatch remained in the combined official/implementation comparison. The persistent outer Workbench rail and Agent selector are accepted product adaptations.

## Domain templates and dark theme target

- Official source visuals:
  - `/tmp/languagegui-reference.zQuUb3/current-dashboard.png`
  - `/tmp/languagegui-reference.zQuUb3/weather.png`
  - `/tmp/languagegui-reference.zQuUb3/chart-detail.png`
  - `/tmp/languagegui-reference.zQuUb3/overview.png`
- Rendered templates:
  - `/tmp/languagegui-reference.zQuUb3/domain-templates-pass1.png`
  - `/tmp/languagegui-reference.zQuUb3/domain-stock-pass3.png`
  - `/tmp/languagegui-reference.zQuUb3/domain-rating-pass1.png`
- Dark Chat: `/tmp/languagegui-reference.zQuUb3/chat-dark-pass1.png` at the same 1500 × 1482 viewport as the accepted light Chat.

### Domain template evidence and history

- Currency, weather, stock and score are composition helpers over generic metric/table/chart blocks; they do not create duplicate domain wire schemas. Rating is the only added interaction block because it requires real radio semantics.
- Currency renders two large-value cards with rates; weather combines current metrics and an hourly table; stock combines summary metrics with a lazily loaded line chart; score uses the same metric language for both teams.
- P2: the first stock chart used a zero-based Y axis, flattening the narrow price movement.
  - Fix: add `y_domain=auto` to generic charts and let the stock template opt in.
- P2: automatic numeric padding initially exposed floating-point precision in axis labels.
  - Fix: format chart ticks and accessible data-table values to two meaningful decimal places with prefix-aware currency units.
- The new RatingBlock exposes five radios, keeps one checked value, and states that feedback remains local. Browser interaction selected four stars with exactly one `aria-checked=true` radio.

### Dark theme evidence

- Dark mode is one scoped semantic-token mapping on `.chat-languagegui-skin[data-theme=dark]`; no component-level `dark:` color fork or global theme mutation was added.
- Primary/secondary/tertiary text, borders, surfaces, code tokens, status hues, chart series, Workflow, Sidebar and PromptBox remain readable and preserve hierarchy in the dark capture.
- Theme control changes its accessible label and icon, persists `dark` across reload, and was returned to the user's accepted light theme after QA.
- Bright brand surfaces use a dark inverse foreground in dark mode; body and tertiary text maintain high contrast against the dark base/raised surfaces.
- No actionable P0/P1/P2 findings remain.

## Developer code and review-summary target

- Official code source: `/tmp/languagegui-reference.zQuUb3/javascript-code.png`.
- User-provided review source: `/var/folders/gl/fpk3jftx5y50txjpk1k7zr280000gn/T/codex-clipboard-eb4d28b6-1c36-4cc7-b574-1e36bbf4bed4.png`.
- Rendered code capture: `/tmp/languagegui-code-preview.png`.
- Rendered review capture: `/tmp/languagegui-review-summary-preview.png`.
- Comparison input: the two source images and two rendered captures were inspected together at their original aspect ratios.
- Live route: `http://localhost:5180/languagegui`; production-build visual acceptance used the equivalent `http://127.0.0.1:5181/languagegui` preview to avoid development HMR affecting capture timing.

### CodeBlock evidence

- The toolbar follows the official hierarchy at Chat density: file path first, an independent language badge, visible「复制代码」action and a high-weight blue「导出」menu.
- Every visible source line has a non-selectable line number. Fence meta `{4,7-9}` highlights exactly those rows without changing copied/downloaded source.
- `filename=` and `title=` remain display metadata. Downloads take a sanitized basename only; unknown or invalid values fall back to a bounded language-derived name.
- The Export control exposes only two real actions: download file and copy Markdown. Browser acceptance proved `aria-expanded=false → true → false`, two visible menu items and Escape close/focus recovery.
- The code body preserves full-document highlight.js parsing, 13px/1.6 mono rhythm, horizontal overflow and the official pale selected-row treatment.

### ReviewSummaryBlock evidence

- The card turns review output into one scan path: verdict → visible statistics → severity-ordered findings → verification checks → next steps. It does not infer structure from ordinary Markdown headings.
- Verdict, severity and verification status use a fixed parser enum and visible text/icon in addition to color. The first high-severity finding opens by default; later findings remain compact and expand with native details semantics.
- File/line, evidence and suggestions remain plain bounded data. Only allow-listed URLs become links; model values cannot create CSS classes, HTML, styles or event handlers.
- Browser acceptance found one `review-summary` block, one enhanced CodeBlock and no console errors. The second finding changed from closed to open and exposed its detail text.
- The visible card matches the user's requested content hierarchy while intentionally using the accepted LanguageGUI card/surface system instead of reproducing the screenshot's red annotation rectangle.

## Tool activity target

- Official source visuals:
  - `/tmp/languagegui-reference.zQuUb3/multi-prompt.png` — 816 × 857 px.
  - `/tmp/languagegui-reference.zQuUb3/javascript-code.png` — 1620 × 1507 px.
- Rendered implementation:
  - `/tmp/languagegui-tool-demo-light.png` — 861 × 855 px.
  - `/tmp/languagegui-tool-chat-light.png` — 876 × 870 px.
  - `/tmp/languagegui-tool-chat-dark.png` — 876 × 870 px.
- The browser capture is normalized to CSS-pixel dimensions at the app's current desktop viewport. Source and implementation were inspected in one comparison input without stretching.
- States: Demo Bash/Read/Search/Edit/MCP fixture with Read selected; production two-action Search/Write group with Search selected; equivalent production dark theme.

### Tool activity comparison evidence

- The production Action cards preserve the official multi-prompt hierarchy: `Action N` and tool badges lead, the operation summary carries the main weight, and status/duration close each card. The horizontal rail is an intentional density adaptation for runs with many tool calls.
- The ActivityGroup adds a truthful group header that the Figma editor pattern does not need: call counts, aggregate status, and one collapse control. Brand blue is limited to focus/selection; success, failure, running and stopped keep semantic color plus visible text.
- Terminal, Read, Search, Diff and generic MCP IN/OUT bodies reuse the accepted code-panel treatment: light toolbar, raised body, bounded scrolling, line numbers where applicable and no fake execution controls.
- Truncated Search output only shows「显示 X / 共 N」when the protocol supplies a trustworthy total; otherwise it says「已截断 · 显示 X」and never presents the retained count as the full result set.
- Dark mode uses the same semantic-token mapping; no component-local dark color fork was added. Text, borders, selected cards, status pills and code/search detail remained readable in the focused capture.
- The Demo fixture is explicitly labeled and reuses production `ActivityGroup`. Browser checks proved single-selection expansion, group collapse/expand, failed MCP detail, 5 Action cards, and zero console errors.

### Production retention evidence

- A real stored conversation with 148 tool calls initially lost every tool card because 500-frame tail truncation favored later `message.delta` events.
- The cap still remains 500, but now retains structural transcript anchors and complete tool-call bundles before filling the remaining budget with noisy tail events.
- Fresh browser history load rendered one collapsed group labeled「展开 148 个工具调用」. Expanding produced all 148 cards with a truthful summary of 142 completed, 5 failed and 1 stopped; no console errors occurred.
- `/languagegui` is lazy-loaded: the production build keeps the base entry near 414 kB and isolates the Demo page plus shared tool renderer instead of moving Demo-only fixtures into every Workbench route.
- No remaining actionable P0/P1/P2 tool-activity findings remain.

final result: passed
