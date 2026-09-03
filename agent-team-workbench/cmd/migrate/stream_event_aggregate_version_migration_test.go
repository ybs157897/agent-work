package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func TestStreamEventAggregateVersionMigrationFreshUpgradeAndRerun(t *testing.T) {
	ctx := context.Background()
	legacy := nativeGovernanceDB(t, "stream-event-aggregate-version-upgrade.db")
	applyNativeGovernanceMigrations(t, legacy, "0030_governed_turn_recovery_checkpoint.sql")
	const now = "2026-09-02T00:00:00Z"
	if _, err := legacy.Exec(`INSERT INTO workspaces
		(id,name,timezone,version,created_at,updated_at)
		VALUES ('ws_event_upgrade','event upgrade','UTC',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	// A pre-0031 row has no aggregate_version column.  After the upgrade its
	// explicit compatibility value must be 0 and remain readable by Since.
	if _, err := legacy.Exec(`INSERT INTO stream_events
		(workspace_id,stream_seq,event_id,event_type,aggregate_type,aggregate_id,payload,occurred_at)
		VALUES ('ws_event_upgrade',1,'evt_legacy_1','work_item.created','work_item','wi_legacy',?,?)`,
		`{"data":{"legacy":true}}`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO outbox_messages
		(event_id,topic,payload,created_at) VALUES ('evt_legacy_1','ws_event_upgrade',?,?)`,
		`{"data":{"legacy":true}}`, now); err != nil {
		t.Fatal(err)
	}
	applyNativeGovernanceMigrations(t, legacy, "")
	var legacyVersion int
	if err := legacy.QueryRow(`SELECT aggregate_version FROM stream_events WHERE event_id='evt_legacy_1'`).Scan(&legacyVersion); err != nil {
		t.Fatal(err)
	}
	if legacyVersion != 0 {
		t.Fatalf("legacy stream event aggregate_version=%d, want explicit 0", legacyVersion)
	}
	readback, err := sqlstore.New(legacy).Events().Since(ctx, "ws_event_upgrade", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(readback) != 1 || readback[0].Aggregate.Version != 0 {
		t.Fatalf("legacy aggregate version must read as 0: %+v", readback)
	}
	var legacyOutboxPayload string
	if err := legacy.QueryRow(`SELECT payload FROM outbox_messages WHERE event_id='evt_legacy_1'`).Scan(&legacyOutboxPayload); err != nil {
		t.Fatal(err)
	}
	var legacyOutbox map[string]any
	if err := json.Unmarshal([]byte(legacyOutboxPayload), &legacyOutbox); err != nil {
		t.Fatal(err)
	}
	if got, ok := legacyOutbox["aggregate_version"].(float64); !ok || int(got) != 0 {
		t.Fatalf("legacy outbox aggregate_version=%v, want explicit 0", legacyOutbox["aggregate_version"])
	}

	fresh := nativeGovernanceDB(t, "stream-event-aggregate-version-fresh.db")
	applyNativeGovernanceMigrations(t, fresh, "")
	var notNull int
	var defaultValue sql.NullString
	if err := fresh.QueryRow(`SELECT "notnull", dflt_value FROM pragma_table_info('stream_events') WHERE name='aggregate_version'`).Scan(&notNull, &defaultValue); err != nil {
		t.Fatal(err)
	}
	if notNull != 1 || !defaultValue.Valid || defaultValue.String != "0" {
		t.Fatalf("aggregate_version must be NOT NULL DEFAULT 0: notnull=%d default=%v", notNull, defaultValue)
	}
	if _, err := fresh.Exec(`INSERT INTO workspaces
		(id,name,timezone,version,created_at,updated_at)
		VALUES ('ws_event_fresh','event fresh','UTC',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	store := sqlstore.New(fresh)
	event, err := domain.NewCanonicalEvent("ws_event_fresh", domain.EventWorkItemCreated,
		domain.AggregateWorkItem, "wi_fresh", 7, map[string]any{"fresh": true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InTx(ctx, func(txctx context.Context) error {
		_, err := store.Events().Append(txctx, event, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var storedVersion int
	if err := fresh.QueryRow(`SELECT aggregate_version FROM stream_events WHERE event_id=?`, event.EventID).Scan(&storedVersion); err != nil {
		t.Fatal(err)
	}
	if storedVersion != 7 {
		t.Fatalf("stored aggregate_version=%d, want 7", storedVersion)
	}
	freshReadback, err := store.Events().Since(ctx, "ws_event_fresh", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(freshReadback) != 1 || freshReadback[0].Aggregate.Version != 7 {
		t.Fatalf("fresh aggregate version must survive Since: %+v", freshReadback)
	}
	var outboxPayload string
	if err := fresh.QueryRow(`SELECT payload FROM outbox_messages WHERE event_id=?`, event.EventID).Scan(&outboxPayload); err != nil {
		t.Fatal(err)
	}
	var outbox map[string]any
	if err := json.Unmarshal([]byte(outboxPayload), &outbox); err != nil {
		t.Fatal(err)
	}
	if got, ok := outbox["aggregate_version"].(float64); !ok || int(got) != 7 {
		t.Fatalf("outbox aggregate_version=%v, want 7", outbox["aggregate_version"])
	}

	var before, after int
	if err := fresh.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	applyNativeGovernanceMigrations(t, fresh, "")
	if err := fresh.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("0031 rerun must not add schema_migrations rows: before=%d after=%d", before, after)
	}
}
