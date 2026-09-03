package main

import "testing"

func TestDeliveryBriefSnapshotMigrationFreshUpgradeAndRerun(t *testing.T) {
	fresh := nativeGovernanceDB(t, "delivery-brief-snapshot-fresh.db")
	applyNativeGovernanceMigrations(t, fresh, "")
	assertNativeGovernanceTable(t, fresh, "governance_delivery_brief_snapshots")
	assertNativeColumns(t, fresh, "governance_delivery_brief_snapshots",
		"id", "schema_version", "goal_id", "todo_id", "work_item_id", "snapshot_json",
		"canonical_digest", "as_of_event_seq", "source_versions", "freshness_state",
		"created_at", "client_key")
	for _, index := range []string{
		"idx_governance_delivery_brief_snapshots_goal",
		"idx_governance_delivery_brief_snapshots_client_key",
	} {
		assertSQLiteObject(t, fresh, "index", index)
	}
	for _, trigger := range []string{
		"governance_delivery_brief_snapshot_scope_insert",
		"governance_delivery_brief_snapshot_identity_immutable",
		"governance_delivery_brief_snapshot_append_only_delete",
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
		t.Fatalf("0032 rerun must not add migration rows: before=%d after=%d", before, after)
	}

	upgrade := nativeGovernanceDB(t, "delivery-brief-snapshot-upgrade.db")
	applyNativeGovernanceMigrations(t, upgrade, "0031_stream_event_aggregate_version.sql")
	assertSQLiteObjectAbsent(t, upgrade, "table", "governance_delivery_brief_snapshots")
	applyNativeGovernanceMigrations(t, upgrade, "")
	assertNativeGovernanceTable(t, upgrade, "governance_delivery_brief_snapshots")
}

func TestDeliveryBriefSnapshotMigrationGuardsScopeAndAppendOnly(t *testing.T) {
	db := nativeGovernanceDB(t, "delivery-brief-snapshot-guards.db")
	applyNativeGovernanceMigrations(t, db, "")
	seedNativeGovernanceFixtures(t, db)
	insertNativeGoal(t, db, "goal_brief_snapshot_guard", "ws_native_1", "wi_native_root_1")
	insertNativeTodo(t, db, "todo_brief_snapshot_guard", "goal_brief_snapshot_guard")

	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const now = "2026-09-01T00:00:00Z"
	if _, err := db.Exec(`INSERT INTO governance_delivery_brief_snapshots
		(id,goal_id,todo_id,work_item_id,snapshot_json,canonical_digest,as_of_event_seq,
		source_versions,freshness_state,created_at,client_key)
		VALUES ('brief_migration_guard','goal_brief_snapshot_guard','todo_brief_snapshot_guard',
		'wi_native_root_1','{"freshness":{"state":"current"}}',?,0,'{}','current',?,?)`,
		digest, now, "brief-guard"); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE governance_delivery_brief_snapshots SET snapshot_json='{}' WHERE id='brief_migration_guard'`)
	assertNativeExecFails(t, db, `UPDATE governance_delivery_brief_snapshots SET canonical_digest=? WHERE id='brief_migration_guard'`, "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	assertNativeExecFails(t, db, `UPDATE governance_delivery_brief_snapshots SET schema_version='delivery-brief-snapshot/v2' WHERE id='brief_migration_guard'`)
	assertNativeExecFails(t, db, `DELETE FROM governance_delivery_brief_snapshots WHERE id='brief_migration_guard'`)
	assertNativeExecFails(t, db, `INSERT INTO governance_delivery_brief_snapshots
		(id,goal_id,todo_id,work_item_id,snapshot_json,canonical_digest,as_of_event_seq,
		source_versions,freshness_state,created_at,client_key)
		VALUES ('brief_migration_cross_root','goal_brief_snapshot_guard','todo_brief_snapshot_guard',
		'wi_native_root_2','{"freshness":{"state":"current"}}',?,0,'{}','current',?,?)`,
		digest, now, "brief-cross-root")
	assertNativeExecFails(t, db, `INSERT INTO governance_delivery_brief_snapshots
		(id,goal_id,todo_id,work_item_id,snapshot_json,canonical_digest,as_of_event_seq,
		source_versions,freshness_state,created_at,client_key)
		VALUES ('brief_migration_duplicate','goal_brief_snapshot_guard','todo_brief_snapshot_guard',
		'wi_native_root_1','{"freshness":{"state":"current"}}',?,0,'{}','current',?,?)`,
		digest, now, "brief-guard")
}
