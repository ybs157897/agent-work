package sqlstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type HandoffRepo struct{ store *Store }

var _ application.HandoffRepo = (*HandoffRepo)(nil)

const handoffCols = `id, goal_id, todo_id, source_kind, source_id, target_kind, target_id,
	reason, context_summary, evidence, open_risks, acceptance, resolution_reason, status,
	claim_transfer_state, source_claim_version, target_claim_version, actor_kind, actor_id,
	client_key, accepted_by_kind, accepted_by_id, accepted_at, version, created_at, updated_at`

func (r *HandoffRepo) scan(row interface{ Scan(...any) error }) (*domain.Handoff, error) {
	h := &domain.Handoff{}
	var evidence, risks string
	var clientKey, acceptedByKind, acceptedByID *string
	var acceptedAt, created, updated scanTime
	if err := row.Scan(&h.ID, &h.GoalID, &h.TodoID, &h.Source.Kind, &h.Source.ID,
		&h.Target.Kind, &h.Target.ID, &h.Reason, &h.ContextSummary, &evidence, &risks,
		&h.Acceptance, &h.ResolutionReason, &h.Status, &h.ClaimTransferState, &h.SourceClaimVersion,
		&h.TargetClaimVersion, &h.Actor.Kind, &h.Actor.ID, &clientKey, &acceptedByKind,
		&acceptedByID, &acceptedAt, &h.Version, &created, &updated); err != nil {
		return nil, err
	}
	if clientKey != nil {
		h.ClientKey = *clientKey
	}
	if acceptedByKind != nil {
		actor := &domain.GovernanceActorRef{Kind: domain.GovernanceActorKind(*acceptedByKind)}
		if acceptedByID != nil {
			actor.ID = *acceptedByID
		}
		h.AcceptedBy = actor
	}
	h.AcceptedAt = optTime(acceptedAt)
	if err := jsonInto(evidence, &h.Evidence); err != nil {
		return nil, err
	}
	if h.Evidence == nil {
		h.Evidence = []domain.GovernanceEvidenceItem{}
	}
	if err := jsonInto(risks, &h.OpenRisks); err != nil {
		return nil, err
	}
	if h.OpenRisks == nil {
		h.OpenRisks = []string{}
	}
	h.CreatedAt, h.UpdatedAt = mustTime(created), mustTime(updated)
	return h, nil
}

func (r *HandoffRepo) Create(ctx context.Context, h *domain.Handoff) error {
	if h == nil {
		return fmt.Errorf("%w: handoff required", domain.ErrValidation)
	}
	if h.CreatedAt.IsZero() {
		h.CreatedAt = timeNow()
	}
	if h.UpdatedAt.IsZero() {
		h.UpdatedAt = h.CreatedAt
	}
	if err := h.Validate(); err != nil {
		return err
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO governance_handoffs(`+handoffCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		h.ID, h.GoalID, h.TodoID, h.Source.Kind, h.Source.ID, h.Target.Kind, h.Target.ID,
		h.Reason, h.ContextSummary, jsonText(nonNilEvidence(h.Evidence)), jsonText(nonNilStrings(h.OpenRisks)),
		h.Acceptance, h.ResolutionReason, h.Status, h.ClaimTransferState, h.SourceClaimVersion, h.TargetClaimVersion,
		h.Actor.Kind, h.Actor.ID, nullString(h.ClientKey), nullableActorKind(h.AcceptedBy),
		nullableActorID(h.AcceptedBy), nullTimeParam(h.AcceptedAt), h.Version,
		timeParam(h.CreatedAt), timeParam(h.UpdatedAt))
	return r.store.mapErr(err)
}

func nullableActorKind(a *domain.GovernanceActorRef) any {
	if a == nil {
		return nil
	}
	return a.Kind
}

func nullableActorID(a *domain.GovernanceActorRef) any {
	if a == nil {
		return nil
	}
	return a.ID
}

func (r *HandoffRepo) Get(ctx context.Context, id string) (*domain.Handoff, error) {
	h, err := r.scan(r.store.queryRow(ctx, r.store.exec(ctx), `SELECT `+handoffCols+` FROM governance_handoffs WHERE id=?`, id))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return h, nil
}

func (r *HandoffRepo) GetByClientKey(ctx context.Context, goalID, todoID, clientKey string) (*domain.Handoff, error) {
	if strings.TrimSpace(goalID) == "" || strings.TrimSpace(todoID) == "" || strings.TrimSpace(clientKey) == "" {
		return nil, fmt.Errorf("%w: handoff client lookup requires goal, todo and key", domain.ErrValidation)
	}
	h, err := r.scan(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+handoffCols+` FROM governance_handoffs WHERE goal_id=? AND todo_id=? AND client_key=?`, goalID, todoID, clientKey))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return h, nil
}

func (r *HandoffRepo) list(ctx context.Context, where string, arg string) ([]*domain.Handoff, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+handoffCols+` FROM governance_handoffs WHERE `+where+` ORDER BY created_at, id`, arg)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.Handoff
	for rows.Next() {
		h, scanErr := r.scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (r *HandoffRepo) ListByTodo(ctx context.Context, todoID string) ([]*domain.Handoff, error) {
	return r.list(ctx, "todo_id=?", todoID)
}

func (r *HandoffRepo) ListByGoal(ctx context.Context, goalID string) ([]*domain.Handoff, error) {
	return r.list(ctx, "goal_id=?", goalID)
}

func (r *HandoffRepo) Update(ctx context.Context, h *domain.Handoff, expectedVersion int) error {
	if h == nil {
		return fmt.Errorf("%w: handoff required", domain.ErrValidation)
	}
	if err := h.Validate(); err != nil {
		return err
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE governance_handoffs SET reason=?, context_summary=?, evidence=?, open_risks=?,
		 acceptance=?, resolution_reason=?, status=?, claim_transfer_state=?, target_claim_version=?, actor_kind=?,
		 actor_id=?, accepted_by_kind=?, accepted_by_id=?, accepted_at=?, version=?, updated_at=?
		 WHERE id=? AND version=?`,
		h.Reason, h.ContextSummary, jsonText(nonNilEvidence(h.Evidence)), jsonText(nonNilStrings(h.OpenRisks)),
		h.Acceptance, h.ResolutionReason, h.Status, h.ClaimTransferState, h.TargetClaimVersion, h.Actor.Kind, h.Actor.ID,
		nullableActorKind(h.AcceptedBy), nullableActorID(h.AcceptedBy), nullTimeParam(h.AcceptedAt), h.Version,
		timeParam(h.UpdatedAt), h.ID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}
