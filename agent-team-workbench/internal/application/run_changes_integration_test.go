package application_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/filechanges"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func seedRunChangesEnv(t *testing.T) (*application.Service, application.Store, *domain.ExecutionRun, string) {
	t.Helper()
	ctx := context.Background()
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_changes", Name: "changes", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, "ws_changes")
	agent := &domain.AgentProfile{
		ID: "agent_changes", WorkspaceID: ws.ID, Name: "Agent", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "codex_local"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	binding := &domain.RuntimeBinding{
		ID: "rb_changes", WorkspaceID: ws.ID, RuntimeLabel: "codex_local", AdapterID: "codex-appserver",
		Capabilities: map[string]string{"multi_turn": "supported"}, Status: domain.BindingReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Bindings().Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	wi, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "changes", AgentProfileID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "edit"})
	if err != nil {
		t.Fatal(err)
	}
	return svc, store, run, t.TempDir()
}

func recordSnapshot(t *testing.T, svc *application.Service, runID, root, path string, before *string, beforeExists bool, after string) {
	t.Helper()
	beforeText := ""
	if before != nil {
		beforeText = *before
	}
	err := svc.RecordRunEvent(context.Background(), runID, domain.EventToolCompleted, map[string]any{
		"call_id": "call-" + path,
		"file_change_snapshot": map[string]any{
			"workspace_root": root,
			"path":           path,
			"before_content": beforeText,
			"after_content":  after,
			"before_exists":  beforeExists,
			"after_exists":   true,
			"before_hash":    filechanges.Hash(beforeText),
			"after_hash":     filechanges.Hash(after),
			"write_count":    1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunChangesFoldsSamePathAndRevertsNewFileIdempotently(t *testing.T) {
	svc, store, run, root := seedRunChangesEnv(t)
	path := "notes/roadmap.md"
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recordSnapshot(t, svc, run.ID, root, path, nil, false, "a\n")
	first := "a\n"
	recordSnapshot(t, svc, run.ID, root, path, &first, true, "a\nb\n")

	changes, err := svc.RunChanges(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if changes.FileCount != 1 || changes.Additions != 2 || changes.Deletions != 0 || changes.Files[0].Kind != "added" || changes.Files[0].WriteCount != 2 {
		t.Fatalf("unexpected changes: %+v", changes)
	}
	diff, err := svc.RunChangeDiff(context.Background(), run.ID, path)
	if err != nil || diff.Diff == "" {
		t.Fatalf("diff=%q err=%v", diff.Diff, err)
	}
	reverted, err := svc.RevertRunChanges(context.Background(), run.ID, "revert-1")
	if err != nil {
		t.Fatal(err)
	}
	if reverted.State != "reverted" || reverted.CanRevert {
		t.Fatalf("unexpected reverted state: %+v", reverted)
	}
	if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
		t.Fatalf("new file was not removed: %v", err)
	}

	restarted := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	if _, err := restarted.RevertRunChanges(context.Background(), run.ID, "revert-1"); err != nil {
		t.Fatalf("same key replay: %v", err)
	}
	if _, err := restarted.RevertRunChanges(context.Background(), run.ID, "revert-2"); !errors.Is(err, application.ErrRunChangesConflict) {
		t.Fatalf("different key should conflict: %v", err)
	}
}

func TestRunChangesExternalModificationConflictsBeforeAnyWrite(t *testing.T) {
	svc, _, run, root := seedRunChangesEnv(t)
	beforeA, beforeB := "old-a\n", "old-b\n"
	for _, value := range []struct{ path, before, after string }{
		{"a.txt", beforeA, "agent-a\n"}, {"b.txt", beforeB, "agent-b\n"},
	} {
		if err := os.WriteFile(filepath.Join(root, value.path), []byte(value.after), 0o644); err != nil {
			t.Fatal(err)
		}
		before := value.before
		recordSnapshot(t, svc, run.ID, root, value.path, &before, true, value.after)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err := svc.RunChanges(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if changes.CanRevert || changes.Reason == "" {
		t.Fatalf("external edit should disable preview revert: %+v", changes)
	}

	if _, err := svc.RevertRunChanges(context.Background(), run.ID, "conflict"); !errors.Is(err, application.ErrRunChangesConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	gotA, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	gotB, _ := os.ReadFile(filepath.Join(root, "b.txt"))
	if string(gotA) != "agent-a\n" || string(gotB) != "external\n" {
		t.Fatalf("conflict wrote partial state: a=%q b=%q", gotA, gotB)
	}
}

func TestRunChangesDistinguishesExistingEmptyFromNewEmpty(t *testing.T) {
	svc, _, run, root := seedRunChangesEnv(t)
	path := "empty.txt"
	if err := os.WriteFile(filepath.Join(root, path), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	recordSnapshot(t, svc, run.ID, root, path, nil, false, "")
	changes, err := svc.RunChanges(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if changes.Files[0].Kind != "added" {
		t.Fatalf("kind=%s", changes.Files[0].Kind)
	}
	if _, err := svc.RevertRunChanges(context.Background(), run.ID, "empty-new"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
		t.Fatalf("empty new file not removed: %v", err)
	}
}

func TestRunChangesRestoresAnExistingEmptyFile(t *testing.T) {
	svc, _, run, root := seedRunChangesEnv(t)
	path := "existing-empty.txt"
	if err := os.WriteFile(filepath.Join(root, path), []byte("agent\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	before := ""
	recordSnapshot(t, svc, run.ID, root, path, &before, true, "agent\n")
	if _, err := svc.RevertRunChanges(context.Background(), run.ID, "empty-existing"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, path))
	if err != nil || len(got) != 0 {
		t.Fatalf("existing empty file was not restored: %q err=%v", got, err)
	}
}

func TestRunChangesRejectsSnapshotOutsideWorkspace(t *testing.T) {
	svc, _, run, root := seedRunChangesEnv(t)
	err := svc.RecordRunEvent(context.Background(), run.ID, domain.EventToolCompleted, map[string]any{
		"file_change_snapshot": map[string]any{
			"workspace_root": root,
			"path":           "../outside.txt",
			"before_content": "",
			"after_content":  "bad",
			"before_exists":  false,
			"after_exists":   true,
			"after_hash":     filechanges.Hash("bad"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	changes, err := svc.RunChanges(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if changes.State != "unavailable" || changes.FileCount != 0 || changes.CanRevert {
		t.Fatalf("unsafe snapshot leaked into summary: %+v", changes)
	}
}
