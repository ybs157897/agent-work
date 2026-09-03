package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func nativeGovernanceDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), name)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureSchemaTable(db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func nativeGovernanceMigrationFiles(t *testing.T, through string) []string {
	t.Helper()
	files, err := discoverMigrations(repoMigrationsDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if through == "" {
		return files
	}
	var selected []string
	for _, file := range files {
		selected = append(selected, file)
		if filepath.Base(file) == through {
			return selected
		}
	}
	t.Fatalf("未找到迁移 %s", through)
	return nil
}

func applyNativeGovernanceMigrations(t *testing.T, db *sql.DB, through string) {
	t.Helper()
	if err := applyMigrations(db, nativeGovernanceMigrationFiles(t, through)); err != nil {
		t.Fatal(err)
	}
}

func assertNativeGovernanceTable(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("应创建表 %s", table)
	}
}

func nativeTableColumns(t *testing.T, db *sql.DB, table string) map[string]struct {
	notNull int
	pk      int
} {
	t.Helper()
	rows, err := db.Query(`SELECT name, "notnull", pk FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := make(map[string]struct {
		notNull int
		pk      int
	})
	for rows.Next() {
		var name string
		var column struct {
			notNull int
			pk      int
		}
		if err := rows.Scan(&name, &column.notNull, &column.pk); err != nil {
			t.Fatal(err)
		}
		columns[name] = column
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func assertNativeColumns(t *testing.T, db *sql.DB, table string, required ...string) {
	t.Helper()
	columns := nativeTableColumns(t, db, table)
	for _, name := range required {
		if _, ok := columns[name]; !ok {
			t.Errorf("%s 缺少字段 %s", table, name)
		}
	}
}

func assertNativeExecFails(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err == nil {
		t.Fatalf("SQL 应失败: %s", query)
	}
}

func assertNativeSchemaHasNoForbiddenColumn(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`SELECT m.name, p.name
		FROM sqlite_master m
		JOIN pragma_table_info(m.name) p
		WHERE m.type='table' AND (p.name IN ('state','turn_id','receipt_id') OR p.name LIKE '%turn_id%' OR p.name LIKE '%receipt_id%')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		t.Errorf("治理迁移不得创建 %s.%s", table, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeGovernanceMigrationFreshSchema(t *testing.T) {
	db := nativeGovernanceDB(t, "native-governance-fresh.db")
	applyNativeGovernanceMigrations(t, db, "")

	for _, table := range []string{"goals", "goal_todos", "turn_receipt_headers", "turn_receipt_phases"} {
		assertNativeGovernanceTable(t, db, table)
	}
	for _, forbidden := range []string{"governance_events", "quota_spends"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, forbidden).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("不得创建旁路表 %s", forbidden)
		}
	}
	assertNativeSchemaHasNoForbiddenColumn(t, db)
	assertNativeColumns(t, db, "goals", "id", "workspace_id", "root_work_item_id", "objective", "acceptance_contract", "status", "phase", "current_todo_id", "quota_policies", "completion_evidence_summary", "version", "created_at", "updated_at")
	assertNativeColumns(t, db, "goal_todos", "id", "goal_id", "class", "status", "instruction", "acceptance", "resume_condition", "priority", "predecessors", "successors", "decision_scope", "claim_owner_agent_id", "claim_version", "claim_claimed_at", "claim_expires_at", "last_turn_seq", "version", "created_at", "updated_at")
	assertNativeColumns(t, db, "turn_receipt_headers", "goal_id", "todo_id", "turn_seq", "attempt", "schema_version", "input_snapshot_digest", "admission_client_key", "canonical_digest", "created_at")
	assertNativeColumns(t, db, "turn_receipt_phases", "goal_id", "todo_id", "turn_seq", "phase_seq", "phase", "payload", "canonical_digest", "plan_id", "run_ids", "quota_reservation_keys", "evidence", "created_at")

	for _, table := range []string{"goals", "goal_todos", "turn_receipt_headers", "turn_receipt_phases"} {
		columns := nativeTableColumns(t, db, table)
		for _, key := range []string{"id", "goal_id", "todo_id", "turn_seq", "phase_seq"} {
			if column, ok := columns[key]; ok && column.pk > 0 && column.notNull == 0 {
				t.Errorf("%s.%s 作为主键时必须显式 NOT NULL", table, key)
			}
		}
	}
}

func TestNativeGovernanceMigrationUpgrades0023(t *testing.T) {
	db := nativeGovernanceDB(t, "native-governance-upgrade.db")
	applyNativeGovernanceMigrations(t, db, "0023_runner_event_dedup_v2.sql")

	const now = "2026-09-01T00:00:00Z"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workspaces(id,name,timezone,version,created_at,updated_at) VALUES ('ws_upgrade','upgrade','UTC',1,?,?)`, []any{now, now}},
		{`INSERT INTO work_items(id,workspace_id,title,record_kind,created_at,updated_at) VALUES ('wi_upgrade','ws_upgrade','upgrade root','task',?,?)`, []any{now, now}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	applyNativeGovernanceMigrations(t, db, "")
	var title string
	if err := db.QueryRow(`SELECT title FROM work_items WHERE id='wi_upgrade'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "upgrade root" {
		t.Fatalf("0024 不得改写 0023 之前的数据，title=%q", title)
	}
	assertNativeGovernanceTable(t, db, "turn_receipt_phases")
}

func TestNativeGovernanceMigrationRerunUsesSchemaMigrationsIdempotency(t *testing.T) {
	db := nativeGovernanceDB(t, "native-governance-rerun.db")
	files := nativeGovernanceMigrationFiles(t, "")
	if err := applyMigrations(db, files); err != nil {
		t.Fatal(err)
	}
	var first int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(db, files); err != nil {
		t.Fatalf("由 schema_migrations 跳过已应用迁移时不应报错: %v", err)
	}
	var second int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first != second || second != len(files) {
		t.Fatalf("迁移重跑不得新增版本记录: first=%d second=%d files=%d", first, second, len(files))
	}
}

func seedNativeGovernanceFixtures(t *testing.T, db *sql.DB) {
	t.Helper()
	const now = "2026-09-01T00:00:00Z"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workspaces(id,name,timezone,version,created_at,updated_at) VALUES ('ws_native_1','one','UTC',1,?,?)`, []any{now, now}},
		{`INSERT INTO workspaces(id,name,timezone,version,created_at,updated_at) VALUES ('ws_native_2','two','UTC',1,?,?)`, []any{now, now}},
		{`INSERT INTO work_items(id,workspace_id,title,record_kind,created_at,updated_at) VALUES ('wi_native_root_1','ws_native_1','root one','task',?,?)`, []any{now, now}},
		{`INSERT INTO work_items(id,workspace_id,title,record_kind,created_at,updated_at) VALUES ('wi_native_root_2','ws_native_2','root two','task',?,?)`, []any{now, now}},
		{`INSERT INTO work_items(id,workspace_id,parent_id,title,record_kind,created_at,updated_at) VALUES ('wi_native_child_1','ws_native_1','wi_native_root_1','child','task',?,?)`, []any{now, now}},
		{`INSERT INTO work_items(id,workspace_id,title,record_kind,created_at,updated_at) VALUES ('wi_native_chat_1','ws_native_1','chat','chat',?,?)`, []any{now, now}},
		{`INSERT INTO agent_profiles(id,workspace_id,name,role,created_at,updated_at) VALUES ('agent_native_1','ws_native_1','one','worker',?,?)`, []any{now, now}},
		{`INSERT INTO agent_profiles(id,workspace_id,name,role,created_at,updated_at) VALUES ('agent_native_2','ws_native_2','two','worker',?,?)`, []any{now, now}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func insertNativeGoal(t *testing.T, db *sql.DB, id, workspaceID, rootID string) {
	t.Helper()
	const now = "2026-09-01T00:00:00Z"
	_, err := db.Exec(`INSERT INTO goals
		(id,workspace_id,root_work_item_id,objective,acceptance_contract,status,phase,current_todo_id,quota_policies,completion_evidence_summary,version,created_at,updated_at)
		VALUES (?,?,?,?,?,'draft','planning',NULL,'[]','[]',1,?,?)`,
		id, workspaceID, rootID, "objective "+id, `["accept"]`, now, now)
	if err != nil {
		t.Fatal(err)
	}
}

func insertNativeTodo(t *testing.T, db *sql.DB, id, goalID string) {
	t.Helper()
	const now = "2026-09-01T00:00:00Z"
	_, err := db.Exec(`INSERT INTO goal_todos
		(id,goal_id,class,status,instruction,acceptance,resume_condition,priority,predecessors,successors,decision_scope,claim_owner_agent_id,claim_version,claim_claimed_at,claim_expires_at,last_turn_seq,version,created_at,updated_at)
		VALUES (?,?,'advancement','pending','do work','["done"]',NULL,'medium','[]','[]',?,NULL,0,NULL,NULL,0,1,?,?)`,
		id, goalID, `{"work_item_ids":["wi_native_root_1"],"agent_ids":["agent_native_1"],"runtime_capabilities":[],"write_scopes":[],"max_dispatch":0}`, now, now)
	if err != nil {
		t.Fatal(err)
	}
}

func nativeDigest(seed string) string {
	nibble := func(value byte) string { return fmt.Sprintf("%x", value&0x0f) }
	return "sha256:" + strings.Repeat(nibble(seed[0]), 63) + nibble(seed[len(seed)-1])
}

func TestNativeGovernanceMigrationTriggersAndInvariants(t *testing.T) {
	db := nativeGovernanceDB(t, "native-governance-triggers.db")
	applyNativeGovernanceMigrations(t, db, "")
	seedNativeGovernanceFixtures(t, db)

	insertNativeGoal(t, db, "goal_native_1", "ws_native_1", "wi_native_root_1")
	if _, err := db.Exec(`INSERT INTO work_items
		(id,workspace_id,title,record_kind,status,priority,version,created_at,updated_at)
		VALUES ('wi_native_accept','ws_native_1','accept root','task','todo','medium',1,'2026-09-01','2026-09-01')`); err != nil {
		t.Fatal(err)
	}
	insertNativeGoal(t, db, "goal_native_accept", "ws_native_1", "wi_native_accept")
	if _, err := db.Exec(`UPDATE goals SET status='active' WHERE id='goal_native_accept'`); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE goals SET status='completed' WHERE id='goal_native_accept'`)
	if _, err := db.Exec(`UPDATE work_items SET status='completed' WHERE id='wi_native_accept'`); err != nil {
		t.Fatal(err)
	}
	// Root status alone is insufficient; Goal completion also carries an exact
	// accepted evidence reference to the same root Task.
	assertNativeExecFails(t, db, `UPDATE goals SET status='completed' WHERE id='goal_native_accept'`)
	if _, err := db.Exec(`UPDATE goals
		SET completion_evidence_summary='[{"source_kind":"work_item","source_id":"wi_native_accept","verification":"accepted","summary":"accepted","recorded_at":"2026-09-01T00:00:00Z"}]',
			status='completed'
		WHERE id='goal_native_accept'`); err != nil {
		t.Fatalf("accepted root Task proof should permit Goal completion: %v", err)
	}
	assertNativeExecFails(t, db, `UPDATE goals SET status='active' WHERE id='goal_native_accept'`)
	assertNativeExecFails(t, db, `INSERT INTO goals
		(id,workspace_id,root_work_item_id,objective,acceptance_contract,status,phase,current_todo_id,quota_policies,completion_evidence_summary,version,created_at,updated_at)
		VALUES ('goal_bad_ws','ws_native_1','wi_native_root_2','bad','["a"]','draft','planning',NULL,'[]','[]',1,'2026-09-01','2026-09-01')`)
	assertNativeExecFails(t, db, `INSERT INTO goals
		(id,workspace_id,root_work_item_id,objective,acceptance_contract,status,phase,current_todo_id,quota_policies,completion_evidence_summary,version,created_at,updated_at)
		VALUES ('goal_bad_child','ws_native_1','wi_native_child_1','bad','["a"]','draft','planning',NULL,'[]','[]',1,'2026-09-01','2026-09-01')`)
	assertNativeExecFails(t, db, `INSERT INTO goals
		(id,workspace_id,root_work_item_id,objective,acceptance_contract,status,phase,current_todo_id,quota_policies,completion_evidence_summary,version,created_at,updated_at)
		VALUES ('goal_bad_chat','ws_native_1','wi_native_chat_1','bad','["a"]','draft','planning',NULL,'[]','[]',1,'2026-09-01','2026-09-01')`)
	assertNativeExecFails(t, db, `INSERT INTO goals
		(id,workspace_id,root_work_item_id,objective,acceptance_contract,status,phase,current_todo_id,quota_policies,completion_evidence_summary,version,created_at,updated_at)
		VALUES ('goal_duplicate_root','ws_native_1','wi_native_root_1','bad','["a"]','draft','planning',NULL,'[]','[]',1,'2026-09-01','2026-09-01')`)

	insertNativeTodo(t, db, "todo_native_1", "goal_native_1")
	insertNativeTodo(t, db, "todo_native_terminal", "goal_native_1")
	if _, err := db.Exec(`UPDATE goal_todos SET status='cancelled' WHERE id='todo_native_terminal'`); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE goal_todos SET status='pending' WHERE id='todo_native_terminal'`)
	if _, err := db.Exec(`UPDATE goals SET current_todo_id='todo_native_1' WHERE id='goal_native_1'`); err != nil {
		t.Fatal(err)
	}
	insertNativeGoal(t, db, "goal_native_2", "ws_native_2", "wi_native_root_2")
	insertNativeTodo(t, db, "todo_native_2", "goal_native_2")
	assertNativeExecFails(t, db, `UPDATE goal_todos SET goal_id='goal_native_1' WHERE id='todo_native_2'`)
	assertNativeExecFails(t, db, `UPDATE goal_todos SET id='todo_native_renamed' WHERE id='todo_native_2'`)
	assertNativeExecFails(t, db, `UPDATE goals SET current_todo_id='todo_native_2' WHERE id='goal_native_1'`)
	assertNativeExecFails(t, db, `UPDATE goals SET root_work_item_id='wi_native_root_2' WHERE id='goal_native_1'`)
	assertNativeExecFails(t, db, `UPDATE goal_todos SET status='claimed' WHERE id='todo_native_1'`)
	assertNativeExecFails(t, db, `UPDATE goal_todos SET status='running' WHERE id='todo_native_1'`)

	if _, err := db.Exec(`UPDATE goal_todos
		SET claim_owner_agent_id='agent_native_1', claim_version=1,
			claim_claimed_at='2026-09-01', claim_expires_at='2026-09-02'
		WHERE id='todo_native_1'`); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE goal_todos SET status='cancelled' WHERE id='todo_native_1'`)
	assertNativeExecFails(t, db, `UPDATE goal_todos
		SET claim_owner_agent_id='agent_native_2', claim_version=2,
			claim_claimed_at='2026-09-01', claim_expires_at='2026-09-02'
		WHERE id='todo_native_1'`)
	assertNativeExecFails(t, db, `UPDATE agent_profiles SET workspace_id='ws_native_2' WHERE id='agent_native_1'`)
	if _, err := db.Exec(`UPDATE goal_todos
		SET claim_owner_agent_id=NULL, claim_version=2,
			claim_claimed_at=NULL, claim_expires_at=NULL
		WHERE id='todo_native_1'`); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE goal_todos SET claim_owner_agent_id='agent_native_1', claim_version=1 WHERE id='todo_native_1'`)
	if _, err := db.Exec(`UPDATE goal_todos
		SET claim_owner_agent_id='agent_native_1', claim_version=3,
			claim_claimed_at='2026-09-01', claim_expires_at='2026-09-02'
		WHERE id='todo_native_1'`); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE goal_todos
		SET claim_version=4, claim_expires_at='2026-09-01'
		WHERE id='todo_native_1'`)
	if _, err := db.Exec(`UPDATE goal_todos SET status='claimed' WHERE id='todo_native_1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE goal_todos SET status='running', last_turn_seq=1 WHERE id='todo_native_1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE goal_todos SET status='waiting' WHERE id='todo_native_1'`); err != nil {
		t.Fatal(err)
	}

	assertNativeExecFails(t, db, `UPDATE goal_todos SET last_turn_seq=3 WHERE id='todo_native_1'`)
	assertNativeExecFails(t, db, `UPDATE goal_todos SET last_turn_seq=0 WHERE id='todo_native_1'`)
	insertNativeTodo(t, db, "todo_native_stale_header", "goal_native_1")
	for seq := 1; seq <= 3; seq++ {
		if _, err := db.Exec(`UPDATE goal_todos SET last_turn_seq=? WHERE id='todo_native_stale_header'`, seq); err != nil {
			t.Fatal(err)
		}
	}

	const now = "2026-09-01T00:00:00Z"
	digest1 := nativeDigest("a")
	digest2 := nativeDigest("b")
	assertNativeExecFails(t, db, `INSERT INTO turn_receipt_headers
		(goal_id,todo_id,turn_seq,attempt,schema_version,input_snapshot_digest,admission_client_key,canonical_digest,created_at)
		VALUES ('goal_native_1','todo_native_stale_header',1,1,'turn-receipt/v1',?,'client-stale',?,?)`, nativeDigest("i"), digest1, now)
	assertNativeExecFails(t, db, `INSERT INTO turn_receipt_headers
		(goal_id,todo_id,turn_seq,attempt,schema_version,input_snapshot_digest,admission_client_key,canonical_digest,created_at)
		VALUES ('goal_native_1','todo_native_1',2,1,'turn-receipt/v1',?,'client-future',?,?)`, nativeDigest("i"), digest1, now)
	insertHeader := func(seq int, client, digest string) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO turn_receipt_headers
			(goal_id,todo_id,turn_seq,attempt,schema_version,input_snapshot_digest,admission_client_key,canonical_digest,created_at)
			VALUES ('goal_native_1','todo_native_1',?,1,'turn-receipt/v1',?,?,?,?)`,
			seq, nativeDigest("i"), client, digest, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	insertHeader(1, "client-1", digest1)
	insertHeader(1, "client-1", digest1)
	var headers int
	if err := db.QueryRow(`SELECT count(*) FROM turn_receipt_headers WHERE goal_id='goal_native_1' AND todo_id='todo_native_1'`).Scan(&headers); err != nil {
		t.Fatal(err)
	}
	if headers != 1 {
		t.Fatalf("同 identity 同 digest 重放必须幂等，实际 Header=%d", headers)
	}
	assertNativeExecFails(t, db, `INSERT INTO turn_receipt_headers
		(goal_id,todo_id,turn_seq,attempt,schema_version,input_snapshot_digest,admission_client_key,canonical_digest,created_at)
		VALUES ('goal_native_1','todo_native_1',1,1,'turn-receipt/v1',?,'client-1',?,?)`, nativeDigest("i"), digest2, now)
	assertNativeExecFails(t, db, `INSERT INTO turn_receipt_headers
		(goal_id,todo_id,turn_seq,attempt,schema_version,input_snapshot_digest,admission_client_key,canonical_digest,created_at)
		VALUES ('goal_native_1','todo_native_1',3,1,'turn-receipt/v1',?,'client-3',?,?)`, nativeDigest("i"), nativeDigest("c"), now)
	assertNativeExecFails(t, db, `INSERT INTO turn_receipt_headers
		(goal_id,todo_id,turn_seq,attempt,schema_version,input_snapshot_digest,admission_client_key,canonical_digest,created_at)
		VALUES ('goal_native_1','todo_native_1',2,1,'turn-receipt/v1',?,'client-2','bad-digest',?)`, nativeDigest("i"), now)
	if _, err := db.Exec(`UPDATE goal_todos SET last_turn_seq=2 WHERE id='todo_native_1'`); err != nil {
		t.Fatal(err)
	}
	insertHeader(2, "client-2", nativeDigest("c"))
	assertNativeExecFails(t, db, `UPDATE turn_receipt_headers SET attempt=2 WHERE goal_id='goal_native_1' AND todo_id='todo_native_1' AND turn_seq=1`)
	assertNativeExecFails(t, db, `DELETE FROM turn_receipt_headers WHERE goal_id='goal_native_1' AND todo_id='todo_native_1' AND turn_seq=1`)
	if _, err := db.Exec(`INSERT INTO plans
		(id,workspace_id,work_item_id,agent_profile_id,status,guardrails,version,created_at,updated_at,
		 client_key,goal_id,todo_id,turn_seq,decision_schema_version,decision_schema_digest,decision_digest)
		VALUES ('plan_native_phase','ws_native_1','wi_native_root_1','agent_native_1','active','{}',1,?,?,
		 'governance:goal_native_1:todo_native_1:1','goal_native_1','todo_native_1',1,'plan-decision/v2',?,?)`,
		now, now, nativeDigest("phase-schema"), nativeDigest("phase-decision")); err != nil {
		t.Fatal(err)
	}

	insertPhase := func(seq int, phase, digest string) {
		t.Helper()
		payload := `{}`
		var planID any
		if seq == 4 {
			planID = "plan_native_phase"
			payload = `{"plan_id":"plan_native_phase","plan_client_key":"governance:test","decision_digest":"` + nativeDigest("phase-plan") + `"}`
		}
		if seq == 5 {
			planID = "plan_native_phase"
			payload = `{"plan_id":"plan_native_phase","dispatch_state":"no_runs","run_count":0}`
		}
		_, err := db.Exec(`INSERT INTO turn_receipt_phases
			(goal_id,todo_id,turn_seq,phase_seq,phase,payload,canonical_digest,plan_id,run_ids,quota_reservation_keys,evidence,created_at)
			VALUES ('goal_native_1','todo_native_1',1,?,?,?, ?,?,'[]','[]','[]',?)`,
			seq, phase, payload, digest, planID, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	insertPhase(1, "decision_decode", nativeDigest("p1"))
	insertPhase(1, "decision_decode", nativeDigest("p1"))
	assertNativeExecFails(t, db, `INSERT INTO turn_receipt_phases
		(goal_id,todo_id,turn_seq,phase_seq,phase,payload,canonical_digest,created_at)
		VALUES ('goal_native_1','todo_native_1',1,1,'decision_decode','{}',?,?)`, nativeDigest("p2"), now)
	assertNativeExecFails(t, db, `INSERT INTO turn_receipt_phases
		(goal_id,todo_id,turn_seq,phase_seq,phase,payload,canonical_digest,created_at)
		VALUES ('goal_native_1','todo_native_1',1,3,'durable_writeback','{}',?,?)`, nativeDigest("p3"), now)
	assertNativeExecFails(t, db, `INSERT INTO turn_receipt_phases
		(goal_id,todo_id,turn_seq,phase_seq,phase,payload,canonical_digest,created_at)
		VALUES ('goal_native_1','todo_native_1',1,2,'durable_writeback','{}','bad-digest',?)`, now)
	assertNativeExecFails(t, db, `INSERT INTO turn_receipt_phases
		(goal_id,todo_id,turn_seq,phase_seq,phase,payload,canonical_digest,plan_id,created_at)
		VALUES ('goal_native_1','todo_native_1',2,1,'decision_decode','{}',?,'plan_missing',?)`, nativeDigest("missing"), now)

	insertPhase(2, "validation", nativeDigest("phase-validation"))
	insertPhase(3, "durable_writeback", nativeDigest("phase-writeback"))
	assertNativeExecFails(t, db, `INSERT INTO turn_receipt_phases
		(goal_id,todo_id,turn_seq,phase_seq,phase,payload,canonical_digest,plan_id,run_ids,quota_reservation_keys,evidence,created_at)
		VALUES ('goal_native_1','todo_native_1',1,4,'plan_compile','{}',?,NULL,'[]','[]','[]',?)`,
		nativeDigest("phase-invalid-plan"), now)
	phaseNames := []string{"plan_compile", "dispatch", "quota_spend", "projection_outbox"}
	for i, phase := range phaseNames {
		insertPhase(i+4, phase, nativeDigest(string(rune('d'+i))))
	}
	assertNativeExecFails(t, db, `UPDATE turn_receipt_phases SET payload='{"changed":true}' WHERE goal_id='goal_native_1' AND todo_id='todo_native_1' AND turn_seq=1 AND phase_seq=1`)
	assertNativeExecFails(t, db, `DELETE FROM turn_receipt_phases WHERE goal_id='goal_native_1' AND todo_id='todo_native_1' AND turn_seq=1 AND phase_seq=1`)
}
