package runnergateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gorilla/websocket"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/migtest"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

// TestSQLiteHelloThenRemoteDispatch is the no-fake ownership regression for
// Runner v2. It proves the hello projection stores a global Runner with a
// NULL legacy workspace_id, the workspace→host Location read model sees it,
// and a remote offer can create a FK-backed lease.
func TestSQLiteHelloThenRemoteDispatch(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "runner-gateway.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	const (
		workspaceID = "ws_runner_gateway"
		hostID      = "host_rungw"
		runnerID    = "runner_gateway"
		secret      = "runner-secret"
		workItemID  = "wi_runner_gateway"
		runID       = "run_runner_gateway"
	)
	if _, err := db.Exec(`INSERT INTO workspaces(id,name,timezone,version,created_at,updated_at)
		VALUES (?,?,?,1,?,?)`, workspaceID, "runner gateway", "UTC", now, now); err != nil {
		t.Fatal(err)
	}
	if err := store.ExecutionHosts().Create(ctx, &domain.ExecutionHost{
		ID: hostID, Name: "remote", Kind: domain.HostKindRemote, Status: domain.HostStatusOffline,
		EnrollmentRef: enrollmentDigest(secret), Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	g := New(store, nil, nil)
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + ConnectPath
	credential := "atw_host_" + hostID + "_" + secret
	conn := dialWithCredential(t, url, credential)
	mount := domain.HostMount{
		Alias: "agent-work", RepositoryIdentity: "repo_runner_gateway", RegistryGeneration: "gen_runner_gateway",
		SupportedRefKinds: []domain.RefKind{domain.RefRoot},
		Checkouts:         []domain.MountCheckout{{Ref: "root", Kind: "root"}},
	}
	if err := conn.WriteMessage(websocket.TextMessage, helloEnvelope(hostID, runnerID, "epoch_1", nil, []domain.HostMount{mount})); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("server.welcome: %v", err)
	}

	runner, err := store.Runners().Get(ctx, runnerID)
	if err != nil {
		t.Fatalf("hello runner projection: %v", err)
	}
	if runner.ExecutionHostID != hostID || runner.WorkspaceID != "" || runner.BootID != "boot_"+runnerID || runner.ConnectionEpoch != "epoch_1" {
		t.Fatalf("Runner v2 projection 应仅归属 Host: %+v", runner)
	}
	if err := store.ExecutionHosts().Create(ctx, &domain.ExecutionHost{
		ID: "host_rungw_b", Name: "remote-b", Kind: domain.HostKindRemote, Status: domain.HostStatusOffline,
		EnrollmentRef: "other", Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Runners().Upsert(ctx, &application.Runner{
		ID: runnerID, ExecutionHostID: "host_rungw_b", BootID: "boot_other", ConnectionEpoch: "epoch_other",
		Label: runnerID, Slots: 1, Status: "connected",
	}); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("同 runner_id 不得跨 Host 改绑，实际 %v", err)
	}
	if err := store.WorkspaceLocations().Create(ctx, &domain.WorkspaceLocation{
		ID: "wsloc_runner_gateway", WorkspaceID: workspaceID, ExecutionHostID: hostID,
		MountAlias: mount.Alias, MountGeneration: mount.RegistryGeneration,
		RepositoryIdentity: mount.RepositoryIdentity, IsDefault: true, Status: domain.LocationReady,
		Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	visible, err := store.Runners().List(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 1 || visible[0].ID != runnerID {
		t.Fatalf("WorkspaceLocation→Host 投影应看见 Runner: %+v", visible)
	}
	if _, err := db.Exec(`INSERT INTO work_items(id,workspace_id,title,record_kind,created_at,updated_at)
		VALUES (?,?,?,'chat',?,?)`, workItemID, workspaceID, "runner", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO execution_runs(id,workspace_id,work_item_id,status,input,version,created_at,updated_at)
		VALUES (?,?,?,'queued','{}',1,?,?)`, runID, workspaceID, workItemID, now, now); err != nil {
		t.Fatal(err)
	}
	snapshot := rootSnapshotFor(hostID)
	snapshot.RunID = runID
	snapshot.WorkspaceID = workspaceID
	snapshot.WorkspaceLocationID = "wsloc_runner_gateway"
	snapshot.MountAlias = mount.Alias
	snapshot.MountGeneration = mount.RegistryGeneration
	snapshot.RepositoryIdentity = mount.RepositoryIdentity
	snapshot.SnapshotDigest = snapshot.ComputeDigest()
	if err := g.Dispatch(ctx, &domain.ExecutionRun{ID: runID, Input: map[string]any{"instruction": "remote"}}, snapshot, "mock"); err != nil {
		t.Fatalf("remote dispatch: %v", err)
	}
	lease, err := store.Runners().ActiveLease(ctx, runID)
	if err != nil {
		t.Fatalf("remote dispatch lease: %v", err)
	}
	if lease.RunnerID != runnerID || lease.FencingToken < 1 || lease.Released {
		t.Fatalf("remote lease 不正确: %+v", lease)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("remote offer 未到达: %v", err)
	}
	var offer Envelope
	if err := json.Unmarshal(raw, &offer); err != nil {
		t.Fatal(err)
	}
	if offer.Method != "run.offer" || offer.V != ProtocolVersion {
		t.Fatalf("remote offer 协议不正确: %+v", offer)
	}
}
