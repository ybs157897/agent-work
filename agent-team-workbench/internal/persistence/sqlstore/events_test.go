package sqlstore_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/migtest"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func TestEventRepoSincePreservesAggregateVersionAndOutboxPayload(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "events.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	seedWorkspace(t, db)
	store := sqlstore.New(db)
	event, err := domain.NewCanonicalEvent("ws_wk", domain.EventWorkItemCreated,
		domain.AggregateWorkItem, "wi_event_version", 9, map[string]any{"source": "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InTx(ctx, func(txctx context.Context) error {
		_, err := store.Events().Append(txctx, event, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	events, err := store.Events().Since(ctx, "ws_wk", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Aggregate.Version != 9 {
		t.Fatalf("SSE replay lost aggregate version: %+v", events)
	}
	var payload string
	if err := db.QueryRow(`SELECT payload FROM outbox_messages WHERE event_id=?`, event.EventID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	if got, ok := decoded["aggregate_version"].(float64); !ok || int(got) != 9 {
		t.Fatalf("outbox aggregate_version=%v, want 9", decoded["aggregate_version"])
	}
}

func TestIdempotencyClaimReclaimsOnlyStaleSameHash(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "idempotency.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO idempotency_keys(workspace_id,key,request_hash,status_code,claim_token,claim_expires_at,created_at) VALUES (?,?,?,?,?,?,?)`,
		"ws_stale", "key_stale", "hash-a", nil, "legacy-stale", old, old); err != nil {
		t.Fatal(err)
	}
	store := sqlstore.New(db)
	claimer, ok := store.Idempotency().(interface {
		Claim(context.Context, string, string, string) (bool, *application.IdempotencyRecord, string, error)
	})
	if !ok {
		t.Fatal("SQLite idempotency repo must implement claim-first extension")
	}
	claimed, rec, token, err := claimer.Claim(ctx, "ws_stale", "key_stale", "hash-a")
	if err != nil || !claimed || rec != nil || token == "" {
		t.Fatalf("stale same-hash claim should be reclaimed: claimed=%v rec=%+v token=%q err=%v", claimed, rec, token, err)
	}
	claimed, rec, _, err = claimer.Claim(ctx, "ws_stale", "key_stale", "hash-b")
	if err != nil || claimed || rec == nil || rec.RequestHash != "hash-a" {
		t.Fatalf("different hash must remain conflict after reclaim: claimed=%v rec=%+v err=%v", claimed, rec, err)
	}
}

func TestIdempotencyClaimOwnerFenceProtectsReplacement(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "idempotency-fence.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	store := sqlstore.New(db)
	type fenced interface {
		Claim(context.Context, string, string, string) (bool, *application.IdempotencyRecord, string, error)
		Complete(context.Context, string, string, string, string, int, string) error
		Release(context.Context, string, string, string, string) error
	}
	repo, ok := store.Idempotency().(fenced)
	if !ok {
		t.Fatal("SQLite idempotency repo must expose owner-fenced completion")
	}
	claimed, _, token, err := repo.Claim(ctx, "ws_fence", "key", "hash")
	if err != nil || !claimed || token == "" {
		t.Fatalf("claim failed: claimed=%v token=%q err=%v", claimed, token, err)
	}
	if err := repo.Complete(ctx, "ws_fence", "key", "hash", "wrong-token", 200, `{"wrong":true}`); !errors.Is(err, domain.ErrIdempotencyClaimLost) {
		t.Fatalf("wrong owner must be fenced: %v", err)
	}
	if err := repo.Complete(ctx, "ws_fence", "key", "hash", token, 200, `{"ok":true}`); err != nil {
		t.Fatalf("owner completion failed: %v", err)
	}
	if err := repo.Release(ctx, "ws_fence", "key", "hash", token); !errors.Is(err, domain.ErrIdempotencyClaimLost) {
		t.Fatalf("completed claim release must report lost owner: %v", err)
	}
}

func TestIdempotencyOwnerRenewalKeepsFreshClaim(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "idempotency-fresh.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	store := sqlstore.New(db)
	type fenced interface {
		Claim(context.Context, string, string, string) (bool, *application.IdempotencyRecord, string, error)
		Renew(context.Context, string, string, string, string) error
	}
	repo, ok := store.Idempotency().(fenced)
	if !ok {
		t.Fatal("SQLite idempotency repo must expose owner-fenced renewal")
	}
	claimed, _, token, err := repo.Claim(ctx, "ws_fresh", "key", "hash")
	if err != nil || !claimed || token == "" {
		t.Fatalf("claim failed: claimed=%v token=%q err=%v", claimed, token, err)
	}
	if err := repo.Renew(ctx, "ws_fresh", "key", "hash", token); err != nil {
		t.Fatalf("fresh owner renewal failed: %v", err)
	}
}

func TestIdempotencyOwnerRenewalPreventsStaleReclaim(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "idempotency-renew.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	store := sqlstore.New(db)
	type fenced interface {
		Claim(context.Context, string, string, string) (bool, *application.IdempotencyRecord, string, error)
		Renew(context.Context, string, string, string, string) error
	}
	repo, ok := store.Idempotency().(fenced)
	if !ok {
		t.Fatal("SQLite idempotency repo must expose owner-fenced renewal")
	}
	claimed, _, token, err := repo.Claim(ctx, "ws_renew", "key", "hash")
	if err != nil || !claimed || token == "" {
		t.Fatalf("claim failed: claimed=%v token=%q err=%v", claimed, token, err)
	}
	var createdBefore string
	if err := db.QueryRow(`SELECT created_at FROM idempotency_keys WHERE workspace_id='ws_renew' AND key='key'`).Scan(&createdBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE idempotency_keys SET claim_expires_at=? WHERE workspace_id='ws_renew' AND key='key'`, time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Renew(ctx, "ws_renew", "key", "hash", token); err != nil {
		t.Fatalf("owner renewal failed: %v", err)
	}
	var createdAfter string
	if err := db.QueryRow(`SELECT created_at FROM idempotency_keys WHERE workspace_id='ws_renew' AND key='key'`).Scan(&createdAfter); err != nil {
		t.Fatal(err)
	}
	if createdAfter != createdBefore {
		t.Fatalf("renewal must not rewrite immutable created_at: before=%q after=%q", createdBefore, createdAfter)
	}
	claimed, _, _, err = repo.Claim(ctx, "ws_renew", "key", "hash")
	if err != nil || claimed {
		t.Fatalf("renewed claim should remain owned by the current request: claimed=%v err=%v", claimed, err)
	}
}
