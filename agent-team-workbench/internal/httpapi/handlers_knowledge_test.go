package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func seedKnowledgeHTTPItem(t *testing.T, s *Server, ws, owner, title string, visibility domain.KnowledgeVisibility) (*domain.KnowledgeItem, *domain.KnowledgeVersion) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	item := &domain.KnowledgeItem{ID: domain.NewID(domain.PrefixKnowledgeItem), WorkspaceID: ws, OwnerAgentID: owner, Visibility: visibility, Kind: "fact", Title: title, Status: domain.KnowledgeStatusDraft, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.store.Knowledge().CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	v := &domain.KnowledgeVersion{ID: domain.NewID(domain.PrefixKnowledgeVersion), ItemID: item.ID, Version: 1, BaseVersion: 0, Status: domain.KnowledgeStatusDraft, Kind: "fact", Title: title, BodyMarkdown: "## 规则\n" + title + " 的原文证据。", Scope: domain.KnowledgeScope{}, Metadata: map[string]any{}, CreatedByAgentID: owner, CreatedAt: now}
	if err := v.SealContent(); err != nil {
		t.Fatal(err)
	}
	source := &domain.KnowledgeSource{ID: domain.NewID(domain.PrefixKnowledgeSource), WorkspaceID: ws, SubmittedByAgentID: owner, Kind: domain.KnowledgeSourceDocument, Ref: "source.md@revision-1", Excerpt: title, CreatedAt: now}
	if err := s.store.Knowledge().CreateVersionBundle(ctx, v, []*domain.KnowledgeSource{source}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Knowledge().PublishVersion(ctx, item.ID, v.ID, 0, now); err != nil {
		t.Fatal(err)
	}
	return item, v
}

func TestKnowledgeHTTPSharedViewCannotSelectPrivateAgentAsViewer(t *testing.T) {
	s := newPlanTestServer(t)
	ws, owner, _ := seedPlanHTTPEnv(t, s)
	shared, _ := seedKnowledgeHTTPItem(t, s, ws, owner, "共享规则", domain.KnowledgeVisibilityWorkspace)
	private, _ := seedKnowledgeHTTPItem(t, s, ws, owner, "私有经验", domain.KnowledgeVisibilityPrivate)
	s.SetDemoRole(domain.RoleViewer)
	mux := s.Routes()
	code, body := getSearchJSONWith(t, mux, "/api/v1/workspaces/"+ws+"/knowledge/items")
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, body)
	}
	if cursor, present := body["next_cursor"]; !present || cursor != nil {
		t.Fatalf("last page must explicitly return null next_cursor: %v", body)
	}
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), shared.ID) || strings.Contains(string(raw), private.ID) {
		t.Fatalf("wrong visibility: %s", raw)
	}
	code, _ = getSearchJSONWith(t, mux, "/api/v1/workspaces/"+ws+"/knowledge/items/"+private.ID)
	if code != http.StatusNotFound {
		t.Fatalf("private direct lookup returned %d", code)
	}
	code, _ = getSearchJSONWith(t, mux, "/api/v1/workspaces/"+ws+"/knowledge/items?agent_id="+owner)
	if code < 400 {
		t.Fatal("read-only viewer impersonated Agent")
	}
}

func TestKnowledgePrivateMaintenanceRequiresHumanManagementPermission(t *testing.T) {
	s := newPlanTestServer(t)
	ws, owner, _ := seedPlanHTTPEnv(t, s)
	item, _ := seedKnowledgeHTTPItem(t, s, ws, owner, "私有维护", domain.KnowledgeVisibilityPrivate)
	path := "/api/v1/workspaces/" + ws + "/knowledge/items/" + item.ID + "/repeal?agent_id=" + owner
	call := func(key string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"expected_version":1,"reason":"原结论已失效"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		s.Routes().ServeHTTP(rec, req)
		return rec
	}
	s.SetDemoRole(domain.RoleViewer)
	if rec := call("viewer-private-repeal"); rec.Code != http.StatusForbidden {
		t.Fatalf("view parameter bypassed management permission: %d %s", rec.Code, rec.Body)
	}
	s.SetDemoRole(domain.RoleOwner)
	if rec := call("owner-private-repeal"); rec.Code != http.StatusOK {
		t.Fatalf("authorized human could not maintain explicit private view: %d %s", rec.Code, rec.Body)
	}
	current, err := s.store.Knowledge().GetItem(context.Background(), ws, owner, item.ID)
	if err != nil || current.Status != domain.KnowledgeStatusRepealed {
		t.Fatalf("private repeal did not persist: %+v %v", current, err)
	}
}

func TestKnowledgeVersionURLCannotReadADifferentItem(t *testing.T) {
	s := newPlanTestServer(t)
	ws, owner, _ := seedPlanHTTPEnv(t, s)
	a, _ := seedKnowledgeHTTPItem(t, s, ws, owner, "A", domain.KnowledgeVisibilityWorkspace)
	_, bVersion := seedKnowledgeHTTPItem(t, s, ws, owner, "B", domain.KnowledgeVisibilityWorkspace)
	code, _ := getSearchJSONWith(t, s.Routes(), "/api/v1/workspaces/"+ws+"/knowledge/items/"+a.ID+"/versions/"+bVersion.ID)
	if code != http.StatusNotFound {
		t.Fatalf("cross-item version accepted: %d", code)
	}
	code, body := getSearchJSONWith(t, s.Routes(), "/api/v1/workspaces/"+ws+"/knowledge/items/"+a.ID+"/versions/1")
	if code != http.StatusOK || body["sources"] == nil || body["version"] == nil {
		t.Fatalf("fixed citation missing evidence: %d %v", code, body)
	}
}

func TestKnowledgeRunAPIRequiresBearerEvenInOwnerDesktop(t *testing.T) {
	s := newPlanTestServer(t)
	for _, path := range []string{"/items/kb_unknown", "/jobs/kbj_unknown"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-agent/runs/run_unknown"+path, nil)
		s.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("demo owner bypassed Run authorization: %d", rec.Code)
		}
	}
}
