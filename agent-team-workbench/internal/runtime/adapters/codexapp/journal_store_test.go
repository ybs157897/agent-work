package codexapp

// 存储层全链验收：adapter 相位事件 → ModuleRunner（runnerCallbacks journal）→
// 真实 sqlite internal 分道。断言用 ListRunEventsIncludeInternal（调试回放），
// 并回归断言 surface 回放（ListRunEvents）过滤 internal——「日志不进 SSE/回放」
// 的可见性分层在 runtime 埋点接上真实存储后仍然成立。

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/migtest"
	"github.com/ybs/agent-team-workbench/internal/observability"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// journalStoreSink 以真实 sqlite 承接 RecordRunEvent（与 application.emit 的
// run 事件路径同构：CanonicalEvent + RunEventRecord 同事务 Append），Run 状态
// 机权威保留在内存 domain（状态持久化是 application 领地，不在本验收面）。
type journalStoreSink struct {
	t     *testing.T
	store *sqlstore.Store

	mu  sync.Mutex
	run *domain.ExecutionRun
}

func (s *journalStoreSink) RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run.Transition(to, time.Now().UTC())
}

func (s *journalStoreSink) RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error {
	ev, err := domain.NewCanonicalEvent("ws_journal", evType,
		domain.AggregateExecutionRun, runID, 1, data)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(txctx context.Context) error {
		_, err := s.store.Events().Append(txctx, ev,
			&application.RunEventRecord{RunID: runID, EventType: evType, Payload: data})
		return err
	})
}

func (s *journalStoreSink) RecordRunProgress(ctx context.Context, runID string, progress float64) error {
	return nil
}

func (s *journalStoreSink) RecordRunSessionUpdate(ctx context.Context, runID string, update atwruntime.SessionUpdate) error {
	return nil
}

func (s *journalStoreSink) RecordRunUsage(ctx context.Context, runID string, usage atwruntime.Usage) error {
	return nil
}

func (s *journalStoreSink) RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error) {
	return nil, nil
}

func (s *journalStoreSink) Run(ctx context.Context, id string) (*domain.ExecutionRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := *s.run
	return &copied, nil
}

func (s *journalStoreSink) status() domain.RunStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run.Status
}

// openJournalStore 起全量迁移的临时库并种子 workspace/work item/run（SQL 形态
// 对齐 persistence/sqlstore 的回放测试种子；internal 事件要求 run 行存在）。
func openJournalStore(t *testing.T, run *domain.ExecutionRun) *sqlstore.Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "journal_e2e.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`INSERT INTO workspaces(id, name, timezone, version, created_at, updated_at) VALUES ('ws_journal','wk','UTC',1,?,?)`,
		now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO work_items(id, workspace_id, record_kind, title, status, priority, version, created_at, updated_at)
			 VALUES ('wi_journal','ws_journal','task','t','todo','medium',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO execution_runs(id, workspace_id, work_item_id, status, version, created_at, updated_at)
			 VALUES (?, 'ws_journal', 'wi_journal', 'queued', 1, ?, ?)`, run.ID, now, now); err != nil {
		t.Fatal(err)
	}
	return sqlstore.New(db)
}

// journalEvents 从存储回放相位事件（event_type 与 phase 的交错序列）。
func journalEvents(events []application.RunEvent) []string {
	var out []string
	for _, ev := range events {
		switch ev.EventType {
		case domain.EventRunPhaseEntered, domain.EventRunPhaseClosed:
			p, _ := ev.Payload["phase"].(string)
			name := "entered"
			if ev.EventType == domain.EventRunPhaseClosed {
				name = "closed"
			}
			out = append(out, name+"("+p+")")
		}
	}
	return out
}

func subsequence(got, want []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

// TestJournalPhaseChainThroughStore：完整 run 的七段相位证据（本波覆盖
// spawn/handshake/first_event/streaming/settle）经真实存储落 run_events，
// run_seq 严格递增、surface 回放过滤 internal。
func TestJournalPhaseChainThroughStore(t *testing.T) {
	run := newRun(nil)
	store := openJournalStore(t, run)
	sink := &journalStoreSink{t: t, store: store, run: run}
	runner := atwruntime.NewModuleRunner(sink)
	runner.Register("codex-appserver", newTestModule(t))
	if err := runner.Dispatch(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	// 等终态 + settle closed 落库（closed 在终态写入之后发出）。
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if sink.status().IsTerminal() {
			events, err := store.Events().ListRunEventsIncludeInternal(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := findPhase(events, domain.EventRunPhaseClosed, observability.PhaseSettle); ok {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := sink.status(); got != domain.RunSucceeded {
		t.Fatalf("期望 succeeded，实际 %s", got)
	}

	// 调试回放（internal 可见）：相位成对、顺序正确、run_seq 严格递增。
	all, err := store.Events().ListRunEventsIncludeInternal(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	seq := journalEvents(all)
	// entered(settle) 先于 closed(streaming) 是真实时序：codexapp 在 Execute 内
	// 的 composeResult 裁决入口发 settle entered，而 streaming 由 ModuleRunner
	// 在 Execute 返回后收口。
	want := []string{
		"entered(spawn)", "closed(spawn)",
		"entered(handshake)", "closed(handshake)",
		"entered(first_event)", "closed(first_event)",
		"entered(streaming)", "entered(settle)",
		"closed(streaming)", "closed(settle)",
	}
	if !subsequence(seq, want) {
		t.Fatalf("相位链漂移，缺少 %v；实际 %v", want, seq)
	}
	var lastSeq int64
	for i, ev := range all {
		if ev.RunSeq <= 0 {
			t.Fatalf("第 %d 个事件缺少 run_seq: %+v", i, ev.EventType)
		}
		if ev.RunSeq <= lastSeq {
			t.Fatalf("run_seq 应严格递增: %d then %d", lastSeq, ev.RunSeq)
		}
		lastSeq = ev.RunSeq
	}

	// 载荷抽查：spawn closed 带 pid/pgid，settle closed 带实际落点终态。
	// （payload 经 sqlite JSON 往返，数值一律 float64。）
	spawnClosed, ok := findPhase(all, domain.EventRunPhaseClosed, observability.PhaseSpawn)
	if !ok {
		t.Fatal("缺少 spawn closed")
	}
	if pid, _ := spawnClosed.Payload["pid"].(float64); pid <= 0 {
		t.Fatalf("spawn closed 应带 pid: %+v", spawnClosed.Payload)
	}
	settleClosed, _ := findPhase(all, domain.EventRunPhaseClosed, observability.PhaseSettle)
	if settleClosed.Payload["terminal_status"] != string(domain.RunSucceeded) {
		t.Fatalf("settle closed 应带 terminal_status: %+v", settleClosed.Payload)
	}
	handshakeEntered, _ := findPhase(all, domain.EventRunPhaseEntered, observability.PhaseHandshake)
	if resume, _ := handshakeEntered.Payload["resume"].(bool); resume {
		t.Fatalf("fresh run 的 handshake entered 不应带 resume: %+v", handshakeEntered.Payload)
	}

	// surface 回放（回归断言）：internal 相位/日志事件不可见。
	surface, err := store.Events().ListRunEvents(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range surface {
		if domain.IsInternalEventName(ev.EventType) {
			t.Fatalf("surface 回放泄漏 internal 事件: %s", ev.EventType)
		}
	}
}

func findPhase(events []application.RunEvent, eventType, phase string) (application.RunEvent, bool) {
	for _, ev := range events {
		if ev.EventType != eventType {
			continue
		}
		if p, _ := ev.Payload["phase"].(string); p == phase {
			return ev, true
		}
	}
	return application.RunEvent{}, false
}
