package sqlstore

import (
	"context"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type BindingRepo struct{ store *Store }

const bindingCols = `id, workspace_id, runtime_label, adapter_id, adapter_version, provider_version,
	provider, model, credential_ref, capabilities, status, version, created_at, updated_at`

func (r *BindingRepo) scan(row interface{ Scan(...any) error }, b *domain.RuntimeBinding) error {
	var providerVersion, credentialRef *string
	var caps string
	var created, updated scanTime
	if err := row.Scan(&b.ID, &b.WorkspaceID, &b.RuntimeLabel, &b.AdapterID, &b.AdapterVersion,
		&providerVersion, &b.Provider, &b.Model, &credentialRef, &caps, &b.Status,
		&b.Version, &created, &updated); err != nil {
		return err
	}
	if providerVersion != nil {
		b.ProviderVersion = *providerVersion
	}
	if credentialRef != nil {
		b.CredentialRef = *credentialRef
	}
	_ = jsonInto(caps, &b.Capabilities)
	b.CreatedAt, b.UpdatedAt = mustTime(created), mustTime(updated)
	return nil
}

func (r *BindingRepo) Create(ctx context.Context, b *domain.RuntimeBinding) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO runtime_bindings(`+bindingCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		b.ID, b.WorkspaceID, b.RuntimeLabel, b.AdapterID, b.AdapterVersion,
		nullString(b.ProviderVersion), b.Provider, b.Model, nullString(b.CredentialRef),
		jsonText(b.Capabilities), b.Status, b.Version,
		timeParam(b.CreatedAt), timeParam(b.UpdatedAt))
	return r.store.mapErr(err)
}

func (r *BindingRepo) Get(ctx context.Context, id string) (*domain.RuntimeBinding, error) {
	b := &domain.RuntimeBinding{}
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+bindingCols+` FROM runtime_bindings WHERE id=?`, id)
	if err := r.scan(row, b); err != nil {
		return nil, r.store.mapErr(err)
	}
	return b, nil
}

func (r *BindingRepo) GetByLabel(ctx context.Context, workspaceID, label string) (*domain.RuntimeBinding, error) {
	b := &domain.RuntimeBinding{}
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+bindingCols+` FROM runtime_bindings WHERE workspace_id=? AND runtime_label=?`,
		workspaceID, label)
	if err := r.scan(row, b); err != nil {
		return nil, r.store.mapErr(err)
	}
	return b, nil
}

func (r *BindingRepo) List(ctx context.Context, workspaceID string) ([]*domain.RuntimeBinding, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+bindingCols+` FROM runtime_bindings WHERE workspace_id=? ORDER BY created_at`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.RuntimeBinding
	for rows.Next() {
		b := &domain.RuntimeBinding{}
		if err := r.scan(rows, b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Update 乐观锁：version 不匹配时更新 0 行 → ErrVersionConflict。
func (r *BindingRepo) Update(ctx context.Context, b *domain.RuntimeBinding, expectedVersion int) error {
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE runtime_bindings SET runtime_label=?, adapter_id=?, adapter_version=?, provider_version=?,
			provider=?, model=?, credential_ref=?, capabilities=?, status=?,
			version=version+1, updated_at=?
		 WHERE id=? AND version=?`,
		b.RuntimeLabel, b.AdapterID, b.AdapterVersion, nullString(b.ProviderVersion),
		b.Provider, b.Model, nullString(b.CredentialRef), jsonText(b.Capabilities), b.Status,
		timeParam(timeNow()), b.ID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}
