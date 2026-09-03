package sqlstore_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func governanceAnchorForTest() *domain.ProviderUsageAnchorV1 {
	value := int64(42)
	return &domain.ProviderUsageAnchorV1{
		SchemaVersion: domain.ProviderUsageAnchorSchemaVersionV1,
		State:         domain.ProviderUsageAnchorReady, AdapterID: "mock", SessionRef: "mock://session-1",
		ContextGeneration: 1, SegmentSeq: 2,
		Counters:    domain.UsageCountersV1{InputTokensTotal: &value},
		SourceRunID: "run_gov_anchor", ObservedAt: time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC),
	}
}

func governanceInt64PtrEqual(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func governanceCountersEqual(a, b domain.UsageCountersV1) bool {
	return governanceInt64PtrEqual(a.InputTokensTotal, b.InputTokensTotal) &&
		governanceInt64PtrEqual(a.InputUncachedTokens, b.InputUncachedTokens) &&
		governanceInt64PtrEqual(a.CacheReadTokens, b.CacheReadTokens) &&
		governanceInt64PtrEqual(a.CacheWriteTokens, b.CacheWriteTokens) &&
		governanceInt64PtrEqual(a.OutputTokens, b.OutputTokens)
}

func governanceAnchorEqual(a, b *domain.ProviderUsageAnchorV1) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.SchemaVersion == b.SchemaVersion && a.State == b.State &&
		a.AdapterID == b.AdapterID && a.SessionRef == b.SessionRef &&
		a.ContextGeneration == b.ContextGeneration && a.SegmentSeq == b.SegmentSeq &&
		a.SourceRunID == b.SourceRunID && a.InvalidationReason == b.InvalidationReason &&
		a.ObservedAt.Equal(b.ObservedAt) && governanceCountersEqual(a.Counters, b.Counters)
}

func TestTaskSessionUpdateProviderUsageAnchorCAS(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	now := time.Now().UTC()
	if err := store.WorkItems().Create(ctx, &domain.WorkItem{
		ID: "wi_gov_usage", WorkspaceID: "ws_wk", RecordKind: domain.RecordKindTask,
		Title: "provider usage anchor", Status: domain.WorkItemTodo, Priority: domain.PriorityMedium,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Runs().Create(ctx, &domain.ExecutionRun{
		ID: "run_gov_anchor", WorkspaceID: "ws_wk", WorkItemID: "wi_gov_usage",
		Status: domain.RunQueued, Input: map[string]any{}, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	inserted, err := store.TaskSessions().InsertIfAbsent(ctx, &domain.TaskSession{
		ID: "ts_gov_usage", WorkspaceID: "ws_wk", AgentProfileID: "", AdapterID: "mock",
		TaskKey: "wi_gov_usage", LastRunID: "run_gov_anchor", AnchorRunSequence: 1,
		SessionParams: map[string]any{}, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || !inserted {
		t.Fatalf("seed session: inserted=%v err=%v", inserted, err)
	}

	if _, err := store.TaskSessions().UpdateProviderUsageAnchorCAS(ctx, "ws_wk", "", "mock", "wi_gov_usage", nil, 0, "run_gov_anchor", 1); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("nil anchor 应拒绝: %v", err)
	}
	if _, err := store.TaskSessions().UpdateProviderUsageAnchorCAS(ctx, "ws_wk", "", "mock", "wi_gov_usage", governanceAnchorForTest(), -1, "run_gov_anchor", 1); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("负 expectedSeq 应拒绝: %v", err)
	}
	if _, err := store.TaskSessions().UpdateProviderUsageAnchorCAS(ctx, "ws_wk", "", "mock", "wi_gov_usage", governanceAnchorForTest(), int64(^uint64(0)>>1), "run_gov_anchor", 1); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("达到 int64 上限的 expectedSeq 应拒绝，避免序列回绕: %v", err)
	}

	first := governanceAnchorForTest()
	ok, err := store.TaskSessions().UpdateProviderUsageAnchorCAS(ctx, "ws_wk", "", "mock", "wi_gov_usage", first, 0, "run_gov_anchor", 1)
	if err != nil || !ok {
		t.Fatalf("首装 anchor 应成功: ok=%v err=%v", ok, err)
	}
	got, err := store.TaskSessions().Get(ctx, "ws_wk", "", "mock", "wi_gov_usage")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderUsageAnchorSeq != 1 || !governanceAnchorEqual(got.ProviderUsageAnchor, first) {
		t.Fatalf("首装往返失真: seq=%d anchor=%+v", got.ProviderUsageAnchorSeq, got.ProviderUsageAnchor)
	}

	ok, err = store.TaskSessions().UpdateProviderUsageAnchorCAS(ctx, "ws_wk", "", "mock", "wi_gov_usage", governanceAnchorForTest(), 0, "run_gov_anchor", 1)
	if err != nil || ok {
		t.Fatalf("过期 expectedSeq 应 CAS miss: ok=%v err=%v", ok, err)
	}
	unchanged, err := store.TaskSessions().Get(ctx, "ws_wk", "", "mock", "wi_gov_usage")
	if err != nil || unchanged.ProviderUsageAnchorSeq != 1 || !governanceAnchorEqual(unchanged.ProviderUsageAnchor, first) {
		t.Fatalf("CAS miss 不得改行: seq=%d err=%v", unchanged.ProviderUsageAnchorSeq, err)
	}

	second := governanceAnchorForTest()
	second.SessionRef = "mock://session-2"
	advanced := int64(100)
	second.Counters = domain.UsageCountersV1{InputTokensTotal: &advanced}
	second.SourceRunID = "run_gov_anchor_2"
	ok, err = store.TaskSessions().UpdateProviderUsageAnchorCAS(ctx, "ws_wk", "", "mock", "wi_gov_usage", second, 1, "run_gov_anchor", 1)
	if err != nil || !ok {
		t.Fatalf("expectedSeq=1 应推进: ok=%v err=%v", ok, err)
	}
	got, err = store.TaskSessions().Get(ctx, "ws_wk", "", "mock", "wi_gov_usage")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderUsageAnchorSeq != 2 || !governanceAnchorEqual(got.ProviderUsageAnchor, second) {
		t.Fatalf("第二代 baseline 往返失真: seq=%d anchor=%+v", got.ProviderUsageAnchorSeq, got.ProviderUsageAnchor)
	}
	// Owner fencing is independent from the provider anchor sequence: a stale
	// Run with a matching sequence must not advance the current owner's baseline.
	stale := governanceAnchorForTest()
	stale.Counters.InputTokensTotal = &advanced
	stale.SessionRef = second.SessionRef
	if ok, err := store.TaskSessions().UpdateProviderUsageAnchorCAS(ctx, "ws_wk", "", "mock", "wi_gov_usage", stale, 2, "run_stale_owner", 1); err != nil || ok {
		t.Fatalf("stale anchor owner must be fenced: ok=%v err=%v", ok, err)
	}

	invalidated := *second
	invalidated.State = domain.ProviderUsageAnchorInvalidated
	invalidated.SessionRef = ""
	invalidated.Counters = domain.UsageCountersV1{}
	invalidated.InvalidationReason = "provider session rotated"
	if err := invalidated.Validate(); err != nil {
		t.Fatal(err)
	}
	ok, err = store.TaskSessions().UpdateProviderUsageAnchorCAS(ctx, "ws_wk", "", "mock", "wi_gov_usage", &invalidated, 2, "run_gov_anchor", 1)
	if err != nil || !ok {
		t.Fatalf("ready→invalidated 转换应落库: ok=%v err=%v", ok, err)
	}
	got, err = store.TaskSessions().Get(ctx, "ws_wk", "", "mock", "wi_gov_usage")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderUsageAnchorSeq != 3 || got.ProviderUsageAnchor.State != domain.ProviderUsageAnchorInvalidated ||
		got.ProviderUsageAnchor.InvalidationReason == "" || !governanceAnchorEqual(got.ProviderUsageAnchor, &invalidated) {
		t.Fatalf("invalidated anchor 往返失真: seq=%d anchor=%+v", got.ProviderUsageAnchorSeq, got.ProviderUsageAnchor)
	}

	ok, err = store.TaskSessions().UpdateProviderUsageAnchorCAS(ctx, "ws_wk", "", "mock", "wi_missing", governanceAnchorForTest(), 0, "run_gov_anchor", 1)
	if err != nil || ok {
		t.Fatalf("不存在的行为键应 (false,nil): ok=%v err=%v", ok, err)
	}
}

func insertGovernanceTurnRun(t *testing.T, db *sql.DB, id, workspaceID, workItemID, agentID, status string,
	createdAt time.Time, governance map[string]any) {
	t.Helper()
	input := map[string]any{}
	if governance != nil {
		input["governance"] = governance
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	stamp := createdAt.UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO execution_runs
		(id, workspace_id, work_item_id, agent_profile_id, status, input, version, created_at, updated_at)
		VALUES (?,?,?,?,?,?,1,?,?)`, id, workspaceID, workItemID, agentID, status, string(raw), stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func TestRunRepoListByGovernanceTurn(t *testing.T) {
	ctx := context.Background()
	f := newQuotaStoreFixture(t)
	defer f.close()
	base := time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC)
	hit := map[string]any{"goal_id": f.goal.ID, "todo_id": f.todo.ID, "turn_seq": int64(1)}
	// 故意先插 run_gov_b：created_at 升序断言不能靠插入顺序巧合。
	insertGovernanceTurnRun(t, f.db, "run_gov_b", "ws_wk", f.goal.RootWorkItemID, f.workerID, "queued", base.Add(time.Second), hit)
	insertGovernanceTurnRun(t, f.db, "run_gov_a", "ws_wk", f.goal.RootWorkItemID, f.workerID, "running", base, hit)
	insertGovernanceTurnRun(t, f.db, "run_gov_turn2", "ws_wk", f.goal.RootWorkItemID, f.workerID, "queued", base.Add(2*time.Second),
		map[string]any{"goal_id": f.goal.ID, "todo_id": f.todo.ID, "turn_seq": int64(2)})
	insertGovernanceTurnRun(t, f.db, "run_gov_goal2", "ws_wk", f.goal.RootWorkItemID, f.workerID, "queued", base.Add(3*time.Second),
		map[string]any{"goal_id": "goal_other", "todo_id": f.todo.ID, "turn_seq": int64(1)})
	insertGovernanceTurnRun(t, f.db, "run_gov_plain", "ws_wk", f.goal.RootWorkItemID, f.workerID, "queued", base.Add(4*time.Second), nil)
	stamp := base.Add(5 * time.Second).Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`INSERT INTO workspaces(id,name,timezone,version,created_at,updated_at)
		VALUES ('ws_gov_other','other','UTC',1,?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO work_items(id,workspace_id,record_kind,title,status,priority,version,created_at,updated_at)
		VALUES ('wi_gov_other','ws_gov_other','task','t','todo','medium',1,?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	insertGovernanceTurnRun(t, f.db, "run_gov_ws2", "ws_gov_other", "wi_gov_other", f.workerID, "queued", base.Add(5*time.Second), hit)

	runs, err := f.store.Runs().ListByGovernanceTurn(ctx, "ws_wk", f.goal.ID, f.todo.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID)
	}
	if len(runs) != 2 || ids[0] != "run_gov_a" || ids[1] != "run_gov_b" {
		t.Fatalf("应只含命中两条且按 created_at 升序: %v", ids)
	}
	other, err := f.store.Runs().ListByGovernanceTurn(ctx, "ws_gov_other", f.goal.ID, f.todo.ID, 1)
	if err != nil || len(other) != 1 || other[0].ID != "run_gov_ws2" {
		t.Fatalf("workspace 过滤失效: %v err=%v", other, err)
	}

	if _, err := f.store.Runs().ListByGovernanceTurn(ctx, "", f.goal.ID, f.todo.ID, 1); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("空 workspace 应拒绝: %v", err)
	}
	if _, err := f.store.Runs().ListByGovernanceTurn(ctx, "ws_wk", " ", f.todo.ID, 1); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("空白 goal 应拒绝: %v", err)
	}
	if _, err := f.store.Runs().ListByGovernanceTurn(ctx, "ws_wk", f.goal.ID, "", 1); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("空 todo 应拒绝: %v", err)
	}
	if _, err := f.store.Runs().ListByGovernanceTurn(ctx, "ws_wk", f.goal.ID, f.todo.ID, 0); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("turn_seq=0 应拒绝: %v", err)
	}
}

func TestQuotaSumActiveReservedCountsOnlyReserved(t *testing.T) {
	ctx := context.Background()
	f := newQuotaStoreFixture(t)
	defer f.close()
	reserved := quotaReservationFor(f, domain.QuotaOutputTokens, 10)
	if created, err := f.store.Quotas().Reserve(ctx, reserved); err != nil || !created {
		t.Fatalf("reserved reservation 创建失败: created=%v err=%v", created, err)
	}
	committed := quotaReservationFor(f, domain.QuotaInputTokensTotal, 7)
	if created, err := f.store.Quotas().Reserve(ctx, committed); err != nil || !created {
		t.Fatalf("committed reservation 创建失败: created=%v err=%v", created, err)
	}
	committed.Status = domain.QuotaReservationCommitted
	committed.CommittedAmount = 7
	committed.Version++
	if err := f.store.Quotas().Commit(ctx, committed, 1); err != nil {
		t.Fatal(err)
	}
	released := quotaReservationFor(f, domain.QuotaCacheReadTokens, 5)
	if created, err := f.store.Quotas().Reserve(ctx, released); err != nil || !created {
		t.Fatalf("released reservation 创建失败: created=%v err=%v", created, err)
	}
	released.Status = domain.QuotaReservationReleased
	released.ReleasedAmount = 5
	released.Version++
	if err := f.store.Quotas().Release(ctx, released, 1); err != nil {
		t.Fatal(err)
	}

	if got, err := f.store.Quotas().SumActiveReserved(ctx, f.goal.ID, domain.QuotaOutputTokens); err != nil || got != 10 {
		t.Fatalf("reserved 应计入: got=%d err=%v", got, err)
	}
	if got, err := f.store.Quotas().SumActiveReserved(ctx, f.goal.ID, domain.QuotaInputTokensTotal); err != nil || got != 0 {
		t.Fatalf("committed 不得计入: got=%d err=%v", got, err)
	}
	if got, err := f.store.Quotas().SumActiveReserved(ctx, f.goal.ID, domain.QuotaCacheReadTokens); err != nil || got != 0 {
		t.Fatalf("released 不得计入: got=%d err=%v", got, err)
	}
	if got, err := f.store.Quotas().SumActiveReserved(ctx, f.goal.ID, domain.QuotaCostMicroUSD); err != nil || got != 0 {
		t.Fatalf("无 reservation 的 kind 应返回 0: got=%d err=%v", got, err)
	}
	if got, err := f.store.Quotas().SumActiveReserved(ctx, "goal_none", domain.QuotaOutputTokens); err != nil || got != 0 {
		t.Fatalf("未知 Goal 应返回 0: got=%d err=%v", got, err)
	}
	if _, err := f.store.Quotas().SumActiveReserved(ctx, "", domain.QuotaOutputTokens); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("空 goal 应拒绝: %v", err)
	}
	if _, err := f.store.Quotas().SumActiveReserved(ctx, f.goal.ID, domain.QuotaKind("bogus")); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("非法 kind 应拒绝: %v", err)
	}
}

func TestQuotaListSpendByTurnOrderingAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newQuotaStoreFixture(t)
	defer f.close()
	output := quotaReservationFor(f, domain.QuotaOutputTokens, 10)
	if created, err := f.store.Quotas().Reserve(ctx, output); err != nil || !created {
		t.Fatalf("output reservation 创建失败: created=%v err=%v", created, err)
	}
	input := quotaReservationFor(f, domain.QuotaInputTokensTotal, 8)
	if created, err := f.store.Quotas().Reserve(ctx, input); err != nil || !created {
		t.Fatalf("input reservation 创建失败: created=%v err=%v", created, err)
	}
	// 两个 kind 各一条 terminal Run + canonical usage；spend entry 先落
	// output 后落 input，断言读取按 (quota_kind, run_id) 排序而非插入顺序。
	quotaRun(t, f.db, "run_gov_spend_out", f.goal.RootWorkItemID, f.workerID, "succeeded")
	outDigest := setQuotaRunCanonical(t, f, "run_gov_spend_out", domain.QuotaOutputTokens, 6, false, "")
	quotaRun(t, f.db, "run_gov_spend_in", f.goal.RootWorkItemID, f.workerID, "succeeded")
	inDigest := setQuotaRunCanonical(t, f, "run_gov_spend_in", domain.QuotaInputTokensTotal, 4, false, "")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`INSERT INTO quota_spend_entries
		(goal_id,todo_id,turn_seq,quota_kind,run_id,amount,usage_basis,usage_digest,policy_digest,status,reason,created_at)
		VALUES (?,?,1,'output_tokens','run_gov_spend_out',6,'per_run',?,?,'committed','',?)`,
		f.goal.ID, f.todo.ID, outDigest, quotaStoreDigest('p'), now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO quota_spend_entries
		(goal_id,todo_id,turn_seq,quota_kind,run_id,amount,usage_basis,usage_digest,policy_digest,status,reason,created_at)
		VALUES (?,?,1,'input_tokens_total','run_gov_spend_in',4,'per_run',?,?,'committed','',?)`,
		f.goal.ID, f.todo.ID, inDigest, quotaStoreDigest('p'), now); err != nil {
		t.Fatal(err)
	}

	entries, err := f.store.Quotas().ListSpendByTurn(ctx, f.header.TurnKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("应返回该 Turn 全部两条 spend: %d", len(entries))
	}
	first, second := entries[0], entries[1]
	if first.Key.Kind != domain.QuotaInputTokensTotal || first.Key.RunID != "run_gov_spend_in" ||
		second.Key.Kind != domain.QuotaOutputTokens || second.Key.RunID != "run_gov_spend_out" {
		t.Fatalf("排序应按 (quota_kind, run_id): %+v", entries)
	}
	if first.Amount != 4 || first.UsageBasis != "per_run" || first.UsageDigest != inDigest ||
		first.PolicyDigest != quotaStoreDigest('p') || first.Status != domain.QuotaSpendCommitted ||
		first.Key.TurnKey.GoalID != f.goal.ID || first.Key.TurnKey.TodoID != f.todo.ID || first.Key.TurnKey.TurnSeq != 1 {
		t.Fatalf("spend 往返失真: %+v", first)
	}
	if second.Amount != 6 || second.UsageBasis != "per_run" || second.UsageDigest != outDigest ||
		second.Status != domain.QuotaSpendCommitted {
		t.Fatalf("spend 往返失真: %+v", second)
	}

	empty, err := f.store.Quotas().ListSpendByTurn(ctx, domain.TurnKey{GoalID: f.goal.ID, TodoID: f.todo.ID, TurnSeq: 2})
	if err != nil || len(empty) != 0 {
		t.Fatalf("无 spend 的 Turn 应返回空: %v err=%v", empty, err)
	}
	if _, err := f.store.Quotas().ListSpendByTurn(ctx, domain.TurnKey{GoalID: "", TodoID: f.todo.ID, TurnSeq: 1}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("非法 TurnKey 应拒绝: %v", err)
	}
}

func TestQuotaSpendOverCapacityExemption(t *testing.T) {
	ctx := context.Background()
	f := newQuotaStoreFixture(t)
	defer f.close()
	reservation := quotaReservationFor(f, domain.QuotaOutputTokens, 6)
	if created, err := f.store.Quotas().Reserve(ctx, reservation); err != nil || !created {
		t.Fatalf("reservation 创建失败: created=%v err=%v", created, err)
	}

	quotaRun(t, f.db, "run_gov_fits", f.goal.RootWorkItemID, f.workerID, "succeeded")
	fitsDigest := setQuotaRunCanonical(t, f, "run_gov_fits", domain.QuotaOutputTokens, 6, false, "")

	// 能装下时必须 committed：resolved canonical + 容量充足 + unresolved entry 是逃账。
	evade := &domain.QuotaSpendEntry{
		Key:    domain.QuotaSpendKey{TurnKey: f.header.TurnKey, Kind: domain.QuotaOutputTokens, RunID: "run_gov_fits"},
		Amount: 0, UsageBasis: "per_run", UsageDigest: fitsDigest,
		PolicyDigest: reservation.PolicyDigest, Status: domain.QuotaSpendUnresolved,
		Reason: "skip commitment", CreatedAt: time.Now().UTC(),
	}
	if _, err := f.store.Quotas().AppendSpend(ctx, evade); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("容量充足时 resolved canonical 不得落 unresolved: %v", err)
	}
	committed := &domain.QuotaSpendEntry{
		Key:    domain.QuotaSpendKey{TurnKey: f.header.TurnKey, Kind: domain.QuotaOutputTokens, RunID: "run_gov_fits"},
		Amount: 6, UsageBasis: "per_run", UsageDigest: fitsDigest,
		PolicyDigest: reservation.PolicyDigest, Status: domain.QuotaSpendCommitted,
		CreatedAt: time.Now().UTC(),
	}
	if created, err := f.store.Quotas().AppendSpend(ctx, committed); err != nil || !created {
		t.Fatalf("等值 committed 应落账: created=%v err=%v", created, err)
	}

	quotaRun(t, f.db, "run_gov_overflow", f.goal.RootWorkItemID, f.workerID, "succeeded")
	overflowDigest := setQuotaRunCanonical(t, f, "run_gov_overflow", domain.QuotaOutputTokens, 4, false, "")
	// 装不下时仍拒绝 committed 超额（容量不变式）。
	over := &domain.QuotaSpendEntry{
		Key:    domain.QuotaSpendKey{TurnKey: f.header.TurnKey, Kind: domain.QuotaOutputTokens, RunID: "run_gov_overflow"},
		Amount: 4, UsageBasis: "per_run", UsageDigest: overflowDigest,
		PolicyDigest: reservation.PolicyDigest, Status: domain.QuotaSpendCommitted,
		CreatedAt: time.Now().UTC(),
	}
	if _, err := f.store.Quotas().AppendSpend(ctx, over); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("committed 超额必须拒绝: %v", err)
	}
	// 证明装不下（4 > 剩余 0）才允许记 unresolved 缺口；真实用量留在 Run canonical。
	gap := &domain.QuotaSpendEntry{
		Key:    domain.QuotaSpendKey{TurnKey: f.header.TurnKey, Kind: domain.QuotaOutputTokens, RunID: "run_gov_overflow"},
		Amount: 0, UsageBasis: "per_run", UsageDigest: overflowDigest,
		PolicyDigest: reservation.PolicyDigest, Status: domain.QuotaSpendUnresolved,
		Reason: "actual 4 exceeds remaining reservation 0", CreatedAt: time.Now().UTC(),
	}
	if created, err := f.store.Quotas().AppendSpend(ctx, gap); err != nil || !created {
		t.Fatalf("容量不足时应允许 unresolved 缺口: created=%v err=%v", created, err)
	}
	if total, err := f.store.Quotas().SumCommitted(ctx, f.goal.ID, domain.QuotaOutputTokens); err != nil || total != 6 {
		t.Fatalf("committed 合计不得含缺口: total=%d err=%v", total, err)
	}
}

// TestQuotaSpendRejectsCrossTurnAttribution 钉 0028 lineage guard：spend entry
// 的 turn_seq 必须与 Run 固化的 governance 身份一致，跨 Turn 归因直接拒绝
// （跨 Goal 的 foreign 用例见 TestQuotaCostSnapshotAndSpendIdempotency，不重复）。
func TestQuotaSpendRejectsCrossTurnAttribution(t *testing.T) {
	ctx := context.Background()
	f := newQuotaStoreFixture(t)
	defer f.close()

	// run 的 governance input 固化在 turn 1（quotaRun 默认），canonical 已封口。
	quotaRun(t, f.db, "run_gov_lineage", f.goal.RootWorkItemID, f.workerID, "succeeded")
	digest := setQuotaRunCanonical(t, f, "run_gov_lineage", domain.QuotaOutputTokens, 4, false, "")

	turn1Key := f.header.TurnKey
	turn2Key := domain.TurnKey{GoalID: f.goal.ID, TodoID: f.todo.ID, TurnSeq: 2}
	// turn2 reservation 的 FK 指向 receipt header：直接落一行 turn2 Header
	//（fixture 的 Todo 已在 turn1 running，repo Admit 不适用本测试）。
	header2 := &domain.TurnReceiptHeader{
		TurnKey: turn2Key, Attempt: 1, SchemaVersion: "quota-test/v1",
		InputSnapshotDigest: quotaStoreDigest('i'), AdmissionClientKey: "quota-admit-lineage-2",
		CreatedAt: f.header.CreatedAt.Add(time.Millisecond),
	}
	header2Digest, err := application.ComputeTurnReceiptHeaderDigest(header2)
	if err != nil {
		t.Fatal(err)
	}
	header2.CanonicalDigest = header2Digest
	// header watermark 触发器要求 turn_seq == todo.last_turn_seq：先推水位再落行。
	if _, err := f.db.Exec(`UPDATE goal_todos SET last_turn_seq=last_turn_seq+1, version=version+1
		 WHERE id=? AND goal_id=?`, f.todo.ID, f.goal.ID); err != nil {
		t.Fatalf("turn2 watermark 推进失败: %v", err)
	}
	if _, err := f.db.Exec(`INSERT INTO turn_receipt_headers
		(goal_id,todo_id,turn_seq,attempt,schema_version,input_snapshot_digest,admission_client_key,canonical_digest,created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		header2.TurnKey.GoalID, header2.TurnKey.TodoID, header2.TurnKey.TurnSeq,
		header2.Attempt, header2.SchemaVersion, header2.InputSnapshotDigest,
		header2.AdmissionClientKey, header2.CanonicalDigest, header2.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("turn2 header 落库失败: %v", err)
	}
	turn1 := quotaReservationFor(f, domain.QuotaOutputTokens, 10)
	if created, err := f.store.Quotas().Reserve(ctx, turn1); err != nil || !created {
		t.Fatalf("turn1 reservation 创建失败: created=%v err=%v", created, err)
	}
	turn2 := quotaReservationFor(f, domain.QuotaOutputTokens, 10)
	turn2.Key = domain.QuotaReservationKey{TurnKey: turn2Key, Kind: domain.QuotaOutputTokens}
	if created, err := f.store.Quotas().Reserve(ctx, turn2); err != nil || !created {
		t.Fatalf("turn2 reservation 创建失败: created=%v err=%v", created, err)
	}

	misattributed := &domain.QuotaSpendEntry{
		Key:    domain.QuotaSpendKey{TurnKey: turn2Key, Kind: domain.QuotaOutputTokens, RunID: "run_gov_lineage"},
		Amount: 4, UsageBasis: "per_run", UsageDigest: digest,
		PolicyDigest: turn2.PolicyDigest, Status: domain.QuotaSpendCommitted,
		CreatedAt: time.Now().UTC(),
	}
	if _, err := f.store.Quotas().AppendSpend(ctx, misattributed); err == nil {
		t.Fatal("turn1 run 的用量不得归因到 turn2 台账（0028 lineage guard）")
	} else if !strings.Contains(err.Error(), "quota spend") {
		t.Fatalf("expected lineage guard rejection, got: %v", err)
	}
	if _, err := f.store.Quotas().GetSpend(ctx, misattributed.Key); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("跨 Turn 归因不得落账: %v", err)
	}

	own := *misattributed
	own.Key = domain.QuotaSpendKey{TurnKey: turn1Key, Kind: domain.QuotaOutputTokens, RunID: "run_gov_lineage"}
	if created, err := f.store.Quotas().AppendSpend(ctx, &own); err != nil || !created {
		t.Fatalf("本 Turn 归因应正常落账: created=%v err=%v", created, err)
	}
}
