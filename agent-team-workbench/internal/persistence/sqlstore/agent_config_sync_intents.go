package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// AgentConfigSyncIntentRepo is the SQLite implementation of the durable
// external-config intent port. Target bytes are stored exactly as canonicalized
// by domain.NewAgentConfigSyncIntent; no file path or credential is persisted.
type AgentConfigSyncIntentRepo struct{ store *Store }

const agentConfigSyncIntentColumns = `id, agent_profile_id, workspace_id,
	target_version, target_snapshot, target_digest, status, last_error, attempts,
	version, created_at, updated_at, applied_at`

func (r *AgentConfigSyncIntentRepo) scan(row interface{ Scan(...any) error }) (*domain.AgentConfigSyncIntent, error) {
	i := &domain.AgentConfigSyncIntent{}
	var status string
	var created, updated, applied scanTime
	if err := row.Scan(&i.ID, &i.AgentID, &i.WorkspaceID, &i.TargetVersion,
		&i.TargetSnapshot, &i.TargetDigest, &status, &i.LastError, &i.Attempts,
		&i.Version, &created, &updated, &applied); err != nil {
		return nil, r.store.mapErr(err)
	}
	i.Status = domain.AgentConfigSyncIntentStatus(status)
	i.CreatedAt, i.UpdatedAt, i.AppliedAt = mustTime(created), mustTime(updated), optTime(applied)
	if err := i.Validate(); err != nil {
		return nil, fmt.Errorf("invalid persisted agent config sync intent %s: %w", i.ID, err)
	}
	return i, nil
}

func (r *AgentConfigSyncIntentRepo) Create(ctx context.Context, intent *domain.AgentConfigSyncIntent) error {
	if intent == nil {
		return fmt.Errorf("%w: nil agent config sync intent", domain.ErrValidation)
	}
	if intent.Status == "" {
		intent.Status = domain.AgentConfigSyncPending
	}
	if intent.Version == 0 {
		intent.Version = 1
	}
	if intent.CreatedAt.IsZero() {
		intent.CreatedAt = time.Now().UTC()
	}
	if intent.UpdatedAt.IsZero() {
		intent.UpdatedAt = intent.CreatedAt
	}
	if err := intent.Validate(); err != nil {
		return err
	}
	if intent.Status != domain.AgentConfigSyncPending {
		return fmt.Errorf("%w: new agent config sync intent must be pending", domain.ErrValidation)
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO agent_config_sync_intents(`+agentConfigSyncIntentColumns+`)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		intent.ID, intent.AgentID, intent.WorkspaceID, intent.TargetVersion,
		intent.TargetSnapshot, intent.TargetDigest, intent.Status, intent.LastError,
		intent.Attempts, intent.Version, timeParam(intent.CreatedAt),
		timeParam(intent.UpdatedAt), nullTimeParam(intent.AppliedAt))
	return r.store.mapErr(err)
}

func (r *AgentConfigSyncIntentRepo) Get(ctx context.Context, id string) (*domain.AgentConfigSyncIntent, error) {
	return r.scan(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+agentConfigSyncIntentColumns+` FROM agent_config_sync_intents WHERE id=?`, id))
}

func (r *AgentConfigSyncIntentRepo) GetActiveByAgent(ctx context.Context, agentID string) (*domain.AgentConfigSyncIntent, error) {
	return r.scan(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+agentConfigSyncIntentColumns+` FROM agent_config_sync_intents
		 WHERE agent_profile_id=? AND status<>? ORDER BY id LIMIT 1`,
		agentID, domain.AgentConfigSyncApplied))
}

func (r *AgentConfigSyncIntentRepo) ListActive(ctx context.Context) ([]*domain.AgentConfigSyncIntent, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+agentConfigSyncIntentColumns+` FROM agent_config_sync_intents
		 WHERE status<>? ORDER BY updated_at, id`, domain.AgentConfigSyncApplied)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	items := make([]*domain.AgentConfigSyncIntent, 0)
	for rows.Next() {
		item, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (r *AgentConfigSyncIntentRepo) MarkFailed(ctx context.Context, id string, expectedVersion int, message string) error {
	return r.updateRecovery(ctx, id, expectedVersion, domain.AgentConfigSyncFailed, message, false, time.Time{})
}

func (r *AgentConfigSyncIntentRepo) MarkConflict(ctx context.Context, id string, expectedVersion int, message string) error {
	return r.updateRecovery(ctx, id, expectedVersion, domain.AgentConfigSyncConflict, message, false, time.Time{})
}

func (r *AgentConfigSyncIntentRepo) updateRecovery(ctx context.Context, id string, expectedVersion int,
	status domain.AgentConfigSyncIntentStatus, message string, applied bool, appliedAt time.Time) error {
	if !status.Valid() || (applied && status != domain.AgentConfigSyncApplied) ||
		(!applied && status == domain.AgentConfigSyncApplied) {
		return fmt.Errorf("%w: invalid recovery status %q", domain.ErrValidation, status)
	}
	var result sql.Result
	var err error
	if applied {
		result, err = r.store.execStmt(ctx, r.store.exec(ctx),
			`UPDATE agent_config_sync_intents SET status=?, last_error='', attempts=attempts+1,
			 version=version+1, updated_at=?, applied_at=?
			 WHERE id=? AND version=? AND status IN (?,?)`,
			status, timeParam(appliedAt), timeParam(appliedAt), id, expectedVersion,
			domain.AgentConfigSyncPending, domain.AgentConfigSyncFailed)
	} else {
		result, err = r.store.execStmt(ctx, r.store.exec(ctx),
			`UPDATE agent_config_sync_intents SET status=?, last_error=?, attempts=attempts+1,
			 version=version+1, updated_at=?
			 WHERE id=? AND version=? AND status<>?`,
			status, message, timeParam(time.Now().UTC()), id, expectedVersion,
			domain.AgentConfigSyncApplied)
	}
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (r *AgentConfigSyncIntentRepo) MarkApplied(ctx context.Context, id string, expectedVersion int, appliedAt time.Time) error {
	if appliedAt.IsZero() {
		appliedAt = time.Now().UTC()
	}
	return r.updateRecovery(ctx, id, expectedVersion, domain.AgentConfigSyncApplied, "", true, appliedAt.UTC())
}

// Ensure compile-time conformance to the application port.
var _ application.AgentConfigSyncIntentRepo = (*AgentConfigSyncIntentRepo)(nil)
