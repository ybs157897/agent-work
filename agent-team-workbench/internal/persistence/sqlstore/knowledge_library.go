package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// LibraryRepo is the SQLite persistence for the unified knowledge library.
// It owns the FIFO queue invariants and the publish transaction; it never
// interprets Markdown itself.
type LibraryRepo struct{ store *Store }

var _ application.KnowledgeLibraryRepo = (*LibraryRepo)(nil)

func (r *LibraryRepo) db(ctx context.Context) executor { return r.store.exec(ctx) }

func (s *Store) Library() application.KnowledgeLibraryRepo { return s.library }

// ── Library root ───────────────────────────────────────────────────────

const libraryCols = `id, workspace_id, root_path, librarian_agent_id, enabled,
	current_release_id, current_snapshot_id, index_revision, version, created_at, updated_at`

func scanLibrary(row interface{ Scan(...any) error }) (*domain.KnowledgeLibrary, error) {
	var lib domain.KnowledgeLibrary
	var librarian, currentRelease, currentSnapshot *string
	var enabled int
	var created, updated scanTime
	if err := row.Scan(&lib.ID, &lib.WorkspaceID, &lib.RootPath, &librarian, &enabled,
		&currentRelease, &currentSnapshot, &lib.IndexRevision, &lib.Version, &created, &updated); err != nil {
		return nil, err
	}
	lib.LibrarianAgentID = deref(librarian)
	lib.CurrentReleaseID = deref(currentRelease)
	lib.CurrentSnapshotID = deref(currentSnapshot)
	lib.Enabled = enabled == 1
	lib.CreatedAt, lib.UpdatedAt = created.T, updated.T
	return &lib, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (r *LibraryRepo) GetLibrary(ctx context.Context, workspaceID string) (*domain.KnowledgeLibrary, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT `+libraryCols+` FROM knowledge_libraries WHERE workspace_id=?`, workspaceID)
	lib, err := scanLibrary(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return lib, err
}

func (r *LibraryRepo) GetLibraryByID(ctx context.Context, libraryID string) (*domain.KnowledgeLibrary, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT `+libraryCols+` FROM knowledge_libraries WHERE id=?`, libraryID)
	lib, err := scanLibrary(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return lib, err
}

func (r *LibraryRepo) ListLibraries(ctx context.Context) ([]*domain.KnowledgeLibrary, error) {
	rows, err := r.db(ctx).QueryContext(ctx, `SELECT `+libraryCols+` FROM knowledge_libraries ORDER BY workspace_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.KnowledgeLibrary
	for rows.Next() {
		lib, err := scanLibrary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, lib)
	}
	return out, rows.Err()
}

func (r *LibraryRepo) CreateLibrary(ctx context.Context, lib *domain.KnowledgeLibrary) error {
	_, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_libraries
		(id, workspace_id, root_path, librarian_agent_id, enabled, current_release_id,
		 current_snapshot_id, index_revision, version, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		lib.ID, lib.WorkspaceID, lib.RootPath, nullString(lib.LibrarianAgentID), boolInt(lib.Enabled),
		nullString(lib.CurrentReleaseID), nullString(lib.CurrentSnapshotID), lib.IndexRevision,
		lib.Version, lib.CreatedAt, lib.UpdatedAt)
	return err
}

func (r *LibraryRepo) UpdateLibraryRoot(ctx context.Context, libraryID, rootPath, librarianAgentID string) error {
	_, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_libraries
		SET root_path=?, librarian_agent_id=?, version=version+1, updated_at=?
		WHERE id=?`, rootPath, nullString(librarianAgentID), timeNow(), libraryID)
	return err
}

// ── Sources ────────────────────────────────────────────────────────────

const sourceCols = `id, library_id, name, kind, repo_path, default_ref, include_globs,
	exclude_globs, usages_json, enabled, version, created_at, updated_at`

func scanSource(row interface{ Scan(...any) error }) (*domain.KnowledgeSource, error) {
	var s domain.KnowledgeSource
	var include, exclude, usages string
	var enabled int
	var created, updated scanTime
	if err := row.Scan(&s.ID, &s.LibraryID, &s.Name, &s.Kind, &s.RepoPath, &s.DefaultRef,
		&include, &exclude, &usages, &enabled, &s.Version, &created, &updated); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(include), &s.IncludeGlobs)
	_ = json.Unmarshal([]byte(exclude), &s.ExcludeGlobs)
	_ = json.Unmarshal([]byte(usages), &s.Usages)
	s.Enabled = enabled == 1
	s.CreatedAt, s.UpdatedAt = created.T, updated.T
	return &s, nil
}

func (r *LibraryRepo) ListSources(ctx context.Context, libraryID string) ([]*domain.KnowledgeSource, error) {
	rows, err := r.db(ctx).QueryContext(ctx, `SELECT `+sourceCols+` FROM knowledge_library_sources
		WHERE library_id=? ORDER BY name`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.KnowledgeSource
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *LibraryRepo) GetSource(ctx context.Context, libraryID, sourceID string) (*domain.KnowledgeSource, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT `+sourceCols+` FROM knowledge_library_sources
		WHERE library_id=? AND id=?`, libraryID, sourceID)
	s, err := scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return s, err
}

func (r *LibraryRepo) CreateSource(ctx context.Context, s *domain.KnowledgeSource) error {
	_, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_library_sources
		(id, library_id, name, kind, repo_path, default_ref, include_globs, exclude_globs,
		 usages_json, enabled, version, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.LibraryID, s.Name, string(s.Kind), s.RepoPath, s.DefaultRef,
		jsonText(s.IncludeGlobs), jsonText(s.ExcludeGlobs), jsonText(s.Usages),
		boolInt(s.Enabled), s.Version, s.CreatedAt, s.UpdatedAt)
	return err
}

func (r *LibraryRepo) UpdateSource(ctx context.Context, s *domain.KnowledgeSource, expectedVersion int) error {
	res, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_library_sources
		SET name=?, kind=?, repo_path=?, default_ref=?, include_globs=?, exclude_globs=?,
		    usages_json=?, enabled=?, version=version+1, updated_at=?
		WHERE id=? AND library_id=? AND version=?`,
		s.Name, string(s.Kind), s.RepoPath, s.DefaultRef, jsonText(s.IncludeGlobs), jsonText(s.ExcludeGlobs),
		jsonText(s.Usages), boolInt(s.Enabled), timeNow(), s.ID, s.LibraryID, expectedVersion)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: source %s version conflict", domain.ErrStateConflict, s.ID)
	}
	return nil
}

// DeleteSource retires a source from future snapshots. Historical bindings and
// evidence keep naming it, so the ledger stays explainable.
func (r *LibraryRepo) DeleteSource(ctx context.Context, libraryID, sourceID string) error {
	res, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_library_sources
		SET enabled=0, version=version+1, updated_at=? WHERE id=? AND library_id=?`,
		timeNow(), sourceID, libraryID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ── Snapshots ──────────────────────────────────────────────────────────

func (r *LibraryRepo) CreateSnapshot(ctx context.Context, snap *domain.KnowledgeSnapshot) error {
	if _, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_snapshots
		(id, library_id, view_id, parent_snapshot_id, reason, captured_at)
		VALUES (?,?,?,?,?,?)`,
		snap.ID, snap.LibraryID, snap.ViewID, nullString(snap.ParentSnapshotID), snap.Reason, snap.CapturedAt); err != nil {
		return err
	}
	for i := range snap.Bindings {
		b := &snap.Bindings[i]
		if _, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_snapshot_bindings
			(id, snapshot_id, source_id, source_name, source_kind, repo_path, git_ref, commit_sha,
			 object_format, dirty, dirty_digest, untracked_json, artifact, artifact_resolution,
		 artifact_resolution_ref, consumer, environment, captured_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			b.ID, b.SnapshotID, b.SourceID, b.SourceName, b.SourceKind, b.RepoPath, b.GitRef, b.CommitSHA,
			b.ObjectFormat, boolInt(b.Dirty), b.DirtyDigest, jsonText(b.Untracked), b.Artifact,
			normalizeArtifactResolution(b.ArtifactResolution), b.ArtifactResolutionRef, b.Consumer,
			b.Environment, b.CapturedAt); err != nil {
			return err
		}
	}
	return nil
}

func (r *LibraryRepo) GetSnapshot(ctx context.Context, snapshotID string) (*domain.KnowledgeSnapshot, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT id, library_id, view_id, parent_snapshot_id, reason, captured_at
		FROM knowledge_snapshots WHERE id=?`, snapshotID)
	var snap domain.KnowledgeSnapshot
	var parent sql.NullString
	var captured scanTime
	if err := row.Scan(&snap.ID, &snap.LibraryID, &snap.ViewID, &parent, &snap.Reason, &captured); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	snap.ParentSnapshotID = parent.String
	snap.CapturedAt = captured.T
	rows, err := r.db(ctx).QueryContext(ctx, `SELECT id, snapshot_id, source_id, source_name, source_kind,
		repo_path, git_ref, commit_sha, object_format, dirty, dirty_digest, untracked_json, artifact,
		artifact_resolution, artifact_resolution_ref, consumer, environment, captured_at
		FROM knowledge_snapshot_bindings WHERE snapshot_id=? ORDER BY source_name, consumer, artifact, environment`, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b domain.KnowledgeSnapshotBinding
		var dirty int
		var untracked string
		var at scanTime
		if err := rows.Scan(&b.ID, &b.SnapshotID, &b.SourceID, &b.SourceName, &b.SourceKind,
			&b.RepoPath, &b.GitRef, &b.CommitSHA, &b.ObjectFormat, &dirty, &b.DirtyDigest,
			&untracked, &b.Artifact, &b.ArtifactResolution, &b.ArtifactResolutionRef, &b.Consumer, &b.Environment, &at); err != nil {
			return nil, err
		}
		b.Dirty = dirty == 1
		b.CapturedAt = at.T
		_ = json.Unmarshal([]byte(untracked), &b.Untracked)
		snap.Bindings = append(snap.Bindings, b)
	}
	return &snap, rows.Err()
}

// ── Evidence ───────────────────────────────────────────────────────────

// EnsureRepresentation inserts a frozen representation or returns the
// already-frozen one for the same (binding, path). Reuse is what keeps a
// snapshot immutable even after the worktree moves on.
func (r *LibraryRepo) EnsureRepresentation(ctx context.Context, rep *domain.KnowledgeRepresentation) (*domain.KnowledgeRepresentation, error) {
	existing, err := r.scanRepresentation(ctx, rep.BindingID, rep.Path)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	if _, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_representations
		(id, binding_id, path, media_type, encoding, digest_algo, content_digest, byte_size,
		 coverage, transform, stored_path, origin, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		rep.ID, rep.BindingID, rep.Path, rep.MediaType, rep.Encoding, rep.DigestAlgo, rep.ContentDigest,
		rep.ByteSize, rep.Coverage, rep.Transform, rep.StoredPath, rep.Origin, rep.CreatedAt); err != nil {
		return nil, err
	}
	return rep, nil
}

func (r *LibraryRepo) scanRepresentation(ctx context.Context, bindingID, path string) (*domain.KnowledgeRepresentation, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT id, binding_id, path, media_type, encoding, digest_algo,
		content_digest, byte_size, coverage, transform, stored_path, origin, created_at
		FROM knowledge_representations WHERE binding_id=? AND path=?`, bindingID, path)
	var rep domain.KnowledgeRepresentation
	var created scanTime
	if err := row.Scan(&rep.ID, &rep.BindingID, &rep.Path, &rep.MediaType, &rep.Encoding, &rep.DigestAlgo,
		&rep.ContentDigest, &rep.ByteSize, &rep.Coverage, &rep.Transform, &rep.StoredPath, &rep.Origin, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	rep.CreatedAt = created.T
	return &rep, nil
}

func (r *LibraryRepo) InsertEvidence(ctx context.Context, e *domain.KnowledgeEvidence) error {
	_, err := r.db(ctx).ExecContext(ctx, `INSERT OR IGNORE INTO knowledge_evidence
		(id, library_id, snapshot_id, binding_id, representation_id, locator_json, locator_kind,
		 excerpt, excerpt_digest, match_count, availability, collected_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.LibraryID, e.SnapshotID, e.BindingID, e.RepresentationID, e.LocatorJSON, e.LocatorKind,
		e.Excerpt, e.ExcerptDigest, e.MatchCount, e.Availability, e.CollectedAt)
	return err
}

const evidenceCols = `id, library_id, snapshot_id, binding_id, representation_id, locator_json,
	locator_kind, excerpt, excerpt_digest, match_count, availability, collected_at`

func scanEvidence(row interface{ Scan(...any) error }) (*domain.KnowledgeEvidence, error) {
	var e domain.KnowledgeEvidence
	var at scanTime
	if err := row.Scan(&e.ID, &e.LibraryID, &e.SnapshotID, &e.BindingID, &e.RepresentationID,
		&e.LocatorJSON, &e.LocatorKind, &e.Excerpt, &e.ExcerptDigest, &e.MatchCount, &e.Availability, &at); err != nil {
		return nil, err
	}
	e.CollectedAt = at.T
	return &e, nil
}

func (r *LibraryRepo) GetEvidence(ctx context.Context, libraryID, evidenceID string) (*domain.KnowledgeEvidence, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT `+evidenceCols+` FROM knowledge_evidence
		WHERE library_id=? AND id=?`, libraryID, evidenceID)
	e, err := scanEvidence(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return e, err
}

func (r *LibraryRepo) ListEvidenceByIDs(ctx context.Context, libraryID string, ids []string) ([]*domain.KnowledgeEvidence, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q := `SELECT ` + evidenceCols + ` FROM knowledge_evidence WHERE library_id=? AND id IN (` + placeholders(len(ids)) + `)`
	args := []any{libraryID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := r.db(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.KnowledgeEvidence
	for rows.Next() {
		e, err := scanEvidence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EvidenceExists reports which of the given canonical evidence IDs are known.
func (r *LibraryRepo) EvidenceExists(ctx context.Context, libraryID string, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	q := `SELECT id FROM knowledge_evidence WHERE library_id=? AND id IN (` + placeholders(len(ids)) + `)`
	args := []any{libraryID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := r.db(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// GetRepresentation returns one frozen representation by ID.
func (r *LibraryRepo) GetRepresentation(ctx context.Context, representationID string) (*domain.KnowledgeRepresentation, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT id, binding_id, path, media_type, encoding, digest_algo,
		content_digest, byte_size, coverage, transform, stored_path, origin, created_at
		FROM knowledge_representations WHERE id=?`, representationID)
	var rep domain.KnowledgeRepresentation
	var created scanTime
	if err := row.Scan(&rep.ID, &rep.BindingID, &rep.Path, &rep.MediaType, &rep.Encoding, &rep.DigestAlgo,
		&rep.ContentDigest, &rep.ByteSize, &rep.Coverage, &rep.Transform, &rep.StoredPath, &rep.Origin, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	rep.CreatedAt = created.T
	return &rep, nil
}

func (r *LibraryRepo) GetBinding(ctx context.Context, bindingID string) (*domain.KnowledgeSnapshotBinding, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT id, snapshot_id, source_id, source_name, source_kind,
		repo_path, git_ref, commit_sha, object_format, dirty, dirty_digest, untracked_json, artifact,
		artifact_resolution, artifact_resolution_ref, consumer, environment, captured_at
		FROM knowledge_snapshot_bindings WHERE id=?`, bindingID)
	var b domain.KnowledgeSnapshotBinding
	var dirty int
	var untracked string
	var at scanTime
	if err := row.Scan(&b.ID, &b.SnapshotID, &b.SourceID, &b.SourceName, &b.SourceKind, &b.RepoPath,
		&b.GitRef, &b.CommitSHA, &b.ObjectFormat, &dirty, &b.DirtyDigest, &untracked,
		&b.Artifact, &b.ArtifactResolution, &b.ArtifactResolutionRef, &b.Consumer, &b.Environment, &at); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	b.Dirty = dirty == 1
	b.CapturedAt = at.T
	_ = json.Unmarshal([]byte(untracked), &b.Untracked)
	return &b, nil
}

// ── Entities and documents ─────────────────────────────────────────────

func (r *LibraryRepo) ListEntities(ctx context.Context, libraryID string) ([]*domain.KnowledgeEntity, error) {
	rows, err := r.db(ctx).QueryContext(ctx, `SELECT id, library_id, kind, namespace, canonical_name,
		aliases_json, source_id, primary_document_id, version, created_at, updated_at
		FROM knowledge_entities WHERE library_id=? ORDER BY kind, canonical_name`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.KnowledgeEntity
	for rows.Next() {
		var e domain.KnowledgeEntity
		var aliases string
		var sourceID, primaryDoc *string
		var created, updated scanTime
		if err := rows.Scan(&e.ID, &e.LibraryID, &e.Kind, &e.Namespace, &e.CanonicalName, &aliases,
			&sourceID, &primaryDoc, &e.Version, &created, &updated); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(aliases), &e.Aliases)
		e.SourceID = deref(sourceID)
		e.PrimaryDocumentID = deref(primaryDoc)
		e.CreatedAt, e.UpdatedAt = created.T, updated.T
		out = append(out, &e)
	}
	return out, rows.Err()
}

// UpsertEntity inserts or refreshes one entity identity.
func (r *LibraryRepo) UpsertEntity(ctx context.Context, e *domain.KnowledgeEntity) error {
	_, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_entities
		(id, library_id, kind, namespace, canonical_name, aliases_json, source_id, primary_document_id, version, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
		  kind=excluded.kind, namespace=excluded.namespace, canonical_name=excluded.canonical_name,
		  aliases_json=excluded.aliases_json,
		  source_id=COALESCE(excluded.source_id, knowledge_entities.source_id),
		  primary_document_id=COALESCE(excluded.primary_document_id, knowledge_entities.primary_document_id),
		  version=knowledge_entities.version+1, updated_at=excluded.updated_at`,
		e.ID, e.LibraryID, e.Kind, e.Namespace, e.CanonicalName, jsonText(e.Aliases),
		nullString(e.SourceID), nullString(e.PrimaryDocumentID), e.Version, e.CreatedAt, e.UpdatedAt)
	return err
}

func (r *LibraryRepo) ListDocuments(ctx context.Context, libraryID string) ([]*domain.KnowledgeDocument, error) {
	rows, err := r.db(ctx).QueryContext(ctx, `SELECT id, library_id, path, kind, title, summary, domains_json,
		status, renamed_from, rename_note, current_version, created_at, updated_at
		FROM knowledge_documents WHERE library_id=? ORDER BY path`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.KnowledgeDocument
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func scanDocument(row interface{ Scan(...any) error }) (*domain.KnowledgeDocument, error) {
	var d domain.KnowledgeDocument
	var domains string
	var created, updated scanTime
	if err := row.Scan(&d.ID, &d.LibraryID, &d.Path, &d.Kind, &d.Title, &d.Summary, &domains,
		&d.Status, &d.RenamedFrom, &d.RenameNote, &d.CurrentVersion, &created, &updated); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(domains), &d.Domains)
	d.CreatedAt, d.UpdatedAt = created.T, updated.T
	return &d, nil
}

func (r *LibraryRepo) GetDocument(ctx context.Context, libraryID, documentID string) (*domain.KnowledgeDocument, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT id, library_id, path, kind, title, summary, domains_json,
		status, renamed_from, rename_note, current_version, created_at, updated_at
		FROM knowledge_documents WHERE library_id=? AND id=?`, libraryID, documentID)
	d, err := scanDocument(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return d, err
}

// ── Releases ───────────────────────────────────────────────────────────

const releaseCols = `id, library_id, seq, snapshot_id, task_id, parent_release_id, projection_digest,
	document_count, assertion_count, relation_count, evidence_count, coverage_json, notes, status, published_at`

func scanRelease(row interface{ Scan(...any) error }) (*domain.KnowledgeRelease, error) {
	var rel domain.KnowledgeRelease
	var taskID, parent *string
	var published scanTime
	if err := row.Scan(&rel.ID, &rel.LibraryID, &rel.Seq, &rel.SnapshotID, &taskID, &parent,
		&rel.ProjectionDigest, &rel.DocumentCount, &rel.AssertionCount, &rel.RelationCount,
		&rel.EvidenceCount, &rel.CoverageJSON, &rel.Notes, &rel.Status, &published); err != nil {
		return nil, err
	}
	rel.TaskID = deref(taskID)
	rel.ParentReleaseID = deref(parent)
	rel.PublishedAt = published.T
	return &rel, nil
}

func (r *LibraryRepo) GetRelease(ctx context.Context, libraryID, releaseID string) (*domain.KnowledgeRelease, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT `+releaseCols+` FROM knowledge_releases
		WHERE library_id=? AND id=?`, libraryID, releaseID)
	rel, err := scanRelease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return rel, err
}

func (r *LibraryRepo) CurrentRelease(ctx context.Context, libraryID string) (*domain.KnowledgeRelease, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT `+releaseCols+` FROM knowledge_releases
		WHERE library_id=? ORDER BY seq DESC LIMIT 1`, libraryID)
	rel, err := scanRelease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return rel, err
}

func (r *LibraryRepo) ListReleases(ctx context.Context, libraryID string, limit int) ([]*domain.KnowledgeRelease, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db(ctx).QueryContext(ctx, `SELECT `+releaseCols+` FROM knowledge_releases
		WHERE library_id=? ORDER BY seq DESC LIMIT ?`, libraryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.KnowledgeRelease
	for rows.Next() {
		rel, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	return out, rows.Err()
}

// ── small helpers ──────────────────────────────────────────────────────

// normalizeArtifactResolution keeps the closed vocabulary even for callers
// that pass an empty value.
func normalizeArtifactResolution(value string) string {
	switch value {
	case "resolved", "unknown", "declared":
		return value
	default:
		return "declared"
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
