package application_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	"github.com/ybs/agent-team-workbench/internal/scheduling"
)

// ── WP4-C：usage 结算 sweep 集成测试 ─────────────────────────────────

// usagePriceSnapshot 构造已 Normalize（含 digest）的价格快照；outputRate 不同则
// digest 不同，用于多模型多价断言。速率按百万 token 级取值，保证测试用量下
// cost_microusd 不因 half-up 取整归零。
func usagePriceSnapshot(t *testing.T, ref string, outputRate int64) *domain.PriceSnapshotRef {
	t.Helper()
	price := &domain.PriceSnapshotRef{
		ModelRef:                        ref,
		Currency:                        "USD",
		InputUncachedMicroUSDPerMillion: 3_000_000,
		CacheReadMicroUSDPerMillion:     1_000_000,
		CacheWriteMicroUSDPerMillion:    2_000_000,
		OutputMicroUSDPerMillion:        outputRate,
		EffectiveAt:                     time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		PriceVersion:                    "usage-test-" + ref,
	}
	if err := price.Normalize(); err != nil {
		t.Fatal(err)
	}
	return price
}

// usageWorkerDecision 渲染 n 个 dispatch + join(all) 的治理决策。
func usageWorkerDecision(t *testing.T, workerIDs ...string) *domain.PlanDecisionV2 {
	t.Helper()
	steps := make([]domain.PlanDecisionStepV2, 0, len(workerIDs)+1)
	for _, id := range workerIDs {
		steps = append(steps, domain.PlanDecisionStepV2{Verb: domain.PlanVerbDispatch,
			Dispatch: &domain.PlanDispatchStepV2{
				AgentID: id, Title: "usage work", Instruction: "produce bounded work",
				Acceptance: []string{"work complete"},
			}})
	}
	steps = append(steps, domain.PlanDecisionStepV2{Verb: domain.PlanVerbJoin,
		Join: &domain.PlanJoinStepV2{Children: domain.JoinChildren{All: true}}})
	return &domain.PlanDecisionV2{
		SchemaVersion: "plan-decision/v2", Kind: "plan",
		Reason: "usage settlement fixture", NextAction: "wait for workers", Steps: steps,
	}
}

// usageDriveSourceDecision 走真实链路驱动 Coordinator source Run 提交治理决策：
// running → message.completed(raw PlanDecisionV2) → succeeded；返回提交的治理 Plan。
func usageDriveSourceDecision(t *testing.T, ctx context.Context, svc *application.Service,
	store *sqlstore.Store, sourceID string, decision *domain.PlanDecisionV2) *domain.Plan {
	t.Helper()
	usageDriveSourceRunning(t, ctx, svc, sourceID)
	raw, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, sourceID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": string(raw)}); err != nil {
		t.Fatal(err)
	}
	usageDriveSourceSucceeded(t, ctx, svc, sourceID)
	plan, err := store.Plans().GetBySourceRun(ctx, sourceID)
	if err != nil || plan == nil || plan.GovernanceTurnKey == nil {
		t.Fatalf("governed decision must produce a Plan with TurnKey: plan=%+v err=%v", plan, err)
	}
	return plan
}

// usageInjectRunUsage 注入一封口的 per_run 报告（真实 RecordRunUsage 链路）。
func usageInjectRunUsage(t *testing.T, ctx context.Context, svc *application.Service,
	store *sqlstore.Store, runID string, counters domain.UsageCountersV1) {
	t.Helper()
	recordProviderUsage(t, ctx, svc, store, runID, "", domain.UsageBasisPerRun, counters)
}

// usagePhase6 读取 Turn 的 quota phase6（不存在则致命）。
func usagePhase6(t *testing.T, ctx context.Context, store *sqlstore.Store, key domain.TurnKey) *domain.TurnReceiptPhase {
	t.Helper()
	phase, err := store.TurnReceipts().GetPhase(ctx, key, 6)
	if err != nil {
		t.Fatalf("phase6 missing: %v", err)
	}
	return phase
}

// usagePayloadKinds 读取 phase6 payload.reservations 里的 quota_kind 集合。
func usagePayloadKinds(t *testing.T, phase *domain.TurnReceiptPhase) map[string]int {
	t.Helper()
	raw, ok := phase.Payload["reservations"].([]any)
	if !ok {
		t.Fatalf("phase6 payload.reservations missing: %#v", phase.Payload["reservations"])
	}
	kinds := map[string]int{}
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("phase6 reservation entry malformed: %#v", item)
		}
		kind, _ := entry["quota_kind"].(string)
		kinds[kind]++
	}
	return kinds
}

// usagePayloadUnresolvedKinds 读取 phase6 payload.unresolved_kinds 字符串集合。
func usagePayloadUnresolvedKinds(t *testing.T, phase *domain.TurnReceiptPhase) []string {
	t.Helper()
	raw, ok := phase.Payload["unresolved_kinds"].([]any)
	if !ok {
		t.Fatalf("phase6 payload.unresolved_kinds missing: %#v", phase.Payload["unresolved_kinds"])
	}
	kinds := make([]string, 0, len(raw))
	for _, item := range raw {
		kind, _ := item.(string)
		kinds = append(kinds, kind)
	}
	return kinds
}

func TestUsageSettlementEndToEndMultiModelMultiPrice(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, worker1ID := seedCoordinatorEnv(t)
	worker2ID := "agent_usage_settlement_worker2"
	now := time.Now().UTC()
	if err := store.Agents().Create(ctx, &domain.AgentProfile{
		ID: worker2ID, WorkspaceID: wsID, Name: "Usage Worker 2", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock"},
		ModelOverride:     domain.ModelRef{Ref: "model-worker-2"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	// worker1 的模型引用在 CreateWorkItem 前固化（DecisionScope 冻结名册已在
	// seedCoordinatorEnv 建 worker1 时确定，模型引用只影响价格快照）。
	worker1, err := store.Agents().Get(ctx, worker1ID)
	if err != nil {
		t.Fatal(err)
	}
	worker1.ModelOverride.Ref = "model-worker-1"
	worker1.Version++
	worker1.UpdatedAt = time.Now().UTC()
	if err := store.Agents().Update(ctx, worker1, worker1.Version-1); err != nil {
		t.Fatal(err)
	}
	config, err := store.TaskCoordinators().EnsureConfig(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	config.ModelRef = domain.ModelRef{Ref: "model-coordinator"}
	if err := store.TaskCoordinators().UpdateConfig(ctx, config, config.Version); err != nil {
		t.Fatal(err)
	}
	prices := map[string]*domain.PriceSnapshotRef{
		"model-coordinator": usagePriceSnapshot(t, "model-coordinator", 5_000_000),
		"model-worker-1":    usagePriceSnapshot(t, "model-worker-1", 10_000_000),
		"model-worker-2":    usagePriceSnapshot(t, "model-worker-2", 20_000_000),
	}
	svc.ModelResolver = func(ref string) (orchestrator.ModelSpec, bool) {
		price, ok := prices[ref]
		if !ok {
			return orchestrator.ModelSpec{}, false
		}
		return orchestrator.ModelSpec{Ref: ref, Provider: "mock", Model: ref, PriceSnapshot: price}, true
	}

	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "multi model settlement", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"both workers settle with their own price"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	const outputLimit, costLimit = int64(1_000_000), int64(1_000_000_000)
	goal = setGoalQuotaPolicies(t, ctx, store, goal,
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: outputLimit, Enforcement: domain.QuotaEnforcementEnforce},
		domain.QuotaPolicy{Kind: domain.QuotaCostMicroUSD, Limit: costLimit, Enforcement: domain.QuotaEnforcementEnforce},
	)

	source := dispatcher.runs[0]
	usageInjectRunUsage(t, ctx, svc, store, source.ID, fullUsageCounters(300, 100, 100, 100, 50))
	decision := usageWorkerDecision(t, worker1ID, worker2ID)
	plan := usageDriveSourceDecision(t, ctx, svc, store, source.ID, decision)
	turnKey := *plan.GovernanceTurnKey
	if len(dispatcher.runs) != 3 {
		t.Fatalf("two dispatches expected, got %d runs", len(dispatcher.runs))
	}

	// admission 冻结：两个 usage reservation 在任何终态前都是 reserved。
	for _, kind := range []domain.QuotaKind{domain.QuotaOutputTokens, domain.QuotaCostMicroUSD} {
		reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: turnKey, Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		wantLimit := outputLimit
		if kind == domain.QuotaCostMicroUSD {
			wantLimit = costLimit
		}
		if reservation.Status != domain.QuotaReservationReserved || reservation.ReservedAmount != wantLimit {
			t.Fatalf("admission reservation %s mismatch: %+v", kind, reservation)
		}
	}

	coordinatorCost, err := domain.ComputeCostMicroUSD(fullUsageCounters(300, 100, 100, 100, 50), prices["model-coordinator"])
	if err != nil {
		t.Fatal(err)
	}
	workerCosts := map[string]int64{}
	for _, worker := range []*domain.ExecutionRun{dispatcher.runs[1], dispatcher.runs[2]} {
		counters := fullUsageCounters(200, 80, 60, 60, 40)
		price := prices["model-worker-1"]
		if worker.AgentProfileID == worker2ID {
			counters = fullUsageCounters(240, 100, 70, 70, 60)
			price = prices["model-worker-2"]
		}
		cost, costErr := domain.ComputeCostMicroUSD(counters, price)
		if costErr != nil {
			t.Fatal(costErr)
		}
		workerCosts[worker.ID] = cost
		usageStartRun(t, ctx, svc, worker.ID)
		usageInjectRunUsage(t, ctx, svc, store, worker.ID, counters)
		usageDriveSourceSucceeded(t, ctx, svc, worker.ID)
	}

	// 每个 Run 两条 spend：output committed = canonical output；cost committed =
	// 重算值且 price digest 各自匹配（两 worker 价格不同）。
	for _, runID := range []string{source.ID, dispatcher.runs[1].ID, dispatcher.runs[2].ID} {
		run, err := store.Runs().Get(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.CanonicalUsage == nil {
			t.Fatalf("run %s must have canonical usage after settlement", runID)
		}
		outputSpend, err := store.Quotas().GetSpend(ctx, domain.QuotaSpendKey{
			TurnKey: turnKey, Kind: domain.QuotaOutputTokens, RunID: runID})
		if err != nil || outputSpend.Status != domain.QuotaSpendCommitted ||
			outputSpend.Amount != usageCounterValue(run.CanonicalUsage.Counters.OutputTokens) {
			t.Fatalf("output spend mismatch for %s: %+v err=%v", runID, outputSpend, err)
		}
		costSpend, err := store.Quotas().GetSpend(ctx, domain.QuotaSpendKey{
			TurnKey: turnKey, Kind: domain.QuotaCostMicroUSD, RunID: runID})
		if err != nil || costSpend.Status != domain.QuotaSpendCommitted ||
			costSpend.PriceDigest != run.CanonicalUsage.PriceDigest {
			t.Fatalf("cost spend digest mismatch for %s: %+v err=%v", runID, costSpend, err)
		}
		wantCost := coordinatorCost
		if runID != source.ID {
			wantCost = workerCosts[runID]
		}
		if costSpend.Amount != wantCost {
			t.Fatalf("cost spend amount mismatch for %s: got %d want %d", runID, costSpend.Amount, wantCost)
		}
	}
	worker1Digest := prices["model-worker-1"].Digest
	worker2Digest := prices["model-worker-2"].Digest
	if worker1Digest == worker2Digest {
		t.Fatal("precondition: the two worker prices must have different digests")
	}
	for _, pair := range []struct {
		runID  string
		digest string
	}{
		{dispatcher.runs[1].ID, worker1Digest},
		{dispatcher.runs[2].ID, worker2Digest},
	} {
		run, _ := store.Runs().Get(ctx, pair.runID)
		if run.CanonicalUsage.PriceDigest != pair.digest {
			t.Fatalf("run %s froze the wrong price digest: %s", pair.runID, run.CanonicalUsage.PriceDigest)
		}
	}

	// reservation 关闭：committed = Σ committed spend，released = reserved − committed。
	spend, err := store.Quotas().ListSpendByTurn(ctx, turnKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(spend) != 6 {
		t.Fatalf("three runs x two kinds expected, got %d spend entries", len(spend))
	}
	for _, kind := range []domain.QuotaKind{domain.QuotaOutputTokens, domain.QuotaCostMicroUSD} {
		reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: turnKey, Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		var committed int64
		for _, entry := range spend {
			if entry.Key.Kind == kind && entry.Status == domain.QuotaSpendCommitted {
				committed += entry.Amount
			}
		}
		if reservation.Status != domain.QuotaReservationCommitted ||
			reservation.CommittedAmount != committed ||
			reservation.ReleasedAmount != reservation.ReservedAmount-committed {
			t.Fatalf("reservation %s close mismatch: %+v committed=%d", kind, reservation, committed)
		}
	}
	quotaEvents, err := store.Events().Since(ctx, wsID, 0, 2000)
	if err != nil {
		t.Fatal(err)
	}
	reservationStates := map[string]map[string]int{}
	spendEvents := 0
	for _, event := range quotaEvents {
		if event == nil || event.AggregateType != domain.AggregateGoal || event.Data["goal_id"] != goal.ID {
			continue
		}
		if event.Aggregate.Version != goal.Version {
			t.Fatalf("quota event aggregate version must follow Goal, got=%d want=%d: %+v",
				event.Aggregate.Version, goal.Version, event)
		}
		switch event.Type {
		case domain.EventQuotaReservationChanged:
			kind, _ := event.Data["quota_kind"].(string)
			status, _ := event.Data["status"].(string)
			if reservationStates[kind] == nil {
				reservationStates[kind] = map[string]int{}
			}
			reservationStates[kind][status]++
			if event.Data["policy_digest"] == "" || event.Data["usage_digest"] != "" || event.Data["price_digest"] != "" {
				t.Fatalf("reservation event must carry policy lineage without usage/provider material: %+v", event)
			}
		case domain.EventQuotaSpendRecorded:
			spendEvents++
			for _, field := range []string{"todo_id", "turn_seq", "quota_kind", "run_id", "amount", "usage_basis", "usage_digest", "policy_digest", "reason"} {
				if _, ok := event.Data[field]; !ok {
					t.Fatalf("spend event missing %s: %+v", field, event)
				}
			}
		}
	}
	for _, kind := range []domain.QuotaKind{domain.QuotaOutputTokens, domain.QuotaCostMicroUSD} {
		states := reservationStates[string(kind)]
		if states["reserved"] != 1 || states["committed"] != 1 || states["released"] != 0 {
			t.Fatalf("quota reservation lifecycle events mismatch for %s: %v", kind, states)
		}
	}
	if spendEvents != 6 {
		t.Fatalf("one quota spend event per newly appended spend required: %d", spendEvents)
	}

	// phase6 恰好一条：reservations 含两个 usage kind，unresolved_kinds 为空。
	phases, err := store.TurnReceipts().ListPhases(ctx, turnKey)
	if err != nil || len(phases) != 7 {
		t.Fatalf("turn receipt must close with 7 phases: phases=%+v err=%v", phases, err)
	}
	phase6 := usagePhase6(t, ctx, store, turnKey)
	kinds := usagePayloadKinds(t, phase6)
	if len(kinds) != 2 || kinds[string(domain.QuotaOutputTokens)] != 1 || kinds[string(domain.QuotaCostMicroUSD)] != 1 {
		t.Fatalf("phase6 must contain exactly the two usage reservations: %v", kinds)
	}
	if unresolved := usagePayloadUnresolvedKinds(t, phase6); len(unresolved) != 0 {
		t.Fatalf("fully reported turn must leave no unresolved kinds: %v", unresolved)
	}
	if len(phase6.QuotaReservationKeys) != 2 {
		t.Fatalf("phase6 must bind both reservation keys: %v", phase6.QuotaReservationKeys)
	}

	// exactly-once：StartCoordinator 恢复面 + admission 重放都不得改变台账。
	phase6Before := *phase6
	reservationVersions := map[domain.QuotaKind]int{}
	for _, kind := range []domain.QuotaKind{domain.QuotaOutputTokens, domain.QuotaCostMicroUSD} {
		reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: turnKey, Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		reservationVersions[kind] = reservation.Version
	}
	spendCount := len(spend)
	quotaEventCount := 0
	for _, event := range quotaEvents {
		if event != nil && (event.Type == domain.EventQuotaReservationChanged || event.Type == domain.EventQuotaSpendRecorded) {
			quotaEventCount++
		}
	}
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	freshSource, err := store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed, err := svc.SubmitGovernedTodoPlanDecision(ctx, freshSource, decision, application.PlanCandidateNativeText); err != nil || replayed.ID != plan.ID {
		t.Fatalf("admission replay mismatch: plan=%+v err=%v", replayed, err)
	}
	phase6After := usagePhase6(t, ctx, store, turnKey)
	if phase6After.CanonicalDigest != phase6Before.CanonicalDigest ||
		!reflect.DeepEqual(phase6After.Payload, phase6Before.Payload) ||
		!reflect.DeepEqual(phase6After.QuotaReservationKeys, phase6Before.QuotaReservationKeys) {
		t.Fatalf("phase6 must be exactly-once: before=%+v after=%+v", phase6Before.Payload, phase6After.Payload)
	}
	spendAfter, err := store.Quotas().ListSpendByTurn(ctx, turnKey)
	if err != nil || len(spendAfter) != spendCount {
		t.Fatalf("replay must not append spend entries: before=%d after=%d err=%v", spendCount, len(spendAfter), err)
	}
	quotaEventsAfter, err := store.Events().Since(ctx, wsID, 0, 2000)
	if err != nil {
		t.Fatal(err)
	}
	quotaEventCountAfter := 0
	for _, event := range quotaEventsAfter {
		if event != nil && (event.Type == domain.EventQuotaReservationChanged || event.Type == domain.EventQuotaSpendRecorded) {
			quotaEventCountAfter++
		}
	}
	if quotaEventCountAfter != quotaEventCount {
		t.Fatalf("quota replay must not append invalidation events: before=%d after=%d", quotaEventCount, quotaEventCountAfter)
	}
	for _, kind := range []domain.QuotaKind{domain.QuotaOutputTokens, domain.QuotaCostMicroUSD} {
		reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: turnKey, Kind: kind})
		if err != nil || reservation.Version != reservationVersions[kind] {
			t.Fatalf("replay must not bump reservation %s version: %+v err=%v", kind, reservation, err)
		}
	}
}

// usageCoordinatorEnvForTest 复刻既有 quota gate 测试的 fixture：在首启前固化
// Goal 配额政策（CreateWorkItem(AutoCoordinate) 无法在启动前注入政策）。
// extraAgents 在 Goal/Todo 冻结 DecisionScope 之前入册（manual 审批闸测试的
// dispatch 目标必须在册，否则 plan_authority_denied）。
func usageCoordinatorEnvForTest(t *testing.T, title string, extraAgents []*domain.AgentProfile,
	policies ...domain.QuotaPolicy) (context.Context, *application.Service, *sqlstore.Store,
	*captureDispatcher, *domain.WorkItem, *domain.Goal) {
	ctx, _, svc, store, dispatcher, root, goal := usageCoordinatorEnvForTestWithDatabase(t, title, extraAgents, policies...)
	return ctx, svc, store, dispatcher, root, goal
}

// usageCoordinatorEnvForTestWithDatabase is the same governance fixture with
// its database exposed for transaction fault-injection tests. The production
// code must not depend on test-only hooks; SQLite triggers model a crash/error
// at the exact durable boundary instead.
func usageCoordinatorEnvForTestWithDatabase(t *testing.T, title string, extraAgents []*domain.AgentProfile,
	policies ...domain.QuotaPolicy) (context.Context, *sql.DB, *application.Service, *sqlstore.Store,
	*captureDispatcher, *domain.WorkItem, *domain.Goal) {
	t.Helper()
	ctx, db, svc, store, dispatcher, wsID, _ := seedCoordinatorEnvWithDatabase(t)
	config, err := store.TaskCoordinators().EnsureConfig(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, agent := range extraAgents {
		if agent.Version == 0 {
			agent.Version = 1
		}
		if agent.CreatedAt.IsZero() {
			agent.CreatedAt, agent.UpdatedAt = now, now
		}
		if agent.WorkspaceID == "" {
			agent.WorkspaceID = wsID
		}
		if err := store.Agents().Create(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	root := &domain.WorkItem{
		ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: wsID, RecordKind: domain.RecordKindTask,
		Title: title, Status: domain.WorkItemInProgress, Priority: domain.PriorityMedium,
		AgentProfileID:     config.AgentProfileID,
		AcceptanceCriteria: []string{"usage settlement fixture"},
		Version:            1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WorkItems().Create(ctx, root); err != nil {
		t.Fatal(err)
	}
	goal, err := svc.CreateGoal(ctx, wsID, application.CreateGoalParams{
		RootWorkItemID: root.ID, Objective: root.Title,
		AcceptanceContract: root.AcceptanceCriteria, QuotaPolicies: policies,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartGoal(ctx, goal.ID, goal.Version); err != nil {
		t.Fatal(err)
	}
	state := &domain.TaskCoordinatorState{
		ID: domain.NewID(domain.PrefixCoordinatorState), WorkspaceID: wsID,
		RootWorkItemID: root.ID, CoordinatorAgentID: config.AgentProfileID,
		Status: domain.CoordinatorQueued, Phase: "queued", CurrentAction: "queued",
		Data:    map[string]any{"acceptance_criteria": root.AcceptanceCriteria},
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.TaskCoordinators().CreateState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := store.TaskComments().EnsureCursor(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	return ctx, db, svc, store, dispatcher, root, goal
}

func TestCostPolicyWithoutPriceFailsClosedAtRunCreation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		enforcement domain.QuotaEnforcement
	}{
		{name: "enforce", enforcement: domain.QuotaEnforcementEnforce},
		{name: "audit", enforcement: domain.QuotaEnforcementAudit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, svc, store, dispatcher, root, _ := usageCoordinatorEnvForTest(t,
				"cost fail closed "+tc.name, nil,
				domain.QuotaPolicy{Kind: domain.QuotaCostMicroUSD, Limit: 1_000_000, Enforcement: tc.enforcement})
			err := svc.StartCoordinator(ctx, root.ID)
			var decisionErr *application.PlanDecisionError
			if !errors.As(err, &decisionErr) || decisionErr.Code != domain.GovernanceErrorCostPriceUnavailable {
				t.Fatalf("cost quota without price must fail closed at run creation: err=%v", err)
			}
			runs, listErr := store.Runs().ListByWorkItem(ctx, root.ID)
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(runs) != 0 || len(dispatcher.runs) != 0 {
				t.Fatalf("no Coordinator Run may be created: runs=%d dispatched=%d", len(runs), len(dispatcher.runs))
			}
			state, stateErr := store.TaskCoordinators().GetState(ctx, root.ID)
			if stateErr != nil || state.Status != domain.CoordinatorBlocked ||
				!strings.Contains(state.BlockerMessage, string(domain.GovernanceErrorCostPriceUnavailable)) {
				t.Fatalf("denial must remain explainable in Coordinator state: state=%+v err=%v", state, stateErr)
			}
		})
	}

	t.Run("token only policy runs normally without price", func(t *testing.T) {
		ctx, svc, store, dispatcher, root, _ := usageCoordinatorEnvForTest(t,
			"token only without price", nil,
			domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: 1_000, Enforcement: domain.QuotaEnforcementAudit})
		if err := svc.StartCoordinator(ctx, root.ID); err != nil {
			t.Fatal(err)
		}
		if len(dispatcher.runs) != 1 {
			t.Fatalf("token-only goal must start a Coordinator Run: %d", len(dispatcher.runs))
		}
		runs, err := store.Runs().ListByWorkItem(ctx, root.ID)
		if err != nil || len(runs) != 1 {
			t.Fatalf("token-only coordinator run must persist: runs=%+v err=%v", runs, err)
		}
		if _, hasPrice := runs[0].Input["price_snapshot"]; hasPrice {
			t.Fatal("unpriced model must not freeze a price snapshot")
		}
	})
}

func TestTokenBudgetExhaustedPreflightDeniesNextCoordinatorRun(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "token budget preflight", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"exhausted tokens deny the next coordinator run"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	const limit = int64(1000)
	goal = setGoalQuotaPolicies(t, ctx, store, goal,
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: limit, Enforcement: domain.QuotaEnforcementEnforce})

	source := dispatcher.runs[0]
	decision := usageWorkerDecision(t, workerID)
	plan := usageDriveSourceDecision(t, ctx, svc, store, source.ID, decision)
	turnKey := *plan.GovernanceTurnKey
	if len(dispatcher.runs) != 2 {
		t.Fatalf("one governed worker expected: %d", len(dispatcher.runs))
	}
	worker := dispatcher.runs[1]
	usageStartRun(t, ctx, svc, worker.ID)
	usageInjectRunUsage(t, ctx, svc, store, worker.ID, fullUsageCounters(1200, 400, 400, 400, limit))
	usageDriveSourceSucceeded(t, ctx, svc, worker.ID)

	// P1-4：source run 无 report → absent 延迟，worker 终态后 Turn 仍开放
	//（reservation 尚未结算），迟到 report 此刻仍可正常首写。
	reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: turnKey, Kind: domain.QuotaOutputTokens})
	if err != nil || reservation.Status != domain.QuotaReservationReserved {
		t.Fatalf("progressive pass must keep the turn open (absent deferred): %+v err=%v", reservation, err)
	}

	// 驱动下一轮 Coordinator 控制轮：StartCoordinator 恢复面先关闭 turn1
	//（为 source run 合成 absent evidence），随后 turn2 预检被预算打满拒绝。
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := state.Version
	state.Status = domain.CoordinatorQueued
	state.Phase = "recovering"
	state.CurrentRunID = ""
	state.CurrentAction = "recover"
	state.NextActionAt = nil
	state.Data = map[string]any{"control_action": "recover"}
	if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		t.Fatal(err)
	}
	startErr := svc.StartCoordinator(ctx, root.ID)
	var decisionErr *application.PlanDecisionError
	if !errors.As(startErr, &decisionErr) || decisionErr.Code != domain.GovernanceErrorPlanQuotaDenied ||
		decisionErr.Path != "/quota/"+string(domain.QuotaOutputTokens) {
		t.Fatalf("next coordinator run must be quota denied in preflight: err=%v", startErr)
	}

	// 关闭后：worker 的真实用量全额 committed。
	reservation, err = store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: turnKey, Kind: domain.QuotaOutputTokens})
	if err != nil || reservation.Status != domain.QuotaReservationCommitted || reservation.CommittedAmount != limit {
		t.Fatalf("turn budget must fully commit: %+v err=%v", reservation, err)
	}
	if total, err := store.Quotas().SumCommitted(ctx, goal.ID, domain.QuotaOutputTokens); err != nil || total != limit {
		t.Fatalf("committed tokens must equal the limit: total=%d err=%v", total, err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("denied turn must not dispatch a run: %d", len(dispatcher.runs))
	}
	runs, err := store.Runs().ListByWorkItem(ctx, root.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("denial must not create a new Coordinator Run: runs=%d err=%v", len(runs), err)
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil || state.Status != domain.CoordinatorBlocked ||
		state.BlockerCode != string(domain.GovernanceErrorPlanQuotaDenied) ||
		!strings.Contains(state.BlockerMessage, string(domain.QuotaOutputTokens)) {
		t.Fatalf("denial must block with an exact reason: state=%+v err=%v", state, err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	if _, headerErr := store.TurnReceipts().GetHeader(ctx, domain.TurnKey{
		GoalID: goal.ID, TodoID: todo.ID, TurnSeq: 2,
	}); !errors.Is(headerErr, domain.ErrNotFound) {
		t.Fatalf("denied next turn must not admit a Header: %v", headerErr)
	}
}

func TestFailedAndCancelledRunsSettleActualUsage(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "failed cancelled settlement", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"non-success terminals still settle actual usage"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	const limit = int64(100_000)
	goal = setGoalQuotaPolicies(t, ctx, store, goal,
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: limit, Enforcement: domain.QuotaEnforcementEnforce})

	source := dispatcher.runs[0]
	decision := usageWorkerDecision(t, workerID, workerID)
	plan := usageDriveSourceDecision(t, ctx, svc, store, source.ID, decision)
	turnKey := *plan.GovernanceTurnKey
	if len(dispatcher.runs) != 3 {
		t.Fatalf("two governed workers expected: %d", len(dispatcher.runs))
	}
	failed, cancelled := dispatcher.runs[1], dispatcher.runs[2]

	usageStartRun(t, ctx, svc, failed.ID)
	usageInjectRunUsage(t, ctx, svc, store, failed.ID, fullUsageCounters(100, 40, 30, 30, 20))
	if err := svc.RecordRunStatus(ctx, failed.ID, domain.RunFailed, map[string]any{
		"code": "permission_denied", "message": "worker failed for good", "retryable": false,
	}); err != nil {
		t.Fatal(err)
	}

	usageStartRun(t, ctx, svc, cancelled.ID)
	usageInjectRunUsage(t, ctx, svc, store, cancelled.ID, fullUsageCounters(140, 60, 40, 40, 30))
	if _, err := svc.ControlRun(ctx, cancelled.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, cancelled.ID, domain.RunCancelled, nil); err != nil {
		t.Fatal(err)
	}

	// P1-4：source run 无 report → absent 延迟到关闭时刻；StartCoordinator
	// 恢复面是关闭性触发源（state 已被 failed worker 阻塞，sweep 在守卫前跑）。
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}

	for _, pair := range []struct {
		run    *domain.ExecutionRun
		output int64
	}{
		{failed, 20}, {cancelled, 30},
	} {
		run, err := store.Runs().Get(ctx, pair.run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if run.CanonicalUsage == nil {
			t.Fatalf("terminal run %s must canonicalize its reported usage", pair.run.ID)
		}
		spend, err := store.Quotas().GetSpend(ctx, domain.QuotaSpendKey{
			TurnKey: turnKey, Kind: domain.QuotaOutputTokens, RunID: pair.run.ID})
		if err != nil || spend.Status != domain.QuotaSpendCommitted || spend.Amount != pair.output {
			t.Fatalf("committed spend mismatch for %s: %+v err=%v", pair.run.ID, spend, err)
		}
	}
	reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: turnKey, Kind: domain.QuotaOutputTokens})
	if err != nil || reservation.Status != domain.QuotaReservationCommitted ||
		reservation.CommittedAmount != 50 || reservation.ReleasedAmount != limit-50 {
		t.Fatalf("reservation must settle actual usage of failed+cancelled runs: %+v err=%v", reservation, err)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, turnKey)
	if err != nil || len(phases) != 7 || phases[5].Phase != domain.TurnReceiptPhaseQuotaSpend {
		t.Fatalf("turn must close with phase6: phases=%+v err=%v", phases, err)
	}
}

func TestWorkerRetrySharesTurnReservationAndExactlyOnce(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "retry shares reservation", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"retry charges the same turn budget"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	const limit = int64(100_000)
	goal = setGoalQuotaPolicies(t, ctx, store, goal,
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: limit, Enforcement: domain.QuotaEnforcementEnforce})

	source := dispatcher.runs[0]
	decision := usageWorkerDecision(t, workerID)
	plan := usageDriveSourceDecision(t, ctx, svc, store, source.ID, decision)
	turnKey := *plan.GovernanceTurnKey
	if len(dispatcher.runs) != 2 {
		t.Fatalf("one governed worker expected: %d", len(dispatcher.runs))
	}
	worker := dispatcher.runs[1]
	usageStartRun(t, ctx, svc, worker.ID)
	usageInjectRunUsage(t, ctx, svc, store, worker.ID, fullUsageCounters(120, 40, 40, 40, 80))
	if err := svc.RecordRunStatus(ctx, worker.ID, domain.RunFailed, map[string]any{
		"code": "transport_stream", "message": "retryable worker failure", "retryable": true,
	}); err != nil {
		t.Fatal(err)
	}

	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil || state.Status != domain.CoordinatorWaitingRetry {
		t.Fatalf("precondition: retryable failure must park a retry checkpoint: %+v err=%v", state, err)
	}
	reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: turnKey, Kind: domain.QuotaOutputTokens})
	if err != nil || reservation.Status != domain.QuotaReservationReserved {
		t.Fatalf("pending retry must keep the reservation open: %+v err=%v", reservation, err)
	}

	forceCoordinatorDue(t, ctx, svc, store, root.ID)
	if len(dispatcher.runs) != 3 || dispatcher.runs[2].RetryOf != worker.ID {
		t.Fatalf("coordinator-owned retry expected: %+v", dispatcher.runs)
	}
	retry := dispatcher.runs[2]
	usageStartRun(t, ctx, svc, retry.ID)
	usageInjectRunUsage(t, ctx, svc, store, retry.ID, fullUsageCounters(60, 20, 20, 20, 50))
	usageDriveSourceSucceeded(t, ctx, svc, retry.ID)

	first, err := store.Quotas().GetSpend(ctx, domain.QuotaSpendKey{
		TurnKey: turnKey, Kind: domain.QuotaOutputTokens, RunID: worker.ID})
	second, err2 := store.Quotas().GetSpend(ctx, domain.QuotaSpendKey{
		TurnKey: turnKey, Kind: domain.QuotaOutputTokens, RunID: retry.ID})
	if err != nil || err2 != nil || first.Status != domain.QuotaSpendCommitted || first.Amount != 80 ||
		second.Status != domain.QuotaSpendCommitted || second.Amount != 50 {
		t.Fatalf("both attempts must commit under one reservation: first=%+v (%v) second=%+v (%v)",
			first, err, second, err2)
	}
	if first.Amount+second.Amount > reservation.ReservedAmount {
		t.Fatalf("committed usage must not exceed the frozen reservation: %d > %d",
			first.Amount+second.Amount, reservation.ReservedAmount)
	}

	// P1-4：source run 无 report → absent 延迟；retry 终态后 Turn 仍开放。
	reservation, err = store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: turnKey, Kind: domain.QuotaOutputTokens})
	if err != nil || reservation.Status != domain.QuotaReservationReserved {
		t.Fatalf("progressive pass must keep the turn open (absent deferred): %+v err=%v", reservation, err)
	}

	// StartCoordinator 恢复面：关闭性触发源——为 source run 合成 absent
	// evidence 后收口本 Turn。
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	reservation, err = store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: turnKey, Kind: domain.QuotaOutputTokens})
	if err != nil || reservation.Status != domain.QuotaReservationCommitted ||
		reservation.CommittedAmount != 130 || reservation.ReleasedAmount != limit-130 {
		t.Fatalf("shared reservation must close with both attempts: %+v err=%v", reservation, err)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, turnKey)
	if err != nil || len(phases) != 7 {
		t.Fatalf("turn must close exactly once: phases=%+v err=%v", phases, err)
	}
	spend, err := store.Quotas().ListSpendByTurn(ctx, turnKey)
	if err != nil {
		t.Fatal(err)
	}
	spendCount := len(spend)

	// StartCoordinator 恢复面重放：spend / phase6 / reservation 都不得重复。
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	spendAfter, err := store.Quotas().ListSpendByTurn(ctx, turnKey)
	if err != nil || len(spendAfter) != spendCount {
		t.Fatalf("replay must not duplicate spend: before=%d after=%d err=%v", spendCount, len(spendAfter), err)
	}
	phasesAfter, err := store.TurnReceipts().ListPhases(ctx, turnKey)
	if err != nil || len(phasesAfter) != 7 {
		t.Fatalf("replay must not append a second phase6: phases=%+v err=%v", phasesAfter, err)
	}
	reservationAfter, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: turnKey, Kind: domain.QuotaOutputTokens})
	if err != nil || reservationAfter.Version != reservation.Version {
		t.Fatalf("replay must not touch the settled reservation: %+v err=%v", reservationAfter, err)
	}
}

func TestWorkerRetryExhaustionSettlesOldTurnBeforeNextTurnQuotaDenial(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "retry exhaustion closes reservation", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"an exhausted retry cannot strand the old turn budget"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	const limit = int64(1000)
	goal = setGoalQuotaPolicies(t, ctx, store, goal,
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: limit, Enforcement: domain.QuotaEnforcementEnforce})
	source := dispatcher.runs[0]
	plan := usageDriveSourceDecision(t, ctx, svc, store, source.ID, usageWorkerDecision(t, workerID))
	turnKey := *plan.GovernanceTurnKey
	worker := dispatcher.runs[1]
	for attempt := 1; attempt <= 3; attempt++ {
		usageStartRun(t, ctx, svc, worker.ID)
		if err := svc.RecordRunStatus(ctx, worker.ID, domain.RunFailed, map[string]any{
			"code": "transport_stream", "message": "retryable worker failure", "retryable": true,
		}); err != nil {
			t.Fatal(err)
		}
		if attempt < 3 {
			forceCoordinatorDue(t, ctx, svc, store, root.ID)
			worker = dispatcher.runs[len(dispatcher.runs)-1]
		}
	}
	reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: turnKey, Kind: domain.QuotaOutputTokens})
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Status == domain.QuotaReservationReserved {
		t.Fatalf("exhausted Worker retry must settle the old Turn reservation: %+v", reservation)
	}
	if active, err := store.Quotas().SumActiveReserved(ctx, goal.ID, domain.QuotaOutputTokens); err != nil || active != 0 {
		t.Fatalf("exhausted Worker retry must leave no active old-turn reservation: active=%d err=%v", active, err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorBlocked || state.BlockerCode != string(domain.GovernanceErrorPlanQuotaDenied) {
		t.Fatalf("exhausted Worker retry should settle the old Turn before the next-turn quota gate: %+v", state)
	}
}

func TestWorkerRetryExhaustionWithProvenZeroUsageStartsReplan(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "retry exhaustion replans after settlement", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"a settled old turn permits the bounded replan"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	goal = setGoalQuotaPolicies(t, ctx, store, goal,
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: 1000, Enforcement: domain.QuotaEnforcementEnforce})

	source := dispatcher.runs[0]
	usageDriveSourceRunning(t, ctx, svc, source.ID)
	raw, err := json.Marshal(usageWorkerDecision(t, workerID))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, source.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": string(raw)}); err != nil {
		t.Fatal(err)
	}
	usageInjectRunUsage(t, ctx, svc, store, source.ID, fullUsageCounters(0, 0, 0, 0, 0))
	usageDriveSourceSucceeded(t, ctx, svc, source.ID)
	plan, err := store.Plans().GetBySourceRun(ctx, source.ID)
	if err != nil || plan == nil || plan.GovernanceTurnKey == nil {
		t.Fatalf("source decision must create the original governed Turn: plan=%+v err=%v", plan, err)
	}
	oldTurn := *plan.GovernanceTurnKey

	worker := dispatcher.runs[1]
	for attempt := 1; attempt <= 3; attempt++ {
		usageStartRun(t, ctx, svc, worker.ID)
		usageInjectRunUsage(t, ctx, svc, store, worker.ID, fullUsageCounters(0, 0, 0, 0, 0))
		if err := svc.RecordRunStatus(ctx, worker.ID, domain.RunFailed, map[string]any{
			"code": "transport_stream", "message": "retryable worker failure", "retryable": true,
		}); err != nil {
			t.Fatal(err)
		}
		if attempt < 3 {
			forceCoordinatorDue(t, ctx, svc, store, root.ID)
			worker = dispatcher.runs[len(dispatcher.runs)-1]
		}
	}

	reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{
		TurnKey: oldTurn, Kind: domain.QuotaOutputTokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Status != domain.QuotaReservationReleased || reservation.CommittedAmount != 0 ||
		reservation.ReleasedAmount != reservation.ReservedAmount {
		t.Fatalf("proven zero usage must fully release the old Turn reservation: %+v", reservation)
	}
	if active, err := store.Quotas().SumActiveReserved(ctx, goal.ID, domain.QuotaOutputTokens); err != nil || active != 0 {
		t.Fatalf("replan must not inherit an active old-Turn reservation: active=%d err=%v", active, err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorRunning || state.CurrentRunID == "" {
		t.Fatalf("settled retry exhaustion must start the bounded replan Coordinator Run: %+v", state)
	}
	replanRun, err := store.Runs().Get(ctx, state.CurrentRunID)
	if err != nil {
		t.Fatal(err)
	}
	control, _ := replanRun.Input["task_coordinator"].(map[string]any)
	if replanRun.AgentProfileID != state.CoordinatorAgentID || control["action"] != "recover" {
		t.Fatalf("next Run must be the system Coordinator replan: run=%+v control=%+v", replanRun, control)
	}
	headers, err := store.TurnReceipts().ListHeadersByGoal(ctx, goal.ID)
	if err != nil || len(headers) != 2 {
		t.Fatalf("retry exhaustion must append exactly one control receipt: headers=%d err=%v", len(headers), err)
	}
	controlPhases, err := store.TurnReceipts().ListPhases(ctx, headers[1].TurnKey)
	if err != nil || len(controlPhases) != 7 {
		t.Fatalf("replan control receipt must close phases 1-7: phases=%d err=%v", len(controlPhases), err)
	}
}

func TestOverflowSpendRecordedUnresolvedNotFabricated(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "overflow not fabricated", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"overflow records a gap instead of inventing spend"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	const limit = int64(10)
	goal = setGoalQuotaPolicies(t, ctx, store, goal,
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: limit, Enforcement: domain.QuotaEnforcementEnforce})

	source := dispatcher.runs[0]
	decision := usageWorkerDecision(t, workerID, workerID)
	plan := usageDriveSourceDecision(t, ctx, svc, store, source.ID, decision)
	turnKey := *plan.GovernanceTurnKey
	if len(dispatcher.runs) != 3 {
		t.Fatalf("two governed workers expected: %d", len(dispatcher.runs))
	}
	first, second := dispatcher.runs[1], dispatcher.runs[2]

	// 先终态的 run 恰好用满 limit → committed；后终态的 run 超容 → unresolved。
	usageStartRun(t, ctx, svc, first.ID)
	usageInjectRunUsage(t, ctx, svc, store, first.ID, fullUsageCounters(30, 10, 10, 10, 10))
	usageDriveSourceSucceeded(t, ctx, svc, first.ID)
	usageStartRun(t, ctx, svc, second.ID)
	usageInjectRunUsage(t, ctx, svc, store, second.ID, fullUsageCounters(15, 5, 5, 5, 5))
	usageDriveSourceSucceeded(t, ctx, svc, second.ID)

	firstSpend, err := store.Quotas().GetSpend(ctx, domain.QuotaSpendKey{
		TurnKey: turnKey, Kind: domain.QuotaOutputTokens, RunID: first.ID})
	if err != nil || firstSpend.Status != domain.QuotaSpendCommitted || firstSpend.Amount != limit {
		t.Fatalf("fitting run must commit in full: %+v err=%v", firstSpend, err)
	}
	secondSpend, err := store.Quotas().GetSpend(ctx, domain.QuotaSpendKey{
		TurnKey: turnKey, Kind: domain.QuotaOutputTokens, RunID: second.ID})
	if err != nil || secondSpend.Status != domain.QuotaSpendUnresolved || secondSpend.Amount != 0 ||
		!strings.Contains(secondSpend.Reason, "exceeds") {
		t.Fatalf("overflow run must record a zero-amount gap: %+v err=%v", secondSpend, err)
	}
	secondRun, err := store.Runs().Get(ctx, second.ID)
	if err != nil || secondRun.CanonicalUsage == nil ||
		usageCounterValue(secondRun.CanonicalUsage.Counters.OutputTokens) != 5 {
		t.Fatalf("real usage must stay on the run canonical: %+v err=%v", secondRun.CanonicalUsage, err)
	}
	// P1-4：source run 无 report → absent 延迟到关闭时刻；StartCoordinator
	// 恢复面收口本 Turn（source 的 absent evidence 一并落账）。
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: turnKey, Kind: domain.QuotaOutputTokens})
	if err != nil || reservation.Status != domain.QuotaReservationCommitted ||
		reservation.CommittedAmount != limit || reservation.ReleasedAmount != 0 {
		t.Fatalf("reservation must close committed=reserved released=0: %+v err=%v", reservation, err)
	}
	phase6 := usagePhase6(t, ctx, store, turnKey)
	unresolved := usagePayloadUnresolvedKinds(t, phase6)
	found := false
	for _, kind := range unresolved {
		if kind == string(domain.QuotaOutputTokens) {
			found = true
		}
	}
	if !found || strings.TrimSpace(phase6.Payload["unresolved_reason"].(string)) == "" {
		t.Fatalf("phase6 must expose the output_tokens gap: kinds=%v payload=%+v", unresolved, phase6.Payload)
	}
	if !strings.Contains(phase6.Payload["unresolved_reason"].(string), "exceeds") {
		t.Fatalf("phase6 gap reason must explain the overflow: %q", phase6.Payload["unresolved_reason"])
	}
}

func TestCrossTurnAttributionIsolated(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "cross turn attribution", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"turns settle in isolated ledgers"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	const limit = int64(10_000)
	goal = setGoalQuotaPolicies(t, ctx, store, goal,
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: limit, Enforcement: domain.QuotaEnforcementEnforce})

	source := dispatcher.runs[0]
	// P1-1：turn1 source run 注入零用量 report——否则 absent 关闭会给 output
	// kind 留下 unresolved 缺口，turn2 的 enforce 预检会被缺口 fail-closed
	// 拒绝（缺口永不自动清除是预期语义；本测试只关注跨 Turn 归因隔离）。
	usageInjectRunUsage(t, ctx, svc, store, source.ID, fullUsageCounters(0, 0, 0, 0, 0))
	decision := usageWorkerDecision(t, workerID)
	firstPlan := usageDriveSourceDecision(t, ctx, svc, store, source.ID, decision)
	turn1 := *firstPlan.GovernanceTurnKey
	if turn1.TurnSeq != 1 {
		t.Fatalf("first turn must be seq 1: %+v", turn1)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("turn1 worker expected: %d", len(dispatcher.runs))
	}
	worker1 := dispatcher.runs[1]
	usageStartRun(t, ctx, svc, worker1.ID)
	usageInjectRunUsage(t, ctx, svc, store, worker1.ID, fullUsageCounters(120, 40, 40, 40, 100))
	usageDriveSourceSucceeded(t, ctx, svc, worker1.ID)

	// turn2：消费 settlement 唤醒 → 汇总 Coordinator Run → 新一轮 dispatch。
	wakeups, err := store.Wakeups().DueTimers(ctx, time.Now().UTC().Add(time.Second), 20)
	if err != nil {
		t.Fatal(err)
	}
	var settlement *domain.WakeupRequest
	for index := range wakeups {
		if _, ok := wakeups[index].Context[domain.WakeupContextSettlementDispatchID].(string); ok {
			settlement = &wakeups[index]
			break
		}
	}
	if settlement == nil {
		t.Fatalf("turn1 settlement wakeup missing: %+v", wakeups)
	}
	scheduler := &scheduling.Scheduler{Store: store.Wakeups(), RunStarter: svc}
	if outcome, err := scheduler.ConsumeOne(ctx, *settlement, time.Now().UTC()); err != nil || outcome != scheduling.OutcomeConsumed {
		t.Fatalf("settlement wake failed: outcome=%s err=%v", outcome, err)
	}
	if len(dispatcher.runs) != 3 {
		t.Fatalf("summary coordinator run expected: %d", len(dispatcher.runs))
	}
	summary := dispatcher.runs[2]
	// 与 turn1 同理：turn2 的 summary source run 注入零用量 report，避免
	// absent 缺口把本 Turn 的关闭推迟到关闭性触发源（本测试关注归因隔离）。
	usageInjectRunUsage(t, ctx, svc, store, summary.ID, fullUsageCounters(0, 0, 0, 0, 0))
	usageDriveSourceRunning(t, ctx, svc, summary.ID)
	if err := svc.RecordRunEvent(ctx, summary.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": `{"schema_version":"plan-decision/v2","kind":"plan","reason":"second turn","next_action":"wait for workers","steps":[{"verb":"dispatch","agent_id":"` + workerID + `","title":"turn2 work","instruction":"do more work","acceptance":["done"]},{"verb":"join","children":"all"}]}`}); err != nil {
		t.Fatal(err)
	}
	usageDriveSourceSucceeded(t, ctx, svc, summary.ID)
	if len(dispatcher.runs) != 4 {
		t.Fatalf("turn2 worker expected: %d", len(dispatcher.runs))
	}
	secondPlan, err := store.Plans().GetBySourceRun(ctx, summary.ID)
	if err != nil || secondPlan.GovernanceTurnKey == nil || secondPlan.GovernanceTurnKey.TurnSeq != 2 {
		t.Fatalf("summary run must admit turn 2: plan=%+v err=%v", secondPlan, err)
	}
	turn2 := *secondPlan.GovernanceTurnKey
	worker2 := dispatcher.runs[3]
	usageStartRun(t, ctx, svc, worker2.ID)
	usageInjectRunUsage(t, ctx, svc, store, worker2.ID, fullUsageCounters(80, 30, 20, 30, 50))
	usageDriveSourceSucceeded(t, ctx, svc, worker2.ID)

	for _, pair := range []struct {
		key    domain.TurnKey
		runID  string
		amount int64
	}{{turn1, worker1.ID, 100}, {turn2, worker2.ID, 50}} {
		spend, err := store.Quotas().GetSpend(ctx, domain.QuotaSpendKey{
			TurnKey: pair.key, Kind: domain.QuotaOutputTokens, RunID: pair.runID})
		if err != nil || spend.Status != domain.QuotaSpendCommitted || spend.Amount != pair.amount ||
			spend.Key.TurnKey.TurnSeq != pair.key.TurnSeq {
			t.Fatalf("turn %d spend mismatch: %+v err=%v", pair.key.TurnSeq, spend, err)
		}
		reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: pair.key, Kind: domain.QuotaOutputTokens})
		if err != nil || reservation.Status != domain.QuotaReservationCommitted || reservation.CommittedAmount != pair.amount {
			t.Fatalf("turn %d reservation mismatch: %+v err=%v", pair.key.TurnSeq, reservation, err)
		}
		entries, err := store.Quotas().ListSpendByTurn(ctx, pair.key)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.Key.TurnKey.TurnSeq != pair.key.TurnSeq {
				t.Fatalf("turn %d ledger leaked cross-turn entries: %+v", pair.key.TurnSeq, entry)
			}
		}
	}
	if total, err := store.Quotas().SumCommitted(ctx, goal.ID, domain.QuotaOutputTokens); err != nil || total != 150 {
		t.Fatalf("committed totals must accumulate across turns: total=%d err=%v", total, err)
	}
}

func TestAdmissionCreatesUsageReservationsAtomically(t *testing.T) {
	ctx, db, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnvWithDatabase(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "atomic admission", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"reservations freeze with the admission header"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	const limit = int64(500)
	goal = setGoalQuotaPolicies(t, ctx, store, goal,
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: limit, Enforcement: domain.QuotaEnforcementAudit})

	source := dispatcher.runs[0]
	markCompilerSourceSucceeded(t, ctx, store, source.ID)
	source, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision := compilerDecision(domain.PlanVerbDispatch, workerID)
	plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText)
	if err != nil {
		t.Fatal(err)
	}
	turnKey := *plan.GovernanceTurnKey
	header, err := store.TurnReceipts().GetHeader(ctx, turnKey)
	if err != nil || !header.TurnKey.Equal(turnKey) {
		t.Fatalf("admission header must share the reservation turn key: header=%+v err=%v", header, err)
	}
	reservationKey := domain.QuotaReservationKey{TurnKey: turnKey, Kind: domain.QuotaOutputTokens}
	reservation, err := store.Quotas().Get(ctx, reservationKey)
	if err != nil || reservation.Status != domain.QuotaReservationReserved || reservation.ReservedAmount != limit {
		t.Fatalf("admission must freeze the usage reservation: %+v err=%v", reservation, err)
	}
	frozen := *reservation

	// admission 重放：created=false 幂等，冻结值原样复用。
	source, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText)
	if err != nil || replayed.ID != plan.ID {
		t.Fatalf("admission replay mismatch: plan=%+v err=%v", replayed, err)
	}
	reservation, err = store.Quotas().Get(ctx, reservationKey)
	if err != nil || !reflect.DeepEqual(*reservation, frozen) {
		t.Fatalf("replay must reuse the frozen reservation: %+v vs %+v err=%v", reservation, &frozen, err)
	}

	// admission 后崩溃窗口：existing Header 重放补齐 reservation。spend 台账
	// 受 append-only 触发器与 FK 保护，测试临时摘除触发器清掉本 Turn 的台账行
	//（仅 harness 操作，触发器随后原样恢复），再重放 submit 断言补齐。
	if _, err := db.Exec(`DROP TRIGGER quota_spend_append_only_delete`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(`CREATE TRIGGER quota_spend_append_only_delete
BEFORE DELETE ON quota_spend_entries
BEGIN
    SELECT RAISE(ABORT, 'quota spend entries are append-only');
END`)
	}()
	if _, err := db.Exec(`DELETE FROM quota_spend_entries
		 WHERE goal_id=? AND todo_id=? AND turn_seq=?`,
		turnKey.GoalID, turnKey.TodoID, turnKey.TurnSeq); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM quota_reservations
		 WHERE goal_id=? AND todo_id=? AND turn_seq=? AND quota_kind=?`,
		turnKey.GoalID, turnKey.TodoID, turnKey.TurnSeq, string(domain.QuotaOutputTokens)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Quotas().Get(ctx, reservationKey); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("precondition: reservation must be gone: %v", err)
	}
	source, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText); err != nil {
		t.Fatal(err)
	}
	backfilled, err := store.Quotas().Get(ctx, reservationKey)
	if err != nil {
		t.Fatalf("header replay must backfill the reservation: %v", err)
	}
	if backfilled.Status != domain.QuotaReservationReserved || backfilled.ReservedAmount != frozen.ReservedAmount ||
		backfilled.PolicyDigest != frozen.PolicyDigest ||
		backfilled.PolicyLimit != frozen.PolicyLimit ||
		backfilled.PolicyEnforcement != frozen.PolicyEnforcement {
		t.Fatalf("backfilled reservation diverges from the frozen admission snapshot: %+v vs %+v", backfilled, &frozen)
	}
	if active, err := store.Quotas().SumActiveReserved(ctx, goal.ID, domain.QuotaOutputTokens); err != nil || active != limit {
		t.Fatalf("backfilled reservation must count as active budget: active=%d err=%v", active, err)
	}
}

// P1-2（复审裁决 #2）：approval_policy=manual 的 dispatch 步在审批前不建
// Run（step pending、无 ResultRunID）而 phase5 已落——sweep 不得在该窗口用
// source-only 集合提前关闭并写 phase6，否则审批后创建的 Worker 会撞上已
// 结算的 reservation（AppendSpend ErrStateConflict）。
func TestManualApprovalDefersQuotaPhase6UntilPlanStepsComplete(t *testing.T) {
	// manual agent 必须在 Goal/Todo 冻结 DecisionScope 前入册，否则 dispatch
	// 目标过不了 plan_authority 校验。
	manualNow := time.Now().UTC()
	manualProfile := &domain.AgentProfile{
		ID: "agent_manual_usage_settlement", Name: "Manual Worker", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock"},
		Policy:            domain.AgentPolicy{ApprovalPolicy: domain.ApprovalPolicyManual},
		CreatedAt:         manualNow, UpdatedAt: manualNow,
	}
	ctx, svc, store, dispatcher, root, goal := usageCoordinatorEnvForTest(t,
		"manual approval defers phase6", []*domain.AgentProfile{manualProfile},
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: 100_000, Enforcement: domain.QuotaEnforcementEnforce})
	wsID := root.WorkspaceID
	manualID := manualProfile.ID
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 1 {
		t.Fatalf("precondition: coordinator source run expected: %d", len(dispatcher.runs))
	}
	source := dispatcher.runs[0]
	plan := usageDriveSourceDecision(t, ctx, svc, store, source.ID, usageWorkerDecision(t, manualID))
	turnKey := *plan.GovernanceTurnKey
	approvalID := pendingPlanApprovalID(t, ctx, store, wsID, plan.ID)

	// 审批挂起窗口：dispatch 步 pending、reservation 仍 reserved、phase5 已落、
	// 绝无 phase6。
	planStored, err := store.Plans().Get(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if step := planStored.Step(0); step.Status != domain.PlanStepPending {
		t.Fatalf("precondition: manual dispatch step must stay pending: %+v", step)
	}
	reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{
		TurnKey: turnKey, Kind: domain.QuotaOutputTokens})
	if err != nil || reservation.Status != domain.QuotaReservationReserved {
		t.Fatalf("pending approval must keep the reservation open: %+v err=%v", reservation, err)
	}
	if _, err := store.TurnReceipts().GetPhase(ctx, turnKey, 5); err != nil {
		t.Fatalf("phase5 must exist while approval is pending: %v", err)
	}
	if _, err := store.TurnReceipts().GetPhase(ctx, turnKey, 6); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("phase6 must not close while a plan step is pending: %v", err)
	}

	// 批准 → Worker Run 创建并携带本 Turn 的治理身份。
	if _, err := svc.ResolveApproval(ctx, approvalID, true, "user_op", "", domain.ApprovalScopeOnce); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("approved dispatch must create one worker run: %d", len(dispatcher.runs))
	}
	worker := dispatcher.runs[1]
	workerStored, err := store.Runs().Get(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	governance, _ := workerStored.Input["governance"].(map[string]any)
	if governance["goal_id"] != goal.ID {
		t.Fatalf("approved worker must inherit the governance turn identity: %#v", governance)
	}
	if _, ok := governanceInt64Of(t, governance); !ok {
		t.Fatalf("governance identity lacks turn_seq: %#v", governance)
	}

	// Worker 终态 + 用量 → 关闭性触发源收口：source 的 absent evidence 在
	// 关闭时刻合成（P1-4），Worker 用量正常 committed。
	usageStartRun(t, ctx, svc, worker.ID)
	usageInjectRunUsage(t, ctx, svc, store, worker.ID, fullUsageCounters(100, 40, 30, 30, 60))
	usageDriveSourceSucceeded(t, ctx, svc, worker.ID)
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}

	phase6 := usagePhase6(t, ctx, store, turnKey)
	kinds := usagePayloadKinds(t, phase6)
	if kinds[string(domain.QuotaOutputTokens)] != 1 {
		t.Fatalf("phase6 must contain the output reservation: %v", kinds)
	}
	workerSpend, err := store.Quotas().GetSpend(ctx, domain.QuotaSpendKey{
		TurnKey: turnKey, Kind: domain.QuotaOutputTokens, RunID: worker.ID})
	if err != nil || workerSpend.Status != domain.QuotaSpendCommitted || workerSpend.Amount != 60 {
		t.Fatalf("approved worker spend must commit after close: %+v err=%v", workerSpend, err)
	}
	sourceSpend, err := store.Quotas().GetSpend(ctx, domain.QuotaSpendKey{
		TurnKey: turnKey, Kind: domain.QuotaOutputTokens, RunID: source.ID})
	if err != nil || sourceSpend.Status != domain.QuotaSpendUnresolved || sourceSpend.Amount != 0 {
		t.Fatalf("report-less source must settle as an explicit gap: %+v err=%v", sourceSpend, err)
	}
	reservation, err = store.Quotas().Get(ctx, domain.QuotaReservationKey{
		TurnKey: turnKey, Kind: domain.QuotaOutputTokens})
	if err != nil || reservation.Status != domain.QuotaReservationCommitted ||
		reservation.CommittedAmount != 60 || reservation.ReleasedAmount != 100_000-60 {
		t.Fatalf("reservation must settle after the approved worker finishes: %+v err=%v", reservation, err)
	}
}

// governanceInt64Of 从落库 Run input 的 governance 身份读回 turn_seq（JSON 往返
// 后是 float64；复用 application 包同语义的宽松解析断言）。
func governanceInt64Of(t *testing.T, governance map[string]any) (int64, bool) {
	t.Helper()
	raw, err := json.Marshal(governance["turn_seq"])
	if err != nil {
		t.Fatal(err)
	}
	var seq int64
	if err := json.Unmarshal(raw, &seq); err != nil {
		return 0, false
	}
	return seq, governance != nil
}

// usageStartRun 把 Run 推到 running（worker 终态驱动前置态）。
func usageStartRun(t *testing.T, ctx context.Context, svc *application.Service, runID string) {
	t.Helper()
	usageDriveSourceRunning(t, ctx, svc, runID)
}

// P1-1（复审裁决 #1）：unresolved usage 缺口必须进入准入判定——turn1 关闭时
// 合成的 absent evidence 缺口让下一轮 Coordinator 预检 fail closed（enforce
// 拒绝、audit 放行但 would_deny=true），直到人工对账。
func TestUnresolvedGapDeniesNextTurnAdmission(t *testing.T) {
	driveGapTurn := func(t *testing.T, ctx context.Context, svc *application.Service,
		store *sqlstore.Store, rootID, sourceID string) {
		t.Helper()
		// 关闭性触发源：为无 report 的受管 Run 合成 absent evidence 并落
		// unresolved 缺口，本 Turn 关闭。
		if err := svc.StartCoordinator(ctx, rootID); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("enforce denies preflight with unresolved reason", func(t *testing.T) {
		ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
		root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
			Title: "unresolved gap enforce", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
			AcceptanceCriteria: []string{"unprovable usage blocks the next turn"},
		})
		if err != nil {
			t.Fatal(err)
		}
		goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
		if err != nil {
			t.Fatal(err)
		}
		goal = setGoalQuotaPolicies(t, ctx, store, goal,
			domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: 1_000_000, Enforcement: domain.QuotaEnforcementEnforce})

		source := dispatcher.runs[0]
		plan := usageDriveSourceDecision(t, ctx, svc, store, source.ID, usageWorkerDecision(t, workerID))
		if len(dispatcher.runs) != 2 {
			t.Fatalf("one governed worker expected: %d", len(dispatcher.runs))
		}
		worker := dispatcher.runs[1]
		usageStartRun(t, ctx, svc, worker.ID)
		usageDriveSourceSucceeded(t, ctx, svc, worker.ID) // kimi 风格：终态但无任何 report

		driveGapTurn(t, ctx, svc, store, root.ID, source.ID)
		gaps, err := store.Quotas().ListUnresolved(ctx, goal.ID, domain.QuotaOutputTokens)
		if err != nil || len(gaps) < 1 {
			t.Fatalf("absent close must leave unresolved gaps: gaps=%+v err=%v", gaps, err)
		}
		reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{
			TurnKey: *plan.GovernanceTurnKey, Kind: domain.QuotaOutputTokens})
		if err != nil || reservation.Status != domain.QuotaReservationReleased {
			t.Fatalf("gap-only turn must release its reservation: %+v err=%v", reservation, err)
		}

		state, err := store.TaskCoordinators().GetState(ctx, root.ID)
		if err != nil {
			t.Fatal(err)
		}
		expected := state.Version
		state.Status = domain.CoordinatorQueued
		state.Phase = "recovering"
		state.CurrentRunID = ""
		state.CurrentAction = "recover"
		state.NextActionAt = nil
		state.Data = map[string]any{"control_action": "recover"}
		if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
			t.Fatal(err)
		}
		startErr := svc.StartCoordinator(ctx, root.ID)
		var decisionErr *application.PlanDecisionError
		if !errors.As(startErr, &decisionErr) || decisionErr.Code != domain.GovernanceErrorPlanQuotaDenied ||
			decisionErr.Path != "/quota/"+string(domain.QuotaOutputTokens) {
			t.Fatalf("unresolved gap must deny the next turn in preflight: err=%v", startErr)
		}
		if !strings.Contains(decisionErr.Message, "无法证明") ||
			!strings.Contains(decisionErr.Message, string(domain.QuotaOutputTokens)) {
			t.Fatalf("denial must explain the unresolved gap semantics: %q", decisionErr.Message)
		}
		if len(dispatcher.runs) != 2 {
			t.Fatalf("denied turn must not dispatch a run: %d", len(dispatcher.runs))
		}
	})

	t.Run("audit admits but records would_deny", func(t *testing.T) {
		ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
		root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
			Title: "unresolved gap audit", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
			AcceptanceCriteria: []string{"audit records the unresolved gap without denying"},
		})
		if err != nil {
			t.Fatal(err)
		}
		goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
		if err != nil {
			t.Fatal(err)
		}
		goal = setGoalQuotaPolicies(t, ctx, store, goal,
			domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: 1_000_000, Enforcement: domain.QuotaEnforcementAudit})

		source := dispatcher.runs[0]
		plan := usageDriveSourceDecision(t, ctx, svc, store, source.ID, usageWorkerDecision(t, workerID))
		if len(dispatcher.runs) != 2 {
			t.Fatalf("one governed worker expected: %d", len(dispatcher.runs))
		}
		worker := dispatcher.runs[1]
		usageStartRun(t, ctx, svc, worker.ID)
		usageDriveSourceSucceeded(t, ctx, svc, worker.ID)
		driveGapTurn(t, ctx, svc, store, root.ID, source.ID)
		if _, err := store.TurnReceipts().GetPhase(ctx, *plan.GovernanceTurnKey, 6); err != nil {
			t.Fatalf("turn must close: %v", err)
		}

		state, err := store.TaskCoordinators().GetState(ctx, root.ID)
		if err != nil {
			t.Fatal(err)
		}
		expected := state.Version
		state.Status = domain.CoordinatorQueued
		state.Phase = "recovering"
		state.CurrentRunID = ""
		state.CurrentAction = "recover"
		state.NextActionAt = nil
		state.Data = map[string]any{"control_action": "recover"}
		if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
			t.Fatal(err)
		}
		if err := svc.StartCoordinator(ctx, root.ID); err != nil {
			t.Fatal(err)
		}
		if len(dispatcher.runs) != 3 {
			t.Fatalf("audit must admit the next coordinator run: %d", len(dispatcher.runs))
		}
		runs, err := store.Runs().ListByWorkItem(ctx, root.ID)
		if err != nil || len(runs) != 2 {
			t.Fatalf("two coordinator runs expected: runs=%d err=%v", len(runs), err)
		}
		admission, _ := runs[1].Input["usage_quota_admission"].(map[string]any)
		entry, _ := admission[string(domain.QuotaOutputTokens)].(map[string]any)
		if entry == nil {
			t.Fatalf("usage admission evidence missing: %#v", runs[1].Input["usage_quota_admission"])
		}
		if wouldDeny, _ := entry["would_deny"].(bool); !wouldDeny {
			t.Fatalf("audit decision must flag would_deny: %#v", entry)
		}
		if unresolved, _ := entry["unresolved"].(bool); !unresolved {
			t.Fatalf("audit decision must flag unresolved: %#v", entry)
		}
		if reason, _ := entry["reason"].(string); !strings.Contains(reason, "无法证明") {
			t.Fatalf("audit decision must carry the gap reason: %#v", entry)
		}
		_ = goal
	})
}

// P1-1：worker 创建闸与预检同口径——reservation 仍有冻结余额但 Goal 存在
// 无法证明的 usage 缺口时，enforce 下的 retry 创建同样被拒（缺口先于预算
// 生效，人工对账前不放行）。
func TestUnresolvedGapGatesWorkerRetryCreation(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "unresolved gap gates retry", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"gaps gate worker retry creation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	const limit = int64(1_000)
	goal = setGoalQuotaPolicies(t, ctx, store, goal,
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: limit, Enforcement: domain.QuotaEnforcementEnforce})

	source := dispatcher.runs[0]
	plan := usageDriveSourceDecision(t, ctx, svc, store, source.ID, usageWorkerDecision(t, workerID, workerID))
	turnKey := *plan.GovernanceTurnKey
	if len(dispatcher.runs) != 3 {
		t.Fatalf("two governed workers expected: %d", len(dispatcher.runs))
	}
	first, second := dispatcher.runs[1], dispatcher.runs[2]

	// first 用 10；second 报 995 → 超出剩余容量（990）落 unresolved 缺口，
	// 且 failure 挂起 retry checkpoint（Turn 保持开放、不关闭）。
	usageStartRun(t, ctx, svc, first.ID)
	usageInjectRunUsage(t, ctx, svc, store, first.ID, fullUsageCounters(20, 8, 6, 6, 10))
	usageDriveSourceSucceeded(t, ctx, svc, first.ID)
	usageStartRun(t, ctx, svc, second.ID)
	usageInjectRunUsage(t, ctx, svc, store, second.ID, fullUsageCounters(1500, 500, 500, 500, 995))
	if err := svc.RecordRunStatus(ctx, second.ID, domain.RunFailed, map[string]any{
		"code": "transport_stream", "message": "retryable worker failure", "retryable": true,
	}); err != nil {
		t.Fatal(err)
	}

	gaps, err := store.Quotas().ListUnresolved(ctx, goal.ID, domain.QuotaOutputTokens)
	if err != nil || len(gaps) != 1 || !strings.Contains(gaps[0].Reason, "exceeds") {
		t.Fatalf("overflow gap expected: gaps=%+v err=%v", gaps, err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil || state.Status != domain.CoordinatorWaitingRetry ||
		coordinatorControlActionForTest(state) != "retry_worker" {
		t.Fatalf("precondition: retry checkpoint expected: %+v err=%v", state, err)
	}

	// 到期的 retry 创建走 worker 创建闸：reservation 冻结余额 1000≠0，但缺口
	// 存在 → enforce 拒绝（无 retry run、Turn 保持开放）。
	expected := state.Version
	due := time.Now().UTC().Add(-time.Second)
	state.NextActionAt = &due
	if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		t.Fatal(err)
	}
	startErr := svc.StartCoordinator(ctx, root.ID)
	var decisionErr *application.PlanDecisionError
	if !errors.As(startErr, &decisionErr) || decisionErr.Code != domain.GovernanceErrorPlanQuotaDenied ||
		!strings.Contains(decisionErr.Message, "无法证明") {
		t.Fatalf("worker gate must deny retry creation on unresolved gap: err=%v", startErr)
	}
	if len(dispatcher.runs) != 3 {
		t.Fatalf("denied retry must not create a run: %d", len(dispatcher.runs))
	}
	reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{
		TurnKey: turnKey, Kind: domain.QuotaOutputTokens})
	if err != nil || reservation.Status != domain.QuotaReservationReserved {
		t.Fatalf("turn must stay open after the denied retry: %+v err=%v", reservation, err)
	}
}

// 复审裁决（修复 B）：plan_dispatch 审批拒绝后的治理收口——reservation 离开
// reserved（source-only 结算 + phase6）、Todo waiting→blocked、Coordinator
// blocked（plan_dispatch_rejected）；幂等重放同一拒绝决定不二次收口。
func TestRejectedPlanDispatchSettlesGovernanceTurn(t *testing.T) {
	manualNow := time.Now().UTC()
	manualProfile := &domain.AgentProfile{
		ID: "agent_manual_reject_settlement", Name: "Manual Reject", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock"},
		Policy:            domain.AgentPolicy{ApprovalPolicy: domain.ApprovalPolicyManual},
		CreatedAt:         manualNow, UpdatedAt: manualNow,
	}
	ctx, svc, store, dispatcher, root, goal := usageCoordinatorEnvForTest(t,
		"reject settles governance turn", []*domain.AgentProfile{manualProfile},
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: 100_000, Enforcement: domain.QuotaEnforcementEnforce})
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	plan := usageDriveSourceDecision(t, ctx, svc, store, source.ID, usageWorkerDecision(t, manualProfile.ID))
	turnKey := *plan.GovernanceTurnKey
	approvalID := pendingPlanApprovalID(t, ctx, store, root.WorkspaceID, plan.ID)

	if _, err := svc.ResolveApproval(ctx, approvalID, false, "user_op", "路线否决", domain.ApprovalScopeOnce); err != nil {
		t.Fatal(err)
	}

	// quota 台账收口：source-only 结算（committed=0 → released）、source absent
	// spend 落账、phase6 存在、无 active 预算占用。
	reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{
		TurnKey: turnKey, Kind: domain.QuotaOutputTokens})
	if err != nil || (reservation.Status != domain.QuotaReservationReleased &&
		reservation.Status != domain.QuotaReservationCommitted) {
		t.Fatalf("rejected turn must settle its reservation: %+v err=%v", reservation, err)
	}
	if reservation.CommittedAmount != 0 {
		t.Fatalf("source-only settlement must commit nothing: %+v", reservation)
	}
	spend, err := store.Quotas().ListSpendByTurn(ctx, turnKey)
	if err != nil || len(spend) == 0 {
		t.Fatalf("source absent spend expected: spend=%+v err=%v", spend, err)
	}
	for _, entry := range spend {
		if entry.Key.RunID == source.ID && entry.Status != domain.QuotaSpendUnresolved {
			t.Fatalf("report-less source must settle as unresolved: %+v", entry)
		}
	}
	usagePhase6(t, ctx, store, turnKey)
	if active, err := store.Quotas().SumActiveReserved(ctx, goal.ID, domain.QuotaOutputTokens); err != nil || active != 0 {
		t.Fatalf("rejected turn must release all active budget: active=%d err=%v", active, err)
	}

	// Todo waiting→blocked（fixture 返回的 goal 快照早于 Todo 挂链，重读取回）。
	goal, err = store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	if todo.Status != domain.TodoBlocked {
		t.Fatalf("rejected turn must block the todo: %+v", todo)
	}
	if goal.Status != domain.GoalBlocked || goal.Phase != "blocked" || todo.Claim != nil {
		t.Fatalf("rejected turn must block Goal/Todo and release governance ownership: goal=%+v todo=%+v", goal, todo)
	}

	// Coordinator 用户可见收口。
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil || state.Status != domain.CoordinatorBlocked ||
		state.BlockerCode != "plan_dispatch_rejected" {
		t.Fatalf("coordinator must surface the rejection blocker: state=%+v err=%v", state, err)
	}

	// 幂等重放：同一拒绝决定不二次追加、不报错。
	phases, err := store.TurnReceipts().ListPhases(ctx, turnKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveApproval(ctx, approvalID, false, "user_op", "路线否决", domain.ApprovalScopeOnce); err != nil {
		t.Fatal(err)
	}
	phasesAfter, err := store.TurnReceipts().ListPhases(ctx, turnKey)
	if err != nil || len(phasesAfter) != len(phases) {
		t.Fatalf("replay must not append phases: before=%d after=%d err=%v", len(phases), len(phasesAfter), err)
	}
	spendAfter, err := store.Quotas().ListSpendByTurn(ctx, turnKey)
	if err != nil || len(spendAfter) != len(spend) {
		t.Fatalf("replay must not append spend: before=%d after=%d err=%v", len(spend), len(spendAfter), err)
	}
}

func TestUsageQuotaEnforceGatesSystemEvaluationRun(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "evaluation respects usage quota", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"evaluation does not bypass exhausted quota"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	setGoalQuotaPolicies(t, ctx, store, goal, domain.QuotaPolicy{
		Kind: domain.QuotaOutputTokens, Limit: 0, Enforcement: domain.QuotaEnforcementEnforce,
	})
	source := dispatcher.runs[0]
	markCompilerSourceSucceeded(t, ctx, store, source.ID)
	source, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision := compilerDecision(domain.PlanVerbFinish, workerID)
	evaluation := true
	decision.Steps[0].Finish.Evaluation = &evaluation
	plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText)
	var decisionErr *application.PlanDecisionError
	if plan != nil || !errors.As(err, &decisionErr) || decisionErr.Code != domain.GovernanceErrorPlanQuotaDenied ||
		decisionErr.Path != "/quota/"+string(domain.QuotaOutputTokens) {
		t.Fatalf("system evaluation must be denied by exhausted usage quota: plan=%+v err=%v", plan, err)
	}
	if len(dispatcher.runs) != 1 {
		t.Fatalf("quota-denied evaluation must not dispatch a Run: %d", len(dispatcher.runs))
	}
	if existing, lookupErr := store.Plans().LatestByWorkItem(ctx, root.ID); lookupErr != nil || existing != nil {
		t.Fatalf("quota-denied evaluation must roll back the Plan transaction: plan=%+v err=%v", existing, lookupErr)
	}
}

// rejectedPlanDispatchFixture creates a governed plan that is paused at a
// manual dispatch approval. The returned database is used only to install a
// SQLite trigger that aborts one half of the post-decision closure.
func rejectedPlanDispatchFixture(t *testing.T) (context.Context, *sql.DB, *application.Service,
	*sqlstore.Store, domain.TurnKey, string) {
	t.Helper()
	now := time.Now().UTC()
	manualProfile := &domain.AgentProfile{
		ID: "agent_manual_reject_fault", Name: "Manual Reject Fault", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock"},
		Policy:            domain.AgentPolicy{ApprovalPolicy: domain.ApprovalPolicyManual},
		CreatedAt:         now, UpdatedAt: now,
	}
	ctx, db, svc, store, dispatcher, root, _ := usageCoordinatorEnvForTestWithDatabase(t,
		"reject settlement fault injection", []*domain.AgentProfile{manualProfile},
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: 100_000,
			Enforcement: domain.QuotaEnforcementEnforce})
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	plan := usageDriveSourceDecision(t, ctx, svc, store, source.ID, usageWorkerDecision(t, manualProfile.ID))
	turnKey := *plan.GovernanceTurnKey
	approvalID := pendingPlanApprovalID(t, ctx, store, root.WorkspaceID, plan.ID)
	return ctx, db, svc, store, turnKey, approvalID
}

func assertRejectedPlanDispatchOpen(t *testing.T, ctx context.Context, store *sqlstore.Store, key domain.TurnKey) {
	t.Helper()
	reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{
		TurnKey: key, Kind: domain.QuotaOutputTokens,
	})
	if err != nil {
		t.Fatalf("rejection failure must preserve reservation for replay: %v", err)
	}
	if reservation.Status != domain.QuotaReservationReserved {
		t.Fatalf("rejection failure must roll back reservation settlement: %+v", reservation)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range phases {
		if phase.PhaseSeq == 6 {
			t.Fatalf("rejection failure must not leave phase6 behind: %+v", phase)
		}
	}
	spend, err := store.Quotas().ListSpendByTurn(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(spend) != 0 {
		t.Fatalf("rejection failure must roll back spend with reservation: %+v", spend)
	}
	todo, err := store.Todos().Get(ctx, key.TodoID)
	if err != nil {
		t.Fatal(err)
	}
	if todo.Status != domain.TodoWaiting {
		t.Fatalf("rejection failure must leave Todo waiting for replay: %+v", todo)
	}
}

func assertRejectedPlanDispatchClosed(t *testing.T, ctx context.Context, store *sqlstore.Store, key domain.TurnKey) {
	t.Helper()
	reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{
		TurnKey: key, Kind: domain.QuotaOutputTokens,
	})
	if err != nil || reservation.Status == domain.QuotaReservationReserved {
		t.Fatalf("replayed rejection must close reservation: %+v err=%v", reservation, err)
	}
	if reservation.CommittedAmount != 0 || reservation.ReleasedAmount != reservation.ReservedAmount {
		t.Fatalf("source-only rejected turn must release the reservation: %+v", reservation)
	}
	usagePhase6(t, ctx, store, key)
	spend, err := store.Quotas().ListSpendByTurn(ctx, key)
	if err != nil || len(spend) != 1 || spend[0].Status != domain.QuotaSpendUnresolved {
		t.Fatalf("replayed rejection must record one unresolved source spend: %+v err=%v", spend, err)
	}
	todo, err := store.Todos().Get(ctx, key.TodoID)
	if err != nil || todo.Status != domain.TodoBlocked {
		t.Fatalf("replayed rejection must block Todo: %+v err=%v", todo, err)
	}
	goal, err := store.Goals().Get(ctx, key.GoalID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, goal.RootWorkItemID)
	if err != nil || state.Status != domain.CoordinatorBlocked || state.BlockerCode != "plan_dispatch_rejected" {
		t.Fatalf("replayed rejection must block Coordinator: %+v err=%v", state, err)
	}
}

// TestRejectedPlanDispatchSettlementReplaysAfterAtomicFailure proves both
// failure orders. The first call returns an error and leaves no half-close;
// replaying the already rejected approval completes the durable closure.
func TestRejectedPlanDispatchSettlementReplaysAfterAtomicFailure(t *testing.T) {
	t.Run("sweep_success_block_failure", func(t *testing.T) {
		ctx, db, svc, store, turnKey, approvalID := rejectedPlanDispatchFixture(t)
		if _, err := db.Exec(`CREATE TRIGGER rejected_block_injected_failure
BEFORE UPDATE OF status ON task_coordinator_states
WHEN NEW.blocker_code = 'plan_dispatch_rejected'
BEGIN SELECT RAISE(ABORT, 'injected rejection blocker failure'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ResolveApproval(ctx, approvalID, false, "user_fault", "路线否决", domain.ApprovalScopeOnce); err == nil {
			t.Fatal("blocker failure must be returned to the caller")
		}
		assertRejectedPlanDispatchOpen(t, ctx, store, turnKey)
		if _, err := db.Exec(`DROP TRIGGER rejected_block_injected_failure`); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ResolveApproval(ctx, approvalID, false, "user_fault", "路线否决", domain.ApprovalScopeOnce); err != nil {
			t.Fatalf("same rejected approval must repair after blocker failure: %v", err)
		}
		assertRejectedPlanDispatchClosed(t, ctx, store, turnKey)
	})

	t.Run("sweep_failure_block_not_reached", func(t *testing.T) {
		ctx, db, svc, store, turnKey, approvalID := rejectedPlanDispatchFixture(t)
		if _, err := db.Exec(`CREATE TRIGGER rejected_sweep_injected_failure
BEFORE UPDATE OF status ON quota_reservations
WHEN NEW.quota_kind = 'output_tokens' AND NEW.status <> OLD.status
BEGIN SELECT RAISE(ABORT, 'injected rejection sweep failure'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ResolveApproval(ctx, approvalID, false, "user_fault", "路线否决", domain.ApprovalScopeOnce); err == nil {
			t.Fatal("sweep failure must be returned to the caller")
		}
		assertRejectedPlanDispatchOpen(t, ctx, store, turnKey)
		if _, err := db.Exec(`DROP TRIGGER rejected_sweep_injected_failure`); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ResolveApproval(ctx, approvalID, false, "user_fault", "路线否决", domain.ApprovalScopeOnce); err != nil {
			t.Fatalf("same rejected approval must repair after sweep failure: %v", err)
		}
		assertRejectedPlanDispatchClosed(t, ctx, store, turnKey)
	})
}
