package main

import "testing"

func TestGovernanceTodoCompletionIdentityMigrationFreshUpgradeAndRerun(t *testing.T) {
	fresh := nativeGovernanceDB(t, "governance-todo-completion-fresh.db")
	applyNativeGovernanceMigrations(t, fresh, "")
	assertNativeColumns(t, fresh, "goal_todos", "completion_turn_seq", "completion_evidence_id")
	for _, trigger := range []string{
		"goal_todos_completion_identity_insert",
		"goal_todos_completion_identity_update",
		"goal_todos_completion_identity_immutable",
		"goal_todos_terminal_immutable",
	} {
		assertSQLiteObject(t, fresh, "trigger", trigger)
	}

	var before int
	if err := fresh.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	applyNativeGovernanceMigrations(t, fresh, "")
	var after int
	if err := fresh.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("migration replay must not add rows: before=%d after=%d", before, after)
	}

	upgrade := nativeGovernanceDB(t, "governance-todo-completion-upgrade.db")
	applyNativeGovernanceMigrations(t, upgrade, "0038_agent_config_sync_intent_applied_immutable.sql")
	seedNativeGovernanceFixtures(t, upgrade)
	insertNativeGoal(t, upgrade, "goal_completion_upgrade", "ws_native_1", "wi_native_root_1")
	insertNativeTodo(t, upgrade, "todo_completion_upgrade", "goal_completion_upgrade")
	applyNativeGovernanceMigrations(t, upgrade, "")
	assertNativeColumns(t, upgrade, "goal_todos", "completion_turn_seq", "completion_evidence_id")
	var status string
	var turnSeq int64
	var evidence any
	if err := upgrade.QueryRow(`SELECT status,completion_turn_seq,completion_evidence_id
		FROM goal_todos WHERE id='todo_completion_upgrade'`).Scan(&status, &turnSeq, &evidence); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || turnSeq != 0 || evidence != nil {
		t.Fatalf("pending Todo upgrade changed completion identity: status=%s turn=%d evidence=%v", status, turnSeq, evidence)
	}
}

func TestGovernanceContinuationBlockedRootAndControlReceiptMigrations(t *testing.T) {
	db := nativeGovernanceDB(t, "governance-post-completion-migrations.db")
	applyNativeGovernanceMigrations(t, db, "")
	for _, trigger := range []string{
		"turn_receipt_headers_recovery_checkpoint_insert",
		"goal_todos_same_generation_claimed_at_guard",
		"goal_todos_same_generation_expiry_guard",
		"goals_status_transition_guard",
		"turn_receipt_phases_semantic_contract",
		"turn_receipt_plan_phase_governance_lineage",
	} {
		assertSQLiteObject(t, db, "trigger", trigger)
	}
	for _, version := range []string{
		"0040_governance_handoff_continuation",
		"0041_governance_blocked_root_rebuild",
		"0042_governance_control_receipt_phases",
	} {
		var applied int
		if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=?`, version).Scan(&applied); err != nil {
			t.Fatal(err)
		}
		if applied != 1 {
			t.Fatalf("migration %s must be recorded exactly once, rows=%d", version, applied)
		}
	}
}

func TestGovernancePostCompletionMigrationsUpgradeAtEveryBoundary(t *testing.T) {
	for _, through := range []string{
		"0039_governance_todo_completion_identity.sql",
		"0040_governance_handoff_continuation.sql",
		"0041_governance_blocked_root_rebuild.sql",
	} {
		t.Run(through, func(t *testing.T) {
			db := nativeGovernanceDB(t, through+".db")
			applyNativeGovernanceMigrations(t, db, through)
			applyNativeGovernanceMigrations(t, db, "")
			var applied int
			if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
				t.Fatal(err)
			}
			if want := len(nativeGovernanceMigrationFiles(t, "")); applied != want {
				t.Fatalf("boundary upgrade applied=%d, want %d", applied, want)
			}
		})
	}
}

func TestGovernanceTodoCompletionIdentityMigrationRejectsUnprovableLegacyCompletion(t *testing.T) {
	db := nativeGovernanceDB(t, "governance-todo-completion-invalid-upgrade.db")
	applyNativeGovernanceMigrations(t, db, "0038_agent_config_sync_intent_applied_immutable.sql")
	seedNativeGovernanceFixtures(t, db)
	insertNativeGoal(t, db, "goal_completion_legacy", "ws_native_1", "wi_native_root_1")
	insertNativeTodo(t, db, "todo_completion_legacy", "goal_completion_legacy")
	const claimedAt = "2026-09-01T00:00:00Z"
	if _, err := db.Exec(`UPDATE goal_todos
		SET status='claimed',claim_owner_agent_id='agent_native_1',claim_version=1,
			claim_claimed_at=?,claim_expires_at=?
		WHERE id='todo_completion_legacy'`, claimedAt, "2026-09-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE goal_todos SET status='running',last_turn_seq=1
		WHERE id='todo_completion_legacy'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE goal_todos
		SET status='completed',claim_owner_agent_id=NULL,claim_version=2,
			claim_claimed_at=NULL,claim_expires_at=NULL
		WHERE id='todo_completion_legacy'`); err != nil {
		t.Fatal(err)
	}

	files := nativeGovernanceMigrationFiles(t, "")
	if err := applyMigrations(db, files); err == nil {
		t.Fatal("0039 must fail closed instead of grandfathering a completed Todo without admitted receipt/evidence")
	}
	var applied int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=?`,
		"0039_governance_todo_completion_identity").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("failed completion migration must not be recorded, rows=%d", applied)
	}
}

func TestGovernanceTodoCompletionAndRenewalSQLiteGuards(t *testing.T) {
	db := nativeGovernanceDB(t, "governance-todo-completion-guards.db")
	applyNativeGovernanceMigrations(t, db, "")
	seedNativeGovernanceFixtures(t, db)
	insertNativeGoal(t, db, "goal_completion_guard", "ws_native_1", "wi_native_root_1")
	insertNativeTodo(t, db, "todo_completion_guard", "goal_completion_guard")
	const (
		claimedAt = "2026-09-01T00:00:00Z"
		expiresAt = "2026-09-02T00:00:00Z"
	)
	if _, err := db.Exec(`UPDATE goal_todos
		SET status='claimed',claim_owner_agent_id='agent_native_1',claim_version=1,
			claim_claimed_at=?,claim_expires_at=?
		WHERE id='todo_completion_guard'`, claimedAt, expiresAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE goal_todos SET claim_expires_at='2026-09-03T00:00:00Z'
		WHERE id='todo_completion_guard'`); err != nil {
		t.Fatalf("same-generation expiry extension must be allowed: %v", err)
	}
	assertNativeExecFails(t, db, `UPDATE goal_todos SET claim_claimed_at='2026-09-01T01:00:00Z',
		claim_expires_at='2026-09-04T00:00:00Z' WHERE id='todo_completion_guard'`)
	assertNativeExecFails(t, db, `UPDATE goal_todos SET claim_expires_at='2026-09-02T00:00:00Z'
		WHERE id='todo_completion_guard'`)

	if _, err := db.Exec(`UPDATE goal_todos SET status='running',last_turn_seq=1
		WHERE id='todo_completion_guard'`); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE goal_todos
		SET status='completed',claim_owner_agent_id=NULL,claim_version=2,
			claim_claimed_at=NULL,claim_expires_at=NULL,
			completion_turn_seq=1,completion_evidence_id='wi_native_root_1'
		WHERE id='todo_completion_guard'`)

	if _, err := db.Exec(`INSERT INTO turn_receipt_headers
		(goal_id,todo_id,turn_seq,attempt,schema_version,input_snapshot_digest,
		 admission_client_key,canonical_digest,created_at)
		VALUES ('goal_completion_guard','todo_completion_guard',1,1,'plan-decision/v2',?,?,?,?)`,
		nativeDigest("input"), "completion-admission", nativeDigest("header"), claimedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE work_items SET status='completed' WHERE id='wi_native_root_1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE goal_todos
		SET status='completed',claim_owner_agent_id=NULL,claim_version=2,
			claim_claimed_at=NULL,claim_expires_at=NULL,
			completion_turn_seq=1,completion_evidence_id='wi_native_root_1',version=version+1
		WHERE id='todo_completion_guard'`); err != nil {
		t.Fatalf("latest admitted turn and completed root evidence must permit completion: %v", err)
	}
	assertNativeExecFails(t, db, `UPDATE goal_todos SET instruction='rewrite terminal history'
		WHERE id='todo_completion_guard'`)

	insertNativeTodo(t, db, "todo_cancelled_guard", "goal_completion_guard")
	if _, err := db.Exec(`UPDATE goal_todos SET status='cancelled' WHERE id='todo_cancelled_guard'`); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE goal_todos SET priority='urgent' WHERE id='todo_cancelled_guard'`)
}
