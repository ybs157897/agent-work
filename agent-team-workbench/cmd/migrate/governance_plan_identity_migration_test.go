package main

import (
	"database/sql"
	"testing"
)

func TestGovernancePlanIdentityMigrationFreshAndRerun(t *testing.T) {
	db := nativeGovernanceDB(t, "governance-plan-identity-fresh.db")
	files := nativeGovernanceMigrationFiles(t, "")
	if err := applyMigrations(db, files); err != nil {
		t.Fatal(err)
	}

	assertNativeColumns(t, db, "plans", "client_key", "goal_id", "todo_id", "turn_seq",
		"decision_schema_version", "decision_schema_digest", "decision_digest")
	for _, index := range []string{
		"idx_plans_ws_client_key",
		"idx_plans_governance_turn_identity",
	} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master
			WHERE type='index' AND name=?`, index).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("应创建索引 %s，实际 %d", index, count)
		}
	}

	var first int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(db, files); err != nil {
		t.Fatalf("迁移重跑应幂等: %v", err)
	}
	var second int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first != second || second != len(files) {
		t.Fatalf("迁移重跑不应增加版本记录: first=%d second=%d files=%d", first, second, len(files))
	}
}

func TestGovernancePlanIdentityMigrationUpgradePreservesLegacyPlan(t *testing.T) {
	db := nativeGovernanceDB(t, "governance-plan-identity-upgrade.db")
	applyNativeGovernanceMigrations(t, db, "0025_coordinator_plan_repair.sql")

	const now = "2026-09-01T00:00:00Z"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workspaces(id,name,timezone,version,created_at,updated_at)
			VALUES ('ws_plan_legacy','legacy','UTC',1,?,?)`, []any{now, now}},
		{`INSERT INTO work_items(id,workspace_id,title,record_kind,status,priority,version,created_at,updated_at)
			VALUES ('wi_plan_legacy','ws_plan_legacy','legacy','task','todo','medium',1,?,?)`, []any{now, now}},
		{`INSERT INTO plans(id,workspace_id,work_item_id,agent_profile_id,status,guardrails,error,version,created_at,updated_at)
			VALUES ('plan_legacy','ws_plan_legacy','wi_plan_legacy','agent_legacy','active','{}',NULL,1,?,?)`, []any{now, now}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	applyNativeGovernanceMigrations(t, db, "")
	var clientKey, goalID, todoID, schemaVersion, schemaDigest, decisionDigest sql.NullString
	var turnSeq sql.NullInt64
	if err := db.QueryRow(`SELECT client_key,goal_id,todo_id,turn_seq,
		decision_schema_version,decision_schema_digest,decision_digest
		FROM plans WHERE id='plan_legacy'`).Scan(&clientKey, &goalID, &todoID, &turnSeq,
		&schemaVersion, &schemaDigest, &decisionDigest); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]sql.NullString{
		"client_key":              clientKey,
		"goal_id":                 goalID,
		"todo_id":                 todoID,
		"decision_schema_version": schemaVersion,
		"decision_schema_digest":  schemaDigest,
		"decision_digest":         decisionDigest,
	} {
		if value.Valid {
			t.Errorf("升级后的既有 Plan.%s 必须保持 NULL，实际 %q", name, value.String)
		}
	}
	if turnSeq.Valid {
		t.Fatalf("升级后的既有 Plan.turn_seq 必须保持 NULL，实际 %d", turnSeq.Int64)
	}
}

func TestGovernancePlanIdentityMigrationTriggersAndUniqueness(t *testing.T) {
	db := nativeGovernanceDB(t, "governance-plan-identity-triggers.db")
	applyNativeGovernanceMigrations(t, db, "")
	seedNativeGovernanceFixtures(t, db)
	insertNativeGoal(t, db, "goal_plan_identity", "ws_native_1", "wi_native_root_1")
	insertNativeTodo(t, db, "todo_plan_identity", "goal_plan_identity")
	if _, err := db.Exec(`INSERT INTO work_items
		(id,workspace_id,title,record_kind,status,priority,version,created_at,updated_at)
		VALUES ('wi_plan_identity_other','ws_native_1','other root','task','todo','medium',1,?,?)`,
		"2026-09-01T00:00:00Z", "2026-09-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	insertNativeGoal(t, db, "goal_plan_identity_other", "ws_native_1", "wi_plan_identity_other")
	insertNativeTodo(t, db, "todo_plan_identity_other", "goal_plan_identity_other")

	insertGovernancePlanForMigrationTest(t, db, "plan_identity_1", "governance:goal_plan_identity:todo_plan_identity:1", "goal_plan_identity", "todo_plan_identity", 1,
		"a", "b")

	// A legacy Plan with all governance columns NULL remains valid.
	const now = "2026-09-01T00:00:00Z"
	if _, err := db.Exec(`INSERT INTO plans
		(id,workspace_id,work_item_id,agent_profile_id,status,guardrails,version,created_at,updated_at)
		VALUES ('plan_identity_legacy','ws_native_1','wi_native_root_1','agent_native_1','active','{}',1,?,?)`, now, now); err != nil {
		t.Fatalf("legacy Plan 仍应可写入: %v", err)
	}

	assertNativeExecFails(t, db, `INSERT INTO plans
		(id,workspace_id,work_item_id,agent_profile_id,status,guardrails,version,created_at,updated_at,client_key)
		VALUES ('plan_identity_partial','ws_native_1','wi_native_root_1','agent_native_1','active','{}',1,?,?,'client:partial')`, now, now)
	assertNativeExecFails(t, db, `INSERT INTO plans
		(id,workspace_id,work_item_id,agent_profile_id,status,guardrails,version,created_at,updated_at,
		 client_key,goal_id,todo_id,turn_seq,decision_schema_version,decision_schema_digest,decision_digest)
		VALUES ('plan_identity_trim_client','ws_native_1','wi_native_root_1','agent_native_1','active','{}',1,?,?,
		 ' governance:goal_plan_identity:todo_plan_identity:2','goal_plan_identity','todo_plan_identity',2,'plan-decision/v2',?,?)`, now, now, nativeDigest("j"), nativeDigest("k"))
	assertNativeExecFails(t, db, `INSERT INTO plans
		(id,workspace_id,work_item_id,agent_profile_id,status,guardrails,version,created_at,updated_at,
		 client_key,goal_id,todo_id,turn_seq,decision_schema_version,decision_schema_digest,decision_digest)
		VALUES ('plan_identity_trim_schema','ws_native_1','wi_native_root_1','agent_native_1','active','{}',1,?,?,
		 'governance:goal_plan_identity:todo_plan_identity:2','goal_plan_identity','todo_plan_identity',2,' plan-decision/v2',?,?)`, now, now, nativeDigest("l"), nativeDigest("m"))
	assertNativeExecFails(t, db, `UPDATE plans SET decision_digest=NULL WHERE id='plan_identity_1'`)
	assertNativeExecFails(t, db, `UPDATE plans SET turn_seq=0 WHERE id='plan_identity_1'`)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE plans SET client_key='client:changed' WHERE id='plan_identity_1'`, nil},
		{`UPDATE plans SET goal_id='goal_changed' WHERE id='plan_identity_1'`, nil},
		{`UPDATE plans SET todo_id='todo_changed' WHERE id='plan_identity_1'`, nil},
		{`UPDATE plans SET turn_seq=9 WHERE id='plan_identity_1'`, nil},
		{`UPDATE plans SET decision_schema_version='plan-decision/v3' WHERE id='plan_identity_1'`, nil},
		{`UPDATE plans SET decision_schema_digest=? WHERE id='plan_identity_1'`, []any{nativeDigest("n")}},
		{`UPDATE plans SET decision_digest=? WHERE id='plan_identity_1'`, []any{nativeDigest("o")}},
	} {
		assertNativeExecFails(t, db, statement.query, statement.args...)
	}
	if _, err := db.Exec(`UPDATE plans SET status='waiting' WHERE id='plan_identity_1'`); err != nil {
		t.Fatalf("Plan 状态更新不应被治理身份 immutable guard 阻断: %v", err)
	}

	// Both identity dimensions are independently unique inside the workspace.
	assertNativeExecFails(t, db, `INSERT INTO plans
		(id,workspace_id,work_item_id,agent_profile_id,status,guardrails,version,created_at,updated_at,
		 client_key,goal_id,todo_id,turn_seq,decision_schema_version,decision_schema_digest,decision_digest)
		VALUES ('plan_identity_duplicate_client','ws_native_1','wi_native_root_1','agent_native_1','active','{}',1,?,?,
		 'governance:goal_plan_identity:todo_plan_identity:1','goal_plan_identity','todo_plan_identity',2,'plan-decision/v2',?,?)`, now, now, nativeDigest("c"), nativeDigest("d"))
	assertNativeExecFails(t, db, `INSERT INTO plans
		(id,workspace_id,work_item_id,agent_profile_id,status,guardrails,version,created_at,updated_at,
		 client_key,goal_id,todo_id,turn_seq,decision_schema_version,decision_schema_digest,decision_digest)
		VALUES ('plan_identity_duplicate_turn','ws_native_1','wi_native_root_1','agent_native_1','active','{}',1,?,?,
		 'governance:goal_plan_identity:todo_plan_identity:1','goal_plan_identity','todo_plan_identity',1,'plan-decision/v2',?,?)`, now, now, nativeDigest("e"), nativeDigest("f"))

	assertNativeExecFails(t, db, `INSERT INTO plans
		(id,workspace_id,work_item_id,agent_profile_id,status,guardrails,version,created_at,updated_at,
		 client_key,goal_id,todo_id,turn_seq,decision_schema_version,decision_schema_digest,decision_digest)
		VALUES ('plan_identity_bad_digest','ws_native_1','wi_native_root_1','agent_native_1','active','{}',1,?,?,
		 'governance:goal_plan_identity:todo_plan_identity:2','goal_plan_identity','todo_plan_identity',2,'plan-decision/v2','sha256:short',?)`, now, now, nativeDigest("g"))
	assertNativeExecFails(t, db, `INSERT INTO plans
		(id,workspace_id,work_item_id,agent_profile_id,status,guardrails,version,created_at,updated_at,
		 client_key,goal_id,todo_id,turn_seq,decision_schema_version,decision_schema_digest,decision_digest)
		VALUES ('plan_identity_wrong_client_key','ws_native_1','wi_native_root_1','agent_native_1','active','{}',1,?,?,
		 'governance:wrong','goal_plan_identity','todo_plan_identity',2,'plan-decision/v2',?,?)`,
		now, now, nativeDigest("r"), nativeDigest("s"))
	assertNativeExecFails(t, db, `INSERT INTO plans
		(id,workspace_id,work_item_id,agent_profile_id,status,guardrails,version,created_at,updated_at,
		 client_key,goal_id,todo_id,turn_seq,decision_schema_version,decision_schema_digest,decision_digest)
		VALUES ('plan_identity_bad_text','ws_native_1','wi_native_root_1','agent_native_1','active','{}',1,?,?,
		 'governance:goal_plan_identity:todo_plan_identity:2','goal_plan_identity','todo_plan_identity',2,'',?,?)`, now, now, nativeDigest("h"), nativeDigest("i"))
	assertNativeExecFails(t, db, `INSERT INTO plans
		(id,workspace_id,work_item_id,agent_profile_id,status,guardrails,version,created_at,updated_at,
		 client_key,goal_id,todo_id,turn_seq,decision_schema_version,decision_schema_digest,decision_digest)
		VALUES ('plan_identity_wrong_root','ws_native_1','wi_native_root_1','agent_native_1','active','{}',1,?,?,
		 'governance:goal_plan_identity_other:todo_plan_identity_other:2','goal_plan_identity_other','todo_plan_identity_other',2,'plan-decision/v2',?,?)`,
		now, now, nativeDigest("p"), nativeDigest("q"))
}

func insertGovernancePlanForMigrationTest(t *testing.T, db *sql.DB, id, clientKey, goalID, todoID string, turnSeq int64, schemaSeed, decisionSeed string) {
	t.Helper()
	const now = "2026-09-01T00:00:00Z"
	_, err := db.Exec(`INSERT INTO plans
		(id,workspace_id,work_item_id,agent_profile_id,status,guardrails,version,created_at,updated_at,
		 client_key,goal_id,todo_id,turn_seq,decision_schema_version,decision_schema_digest,decision_digest)
		VALUES (?,?,?,?, 'active','{}',1,?,?, ?,?,?,?,'plan-decision/v2',?,?)`,
		id, "ws_native_1", "wi_native_root_1", "agent_native_1", now, now, clientKey, goalID, todoID,
		turnSeq, nativeDigest(schemaSeed), nativeDigest(decisionSeed))
	if err != nil {
		t.Fatal(err)
	}
}
