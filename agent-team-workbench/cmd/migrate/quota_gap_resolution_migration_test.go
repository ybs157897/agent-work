package main

import "testing"

func TestQuotaGapResolutionMigrationFreshUpgradeAndRerun(t *testing.T) {
	fresh := nativeGovernanceDB(t, "quota-gap-resolution-fresh.db")
	applyNativeGovernanceMigrations(t, fresh, "")
	assertNativeGovernanceTable(t, fresh, "governance_quota_gap_resolutions")
	assertNativeColumns(t, fresh, "governance_quota_gap_resolutions",
		"id", "schema_version", "goal_id", "todo_id", "turn_seq", "quota_kind", "run_id",
		"original_usage_digest", "original_policy_digest", "original_price_digest", "status", "amount",
		"evidence", "evidence_digest", "canonical_digest", "actor_kind", "actor_id", "reason", "client_key", "created_at")
	for _, index := range []string{
		"idx_governance_quota_gap_resolutions_target",
		"idx_governance_quota_gap_resolutions_client_key",
		"idx_governance_quota_gap_resolutions_goal",
	} {
		assertSQLiteObject(t, fresh, "index", index)
	}
	for _, trigger := range []string{
		"governance_quota_gap_resolution_target_guard",
		"governance_quota_gap_resolution_identity_immutable",
		"governance_quota_gap_resolution_append_only_delete",
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
		t.Fatalf("0033 rerun must not add migration rows: before=%d after=%d", before, after)
	}

	upgrade := nativeGovernanceDB(t, "quota-gap-resolution-upgrade.db")
	applyNativeGovernanceMigrations(t, upgrade, "0032_governance_delivery_brief_snapshot.sql")
	assertSQLiteObjectAbsent(t, upgrade, "table", "governance_quota_gap_resolutions")
	applyNativeGovernanceMigrations(t, upgrade, "")
	assertNativeGovernanceTable(t, upgrade, "governance_quota_gap_resolutions")
}
