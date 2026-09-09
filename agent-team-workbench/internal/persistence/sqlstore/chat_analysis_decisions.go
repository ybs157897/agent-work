package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type ChatAnalysisDecisionRepo struct{ store *Store }

var _ application.ChatAnalysisDecisionRepo = (*ChatAnalysisDecisionRepo)(nil)

func scanChatAnalysisDecision(row interface{ Scan(...any) error }) (*domain.ChatAnalysisDecision, error) {
	d := &domain.ChatAnalysisDecision{}
	var created scanTime
	if err := row.Scan(&d.ID, &d.WorkspaceID, &d.ChatWorkItemID, &d.AgentProfileID,
		&d.Revision, &d.ItemID, &d.Outcome, &d.Conclusion, &d.Basis, &d.ProductVersion,
		&d.ItemFingerprint, &d.SourceDependenciesJSON, &d.ActorID, &d.ClientKey, &created); err != nil {
		return nil, err
	}
	d.CreatedAt = mustTime(created)
	return d, nil
}

func scanChatAnalysisDecisionState(row interface{ Scan(...any) error }) (*domain.ChatAnalysisDecisionState, error) {
	s := &domain.ChatAnalysisDecisionState{}
	var updated scanTime
	if err := row.Scan(&s.WorkspaceID, &s.ChatWorkItemID, &s.ItemID, &s.Revision, &s.DecisionID,
		&s.Outcome, &s.Conclusion, &s.Basis, &s.ProductVersion, &s.ItemFingerprint,
		&s.SourceDependenciesJSON, &s.Status, &s.ReviewReason, &updated); err != nil {
		return nil, err
	}
	s.UpdatedAt = mustTime(updated)
	return s, nil
}

func scanChatAnalysisReopen(row interface{ Scan(...any) error }) (*domain.ChatAnalysisReopen, error) {
	r := &domain.ChatAnalysisReopen{}
	var created scanTime
	if err := row.Scan(&r.ID, &r.WorkspaceID, &r.ChatWorkItemID, &r.ItemID, &r.FromRevision,
		&r.ToRevision, &r.Reason, &r.SourceJSON, &created); err != nil {
		return nil, err
	}
	r.CreatedAt = mustTime(created)
	return r, nil
}

func (r *ChatAnalysisDecisionRepo) GetDecisionByClientKey(ctx context.Context, workspaceID, chatID, clientKey string) (*domain.ChatAnalysisDecision, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(chatID) == "" || strings.TrimSpace(clientKey) == "" {
		return nil, fmt.Errorf("%w: decision identity is required", domain.ErrValidation)
	}
	d, err := scanChatAnalysisDecision(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT id,workspace_id,chat_work_item_id,agent_profile_id,revision,item_id,outcome,conclusion,basis,product_version,item_fingerprint,source_dependencies_json,actor_id,client_key,created_at
		 FROM chat_analysis_decisions WHERE workspace_id=? AND chat_work_item_id=? AND client_key=?`, workspaceID, chatID, clientKey))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return d, nil
}

func (r *ChatAnalysisDecisionRepo) ListDecisionStates(ctx context.Context, workspaceID, chatID string) ([]*domain.ChatAnalysisDecisionState, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT workspace_id,chat_work_item_id,item_id,revision,decision_id,outcome,conclusion,basis,product_version,item_fingerprint,source_dependencies_json,status,review_reason,updated_at
		 FROM chat_analysis_decision_states WHERE workspace_id=? AND chat_work_item_id=? ORDER BY item_id`, workspaceID, chatID)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.ChatAnalysisDecisionState
	for rows.Next() {
		state, scanErr := scanChatAnalysisDecisionState(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, state)
	}
	return out, rows.Err()
}

func (r *ChatAnalysisDecisionRepo) ListDecisions(ctx context.Context, workspaceID, chatID string, limit int) ([]*domain.ChatAnalysisDecision, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT id,workspace_id,chat_work_item_id,agent_profile_id,revision,item_id,outcome,conclusion,basis,product_version,item_fingerprint,source_dependencies_json,actor_id,client_key,created_at
		 FROM chat_analysis_decisions WHERE workspace_id=? AND chat_work_item_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, workspaceID, chatID, limit)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.ChatAnalysisDecision
	for rows.Next() {
		decision, scanErr := scanChatAnalysisDecision(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, decision)
	}
	return out, rows.Err()
}

func (r *ChatAnalysisDecisionRepo) ListReopens(ctx context.Context, workspaceID, chatID string, limit int) ([]*domain.ChatAnalysisReopen, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT id,workspace_id,chat_work_item_id,item_id,from_revision,to_revision,reason,source_json,created_at
		 FROM chat_analysis_reopens WHERE workspace_id=? AND chat_work_item_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, workspaceID, chatID, limit)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.ChatAnalysisReopen
	for rows.Next() {
		reopen, scanErr := scanChatAnalysisReopen(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, reopen)
	}
	return out, rows.Err()
}

func (r *ChatAnalysisDecisionRepo) AppendDecision(ctx context.Context, decision *domain.ChatAnalysisDecision,
	state *domain.ChatAnalysisDecisionState, expectedVersion int, expectedRevision int64) (*domain.ChatAnalysisDecision, *domain.ChatAnalysisDecisionState, bool, error) {
	if err := decision.Validate(); err != nil {
		return nil, nil, false, err
	}
	if state == nil || expectedVersion < 1 || expectedRevision < 1 || decision.Revision != expectedRevision ||
		state.Revision != expectedRevision || state.ItemID != decision.ItemID {
		return nil, nil, false, fmt.Errorf("%w: invalid decision CAS", domain.ErrValidation)
	}
	if existing, err := r.GetDecisionByClientKey(ctx, decision.WorkspaceID, decision.ChatWorkItemID, decision.ClientKey); err == nil {
		if !sameDecision(existing, decision) {
			return nil, nil, false, domain.ErrIdempotencyConflict
		}
		current, stateErr := r.stateByItem(ctx, decision.WorkspaceID, decision.ChatWorkItemID, decision.ItemID)
		return existing, current, true, stateErr
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, nil, false, err
	}
	projection, err := r.store.ChatAnalyses().Get(ctx, decision.WorkspaceID, decision.ChatWorkItemID)
	if err != nil {
		return nil, nil, false, err
	}
	if projection.Version != expectedVersion || projection.Revision != expectedRevision ||
		projection.Status == domain.ChatAnalysisAnalyzing || projection.Status == domain.ChatAnalysisFailed || projection.Status == domain.ChatAnalysisStale {
		return nil, nil, false, domain.ErrVersionConflict
	}
	decision.CreatedAt = timeNow()
	state.UpdatedAt = decision.CreatedAt
	if err := state.Validate(); err != nil {
		return nil, nil, false, err
	}
	if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO chat_analysis_decisions
		 (id,workspace_id,chat_work_item_id,agent_profile_id,revision,item_id,outcome,conclusion,basis,product_version,item_fingerprint,source_dependencies_json,actor_id,client_key,created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, decision.ID, decision.WorkspaceID, decision.ChatWorkItemID,
		decision.AgentProfileID, decision.Revision, decision.ItemID, decision.Outcome, decision.Conclusion,
		decision.Basis, decision.ProductVersion, decision.ItemFingerprint, jsonArrayOrEmpty(decision.SourceDependenciesJSON),
		decision.ActorID, decision.ClientKey, timeParam(decision.CreatedAt)); err != nil {
		if existing, getErr := r.GetDecisionByClientKey(ctx, decision.WorkspaceID, decision.ChatWorkItemID, decision.ClientKey); getErr == nil && sameDecision(existing, decision) {
			current, stateErr := r.stateByItem(ctx, decision.WorkspaceID, decision.ChatWorkItemID, decision.ItemID)
			return existing, current, true, stateErr
		}
		return nil, nil, false, r.store.mapErr(err)
	}
	if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO chat_analysis_decision_states
		 (workspace_id,chat_work_item_id,item_id,revision,decision_id,outcome,conclusion,basis,product_version,item_fingerprint,source_dependencies_json,status,review_reason,updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(workspace_id,chat_work_item_id,item_id) DO UPDATE SET revision=excluded.revision,decision_id=excluded.decision_id,outcome=excluded.outcome,conclusion=excluded.conclusion,basis=excluded.basis,product_version=excluded.product_version,item_fingerprint=excluded.item_fingerprint,source_dependencies_json=excluded.source_dependencies_json,status=excluded.status,review_reason='',updated_at=excluded.updated_at`,
		state.WorkspaceID, state.ChatWorkItemID, state.ItemID, state.Revision, state.DecisionID, state.Outcome,
		state.Conclusion, state.Basis, state.ProductVersion, state.ItemFingerprint, jsonArrayOrEmpty(state.SourceDependenciesJSON),
		state.Status, state.ReviewReason, timeParam(state.UpdatedAt)); err != nil {
		return nil, nil, false, r.store.mapErr(err)
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE chat_analyses SET version=version+1,updated_at=? WHERE workspace_id=? AND chat_work_item_id=? AND version=? AND revision=?`,
		timeParam(decision.CreatedAt), decision.WorkspaceID, decision.ChatWorkItemID, expectedVersion, expectedRevision)
	if err != nil {
		return nil, nil, false, r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, nil, false, domain.ErrVersionConflict
	}
	return decision, state, false, nil
}

func (r *ChatAnalysisDecisionRepo) UpsertState(ctx context.Context, state *domain.ChatAnalysisDecisionState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO chat_analysis_decision_states
		 (workspace_id,chat_work_item_id,item_id,revision,decision_id,outcome,conclusion,basis,product_version,item_fingerprint,source_dependencies_json,status,review_reason,updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(workspace_id,chat_work_item_id,item_id) DO UPDATE SET revision=excluded.revision,decision_id=excluded.decision_id,outcome=excluded.outcome,conclusion=excluded.conclusion,basis=excluded.basis,product_version=excluded.product_version,item_fingerprint=excluded.item_fingerprint,source_dependencies_json=excluded.source_dependencies_json,status=excluded.status,review_reason=excluded.review_reason,updated_at=excluded.updated_at`,
		state.WorkspaceID, state.ChatWorkItemID, state.ItemID, state.Revision, state.DecisionID, state.Outcome,
		state.Conclusion, state.Basis, state.ProductVersion, state.ItemFingerprint, jsonArrayOrEmpty(state.SourceDependenciesJSON),
		state.Status, state.ReviewReason, timeParam(state.UpdatedAt))
	return r.store.mapErr(err)
}

func (r *ChatAnalysisDecisionRepo) AppendReopen(ctx context.Context, reopen *domain.ChatAnalysisReopen) (bool, error) {
	if err := reopen.Validate(); err != nil {
		return false, err
	}
	if reopen.CreatedAt.IsZero() {
		reopen.CreatedAt = timeNow()
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT OR IGNORE INTO chat_analysis_reopens
		 (id,workspace_id,chat_work_item_id,item_id,from_revision,to_revision,reason,source_json,created_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`, reopen.ID, reopen.WorkspaceID, reopen.ChatWorkItemID, reopen.ItemID,
		reopen.FromRevision, reopen.ToRevision, reopen.Reason, jsonArrayOrEmpty(reopen.SourceJSON), timeParam(reopen.CreatedAt))
	if err != nil {
		return false, r.store.mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (r *ChatAnalysisDecisionRepo) stateByItem(ctx context.Context, workspaceID, chatID, itemID string) (*domain.ChatAnalysisDecisionState, error) {
	state, err := scanChatAnalysisDecisionState(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT workspace_id,chat_work_item_id,item_id,revision,decision_id,outcome,conclusion,basis,product_version,item_fingerprint,source_dependencies_json,status,review_reason,updated_at
		 FROM chat_analysis_decision_states WHERE workspace_id=? AND chat_work_item_id=? AND item_id=?`, workspaceID, chatID, itemID))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return state, nil
}

func sameDecision(a, b *domain.ChatAnalysisDecision) bool {
	return a != nil && b != nil && a.WorkspaceID == b.WorkspaceID && a.ChatWorkItemID == b.ChatWorkItemID &&
		a.Revision == b.Revision && a.ItemID == b.ItemID && a.Outcome == b.Outcome && a.Conclusion == b.Conclusion &&
		a.Basis == b.Basis && a.ProductVersion == b.ProductVersion && a.ItemFingerprint == b.ItemFingerprint &&
		a.SourceDependenciesJSON == b.SourceDependenciesJSON && a.ActorID == b.ActorID && a.ClientKey == b.ClientKey
}

func jsonArrayOrEmpty(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "[]"
	}
	return raw
}
