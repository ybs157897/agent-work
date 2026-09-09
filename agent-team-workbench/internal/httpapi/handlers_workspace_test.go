package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/agentconfig"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/hostregistry"
)

type nonDurableWorkspaceAgentConfig struct{}

func (nonDurableWorkspaceAgentConfig) Import(context.Context, string) (agentconfig.ImportResult, error) {
	return agentconfig.ImportResult{}, nil
}

func TestCreateWorkspaceKeepsSetupFailedWhenAgentConfigSyncIsNotDurable(t *testing.T) {
	server := newPlanTestServer(t)
	server.svc.EnableAgentConfigSyncIntents()
	server.agentCfg = nonDurableWorkspaceAgentConfig{}
	wsID, _, _ := seedPlanHTTPEnv(t, server)
	ctx := context.Background()
	now := time.Now().UTC()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("workspace"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	registryPath := filepath.Join(t.TempDir(), "host-registry.yaml")
	registryYAML := fmt.Sprintf("version: 1\nmounts:\n  - alias: target\n    root: %q\n    repository_identity: repo-target\n", root)
	if err := os.WriteFile(registryPath, []byte(registryYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := hostregistry.Load(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	server.hostRegistry = registry
	if _, err := server.store.ExecutionHosts().EnsureLocalHost(ctx, now); err != nil {
		t.Fatal(err)
	}
	mount := registry.Advertise()[0]
	mount.ExecutionHostID = domain.LocalHostID
	mount.Status = domain.MountStatusReady
	mount.LastSeenAt = now
	if err := server.store.ExecutionHosts().UpsertMount(ctx, &mount); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"name":"target","project":{"execution_host_id":%q,"mount_alias":"target","mount_generation":%q,"repository_identity":"repo-target"},"source_workspace_id":%q}`,
		domain.LocalHostID, mount.RegistryGeneration, wsID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "workspace-nondurable-sync")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Setup struct {
			Status string `json:"status"`
		} `json:"setup"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Setup.Status != "failed" {
		t.Fatalf("setup status must use frontend contract value failed, got %q", response.Setup.Status)
	}
	projectList, err := server.store.WorkspaceProjects().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projectList) != 1 || projectList[0].Status != domain.WorkspaceProjectSetupFailed {
		t.Fatalf("project setup must remain failed, got %+v", projectList)
	}
}

func TestWorkspaceScopeFenceRejectsForeignLocationPatch(t *testing.T) {
	server := newPlanTestServer(t)
	wsID, _, _ := seedPlanHTTPEnv(t, server)
	location, err := server.store.WorkspaceLocations().DefaultFor(context.Background(), wsID)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/workspace-locations/"+location.ID,
		strings.NewReader(`{"mount_generation":"gen_seed","expected_version":1}`))
	req.Header.Set("Idempotency-Key", "foreign-location-patch")
	req.Header.Set("X-Workspace-ID", "ws_other")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("foreign location patch must be scope rejected: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
