package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ── Events ─────────────────────────────────────────────────────────────

const eventCols = `id, library_id, protocol_version, event_type, source, subject_json, content_ref,
	payload_json, client_key, request_digest, status, task_id, received_at, updated_at`

func scanEvent(row interface{ Scan(...any) error }) (*domain.KnowledgeLibraryEvent, error) {
	var e domain.KnowledgeLibraryEvent
	var taskID *string
	var received, updated scanTime
	if err := row.Scan(&e.ID, &e.LibraryID, &e.ProtocolVersion, &e.EventType, &e.Source, &e.SubjectJSON,
		&e.ContentRef, &e.PayloadJSON, &e.ClientKey, &e.RequestDigest, &e.Status, &taskID, &received, &updated); err != nil {
		return nil, err
	}
	e.TaskID = deref(taskID)
	e.ReceivedAt, e.UpdatedAt = received.T, updated.T
	return &e, nil
}

// InsertEvent persists an event report. It returns false with no error when
// the same client_key was already accepted, so a retried delivery returns the
// original receipt instead of creating a second task.
func (r *LibraryRepo) InsertEvent(ctx context.Context, e *domain.KnowledgeLibraryEvent) (bool, error) {
	res, err := r.db(ctx).ExecContext(ctx, `INSERT OR IGNORE INTO knowledge_library_events
		(id, library_id, protocol_version, event_type, source, subject_json, content_ref,
		 payload_json, client_key, request_digest, status, task_id, received_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.LibraryID, e.ProtocolVersion, e.EventType, e.Source, e.SubjectJSON, e.ContentRef,
		e.PayloadJSON, e.ClientKey, e.RequestDigest, e.Status, nullString(e.TaskID), e.ReceivedAt, e.UpdatedAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (r *LibraryRepo) GetEventByClientKey(ctx context.Context, libraryID, clientKey string) (*domain.KnowledgeLibraryEvent, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT `+eventCols+` FROM knowledge_library_events
		WHERE library_id=? AND client_key=?`, libraryID, clientKey)
	e, err := scanEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return e, err
}

// GetEvent reads one event row. The worker re-reads the event at head time so
// the declared requirement text and the payload reach the agent from the
// durable record rather than from whatever the caller happened to pass.
func (r *LibraryRepo) GetEvent(ctx context.Context, libraryID, eventID string) (*domain.KnowledgeLibraryEvent, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT `+eventCols+` FROM knowledge_library_events
		WHERE library_id=? AND id=?`, libraryID, eventID)
	e, err := scanEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return e, err
}

func (r *LibraryRepo) ListEvents(ctx context.Context, libraryID, status string, limit int) ([]*domain.KnowledgeLibraryEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT ` + eventCols + ` FROM knowledge_library_events WHERE library_id=?`
	args := []any{libraryID}
	if strings.TrimSpace(status) != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY received_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.db(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.KnowledgeLibraryEvent
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *LibraryRepo) UpdateEventStatus(ctx context.Context, eventID string, status domain.KnowledgeEventStatus, taskID string) error {
	_, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_library_events
		SET status=?, task_id=COALESCE(NULLIF(?,''), task_id), updated_at=? WHERE id=?`,
		status, taskID, timeNow(), eventID)
	return err
}

// ── FIFO write queue ───────────────────────────────────────────────────

const taskCols = `id, library_id, seq, kind, event_id, status, base_release_id, target_release_id,
	snapshot_id, view_id, focus_json, plan_json, coverage_json, staging_path, work_item_id,
	current_run_id, attempt, repair_attempt, max_attempts, max_repair_attempts, turn_seq,
	next_attempt_at, owner_token, last_error, blocked_reason, diagnostics_json,
	created_at, updated_at, finished_at`

func scanTask(row interface{ Scan(...any) error }) (*domain.KnowledgeWriteTask, error) {
	var t domain.KnowledgeWriteTask
	var eventID, baseRelease, targetRelease, snapshotID, workItem, currentRun, nextAttempt, finished *string
	var plan, coverage, diagnostics string
	var created, updated scanTime
	var nextAttemptAt, finishedAt scanTime
	if err := row.Scan(&t.ID, &t.LibraryID, &t.Seq, &t.Kind, &eventID, &t.Status, &baseRelease, &targetRelease,
		&snapshotID, &t.ViewID, &t.FocusJSON, &plan, &coverage, &t.StagingPath, &workItem, &currentRun,
		&t.Attempt, &t.RepairAttempt, &t.MaxAttempts, &t.MaxRepairAttempts, &t.TurnSeq,
		&nextAttemptAt, &t.OwnerToken, &t.LastError, &t.BlockedReason, &diagnostics, &created, &updated, &finishedAt); err != nil {
		return nil, err
	}
	_ = nextAttempt
	_ = finished
	t.EventID, t.BaseReleaseID, t.TargetReleaseID = deref(eventID), deref(baseRelease), deref(targetRelease)
	t.SnapshotID, t.WorkItemID, t.CurrentRunID = deref(snapshotID), deref(workItem), deref(currentRun)
	t.PlanJSON, t.CoverageJSON, t.DiagnosticsJSON = plan, coverage, diagnostics
	t.CreatedAt, t.UpdatedAt = created.T, updated.T
	t.NextAttemptAt = optTime(nextAttemptAt)
	t.FinishedAt = optTime(finishedAt)
	return &t, nil
}

// CreateTaskWithEvent inserts one queued task and points its event at it in
// the same transaction, so an accepted event always has exactly one task.
// CreateTask enqueues a task that has no external event behind it, such as an
// administrator-requested reindex. It is the same FIFO insert as
// CreateTaskWithEvent, minus the event bookkeeping.
func (r *LibraryRepo) CreateTask(ctx context.Context, t *domain.KnowledgeWriteTask) error {
	return r.insertTask(ctx, t)
}

func (r *LibraryRepo) CreateTaskWithEvent(ctx context.Context, t *domain.KnowledgeWriteTask) error {
	if err := r.insertTask(ctx, t); err != nil {
		return err
	}
	if t.EventID != "" {
		if _, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_library_events
			SET status=?, task_id=?, updated_at=? WHERE id=?`,
			domain.KnowledgeEventQueued, t.ID, timeNow(), t.EventID); err != nil {
			return err
		}
	}
	return nil
}

func (r *LibraryRepo) insertTask(ctx context.Context, t *domain.KnowledgeWriteTask) error {
	if _, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_write_tasks
		(id, library_id, seq, kind, event_id, status, base_release_id, target_release_id, snapshot_id,
		 view_id, focus_json, plan_json, coverage_json, staging_path, work_item_id, current_run_id,
		 attempt, repair_attempt, max_attempts, max_repair_attempts, turn_seq, next_attempt_at,
		 owner_token, last_error, blocked_reason, diagnostics_json, created_at, updated_at, finished_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.LibraryID, t.Seq, t.Kind, nullString(t.EventID), t.Status,
		nullString(t.BaseReleaseID), nullString(t.TargetReleaseID), nullString(t.SnapshotID),
		t.ViewID, t.FocusJSON, t.PlanJSON, t.CoverageJSON, t.StagingPath,
		nullString(t.WorkItemID), nullString(t.CurrentRunID), t.Attempt, t.RepairAttempt,
		t.MaxAttempts, t.MaxRepairAttempts, t.TurnSeq, t.NextAttemptAt,
		t.OwnerToken, t.LastError, t.BlockedReason, t.DiagnosticsJSON, t.CreatedAt, t.UpdatedAt, t.FinishedAt); err != nil {
		return err
	}
	return nil
}

// HeadTask returns the oldest task that still occupies the queue head.
func (r *LibraryRepo) HeadTask(ctx context.Context, libraryID string) (*domain.KnowledgeWriteTask, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT `+taskCols+` FROM knowledge_write_tasks
		WHERE library_id=? AND status IN ('queued','running','awaiting_agent','retry_wait','blocked')
		ORDER BY seq ASC LIMIT 1`, libraryID)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return t, err
}

// TaskByWorkItem resolves the library task that owns a WorkItem.
func (r *LibraryRepo) TaskByWorkItem(ctx context.Context, workItemID string) (*domain.KnowledgeWriteTask, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT `+taskCols+` FROM knowledge_write_tasks WHERE work_item_id=?`, workItemID)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return t, err
}

func (r *LibraryRepo) GetTask(ctx context.Context, libraryID, taskID string) (*domain.KnowledgeWriteTask, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT `+taskCols+` FROM knowledge_write_tasks
		WHERE library_id=? AND id=?`, libraryID, taskID)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return t, err
}

func (r *LibraryRepo) ListTasks(ctx context.Context, libraryID, status string, limit int) ([]*domain.KnowledgeWriteTask, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT ` + taskCols + ` FROM knowledge_write_tasks WHERE library_id=?`
	args := []any{libraryID}
	if strings.TrimSpace(status) != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.db(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.KnowledgeWriteTask
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ClaimTask moves the head task to 'running'. The partial unique index on
// (library_id) WHERE status='running' is the actual single-writer guarantee:
// a second concurrent claim fails at the database, not at a convention.
func (r *LibraryRepo) ClaimTask(ctx context.Context, libraryID, taskID, ownerToken string) (bool, error) {
	res, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_write_tasks
		SET status='running', owner_token=?, updated_at=?, finished_at=NULL
		WHERE id=? AND library_id=? AND status IN ('queued','running','retry_wait','awaiting_agent')
		  AND seq = (SELECT MIN(seq) FROM knowledge_write_tasks
		             WHERE library_id=? AND status IN ('queued','running','awaiting_agent','retry_wait','blocked'))`,
		ownerToken, timeNow(), taskID, libraryID, libraryID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (r *LibraryRepo) UpdateTask(ctx context.Context, t *domain.KnowledgeWriteTask) error {
	_, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_write_tasks SET
		status=?, base_release_id=?, target_release_id=?, snapshot_id=?, focus_json=?, plan_json=?,
		coverage_json=?, staging_path=?, work_item_id=?, current_run_id=?, attempt=?, repair_attempt=?,
		max_attempts=?, max_repair_attempts=?, turn_seq=?, next_attempt_at=?, owner_token=?,
		last_error=?, blocked_reason=?, diagnostics_json=?, updated_at=?, finished_at=?
		WHERE id=? AND library_id=?`,
		t.Status, nullString(t.BaseReleaseID), nullString(t.TargetReleaseID), nullString(t.SnapshotID),
		t.FocusJSON, t.PlanJSON, t.CoverageJSON, t.StagingPath, nullString(t.WorkItemID),
		nullString(t.CurrentRunID), t.Attempt, t.RepairAttempt, t.MaxAttempts, t.MaxRepairAttempts,
		t.TurnSeq, t.NextAttemptAt, t.OwnerToken, t.LastError, t.BlockedReason, t.DiagnosticsJSON,
		t.UpdatedAt, t.FinishedAt, t.ID, t.LibraryID)
	return err
}

// CompleteHeadTask finishes a task and claims the single-writer slot in one
// statement, so the next task can never be started by a lost update.
func (r *LibraryRepo) CompleteHeadTask(ctx context.Context, taskID string, status domain.KnowledgeTaskStatus, lastErr, blocked string, targetReleaseID string) error {
	res, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_write_tasks
		SET status=?, last_error=?, blocked_reason=?, target_release_id=COALESCE(NULLIF(?,''), target_release_id),
		    owner_token='', updated_at=?, finished_at=?
		WHERE id=? AND status='running'`,
		status, lastErr, blocked, targetReleaseID, timeNow(), timeNow(), taskID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: task %s is not the running task", domain.ErrStateConflict, taskID)
	}
	return nil
}

func (r *LibraryRepo) CountTasks(ctx context.Context, libraryID string) (int, int, error) {
	var pending, blocked int
	err := r.db(ctx).QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN status IN ('queued','running','awaiting_agent','retry_wait') THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='blocked' THEN 1 ELSE 0 END),0)
		FROM knowledge_write_tasks WHERE library_id=?`, libraryID).Scan(&pending, &blocked)
	return pending, blocked, err
}

// ── Turns ──────────────────────────────────────────────────────────────

const turnCols = `id, task_id, turn_seq, run_id, purpose, status, request_json, result_json, error_message, created_at, updated_at`

func scanTurn(row interface{ Scan(...any) error }) (*domain.KnowledgeTaskTurn, error) {
	var t domain.KnowledgeTaskTurn
	var created, updated scanTime
	if err := row.Scan(&t.ID, &t.TaskID, &t.TurnSeq, &t.RunID, &t.Purpose, &t.Status,
		&t.RequestJSON, &t.ResultJSON, &t.ErrorMessage, &created, &updated); err != nil {
		return nil, err
	}
	t.CreatedAt, t.UpdatedAt = created.T, updated.T
	return &t, nil
}

func (r *LibraryRepo) CreateTurn(ctx context.Context, t *domain.KnowledgeTaskTurn) error {
	_, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_task_turns
		(id, task_id, turn_seq, run_id, purpose, status, request_json, result_json, error_message, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.TaskID, t.TurnSeq, t.RunID, t.Purpose, t.Status, t.RequestJSON, t.ResultJSON,
		t.ErrorMessage, t.CreatedAt, t.UpdatedAt)
	return err
}

func (r *LibraryRepo) UpdateTurn(ctx context.Context, turnID, status, resultJSON, errMsg string) error {
	_, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_task_turns
		SET status=?, result_json=?, error_message=?, updated_at=? WHERE id=?`,
		status, resultJSON, errMsg, timeNow(), turnID)
	return err
}

func (r *LibraryRepo) ListTurns(ctx context.Context, taskID string) ([]*domain.KnowledgeTaskTurn, error) {
	rows, err := r.db(ctx).QueryContext(ctx, `SELECT `+turnCols+` FROM knowledge_task_turns
		WHERE task_id=? ORDER BY turn_seq`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.KnowledgeTaskTurn
	for rows.Next() {
		t, err := scanTurn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ── Publications ───────────────────────────────────────────────────────

func (r *LibraryRepo) CreatePublication(ctx context.Context, p *domain.KnowledgePublication) error {
	_, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_publications
		(id, library_id, task_id, release_id, status, projection_digest, prepared_at, committed_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(task_id) DO NOTHING`,
		p.ID, p.LibraryID, p.TaskID, p.ReleaseID, p.Status, p.ProjectionDigest, p.PreparedAt, p.CommittedAt)
	return err
}

// ReleaseByProjectionDigest finds a release by its exact content projection,
// which is how a recovered publish proves it already happened.
func (r *LibraryRepo) ReleaseByProjectionDigest(ctx context.Context, libraryID, digest string) (*domain.KnowledgeRelease, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT `+releaseCols+` FROM knowledge_releases
		WHERE library_id=? AND projection_digest=? ORDER BY seq DESC LIMIT 1`, libraryID, digest)
	rel, err := scanRelease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return rel, err
}

func (r *LibraryRepo) GetPublicationByTask(ctx context.Context, taskID string) (*domain.KnowledgePublication, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT id, library_id, task_id, release_id, status,
		projection_digest, prepared_at, committed_at FROM knowledge_publications WHERE task_id=?`, taskID)
	var p domain.KnowledgePublication
	var prepared scanTime
	var committed scanTime
	if err := row.Scan(&p.ID, &p.LibraryID, &p.TaskID, &p.ReleaseID, &p.Status, &p.ProjectionDigest, &prepared, &committed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	p.PreparedAt = prepared.T
	p.CommittedAt = optTime(committed)
	return &p, nil
}

func (r *LibraryRepo) ListPreparedPublications(ctx context.Context, libraryID string) ([]*domain.KnowledgePublication, error) {
	rows, err := r.db(ctx).QueryContext(ctx, `SELECT id, library_id, task_id, release_id, status,
		projection_digest, prepared_at, committed_at FROM knowledge_publications
		WHERE library_id=? AND status='prepared' ORDER BY prepared_at`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.KnowledgePublication
	for rows.Next() {
		var p domain.KnowledgePublication
		var prepared scanTime
		var committed scanTime
		if err := rows.Scan(&p.ID, &p.LibraryID, &p.TaskID, &p.ReleaseID, &p.Status, &p.ProjectionDigest, &prepared, &committed); err != nil {
			return nil, err
		}
		p.PreparedAt = prepared.T
		p.CommittedAt = optTime(committed)
		out = append(out, &p)
	}
	return out, rows.Err()
}

func (r *LibraryRepo) CommitPublication(ctx context.Context, taskID string) error {
	_, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_publications
		SET status='committed', committed_at=? WHERE task_id=?`, timeNow(), taskID)
	return err
}

func (r *LibraryRepo) AbandonPublication(ctx context.Context, taskID string) error {
	_, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_publications
		SET status='abandoned' WHERE task_id=? AND status='prepared'`, taskID)
	return err
}

// ── Publish ────────────────────────────────────────────────────────────

// PublishProjection writes one release atomically: new document versions,
// materialized assertions and relations, carried-forward documents, the
// release row, the current-release pointer and the search projection all move
// together or not at all.
func (r *LibraryRepo) PublishProjection(ctx context.Context, in application.PublishInput) (*domain.KnowledgeRelease, error) {
	var release *domain.KnowledgeRelease
	err := r.store.InTx(ctx, func(ctx context.Context) error {
		rel, err := r.publishLocked(ctx, in)
		if err != nil {
			return err
		}
		release = rel
		return nil
	})
	if err != nil {
		return nil, err
	}
	return release, nil
}

func (r *LibraryRepo) publishLocked(ctx context.Context, in application.PublishInput) (*domain.KnowledgeRelease, error) {
	var maxSeq int
	if err := r.db(ctx).QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM knowledge_releases WHERE library_id=?`, in.LibraryID).Scan(&maxSeq); err != nil {
		return nil, err
	}
	// The carry-forward baseline is the library's real current release at the
	// moment of publication, never the baseline captured when the task was
	// enqueued. Two increments queued from the same release must each build on
	// the previous one's result, or the later one would silently drop the
	// earlier one's documents.
	var liveReleaseID string
	var liveSnapshotID string
	if err := r.db(ctx).QueryRowContext(ctx, `SELECT COALESCE(current_release_id,''), COALESCE(current_snapshot_id,'')
		FROM knowledge_libraries WHERE id=?`, in.LibraryID).Scan(&liveReleaseID, &liveSnapshotID); err != nil {
		return nil, err
	}
	in.ParentReleaseID = liveReleaseID
	now := timeNow()
	releaseID := domain.NewID("rel_")
	rel := &domain.KnowledgeRelease{
		ID: releaseID, LibraryID: in.LibraryID, Seq: maxSeq + 1, SnapshotID: in.SnapshotID,
		TaskID: in.TaskID, ParentReleaseID: in.ParentReleaseID, ProjectionDigest: in.ProjectionDigest,
		DocumentCount: len(in.Documents), EvidenceCount: len(in.Evidence),
		CoverageJSON: in.CoverageJSON, Notes: in.Notes, Status: "published", PublishedAt: now,
	}
	for _, e := range in.Evidence {
		if _, err := r.db(ctx).ExecContext(ctx, `INSERT OR IGNORE INTO knowledge_evidence
			(id, library_id, snapshot_id, binding_id, representation_id, locator_json, locator_kind,
			 excerpt, excerpt_digest, match_count, availability, collected_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			e.ID, in.LibraryID, e.SnapshotID, e.BindingID, e.RepresentationID, e.LocatorJSON,
			e.LocatorKind, e.Excerpt, e.ExcerptDigest, e.MatchCount, e.Availability, e.CollectedAt); err != nil {
			return nil, err
		}
	}
	// Carry forward every document still present in the parent release.
	carried := map[string]domain.KnowledgeReleaseDocument{}
	if in.ParentReleaseID != "" {
		rows, err := r.db(ctx).QueryContext(ctx, `SELECT release_id, document_id, document_version_id, is_removed
			FROM knowledge_release_documents WHERE release_id=?`, in.ParentReleaseID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var d domain.KnowledgeReleaseDocument
			var removed int
			if err := rows.Scan(&d.ReleaseID, &d.DocumentID, &d.DocumentVersionID, &removed); err != nil {
				rows.Close()
				return nil, err
			}
			d.IsRemoved = removed == 1
			carried[d.DocumentID] = d
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	assertionCount, relationCount := 0, 0
	for _, doc := range in.Documents {
		docID := doc.DocumentID
		var currentVersion int
		err := r.db(ctx).QueryRowContext(ctx, `SELECT current_version FROM knowledge_documents WHERE id=? AND library_id=?`, docID, in.LibraryID).Scan(&currentVersion)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_documents
				(id, library_id, path, kind, title, summary, domains_json, status, renamed_from, rename_note,
				 current_version, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?, 'active', '', '', 0, ?, ?)`,
				docID, in.LibraryID, doc.Path, doc.Kind, doc.Title, doc.Summary, jsonText(doc.Domains), now, now); err != nil {
				return nil, err
			}
			currentVersion = 0
		case err != nil:
			return nil, err
		}
		nextVersion := currentVersion + 1
		versionID := domain.NewID("kdv_")
		aliasJSON := doc.EvidenceAliasJSON
		if aliasJSON == "" {
			aliasJSON = "{}"
		}
		if _, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_document_versions
			(id, document_id, library_id, version, title, path, content_markdown, frontmatter_json,
			 content_digest, snapshot_id, task_id, release_id, evidence_alias_json,
			 derived_from_version_id, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			versionID, docID, in.LibraryID, nextVersion, doc.Title, doc.Path, doc.ContentMarkdown,
			doc.FrontmatterJSON, doc.ContentDigest, nullString(in.SnapshotID), nullString(in.TaskID),
			releaseID, aliasJSON, "", now); err != nil {
			return nil, err
		}
		for i := range doc.Assertions {
			a := &doc.Assertions[i]
			a.RowID = domain.NewID("kba_")
			a.LibraryID = in.LibraryID
			a.DocumentID = docID
			a.DocumentVersionID = versionID
			a.CreatedAt = now
			if _, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_assertions
				(row_id, assertion_id, library_id, document_id, document_version_id, heading, about_json,
				 perspective, basis, statement, scope_json, evidence_json, unknown_notes, ordinal, content_digest, created_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				a.RowID, a.ID, a.LibraryID, a.DocumentID, a.DocumentVersionID, a.Heading, jsonText(a.About),
				a.Perspective, a.Basis, a.Statement, a.ScopeJSON, a.EvidenceJSON, a.UnknownNotes,
				a.Ordinal, a.ContentDigest, a.CreatedAt); err != nil {
				return nil, err
			}
			assertionCount++
		}
		for i := range doc.Relations {
			relRow := &doc.Relations[i]
			relRow.RowID = domain.NewID("kbr_")
			relRow.LibraryID = in.LibraryID
			relRow.DocumentID = docID
			relRow.DocumentVersionID = versionID
			relRow.CreatedAt = now
			if _, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_assertion_relations
				(row_id, relation_id, library_id, document_id, document_version_id, from_kind, from_id,
				 predicate, to_kind, to_id, to_resolved, to_raw, perspective, basis, condition,
				 evidence_json, ordinal, content_digest, created_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				relRow.RowID, relRow.ID, relRow.LibraryID, relRow.DocumentID, relRow.DocumentVersionID,
				relRow.FromKind, relRow.FromID, relRow.Predicate, relRow.ToKind, relRow.ToID,
				boolInt(relRow.ToResolved), relRow.ToRaw, relRow.Perspective, relRow.Basis,
				relRow.Condition, relRow.EvidenceJSON, relRow.Ordinal, relRow.ContentDigest, relRow.CreatedAt); err != nil {
				return nil, err
			}
			relationCount++
		}
		if _, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_documents
			SET path=?, kind=?, title=?, summary=?, domains_json=?, status='active',
			    current_version=?, updated_at=? WHERE id=? AND library_id=?`,
			doc.Path, doc.Kind, doc.Title, doc.Summary, jsonText(doc.Domains), nextVersion, now, docID, in.LibraryID); err != nil {
			return nil, err
		}
		carried[docID] = domain.KnowledgeReleaseDocument{
			ReleaseID: releaseID, DocumentID: docID, DocumentVersionID: versionID,
		}
		if err := r.indexVersion(ctx, in.LibraryID, versionID, docID, doc); err != nil {
			return nil, err
		}
	}
	for _, docID := range in.Removals {
		entry, ok := carried[docID]
		if !ok {
			continue
		}
		entry.IsRemoved = true
		carried[docID] = entry
		if _, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_documents
			SET status='removed', rename_note=?, updated_at=? WHERE id=? AND library_id=?`,
			in.RemovedNotes[docID], now, docID, in.LibraryID); err != nil {
			return nil, err
		}
	}
	for _, rn := range in.Renames {
		if _, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_documents
			SET path=?, status='active', renamed_from=?, rename_note=?, updated_at=?
			WHERE id=? AND library_id=?`,
			rn.ToPath, rn.FromPath, rn.Note, now, rn.DocumentID, in.LibraryID); err != nil {
			return nil, err
		}
	}
	for _, e := range in.Entities {
		if _, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_entities
			(id, library_id, kind, namespace, canonical_name, aliases_json, source_id, primary_document_id, version, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,1,?,?)
			ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, namespace=excluded.namespace,
			  canonical_name=excluded.canonical_name, aliases_json=excluded.aliases_json,
			  source_id=COALESCE(excluded.source_id, knowledge_entities.source_id),
			  version=knowledge_entities.version+1, updated_at=excluded.updated_at`,
			e.ID, in.LibraryID, e.Kind, e.Namespace, e.CanonicalName, jsonText(e.Aliases),
			nullString(e.SourceID), nil, now, now); err != nil {
			return nil, err
		}
	}
	// The release row must exist before its membership rows, because
	// knowledge_release_documents.release_id is a real foreign key.
	rel.DocumentCount = len(carried)
	rel.AssertionCount = assertionCount
	rel.RelationCount = relationCount
	if _, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_releases
		(id, library_id, seq, snapshot_id, task_id, parent_release_id, projection_digest,
		 document_count, assertion_count, relation_count, evidence_count, coverage_json, notes, status, published_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		rel.ID, rel.LibraryID, rel.Seq, rel.SnapshotID, nullString(rel.TaskID), nullString(rel.ParentReleaseID),
		rel.ProjectionDigest, rel.DocumentCount, rel.AssertionCount, rel.RelationCount, rel.EvidenceCount,
		rel.CoverageJSON, rel.Notes, rel.Status, rel.PublishedAt); err != nil {
		return nil, err
	}
	docIDs := make([]string, 0, len(carried))
	for id := range carried {
		docIDs = append(docIDs, id)
	}
	sort.Strings(docIDs)
	for _, id := range docIDs {
		entry := carried[id]
		if _, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_release_documents
			(release_id, document_id, document_version_id, is_removed) VALUES (?,?,?,?)`,
			releaseID, entry.DocumentID, entry.DocumentVersionID, boolInt(entry.IsRemoved)); err != nil {
			return nil, err
		}
	}
	rel.DocumentCount = len(docIDs)
	rel.AssertionCount = assertionCount
	rel.RelationCount = relationCount
	if _, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_releases SET status='superseded'
		WHERE library_id=? AND id<>? AND status='published'`, in.LibraryID, releaseID); err != nil {
		return nil, err
	}
	if _, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_libraries
		SET current_release_id=?, current_snapshot_id=COALESCE(NULLIF(?,''), current_snapshot_id),
		    index_revision=index_revision+1, version=version+1, updated_at=? WHERE id=?`,
		releaseID, in.SnapshotID, now, in.LibraryID); err != nil {
		return nil, err
	}
	// The journal row moves to committed in the same transaction as the
	// release and the current-release pointer: a crash can therefore never
	// leave a published release without a matching completion record.
	if in.TaskID != "" {
		pubID := in.PublicationID
		if pubID == "" {
			pubID = domain.NewID("kpub_")
		}
		if _, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_publications
			(id, library_id, task_id, release_id, status, projection_digest, prepared_at, committed_at)
			VALUES (?,?,?,?, 'committed', ?, ?, ?)
			ON CONFLICT(task_id) DO UPDATE SET
			  release_id=excluded.release_id, status='committed',
			  projection_digest=excluded.projection_digest, committed_at=excluded.committed_at`,
			pubID, in.LibraryID, in.TaskID, releaseID, in.ProjectionDigest, now, now); err != nil {
			return nil, err
		}
	}
	return rel, nil
}

func (r *LibraryRepo) indexVersion(ctx context.Context, libraryID, versionID, documentID string, doc application.PublishDocument) error {
	if _, err := r.db(ctx).ExecContext(ctx, `DELETE FROM knowledge_search_index WHERE document_version_id=?`, versionID); err != nil {
		return err
	}
	about := []string{}
	statements := []string{}
	scopes := []string{}
	for _, a := range doc.Assertions {
		about = append(about, a.About...)
		statements = append(statements, a.Statement)
		scopes = append(scopes, a.ScopeJSON)
	}
	_, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_search_index
		(document_version_id, document_id, library_id, title, summary, body, aliases, tags, scope, entities)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		versionID, documentID, libraryID, doc.Title, doc.Summary,
		doc.ContentMarkdown+"\n"+strings.Join(statements, "\n"),
		"", "", strings.Join(scopes, " "), strings.Join(about, " "))
	return err
}

// ── Reads ──────────────────────────────────────────────────────────────

// SearchRelease answers a query against exactly one published release. It
// never reads the worktree, never reads an unpublished draft and never
// silently falls back to a different release.
func (r *LibraryRepo) SearchRelease(ctx context.Context, libraryID, releaseID string, terms []string, limit int) ([]domain.KnowledgeQueryHit, bool, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	norm := normalizeTerms(terms)
	versions, order, err := r.releaseVersionSet(ctx, releaseID)
	if err != nil {
		return nil, false, 0, err
	}
	if len(versions) == 0 {
		return nil, false, 0, nil
	}
	type scored struct {
		hit   domain.KnowledgeQueryHit
		score float64
	}
	var out []scored
	for _, versionID := range order {
		meta := versions[versionID]
		assertions, err := r.assertionsForVersion(ctx, versionID)
		if err != nil {
			return nil, false, 0, err
		}
		doc, err := r.GetDocument(ctx, libraryID, meta.documentID)
		if err != nil {
			return nil, false, 0, err
		}
		for _, a := range assertions {
			score, snippet := scoreAssertion(a, norm)
			if len(norm) > 0 && score <= 0 {
				continue
			}
			if len(norm) == 0 {
				score = 1
			}
			out = append(out, scored{hit: domain.KnowledgeQueryHit{
				Assertion: a, Document: *doc,
				Score: score, Snippet: snippet,
			}, score: score})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].hit.Assertion.ID < out[j].hit.Assertion.ID
	})
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	hits := make([]domain.KnowledgeQueryHit, 0, len(out))
	for _, s := range out {
		ev, err := r.evidenceForJSON(ctx, libraryID, s.hit.Assertion.EvidenceJSON)
		if err != nil {
			return nil, false, 0, err
		}
		s.hit.Evidence = ev
		hits = append(hits, s.hit)
	}
	return hits, truncated, len(order), nil
}

type versionMeta struct {
	documentID string
	version    int
}

func (r *LibraryRepo) releaseVersionSet(ctx context.Context, releaseID string) (map[string]versionMeta, []string, error) {
	rows, err := r.db(ctx).QueryContext(ctx, `SELECT document_version_id, document_id, is_removed
		FROM knowledge_release_documents WHERE release_id=? ORDER BY document_id`, releaseID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := map[string]versionMeta{}
	var order []string
	for rows.Next() {
		var versionID, documentID string
		var removed int
		if err := rows.Scan(&versionID, &documentID, &removed); err != nil {
			return nil, nil, err
		}
		if removed == 1 {
			continue
		}
		out[versionID] = versionMeta{documentID: documentID}
		order = append(order, versionID)
	}
	return out, order, rows.Err()
}

func (r *LibraryRepo) assertionsForVersion(ctx context.Context, versionID string) ([]domain.KnowledgeAssertion, error) {
	rows, err := r.db(ctx).QueryContext(ctx, `SELECT row_id, assertion_id, library_id, document_id,
		document_version_id, heading, about_json, perspective, basis, statement, scope_json,
		evidence_json, unknown_notes, ordinal, content_digest, created_at
		FROM knowledge_assertions WHERE document_version_id=? ORDER BY ordinal, assertion_id`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssertions(rows)
}

func scanAssertions(rows *sql.Rows) ([]domain.KnowledgeAssertion, error) {
	var out []domain.KnowledgeAssertion
	for rows.Next() {
		var a domain.KnowledgeAssertion
		var about string
		var created scanTime
		if err := rows.Scan(&a.RowID, &a.ID, &a.LibraryID, &a.DocumentID, &a.DocumentVersionID,
			&a.Heading, &about, &a.Perspective, &a.Basis, &a.Statement, &a.ScopeJSON,
			&a.EvidenceJSON, &a.UnknownNotes, &a.Ordinal, &a.ContentDigest, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(about), &a.About)
		a.CreatedAt = created.T
		out = append(out, a)
	}
	return out, rows.Err()
}

// ReleaseAssertionsByIDs resolves assertion IDs inside the current library.
func (r *LibraryRepo) ReleaseAssertionsByIDs(ctx context.Context, assertionIDs []string) ([]domain.KnowledgeAssertion, error) {
	if len(assertionIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db(ctx).QueryContext(ctx, `SELECT row_id, assertion_id, library_id, document_id,
		document_version_id, heading, about_json, perspective, basis, statement, scope_json,
		evidence_json, unknown_notes, ordinal, content_digest, created_at
		FROM knowledge_assertions WHERE assertion_id IN (`+placeholders(len(assertionIDs))+`)
		ORDER BY assertion_id`, toAny(assertionIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssertions(rows)
}

func (r *LibraryRepo) evidenceForJSON(ctx context.Context, libraryID, evidenceJSON string) ([]domain.KnowledgeEvidence, error) {
	var refs []struct {
		EvidenceID string `json:"evidence_id"`
		Role       string `json:"role"`
	}
	if err := json.Unmarshal([]byte(evidenceJSON), &refs); err != nil || len(refs) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.EvidenceID)
	}
	list, err := r.ListEvidenceByIDs(ctx, libraryID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.KnowledgeEvidence, 0, len(list))
	for _, e := range list {
		out = append(out, *e)
	}
	return out, nil
}

// ReleaseDocuments lists the documents pinned in one release. Path, title and
// version come from the pinned version row, not from the current document, so
// a later rename or removal cannot change how history reads.
func (r *LibraryRepo) ReleaseDocuments(ctx context.Context, releaseID, query, kind string, limit int) ([]*domain.KnowledgeDocument, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.db(ctx).QueryContext(ctx, `SELECT d.id, d.library_id, v.path, d.kind, v.title, d.summary,
		d.domains_json, CASE WHEN rd.is_removed=1 THEN 'removed' ELSE 'active' END, d.renamed_from,
		d.rename_note, v.version, v.created_at, v.created_at
		FROM knowledge_release_documents rd
		JOIN knowledge_document_versions v ON v.id = rd.document_version_id
		JOIN knowledge_documents d ON d.id = rd.document_id
		WHERE rd.release_id=? AND rd.is_removed=0
		  AND (?='' OR d.kind LIKE '%'||?||'%')
		  AND (?='' OR v.title LIKE '%'||?||'%' OR v.path LIKE '%'||?||'%' OR d.summary LIKE '%'||?||'%')
		ORDER BY v.path LIMIT ?`,
		releaseID, kind, kind, query, query, query, query, limit)
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

// ReleaseDocumentVersion returns the document version a release pinned.
func (r *LibraryRepo) ReleaseDocumentVersion(ctx context.Context, releaseID, documentID string) (*domain.KnowledgeDocumentVersion, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT v.id, v.document_id, v.library_id, v.version, v.title, v.path,
		v.content_markdown, v.frontmatter_json, v.content_digest, v.snapshot_id, v.task_id, v.release_id,
		v.evidence_alias_json, v.derived_from_version_id, v.created_at
		FROM knowledge_release_documents rd
		JOIN knowledge_document_versions v ON v.id = rd.document_version_id
		WHERE rd.release_id=? AND rd.document_id=?`, releaseID, documentID)
	return scanDocumentVersion(row)
}

// AssertionsInRelease resolves assertion IDs scoped to one release, so an
// expand handle from a historical answer cannot silently read a newer version.
func (r *LibraryRepo) AssertionsInRelease(ctx context.Context, releaseID string, assertionIDs []string) ([]domain.KnowledgeAssertion, error) {
	if len(assertionIDs) == 0 {
		return nil, nil
	}
	q := `SELECT a.row_id, a.assertion_id, a.library_id, a.document_id, a.document_version_id,
		a.heading, a.about_json, a.perspective, a.basis, a.statement, a.scope_json,
		a.evidence_json, a.unknown_notes, a.ordinal, a.content_digest, a.created_at
		FROM knowledge_assertions a
		JOIN knowledge_release_documents rd ON rd.document_version_id = a.document_version_id
		WHERE rd.release_id=? AND a.assertion_id IN (` + placeholders(len(assertionIDs)) + `)
		ORDER BY a.assertion_id`
	args := []any{releaseID}
	for _, id := range assertionIDs {
		args = append(args, id)
	}
	rows, err := r.db(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssertions(rows)
}

func (r *LibraryRepo) CurrentDocumentVersion(ctx context.Context, documentID string) (*domain.KnowledgeDocumentVersion, error) {
	row := r.db(ctx).QueryRowContext(ctx, `SELECT v.id, v.document_id, v.library_id, v.version, v.title, v.path,
		v.content_markdown, v.frontmatter_json, v.content_digest, v.snapshot_id, v.task_id, v.release_id,
		v.evidence_alias_json, v.derived_from_version_id, v.created_at
		FROM knowledge_document_versions v WHERE v.document_id=?
		ORDER BY v.version DESC LIMIT 1`, documentID)
	return scanDocumentVersion(row)
}

// DocumentVersionDetail returns one pinned version with its records. A version
// number of 0 means the current version.
func (r *LibraryRepo) DocumentVersionDetail(ctx context.Context, documentID string, version int) (*domain.KnowledgeDocumentVersion, []domain.KnowledgeAssertion, []domain.KnowledgeAssertionRelation, error) {
	var v *domain.KnowledgeDocumentVersion
	var err error
	if version <= 0 {
		v, err = r.CurrentDocumentVersion(ctx, documentID)
	} else {
		row := r.db(ctx).QueryRowContext(ctx, `SELECT id, document_id, library_id, version, title, path,
			content_markdown, frontmatter_json, content_digest, snapshot_id, task_id, release_id,
			evidence_alias_json, derived_from_version_id, created_at FROM knowledge_document_versions
			WHERE document_id=? AND version=?`, documentID, version)
		v, err = scanDocumentVersion(row)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	assertions, err := r.assertionsForVersion(ctx, v.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	relations, err := r.relationsForVersion(ctx, v.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	return v, assertions, relations, nil
}

func (r *LibraryRepo) relationsForVersion(ctx context.Context, versionID string) ([]domain.KnowledgeAssertionRelation, error) {
	rows, err := r.db(ctx).QueryContext(ctx, `SELECT row_id, relation_id, library_id, document_id,
		document_version_id, from_kind, from_id, predicate, to_kind, to_id, to_resolved, to_raw,
		perspective, basis, condition, evidence_json, ordinal, content_digest, created_at
		FROM knowledge_assertion_relations WHERE document_version_id=? ORDER BY ordinal, relation_id`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.KnowledgeAssertionRelation
	for rows.Next() {
		var rel domain.KnowledgeAssertionRelation
		var resolved int
		var created scanTime
		if err := rows.Scan(&rel.RowID, &rel.ID, &rel.LibraryID, &rel.DocumentID, &rel.DocumentVersionID,
			&rel.FromKind, &rel.FromID, &rel.Predicate, &rel.ToKind, &rel.ToID, &resolved, &rel.ToRaw,
			&rel.Perspective, &rel.Basis, &rel.Condition, &rel.EvidenceJSON, &rel.Ordinal,
			&rel.ContentDigest, &created); err != nil {
			return nil, err
		}
		rel.ToResolved = resolved == 1
		rel.CreatedAt = created.T
		out = append(out, rel)
	}
	return out, rows.Err()
}

func (r *LibraryRepo) ListDocumentVersions(ctx context.Context, documentID string) ([]*domain.KnowledgeDocumentVersion, error) {
	rows, err := r.db(ctx).QueryContext(ctx, `SELECT id, document_id, library_id, version, title, path,
		content_markdown, frontmatter_json, content_digest, snapshot_id, task_id, release_id,
		evidence_alias_json, derived_from_version_id, created_at FROM knowledge_document_versions
		WHERE document_id=? ORDER BY version DESC`, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.KnowledgeDocumentVersion
	for rows.Next() {
		v, err := scanDocumentVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func scanDocumentVersion(row interface{ Scan(...any) error }) (*domain.KnowledgeDocumentVersion, error) {
	var v domain.KnowledgeDocumentVersion
	var snapshotID, taskID, releaseID, derivedFrom *string
	var created scanTime
	if err := row.Scan(&v.ID, &v.DocumentID, &v.LibraryID, &v.Version, &v.Title, &v.Path, &v.ContentMarkdown,
		&v.FrontmatterJSON, &v.ContentDigest, &snapshotID, &taskID, &releaseID, &v.EvidenceAliasJSON,
		&derivedFrom, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	v.SnapshotID, v.TaskID, v.ReleaseID = deref(snapshotID), deref(taskID), deref(releaseID)
	v.DerivedFromID = deref(derivedFrom)
	v.CreatedAt = created.T
	return &v, nil
}

func (r *LibraryRepo) ReleaseGraph(ctx context.Context, libraryID, releaseID string) ([]*domain.KnowledgeEntity, []domain.KnowledgeAssertionRelation, []*domain.KnowledgeDocument, error) {
	_, order, err := r.releaseVersionSet(ctx, releaseID)
	if err != nil {
		return nil, nil, nil, err
	}
	var relations []domain.KnowledgeAssertionRelation
	for _, versionID := range order {
		list, err := r.relationsForVersion(ctx, versionID)
		if err != nil {
			return nil, nil, nil, err
		}
		relations = append(relations, list...)
	}
	entities, err := r.ListEntities(ctx, libraryID)
	if err != nil {
		return nil, nil, nil, err
	}
	docs, err := r.ReleaseDocuments(ctx, releaseID, "", "", 500)
	if err != nil {
		return nil, nil, nil, err
	}
	return entities, relations, docs, nil
}

func (r *LibraryRepo) ListBridges(ctx context.Context, libraryID, releaseID string) ([]*domain.KnowledgeBridgeView, error) {
	q := `SELECT id, library_id, release_id, base_release_id, anchor_entity_id, title, summary,
		steps_json, participants_json, coverage_json, gaps_json, builder_version, content_digest, created_at
		FROM knowledge_bridge_views WHERE library_id=?`
	args := []any{libraryID}
	if releaseID != "" {
		q += ` AND release_id=?`
		args = append(args, releaseID)
	}
	q += ` ORDER BY created_at DESC LIMIT 100`
	rows, err := r.db(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.KnowledgeBridgeView
	for rows.Next() {
		var b domain.KnowledgeBridgeView
		var base *string
		var created scanTime
		if err := rows.Scan(&b.ID, &b.LibraryID, &b.ReleaseID, &base, &b.AnchorEntityID, &b.Title,
			&b.Summary, &b.StepsJSON, &b.ParticipantsJSON, &b.CoverageJSON, &b.GapsJSON,
			&b.BuilderVersion, &b.ContentDigest, &created); err != nil {
			return nil, err
		}
		b.BaseReleaseID = deref(base)
		b.CreatedAt = created.T
		out = append(out, &b)
	}
	return out, rows.Err()
}

// OwnedContentPaths returns every content path the library has ever
// published, including the source side of a rename and every historical
// version path. Materialisation may only delete paths from this set: a file
// the library does not own is never removed.
func (r *LibraryRepo) OwnedContentPaths(ctx context.Context, libraryID string) ([]string, error) {
	rows, err := r.db(ctx).QueryContext(ctx, `SELECT path FROM knowledge_documents WHERE library_id=? AND path<>''
		UNION SELECT renamed_from FROM knowledge_documents WHERE library_id=? AND renamed_from<>''
		UNION SELECT path FROM knowledge_document_versions WHERE library_id=? AND path<>''`,
		libraryID, libraryID, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ReprojectRecordJSON rebuilds the assertion/relation JSON columns from the
// immutable Markdown of each document version. Those columns are derived from
// the record grammar, so they belong to the rebuildable projection: a change
// in how the grammar serializes must not leave published rows in the old
// shape.
func (r *LibraryRepo) ReprojectRecordJSON(ctx context.Context, libraryID string, project func(markdown string, aliases map[string]string) (map[string]struct {
	About, Scope, Evidence string
}, map[string]string, error)) (int, error) {
	rows, err := r.db(ctx).QueryContext(ctx, `SELECT id, content_markdown, evidence_alias_json
		FROM knowledge_document_versions WHERE library_id=?`, libraryID)
	if err != nil {
		return 0, err
	}
	type version struct{ id, markdown, aliases string }
	var versions []version
	for rows.Next() {
		var v version
		if err := rows.Scan(&v.id, &v.markdown, &v.aliases); err != nil {
			rows.Close()
			return 0, err
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	updated := 0
	for _, v := range versions {
		var aliases map[string]string
		if err := json.Unmarshal([]byte(v.aliases), &aliases); err != nil {
			aliases = map[string]string{}
		}
		assertions, relations, err := project(v.markdown, aliases)
		if err != nil {
			return updated, err
		}
		for assertionID, fields := range assertions {
			if _, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_assertions
				SET about_json=?, scope_json=?, evidence_json=?
				WHERE document_version_id=? AND assertion_id=?`,
				fields.About, fields.Scope, fields.Evidence, v.id, assertionID); err != nil {
				return updated, err
			}
			updated++
		}
		for relationID, evidence := range relations {
			if _, err := r.db(ctx).ExecContext(ctx, `UPDATE knowledge_assertion_relations
				SET evidence_json=? WHERE document_version_id=? AND relation_id=?`,
				evidence, v.id, relationID); err != nil {
				return updated, err
			}
			updated++
		}
	}
	return updated, nil
}

// RebuildSearchIndex reconstructs the full-text projection from the immutable
// document versions, which is what makes the index a projection rather than a
// second source of truth.
func (r *LibraryRepo) RebuildSearchIndex(ctx context.Context, libraryID string) (int, error) {
	count := 0
	err := r.store.InTx(ctx, func(ctx context.Context) error {
		if _, err := r.db(ctx).ExecContext(ctx, `DELETE FROM knowledge_search_index WHERE library_id=?`, libraryID); err != nil {
			return err
		}
		rows, err := r.db(ctx).QueryContext(ctx, `SELECT v.id, v.document_id, v.title, v.content_markdown, v.frontmatter_json
			FROM knowledge_document_versions v WHERE v.library_id=?`, libraryID)
		if err != nil {
			return err
		}
		type row struct{ id, docID, title, body, front string }
		var all []row
		for rows.Next() {
			var x row
			if err := rows.Scan(&x.id, &x.docID, &x.title, &x.body, &x.front); err != nil {
				rows.Close()
				return err
			}
			all = append(all, x)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, x := range all {
			assertions, err := r.assertionsForVersion(ctx, x.id)
			if err != nil {
				return err
			}
			about, statements, scopes := []string{}, []string{}, []string{}
			for _, a := range assertions {
				about = append(about, a.About...)
				statements = append(statements, a.Statement)
				scopes = append(scopes, a.ScopeJSON)
			}
			if _, err := r.db(ctx).ExecContext(ctx, `INSERT INTO knowledge_search_index
				(document_version_id, document_id, library_id, title, summary, body, aliases, tags, scope, entities)
				VALUES (?,?,?,?,?,?,?,?,?,?)`,
				x.id, x.docID, libraryID, x.title, "",
				x.body+"\n"+strings.Join(statements, "\n"), "", "",
				strings.Join(scopes, " "), strings.Join(about, " ")); err != nil {
				return err
			}
			count++
		}
		_, err = r.db(ctx).ExecContext(ctx, `UPDATE knowledge_libraries
			SET index_revision=index_revision+1, version=version+1, updated_at=? WHERE id=?`, timeNow(), libraryID)
		return err
	})
	return count, err
}

// ── Search scoring ─────────────────────────────────────────────────────

func normalizeTerms(terms []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range terms {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	return out
}

// scoreAssertion is deliberately a simple, explainable exact/substring
// scorer. A stable-ID term is an exact hit; other terms are counted, which
// also covers CJK text that unicode61 tokenization would not split.
func scoreAssertion(a domain.KnowledgeAssertion, terms []string) (float64, string) {
	if len(terms) == 0 {
		return 1, firstRunes(a.Statement, 160)
	}
	haystacks := []struct {
		text   string
		weight float64
	}{
		{a.ID, 12},
		{strings.Join(a.About, " "), 6},
		{a.Statement, 3},
		{a.Heading, 2},
		{a.ScopeJSON, 1.5},
		{a.UnknownNotes, 1},
	}
	score := 0.0
	for _, term := range terms {
		lower := strings.ToLower(term)
		for _, h := range haystacks {
			if strings.Contains(strings.ToLower(h.text), lower) {
				score += h.weight
				break
			}
		}
	}
	return score, firstRunes(a.Statement, 160)
}

func firstRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

func toAny(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}
