-- 0057_knowledge_library.sql — Unified project knowledge library (资料库).
--
-- Replaces the retired per-item librarian authority with one Markdown library
-- per workspace: registered multi-repo sources, immutable source snapshots and
-- representations, program-collected evidence, parsed Assertion/Relation
-- records, release-pinned query, and a single FIFO write queue.
--
-- ── Legacy retirement ───────────────────────────────────────────────────
--
-- The retired librarian's tables are preserved byte-for-byte, never dropped:
-- existing user knowledge stays readable for recovery.  They are deliberately
-- NOT renamed, because SQLite re-parses every trigger and view in the schema
-- on ALTER TABLE ... RENAME, which would couple this migration to the state of
-- unrelated tables.  Retirement is a code-level fact instead: no Go source
-- references these tables any more (pinned by
-- internal/persistence/sqlstore/knowledge_library_retirement_test.go), so the
-- old and the new administrator can never double-write.
--
--   knowledge_items, knowledge_versions, knowledge_sources,
--   knowledge_version_sources, knowledge_relations, knowledge_relation_sources,
--   knowledge_repeals, knowledge_submissions, knowledge_query_snapshots,
--   knowledge_jobs, knowledge_job_actions, knowledge_run_captures,
--   knowledge_librarian_configs, knowledge_index, knowledge_index_state
--
-- The new library uses a distinct namespace (knowledge_libraries,
-- knowledge_library_*, knowledge_document*, knowledge_assertion*,
-- knowledge_release*, knowledge_write_tasks, knowledge_search_index, ...), so
-- both schemas can coexist while the old one is only ever read by a human.

-- ── Library root ────────────────────────────────────────────────────────
-- One library per workspace.  root_path is the absolute directory that holds
-- the Markdown library (content/, catalog/, _sources/, _system/).
CREATE TABLE knowledge_libraries (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL UNIQUE REFERENCES workspaces(id),
    root_path           TEXT NOT NULL,
    librarian_agent_id  TEXT REFERENCES agent_profiles(id),
    enabled             INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    current_release_id  TEXT,
    current_snapshot_id TEXT,
    index_revision      INTEGER NOT NULL DEFAULT 0 CHECK (index_revision >= 0),
    version             INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at          DATETIME NOT NULL,
    updated_at          DATETIME NOT NULL
);

-- ── Source ledger ───────────────────────────────────────────────────────
-- Registered repositories.  A service, a common library and a document
-- collection are all sources of the same one library.
CREATE TABLE knowledge_library_sources (
    id            TEXT PRIMARY KEY,
    library_id    TEXT NOT NULL REFERENCES knowledge_libraries(id),
    name          TEXT NOT NULL,
    kind          TEXT NOT NULL CHECK (kind IN ('service','common','documents','other')),
    repo_path     TEXT NOT NULL,
    default_ref   TEXT NOT NULL DEFAULT '',
    include_globs TEXT NOT NULL DEFAULT '[]',
    exclude_globs TEXT NOT NULL DEFAULT '[]',
    -- usages lists the concrete use bindings this logical source takes part
    -- in. One common source may be used by several consumers at different
    -- artifact versions; each usage becomes its own snapshot binding, so the
    -- library never has to register the same repository twice to express it.
    usages_json   TEXT NOT NULL DEFAULT '[]',
    enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    version       INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at    DATETIME NOT NULL,
    updated_at    DATETIME NOT NULL,
    UNIQUE (library_id, name),
    CHECK (length(trim(repo_path)) > 0)
);

-- An immutable input set.  view_id distinguishes the shared baseline from a
-- development/branch overlay; overlay writes use the same queue.
CREATE TABLE knowledge_snapshots (
    id                 TEXT PRIMARY KEY,
    library_id         TEXT NOT NULL REFERENCES knowledge_libraries(id),
    view_id            TEXT NOT NULL DEFAULT 'baseline',
    parent_snapshot_id TEXT REFERENCES knowledge_snapshots(id),
    reason             TEXT NOT NULL DEFAULT '',
    captured_at        DATETIME NOT NULL,
    CHECK (length(trim(view_id)) > 0)
);
CREATE INDEX idx_knowledge_snapshots_library
    ON knowledge_snapshots(library_id, captured_at DESC);

-- One row per source actually read for this snapshot.  A logical source_id
-- may appear under several bindings with different artifact versions.
CREATE TABLE knowledge_snapshot_bindings (
    id               TEXT PRIMARY KEY,
    snapshot_id      TEXT NOT NULL REFERENCES knowledge_snapshots(id),
    source_id        TEXT NOT NULL REFERENCES knowledge_library_sources(id),
    source_name      TEXT NOT NULL,
    source_kind      TEXT NOT NULL,
    repo_path        TEXT NOT NULL,
    git_ref          TEXT NOT NULL DEFAULT '',
    commit_sha       TEXT NOT NULL DEFAULT '',
    object_format    TEXT NOT NULL DEFAULT 'sha1',
    dirty            INTEGER NOT NULL DEFAULT 0 CHECK (dirty IN (0,1)),
    dirty_digest     TEXT NOT NULL DEFAULT '',
    untracked_json   TEXT NOT NULL DEFAULT '[]',
    artifact         TEXT NOT NULL DEFAULT '',
    -- artifact_resolution records how the artifact value was obtained. A
    -- version typed into the admin form is 'declared'; only a real
    -- build/dependency resolution may claim 'resolved'. Unknown stays
    -- 'unknown' rather than being presented as the actual version.
    artifact_resolution TEXT NOT NULL DEFAULT 'declared'
                        CHECK (artifact_resolution IN ('declared','resolved','unknown')),
    -- artifact_resolution_ref is the verifiable basis of a 'resolved' claim.
    -- Without it the effective state stays 'declared'.
    artifact_resolution_ref TEXT NOT NULL DEFAULT '',
    consumer         TEXT NOT NULL DEFAULT '',
    environment      TEXT NOT NULL DEFAULT '',
    captured_at      DATETIME NOT NULL,
    -- A logical source appears once per full usage identity. The identity
    -- includes environment as well as consumer and artifact, so two
    -- environments of the same consumer/artifact are two bindings rather than
    -- one silently reused row.
    UNIQUE (snapshot_id, source_id, consumer, artifact, environment)
);
CREATE INDEX idx_knowledge_bindings_source ON knowledge_snapshot_bindings(source_id);

-- Frozen bytes actually used as the basis for a locator.  The digest is
-- always computed by the collector over the stored representation.
CREATE TABLE knowledge_representations (
    id             TEXT PRIMARY KEY,
    binding_id     TEXT NOT NULL REFERENCES knowledge_snapshot_bindings(id),
    path           TEXT NOT NULL,
    media_type     TEXT NOT NULL DEFAULT 'text/plain',
    encoding       TEXT NOT NULL DEFAULT 'utf-8',
    digest_algo    TEXT NOT NULL CHECK (digest_algo IN ('sha256','git-blob-sha1')),
    content_digest TEXT NOT NULL,
    byte_size      INTEGER NOT NULL CHECK (byte_size >= 0),
    coverage       TEXT NOT NULL DEFAULT 'full' CHECK (coverage IN ('full','excerpt','export')),
    transform      TEXT NOT NULL DEFAULT '',
    stored_path    TEXT NOT NULL,
    origin         TEXT NOT NULL CHECK (origin IN ('committed','worktree')),
    created_at     DATETIME NOT NULL,
    UNIQUE (binding_id, path)
);
CREATE INDEX idx_knowledge_representations_digest
    ON knowledge_representations(content_digest);

CREATE TRIGGER knowledge_representations_immutable_update
BEFORE UPDATE ON knowledge_representations
BEGIN
    SELECT RAISE(ABORT, 'knowledge representation is immutable');
END;

-- ── Evidence ledger ─────────────────────────────────────────────────────
-- Evidence is a locator inside one frozen representation.  It is created by
-- the collector, never by the model, so id/digest/excerpt cannot be forged.
CREATE TABLE knowledge_evidence (
    id                TEXT PRIMARY KEY,
    library_id        TEXT NOT NULL REFERENCES knowledge_libraries(id),
    snapshot_id       TEXT NOT NULL REFERENCES knowledge_snapshots(id),
    binding_id        TEXT NOT NULL REFERENCES knowledge_snapshot_bindings(id),
    representation_id TEXT NOT NULL REFERENCES knowledge_representations(id),
    locator_json      TEXT NOT NULL DEFAULT '{}',
    locator_kind      TEXT NOT NULL DEFAULT 'source_text',
    excerpt           TEXT NOT NULL DEFAULT '',
    excerpt_digest    TEXT NOT NULL DEFAULT '',
    match_count       INTEGER NOT NULL DEFAULT 1 CHECK (match_count >= 0),
    availability      TEXT NOT NULL DEFAULT 'available'
                      CHECK (availability IN ('available','unavailable')),
    collected_at      DATETIME NOT NULL,
    UNIQUE (representation_id, locator_json)
);
CREATE INDEX idx_knowledge_evidence_binding ON knowledge_evidence(binding_id);
CREATE INDEX idx_knowledge_evidence_library ON knowledge_evidence(library_id, collected_at DESC);

-- Evidence may only point at a representation that belongs to its own
-- binding, and at the binding's own snapshot. A cross-binding reference would
-- let one usage's frozen bytes be cited as another usage's evidence.
CREATE TRIGGER knowledge_evidence_binding_consistency_insert
BEFORE INSERT ON knowledge_evidence
WHEN NOT EXISTS (
    SELECT 1 FROM knowledge_representations r
    JOIN knowledge_snapshot_bindings b ON b.id = r.binding_id
    WHERE r.id = NEW.representation_id
      AND r.binding_id = NEW.binding_id
      AND b.snapshot_id = NEW.snapshot_id
)
BEGIN
    SELECT RAISE(ABORT, 'knowledge evidence must reference its own binding and snapshot');
END;

CREATE TRIGGER knowledge_evidence_immutable_update
BEFORE UPDATE ON knowledge_evidence
BEGIN
    SELECT RAISE(ABORT, 'knowledge evidence is immutable');
END;
CREATE TRIGGER knowledge_evidence_immutable_delete
BEFORE DELETE ON knowledge_evidence
BEGIN
    SELECT RAISE(ABORT, 'knowledge evidence history is immutable');
END;

-- ── Entities, documents, assertions, relations ──────────────────────────
CREATE TABLE knowledge_entities (
    id                  TEXT PRIMARY KEY,
    library_id          TEXT NOT NULL REFERENCES knowledge_libraries(id),
    kind                TEXT NOT NULL,
    namespace           TEXT NOT NULL DEFAULT '',
    canonical_name      TEXT NOT NULL,
    aliases_json        TEXT NOT NULL DEFAULT '[]',
    source_id           TEXT REFERENCES knowledge_library_sources(id),
    primary_document_id TEXT,
    version             INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at          DATETIME NOT NULL,
    updated_at          DATETIME NOT NULL,
    UNIQUE (library_id, kind, namespace, canonical_name)
);

CREATE TABLE knowledge_documents (
    id              TEXT PRIMARY KEY,
    library_id      TEXT NOT NULL REFERENCES knowledge_libraries(id),
    path            TEXT NOT NULL,
    kind            TEXT NOT NULL,
    title           TEXT NOT NULL,
    summary         TEXT NOT NULL DEFAULT '',
    domains_json    TEXT NOT NULL DEFAULT '[]',
    status          TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active','removed','renamed')),
    renamed_from    TEXT NOT NULL DEFAULT '',
    rename_note     TEXT NOT NULL DEFAULT '',
    current_version INTEGER NOT NULL DEFAULT 0 CHECK (current_version >= 0),
    created_at      DATETIME NOT NULL,
    updated_at      DATETIME NOT NULL,
    UNIQUE (library_id, path)
);
CREATE INDEX idx_knowledge_documents_status ON knowledge_documents(library_id, status);

CREATE TABLE knowledge_document_versions (
    id               TEXT PRIMARY KEY,
    document_id      TEXT NOT NULL REFERENCES knowledge_documents(id),
    library_id       TEXT NOT NULL REFERENCES knowledge_libraries(id),
    version          INTEGER NOT NULL CHECK (version >= 1),
    title            TEXT NOT NULL,
    -- path is the document path as of this version. A later rename must not
    -- change how a historical release is read.
    path             TEXT NOT NULL DEFAULT '',
    content_markdown TEXT NOT NULL,
    frontmatter_json TEXT NOT NULL DEFAULT '{}',
    content_digest   TEXT NOT NULL,
    snapshot_id      TEXT REFERENCES knowledge_snapshots(id),
    task_id          TEXT REFERENCES knowledge_write_tasks(id),
    release_id       TEXT,
    derived_from_version_id TEXT,
    created_at       DATETIME NOT NULL,
    UNIQUE (document_id, version)
);
CREATE INDEX idx_knowledge_document_versions_library
    ON knowledge_document_versions(library_id, created_at DESC);

CREATE TRIGGER knowledge_document_versions_immutable_update
BEFORE UPDATE OF document_id, library_id, version, title, path, content_markdown,
                 frontmatter_json, content_digest, snapshot_id, created_at
ON knowledge_document_versions
WHEN OLD.document_id IS NOT NEW.document_id
  OR OLD.library_id IS NOT NEW.library_id
  OR OLD.version IS NOT NEW.version
  OR OLD.title IS NOT NEW.title
  OR OLD.path IS NOT NEW.path
  OR OLD.content_markdown IS NOT NEW.content_markdown
  OR OLD.frontmatter_json IS NOT NEW.frontmatter_json
  OR OLD.content_digest IS NOT NEW.content_digest
  OR OLD.snapshot_id IS NOT NEW.snapshot_id
  OR OLD.created_at IS NOT NEW.created_at
BEGIN
    SELECT RAISE(ABORT, 'knowledge document version content is immutable');
END;
CREATE TRIGGER knowledge_document_versions_immutable_delete
BEFORE DELETE ON knowledge_document_versions
BEGIN
    SELECT RAISE(ABORT, 'knowledge document version history is immutable');
END;

-- Assertion rows are materialized per document version so an old release
-- keeps reading the assertions exactly as they were published.
CREATE TABLE knowledge_assertions (
    row_id              TEXT PRIMARY KEY,
    assertion_id        TEXT NOT NULL,
    library_id          TEXT NOT NULL REFERENCES knowledge_libraries(id),
    document_id         TEXT NOT NULL REFERENCES knowledge_documents(id),
    document_version_id TEXT NOT NULL REFERENCES knowledge_document_versions(id),
    heading             TEXT NOT NULL DEFAULT '',
    about_json          TEXT NOT NULL DEFAULT '[]',
    perspective         TEXT NOT NULL CHECK (perspective IN ('normative','descriptive')),
    basis               TEXT NOT NULL CHECK (basis IN ('source_statement','code_static','runtime_observed','inferred')),
    statement           TEXT NOT NULL,
    scope_json          TEXT NOT NULL DEFAULT '{}',
    evidence_json       TEXT NOT NULL DEFAULT '[]',
    unknown_notes       TEXT NOT NULL DEFAULT '',
    ordinal             INTEGER NOT NULL DEFAULT 0,
    content_digest      TEXT NOT NULL,
    created_at          DATETIME NOT NULL,
    UNIQUE (document_version_id, assertion_id)
);
CREATE INDEX idx_knowledge_assertions_doc ON knowledge_assertions(document_id);
CREATE INDEX idx_knowledge_assertions_perspective
    ON knowledge_assertions(library_id, perspective, basis);

CREATE TABLE knowledge_assertion_relations (
    row_id              TEXT PRIMARY KEY,
    relation_id         TEXT NOT NULL,
    library_id          TEXT NOT NULL REFERENCES knowledge_libraries(id),
    document_id         TEXT NOT NULL REFERENCES knowledge_documents(id),
    document_version_id TEXT NOT NULL REFERENCES knowledge_document_versions(id),
    from_kind           TEXT NOT NULL CHECK (from_kind IN ('entity','document','assertion')),
    from_id             TEXT NOT NULL,
    predicate           TEXT NOT NULL,
    to_kind             TEXT NOT NULL CHECK (to_kind IN ('entity','document','assertion','unresolved')),
    to_id               TEXT NOT NULL,
    to_resolved         INTEGER NOT NULL DEFAULT 1 CHECK (to_resolved IN (0,1)),
    to_raw              TEXT NOT NULL DEFAULT '',
    perspective         TEXT NOT NULL CHECK (perspective IN ('normative','descriptive')),
    basis               TEXT NOT NULL CHECK (basis IN ('source_statement','code_static','runtime_observed','inferred')),
    condition           TEXT NOT NULL DEFAULT '',
    evidence_json       TEXT NOT NULL DEFAULT '[]',
    ordinal             INTEGER NOT NULL DEFAULT 0,
    content_digest      TEXT NOT NULL,
    created_at          DATETIME NOT NULL,
    UNIQUE (document_version_id, relation_id),
    CHECK (from_id <> to_id)
);
CREATE INDEX idx_knowledge_assertion_relations_from
    ON knowledge_assertion_relations(library_id, from_id, predicate);
CREATE INDEX idx_knowledge_assertion_relations_to
    ON knowledge_assertion_relations(library_id, to_id, predicate);

-- ── Releases ────────────────────────────────────────────────────────────
CREATE TABLE knowledge_releases (
    id                TEXT PRIMARY KEY,
    library_id        TEXT NOT NULL REFERENCES knowledge_libraries(id),
    seq               INTEGER NOT NULL CHECK (seq >= 1),
    snapshot_id       TEXT NOT NULL REFERENCES knowledge_snapshots(id),
    task_id           TEXT REFERENCES knowledge_write_tasks(id),
    parent_release_id TEXT REFERENCES knowledge_releases(id),
    projection_digest TEXT NOT NULL,
    document_count    INTEGER NOT NULL DEFAULT 0 CHECK (document_count >= 0),
    assertion_count   INTEGER NOT NULL DEFAULT 0 CHECK (assertion_count >= 0),
    relation_count    INTEGER NOT NULL DEFAULT 0 CHECK (relation_count >= 0),
    evidence_count    INTEGER NOT NULL DEFAULT 0 CHECK (evidence_count >= 0),
    coverage_json     TEXT NOT NULL DEFAULT '{}',
    notes             TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'published'
                      CHECK (status IN ('published','superseded')),
    published_at      DATETIME NOT NULL,
    UNIQUE (library_id, seq)
);
CREATE INDEX idx_knowledge_releases_library
    ON knowledge_releases(library_id, seq DESC);

CREATE TABLE knowledge_release_documents (
    release_id          TEXT NOT NULL REFERENCES knowledge_releases(id),
    document_id         TEXT NOT NULL REFERENCES knowledge_documents(id),
    document_version_id TEXT NOT NULL REFERENCES knowledge_document_versions(id),
    is_removed          INTEGER NOT NULL DEFAULT 0 CHECK (is_removed IN (0,1)),
    PRIMARY KEY (release_id, document_id)
);
CREATE INDEX idx_knowledge_release_documents_version
    ON knowledge_release_documents(document_version_id);

-- Derived cross-service views.  They pin both the release they were built
-- from and the version of every participant, so a model-stitched chain can
-- never be mistaken for a verified one.
CREATE TABLE knowledge_bridge_views (
    id                TEXT PRIMARY KEY,
    library_id        TEXT NOT NULL REFERENCES knowledge_libraries(id),
    release_id        TEXT NOT NULL REFERENCES knowledge_releases(id),
    base_release_id   TEXT REFERENCES knowledge_releases(id),
    anchor_entity_id  TEXT NOT NULL,
    title             TEXT NOT NULL,
    summary           TEXT NOT NULL DEFAULT '',
    steps_json        TEXT NOT NULL DEFAULT '[]',
    participants_json TEXT NOT NULL DEFAULT '[]',
    coverage_json     TEXT NOT NULL DEFAULT '{}',
    gaps_json         TEXT NOT NULL DEFAULT '[]',
    builder_version   TEXT NOT NULL DEFAULT 'bridge/v1',
    content_digest    TEXT NOT NULL,
    created_at        DATETIME NOT NULL
);
CREATE INDEX idx_knowledge_bridge_views_release
    ON knowledge_bridge_views(release_id, created_at DESC);

-- ── Retrieval projection (rebuildable) ──────────────────────────────────
-- Keyed by document version; a release-scoped search joins the release's
-- version set, so the index never has to be duplicated per release.
CREATE VIRTUAL TABLE knowledge_search_index USING fts5(
    document_version_id UNINDEXED,
    document_id UNINDEXED,
    library_id UNINDEXED,
    title,
    summary,
    body,
    aliases,
    tags,
    scope,
    entities,
    tokenize = 'unicode61'
);

-- ── External events and the single FIFO write queue ─────────────────────
CREATE TABLE knowledge_library_events (
    id               TEXT PRIMARY KEY,
    library_id       TEXT NOT NULL REFERENCES knowledge_libraries(id),
    protocol_version TEXT NOT NULL DEFAULT '1',
    event_type       TEXT NOT NULL,
    source           TEXT NOT NULL,
    subject_json     TEXT NOT NULL DEFAULT '{}',
    content_ref      TEXT NOT NULL DEFAULT '',
    payload_json     TEXT NOT NULL DEFAULT '{}',
    client_key       TEXT NOT NULL,
    request_digest   TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'accepted'
                     CHECK (status IN ('accepted','queued','processing','completed','failed','blocked')),
    task_id          TEXT,
    received_at      DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL,
    UNIQUE (library_id, client_key)
);
CREATE INDEX idx_knowledge_library_events_queue
    ON knowledge_library_events(library_id, status, received_at);
CREATE INDEX idx_knowledge_library_events_time
    ON knowledge_library_events(library_id, received_at DESC);

-- An event is an immutable report of a fact.  Only processing state moves.
CREATE TRIGGER knowledge_library_events_immutable_report_update
BEFORE UPDATE OF library_id, protocol_version, event_type, source, subject_json,
                 content_ref, payload_json, client_key, request_digest, received_at
ON knowledge_library_events
WHEN OLD.library_id IS NOT NEW.library_id
  OR OLD.protocol_version IS NOT NEW.protocol_version
  OR OLD.event_type IS NOT NEW.event_type
  OR OLD.source IS NOT NEW.source
  OR OLD.subject_json IS NOT NEW.subject_json
  OR OLD.content_ref IS NOT NEW.content_ref
  OR OLD.payload_json IS NOT NEW.payload_json
  OR OLD.client_key IS NOT NEW.client_key
  OR OLD.request_digest IS NOT NEW.request_digest
  OR OLD.received_at IS NOT NEW.received_at
BEGIN
    SELECT RAISE(ABORT, 'knowledge library event report is immutable');
END;

-- A write task is one complete unit of work: it runs to a terminal state
-- before the next task may write.  All persistent knowledge changes —
-- initialize, incremental, removal, invalidation, branch overlay — are tasks
-- in this one queue.
CREATE TABLE knowledge_write_tasks (
    id               TEXT PRIMARY KEY,
    library_id       TEXT NOT NULL REFERENCES knowledge_libraries(id),
    seq              INTEGER NOT NULL CHECK (seq >= 1),
    kind             TEXT NOT NULL CHECK (kind IN (
                         'initialize','incremental','remove','rename',
                         'invalidate','branch_view','legacy_import','reindex')),
    event_id         TEXT REFERENCES knowledge_library_events(id),
    status           TEXT NOT NULL DEFAULT 'queued' CHECK (status IN (
                         'queued','running','awaiting_agent','retry_wait',
                         'blocked','completed','failed','cancelled')),
    base_release_id  TEXT REFERENCES knowledge_releases(id),
    target_release_id TEXT REFERENCES knowledge_releases(id),
    snapshot_id      TEXT REFERENCES knowledge_snapshots(id),
    view_id          TEXT NOT NULL DEFAULT 'baseline',
    focus_json       TEXT NOT NULL DEFAULT '{}',
    plan_json        TEXT NOT NULL DEFAULT '{}',
    coverage_json    TEXT NOT NULL DEFAULT '{}',
    staging_path     TEXT NOT NULL DEFAULT '',
    work_item_id     TEXT REFERENCES work_items(id),
    current_run_id   TEXT REFERENCES execution_runs(id),
    attempt          INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    repair_attempt   INTEGER NOT NULL DEFAULT 0 CHECK (repair_attempt >= 0),
    max_attempts     INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts >= 1),
    max_repair_attempts INTEGER NOT NULL DEFAULT 2 CHECK (max_repair_attempts >= 0),
    turn_seq         INTEGER NOT NULL DEFAULT 0 CHECK (turn_seq >= 0),
    next_attempt_at  DATETIME,
    owner_token      TEXT NOT NULL DEFAULT '',
    last_error       TEXT NOT NULL DEFAULT '',
    blocked_reason   TEXT NOT NULL DEFAULT '',
    diagnostics_json TEXT NOT NULL DEFAULT '[]',
    created_at       DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL,
    finished_at      DATETIME,
    UNIQUE (library_id, seq)
);

-- The single-writer invariant is enforced by the database, not by a
-- convention in application code: at most one task per library may be in
-- 'running'.
CREATE UNIQUE INDEX idx_knowledge_write_tasks_single_active
    ON knowledge_write_tasks(library_id) WHERE status = 'running';
CREATE INDEX idx_knowledge_write_tasks_head
    ON knowledge_write_tasks(library_id, status, seq);

CREATE TABLE knowledge_task_turns (
    id           TEXT PRIMARY KEY,
    task_id      TEXT NOT NULL REFERENCES knowledge_write_tasks(id),
    turn_seq     INTEGER NOT NULL CHECK (turn_seq >= 1),
    run_id       TEXT NOT NULL REFERENCES execution_runs(id),
    purpose      TEXT NOT NULL CHECK (purpose IN ('initialize','incremental','repair','remove','rename','invalidate','branch_view','legacy_import','reindex')),
    status       TEXT NOT NULL CHECK (status IN ('pending','completed','failed')),
    request_json TEXT NOT NULL DEFAULT '{}',
    result_json  TEXT NOT NULL DEFAULT '{}',
    error_message TEXT NOT NULL DEFAULT '',
    created_at   DATETIME NOT NULL,
    updated_at   DATETIME NOT NULL,
    UNIQUE (task_id, turn_seq)
);
CREATE INDEX idx_knowledge_task_turns_run ON knowledge_task_turns(run_id);

-- Publish journal.  A release is prepared (rows + files staged), then
-- committed; recovery reads this table to decide whether an interrupted
-- publish already happened instead of guessing.
CREATE TABLE knowledge_publications (
    id                TEXT PRIMARY KEY,
    library_id        TEXT NOT NULL REFERENCES knowledge_libraries(id),
    task_id           TEXT NOT NULL REFERENCES knowledge_write_tasks(id),
    release_id        TEXT NOT NULL,
    status            TEXT NOT NULL CHECK (status IN ('prepared','committed','abandoned')),
    projection_digest TEXT NOT NULL,
    prepared_at       DATETIME NOT NULL,
    committed_at      DATETIME,
    UNIQUE (task_id)
);
CREATE INDEX idx_knowledge_publications_status
    ON knowledge_publications(library_id, status, prepared_at);

-- ── Built-in library agent ──────────────────────────────────────────────
-- The workspace-scoped system Agent stays the one internal executor, but its
-- contract changes from a chat persona to the versioned curation harness
-- prompt.  It now needs write access to the task staging directory, so the
-- fixed policy moves from read-only to workspace-write.
DROP TRIGGER agent_profiles_knowledge_librarian_insert_protected;
DROP TRIGGER agent_profiles_knowledge_librarian_protected;
DROP TRIGGER agent_profiles_knowledge_librarian_promote_forbidden;

UPDATE agent_profiles
SET prompt_version = 'knowledge-harness/v2',
    instructions   = '',
    policy         = json_object('approval_policy', 'auto', 'sandbox', 'workspace-write', 'tools', json('[]')),
    updated_at     = datetime('now')
WHERE kind = 'knowledge_librarian';

CREATE TRIGGER agent_profiles_knowledge_librarian_insert_protected
BEFORE INSERT ON agent_profiles
WHEN NEW.kind = 'knowledge_librarian'
 AND (NEW.prompt_version <> 'knowledge-harness/v2'
      OR NEW.instructions_editable <> 0
      OR COALESCE(json_extract(NEW.policy, '$.sandbox'), '') <> 'workspace-write'
      OR COALESCE(json_array_length(json_extract(NEW.policy, '$.tools')), 0) <> 0
      OR COALESCE(json_extract(NEW.policy, '$.approval_policy'), '') <> 'auto')
BEGIN
    SELECT RAISE(ABORT, 'system knowledge librarian profile must use the built-in prompt');
END;

CREATE TRIGGER agent_profiles_knowledge_librarian_protected
BEFORE UPDATE OF kind, instructions, prompt_version, instructions_editable, policy
ON agent_profiles
WHEN OLD.kind = 'knowledge_librarian'
 AND (NEW.kind <> OLD.kind
      OR NEW.instructions <> OLD.instructions
      OR NEW.prompt_version <> OLD.prompt_version
      OR NEW.instructions_editable <> OLD.instructions_editable
      OR NEW.policy <> OLD.policy)
BEGIN
    SELECT RAISE(ABORT, 'system knowledge librarian profile is protected');
END;

CREATE TRIGGER agent_profiles_knowledge_librarian_promote_forbidden
BEFORE UPDATE OF kind ON agent_profiles
WHEN OLD.kind <> 'knowledge_librarian' AND NEW.kind = 'knowledge_librarian'
BEGIN
    SELECT RAISE(ABORT, 'system knowledge librarian profile must be created by system provisioning');
END;
