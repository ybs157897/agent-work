package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestAcceptArtifactEndpointIsRunScopedIdempotentAndApprovalProtected(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	workspaceID, leadID, _ := seedPlanHTTPEnv(t, s)
	root, err := s.svc.CreateWorkItem(context.Background(), workspaceID, application.CreateWorkItemParams{
		Title: "artifact evidence task", RecordKind: domain.RecordKindTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.svc.CreateRun(context.Background(), root.ID, application.CreateRunParams{
		AgentProfileID: leadID, Instruction: "produce evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact := &domain.Artifact{
		ID: domain.NewID(domain.PrefixArtifact), RunID: run.ID, LogicalPath: "result.txt",
		Mime: "text/plain", Sha256: "sha256:test", Status: domain.ArtifactDraft, CreatedAt: time.Now().UTC(),
	}
	if err := s.store.Runs().CreateArtifact(context.Background(), artifact); err != nil {
		t.Fatal(err)
	}

	path := "/api/v1/runs/" + run.ID + "/artifacts/" + artifact.ID + "/commands/accept"
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
	req.Header.Set("Idempotency-Key", "artifact-accept-1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("artifact accept status=%d body=%s", rec.Code, rec.Body.String())
	}
	var accepted map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted["id"] != artifact.ID || accepted["run_id"] != run.ID || accepted["status"] != string(domain.ArtifactAccepted) {
		t.Fatalf("artifact accept response mismatch: %#v", accepted)
	}

	replayReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
	replayReq.Header.Set("Idempotency-Key", "artifact-accept-1")
	replay := httptest.NewRecorder()
	mux.ServeHTTP(replay, replayReq)
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("exact accept replay must be idempotent: status=%d headers=%v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}

	wrongRunReq := httptest.NewRequest(http.MethodPost,
		"/api/v1/runs/run_other/artifacts/"+artifact.ID+"/commands/accept", strings.NewReader(`{}`))
	wrongRunReq.Header.Set("Idempotency-Key", "artifact-accept-wrong-run")
	wrongRun := httptest.NewRecorder()
	mux.ServeHTTP(wrongRun, wrongRunReq)
	if wrongRun.Code != http.StatusNotFound {
		t.Fatalf("artifact from a different path Run must be hidden: status=%d body=%s", wrongRun.Code, wrongRun.Body.String())
	}

	s.SetDemoRole(domain.RoleViewer)
	viewerReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
	viewerReq.Header.Set("Idempotency-Key", "artifact-accept-viewer")
	viewer := httptest.NewRecorder()
	mux.ServeHTTP(viewer, viewerReq)
	if viewer.Code != http.StatusForbidden {
		t.Fatalf("viewer must not accept artifact evidence: status=%d body=%s", viewer.Code, viewer.Body.String())
	}
}
