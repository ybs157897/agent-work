package sqlstore

import (
	"context"
	"encoding/json"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type EventRepo struct{ store *Store }

// streamPayload 是 stream_events.payload 中保存的 envelope 可变部分；
// stream_seq / event_id / type / aggregate 由列承载，读取时重组完整 envelope。
type streamPayload struct {
	RunSeq        int64              `json:"run_seq,omitempty"`
	Actor         *domain.EventActor `json:"actor,omitempty"`
	CorrelationID string             `json:"correlation_id,omitempty"`
	Data          map[string]any     `json:"data,omitempty"`
}

// Append 必须在事务内调用：分配 run_seq（可选）与 stream_seq，
// 同一事务写 stream_events + outbox_messages。
func (r *EventRepo) Append(ctx context.Context, ev *domain.CanonicalEvent, runEvent *application.RunEventRecord) (*domain.CanonicalEvent, error) {
	if !domain.IsKnownEventName(ev.Type) {
		return nil, domain.ErrValidation
	}
	db := r.store.exec(ctx)
	d := r.store.dialect

	// 串行化同一 workspace 的序号分配（Postgres advisory lock；SQLite 写串行）。
	if err := d.AdvisoryLock(ctx, db,
		r.store.ph(`SELECT pg_advisory_xact_lock(hashtext(?))`), ev.WorkspaceID); err != nil {
		return nil, err
	}

	if runEvent != nil {
		if err := r.store.queryRow(ctx, db,
			`INSERT INTO run_events(run_id, run_seq, event_type, payload, occurred_at)
			 VALUES (?, (SELECT COALESCE(MAX(run_seq),0)+1 FROM run_events WHERE run_id=?), ?, ?, ?)
			 RETURNING run_seq`,
			runEvent.RunID, runEvent.RunID, runEvent.EventType, jsonText(runEvent.Payload),
			d.TimeParam(ev.OccurredAt)).Scan(&ev.RunSeq); err != nil {
			return nil, r.store.mapErr(err)
		}
	}

	payload, err := json.Marshal(streamPayload{
		RunSeq: ev.RunSeq, Actor: ev.Actor, CorrelationID: ev.CorrelationID, Data: ev.Data,
	})
	if err != nil {
		return nil, err
	}

	if err := r.store.queryRow(ctx, db,
		`INSERT INTO stream_events(workspace_id, stream_seq, event_id, event_type,
			aggregate_type, aggregate_id, payload, occurred_at)
		 VALUES (?, (SELECT COALESCE(MAX(stream_seq),0)+1 FROM stream_events WHERE workspace_id=?),
			?, ?, ?, ?, ?, ?)
		 RETURNING stream_seq`,
		ev.WorkspaceID, ev.WorkspaceID, ev.EventID, ev.Type, ev.AggregateType, ev.AggregateID,
		string(payload), d.TimeParam(ev.OccurredAt)).Scan(&ev.StreamSeq); err != nil {
		return nil, r.store.mapErr(err)
	}

	// Outbox：至少一次投递，publisher 可重复发布。
	if _, err := r.store.execStmt(ctx, db,
		`INSERT INTO outbox_messages(event_id, topic, payload, created_at) VALUES (?,?,?,?)`,
		ev.EventID, ev.WorkspaceID, string(payload), d.TimeParam(ev.OccurredAt)); err != nil {
		return nil, r.store.mapErr(err)
	}
	return ev, nil
}

// Since 从 afterSeq 之后补发；游标早于保留窗口返回 ErrCursorExpired。
func (r *EventRepo) Since(ctx context.Context, workspaceID string, afterSeq int64, limit int) ([]*domain.CanonicalEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	db := r.store.exec(ctx)

	var minSeq int64
	if err := r.store.queryRow(ctx, db,
		`SELECT COALESCE(MIN(stream_seq),0) FROM stream_events WHERE workspace_id=?`, workspaceID).
		Scan(&minSeq); err != nil {
		return nil, err
	}
	if afterSeq > 0 && minSeq > 0 && afterSeq+1 < minSeq {
		return nil, domain.ErrCursorExpired
	}

	rows, err := r.store.query(ctx, db,
		`SELECT stream_seq, event_id, event_type, aggregate_type, aggregate_id, payload, occurred_at
		 FROM stream_events WHERE workspace_id=? AND stream_seq>?
		 ORDER BY stream_seq LIMIT ?`, workspaceID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*domain.CanonicalEvent
	for rows.Next() {
		ev := &domain.CanonicalEvent{ContractVersion: "events/v1", WorkspaceID: workspaceID}
		var raw string
		var occurred scanTime
		if err := rows.Scan(&ev.StreamSeq, &ev.EventID, &ev.Type, &ev.AggregateType, &ev.AggregateID,
			&raw, &occurred); err != nil {
			return nil, err
		}
		ev.OccurredAt = mustTime(occurred)
		var p streamPayload
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		ev.RunSeq = p.RunSeq
		ev.Actor = p.Actor
		ev.CorrelationID = p.CorrelationID
		ev.Data = p.Data
		ev.Aggregate = domain.AggregateRef{Type: ev.AggregateType, ID: ev.AggregateID}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (r *EventRepo) LatestSeq(ctx context.Context, workspaceID string) (int64, error) {
	var seq int64
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT COALESCE(MAX(stream_seq),0) FROM stream_events WHERE workspace_id=?`, workspaceID).Scan(&seq)
	return seq, r.store.mapErr(err)
}

func (r *EventRepo) AppendActivity(ctx context.Context, workspaceID, kind, message string) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO activities(id, workspace_id, kind, message, occurred_at) VALUES (?,?,?,?,?)`,
		domain.NewID(domain.PrefixEvent), workspaceID, kind, message, d.TimeParam(timeNow()))
	return r.store.mapErr(err)
}

func (r *EventRepo) ListActivities(ctx context.Context, workspaceID string, limit int) ([]application.Activity, error) {
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT id, kind, message, occurred_at FROM activities
		 WHERE workspace_id=? ORDER BY occurred_at DESC LIMIT ?`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []application.Activity
	for rows.Next() {
		var a application.Activity
		var occurred scanTime
		if err := rows.Scan(&a.ID, &a.Kind, &a.Message, &occurred); err != nil {
			return nil, err
		}
		a.OccurredAt = mustTime(occurred)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListRunEvents 按 run_seq 正序回放；payload 为 JSON 文本（可空）。
func (r *EventRepo) ListRunEvents(ctx context.Context, runID string) ([]application.RunEvent, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT run_seq, event_type, payload, occurred_at FROM run_events WHERE run_id=? ORDER BY run_seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []application.RunEvent
	for rows.Next() {
		var e application.RunEvent
		var payload *string
		var occurred scanTime
		if err := rows.Scan(&e.RunSeq, &e.EventType, &payload, &occurred); err != nil {
			return nil, err
		}
		if payload != nil && *payload != "" {
			_ = jsonInto(*payload, &e.Payload)
		}
		e.OccurredAt = mustTime(occurred)
		out = append(out, e)
	}
	return out, rows.Err()
}

type IdempotencyRepo struct{ store *Store }

func (r *IdempotencyRepo) Check(ctx context.Context, workspaceID, key string) (*application.IdempotencyRecord, error) {
	rec := &application.IdempotencyRecord{}
	var resultRef *string
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT request_hash, status_code, result_ref FROM idempotency_keys
		 WHERE workspace_id=? AND key=?`, workspaceID, key).
		Scan(&rec.RequestHash, &rec.StatusCode, &resultRef)
	if err != nil {
		if err = r.store.mapErr(err); err == domain.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	if resultRef != nil {
		rec.ResultBody = *resultRef
	}
	return rec, nil
}

func (r *IdempotencyRepo) Record(ctx context.Context, workspaceID, key string, rec application.IdempotencyRecord) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO idempotency_keys(workspace_id, key, request_hash, result_ref, status_code, created_at)
		 VALUES (?,?,?,?,?,?) ON CONFLICT (workspace_id, key) DO NOTHING`,
		workspaceID, key, rec.RequestHash, rec.ResultBody, rec.StatusCode, d.TimeParam(timeNow()))
	return r.store.mapErr(err)
}
