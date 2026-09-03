package main

import "testing"

func TestGovernanceQuotaMigrationFreshUpgradeAndRerun(t *testing.T) {
	db := nativeGovernanceDB(t, "governance-quota-fresh.db")
	applyNativeGovernanceMigrations(t, db, "")

	for _, table := range []string{"quota_reservations", "quota_spend_entries"} {
		assertNativeGovernanceTable(t, db, table)
	}
	assertNativeColumns(t, db, "quota_reservations",
		"goal_id", "todo_id", "turn_seq", "quota_kind", "status",
		"reserved_amount", "committed_amount", "released_amount", "policy_limit",
		"policy_enforcement", "policy_digest", "version",
		"created_at", "updated_at")
	assertNativeColumns(t, db, "quota_spend_entries",
		"goal_id", "todo_id", "turn_seq", "quota_kind", "run_id", "amount",
		"usage_basis", "usage_digest", "policy_digest", "price_digest", "status",
		"reason", "created_at")

	var first int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(db, nativeGovernanceMigrationFiles(t, "")); err != nil {
		t.Fatalf("quota migration 重跑应幂等: %v", err)
	}
	var second int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("quota migration 重跑不应增加版本记录: first=%d second=%d", first, second)
	}
}

func TestGovernanceQuotaMigrationUpgradeKeepsGoalPolicyJSON(t *testing.T) {
	db := nativeGovernanceDB(t, "governance-quota-upgrade.db")
	applyNativeGovernanceMigrations(t, db, "0026_governance_plan_identity.sql")

	const now = "2026-09-01T00:00:00Z"
	if _, err := db.Exec(`INSERT INTO workspaces(id,name,timezone,version,created_at,updated_at)
		VALUES ('ws_quota_upgrade','quota','UTC',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO work_items
		(id,workspace_id,title,record_kind,status,priority,version,created_at,updated_at)
		VALUES ('wi_quota_upgrade','ws_quota_upgrade','quota root','task','todo','medium',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	policy := `[{"kind":"output_tokens","limit":42,"enforcement":"audit"}]`
	if _, err := db.Exec(`INSERT INTO goals
		(id,workspace_id,root_work_item_id,objective,acceptance_contract,status,phase,
		 quota_policies,completion_evidence_summary,version,created_at,updated_at)
		VALUES ('goal_quota_upgrade','ws_quota_upgrade','wi_quota_upgrade','goal',
		 '["done"]','draft','draft',?,'[]',1,?,?)`, policy, now, now); err != nil {
		t.Fatal(err)
	}

	applyNativeGovernanceMigrations(t, db, "")
	var got string
	if err := db.QueryRow(`SELECT quota_policies FROM goals WHERE id='goal_quota_upgrade'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != policy {
		t.Fatalf("0027 不应复制/改写 Goal.quota_policies: got=%q want=%q", got, policy)
	}
	var policyTable int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master
		WHERE type='table' AND name='goal_quota_policies'`).Scan(&policyTable); err != nil {
		t.Fatal(err)
	}
	if policyTable != 0 {
		t.Fatal("0027 不得创建第二套 goal_quota_policies 表")
	}
}
