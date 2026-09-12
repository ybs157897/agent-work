-- 0064_knowledge_representation_origin.sql — a representation of a frozen
-- requirement input is neither a committed blob nor a worktree file. Recording
-- it as 'committed' would state that the bytes came from a commit, which is
-- exactly the confusion the evidence panel must not create.
-- migrate:no-transaction
PRAGMA foreign_keys = OFF;
BEGIN;

CREATE TABLE knowledge_representations_carry (
    id             TEXT PRIMARY KEY,
    binding_id     TEXT NOT NULL,
    path           TEXT NOT NULL,
    media_type     TEXT NOT NULL DEFAULT 'text/plain',
    encoding       TEXT NOT NULL DEFAULT 'utf-8',
    digest_algo    TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    byte_size      INTEGER NOT NULL,
    coverage       TEXT NOT NULL DEFAULT 'full',
    transform      TEXT NOT NULL DEFAULT '',
    stored_path    TEXT NOT NULL,
    origin         TEXT NOT NULL,
    created_at     DATETIME NOT NULL
);

INSERT INTO knowledge_representations_carry
    (id, binding_id, path, media_type, encoding, digest_algo, content_digest, byte_size,
     coverage, transform, stored_path, origin, created_at)
SELECT id, binding_id, path, media_type, encoding, digest_algo, content_digest, byte_size,
       coverage, transform, stored_path, origin, created_at
FROM knowledge_representations;

DROP TABLE knowledge_representations;

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
    origin         TEXT NOT NULL CHECK (origin IN ('committed','worktree','frozen_requirement')),
    created_at     DATETIME NOT NULL,
    UNIQUE (binding_id, path)
);

INSERT INTO knowledge_representations
    (id, binding_id, path, media_type, encoding, digest_algo, content_digest, byte_size,
     coverage, transform, stored_path, origin, created_at)
SELECT id, binding_id, path, media_type, encoding, digest_algo, content_digest, byte_size,
       coverage, transform, stored_path, origin, created_at
FROM knowledge_representations_carry;

DROP TABLE knowledge_representations_carry;

COMMIT;
PRAGMA foreign_keys = ON;
