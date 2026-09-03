package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func TestGovernedTurnRecoveryCheckpointMigrationFreshUpgradeAndRerun(t *testing.T) {
	db := nativeGovernanceDB(t, "governed-turn-recovery-checkpoint.db")
	applyNativeGovernanceMigrations(t, db, "0029_governance_handoff_projection.sql")
	applyNativeGovernanceMigrations(t, db, "")

	assertNativeColumns(t, db, "turn_receipt_headers", "source_run_id", "plan_client_key", "decision_digest")
	for _, name := range []string{
		"idx_turn_receipt_headers_source_run",
		"idx_turn_receipt_headers_plan_client_key",
	} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("0030 must create index %s", name)
		}
	}
	for _, name := range []string{
		"turn_receipt_headers_recovery_checkpoint_insert",
		"turn_receipt_headers_recovery_checkpoint_update",
		"quota_spend_canonical_amount_guard",
	} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("0030 must create trigger %s", name)
		}
	}

	var first int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	applyNativeGovernanceMigrations(t, db, "")
	var second int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("0030 rerun must not add a migration record: first=%d second=%d", first, second)
	}
}

func checkpointCanonicalUsage(t *testing.T, runID string, output int64) (string, string) {
	t.Helper()
	zero := int64(0)
	usage := &domain.CanonicalUsageV1{
		SchemaVersion: domain.CanonicalUsageSchemaVersionV1,
		RunID:         runID,
		Basis:         domain.UsageBasisPerRun,
		Counters: domain.UsageCountersV1{
			InputTokensTotal: &zero, InputUncachedTokens: &zero,
			CacheReadTokens: &zero, CacheWriteTokens: &zero,
			OutputTokens: &output,
		},
		ResolvedKinds: []domain.QuotaKind{
			domain.QuotaCacheReadTokens, domain.QuotaCacheWriteTokens,
			domain.QuotaInputTokensTotal, domain.QuotaInputUncachedTokens,
			domain.QuotaOutputTokens,
		},
		UnresolvedKinds: []domain.QuotaKind{},
		Provenance: domain.UsageProvenanceV1{
			AdapterID: "mock", Protocol: "migration", ProtocolVersion: "v1",
			Source: "migration_test", ReportedBasis: domain.UsageBasisPerRun,
			AgentID: "agent_native_1", Mapping: "migration fixture",
		},
	}
	if err := usage.Seal(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), usage.Digest
}

func TestGovernedTurnRecoveryCheckpointMigrationGuardsAndQuotaAmount(t *testing.T) {
	db := nativeGovernanceDB(t, "governed-turn-recovery-checkpoint-guards.db")
	applyNativeGovernanceMigrations(t, db, "")
	seedNativeGovernanceFixtures(t, db)
	const now = "2026-09-01T00:00:00Z"

	coordinator, err := sqlstore.New(db).TaskCoordinators().EnsureConfig(context.Background(), "ws_native_1")
	if err != nil {
		t.Fatal(err)
	}
	insertNativeGoal(t, db, "goal_checkpoint", "ws_native_1", "wi_native_root_1")
	insertNativeTodo(t, db, "todo_checkpoint", "goal_checkpoint")
	if _, err := db.Exec(`UPDATE goal_todos SET claim_owner_agent_id='agent_native_1',
		claim_version=1,claim_claimed_at=?,claim_expires_at=?,status='claimed'
		WHERE id='todo_checkpoint'`, now, "2026-09-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE goal_todos SET status='running',last_turn_seq=1 WHERE id='todo_checkpoint'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE goals SET status='active',phase='execution',current_todo_id='todo_checkpoint'
		WHERE id='goal_checkpoint'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO execution_runs
		(id,workspace_id,work_item_id,agent_profile_id,status,input,version,created_at,updated_at)
		VALUES ('run_checkpoint_source','ws_native_1','wi_native_root_1',?,'succeeded','{}',1,?,?)`, coordinator.AgentProfileID, now, now); err != nil {
		t.Fatal(err)
	}
	planKey := "governance:goal_checkpoint:todo_checkpoint:1"
	if _, err := db.Exec(`INSERT INTO turn_receipt_headers
		(goal_id,todo_id,turn_seq,attempt,schema_version,input_snapshot_digest,admission_client_key,
		source_run_id,plan_client_key,decision_digest,canonical_digest,created_at)
		VALUES ('goal_checkpoint','todo_checkpoint',1,1,'plan-decision/v2',?,?,?,?,?,?,?)`,
		nativeDigest("i"), "plan-decision:run_checkpoint_source", "run_checkpoint_source", planKey,
		nativeDigest("d"), nativeDigest("h"), now); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `INSERT INTO turn_receipt_headers
		(goal_id,todo_id,turn_seq,attempt,schema_version,input_snapshot_digest,admission_client_key,
		source_run_id,canonical_digest,created_at)
		VALUES ('goal_checkpoint','todo_checkpoint',2,1,'plan-decision/v2',?,?,?, ?,?)`,
		nativeDigest("i"), "partial", "run_checkpoint_source", nativeDigest("h"), now)
	assertNativeExecFails(t, db, `UPDATE turn_receipt_headers SET decision_digest=?
		WHERE goal_id='goal_checkpoint' AND todo_id='todo_checkpoint' AND turn_seq=1`, nativeDigest("x"))

	policyDigest := nativeDigest("p")
	if _, err := db.Exec(`INSERT INTO quota_reservations
		(goal_id,todo_id,turn_seq,quota_kind,status,reserved_amount,committed_amount,released_amount,
		policy_limit,policy_enforcement,policy_digest,version,created_at,updated_at)
		VALUES ('goal_checkpoint','todo_checkpoint',1,'output_tokens','reserved',10,0,0,100,'audit',?,1,?,?)`,
		policyDigest, now, now); err != nil {
		t.Fatal(err)
	}
	insertCheckpointRun := func(t *testing.T, runID string, output int64) string {
		t.Helper()
		canonicalJSON, digest := checkpointCanonicalUsage(t, runID, output)
		input := `{"governance":{"goal_id":"goal_checkpoint","todo_id":"todo_checkpoint","turn_seq":1}}`
		if _, err := db.Exec(`INSERT INTO execution_runs
			(id,workspace_id,work_item_id,agent_profile_id,status,input,version,created_at,updated_at,
			canonical_usage,canonical_usage_digest)
			VALUES (?,?,?,?,?,?,1,?,?,?,?)`, runID, "ws_native_1", "wi_native_root_1", "agent_native_1",
			"succeeded", input, now, now, canonicalJSON, digest); err != nil {
			t.Fatal(err)
		}
		return digest
	}
	insertSpend := func(t *testing.T, runID string, amount int64, status, reason, digest string) error {
		t.Helper()
		_, err := db.Exec(`INSERT INTO quota_spend_entries
			(goal_id,todo_id,turn_seq,quota_kind,run_id,amount,usage_basis,usage_digest,policy_digest,status,reason,created_at)
			VALUES ('goal_checkpoint','todo_checkpoint',1,'output_tokens',?,?,'per_run',?,?,?, ?,?)`,
			runID, amount, digest, policyDigest, status, reason, now)
		return err
	}
	fitDigest := insertCheckpointRun(t, "run_checkpoint_fit", 6)
	if err := insertSpend(t, "run_checkpoint_fit", 6, "committed", "", fitDigest); err != nil {
		t.Fatalf("matching canonical spend must insert: %v", err)
	}
	capacityDigest := insertCheckpointRun(t, "run_checkpoint_capacity", 4)
	if err := insertSpend(t, "run_checkpoint_capacity", 0, "unresolved", "resolved usage still fits", capacityDigest); err == nil {
		t.Fatal("capacity-sufficient resolved usage must not escape through unresolved")
	}
	overDigest := insertCheckpointRun(t, "run_checkpoint_over", 5)
	if err := insertSpend(t, "run_checkpoint_over", 0, "unresolved", "resolved usage exceeds remaining reservation", overDigest); err != nil {
		t.Fatalf("over-capacity usage may be recorded as an unresolved gap: %v", err)
	}
	var entries int
	if err := db.QueryRow(`SELECT count(*) FROM quota_spend_entries WHERE goal_id='goal_checkpoint'`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 2 {
		t.Fatalf("only matching committed and over-capacity unresolved entries should persist: %d", entries)
	}
}
