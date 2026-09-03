package main

import "testing"

func TestAgentConfigSyncIntentMigrationInstallsDurableGuards(t *testing.T) {
	db := nativeGovernanceDB(t, "agent-config-sync-intent.db")
	applyNativeGovernanceMigrations(t, db, "")
	assertNativeGovernanceTable(t, db, "agent_config_sync_intents")
	assertNativeColumns(t, db, "agent_config_sync_intents",
		"id", "agent_profile_id", "workspace_id", "target_version", "target_snapshot",
		"target_digest", "status", "last_error", "attempts", "version", "created_at", "updated_at", "applied_at")
	for _, object := range []string{
		"agent_config_sync_intents_one_active",
		"agent_config_sync_intents_active_order",
		"agent_config_sync_intents_workspace",
	} {
		assertSQLiteObject(t, db, "index", object)
	}
	for _, trigger := range []string{
		"agent_config_sync_intent_workspace_guard",
		"agent_config_sync_intent_version_guard",
		"agent_config_sync_intent_target_immutable",
		"agent_config_sync_intent_applied_monotonic",
		"agent_config_sync_intent_applied_at_guard",
		"agent_config_sync_intent_status_timestamp_guard",
		"agent_config_sync_intent_applied_row_immutable",
		"agent_config_sync_intent_applied_delete_guard",
	} {
		assertSQLiteObject(t, db, "trigger", trigger)
	}

	seedNativeGovernanceFixtures(t, db)
	const now = "2026-09-01T00:00:00Z"
	const target = `{"agent_id":"agent_native_1","workspace_id":"ws_native_1","kind":"user","slug":"one","name":"one","role":"worker","skills":[],"instructions":"","runtime_preference":{},"model_override":{},"policy":{},"agent_version":1}`
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.Exec(`INSERT INTO agent_config_sync_intents
		(id,agent_profile_id,workspace_id,target_version,target_snapshot,target_digest,status,last_error,attempts,version,created_at,updated_at)
		VALUES ('agentsync_guard_1','agent_native_1','ws_native_1',1,?,?,'pending','',0,1,?,?)`, target, digest, now, now); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `INSERT INTO agent_config_sync_intents
		(id,agent_profile_id,workspace_id,target_version,target_snapshot,target_digest,status,last_error,attempts,version,created_at,updated_at)
		VALUES ('agentsync_guard_2','agent_native_1','ws_native_1',1,?,?,'pending','',0,1,?,?)`, target, digest, now, now)
	assertNativeExecFails(t, db, `INSERT INTO agent_config_sync_intents
		(id,agent_profile_id,workspace_id,target_version,target_snapshot,target_digest,status,last_error,attempts,version,created_at,updated_at)
		VALUES ('agentsync_guard_cross_ws','agent_native_1','ws_native_2',1,?,?,'pending','',0,1,?,?)`, target, digest, now, now)
	assertNativeExecFails(t, db, `INSERT INTO agent_config_sync_intents
		(id,agent_profile_id,workspace_id,target_version,target_snapshot,target_digest,status,last_error,attempts,version,created_at,updated_at)
		VALUES ('agentsync_guard_stale','agent_native_1','ws_native_1',2,?,?,'pending','',0,1,?,?)`, target, digest, now, now)
	assertNativeExecFails(t, db, `UPDATE agent_config_sync_intents SET target_snapshot='{}' WHERE id='agentsync_guard_1'`)
	if _, err := db.Exec(`UPDATE agent_config_sync_intents SET status='applied', applied_at=?, version=version+1 WHERE id='agentsync_guard_1'`, now); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE agent_config_sync_intents SET status='failed' WHERE id='agentsync_guard_1'`)
	assertNativeExecFails(t, db, `UPDATE agent_config_sync_intents SET last_error='tamper' WHERE id='agentsync_guard_1'`)
	assertNativeExecFails(t, db, `DELETE FROM agent_config_sync_intents WHERE id='agentsync_guard_1'`)
}
