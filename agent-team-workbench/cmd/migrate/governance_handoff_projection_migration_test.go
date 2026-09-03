package main

import (
	"database/sql"
	"testing"
)

func TestGovernanceHandoffProjectionMigrationFreshUpgradeAndRerun(t *testing.T) {
	fresh := nativeGovernanceDB(t, "governance-handoff-projection-fresh.db")
	applyNativeGovernanceMigrations(t, fresh, "")

	for _, table := range []string{
		"governance_handoffs", "governance_validation_results",
		"governance_goal_projections", "governance_projection_repairs",
	} {
		assertNativeGovernanceTable(t, fresh, table)
	}
	assertNativeColumns(t, fresh, "governance_handoffs",
		"id", "goal_id", "todo_id", "source_kind", "source_id", "target_kind", "target_id",
		"reason", "context_summary", "evidence", "open_risks", "status", "claim_transfer_state",
		"source_claim_version", "target_claim_version", "actor_kind", "actor_id", "client_key",
		"version", "created_at", "updated_at")
	assertNativeColumns(t, fresh, "governance_validation_results",
		"id", "goal_id", "todo_id", "work_item_id", "source_run_id", "criteria_digest",
		"status", "summary", "produced_by", "recorded_at", "version", "created_at")
	assertNativeColumns(t, fresh, "governance_goal_projections",
		"goal_id", "goal_progress", "todo_current_state", "receipt_timeline", "evidence_summary",
		"next_action_checkpoint", "counters", "source_event_stream_seq", "through_turn_seq",
		"digest", "version", "updated_at")
	assertNativeColumns(t, fresh, "governance_projection_repairs",
		"id", "goal_id", "status", "scope", "source_event_stream_seq", "through_turn_seq",
		"replayed_event_count", "replayed_receipt_count", "error_code", "error_message", "client_key",
		"version", "started_at", "completed_at", "created_at", "updated_at")

	for _, index := range []string{
		"idx_governance_handoffs_goal", "idx_governance_handoffs_todo",
		"idx_governance_handoffs_client_key", "idx_governance_validation_results_goal",
		"idx_governance_validation_results_source_run", "idx_governance_projection_repairs_goal",
		"idx_governance_projection_repairs_client_key",
	} {
		assertSQLiteObject(t, fresh, "index", index)
	}
	for _, trigger := range []string{
		"governance_handoffs_identity_immutable", "governance_handoffs_status_transition",
		"governance_validation_result_scope_insert", "governance_validation_result_immutable_update",
		"governance_validation_result_immutable_delete", "governance_goal_projection_identity_immutable",
		"governance_projection_repair_identity_immutable", "governance_projection_repair_status_transition",
	} {
		assertSQLiteObject(t, fresh, "trigger", trigger)
	}

	var first int
	if err := fresh.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	applyNativeGovernanceMigrations(t, fresh, "")
	var second int
	if err := fresh.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("0029/0030 rerun must not add migration rows: first=%d second=%d", first, second)
	}

	upgrade := nativeGovernanceDB(t, "governance-handoff-projection-upgrade.db")
	applyNativeGovernanceMigrations(t, upgrade, "0028_governance_canonical_usage.sql")
	for _, table := range []string{
		"governance_handoffs", "governance_validation_results",
		"governance_goal_projections", "governance_projection_repairs",
	} {
		assertSQLiteObjectAbsent(t, upgrade, "table", table)
	}
	applyNativeGovernanceMigrations(t, upgrade, "")
	for _, table := range []string{
		"governance_handoffs", "governance_validation_results",
		"governance_goal_projections", "governance_projection_repairs",
	} {
		assertNativeGovernanceTable(t, upgrade, table)
	}
}

func TestGovernanceHandoffProjectionMigrationGuardsIdentityAndLifecycle(t *testing.T) {
	db := nativeGovernanceDB(t, "governance-handoff-projection-guards.db")
	applyNativeGovernanceMigrations(t, db, "")
	seedNativeGovernanceFixtures(t, db)
	insertNativeGoal(t, db, "goal_handoff_guard", "ws_native_1", "wi_native_root_1")
	insertNativeTodo(t, db, "todo_handoff_guard", "goal_handoff_guard")

	const now = "2026-09-01T00:00:00Z"
	if _, err := db.Exec(`UPDATE goal_todos SET status='claimed', claim_owner_agent_id='agent_native_1',
		claim_version=1, claim_claimed_at=?, claim_expires_at=? WHERE id='todo_handoff_guard'`,
		now, "2026-09-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO governance_handoffs
		(id,goal_id,todo_id,source_kind,source_id,target_kind,target_id,reason,context_summary,
		evidence,open_risks,acceptance,resolution_reason,status,claim_transfer_state,source_claim_version,
		target_claim_version,actor_kind,actor_id,client_key,version,created_at,updated_at)
		VALUES ('handoff_migration_guard','goal_handoff_guard','todo_handoff_guard','agent','agent_native_1',
		'agent','agent_native_1','handoff reason','handoff context','[]','[]','','','pending',
		'retained_by_source',1,0,'agent','agent_native_1','handoff-guard',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE governance_handoffs SET reason='changed' WHERE id='handoff_migration_guard'`)
	// The status and claim-transfer state form one invariant. A final transfer
	// may be persisted atomically, but changing status alone must fail closed.
	assertNativeExecFails(t, db, `UPDATE governance_handoffs SET status='transferred' WHERE id='handoff_migration_guard'`)
	if _, err := db.Exec(`UPDATE governance_handoffs SET status='accepted', claim_transfer_state='claimed_by_target',
		acceptance='accepted', accepted_by_kind='agent', accepted_by_id='agent_native_1', accepted_at=?,
		target_claim_version=2, version=2 WHERE id='handoff_migration_guard'`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE governance_handoffs SET status='transferred', claim_transfer_state='transferred',
		version=3 WHERE id='handoff_migration_guard'`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`INSERT INTO execution_runs
		(id,workspace_id,work_item_id,agent_profile_id,status,input,version,created_at,updated_at)
		VALUES ('run_validation_migration','ws_native_1','wi_native_root_1','agent_native_1','succeeded','{}',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO governance_validation_results
		(id,goal_id,todo_id,work_item_id,source_run_id,criteria_digest,status,summary,produced_by,
		recorded_at,version,created_at)
		VALUES ('validation_migration_guard','goal_handoff_guard','todo_handoff_guard','wi_native_root_1',
		'run_validation_migration',?,'failed','validation failed','control_plane',?,1,?)`,
		nativeDigest("criteria"), now, now); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `INSERT INTO governance_validation_results
		(id,goal_id,todo_id,work_item_id,source_run_id,criteria_digest,status,summary,produced_by,recorded_at,version,created_at)
		VALUES ('validation_migration_duplicate','goal_handoff_guard','todo_handoff_guard','wi_native_root_1',
		'run_validation_migration',?,'failed','duplicate','control_plane',?,1,?)`, nativeDigest("criteria"), now, now)
	assertNativeExecFails(t, db, `UPDATE governance_validation_results SET summary='changed' WHERE id='validation_migration_guard'`)
	assertNativeExecFails(t, db, `DELETE FROM governance_validation_results WHERE id='validation_migration_guard'`)

	if _, err := db.Exec(`INSERT INTO governance_goal_projections
		(goal_id,goal_progress,todo_current_state,receipt_timeline,evidence_summary,next_action_checkpoint,counters,
		source_event_stream_seq,through_turn_seq,digest,version,updated_at)
		VALUES ('goal_handoff_guard','{}','{}','[]','[]','{}','{}',0,0,?,1,?)`, nativeDigest("projection"), now); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE governance_goal_projections SET goal_id='goal_other' WHERE goal_id='goal_handoff_guard'`)
	assertNativeExecFails(t, db, `UPDATE governance_goal_projections SET digest='bad' WHERE goal_id='goal_handoff_guard'`)

	if _, err := db.Exec(`INSERT INTO governance_projection_repairs
		(id,goal_id,status,scope,version,started_at,created_at,updated_at)
		VALUES ('projection_repair_migration','goal_handoff_guard','pending','["goal_progress"]',1,?,?,?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE governance_projection_repairs SET goal_id='goal_other' WHERE id='projection_repair_migration'`)
	assertNativeExecFails(t, db, `UPDATE governance_projection_repairs SET status='completed' WHERE id='projection_repair_migration'`)
}

func assertSQLiteObject(t *testing.T, db *sql.DB, kind, name string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type=? AND name=?`, kind, name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("应创建 SQLite %s %s", kind, name)
	}
}

func assertSQLiteObjectAbsent(t *testing.T, db *sql.DB, kind, name string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type=? AND name=?`, kind, name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("不应创建 SQLite %s %s", kind, name)
	}
}
