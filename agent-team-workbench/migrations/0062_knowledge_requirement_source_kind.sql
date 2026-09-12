-- 0062_knowledge_requirement_source_kind.sql — a frozen requirement input is a
-- registered source so evidence collected from its text has a binding the
-- ledger can reference. SQLite cannot extend a CHECK constraint in place, so
-- the rows are moved aside, the table is recreated with the wider constraint
-- and the rows are put back. The table is recreated under its own name instead
-- of being renamed into place: SQLite re-parses every trigger in the schema
-- when a table is renamed, and that would couple this migration to unrelated
-- parts of the schema (it breaks incremental migration replays).
-- migrate:no-transaction
PRAGMA foreign_keys = OFF;
BEGIN;

CREATE TABLE knowledge_library_sources_carry (
    id            TEXT PRIMARY KEY,
    library_id    TEXT NOT NULL,
    name          TEXT NOT NULL,
    kind          TEXT NOT NULL,
    repo_path     TEXT NOT NULL,
    default_ref   TEXT NOT NULL DEFAULT '',
    include_globs TEXT NOT NULL DEFAULT '[]',
    exclude_globs TEXT NOT NULL DEFAULT '[]',
    usages_json   TEXT NOT NULL DEFAULT '[]',
    enabled       INTEGER NOT NULL DEFAULT 1,
    version       INTEGER NOT NULL DEFAULT 1,
    created_at    DATETIME NOT NULL,
    updated_at    DATETIME NOT NULL
);

INSERT INTO knowledge_library_sources_carry
    (id, library_id, name, kind, repo_path, default_ref, include_globs, exclude_globs,
     usages_json, enabled, version, created_at, updated_at)
SELECT id, library_id, name, kind, repo_path, default_ref, include_globs, exclude_globs,
       usages_json, enabled, version, created_at, updated_at
FROM knowledge_library_sources;

DROP TABLE knowledge_library_sources;

CREATE TABLE knowledge_library_sources (
    id            TEXT PRIMARY KEY,
    library_id    TEXT NOT NULL REFERENCES knowledge_libraries(id),
    name          TEXT NOT NULL,
    kind          TEXT NOT NULL CHECK (kind IN ('service','common','documents','requirement','other')),
    repo_path     TEXT NOT NULL,
    default_ref   TEXT NOT NULL DEFAULT '',
    include_globs TEXT NOT NULL DEFAULT '[]',
    exclude_globs TEXT NOT NULL DEFAULT '[]',
    usages_json   TEXT NOT NULL DEFAULT '[]',
    enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    version       INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at    DATETIME NOT NULL,
    updated_at    DATETIME NOT NULL,
    UNIQUE (library_id, name),
    CHECK (length(trim(repo_path)) > 0)
);

INSERT INTO knowledge_library_sources
    (id, library_id, name, kind, repo_path, default_ref, include_globs, exclude_globs,
     usages_json, enabled, version, created_at, updated_at)
SELECT id, library_id, name, kind, repo_path, default_ref, include_globs, exclude_globs,
       usages_json, enabled, version, created_at, updated_at
FROM knowledge_library_sources_carry;

DROP TABLE knowledge_library_sources_carry;

COMMIT;
PRAGMA foreign_keys = ON;
