package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type ChatSourceRepo struct{ store *Store }

var _ application.ChatSourceRepo = (*ChatSourceRepo)(nil)

const chatSourceCols = `id, workspace_id, chat_work_item_id, agent_profile_id, client_key,
	filename, mime, size, opaque_key, sha256, status, created_at, updated_at, handed_at`

func scanChatSource(row interface{ Scan(...any) error }) (*domain.ChatSource, error) {
	s := &domain.ChatSource{}
	var handed scanTime
	var created, updated scanTime
	if err := row.Scan(&s.ID, &s.WorkspaceID, &s.ChatWorkItemID, &s.AgentProfileID,
		&s.ClientKey, &s.Filename, &s.MIME, &s.Size, &s.OpaqueKey, &s.SHA256,
		&s.Status, &created, &updated, &handed); err != nil {
		return nil, err
	}
	s.CreatedAt, s.UpdatedAt = mustTime(created), mustTime(updated)
	if handed.Valid {
		t := handed.T
		s.HandedAt = &t
	}
	return s, nil
}

func (r *ChatSourceRepo) Create(ctx context.Context, source *domain.ChatSource) error {
	if err := source.Validate(); err != nil {
		return err
	}
	if source.CreatedAt.IsZero() {
		source.CreatedAt = timeNow()
	}
	if source.UpdatedAt.IsZero() {
		source.UpdatedAt = source.CreatedAt
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO chat_sources(`+chatSourceCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		source.ID, source.WorkspaceID, source.ChatWorkItemID, source.AgentProfileID,
		source.ClientKey, source.Filename, source.MIME, source.Size, source.OpaqueKey,
		source.SHA256, source.Status, timeParam(source.CreatedAt), timeParam(source.UpdatedAt), nullTimeParam(source.HandedAt))
	return r.store.mapErr(err)
}

func (r *ChatSourceRepo) Get(ctx context.Context, workspaceID, chatID, sourceID string) (*domain.ChatSource, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(chatID) == "" || strings.TrimSpace(sourceID) == "" {
		return nil, fmt.Errorf("%w: source scope is required", domain.ErrValidation)
	}
	source, err := scanChatSource(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+chatSourceCols+` FROM chat_sources WHERE workspace_id=? AND chat_work_item_id=? AND id=?`, workspaceID, chatID, sourceID))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return source, nil
}

func (r *ChatSourceRepo) GetByClientKey(ctx context.Context, workspaceID, chatID, clientKey string) (*domain.ChatSource, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(chatID) == "" || strings.TrimSpace(clientKey) == "" {
		return nil, fmt.Errorf("%w: source client scope is required", domain.ErrValidation)
	}
	source, err := scanChatSource(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+chatSourceCols+` FROM chat_sources WHERE workspace_id=? AND chat_work_item_id=? AND client_key=?`, workspaceID, chatID, clientKey))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return source, nil
}

func (r *ChatSourceRepo) ListByChat(ctx context.Context, workspaceID, chatID string) ([]*domain.ChatSource, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(chatID) == "" {
		return nil, fmt.Errorf("%w: source scope is required", domain.ErrValidation)
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+chatSourceCols+` FROM chat_sources WHERE workspace_id=? AND chat_work_item_id=? ORDER BY created_at, id`, workspaceID, chatID)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.ChatSource
	for rows.Next() {
		source, scanErr := scanChatSource(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, source)
	}
	return out, rows.Err()
}

func (r *ChatSourceRepo) MarkHandedToAgent(ctx context.Context, sourceID string, at time.Time) error {
	if strings.TrimSpace(sourceID) == "" {
		return fmt.Errorf("%w: source id required", domain.ErrValidation)
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE chat_sources SET status=?, handed_at=COALESCE(handed_at,?), updated_at=?
		 WHERE id=? AND status IN (?,?)`, domain.ChatSourceHandedToAgent, timeParam(at), timeParam(at), sourceID,
		domain.ChatSourceSaved, domain.ChatSourceHandedToAgent)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists int
		if scanErr := r.store.queryRow(ctx, r.store.exec(ctx), `SELECT 1 FROM chat_sources WHERE id=?`, sourceID).Scan(&exists); scanErr == sql.ErrNoRows {
			return domain.ErrNotFound
		}
	}
	return nil
}
