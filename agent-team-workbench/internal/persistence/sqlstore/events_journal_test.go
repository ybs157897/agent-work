package sqlstore_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/migtest"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

// openJournalTestDB 起一个全量迁移的临时库并完成 workspace/work item/run 种子。
func openJournalTestDB(t *testing.T) (*sql.DB, *sqlstore.Store) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "journal.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	seedWorkspace(t, db)
	insertWorkItem(t, db, "wi_journal")
	seedDispatchRun(t, db, "run_journal", "wi_journal", "")
	return db, sqlstore.New(db)
}

// internal 类事件（Run Journal）必须只落 run_events：stream_events 与
// outbox_messages 都不该有它的行；surface 回放（ListRunEvents）必须过滤，
// 调试回放（ListRunEventsIncludeInternal）必须可见。
func TestInternalEventsBypassStreamAndOutbox(t *testing.T) {
	ctx := context.Background()
	db, store := openJournalTestDB(t)

	surface, err := domain.NewCanonicalEvent("ws_wk", domain.EventMessageDelta,
		domain.AggregateExecutionRun, "run_journal", 1, map[string]any{"role": "assistant", "text": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	internal, err := domain.NewCanonicalEvent("ws_wk", domain.EventRunPhaseEntered,
		domain.AggregateExecutionRun, "run_journal", 1, map[string]any{"phase": "handshake", "attempt": 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InTx(ctx, func(txctx context.Context) error {
		if _, err := store.Events().Append(txctx, surface,
			&application.RunEventRecord{RunID: "run_journal", EventType: surface.Type, Payload: surface.Data}); err != nil {
			return err
		}
		_, err := store.Events().Append(txctx, internal,
			&application.RunEventRecord{RunID: "run_journal", EventType: internal.Type, Payload: internal.Data})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if internal.StreamSeq != 0 {
		t.Fatalf("internal event must not get a stream_seq, got %d", internal.StreamSeq)
	}
	if internal.RunSeq == 0 {
		t.Fatal("internal event must still get a run_seq")
	}

	var streamCount, outboxCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM stream_events WHERE event_type=?`, domain.EventRunPhaseEntered).Scan(&streamCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM outbox_messages WHERE event_id=?`, internal.EventID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if streamCount != 0 || outboxCount != 0 {
		t.Fatalf("internal event leaked to stream/outbox: stream=%d outbox=%d", streamCount, outboxCount)
	}

	surfaceEvents, err := store.Events().ListRunEvents(ctx, "run_journal")
	if err != nil {
		t.Fatal(err)
	}
	if len(surfaceEvents) != 1 || surfaceEvents[0].EventType != domain.EventMessageDelta {
		t.Fatalf("surface replay must hide internal events: %+v", surfaceEvents)
	}
	all, err := store.Events().ListRunEventsIncludeInternal(ctx, "run_journal")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("debug replay must include internal events: %+v", all)
	}
	if all[0].EventType != domain.EventMessageDelta || all[1].EventType != domain.EventRunPhaseEntered {
		t.Fatalf("run_seq ordering must be append order: %+v", all)
	}
}

// internal 事件脱离 run 维度没有意义：runEvent 为 nil 时响亮拒绝。
func TestInternalEventRequiresRunRecord(t *testing.T) {
	ctx := context.Background()
	_, store := openJournalTestDB(t)

	ev, err := domain.NewCanonicalEvent("ws_wk", domain.EventRunLogChunk,
		domain.AggregateExecutionRun, "run_journal", 1, map[string]any{"stream": "stderr", "chunk": "x"})
	if err != nil {
		t.Fatal(err)
	}
	err = store.InTx(ctx, func(txctx context.Context) error {
		_, err := store.Events().Append(txctx, ev, nil)
		return err
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("internal event without run record must fail validation, got %v", err)
	}
}
