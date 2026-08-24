package sqlstore

import (
	"context"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ApprovalGrantRepo：scope/工作区/agent/kind 由 SQL 收敛，pattern 前缀在
// Matches（domain）判定——LIKE 转义脆弱且方言分叉，Go 侧匹配语义唯一。
type ApprovalGrantRepo struct {
	store *Store
}

const grantCols = `id, workspace_id, agent_profile_id, work_item_id, scope, kind, pattern, created_at`

func (r *ApprovalGrantRepo) Create(ctx context.Context, g *domain.ApprovalGrant) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO approval_grants(id, workspace_id, agent_profile_id, work_item_id, scope, kind, pattern, created_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		g.ID, g.WorkspaceID, g.AgentProfileID, nullString(g.WorkItemID), string(g.Scope),
		g.Kind, g.Pattern, r.store.dialect.TimeParam(g.CreatedAt))
	return r.store.mapErr(err)
}

func (r *ApprovalGrantRepo) Matching(ctx context.Context, workspaceID, agentProfileID, workItemID, kind, summary string) (*domain.ApprovalGrant, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+grantCols+` FROM approval_grants
		 WHERE workspace_id=? AND agent_profile_id=? AND kind=?
		   AND (scope='workspace' OR (scope='thread' AND work_item_id=?))
		 ORDER BY created_at DESC`, workspaceID, agentProfileID, kind, workItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		g := &domain.ApprovalGrant{}
		var workItemID *string
		var created scanTime
		if err := rows.Scan(&g.ID, &g.WorkspaceID, &g.AgentProfileID, &workItemID,
			&g.Scope, &g.Kind, &g.Pattern, &created); err != nil {
			return nil, err
		}
		if workItemID != nil {
			g.WorkItemID = *workItemID
		}
		g.CreatedAt = mustTime(created)
		if g.Matches(kind, summary) {
			return g, nil
		}
	}
	return nil, rows.Err()
}
