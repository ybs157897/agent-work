package sqlstore_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func quotaStoreDigest(ch byte) string {
	return "sha256:" + strings.Repeat(string("abcdef"[int(ch)%6]), 64)
}

type quotaStoreFixture struct {
	db       *sql.DB
	store    *sqlstore.Store
	goal     *domain.Goal
	todo     *domain.Todo
	header   *domain.TurnReceiptHeader
	workerID string
}

func newQuotaStoreFixture(t *testing.T) *quotaStoreFixture {
	t.Helper()
	ctx := context.Background()
	db := openWakeupTestDB(t)
	seedWorkspace(t, db)
	store := sqlstore.New(db)
	workerID := "agent_quota_worker"
	now := time.Now().UTC()
	seedAgent(t, store, &domain.AgentProfile{
		ID: workerID, WorkspaceID: "ws_wk", Name: "quota worker", Role: "worker",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	})
	insertWorkItem(t, db, "wi_quota_root")
	goal := &domain.Goal{
		ID: "goal_quota", WorkspaceID: "ws_wk", RootWorkItemID: "wi_quota_root",
		Objective: "quota test", AcceptanceContract: []string{"done"},
		Status: domain.GoalActive, Phase: "execution", QuotaPolicies: []domain.QuotaPolicy{
			{Kind: domain.QuotaOutputTokens, Limit: 1000, Enforcement: domain.QuotaEnforcementAudit},
		},
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Goals().Create(ctx, goal); err != nil {
		t.Fatal(err)
	}
	todo := &domain.Todo{
		ID: "todo_quota", GoalID: goal.ID, Class: domain.TodoAdvancement,
		Status: domain.TodoPending, Instruction: "quota test", Acceptance: []string{"done"},
		Priority: domain.PriorityMedium, Predecessors: []string{}, Successors: []string{},
		DecisionScope: domain.DecisionScope{
			WorkItemIDs: []string{goal.RootWorkItemID}, AgentIDs: []string{workerID},
			RuntimeCapabilities: []string{}, WriteScopes: []string{}, MaxDispatch: 4,
		},
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Todos().Create(ctx, todo); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Todos().Claim(ctx, todo.ID, workerID, now, now.Add(time.Hour), todo.Version)
	if err != nil {
		t.Fatal(err)
	}
	header := &domain.TurnReceiptHeader{
		TurnKey: domain.TurnKey{GoalID: goal.ID, TodoID: todo.ID, TurnSeq: 1},
		Attempt: 1, SchemaVersion: "quota-test/v1", InputSnapshotDigest: quotaStoreDigest('i'),
		AdmissionClientKey: "quota-admit", CreatedAt: now.Add(time.Millisecond),
	}
	header.CanonicalDigest, err = application.ComputeTurnReceiptHeaderDigest(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TurnReceipts().Admit(ctx, header, workerID, claimed.Version); err != nil {
		t.Fatal(err)
	}
	return &quotaStoreFixture{db: db, store: store, goal: goal, todo: todo, header: header, workerID: workerID}
}

func (f *quotaStoreFixture) close() { _ = f.db.Close() }

func TestTurnReceiptQuotaReservationReferencesAreSameTurnAndExisting(t *testing.T) {
	cases := []struct {
		name      string
		reference func(*quotaStoreFixture) string
		reserve   bool
		wantErr   bool
	}{
		{name: "same turn existing", reserve: true, reference: func(f *quotaStoreFixture) string {
			return f.header.TurnKey.GoalID + ":" + f.header.TurnKey.TodoID + ":1:" + string(domain.QuotaOutputTokens)
		}},
		{name: "cross turn", reference: func(f *quotaStoreFixture) string {
			return f.header.TurnKey.GoalID + ":" + f.header.TurnKey.TodoID + ":2:" + string(domain.QuotaOutputTokens)
		}, wantErr: true},
		{name: "malformed", reference: func(*quotaStoreFixture) string { return "not-a-quota-key" }, wantErr: true},
		{name: "missing", reference: func(f *quotaStoreFixture) string {
			return f.header.TurnKey.GoalID + ":" + f.header.TurnKey.TodoID + ":1:" + string(domain.QuotaInputTokensTotal)
		}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newQuotaStoreFixture(t)
			defer f.close()
			ctx := context.Background()
			if tc.reserve {
				if created, err := f.store.Quotas().Reserve(ctx,
					quotaReservationFor(f, domain.QuotaOutputTokens, 100)); err != nil || !created {
					t.Fatalf("same-turn reservation setup failed: created=%v err=%v", created, err)
				}
			}
			phase := &domain.TurnReceiptPhase{
				TurnKey: f.header.TurnKey, PhaseSeq: 1,
				Phase:                domain.TurnReceiptPhaseDecisionDecode,
				Payload:              map[string]any{"quota_reference_test": tc.name},
				QuotaReservationKeys: []string{tc.reference(f)}, CreatedAt: time.Now().UTC(),
			}
			setGovernancePhaseDigest(t, phase)
			_, err := f.store.TurnReceipts().AppendPhase(ctx, phase)
			if tc.wantErr && err == nil {
				t.Fatal("invalid quota reservation reference must be rejected")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("same-turn existing quota reservation should pass: %v", err)
			}
		})
	}
}

func TestTodoClaimRenewalPreservesClaimedAtAndRejectsInvalidDirectWrites(t *testing.T) {
	f := newQuotaStoreFixture(t)
	defer f.close()
	ctx := context.Background()
	todo, err := f.store.Todos().Get(ctx, f.todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeClaimedAt := todo.Claim.ClaimedAt
	beforeVersion := todo.Version
	renewedAt := time.Now().UTC()
	renewed, err := f.store.Todos().RenewClaim(ctx, todo.ID, f.workerID, todo.ClaimVersion,
		renewedAt, renewedAt.Add(time.Hour), todo.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.Claim.ClaimedAt.Equal(beforeClaimedAt) || renewed.Version != beforeVersion+1 ||
		!renewed.Claim.ExpiresAt.After(todo.Claim.ExpiresAt) {
		t.Fatalf("renewal must preserve claimed_at and extend only expiry/version: before=%+v after=%+v", todo, renewed)
	}
	if _, err := f.db.Exec(`UPDATE goal_todos SET claim_claimed_at=? WHERE id=?`,
		time.Now().UTC().Format(time.RFC3339Nano), todo.ID); err == nil {
		t.Fatal("same-generation direct claimed_at rewrite must fail")
	}
	if _, err := f.db.Exec(`UPDATE goal_todos SET claim_expires_at=? WHERE id=?`,
		todo.Claim.ExpiresAt.Format(time.RFC3339Nano), todo.ID); err == nil {
		t.Fatal("same-generation direct expiry shortening must fail")
	}
}

func quotaReservationFor(f *quotaStoreFixture, kind domain.QuotaKind, amount int64) *domain.QuotaReservation {
	now := f.header.CreatedAt
	reservation := &domain.QuotaReservation{
		Key:    domain.QuotaReservationKey{TurnKey: f.header.TurnKey, Kind: kind},
		Status: domain.QuotaReservationReserved, ReservedAmount: amount,
		PolicyLimit: 1000, PolicyEnforcement: domain.QuotaEnforcementAudit,
		PolicyDigest: quotaStoreDigest('p'), Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	return reservation
}

func quotaPriceSnapshot(t *testing.T) *domain.PriceSnapshotRef {
	t.Helper()
	price := &domain.PriceSnapshotRef{
		ModelRef: "model:test", Currency: "USD",
		InputUncachedMicroUSDPerMillion: 100,
		CacheReadMicroUSDPerMillion:     50,
		CacheWriteMicroUSDPerMillion:    75,
		OutputMicroUSDPerMillion:        200,
		EffectiveAt:                     time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC),
		PriceVersion:                    "price-v1",
	}
	if err := price.Normalize(); err != nil {
		t.Fatal(err)
	}
	return price
}

func quotaRun(t *testing.T, db *sql.DB, id, workItemID, agentID, status string,
	prices ...*domain.PriceSnapshotRef) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	input := map[string]any{"governance": map[string]any{
		"goal_id": "goal_quota", "todo_id": "todo_quota", "turn_seq": int64(1),
	}}
	if len(prices) > 0 && prices[0] != nil {
		input["price_snapshot"] = prices[0]
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO execution_runs
		(id,workspace_id,work_item_id,agent_profile_id,status,input,version,created_at,updated_at)
		VALUES (?,?,?,?,?,?,1,?,?)`, id, "ws_wk", workItemID, agentID, status, string(raw), now, now); err != nil {
		t.Fatal(err)
	}
}

func setQuotaRunCanonical(t *testing.T, f *quotaStoreFixture, runID string,
	kind domain.QuotaKind, amount int64, unresolved bool, priceDigest string) string {
	t.Helper()
	zeroTotal, zeroUncached, zeroRead, zeroWrite, zeroOutput := int64(0), int64(0), int64(0), int64(0), int64(0)
	usage := &domain.CanonicalUsageV1{
		SchemaVersion: domain.CanonicalUsageSchemaVersionV1, RunID: runID,
		Basis: domain.UsageBasisPerRun,
		Counters: domain.UsageCountersV1{
			InputTokensTotal:    &zeroTotal,
			InputUncachedTokens: &zeroUncached,
			CacheReadTokens:     &zeroRead,
			CacheWriteTokens:    &zeroWrite,
			OutputTokens:        &zeroOutput,
		},
		Provenance: domain.UsageProvenanceV1{
			AdapterID: "quota-test", Protocol: "fixture", ProtocolVersion: "v1",
			Source: "quota_test", ReportedBasis: domain.UsageBasisPerRun,
			AgentID: f.workerID, Mapping: "fixture",
		},
		ResolvedKinds: []domain.QuotaKind{
			domain.QuotaCacheReadTokens, domain.QuotaCacheWriteTokens,
			domain.QuotaInputTokensTotal, domain.QuotaInputUncachedTokens,
			domain.QuotaOutputTokens,
		},
		UnresolvedKinds: []domain.QuotaKind{},
	}
	value := amount
	if unresolved {
		resolved := make([]domain.QuotaKind, 0, len(usage.ResolvedKinds))
		for _, candidate := range usage.ResolvedKinds {
			if candidate != kind {
				resolved = append(resolved, candidate)
			}
		}
		usage.ResolvedKinds = resolved
		usage.UnresolvedKinds = []domain.QuotaKind{kind}
		usage.UnresolvedReason = "provider did not expose a per-run delta"
		switch kind {
		case domain.QuotaInputTokensTotal:
			usage.Counters.InputTokensTotal = nil
		case domain.QuotaInputUncachedTokens:
			usage.Counters.InputUncachedTokens = nil
		case domain.QuotaCacheReadTokens:
			usage.Counters.CacheReadTokens = nil
		case domain.QuotaCacheWriteTokens:
			usage.Counters.CacheWriteTokens = nil
		case domain.QuotaOutputTokens:
			usage.Counters.OutputTokens = nil
		}
	} else {
		switch kind {
		case domain.QuotaInputTokensTotal:
			usage.Counters.InputTokensTotal = &value
			usage.Counters.InputUncachedTokens = &value
		case domain.QuotaInputUncachedTokens:
			usage.Counters.InputUncachedTokens = &value
			usage.Counters.InputTokensTotal = &value
		case domain.QuotaCacheReadTokens:
			usage.Counters.CacheReadTokens = &value
			usage.Counters.InputTokensTotal = &value
		case domain.QuotaCacheWriteTokens:
			usage.Counters.CacheWriteTokens = &value
			usage.Counters.InputTokensTotal = &value
		case domain.QuotaOutputTokens:
			usage.Counters.OutputTokens = &value
		case domain.QuotaCostMicroUSD:
			usage.CostMicroUSD = &value
			usage.PriceDigest = priceDigest
			usage.ResolvedKinds = []domain.QuotaKind{
				domain.QuotaCacheReadTokens, domain.QuotaCacheWriteTokens,
				domain.QuotaCostMicroUSD, domain.QuotaInputTokensTotal,
				domain.QuotaInputUncachedTokens, domain.QuotaOutputTokens,
			}
		}
	}
	if err := usage.Seal(); err != nil {
		t.Fatal(err)
	}
	run, err := f.store.Runs().Get(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	run.CanonicalUsage = usage
	run.CanonicalUsageDigest = usage.Digest
	if err := f.store.Runs().Update(context.Background(), run, run.Version); err != nil {
		t.Fatal(err)
	}
	return usage.Digest
}

func TestQuotaReservationRoundTripReplayAndSettlement(t *testing.T) {
	ctx := context.Background()
	f := newQuotaStoreFixture(t)
	defer f.close()
	reservation := quotaReservationFor(f, domain.QuotaOutputTokens, 10)
	created, err := f.store.Quotas().Reserve(ctx, reservation)
	if err != nil || !created {
		t.Fatalf("首个 reservation 应创建: created=%v err=%v", created, err)
	}
	got, err := f.store.Quotas().Get(ctx, reservation.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReservedAmount != 10 || got.PolicyDigest != reservation.PolicyDigest || got.Version != 1 {
		t.Fatalf("reservation 往返失真: %+v", got)
	}
	replay := *reservation
	replay.CreatedAt = replay.CreatedAt.Add(time.Hour)
	replay.UpdatedAt = replay.UpdatedAt.Add(time.Hour)
	created, err = f.store.Quotas().Reserve(ctx, &replay)
	if err != nil || created {
		t.Fatalf("相同冻结 intent 应幂等: created=%v err=%v", created, err)
	}
	conflict := *reservation
	conflict.ReservedAmount++
	if _, err := f.store.Quotas().Reserve(ctx, &conflict); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("同 identity 不同 reserved intent 应冲突: %v", err)
	}

	got.Status = domain.QuotaReservationCommitted
	got.CommittedAmount = 7
	got.ReleasedAmount = 3
	got.Version++
	got.UpdatedAt = time.Now().UTC()
	if err := f.store.Quotas().Commit(ctx, got, 1); err != nil {
		t.Fatal(err)
	}
	settled, err := f.store.Quotas().Get(ctx, reservation.Key)
	if err != nil || settled.Status != domain.QuotaReservationCommitted || settled.Version != 2 {
		t.Fatalf("commit 未持久化: %+v err=%v", settled, err)
	}
	settled.Status = domain.QuotaReservationReleased
	settled.Version++
	if err := f.store.Quotas().Release(ctx, settled, 2); err == nil {
		t.Fatal("terminal reservation 不得再次 release")
	}
	if _, err := f.db.Exec(`UPDATE quota_reservations SET policy_limit=1 WHERE goal_id=?`, f.goal.ID); err == nil {
		t.Fatal("直接修改冻结 policy 应被拒")
	}
	if _, err := f.db.Exec(`UPDATE quota_reservations SET version=version+2 WHERE goal_id=?`, f.goal.ID); err == nil {
		t.Fatal("reservation version 必须单步递增")
	}
	released := quotaReservationFor(f, domain.QuotaInputTokensTotal, 5)
	if created, err := f.store.Quotas().Reserve(ctx, released); err != nil || !created {
		t.Fatalf("release reservation 创建失败: created=%v err=%v", created, err)
	}
	released.Status = domain.QuotaReservationReleased
	released.ReleasedAmount = released.ReservedAmount
	released.Version++
	if err := f.store.Quotas().Release(ctx, released, 1); err != nil {
		t.Fatalf("reserved->released 应合法: %v", err)
	}
	expired := quotaReservationFor(f, domain.QuotaCacheReadTokens, 6)
	if created, err := f.store.Quotas().Reserve(ctx, expired); err != nil || !created {
		t.Fatalf("expire reservation 创建失败: created=%v err=%v", created, err)
	}
	expired.Status = domain.QuotaReservationExpired
	expired.ReleasedAmount = expired.ReservedAmount
	expired.Version++
	if err := f.store.Quotas().Expire(ctx, expired, 1); err != nil {
		t.Fatalf("reserved->expired 应合法: %v", err)
	}
}

func TestQuotaCostSnapshotAndSpendIdempotency(t *testing.T) {
	ctx := context.Background()
	f := newQuotaStoreFixture(t)
	defer f.close()
	cost := quotaReservationFor(f, domain.QuotaCostMicroUSD, 100)
	if created, err := f.store.Quotas().Reserve(ctx, cost); err != nil || !created {
		t.Fatalf("cost reservation 创建失败: created=%v err=%v", created, err)
	}
	price := quotaPriceSnapshot(t)
	quotaRun(t, f.db, "run_quota_cost", f.goal.RootWorkItemID, f.workerID, "succeeded", price)
	usageDigest := setQuotaRunCanonical(t, f, "run_quota_cost", domain.QuotaCostMicroUSD, 12, false, price.Digest)
	entry := &domain.QuotaSpendEntry{
		Key:    domain.QuotaSpendKey{TurnKey: f.header.TurnKey, Kind: domain.QuotaCostMicroUSD, RunID: "run_quota_cost"},
		Amount: 12, UsageBasis: "per_run", UsageDigest: usageDigest,
		PolicyDigest: cost.PolicyDigest, PriceDigest: price.Digest,
		Status: domain.QuotaSpendCommitted, CreatedAt: time.Now().UTC(),
	}
	if created, err := f.store.Quotas().AppendSpend(ctx, entry); err != nil || !created {
		t.Fatalf("cost spend append 失败: created=%v err=%v", created, err)
	}
	replay := *entry
	replay.CreatedAt = replay.CreatedAt.Add(time.Hour)
	if created, err := f.store.Quotas().AppendSpend(ctx, &replay); err != nil || created {
		t.Fatalf("cost spend replay 应幂等: created=%v err=%v", created, err)
	}
	conflict := *entry
	conflict.UsageDigest = quotaStoreDigest('v')
	if _, err := f.store.Quotas().AppendSpend(ctx, &conflict); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("同 spend identity 不同 digest 应冲突: %v", err)
	}
	badPrice := *entry
	badPrice.Key.RunID = "run_quota_cost_bad_price"
	badPrice.PriceDigest = quotaStoreDigest('y')
	quotaRun(t, f.db, badPrice.Key.RunID, f.goal.RootWorkItemID, f.workerID, "failed", price)
	badPrice.UsageDigest = setQuotaRunCanonical(t, f, badPrice.Key.RunID,
		domain.QuotaCostMicroUSD, 12, false, price.Digest)
	if _, err := f.store.Quotas().AppendSpend(ctx, &badPrice); err == nil {
		t.Fatal("cost spend 必须匹配 per-Run price digest")
	}

	loaded, err := f.store.Quotas().GetSpend(ctx, entry.Key)
	if err != nil || loaded.Amount != entry.Amount || loaded.PriceDigest != entry.PriceDigest {
		t.Fatalf("spend 往返失真: %+v err=%v", loaded, err)
	}
	if total, err := f.store.Quotas().SumCommitted(ctx, f.goal.ID, domain.QuotaCostMicroUSD); err != nil || total != 12 {
		t.Fatalf("cost committed sum 异常: total=%d err=%v", total, err)
	}
	quotaRun(t, f.db, "run_quota_cost_over", f.goal.RootWorkItemID, f.workerID, "failed", price)
	over := *entry
	over.Key.RunID = "run_quota_cost_over"
	over.Amount = 89
	over.UsageDigest = setQuotaRunCanonical(t, f, over.Key.RunID,
		domain.QuotaCostMicroUSD, over.Amount, false, price.Digest)
	if _, err := f.store.Quotas().AppendSpend(ctx, &over); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("aggregate spend beyond reservation must fail: %v", err)
	}
	if _, err := f.db.Exec(`INSERT INTO work_items
		(id,workspace_id,record_kind,title,status,priority,version,created_at,updated_at)
		VALUES ('wi_quota_foreign','ws_wk','task','foreign','todo','medium',1,?,?)`,
		time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	quotaRun(t, f.db, "run_quota_cost_foreign", "wi_quota_foreign", f.workerID, "failed", price)
	foreign := *entry
	foreign.Key.RunID = "run_quota_cost_foreign"
	foreign.Amount = 1
	foreign.UsageDigest = setQuotaRunCanonical(t, f, foreign.Key.RunID,
		domain.QuotaCostMicroUSD, foreign.Amount, false, price.Digest)
	if _, err := f.store.Quotas().AppendSpend(ctx, &foreign); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("foreign Goal Run spend must fail: %v", err)
	}
}

func TestQuotaUnresolvedAndTurnCountAccounting(t *testing.T) {
	ctx := context.Background()
	f := newQuotaStoreFixture(t)
	defer f.close()
	turn := quotaReservationFor(f, domain.QuotaTurnCount, 1)
	if created, err := f.store.Quotas().Reserve(ctx, turn); err != nil || !created {
		t.Fatalf("turn reservation 创建失败: created=%v err=%v", created, err)
	}
	turn.Status = domain.QuotaReservationCommitted
	turn.CommittedAmount = 1
	turn.Version++
	if err := f.store.Quotas().Commit(ctx, turn, 1); err != nil {
		t.Fatal(err)
	}
	if got, err := f.store.Quotas().SumCommitted(ctx, f.goal.ID, domain.QuotaTurnCount); err != nil || got != 1 {
		t.Fatalf("turn_count 必须从 reservation 计费: got=%d err=%v", got, err)
	}
	quotaRun(t, f.db, "run_quota_unresolved", f.goal.RootWorkItemID, f.workerID, "failed")
	usageDigest := setQuotaRunCanonical(t, f, "run_quota_unresolved",
		domain.QuotaOutputTokens, 0, true, "")
	output := quotaReservationFor(f, domain.QuotaOutputTokens, 20)
	if _, err := f.store.Quotas().Reserve(ctx, output); err != nil {
		t.Fatal(err)
	}
	unresolved := &domain.QuotaSpendEntry{
		Key:    domain.QuotaSpendKey{TurnKey: f.header.TurnKey, Kind: domain.QuotaOutputTokens, RunID: "run_quota_unresolved"},
		Amount: 0, UsageBasis: "per_run", UsageDigest: usageDigest,
		PolicyDigest: output.PolicyDigest, Status: domain.QuotaSpendUnresolved,
		Reason: "provider did not expose a per-run delta", CreatedAt: time.Now().UTC(),
	}
	if created, err := f.store.Quotas().AppendSpend(ctx, unresolved); err != nil || !created {
		t.Fatalf("unresolved spend append 失败: created=%v err=%v", created, err)
	}
	entries, err := f.store.Quotas().ListUnresolved(ctx, f.goal.ID)
	if err != nil || len(entries) != 1 || entries[0].Amount != 0 {
		t.Fatalf("unresolved 列表异常: %#v err=%v", entries, err)
	}
	if total, err := f.store.Quotas().SumCommitted(ctx, f.goal.ID, domain.QuotaOutputTokens); err != nil || total != 0 {
		t.Fatalf("unresolved 不得计入 committed sum: total=%d err=%v", total, err)
	}
	if _, err := f.store.Quotas().GetSpend(ctx, domain.QuotaSpendKey{TurnKey: f.header.TurnKey, Kind: domain.QuotaTurnCount, RunID: "run_quota_unresolved"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("turn_count spend lookup 应拒绝: %v", err)
	}
}

func TestQuotaActiveWorkerCountUsesRootTaskSubtree(t *testing.T) {
	ctx := context.Background()
	f := newQuotaStoreFixture(t)
	defer f.close()
	if _, err := f.db.Exec(`INSERT INTO work_items
		(id,workspace_id,parent_id,record_kind,title,status,priority,version,created_at,updated_at)
		VALUES ('wi_quota_child','ws_wk',?,'task','child','todo','medium',1,?,?)`,
		f.goal.RootWorkItemID, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO work_items
		(id,workspace_id,record_kind,title,status,priority,version,created_at,updated_at)
		VALUES ('wi_quota_other','ws_wk','task','outside','todo','medium',1,?,?)`,
		time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	coordinator, err := f.store.TaskCoordinators().EnsureConfig(ctx, "ws_wk")
	if err != nil {
		t.Fatal(err)
	}
	quotaRun(t, f.db, "run_quota_root", f.goal.RootWorkItemID, f.workerID, "running")
	quotaRun(t, f.db, "run_quota_child", "wi_quota_child", f.workerID, "queued")
	quotaRun(t, f.db, "run_quota_coordinator", f.goal.RootWorkItemID, coordinator.AgentProfileID, "running")
	quotaRun(t, f.db, "run_quota_done", f.goal.RootWorkItemID, f.workerID, "succeeded")
	quotaRun(t, f.db, "run_quota_other", "wi_quota_other", f.workerID, "running")
	count, err := f.store.Quotas().ActiveWorkerCount(ctx, f.goal.ID)
	if err != nil || count != 2 {
		t.Fatalf("active_worker 应只计 root subtree 非终态 worker: count=%d err=%v", count, err)
	}
	if _, err := f.store.Quotas().ActiveWorkerCount(ctx, "goal_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("未知 Goal 应返回 ErrNotFound: %v", err)
	}
}

func TestQuotaDirectSQLRejectsRealAndAppendOnlyMutation(t *testing.T) {
	f := newQuotaStoreFixture(t)
	defer f.close()
	now := f.header.CreatedAt.Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`INSERT INTO quota_reservations
		(goal_id,todo_id,turn_seq,quota_kind,status,reserved_amount,committed_amount,released_amount,
		 policy_limit,policy_enforcement,policy_digest,version,created_at,updated_at)
		VALUES (?,?,1,'output_tokens','reserved',1.5,0,0,10,'audit',?,1,?,?)`,
		f.goal.ID, f.todo.ID, quotaStoreDigest('p'), now, now); err == nil {
		t.Fatal("REAL reservation amount must be rejected")
	}
	if _, err := f.db.Exec(`INSERT INTO quota_reservations
		(goal_id,todo_id,turn_seq,quota_kind,status,reserved_amount,committed_amount,released_amount,
		 policy_limit,policy_enforcement,policy_digest,version,created_at,updated_at)
		VALUES (?,?,1,'cost_microusd','reserved',1,0,0,10,'audit',?,1,?,?)`,
		f.goal.ID, f.todo.ID, quotaStoreDigest('p'), now, now); err != nil {
		t.Fatalf("turn-level cost reservation must not assume one Run price: %v", err)
	}

	if _, err := f.db.Exec(`INSERT INTO quota_reservations
		(goal_id,todo_id,turn_seq,quota_kind,status,reserved_amount,committed_amount,released_amount,
		 policy_limit,policy_enforcement,policy_digest,version,created_at,updated_at)
		VALUES (?,?,1,'output_tokens','reserved',10,0,0,100,'audit',?,1,?,?)`,
		f.goal.ID, f.todo.ID, quotaStoreDigest('p'), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE quota_reservations SET reserved_amount=9 WHERE goal_id=?`, f.goal.ID); err == nil {
		t.Fatal("reservation requested amount must be immutable")
	}
	quotaRun(t, f.db, "run_quota_sql", f.goal.RootWorkItemID, f.workerID, "failed")
	usageDigest := setQuotaRunCanonical(t, f, "run_quota_sql", domain.QuotaOutputTokens, 6, false, "")
	if _, err := f.db.Exec(`INSERT INTO quota_spend_entries
		(goal_id,todo_id,turn_seq,quota_kind,run_id,amount,usage_basis,usage_digest,policy_digest,status,reason,created_at)
		VALUES (?,?,1,'output_tokens',?,1.5,'per_run',?,?, 'committed','',?)`,
		f.goal.ID, f.todo.ID, "run_quota_sql", usageDigest, quotaStoreDigest('p'), now); err == nil {
		t.Fatal("REAL spend amount must be rejected")
	}
	if _, err := f.db.Exec(`INSERT INTO quota_spend_entries
		(goal_id,todo_id,turn_seq,quota_kind,run_id,amount,usage_basis,usage_digest,policy_digest,status,reason,created_at)
		VALUES (?,?,1,'output_tokens',?,6,'per_run',?,?,'committed','',?)`,
		f.goal.ID, f.todo.ID, "run_quota_sql", usageDigest, quotaStoreDigest('p'), now); err != nil {
		t.Fatalf("valid in-scope spend should insert: %v", err)
	}
	quotaRun(t, f.db, "run_quota_sql_evasion", f.goal.RootWorkItemID, f.workerID, "failed")
	evasionDigest := setQuotaRunCanonical(t, f, "run_quota_sql_evasion", domain.QuotaOutputTokens, 4, false, "")
	if _, err := f.db.Exec(`INSERT INTO quota_spend_entries
		(goal_id,todo_id,turn_seq,quota_kind,run_id,amount,usage_basis,usage_digest,policy_digest,status,reason,created_at)
		VALUES (?,?,1,'output_tokens',?,0,'per_run',?,?,'unresolved','resolved usage fits reservation',?)`,
		f.goal.ID, f.todo.ID, "run_quota_sql_evasion", evasionDigest, quotaStoreDigest('p'), now); err == nil {
		t.Fatal("direct SQL must reject unresolved escape when resolved usage still fits remaining reservation")
	}
	quotaRun(t, f.db, "run_quota_sql_over", f.goal.RootWorkItemID, f.workerID, "failed")
	overDigest := setQuotaRunCanonical(t, f, "run_quota_sql_over", domain.QuotaOutputTokens, 5, false, "")
	if _, err := f.db.Exec(`INSERT INTO quota_spend_entries
		(goal_id,todo_id,turn_seq,quota_kind,run_id,amount,usage_basis,usage_digest,policy_digest,status,reason,created_at)
		VALUES (?,?,1,'output_tokens',?,5,'per_run',?,?,'committed','',?)`,
		f.goal.ID, f.todo.ID, "run_quota_sql_over", overDigest, quotaStoreDigest('p'), now); err == nil {
		t.Fatal("direct SQL aggregate spend beyond reservation must be rejected")
	}
	if _, err := f.db.Exec(`INSERT INTO quota_spend_entries
		(goal_id,todo_id,turn_seq,quota_kind,run_id,amount,usage_basis,usage_digest,policy_digest,status,reason,created_at)
		VALUES (?,?,1,'output_tokens',?,0,'per_run',?,?,'unresolved','resolved usage exceeds remaining reservation',?)`,
		f.goal.ID, f.todo.ID, "run_quota_sql_over", overDigest, quotaStoreDigest('p'), now); err != nil {
		t.Fatalf("direct SQL may record an explicit over-capacity unresolved gap: %v", err)
	}
	if _, err := f.db.Exec(`INSERT INTO work_items
		(id,workspace_id,record_kind,title,status,priority,version,created_at,updated_at)
		VALUES ('wi_quota_sql_foreign','ws_wk','task','foreign','todo','medium',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	quotaRun(t, f.db, "run_quota_sql_foreign", "wi_quota_sql_foreign", f.workerID, "failed")
	foreignDigest := setQuotaRunCanonical(t, f, "run_quota_sql_foreign", domain.QuotaOutputTokens, 1, false, "")
	if _, err := f.db.Exec(`INSERT INTO quota_spend_entries
		(goal_id,todo_id,turn_seq,quota_kind,run_id,amount,usage_basis,usage_digest,policy_digest,status,reason,created_at)
		VALUES (?,?,1,'output_tokens',?,1,'per_run',?,?,'committed','',?)`,
		f.goal.ID, f.todo.ID, "run_quota_sql_foreign", foreignDigest, quotaStoreDigest('p'), now); err == nil {
		t.Fatal("direct SQL foreign Goal Run spend must be rejected")
	}
}

func TestQuotaTransactionContextReuse(t *testing.T) {
	ctx := context.Background()
	f := newQuotaStoreFixture(t)
	defer f.close()
	r := quotaReservationFor(f, domain.QuotaOutputTokens, 2)
	if err := f.store.InTx(ctx, func(txctx context.Context) error {
		created, err := f.store.Quotas().Reserve(txctx, r)
		if err != nil || !created {
			return fmt.Errorf("reserve in tx: created=%v err=%w", created, err)
		}
		if _, err := f.store.Quotas().Get(txctx, r.Key); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Quotas().Get(ctx, r.Key); err != nil {
		t.Fatal(err)
	}
}
