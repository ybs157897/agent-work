package main

import "testing"

func TestArtifactAcceptanceMigrationIsMonotonicAcrossFreshUpgradeAndRerun(t *testing.T) {
	fresh := nativeGovernanceDB(t, "artifact-acceptance-fresh.db")
	applyNativeGovernanceMigrations(t, fresh, "")
	assertSQLiteObject(t, fresh, "trigger", "artifacts_acceptance_monotonic")

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
		t.Fatalf("0034 rerun must not add migration rows: first=%d second=%d", first, second)
	}

	upgrade := nativeGovernanceDB(t, "artifact-acceptance-upgrade.db")
	applyNativeGovernanceMigrations(t, upgrade, "0033_governance_quota_gap_resolution.sql")
	assertSQLiteObjectAbsent(t, upgrade, "trigger", "artifacts_acceptance_monotonic")
	applyNativeGovernanceMigrations(t, upgrade, "")
	assertSQLiteObject(t, upgrade, "trigger", "artifacts_acceptance_monotonic")
}

func TestArtifactAcceptanceMigrationRejectsAcceptedToDraftDirectSQL(t *testing.T) {
	db := nativeGovernanceDB(t, "artifact-acceptance-guards.db")
	applyNativeGovernanceMigrations(t, db, "")
	seedNativeGovernanceFixtures(t, db)
	const now = "2026-09-01T00:00:00Z"
	if _, err := db.Exec(`INSERT INTO execution_runs
		(id,workspace_id,work_item_id,agent_profile_id,status,input,version,created_at,updated_at)
		VALUES ('run_artifact_guard','ws_native_1','wi_native_root_1','agent_native_1','succeeded','{}',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifacts
		(id,run_id,logical_path,mime,size,sha256,classification,status,created_at)
		VALUES ('artifact_guard','run_artifact_guard','result.txt','text/plain',0,'sha256:test','internal','accepted',?)`, now); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE artifacts SET status='draft' WHERE id='artifact_guard'`)
	if _, err := db.Exec(`UPDATE artifacts SET status='accepted' WHERE id='artifact_guard'`); err != nil {
		t.Fatalf("accepted no-op update must remain legal: %v", err)
	}
}
