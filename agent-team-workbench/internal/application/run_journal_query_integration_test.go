package application_test

// Run Journal 查询面（设计 §4 playbook / §5 M3，GET /runs/{run_id}/journal 的
// Service 装配验收；与 run_journal_integration_test.go 的 M1 写入侧埋点验收
// 互不重叠）：
//   - phase_entered/phase_closed 依 run_seq 配对；只有 entered 的环节保留
//     未闭合形态（closed_at/outcome/duration_ms=null）——即故障点消费形态；
//   - 无配对 entered 的 closed 不凭空造环节；
//   - log 段统计 run.log_chunk 条数与 truncated 标记；
//   - decisions 投影 run.decision（kind/reason/occurred_at/link_run_id/inputs）；
//   - governance 互链：turn_receipt_phases.run_ids[] 含本 run 的最新 turn，
//     无治理引用为 null。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/observability"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

// seedJournalRunForQuery 直插一条 queued run（绕过 CreateRun 的执行副作用）。
func seedJournalRunForQuery(t *testing.T, ctx context.Context, store *sqlstore.Store, workspaceID, workItemID string) string {
	t.Helper()
	now := time.Now().UTC()
	run := &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), WorkspaceID: workspaceID, WorkItemID: workItemID,
		Status: domain.RunQueued, Version: 1,
		Input:     map[string]any{"instruction": "journal 调试面"},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Runs().Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

func TestGetRunJournalAssemblesPhaseChain(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	runID := seedJournalRunForQuery(t, ctx, store, workspaceID, rootID)

	// 播种走 RecordRunEvent 同一核心（Journal 的生产写入口）。
	record := func(evType string, data map[string]any) {
		t.Helper()
		if err := svc.RecordRunEvent(ctx, runID, evType, data); err != nil {
			t.Fatal(err)
		}
	}
	record(domain.EventRunPhaseEntered, observability.PhaseEnteredPayload(observability.PhaseDispatch, 1,
		map[string]any{"host_id": "host_local"}))
	record(domain.EventRunPhaseClosed, observability.PhaseClosedPayload(observability.PhaseDispatch, observability.PhaseOK,
		nil, 12, map[string]any{"lease_id": "lease_1"}))
	record(domain.EventRunPhaseEntered, observability.PhaseEnteredPayload(observability.PhaseHandshake, 1,
		map[string]any{"session_ref": "session_1"}))
	record(domain.EventRunLogChunk, observability.LogChunkPayload("stderr", "probe failed", false))
	record(domain.EventRunLogChunk, observability.LogChunkPayload("stderr", "tail cut", true))
	record(domain.EventRunDecision, observability.DecisionPayload(observability.DecisionSelfHealRetry,
		"session_unknown triggers fresh retry", map[string]any{"failure_code": "session_unknown"}, "run_prev"))

	journal, err := svc.GetRunJournal(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.RunID != runID || journal.GeneratedAt.IsZero() {
		t.Fatalf("journal 头部不对: %+v", journal)
	}
	if len(journal.Phases) != 2 {
		t.Fatalf("应有 2 个环节（含一个未闭合），实际 %d: %+v", len(journal.Phases), journal.Phases)
	}

	dispatch := journal.Phases[0]
	if dispatch.Phase != observability.PhaseDispatch || dispatch.Attempt != 1 {
		t.Fatalf("dispatch 环节头不对: %+v", dispatch)
	}
	if dispatch.ClosedAt == nil || dispatch.Outcome == nil || *dispatch.Outcome != string(observability.PhaseOK) {
		t.Fatalf("dispatch 应闭合 ok: %+v", dispatch)
	}
	if dispatch.DurationMS == nil || *dispatch.DurationMS != 12 {
		t.Fatalf("dispatch duration_ms 应为 12: %+v", dispatch)
	}
	if dispatch.Failure != nil {
		t.Fatalf("ok 环节 failure 应为 null: %+v", dispatch.Failure)
	}
	if dispatch.Detail["lease_id"] != "lease_1" {
		t.Fatalf("closed detail 应带 lease_id: %+v", dispatch.Detail)
	}
	if _, has := dispatch.Detail["phase"]; has {
		t.Fatalf("detail 不得携带保留键 phase: %+v", dispatch.Detail)
	}

	// 未闭合环节：closed_at/outcome/duration_ms 全 null，detail 取 entered 载荷。
	handshake := journal.Phases[1]
	if handshake.Phase != observability.PhaseHandshake || handshake.Attempt != 1 {
		t.Fatalf("handshake 环节头不对: %+v", handshake)
	}
	if handshake.ClosedAt != nil || handshake.Outcome != nil || handshake.DurationMS != nil {
		t.Fatalf("未闭合环节 closed_at/outcome/duration_ms 应全 null: %+v", handshake)
	}
	if handshake.Detail["session_ref"] != "session_1" {
		t.Fatalf("未闭合 detail 应取 entered 载荷: %+v", handshake.Detail)
	}

	if journal.Log.Chunks != 2 || !journal.Log.Truncated {
		t.Fatalf("log 统计应 chunks=2 truncated=true: %+v", journal.Log)
	}

	if len(journal.Decisions) != 1 {
		t.Fatalf("应有 1 条 decision: %+v", journal.Decisions)
	}
	decision := journal.Decisions[0]
	if decision.Kind != observability.DecisionSelfHealRetry || decision.Reason != "session_unknown triggers fresh retry" {
		t.Fatalf("decision kind/reason 不对: %+v", decision)
	}
	if decision.LinkRunID != "run_prev" || decision.OccurredAt.IsZero() {
		t.Fatalf("decision link/occurred_at 不对: %+v", decision)
	}
	if decision.Inputs["failure_code"] != "session_unknown" {
		t.Fatalf("decision inputs 应带 failure_code: %+v", decision.Inputs)
	}

	// 无治理引用：governance=null。
	if journal.Governance != nil {
		t.Fatalf("无 receipt 引用的 run governance 应为 null: %+v", journal.Governance)
	}

	// run 不存在 → ErrNotFound（HTTP 404）。
	if _, err := svc.GetRunJournal(ctx, "run_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("缺失 run 应 ErrNotFound，实际 %v", err)
	}
}

func TestGetRunJournalClosedWithoutEnteredIsIgnored(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	runID := seedJournalRunForQuery(t, ctx, store, workspaceID, rootID)
	// 退化序列：无 entered 的 closed 不得凭空造环节（journal 是投影不是校验器）。
	if err := svc.RecordRunEvent(ctx, runID, domain.EventRunPhaseClosed,
		observability.PhaseClosedPayload(observability.PhaseSpawn, observability.PhaseFailed,
			&observability.PhaseFailure{Code: "spawn_bin", Message: "binary missing", Retryable: false}, 3, nil)); err != nil {
		t.Fatal(err)
	}
	journal, err := svc.GetRunJournal(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Phases) != 0 {
		t.Fatalf("无配对 entered 的 closed 不得产生环节: %+v", journal.Phases)
	}
}

func TestGetRunJournalLinksLatestGovernanceTurn(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	runID := seedJournalRunForQuery(t, ctx, store, workspaceID, rootID)
	goal, todo := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)

	// 旧 turn：todo1 的 turn 1，phase1 带 run_ids=[runID]。
	olderAt := time.Now().UTC().Add(-2 * time.Minute)
	claimed1, err := svc.ClaimTodo(ctx, todo.ID, "agent_governance_owner", todo.Version, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	header1 := &domain.TurnReceiptHeader{
		TurnKey: domain.TurnKey{GoalID: goal.ID, TodoID: todo.ID, TurnSeq: 1},
		Attempt: 1, SchemaVersion: "turn-receipt/v1",
		InputSnapshotDigest: governanceDigest('a'), AdmissionClientKey: "journal-admit-older",
		CreatedAt: olderAt,
	}
	if header1.CanonicalDigest, err = application.ComputeTurnReceiptHeaderDigest(header1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TurnReceipts().Admit(ctx, header1, "agent_governance_owner", claimed1.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendTurnReceiptPhase(ctx, &domain.TurnReceiptPhase{
		TurnKey: header1.TurnKey, PhaseSeq: 1, Phase: domain.TurnReceiptPhaseDecisionDecode,
		Payload: map[string]any{"accepted": true}, RunIDs: []string{runID},
	}); err != nil {
		t.Fatal(err)
	}

	// 新 turn：第二个 todo 的 turn 1，同样引用 runID——反查必须取最新（created_at）。
	now := time.Now().UTC()
	secondary := &domain.Todo{
		ID: domain.NewID(domain.PrefixTodo), GoalID: goal.ID, Class: domain.TodoValidation,
		Status: domain.TodoPending, Instruction: "validate the secondary branch",
		Acceptance: []string{"secondary validation references the same run"}, Priority: domain.PriorityMedium,
		Predecessors: []string{}, Successors: []string{},
		DecisionScope: domain.DecisionScope{
			WorkItemIDs: []string{rootID}, AgentIDs: []string{"agent_governance_owner"},
			RuntimeCapabilities: []string{}, WriteScopes: []string{}, MaxDispatch: 1,
		}, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Todos().Create(ctx, secondary); err != nil {
		t.Fatal(err)
	}
	claimed2, err := svc.ClaimTodo(ctx, secondary.ID, "agent_governance_owner", secondary.Version, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	header2 := &domain.TurnReceiptHeader{
		TurnKey: domain.TurnKey{GoalID: goal.ID, TodoID: secondary.ID, TurnSeq: 1},
		Attempt: 1, SchemaVersion: "turn-receipt/v1",
		InputSnapshotDigest: governanceDigest('b'), AdmissionClientKey: "journal-admit-newer",
		CreatedAt: olderAt.Add(time.Minute),
	}
	if header2.CanonicalDigest, err = application.ComputeTurnReceiptHeaderDigest(header2); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TurnReceipts().Admit(ctx, header2, "agent_governance_owner", claimed2.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendTurnReceiptPhase(ctx, &domain.TurnReceiptPhase{
		TurnKey: header2.TurnKey, PhaseSeq: 1, Phase: domain.TurnReceiptPhaseDecisionDecode,
		Payload: map[string]any{"accepted": true}, RunIDs: []string{runID},
	}); err != nil {
		t.Fatal(err)
	}

	journal, err := svc.GetRunJournal(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Governance == nil {
		t.Fatal("被 receipt 引用的 run 应带 governance 互链")
	}
	want := &application.RunJournalGovernance{
		GoalID: goal.ID, TodoID: secondary.ID, TurnSeq: 1, Digest: header2.CanonicalDigest,
	}
	if journal.Governance.GoalID != want.GoalID || journal.Governance.TodoID != want.TodoID ||
		journal.Governance.TurnSeq != want.TurnSeq || journal.Governance.Digest != want.Digest {
		t.Fatalf("governance 互链应取最新 turn %+v，实际 %+v", want, journal.Governance)
	}
}
