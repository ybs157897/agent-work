package sqlstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type GovernanceProjectionRepo struct{ store *Store }

var _ application.GovernanceProjectionRepo = (*GovernanceProjectionRepo)(nil)

const projectionCols = `goal_id, goal_progress, todo_current_state, receipt_timeline,
	evidence_summary, next_action_checkpoint, counters, source_event_stream_seq, through_turn_seq,
	digest, version, updated_at`

func scanProjection(row interface{ Scan(...any) error }) (*domain.GovernanceGoalProjection, error) {
	p := &domain.GovernanceGoalProjection{}
	var goalProgress, todoCurrent, timeline, evidence, nextAction string
	var updated scanTime
	var counters string
	if err := row.Scan(&p.GoalID, &goalProgress, &todoCurrent, &timeline, &evidence, &nextAction, &counters,
		&p.SourceCursor.EventStreamSeq, &p.SourceCursor.ThroughTurnSeq, &p.Digest, &p.Version, &updated); err != nil {
		return nil, err
	}
	if err := jsonInto(goalProgress, &p.GoalProgress); err != nil {
		return nil, err
	}
	if err := jsonInto(todoCurrent, &p.TodoCurrentState); err != nil {
		return nil, err
	}
	if err := jsonInto(timeline, &p.ReceiptTimeline); err != nil {
		return nil, err
	}
	if err := jsonInto(evidence, &p.EvidenceSummary); err != nil {
		return nil, err
	}
	if err := jsonInto(nextAction, &p.NextActionCheckpoint); err != nil {
		return nil, err
	}
	if err := jsonInto(counters, &p.Counters); err != nil {
		return nil, err
	}
	p.UpdatedAt = mustTime(updated)
	return p, nil
}

func (r *GovernanceProjectionRepo) Get(ctx context.Context, goalID string) (*domain.GovernanceGoalProjection, error) {
	p, err := scanProjection(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+projectionCols+` FROM governance_goal_projections WHERE goal_id=?`, goalID))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return p, nil
}

func (r *GovernanceProjectionRepo) Upsert(ctx context.Context, p *domain.GovernanceGoalProjection) error {
	if p == nil {
		return fmt.Errorf("%w: governance projection required", domain.ErrValidation)
	}
	if err := p.Validate(); err != nil {
		return err
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO governance_goal_projections(`+projectionCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(goal_id) DO UPDATE SET goal_progress=excluded.goal_progress,
		 todo_current_state=excluded.todo_current_state, receipt_timeline=excluded.receipt_timeline,
		 evidence_summary=excluded.evidence_summary, next_action_checkpoint=excluded.next_action_checkpoint,
		 counters=excluded.counters,
		 source_event_stream_seq=excluded.source_event_stream_seq, through_turn_seq=excluded.through_turn_seq,
		 digest=excluded.digest, version=excluded.version, updated_at=excluded.updated_at`,
		p.GoalID, jsonText(p.GoalProgress), jsonText(p.TodoCurrentState), jsonText(nonNilMapSlice(p.ReceiptTimeline)),
		jsonText(nonNilEvidence(p.EvidenceSummary)), jsonText(p.NextActionCheckpoint), jsonText(p.Counters), p.SourceCursor.EventStreamSeq,
		p.SourceCursor.ThroughTurnSeq, p.Digest, p.Version, timeParam(p.UpdatedAt))
	return r.store.mapErr(err)
}

func nonNilMapSlice(values []map[string]any) []map[string]any {
	if values == nil {
		return []map[string]any{}
	}
	return values
}

const repairCols = `id, goal_id, status, scope, source_event_stream_seq, through_turn_seq,
	replayed_event_count, replayed_receipt_count, error_code, error_message, client_key,
	version, started_at, completed_at, created_at, updated_at`

func scanRepair(row interface{ Scan(...any) error }) (*domain.ProjectionRepair, error) {
	r := &domain.ProjectionRepair{}
	var scope string
	var clientKey, errorCode, errorMessage *string
	var started, completed, created, updated scanTime
	if err := row.Scan(&r.ID, &r.GoalID, &r.Status, &scope, &r.SourceCursor.EventStreamSeq,
		&r.SourceCursor.ThroughTurnSeq, &r.ReplayedEventCount, &r.ReplayedReceiptCount,
		&errorCode, &errorMessage, &clientKey, &r.Version, &started, &completed, &created, &updated); err != nil {
		return nil, err
	}
	if err := jsonInto(scope, &r.Scope); err != nil {
		return nil, err
	}
	if r.Scope == nil {
		r.Scope = []domain.GovernanceProjectionScope{}
	}
	if errorCode != nil {
		r.ErrorCode = *errorCode
	}
	if errorMessage != nil {
		r.ErrorMessage = *errorMessage
	}
	if clientKey != nil {
		r.ClientKey = *clientKey
	}
	r.StartedAt, r.CompletedAt, r.CreatedAt, r.UpdatedAt = mustTime(started), optTime(completed), mustTime(created), mustTime(updated)
	return r, nil
}

func (r *GovernanceProjectionRepo) CreateRepair(ctx context.Context, repair *domain.ProjectionRepair) error {
	if repair == nil {
		return fmt.Errorf("%w: projection repair required", domain.ErrValidation)
	}
	if repair.CreatedAt.IsZero() {
		repair.CreatedAt = timeNow()
	}
	if repair.UpdatedAt.IsZero() {
		repair.UpdatedAt = repair.CreatedAt
	}
	if repair.StartedAt.IsZero() {
		repair.StartedAt = repair.CreatedAt
	}
	if err := repair.Validate(); err != nil {
		return err
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO governance_projection_repairs(`+repairCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		repair.ID, repair.GoalID, repair.Status, jsonText(repair.Scope), repair.SourceCursor.EventStreamSeq,
		repair.SourceCursor.ThroughTurnSeq, repair.ReplayedEventCount, repair.ReplayedReceiptCount,
		repair.ErrorCode, repair.ErrorMessage, nullString(repair.ClientKey), repair.Version,
		timeParam(repair.StartedAt), nullTimeParam(repair.CompletedAt), timeParam(repair.CreatedAt), timeParam(repair.UpdatedAt))
	return r.store.mapErr(err)
}

func (r *GovernanceProjectionRepo) GetRepair(ctx context.Context, id string) (*domain.ProjectionRepair, error) {
	repair, err := scanRepair(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+repairCols+` FROM governance_projection_repairs WHERE id=?`, id))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return repair, nil
}

func (r *GovernanceProjectionRepo) GetRepairByClientKey(ctx context.Context, goalID, clientKey string) (*domain.ProjectionRepair, error) {
	if strings.TrimSpace(goalID) == "" || strings.TrimSpace(clientKey) == "" {
		return nil, fmt.Errorf("%w: repair client lookup requires goal and key", domain.ErrValidation)
	}
	repair, err := scanRepair(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+repairCols+` FROM governance_projection_repairs WHERE goal_id=? AND client_key=?`, goalID, clientKey))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return repair, nil
}

func (r *GovernanceProjectionRepo) ListRepairsByGoal(ctx context.Context, goalID string) ([]*domain.ProjectionRepair, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+repairCols+` FROM governance_projection_repairs WHERE goal_id=? ORDER BY created_at, id`, goalID)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.ProjectionRepair
	for rows.Next() {
		repair, scanErr := scanRepair(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, repair)
	}
	return out, rows.Err()
}

func (r *GovernanceProjectionRepo) UpdateRepair(ctx context.Context, repair *domain.ProjectionRepair, expectedVersion int) error {
	if repair == nil {
		return fmt.Errorf("%w: projection repair required", domain.ErrValidation)
	}
	if err := repair.Validate(); err != nil {
		return err
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE governance_projection_repairs SET status=?, scope=?, source_event_stream_seq=?,
		 through_turn_seq=?, replayed_event_count=?, replayed_receipt_count=?, error_code=?,
		 error_message=?, version=?, started_at=?, completed_at=?, updated_at=?
		 WHERE id=? AND version=?`,
		repair.Status, jsonText(repair.Scope), repair.SourceCursor.EventStreamSeq, repair.SourceCursor.ThroughTurnSeq,
		repair.ReplayedEventCount, repair.ReplayedReceiptCount, repair.ErrorCode, repair.ErrorMessage,
		repair.Version, timeParam(repair.StartedAt), nullTimeParam(repair.CompletedAt), timeParam(repair.UpdatedAt),
		repair.ID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}
