package main

import (
	"testing"
	"time"
)

func TestIdempotencyClaimFencingMigrationInstallsOwnerGuards(t *testing.T) {
	db := nativeGovernanceDB(t, "idempotency-claim-fencing.db")
	applyNativeGovernanceMigrations(t, db, "")
	assertNativeColumns(t, db, "idempotency_keys", "claim_token", "claim_expires_at")
	for _, object := range []struct{ kind, name string }{
		{"index", "idempotency_keys_active_claim_token"},
		{"trigger", "idempotency_active_claim_token_insert_guard"},
		{"trigger", "idempotency_active_claim_token_update_guard"},
	} {
		assertSQLiteObject(t, db, object.kind, object.name)
	}
	if _, err := db.Exec(`INSERT INTO workspaces(id,name,timezone,version,created_at,updated_at)
		VALUES ('ws_idem_migration','idem','UTC',1,datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `INSERT INTO idempotency_keys(workspace_id,key,request_hash,status_code,created_at)
		VALUES ('ws_idem_migration','missing-token','hash',NULL,datetime('now'))`)
	if _, err := db.Exec(`INSERT INTO idempotency_keys(workspace_id,key,request_hash,status_code,claim_token,claim_expires_at,created_at)
		VALUES ('ws_idem_migration','with-token','hash',NULL,'claim-1',datetime('now','+15 minutes'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE idempotency_keys SET claim_token=NULL WHERE workspace_id='ws_idem_migration' AND key='with-token'`)
	assertNativeExecFails(t, db, `UPDATE idempotency_keys SET claim_expires_at=NULL WHERE workspace_id='ws_idem_migration' AND key='with-token'`)
	if _, err := db.Exec(`UPDATE idempotency_keys SET status_code=200, result_ref='{}', claim_token=NULL, claim_expires_at=NULL
		WHERE workspace_id='ws_idem_migration' AND key='with-token'`); err != nil {
		t.Fatalf("completed claim may clear owner token: %v", err)
	}
	assertNativeExecFails(t, db, `UPDATE idempotency_keys SET created_at='2000-01-01T00:00:00Z'
		WHERE workspace_id='ws_idem_migration' AND key='with-token'`)
	assertNativeExecFails(t, db, `UPDATE idempotency_keys SET claim_token='leaked-token'
		WHERE workspace_id='ws_idem_migration' AND key='with-token'`)
}

func TestIdempotencyClaimFencingBackfillsLegacyClaimsAsExpiredRFC3339(t *testing.T) {
	db := nativeGovernanceDB(t, "idempotency-claim-fencing-upgrade.db")
	applyNativeGovernanceMigrations(t, db, "0036_agent_config_sync_intent.sql")
	created := "2026-09-03T00:00:00.123456789Z"
	if _, err := db.Exec(`INSERT INTO idempotency_keys(workspace_id,key,request_hash,status_code,created_at)
		VALUES ('ws_legacy_claim','legacy','hash',NULL,?)`, created); err != nil {
		t.Fatal(err)
	}
	applyNativeGovernanceMigrations(t, db, "")
	var expiry string
	if err := db.QueryRow(`SELECT claim_expires_at FROM idempotency_keys WHERE workspace_id='ws_legacy_claim' AND key='legacy'`).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, expiry)
	if err != nil {
		t.Fatalf("legacy expiry must use RFC3339-compatible text, got %q: %v", expiry, err)
	}
	if parsed.After(time.Now().UTC()) {
		t.Fatalf("legacy claim must be deliberately expired during migration: %s", expiry)
	}
	if expiry != "1970-01-01T00:00:00Z" {
		t.Fatalf("legacy expiry should use the explicit expired sentinel, got %q", expiry)
	}
}
