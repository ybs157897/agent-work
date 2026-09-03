package sqlstore

import (
	"context"
	"fmt"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// DeliveryBriefSnapshotRepo persists sealed deterministic Delivery Brief
// captures. Lifecycle immutability is enforced by migration 0032 triggers;
// this repository intentionally exposes no update or delete method.
type DeliveryBriefSnapshotRepo struct{ store *Store }

var _ application.DeliveryBriefSnapshotRepo = (*DeliveryBriefSnapshotRepo)(nil)

const deliveryBriefSnapshotCols = `id, schema_version, goal_id, todo_id, work_item_id, snapshot_json,
	canonical_digest, as_of_event_seq, source_versions, freshness_state, created_at, client_key`

func scanDeliveryBriefSnapshot(row interface{ Scan(...any) error }) (*domain.DeliveryBriefSnapshot, error) {
	snapshot := &domain.DeliveryBriefSnapshot{}
	var sourceVersions string
	var clientKey *string
	var created scanTime
	if err := row.Scan(&snapshot.ID, &snapshot.SchemaVersion, &snapshot.GoalID, &snapshot.TodoID, &snapshot.WorkItemID,
		&snapshot.SnapshotJSON, &snapshot.CanonicalDigest, &snapshot.AsOfEventSeq,
		&sourceVersions, &snapshot.FreshnessState, &created, &clientKey); err != nil {
		return nil, err
	}
	if err := jsonInto(sourceVersions, &snapshot.SourceVersions); err != nil {
		return nil, err
	}
	if snapshot.SourceVersions == nil {
		snapshot.SourceVersions = map[string]int64{}
	}
	if clientKey != nil {
		snapshot.ClientKey = *clientKey
	}
	snapshot.CreatedAt = mustTime(created)
	return snapshot, nil
}

func (r *DeliveryBriefSnapshotRepo) Create(ctx context.Context, snapshot *domain.DeliveryBriefSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("%w: delivery brief snapshot required", domain.ErrValidation)
	}
	if snapshot.CreatedAt.IsZero() {
		return fmt.Errorf("%w: delivery brief snapshot created_at is required", domain.ErrValidation)
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO governance_delivery_brief_snapshots(`+deliveryBriefSnapshotCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		snapshot.ID, snapshot.SchemaVersion, snapshot.GoalID, snapshot.TodoID, snapshot.WorkItemID,
		snapshot.SnapshotJSON, snapshot.CanonicalDigest, snapshot.AsOfEventSeq,
		jsonText(snapshot.SourceVersions), snapshot.FreshnessState,
		timeParam(snapshot.CreatedAt), nullString(snapshot.ClientKey))
	return r.store.mapErr(err)
}

func (r *DeliveryBriefSnapshotRepo) Get(ctx context.Context, id string) (*domain.DeliveryBriefSnapshot, error) {
	snapshot, err := scanDeliveryBriefSnapshot(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+deliveryBriefSnapshotCols+` FROM governance_delivery_brief_snapshots WHERE id=?`, id))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (r *DeliveryBriefSnapshotRepo) GetByClientKey(ctx context.Context, goalID, todoID, clientKey string) (*domain.DeliveryBriefSnapshot, error) {
	if goalID == "" || todoID == "" || clientKey == "" {
		return nil, fmt.Errorf("%w: delivery brief snapshot replay key requires Goal, Todo and client key", domain.ErrValidation)
	}
	snapshot, err := scanDeliveryBriefSnapshot(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+deliveryBriefSnapshotCols+` FROM governance_delivery_brief_snapshots
		 WHERE goal_id=? AND todo_id=? AND client_key=?`, goalID, todoID, clientKey))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	return snapshot, nil
}
