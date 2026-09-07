# Frontend owner task state

Status: completed-local-scope

Owner: knowledge product / frontend

Worktree: /Users/yin/Documents/ybs/code/agent-work-knowledge-librarian

Updated: 2026-09-06

Completed:

- Added web/src/api/knowledge.ts with workspace-scoped config, items, versions, relations, submissions, curation, publish, inquiry, job and cancel contracts.
- Added the /knowledge route and persistent sidebar entry.
- Added the KnowledgePage with real API states for librarian configuration, published knowledge and scope/private view, source/version/relation detail, candidate submission, inbox actions, inquiry form, job polling/cancel, coverage, gaps and citations.
- Added manual candidate fields for source Agent, title, kind, visibility, evidence reference and Markdown body.
- Added keyset "加载更多" for published items with workspace/view-agent request fencing, scope input validation (key=value or JSON object), and an explicit scope placeholder.
- Added detail-drawer repeal flow with reason entry, confirmation, current-version CAS request, and historical-version preservation.
- Marked jobs and submissions as "已加载" with a visible partial-list indicator when the API returns `truncated`.
- Replaced backend-facing UI labels with Chinese product copy, added Chinese presets for fact/rule/decision, and surfaced source metadata as 未验证/已核验 when explicitly provided.
- Fixed the publication gate: only `needs_review` submissions with non-empty, count-matched result item/version IDs can publish; accepted/merged rows are read-only.
- Source badges now interpret only the controlled `metadata.verification` codes and never trust a model-supplied `metadata.verified` flag; candidate source inputs without persisted IDs remain 待核验.
- When the selected librarian job crosses from running to a terminal state, the UI refreshes the submissions inbox while preserving unsent manual candidate form input.
- Added API and page presentation tests.

Validation:

- pnpm tsc -b passed.
- pnpm lint passed.
- pnpm test passed: 107 files, 882 tests.
- Documentation link check passed for the product/protocol knowledge documents.
- Focused knowledge/page/design token tests passed.
- OpenAPI YAML parses with 122 paths and 139 operationIds after knowledge pagination/repeal/list-result updates.
- No application was started and no code outside the frontend scope was intentionally changed.

Root acceptance:

- Real config, submission, curation/publication, inquiry, cancellation and historical read paths were exercised. The user confirmed the final repeal UI action; HTTP checks verified withdrawal and retained history. See `docs/review/knowledge-librarian-acceptance.md`.
