-- 0044_knowledge_library.sql — Knowledge librarian authority.
--
-- Markdown is the durable content representation, stored as TEXT in the
-- immutable version row.  The FTS table is a published read projection.  A
-- publish transaction changes the current pointer, evidence/relations, and
-- projection together; a rebuild can recreate only the projection.

CREATE TABLE knowledge_items (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id),
    owner_agent_id      TEXT REFERENCES agent_profiles(id),
    visibility           TEXT NOT NULL CHECK (visibility IN ('private','workspace')),
    kind                TEXT NOT NULL,
    title               TEXT NOT NULL,
    summary             TEXT NOT NULL DEFAULT '',
    tags                TEXT NOT NULL DEFAULT '[]',
    aliases             TEXT NOT NULL DEFAULT '[]',
    scope_json          TEXT NOT NULL DEFAULT '{}',
    current_version_id  TEXT,
    current_version     INTEGER NOT NULL DEFAULT 0 CHECK (current_version >= 0),
    status              TEXT NOT NULL DEFAULT 'candidate'
                        CHECK (status IN ('candidate','draft','effective','superseded','repealed')),
    repeal_reason       TEXT NOT NULL DEFAULT '',
    version             INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at          DATETIME NOT NULL,
    updated_at          DATETIME NOT NULL
);
CREATE INDEX idx_knowledge_items_scope
    ON knowledge_items(workspace_id, visibility, owner_agent_id, status);

CREATE TABLE knowledge_versions (
    id                    TEXT PRIMARY KEY,
    item_id               TEXT NOT NULL REFERENCES knowledge_items(id),
    version               INTEGER NOT NULL CHECK (version >= 1),
    base_version          INTEGER NOT NULL DEFAULT 0 CHECK (base_version >= 0),
    status                TEXT NOT NULL CHECK (status IN ('candidate','draft','effective','superseded','repealed')),
    kind                  TEXT NOT NULL,
    title                 TEXT NOT NULL,
    summary               TEXT NOT NULL DEFAULT '',
    body_markdown         TEXT NOT NULL,
    tags                  TEXT NOT NULL DEFAULT '[]',
    aliases               TEXT NOT NULL DEFAULT '[]',
    scope_json            TEXT NOT NULL DEFAULT '{}',
    metadata_json         TEXT NOT NULL DEFAULT '{}',
    content_digest        TEXT NOT NULL,
    created_by_agent_id   TEXT NOT NULL REFERENCES agent_profiles(id),
    created_by_run_id     TEXT REFERENCES execution_runs(id),
    created_by_work_item_id TEXT REFERENCES work_items(id),
    supersedes_version_id TEXT,
    published_at          DATETIME,
    created_at            DATETIME NOT NULL,
    UNIQUE (item_id, version)
);
CREATE INDEX idx_knowledge_versions_item_status
    ON knowledge_versions(item_id, status, version DESC);
CREATE INDEX idx_knowledge_versions_workspace
    ON knowledge_versions(created_by_agent_id, created_at DESC);

-- The Markdown and identity fields are immutable.  Lifecycle transitions are
-- deliberately left to the repository publish CAS, so historical content can
-- never be rewritten while the current pointer changes atomically.
CREATE TRIGGER knowledge_versions_immutable_update
BEFORE UPDATE OF item_id, version, base_version, kind, title, summary,
                 body_markdown, tags, aliases, scope_json, metadata_json,
                 content_digest, created_by_agent_id, created_by_run_id,
                 created_by_work_item_id, supersedes_version_id, created_at
ON knowledge_versions
WHEN OLD.item_id IS NOT NEW.item_id
  OR OLD.version IS NOT NEW.version
  OR OLD.base_version IS NOT NEW.base_version
  OR OLD.kind IS NOT NEW.kind
  OR OLD.title IS NOT NEW.title
  OR OLD.summary IS NOT NEW.summary
  OR OLD.body_markdown IS NOT NEW.body_markdown
  OR OLD.tags IS NOT NEW.tags
  OR OLD.aliases IS NOT NEW.aliases
  OR OLD.scope_json IS NOT NEW.scope_json
  OR OLD.metadata_json IS NOT NEW.metadata_json
  OR OLD.content_digest IS NOT NEW.content_digest
  OR OLD.created_by_agent_id IS NOT NEW.created_by_agent_id
  OR OLD.created_by_run_id IS NOT NEW.created_by_run_id
  OR OLD.created_by_work_item_id IS NOT NEW.created_by_work_item_id
  OR OLD.supersedes_version_id IS NOT NEW.supersedes_version_id
  OR OLD.created_at IS NOT NEW.created_at
BEGIN
    SELECT RAISE(ABORT, 'knowledge version content is immutable');
END;

CREATE TRIGGER knowledge_versions_immutable_delete
BEFORE DELETE ON knowledge_versions
BEGIN
    SELECT RAISE(ABORT, 'knowledge version history is immutable');
END;

CREATE TABLE knowledge_sources (
    id                    TEXT PRIMARY KEY,
    workspace_id          TEXT NOT NULL REFERENCES workspaces(id),
    submitted_by_agent_id TEXT NOT NULL REFERENCES agent_profiles(id),
    kind                  TEXT NOT NULL CHECK (kind IN ('run','artifact','work_item','document','code','test','user','agent')),
    ref                   TEXT NOT NULL,
    locator               TEXT NOT NULL DEFAULT '',
    excerpt               TEXT NOT NULL DEFAULT '',
    digest                TEXT NOT NULL DEFAULT '',
    metadata_json         TEXT NOT NULL DEFAULT '{}',
    created_at            DATETIME NOT NULL
);
CREATE INDEX idx_knowledge_sources_workspace ON knowledge_sources(workspace_id, created_at DESC);

CREATE TRIGGER knowledge_sources_immutable_update
BEFORE UPDATE ON knowledge_sources
BEGIN
    SELECT RAISE(ABORT, 'knowledge source is immutable');
END;

CREATE TRIGGER knowledge_sources_immutable_delete
BEFORE DELETE ON knowledge_sources
BEGIN
    SELECT RAISE(ABORT, 'knowledge source history is immutable');
END;

CREATE TABLE knowledge_version_sources (
    version_id TEXT NOT NULL REFERENCES knowledge_versions(id),
    source_id  TEXT NOT NULL REFERENCES knowledge_sources(id),
    role       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (version_id, source_id)
);
CREATE INDEX idx_knowledge_version_sources_source ON knowledge_version_sources(source_id);

CREATE TRIGGER knowledge_version_sources_immutable_update
BEFORE UPDATE ON knowledge_version_sources
BEGIN
    SELECT RAISE(ABORT, 'knowledge version source link is immutable');
END;

CREATE TRIGGER knowledge_version_sources_immutable_delete
BEFORE DELETE ON knowledge_version_sources
BEGIN
    SELECT RAISE(ABORT, 'knowledge version source history is immutable');
END;

CREATE TABLE knowledge_relations (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL REFERENCES workspaces(id),
    source_version_id TEXT NOT NULL REFERENCES knowledge_versions(id),
    from_item_id      TEXT NOT NULL REFERENCES knowledge_items(id),
    to_item_id        TEXT NOT NULL REFERENCES knowledge_items(id),
    relation_kind     TEXT NOT NULL CHECK (relation_kind IN (
                          'related_to','depends_on','impacts','triggers','calls',
                          'subscribes_to','shares_state','constrained_by',
                          'conflicts_with','supersedes')),
    condition         TEXT NOT NULL DEFAULT '',
    rationale         TEXT NOT NULL DEFAULT '',
    created_at        DATETIME NOT NULL,
    CHECK (from_item_id <> to_item_id),
    UNIQUE (source_version_id, from_item_id, to_item_id, relation_kind)
);
CREATE INDEX idx_knowledge_relations_from ON knowledge_relations(from_item_id, source_version_id);
CREATE INDEX idx_knowledge_relations_to ON knowledge_relations(to_item_id, source_version_id);
CREATE INDEX idx_knowledge_relations_workspace ON knowledge_relations(workspace_id, relation_kind);

CREATE TRIGGER knowledge_relations_immutable_update
BEFORE UPDATE ON knowledge_relations
BEGIN
    SELECT RAISE(ABORT, 'knowledge relation is immutable');
END;

CREATE TRIGGER knowledge_relations_immutable_delete
BEFORE DELETE ON knowledge_relations
BEGIN
    SELECT RAISE(ABORT, 'knowledge relation history is immutable');
END;

CREATE TABLE knowledge_repeals (
    id              TEXT PRIMARY KEY,
    workspace_id    TEXT NOT NULL REFERENCES workspaces(id),
    item_id         TEXT NOT NULL REFERENCES knowledge_items(id),
    version_id      TEXT NOT NULL REFERENCES knowledge_versions(id),
    current_version INTEGER NOT NULL CHECK (current_version >= 1),
    reason          TEXT NOT NULL,
    created_at      DATETIME NOT NULL,
    UNIQUE (item_id, current_version)
);
CREATE INDEX idx_knowledge_repeals_item ON knowledge_repeals(item_id, created_at DESC);

CREATE TRIGGER knowledge_repeals_immutable_update
BEFORE UPDATE ON knowledge_repeals
BEGIN
    SELECT RAISE(ABORT, 'knowledge repeal is immutable');
END;

CREATE TRIGGER knowledge_repeals_immutable_delete
BEFORE DELETE ON knowledge_repeals
BEGIN
    SELECT RAISE(ABORT, 'knowledge repeal history is immutable');
END;

CREATE TABLE knowledge_relation_sources (
    relation_id TEXT NOT NULL REFERENCES knowledge_relations(id),
    source_id   TEXT NOT NULL REFERENCES knowledge_sources(id),
    PRIMARY KEY (relation_id, source_id)
);

CREATE TRIGGER knowledge_relation_sources_immutable_update
BEFORE UPDATE ON knowledge_relation_sources
BEGIN
    SELECT RAISE(ABORT, 'knowledge relation source link is immutable');
END;

CREATE TRIGGER knowledge_relation_sources_immutable_delete
BEFORE DELETE ON knowledge_relation_sources
BEGIN
    SELECT RAISE(ABORT, 'knowledge relation source history is immutable');
END;

CREATE TABLE knowledge_submissions (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL REFERENCES workspaces(id),
    agent_id          TEXT NOT NULL REFERENCES agent_profiles(id),
    run_id            TEXT REFERENCES execution_runs(id),
    work_item_id      TEXT REFERENCES work_items(id),
    client_key        TEXT NOT NULL,
    request_json      TEXT NOT NULL,
    request_digest    TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'received'
                      CHECK (status IN ('received','processing','accepted','merged','rejected','needs_review')),
    result_item_ids   TEXT NOT NULL DEFAULT '[]',
    result_version_ids TEXT NOT NULL DEFAULT '[]',
    error_message     TEXT NOT NULL DEFAULT '',
    version           INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at        DATETIME NOT NULL,
    updated_at        DATETIME NOT NULL,
    UNIQUE (workspace_id, agent_id, client_key)
);
CREATE INDEX idx_knowledge_submissions_queue ON knowledge_submissions(workspace_id, status, created_at);

-- A submission is an append-only capture envelope.  Only processing metadata
-- can move; the agent's original request/digest is retained for audit.
CREATE TRIGGER knowledge_submissions_immutable_update
BEFORE UPDATE OF workspace_id, agent_id, run_id, work_item_id, client_key,
                 request_json, request_digest, created_at
ON knowledge_submissions
WHEN OLD.workspace_id IS NOT NEW.workspace_id
  OR OLD.agent_id IS NOT NEW.agent_id
  OR OLD.run_id IS NOT NEW.run_id
  OR OLD.work_item_id IS NOT NEW.work_item_id
  OR OLD.client_key IS NOT NEW.client_key
  OR OLD.request_json IS NOT NEW.request_json
  OR OLD.request_digest IS NOT NEW.request_digest
  OR OLD.created_at IS NOT NEW.created_at
BEGIN
    SELECT RAISE(ABORT, 'knowledge submission request is immutable');
END;

CREATE TABLE knowledge_query_snapshots (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id),
    requester_agent_id  TEXT NOT NULL REFERENCES agent_profiles(id),
    question            TEXT NOT NULL,
    context             TEXT NOT NULL DEFAULT '',
    budget_json         TEXT NOT NULL DEFAULT '{}',
    index_revision      INTEGER NOT NULL DEFAULT 0 CHECK (index_revision >= 0),
    result_json         TEXT NOT NULL DEFAULT '[]',
    coverage_json       TEXT NOT NULL DEFAULT '{}',
    created_at          DATETIME NOT NULL
);
CREATE INDEX idx_knowledge_query_snapshots_scope
    ON knowledge_query_snapshots(workspace_id, requester_agent_id, created_at DESC);

CREATE TRIGGER knowledge_query_snapshots_immutable_update
BEFORE UPDATE ON knowledge_query_snapshots
BEGIN
    SELECT RAISE(ABORT, 'knowledge query snapshot is immutable');
END;

CREATE TABLE knowledge_index_state (
    workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id),
    revision     INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
    item_count   INTEGER NOT NULL DEFAULT 0 CHECK (item_count >= 0),
    built_at     DATETIME NOT NULL
);

-- The rows are the current effective versions only.  UNINDEXED identity and
-- scope columns make permission filtering cheap while title/body/aliases/tags
-- remain searchable.  CJK substring fallback is implemented in Go, matching
-- the existing session search contract.
CREATE VIRTUAL TABLE knowledge_index USING fts5(
    version_id UNINDEXED,
    item_id UNINDEXED,
    workspace_id UNINDEXED,
    owner_agent_id UNINDEXED,
    visibility UNINDEXED,
    kind UNINDEXED,
    title,
    summary,
    body,
    aliases,
    tags,
    scope,
    tokenize = 'unicode61'
);
