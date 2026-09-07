package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type knowledgeAccessStore struct {
	Store
	runs RunRepo
}

func (s knowledgeAccessStore) Runs() RunRepo { return s.runs }

type knowledgeAccessRuns struct {
	RunRepo
	run *domain.ExecutionRun
}

func (r knowledgeAccessRuns) Get(context.Context, string) (*domain.ExecutionRun, error) {
	return r.run, nil
}

func TestKnowledgeRunCapabilityCannotBeForgedOrUsedAfterCompletion(t *testing.T) {
	run := &domain.ExecutionRun{ID: "run_test", WorkspaceID: "ws_test", AgentProfileID: "agent_test", Status: domain.RunRunning, Input: map[string]any{"instruction": "do the task"}}
	svc := NewService(knowledgeAccessStore{runs: knowledgeAccessRuns{run: run}}, nil, nil, nil)
	svc.KnowledgeEndpoint = "http://127.0.0.1:12345"
	svc.KnowledgeCLIPath = "/tmp/a directory/atw-knowledge"
	svc.KnowledgeAccessDir = t.TempDir()
	if err := svc.AttachKnowledgeRunAccess(run, domain.LocalHostID); err != nil {
		t.Fatal(err)
	}
	instruction := run.Input["instruction"].(string)
	if strings.Contains(instruction, "ATW_KNOWLEDGE_TOKEN") || strings.Contains(instruction, "ATW_KNOWLEDGE_URL") {
		t.Fatalf("instruction contains a secret-bearing env bridge: %s", instruction)
	}
	if !strings.Contains(instruction, "--access-file") {
		t.Fatalf("instruction missing access-file bridge: %s", instruction)
	}
	access := run.Input["knowledge_access"].(map[string]any)
	accessPath, _ := access["access_file"].(string)
	if accessPath == "" || !strings.Contains(instruction, accessPath) {
		t.Fatalf("instruction/access metadata missing capability path: %q / %v", accessPath, access)
	}
	raw, err := os.ReadFile(accessPath)
	if err != nil {
		t.Fatal(err)
	}
	var capability knowledgeAccessFile
	if err := json.Unmarshal(raw, &capability); err != nil {
		t.Fatal(err)
	}
	if capability.Token == "" || capability.URL == "" {
		t.Fatalf("capability file is incomplete: %+v", capability)
	}
	info, err := os.Stat(accessPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("capability file mode = %o, want 600", got)
	}
	dirInfo, err := os.Stat(svc.KnowledgeAccessDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("capability directory mode = %o, want 700", got)
	}
	token := capability.Token
	digest := sha256.Sum256([]byte(token))
	if access["token_digest"] != hex.EncodeToString(digest[:]) {
		t.Fatal("capability digest mismatch")
	}
	if !strings.Contains(instruction, "'/tmp/a directory/atw-knowledge'") {
		t.Fatal("client path is not shell quoted")
	}
	encoded, err := json.Marshal(run.Input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), token) {
		t.Fatal("token leaked into Run.Input")
	}
	if _, err := svc.AuthenticateKnowledgeRun(context.Background(), run.ID, strings.Repeat("0", 64)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("forged credential accepted")
	}
	if got, err := svc.AuthenticateKnowledgeRun(context.Background(), run.ID, token); err != nil || got.AgentProfileID != "agent_test" {
		t.Fatal("caller identity not bound to Run")
	}
	if err := svc.CleanupKnowledgeRunAccess(context.Background(), run.ID); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("active Run capability was cleaned: %v", err)
	}
	run.Status = domain.RunSucceeded
	if _, err := svc.AuthenticateKnowledgeRun(context.Background(), run.ID, token); err == nil {
		t.Fatal("terminal Run retained knowledge authority")
	}
	if err := svc.CleanupKnowledgeRunAccess(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(accessPath); !os.IsNotExist(err) {
		t.Fatalf("terminal cleanup left capability file: %v", err)
	}
}

func TestLibrarianNeverReceivesWorkerQueryCapability(t *testing.T) {
	svc := NewService(nil, nil, nil, nil)
	svc.KnowledgeEndpoint = "http://localhost"
	svc.KnowledgeCLIPath = "/tmp/client"
	run := &domain.ExecutionRun{Input: map[string]any{"instruction": "investigate", "knowledge_librarian": map[string]any{"job_id": "job"}}}
	if err := svc.AttachKnowledgeRunAccess(run, domain.LocalHostID); err != nil {
		t.Fatal(err)
	}
	if _, ok := run.Input["knowledge_access"]; ok {
		t.Fatal("recursive knowledge authority attached to librarian")
	}
}

func TestEmptyHostDoesNotReceiveShellCapability(t *testing.T) {
	svc := NewService(nil, nil, nil, nil)
	svc.KnowledgeEndpoint = "http://localhost"
	svc.KnowledgeCLIPath = "/tmp/client"
	svc.KnowledgeAccessDir = t.TempDir()
	run := &domain.ExecutionRun{ID: "run_empty_host", Input: map[string]any{"instruction": "task"}}
	if err := svc.AttachKnowledgeRunAccess(run, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := run.Input["knowledge_access"]; ok {
		t.Fatal("empty-host Run received local knowledge capability")
	}
}

func TestRemoteRunDoesNotReceiveShellCapability(t *testing.T) {
	svc := NewService(nil, nil, nil, nil)
	svc.KnowledgeEndpoint = "http://localhost"
	svc.KnowledgeCLIPath = "/tmp/client"
	svc.KnowledgeAccessDir = t.TempDir()
	run := &domain.ExecutionRun{ID: "run_remote", Input: map[string]any{"instruction": "remote task"}}
	if err := svc.AttachKnowledgeRunAccess(run, "host_remote"); err != nil {
		t.Fatal(err)
	}
	if _, ok := run.Input["knowledge_access"]; ok {
		t.Fatal("remote Run received local knowledge capability")
	}
	if strings.Contains(run.Input["instruction"].(string), "knowledge") {
		t.Fatal("remote instruction received a knowledge bridge")
	}
	entries, err := os.ReadDir(svc.KnowledgeAccessDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("remote bridge created capability files: %v", entries)
	}
}

func TestKnowledgeAccessRejectsSymlinkedCapabilityDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	svc := NewService(nil, nil, nil, nil)
	svc.KnowledgeEndpoint = "http://localhost"
	svc.KnowledgeCLIPath = "/tmp/client"
	svc.KnowledgeAccessDir = link
	run := &domain.ExecutionRun{ID: "run_symlink", Input: map[string]any{"instruction": "task"}}
	if err := svc.AttachKnowledgeRunAccess(run, domain.LocalHostID); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("symlinked capability directory error = %v", err)
	}
}

func TestKnowledgeAccessRejectsPathTraversalRunID(t *testing.T) {
	svc := NewService(nil, nil, nil, nil)
	svc.KnowledgeEndpoint = "http://localhost"
	svc.KnowledgeCLIPath = "/tmp/client"
	svc.KnowledgeAccessDir = t.TempDir()
	run := &domain.ExecutionRun{ID: "../escape", Input: map[string]any{"instruction": "task"}}
	if err := svc.AttachKnowledgeRunAccess(run, domain.LocalHostID); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("path traversal Run ID error = %v", err)
	}
}
