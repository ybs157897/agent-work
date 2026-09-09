# U04 implementation contract

Status: implementing in U04 after verified U03 handoff.

Owner split: root owns `internal/chatanalysis/` strict output schema/decoder/pure validation and acceptance; backend owns domain/repository/migration/application/HTTP/runtime terminal wiring/OpenAPI; frontend owns `web/`. No owner modifies another owner's files. No commits without root integration.

## Run entry

Reuse `POST /work-items/{chat_id}/runs`, with `output_contract="chat-analysis/v1"`. This is an existing Chat Run, not a second model loop or new page. The UI exposes one Chat action “整理需求，逐项确认”. If a conversation does not yet exist, reuse the normal Chat creation/upload path. Use the existing instruction, source_refs, queue, retry, and failure retention.

Backend starts a Chat analysis attempt in the same CreateRun transaction and freezes its version, source catalog, request digest and Run ID. The model receives the existing conversation plus all current Chat originals (merge existing/request refs, deduplicate; availability errors preserve the previous valid analysis). Code and knowledge are read through current Workspace capabilities. Caller cannot supply trusted attempt metadata.

## Model output (root package)

One closed `atw-analysis` JSON fence:

```json
{"version":"chat-analysis/v1","summary":"...","sources":[{"id":"s1","kind":"attachment","ref":"src_...","sha256":"64hex","locator":"第1页","read_status":"partial","coverage":"正文","limitations":"图片未读"}],"items":[{"id":"java-reading","kind":"requirement","title":"...","detail":"...","source_ids":["s1"],"basis":"observed","impact":"...","recommendation":"..."}],"questions":[{"id":"first-scope","prompt":"...","selection":"single","options":[{"id":"reading","label":"只读能力优先"},{"id":"editing","label":"包含编辑能力"}],"item_ids":["java-reading"]}]}
```

Sources: `kind=attachment|conversation|code|knowledge`, `ref` is source ID, a frozen conversation-source key, relative code path, or knowledge item ID. `sha256` is the version digest; optional `version` is the knowledge version integer. `read_status=read|partial|failed`. `locator`, `coverage`, `limitations`, and `quote` are bounded model-reported source context; a failed/partial source requires limitations. No arbitrary URLs or absolute code paths.

Items: `kind=requirement|normal|exception|conflict|unknown`, `basis=observed|proposed|unverified`. All item IDs and question IDs are stable local slugs, unique per array. Every item names declared source IDs. A conflict carries impact and recommendation. Failed sources cannot support an observed item. No model field can represent approved product decisions.

Questions: single/multiple choice with 2–8 options, or `selection=text` with no options. A question references valid item IDs. Model can enumerate its backlog inside the validated document, but the UI renders only one active question. The raw machine block is not rendered as a pile of questions in the transcript; show its persisted projection after server validation, or a failure message if invalid.

Bounds: 40 sources, 100 items, 30 questions; 128 KiB envelope; reasonable text bounds; unknown JSON fields, duplicate keys, duplicate IDs, wrong versions and dangling references rejected. Root decoder takes final assistant text, returns typed Document; it does not call models or read original files.

## Server source validation

Attachment refs must be in this Run's frozen source catalog, same Chat/Agent/Workspace and digest; use U03 Store to check exact bytes again. Conversation refs/digests must match the catalog frozen by the service from user messages/current instruction (include the complete catalog in the analysis context so the Agent copies rather than invents digests). Code refs must be relative to this Run's trusted canonical project root, no traversal/symlink escapes, and match actual file digest. Knowledge refs/versions must be visible through the existing Workspace/Agent repository and match its content hash. Use actual Run tool events where available to retain reading evidence; do not infer product approval from a quote or tool call.

On succeeded terminal Run, decode the final analysis fence and validate sources in a transaction before appending immutable revision. Failed/invalid/late results retain the last valid revision; attempts are idempotent by Run ID. The current attempt/version fence prevents older results overwriting new answers or newer analysis. GET may reconcile a terminal unprocessed attempt to recover a crash after Run terminalization.

## Durable projection and HTTP

`GET /work-items/{chat_id}/analysis` returns:

```text
{workspace_id,chat_id,agent_id,version,revision,status,run_id?,error?,
 document?,current_question?,pending_count,answered_count,deferred_count,answers:[]}
```

`status=idle|analyzing|needs_answer|ready|failed|stale`. No analysis yet is 200 idle. `version` is aggregate CAS; revision is the last valid document revision. Preserve previous document on failed/rejected attempts.

`POST /work-items/{chat_id}/analysis/answers` body:
`{expected_version,revision,question_id,selected_option_ids:[],text,disposition:"answered"|"deferred",client_key}` plus Idempotency-Key. Append answer and advance in one transaction; matching retry returns same projection, conflicting retry rejects. “暂不确定” is explicit deferred, moves to next question but never becomes product confirmation. Current question is the first question without an answer; there is no automatic re-asking of answered questions. Invalid/late question or revision returns 409 and preserves UI draft.

Answers store revision/question fingerprint/source dependencies so U05 can retain unaffected answers/confirmations across changed analyses. U04 answers are developer context, never product approval. U05 adds confirmation history and selective reopening; U06 alone creates drafts and requires explicit publication.

## Known workflow context

Do not ask these again: internal developers use this; they discuss with product and record conclusions themselves; requirements are scattered; sources include Chat, project code, Workspace/Agent knowledge and original attachments; conflicts require sources/impact/suggestion and later product confirmation. These are process defaults, not approved Java delivery scope.

## UI

Persist only unsubmitted answer draft locally under Workspace/Agent/Chat/revision/question; server owns saved answers and active question. Radio/checkbox + optional text, explicit “保存回答并继续”, separate “暂不确定”. Refresh/A-B-A restore. Normal Chat remains available. Source details/analysis items are reviewable without exposing all questions. Start/answer failures keep input; workspace/request fences reject old responses.
