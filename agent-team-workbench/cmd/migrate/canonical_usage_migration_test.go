package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func migrationCanonicalUsage(t *testing.T, runID string) (string, string) {
	t.Helper()
	value := func(v int64) *int64 { return &v }
	report := &domain.ProviderUsageReportV1{
		SchemaVersion: domain.ProviderUsageReportSchemaVersionV1,
		RunID:         runID,
		Basis:         domain.UsageBasisPerRun,
		Counters: domain.UsageCountersV1{
			InputTokensTotal: value(3), InputUncachedTokens: value(1),
			CacheReadTokens: value(1), CacheWriteTokens: value(1), OutputTokens: value(1),
		},
		Provenance: domain.UsageProvenanceV1{
			AdapterID: "mock", Protocol: "mock", ProtocolVersion: "v1",
			Source: "test", ReportedBasis: domain.UsageBasisPerRun,
			AgentID: "agent_native_1", Mapping: "test counters",
		},
	}
	usage, err := domain.CanonicalizeProviderUsageReport(report)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), usage.Digest
}

func migrationProviderUsageReport(t *testing.T, runID string, output int64) (string, string) {
	t.Helper()
	value := func(v int64) *int64 { return &v }
	report := &domain.ProviderUsageReportV1{
		SchemaVersion: domain.ProviderUsageReportSchemaVersionV1,
		RunID:         runID, Basis: domain.UsageBasisPerRun,
		Counters: domain.UsageCountersV1{
			InputTokensTotal: value(3), InputUncachedTokens: value(1),
			CacheReadTokens: value(1), CacheWriteTokens: value(1), OutputTokens: value(output),
		},
		Provenance: domain.UsageProvenanceV1{
			AdapterID: "mock", Protocol: "mock", ProtocolVersion: "v1",
			Source: "test", ReportedBasis: domain.UsageBasisPerRun,
			AgentID: "agent_native_1", Mapping: "test counters",
		},
	}
	if err := report.Seal(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), report.Digest
}

func TestCanonicalUsageMigrationFreshUpgradeAndRerun(t *testing.T) {
	db := nativeGovernanceDB(t, "canonical-usage-fresh.db")
	applyNativeGovernanceMigrations(t, db, "0027_governance_quota.sql")

	const now = "2026-09-01T00:00:00Z"
	seedNativeGovernanceFixtures(t, db)
	if _, err := db.Exec(`INSERT INTO execution_runs
		(id,workspace_id,work_item_id,agent_profile_id,status,version,created_at,updated_at)
		VALUES ('run_canonical_legacy','ws_native_1','wi_native_root_1','agent_native_1','running',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO task_sessions
		(id,workspace_id,agent_profile_id,adapter_id,task_key,session_params,display_id,
		runs_count,input_tokens_cum,created_at,updated_at)
		VALUES ('ts_canonical_legacy','ws_native_1','agent_native_1','mock','wi_native_root_1','{}',NULL,0,0,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}

	applyNativeGovernanceMigrations(t, db, "")
	assertNativeColumns(t, db, "execution_runs", "canonical_usage", "canonical_usage_digest",
		"provider_usage_report", "provider_usage_report_digest", "provider_usage_report_seq")
	assertNativeColumns(t, db, "task_sessions", "provider_usage_anchor", "provider_usage_anchor_seq")

	var canonical, canonicalDigest string
	if err := db.QueryRow(`SELECT COALESCE(canonical_usage,''), COALESCE(canonical_usage_digest,'')
		FROM execution_runs WHERE id='run_canonical_legacy'`).Scan(&canonical, &canonicalDigest); err != nil {
		t.Fatal(err)
	}
	if canonical != "" || canonicalDigest != "" {
		t.Fatalf("升级旧 Run 不得伪造 canonical usage: usage=%q digest=%q", canonical, canonicalDigest)
	}
	var anchorSeq int
	if err := db.QueryRow(`SELECT provider_usage_anchor_seq FROM task_sessions
		WHERE id='ts_canonical_legacy'`).Scan(&anchorSeq); err != nil {
		t.Fatal(err)
	}
	if anchorSeq != 0 {
		t.Fatalf("升级旧 TaskSession anchor seq 应为 0，实际 %d", anchorSeq)
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
		t.Fatalf("0028 重跑不应增加 schema_migrations: first=%d second=%d", first, second)
	}
}

func TestCanonicalUsageMigrationRunPairAndImmutability(t *testing.T) {
	db := nativeGovernanceDB(t, "canonical-usage-guards.db")
	applyNativeGovernanceMigrations(t, db, "")
	seedNativeGovernanceFixtures(t, db)

	const now = "2026-09-01T00:00:00Z"
	validJSON, validDigest := migrationCanonicalUsage(t, "run_canonical_guard")
	if _, err := db.Exec(`INSERT INTO execution_runs
		(id,workspace_id,work_item_id,agent_profile_id,status,version,created_at,updated_at,
		 canonical_usage,canonical_usage_digest)
		VALUES ('run_canonical_guard','ws_native_1','wi_native_root_1','agent_native_1','succeeded',1,?,?,?,?)`,
		now, now, validJSON, validDigest); err != nil {
		t.Fatal(err)
	}

	assertNativeExecFails(t, db, `INSERT INTO execution_runs
		(id,workspace_id,work_item_id,status,version,created_at,updated_at,canonical_usage)
		VALUES ('run_canonical_missing_digest','ws_native_1','wi_native_root_1','running',1,?,?,?)`,
		now, now, validJSON)
	assertNativeExecFails(t, db, `INSERT INTO execution_runs
		(id,workspace_id,work_item_id,status,version,created_at,updated_at,canonical_usage,canonical_usage_digest)
		VALUES ('run_canonical_bad_schema','ws_native_1','wi_native_root_1','running',1,?,?,?,?)`,
		now, now, strings.Replace(validJSON, "canonical-usage/v1", "canonical-usage/v2", 1), validDigest)
	assertNativeExecFails(t, db, `INSERT INTO execution_runs
		(id,workspace_id,work_item_id,status,version,created_at,updated_at,canonical_usage,canonical_usage_digest)
		VALUES ('run_canonical_missing_json','ws_native_1','wi_native_root_1','running',1,?,?,NULL,?)`,
		now, now, validDigest)
	assertNativeExecFails(t, db, `INSERT INTO execution_runs
		(id,workspace_id,work_item_id,status,version,created_at,updated_at,canonical_usage,canonical_usage_digest)
		VALUES ('run_canonical_bad_json','ws_native_1','wi_native_root_1','running',1,?,?, '{',?)`,
		now, now, validDigest)
	assertNativeExecFails(t, db, `INSERT INTO execution_runs
		(id,workspace_id,work_item_id,status,version,created_at,updated_at,canonical_usage,canonical_usage_digest)
		VALUES ('run_canonical_bad_digest','ws_native_1','wi_native_root_1','running',1,?,?,?, 'sha256:short')`,
		now, now, validJSON)

	if _, err := db.Exec(`UPDATE execution_runs SET canonical_usage=?, canonical_usage_digest=?
		WHERE id='run_canonical_guard'`, validJSON, validDigest); err != nil {
		t.Fatalf("exact canonical usage replay 应允许: %v", err)
	}
	assertNativeExecFails(t, db, `UPDATE execution_runs SET canonical_usage=?
		WHERE id='run_canonical_guard'`, `{"schema_version":"canonical-usage/v1","run_id":"changed"}`)
	assertNativeExecFails(t, db, `UPDATE execution_runs SET canonical_usage_digest=?
		WHERE id='run_canonical_guard'`, nativeDigest("b"))

	if _, err := db.Exec(`INSERT INTO task_sessions
		(id,workspace_id,agent_profile_id,adapter_id,task_key,session_params,runs_count,input_tokens_cum,created_at,updated_at)
		VALUES ('ts_canonical_guard','ws_native_1','agent_native_1','mock','wi_native_root_1','{}',0,0,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE task_sessions SET provider_usage_anchor=?, provider_usage_anchor_seq=1
		WHERE id='ts_canonical_guard'`, `{"schema_version":"provider-usage-anchor/v1","state":"ready","adapter_id":"mock","session_ref":"mock://session","context_generation":1,"segment_seq":1,"counters":{"input_tokens_total":1},"source_run_id":"run_canonical_guard","observed_at":"2026-09-01T00:00:00Z"}`); err != nil {
		t.Fatalf("first provider anchor should install at seq=1: %v", err)
	}
	if _, err := db.Exec(`UPDATE task_sessions SET provider_usage_anchor=?, provider_usage_anchor_seq=1
		WHERE id='ts_canonical_guard'`, `{"schema_version":"provider-usage-anchor/v1","state":"ready","adapter_id":"mock","session_ref":"mock://session","context_generation":1,"segment_seq":1,"counters":{"input_tokens_total":1},"source_run_id":"run_canonical_guard","observed_at":"2026-09-01T00:00:00Z"}`); err != nil {
		t.Fatalf("exact provider anchor replay should be allowed: %v", err)
	}
	assertNativeExecFails(t, db, `UPDATE task_sessions SET provider_usage_anchor=?, provider_usage_anchor_seq=1
		WHERE id='ts_canonical_guard'`, `{"schema_version":"provider-usage-anchor/v1","state":"ready","adapter_id":"mock","session_ref":"mock://session","context_generation":1,"segment_seq":1,"counters":{"input_tokens_total":2},"source_run_id":"run_canonical_guard","observed_at":"2026-09-01T00:00:00Z"}`)
	if _, err := db.Exec(`UPDATE task_sessions SET provider_usage_anchor=?, provider_usage_anchor_seq=2
		WHERE id='ts_canonical_guard'`, `{"schema_version":"provider-usage-anchor/v1","state":"ready","adapter_id":"mock","session_ref":"mock://session","context_generation":1,"segment_seq":1,"counters":{"input_tokens_total":2},"source_run_id":"run_canonical_guard","observed_at":"2026-09-01T00:00:00Z"}`); err != nil {
		t.Fatalf("next provider anchor should advance by one: %v", err)
	}
	assertNativeExecFails(t, db, `UPDATE task_sessions SET provider_usage_anchor=?, provider_usage_anchor_seq=4
		WHERE id='ts_canonical_guard'`, `{"schema_version":"provider-usage-anchor/v1","state":"ready","adapter_id":"mock","session_ref":"mock://session","context_generation":1,"segment_seq":1,"counters":{"input_tokens_total":4},"source_run_id":"run_canonical_guard","observed_at":"2026-09-01T00:00:00Z"}`)
	if _, err := db.Exec(`UPDATE task_sessions SET provider_usage_anchor=?, provider_usage_anchor_seq=3
		WHERE id='ts_canonical_guard'`, `{"schema_version":"provider-usage-anchor/v1","state":"invalidated","adapter_id":"mock","context_generation":2,"segment_seq":2,"counters":{},"source_run_id":"run_canonical_guard","observed_at":"2026-09-01T00:00:00Z","invalidation_reason":"provider session rotated"}`); err != nil {
		t.Fatalf("rotation tombstone should advance anchor sequence: %v", err)
	}
	assertNativeExecFails(t, db, `UPDATE task_sessions SET provider_usage_anchor=NULL, provider_usage_anchor_seq=0
		WHERE id='ts_canonical_guard'`)
}

func TestCanonicalUsageMigrationProviderReportLatestAndCanonicalBoundary(t *testing.T) {
	db := nativeGovernanceDB(t, "canonical-usage-provider-report.db")
	applyNativeGovernanceMigrations(t, db, "")
	seedNativeGovernanceFixtures(t, db)
	const now = "2026-09-01T00:00:00Z"
	if _, err := db.Exec(`INSERT INTO execution_runs
		(id,workspace_id,work_item_id,agent_profile_id,status,version,created_at,updated_at)
		VALUES ('run_provider_guard','ws_native_1','wi_native_root_1','agent_native_1','succeeded',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	reportOne, digestOne := migrationProviderUsageReport(t, "run_provider_guard", 1)
	if _, err := db.Exec(`UPDATE execution_runs SET provider_usage_report=?,
		provider_usage_report_digest=?,provider_usage_report_seq=1 WHERE id='run_provider_guard'`, reportOne, digestOne); err != nil {
		t.Fatalf("first provider report should persist: %v", err)
	}
	if _, err := db.Exec(`UPDATE execution_runs SET provider_usage_report=?,
		provider_usage_report_digest=?,provider_usage_report_seq=1 WHERE id='run_provider_guard'`, reportOne, digestOne); err != nil {
		t.Fatalf("exact provider report replay should persist: %v", err)
	}
	reportTwo, digestTwo := migrationProviderUsageReport(t, "run_provider_guard", 2)
	if _, err := db.Exec(`UPDATE execution_runs SET provider_usage_report=?,
		provider_usage_report_digest=?,provider_usage_report_seq=2 WHERE id='run_provider_guard'`, reportTwo, digestTwo); err != nil {
		t.Fatalf("growing provider report should advance sequence: %v", err)
	}
	assertNativeExecFails(t, db, `UPDATE execution_runs SET provider_usage_report=?,
		provider_usage_report_digest=?,provider_usage_report_seq=4 WHERE id='run_provider_guard'`, reportOne, digestOne)
	assertNativeExecFails(t, db, `UPDATE execution_runs SET provider_usage_report=?,
		provider_usage_report_digest=?,provider_usage_report_seq=3 WHERE id='run_provider_guard'`, "{", digestOne)
	assertNativeExecFails(t, db, `UPDATE execution_runs SET provider_usage_report=?,
		provider_usage_report_digest=?,provider_usage_report_seq=3 WHERE id='run_provider_guard'`, reportOne, nativeDigest("z"))
	assertNativeExecFails(t, db, `INSERT INTO execution_runs
		(id,workspace_id,work_item_id,status,version,created_at,updated_at,provider_usage_report,provider_usage_report_digest,provider_usage_report_seq)
		VALUES ('run_provider_bad_schema','ws_native_1','wi_native_root_1','running',1,?,?,?, ?,1)`,
		now, now, strings.Replace(reportOne, "provider-usage/v1", "provider-usage/v2", 1), digestOne)

	canonicalJSON, canonicalDigest := migrationCanonicalUsage(t, "run_provider_guard")
	if _, err := db.Exec(`UPDATE execution_runs SET canonical_usage=?, canonical_usage_digest=?
		WHERE id='run_provider_guard'`, canonicalJSON, canonicalDigest); err != nil {
		t.Fatalf("terminal canonical usage should finalize after latest report: %v", err)
	}
	reportThree, digestThree := migrationProviderUsageReport(t, "run_provider_guard", 3)
	assertNativeExecFails(t, db, `UPDATE execution_runs SET provider_usage_report=?,
		provider_usage_report_digest=?,provider_usage_report_seq=3 WHERE id='run_provider_guard'`, reportThree, digestThree)
}

func TestCanonicalUsageMigrationSpendDigestLineage(t *testing.T) {
	db := nativeGovernanceDB(t, "canonical-usage-spend-lineage.db")
	applyNativeGovernanceMigrations(t, db, "")
	seedNativeGovernanceFixtures(t, db)
	insertNativeGoal(t, db, "goal_usage_guard", "ws_native_1", "wi_native_root_1")
	insertNativeTodo(t, db, "todo_usage_guard", "goal_usage_guard")
	const now = "2026-09-01T00:00:00Z"
	if _, err := db.Exec(`UPDATE goal_todos SET claim_owner_agent_id='agent_native_1',
		claim_version=1,claim_claimed_at=?,claim_expires_at=? WHERE id='todo_usage_guard'`, now, "2026-09-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE goal_todos SET status='claimed' WHERE id='todo_usage_guard'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE goal_todos SET status='running',last_turn_seq=1 WHERE id='todo_usage_guard'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO turn_receipt_headers
		(goal_id,todo_id,turn_seq,attempt,schema_version,input_snapshot_digest,admission_client_key,canonical_digest,created_at)
		VALUES ('goal_usage_guard','todo_usage_guard',1,1,'canonical-test/v1',?,'usage-admit',?,?)`,
		nativeDigest("i"), nativeDigest("h"), now); err != nil {
		t.Fatal(err)
	}
	workerJSON, workerDigest := migrationCanonicalUsage(t, "run_usage_guard_worker")
	workerInput := `{"governance":{"goal_id":"goal_usage_guard","todo_id":"todo_usage_guard","turn_seq":1}}`
	if _, err := db.Exec(`INSERT INTO execution_runs
		(id,workspace_id,work_item_id,agent_profile_id,status,input,version,created_at,updated_at,
		 canonical_usage,canonical_usage_digest)
		VALUES ('run_usage_guard_worker','ws_native_1','wi_native_root_1','agent_native_1','succeeded',?,1,?,?,?,?)`,
		workerInput, now, now, workerJSON, workerDigest); err != nil {
		t.Fatal(err)
	}
	policyDigest := nativeDigest("p")
	if _, err := db.Exec(`INSERT INTO quota_reservations
		(goal_id,todo_id,turn_seq,quota_kind,status,reserved_amount,committed_amount,released_amount,
		 policy_limit,policy_enforcement,policy_digest,version,created_at,updated_at)
		VALUES ('goal_usage_guard','todo_usage_guard',1,'output_tokens','reserved',10,0,0,100,'audit',?,1,?,?)`,
		policyDigest, now, now); err != nil {
		t.Fatal(err)
	}
	spendInsert := `INSERT INTO quota_spend_entries
		(goal_id,todo_id,turn_seq,quota_kind,run_id,amount,usage_basis,usage_digest,policy_digest,status,reason,created_at)
		VALUES ('goal_usage_guard','todo_usage_guard',1,'output_tokens',?,1,'per_run',?,?, 'committed','',?)`
	assertNativeExecFails(t, db, spendInsert, "run_usage_guard_worker", nativeDigest("b"), policyDigest, now)
	if _, err := db.Exec(spendInsert, "run_usage_guard_worker", workerDigest, policyDigest, now); err != nil {
		t.Fatalf("matching terminal in-scope worker canonical digest should insert: %v", err)
	}

	runningJSON, runningDigest := migrationCanonicalUsage(t, "run_usage_guard_running")
	if _, err := db.Exec(`INSERT INTO execution_runs
		(id,workspace_id,work_item_id,agent_profile_id,status,input,version,created_at,updated_at)
		VALUES ('run_usage_guard_running','ws_native_1','wi_native_root_1','agent_native_1','running',?,1,?,?)`,
		workerInput, now, now); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE execution_runs SET canonical_usage=?, canonical_usage_digest=?
		WHERE id='run_usage_guard_running'`, runningJSON, runningDigest)
	assertNativeExecFails(t, db, spendInsert, "run_usage_guard_running", runningDigest, policyDigest, now)

	if _, err := db.Exec(`INSERT INTO work_items
		(id,workspace_id,title,record_kind,status,priority,version,created_at,updated_at)
		VALUES ('wi_usage_guard_other','ws_native_2','other','task','todo','medium',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	foreignJSON, foreignDigest := migrationCanonicalUsage(t, "run_usage_guard_foreign")
	if _, err := db.Exec(`INSERT INTO execution_runs
		(id,workspace_id,work_item_id,agent_profile_id,status,input,version,created_at,updated_at,
		 canonical_usage,canonical_usage_digest)
		VALUES ('run_usage_guard_foreign','ws_native_2','wi_usage_guard_other','agent_native_2','failed',?,1,?,?,?,?)`,
		workerInput, now, now, foreignJSON, foreignDigest); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, spendInsert, "run_usage_guard_foreign", foreignDigest, policyDigest, now)

	insertNativeGoal(t, db, "goal_usage_coord", "ws_native_2", "wi_native_root_2")
	insertNativeTodo(t, db, "todo_usage_coord", "goal_usage_coord")
	if _, err := db.Exec(`UPDATE goal_todos SET claim_owner_agent_id='agent_native_2',
		claim_version=1,claim_claimed_at=?,claim_expires_at=? WHERE id='todo_usage_coord'`, now, "2026-09-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE goal_todos SET status='claimed' WHERE id='todo_usage_coord'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE goal_todos SET status='running',last_turn_seq=1 WHERE id='todo_usage_coord'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO turn_receipt_headers
		(goal_id,todo_id,turn_seq,attempt,schema_version,input_snapshot_digest,admission_client_key,canonical_digest,created_at)
		VALUES ('goal_usage_coord','todo_usage_coord',1,1,'canonical-test/v1',?,'coord-admit',?,?)`,
		nativeDigest("i"), nativeDigest("j"), now); err != nil {
		t.Fatal(err)
	}
	coordJSON, coordDigest := migrationCanonicalUsage(t, "run_usage_guard_coord")
	if _, err := db.Exec(`INSERT INTO execution_runs
		(id,workspace_id,work_item_id,agent_profile_id,status,version,created_at,updated_at,
		 canonical_usage,canonical_usage_digest)
		VALUES ('run_usage_guard_coord','ws_native_2','wi_native_root_2','agent_native_2','failed',1,?,?,?,?)`,
		now, now, coordJSON, coordDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO turn_receipt_phases
		(goal_id,todo_id,turn_seq,phase_seq,phase,payload,canonical_digest,created_at)
		VALUES ('goal_usage_coord','todo_usage_coord',1,1,'decision_decode',?,?,?)`,
		`{"source_run_id":"run_usage_guard_coord"}`, nativeDigest("d"), now); err != nil {
		t.Fatal(err)
	}
	coordPolicyDigest := nativeDigest("q")
	if _, err := db.Exec(`INSERT INTO quota_reservations
		(goal_id,todo_id,turn_seq,quota_kind,status,reserved_amount,committed_amount,released_amount,
		 policy_limit,policy_enforcement,policy_digest,version,created_at,updated_at)
		VALUES ('goal_usage_coord','todo_usage_coord',1,'output_tokens','reserved',10,0,0,100,'audit',?,1,?,?)`,
		coordPolicyDigest, now, now); err != nil {
		t.Fatal(err)
	}
	coordSpend := `INSERT INTO quota_spend_entries
		(goal_id,todo_id,turn_seq,quota_kind,run_id,amount,usage_basis,usage_digest,policy_digest,status,reason,created_at)
		VALUES ('goal_usage_coord','todo_usage_coord',1,'output_tokens','run_usage_guard_coord',1,'per_run',?,?, 'committed','',?)`
	if _, err := db.Exec(coordSpend, coordDigest, coordPolicyDigest, now); err != nil {
		t.Fatalf("Coordinator source Run matched by phase1 should insert: %v", err)
	}
}

func TestCanonicalUsageMigrationRejectsNonTerminalRun(t *testing.T) {
	db := nativeGovernanceDB(t, "canonical-usage-terminal-boundary.db")
	applyNativeGovernanceMigrations(t, db, "")
	seedNativeGovernanceFixtures(t, db)

	const now = "2026-09-01T00:00:00Z"
	insertWithCanonical := `INSERT INTO execution_runs
		(id,workspace_id,work_item_id,agent_profile_id,status,version,created_at,updated_at,
		 canonical_usage,canonical_usage_digest)
		VALUES (?,?,?,?,?,1,?,?,?,?)`
	for _, status := range []string{
		"queued", "starting", "running", "waiting_approval", "interrupting",
		"cancelling", "reconnecting", "succeeding",
	} {
		runID := "run_canonical_" + status
		canonicalJSON, canonicalDigest := migrationCanonicalUsage(t, runID)
		assertNativeExecFails(t, db, insertWithCanonical,
			runID, "ws_native_1", "wi_native_root_1", "agent_native_1", status,
			now, now, canonicalJSON, canonicalDigest)
	}

	for _, status := range []string{"succeeded", "interrupted", "cancelled", "lost", "failed"} {
		runID := "run_canonical_" + status
		canonicalJSON, canonicalDigest := migrationCanonicalUsage(t, runID)
		if _, err := db.Exec(insertWithCanonical,
			runID, "ws_native_1", "wi_native_root_1", "agent_native_1", status,
			now, now, canonicalJSON, canonicalDigest); err != nil {
			t.Fatalf("终态 Run %s 应允许 canonical usage: %v", status, err)
		}
	}
	assertNativeExecFails(t, db, `UPDATE execution_runs SET status='running'
		WHERE id='run_canonical_succeeded'`)

	runningJSON, runningDigest := migrationCanonicalUsage(t, "run_canonical_running_update")
	if _, err := db.Exec(`INSERT INTO execution_runs
		(id,workspace_id,work_item_id,agent_profile_id,status,version,created_at,updated_at)
		VALUES ('run_canonical_running_update','ws_native_1','wi_native_root_1','agent_native_1','running',1,?,?)`,
		now, now); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE execution_runs SET canonical_usage=?, canonical_usage_digest=?
		WHERE id='run_canonical_running_update'`, runningJSON, runningDigest)

	transitionJSON, transitionDigest := migrationCanonicalUsage(t, "run_canonical_transition")
	if _, err := db.Exec(`INSERT INTO execution_runs
		(id,workspace_id,work_item_id,agent_profile_id,status,version,created_at,updated_at)
		VALUES ('run_canonical_transition','ws_native_1','wi_native_root_1','agent_native_1','running',1,?,?)`,
		now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE execution_runs SET status='succeeded', canonical_usage=?, canonical_usage_digest=?
		WHERE id='run_canonical_transition'`, transitionJSON, transitionDigest); err != nil {
		t.Fatalf("同一事务从 active 收敛终态并写 canonical usage 应允许: %v", err)
	}
}

func TestCanonicalUsageTerminalMigrationRejectsExistingViolation(t *testing.T) {
	db := nativeGovernanceDB(t, "canonical-usage-terminal-upgrade-violation.db")
	applyNativeGovernanceMigrations(t, db, "0034_artifact_acceptance_monotonic.sql")
	seedNativeGovernanceFixtures(t, db)

	const now = "2026-09-01T00:00:00Z"
	canonicalJSON, canonicalDigest := migrationCanonicalUsage(t, "run_canonical_preexisting_violation")
	if _, err := db.Exec(`INSERT INTO execution_runs
		(id,workspace_id,work_item_id,agent_profile_id,status,version,created_at,updated_at,
		 canonical_usage,canonical_usage_digest)
		VALUES ('run_canonical_preexisting_violation','ws_native_1','wi_native_root_1','agent_native_1',
		 'running',1,?,?,?,?)`, now, now, canonicalJSON, canonicalDigest); err != nil {
		t.Fatal(err)
	}

	err := applyMigrations(db, nativeGovernanceMigrationFiles(t, "0035_governance_canonical_usage_terminal.sql"))
	if err == nil || !strings.Contains(err.Error(), "canonical usage requires terminal status") {
		t.Fatalf("0035 must fail closed when an old direct writer left non-terminal canonical usage: %v", err)
	}
	var installed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations
		WHERE version='0035_governance_canonical_usage_terminal'`).Scan(&installed); err != nil {
		t.Fatal(err)
	}
	if installed != 0 {
		t.Fatal("failed terminal invariant migration must not be recorded as installed")
	}
}
