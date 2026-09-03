package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type EventRepo struct{ store *Store }

// streamPayload 是 stream_events.payload 中保存的 envelope 可变部分；
// stream_seq / event_id / type / aggregate 由列承载，读取时重组完整 envelope。
type streamPayload struct {
	RunSeq           int64              `json:"run_seq,omitempty"`
	AgentID          string             `json:"agent_id,omitempty"`
	Actor            *domain.EventActor `json:"actor,omitempty"`
	CorrelationID    string             `json:"correlation_id,omitempty"`
	AggregateVersion int                `json:"aggregate_version"`
	Data             map[string]any     `json:"data,omitempty"`
}

// Append 必须在事务内调用：分配 run_seq（可选）与 stream_seq，
// 同一事务写 stream_events + outbox_messages。
func (r *EventRepo) Append(ctx context.Context, ev *domain.CanonicalEvent, runEvent *application.RunEventRecord) (*domain.CanonicalEvent, error) {
	if !domain.IsKnownEventName(ev.Type) {
		return nil, domain.ErrValidation
	}
	db := r.store.exec(ctx)

	if runEvent != nil {
		if err := r.store.queryRow(ctx, db,
			`INSERT INTO run_events(run_id, agent_id, run_seq, event_type, payload, occurred_at)
			 VALUES (?, NULLIF(?, ''), (SELECT COALESCE(MAX(run_seq),0)+1 FROM run_events WHERE run_id=?), ?, ?, ?)
			 RETURNING run_seq`,
			runEvent.RunID, runEvent.AgentID, runEvent.RunID, runEvent.EventType, jsonText(runEvent.Payload),
			timeParam(ev.OccurredAt)).Scan(&ev.RunSeq); err != nil {
			return nil, r.store.mapErr(err)
		}
	}

	payload, err := json.Marshal(streamPayload{
		RunSeq: ev.RunSeq, AgentID: ev.AgentID, Actor: ev.Actor, CorrelationID: ev.CorrelationID,
		AggregateVersion: ev.Aggregate.Version, Data: ev.Data,
	})
	if err != nil {
		return nil, err
	}

	if err := r.store.queryRow(ctx, db,
		`INSERT INTO stream_events(workspace_id, stream_seq, event_id, event_type,
			aggregate_type, aggregate_id, aggregate_version, payload, occurred_at)
			 VALUES (?, (SELECT COALESCE(MAX(stream_seq),0)+1 FROM stream_events WHERE workspace_id=?),
			?, ?, ?, ?, ?, ?, ?)
			 RETURNING stream_seq`,
		ev.WorkspaceID, ev.WorkspaceID, ev.EventID, ev.Type, ev.AggregateType, ev.AggregateID,
		ev.Aggregate.Version, string(payload), timeParam(ev.OccurredAt)).Scan(&ev.StreamSeq); err != nil {
		return nil, r.store.mapErr(err)
	}

	// Outbox：至少一次投递，publisher 可重复发布。
	if _, err := r.store.execStmt(ctx, db,
		`INSERT INTO outbox_messages(event_id, topic, payload, created_at) VALUES (?,?,?,?)`,
		ev.EventID, ev.WorkspaceID, string(payload), timeParam(ev.OccurredAt)); err != nil {
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
		`SELECT stream_seq, event_id, event_type, aggregate_type, aggregate_id, aggregate_version, payload, occurred_at
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
		var aggregateVersion int
		if err := rows.Scan(&ev.StreamSeq, &ev.EventID, &ev.Type, &ev.AggregateType, &ev.AggregateID,
			&aggregateVersion, &raw, &occurred); err != nil {
			return nil, err
		}
		ev.OccurredAt = mustTime(occurred)
		var p streamPayload
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		ev.RunSeq = p.RunSeq
		ev.AgentID = p.AgentID
		// Pre-0015 stream payloads have no agent identity; run events from
		// those rows are legacy main-agent events just like run_events rows.
		if ev.RunSeq > 0 && ev.AgentID == "" {
			ev.AgentID = "main"
		}
		ev.Actor = p.Actor
		ev.CorrelationID = p.CorrelationID
		ev.Data = p.Data
		// 0031 makes aggregate_version durable. Legacy rows are deliberately
		// read as version 0; zero is the explicit compatibility value for
		// historical events that never carried an aggregate version.
		ev.Aggregate = domain.AggregateRef{Type: ev.AggregateType, ID: ev.AggregateID, Version: aggregateVersion}
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
	return r.AppendActivityFor(ctx, workspaceID, "", kind, message)
}

// AppendActivityFor 带 work item 归因写入（M4）；workItemID 空串落 NULL。
func (r *EventRepo) AppendActivityFor(ctx context.Context, workspaceID, workItemID, kind, message string) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO activities(id, workspace_id, work_item_id, kind, message, occurred_at) VALUES (?,?,?,?,?,?)`,
		domain.NewID(domain.PrefixEvent), workspaceID, nullString(workItemID), kind, message, timeParam(timeNow()))
	return r.store.mapErr(err)
}

func (r *EventRepo) ListActivities(ctx context.Context, workspaceID string, limit int) ([]application.Activity, error) {
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT id, work_item_id, kind, message, occurred_at FROM activities
		 WHERE workspace_id=? ORDER BY occurred_at DESC LIMIT ?`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []application.Activity
	for rows.Next() {
		var a application.Activity
		var workItemID *string
		var occurred scanTime
		if err := rows.Scan(&a.ID, &workItemID, &a.Kind, &a.Message, &occurred); err != nil {
			return nil, err
		}
		if workItemID != nil {
			a.WorkItemID = *workItemID
		}
		a.OccurredAt = mustTime(occurred)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListRunEvents 按 run_seq 正序回放；payload 为 JSON 文本（可空）。
func (r *EventRepo) ListRunEvents(ctx context.Context, runID string) ([]application.RunEvent, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT run_seq, agent_id, event_type, payload, occurred_at FROM run_events WHERE run_id=? ORDER BY run_seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []application.RunEvent
	for rows.Next() {
		var e application.RunEvent
		var payload *string
		var agentID *string
		var occurred scanTime
		if err := rows.Scan(&e.RunSeq, &agentID, &e.EventType, &payload, &occurred); err != nil {
			return nil, err
		}
		if agentID == nil || *agentID == "" {
			e.AgentID = "main"
		} else {
			e.AgentID = *agentID
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

// A process can die after claiming a key and before Complete/Release. Claims
// whose explicit expiry is older than this bounded lease are reclaimable only for the same request hash;
// a different hash remains a conflict forever. Live HTTP handlers renew their
// owner token, so a long operation is not mistaken for a crashed process.
const idempotencyClaimLease = 15 * time.Minute

// fetch 读回一条幂等记录；不存在返回 (nil, nil)。
// status_code 为 NULL（claim 占位中，见 Claim）时 StatusCode 读出为 0。
func (r *IdempotencyRepo) fetch(ctx context.Context, workspaceID, key string) (*application.IdempotencyRecord, error) {
	rec := &application.IdempotencyRecord{}
	var resultRef *string
	var statusCode sql.NullInt64
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT request_hash, status_code, result_ref FROM idempotency_keys
		 WHERE workspace_id=? AND key=?`, workspaceID, key).
		Scan(&rec.RequestHash, &statusCode, &resultRef)
	if err != nil {
		if err = r.store.mapErr(err); err == domain.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	rec.StatusCode = int(statusCode.Int64)
	if resultRef != nil {
		rec.ResultBody = *resultRef
	}
	return rec, nil
}

func (r *IdempotencyRepo) Check(ctx context.Context, workspaceID, key string) (*application.IdempotencyRecord, error) {
	return r.fetch(ctx, workspaceID, key)
}

func (r *IdempotencyRepo) Record(ctx context.Context, workspaceID, key string, rec application.IdempotencyRecord) error {
	if rec.StatusCode < 100 || rec.StatusCode > 599 {
		return domain.ErrValidation
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO idempotency_keys(workspace_id, key, request_hash, result_ref, status_code, claim_token, claim_expires_at, created_at)
		 VALUES (?,?,?,?,?,NULL,NULL,?) ON CONFLICT (workspace_id, key) DO NOTHING`,
		workspaceID, key, rec.RequestHash, rec.ResultBody, rec.StatusCode, timeParam(timeNow()))
	return r.store.mapErr(err)
}

// Claim 是 claim-first 幂等协议的占位原语（0037 claim_token/claim_expires_at）：
// 利用现有列表达状态——行存在即占位，status_code IS NULL 表示执行中，非 NULL 表示已完成；
// 同 hash 的过期 NULL 占位可被一次性回收，防止进程崩溃留下永久 in_progress。
//
//	(true, nil, nil)  → 占位成功，调用方独占执行权（此后 Complete 或 Release）；
//	(false, rec, nil) → 同 key 行已存在：rec.StatusCode > 0 为已完成响应（可重放），
//	                     rec.StatusCode == 0 表示同 hash 请求仍在执行中。
//
// 若并发对手刚好在占位冲突与读回之间 Release 了行（窗口极小），重试一次 INSERT。
func (r *IdempotencyRepo) Claim(ctx context.Context, workspaceID, key, requestHash string) (bool, *application.IdempotencyRecord, string, error) {
	for attempt := 0; attempt < 2; attempt++ {
		claimToken := domain.NewID("idemclaim_")
		now := timeNow()
		res, err := r.store.execStmt(ctx, r.store.exec(ctx),
			`INSERT INTO idempotency_keys(workspace_id, key, request_hash, result_ref, status_code, claim_token, claim_expires_at, created_at)
			 VALUES (?,?,?,NULL,NULL,?,?,?) ON CONFLICT (workspace_id, key) DO NOTHING`,
			workspaceID, key, requestHash, claimToken, timeParam(now.Add(idempotencyClaimLease)), timeParam(now))
		if err != nil {
			return false, nil, "", r.store.mapErr(err)
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			return true, nil, claimToken, nil
		}
		rec, err := r.fetch(ctx, workspaceID, key)
		if err != nil {
			return false, nil, "", err
		}
		if rec != nil {
			if rec.StatusCode == 0 && rec.RequestHash == requestHash {
				res, err := r.store.execStmt(ctx, r.store.exec(ctx),
					`DELETE FROM idempotency_keys
					 WHERE workspace_id=? AND key=? AND request_hash=? AND status_code IS NULL AND claim_expires_at<?`,
					workspaceID, key, requestHash,
					timeParam(now))
				if err != nil {
					return false, nil, "", r.store.mapErr(err)
				}
				if n, _ := res.RowsAffected(); n > 0 {
					// The next loop performs the replacement claim. A competing
					// claimant is serialized by SQLite and will observe its fresh
					// row as in-progress.
					continue
				}
			}
			return false, rec, "", nil
		}
	}
	return false, nil, "", domain.ErrIdempotencyConflict
}

// Complete 把执行结果写回占位行；request hash + claim token 共同构成
// owner fence，旧请求不能覆盖被回收后重新取得的同一 key。
func (r *IdempotencyRepo) Complete(ctx context.Context, workspaceID, key, requestHash, claimToken string, statusCode int, resultBody string) error {
	if claimToken == "" || requestHash == "" {
		return domain.ErrIdempotencyClaimLost
	}
	if statusCode < 100 || statusCode > 599 {
		return domain.ErrValidation
	}
	result, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE idempotency_keys SET result_ref=?, status_code=?, claim_token=NULL, claim_expires_at=NULL
		 WHERE workspace_id=? AND key=? AND request_hash=? AND claim_token=? AND status_code IS NULL`,
		resultBody, statusCode, workspaceID, key, requestHash, claimToken)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return domain.ErrIdempotencyClaimLost
	}
	return nil
}

// Renew 延长仍由当前请求持有的 claim lease，避免长操作被误判为崩溃。
func (r *IdempotencyRepo) Renew(ctx context.Context, workspaceID, key, requestHash, claimToken string) error {
	if claimToken == "" || requestHash == "" {
		return domain.ErrIdempotencyClaimLost
	}
	now := timeNow()
	result, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE idempotency_keys SET claim_expires_at=?
		 WHERE workspace_id=? AND key=? AND request_hash=? AND claim_token=? AND status_code IS NULL`,
		timeParam(now.Add(idempotencyClaimLease)), workspaceID, key, requestHash, claimToken)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return domain.ErrIdempotencyClaimLost
	}
	return nil
}

// Release 删除当前请求持有的未完成占位；exec 返回 5xx 时调用，允许客户端
// 以同 key 重试。旧 owner 不得删除 replacement claim。
func (r *IdempotencyRepo) Release(ctx context.Context, workspaceID, key, requestHash, claimToken string) error {
	if claimToken == "" || requestHash == "" {
		return domain.ErrIdempotencyClaimLost
	}
	result, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`DELETE FROM idempotency_keys
		 WHERE workspace_id=? AND key=? AND request_hash=? AND claim_token=? AND status_code IS NULL`,
		workspaceID, key, requestHash, claimToken)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return domain.ErrIdempotencyClaimLost
	}
	return nil
}
