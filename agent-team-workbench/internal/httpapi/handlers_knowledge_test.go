package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
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

func TestKnowledgeHTTPOwnerFilterCannotExpandViewerVisibility(t *testing.T) {
	s := newPlanTestServer(t)
	ws, lead, worker := seedPlanHTTPEnv(t, s)
	leadPublic, _ := seedKnowledgeHTTPItem(t, s, ws, lead, "Lead 公开规则", domain.KnowledgeVisibilityWorkspace)
	_, _ = seedKnowledgeHTTPItem(t, s, ws, lead, "Lead 私有规则", domain.KnowledgeVisibilityPrivate)
	_, _ = seedKnowledgeHTTPItem(t, s, ws, worker, "Worker 公开规则", domain.KnowledgeVisibilityWorkspace)
	s.SetDemoRole(domain.RoleViewer)

	code, body := getSearchJSONWith(t, s.Routes(), "/api/v1/workspaces/"+ws+"/knowledge/items?owner_agent_id="+lead+"&limit=10")
	if code != http.StatusOK {
		t.Fatalf("owner-filtered list: %d %v", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != leadPublic.ID {
		t.Fatalf("viewer owner filter exposed private or wrong owner items: %v", body)
	}
	code, body = getSearchJSONWith(t, s.Routes(), "/api/v1/workspaces/"+ws+"/knowledge/items?q=原文证据&owner_agent_id="+lead+"&limit=10")
	if code != http.StatusOK {
		t.Fatalf("owner-filtered search: %d %v", code, body)
	}
	items, _ = body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != leadPublic.ID {
		t.Fatalf("viewer owner filter did not reach body search: %v", body)
	}

	code, body = getSearchJSONWith(t, s.Routes(), "/api/v1/workspaces/"+ws+"/knowledge/items?owner_agent_id=agent_unknown_owner&limit=10")
	if code != http.StatusOK {
		t.Fatalf("unknown owner should remain a safe list: %d %v", code, body)
	}
	items, _ = body["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("unknown owner leaked items: %v", body)
	}
	code, _ = getSearchJSONWith(t, s.Routes(), "/api/v1/workspaces/"+ws+"/knowledge/items?owner_agent_id=invalid-owner")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid owner id returned %d, want 422", code)
	}

	s.SetDemoRole(domain.RoleOwner)
	code, body = getSearchJSONWith(t, s.Routes(), "/api/v1/workspaces/"+ws+"/knowledge/items?agent_id="+lead+"&owner_agent_id="+lead+"&limit=10")
	if code != http.StatusOK {
		t.Fatalf("management owner view: %d %v", code, body)
	}
	items, _ = body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("owner filter changed existing agent_id private permission: %v", body)
	}
}

func TestKnowledgeHTTPItemsSearchUsesBodyProjectionAndStableCursor(t *testing.T) {
	s := newPlanTestServer(t)
	ws, owner, _ := seedPlanHTTPEnv(t, s)
	first, _ := seedKnowledgeHTTPItem(t, s, ws, owner, "检索结果一", domain.KnowledgeVisibilityWorkspace)
	second, _ := seedKnowledgeHTTPItem(t, s, ws, owner, "检索结果二", domain.KnowledgeVisibilityWorkspace)
	_, _ = seedKnowledgeHTTPItem(t, s, ws, owner, "不应泄漏的私有结果", domain.KnowledgeVisibilityPrivate)
	s.SetDemoRole(domain.RoleViewer)
	path := "/api/v1/workspaces/" + ws + "/knowledge/items?q=检索结果&limit=1"
	code, body := getSearchJSONWith(t, s.Routes(), path)
	if code != http.StatusOK {
		t.Fatalf("search list: %d %v", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("search limit not applied: %v", body)
	}
	firstPage := items[0].(map[string]any)
	if firstPage["id"] != second.ID || firstPage["search_excerpt"] == "" {
		t.Fatalf("search item missing stable order/excerpt: %v", firstPage)
	}
	cursor, ok := body["next_cursor"].(string)
	if !ok || cursor == "" {
		t.Fatalf("search page did not return cursor: %v", body)
	}
	code, body = getSearchJSONWith(t, s.Routes(), path+"&cursor="+cursor)
	if code != http.StatusOK {
		t.Fatalf("search second page: %d %v", code, body)
	}
	items, _ = body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != first.ID || body["next_cursor"] != nil {
		t.Fatalf("search cursor page = %v", body)
	}

	for _, query := range []string{"@@@(*", "%22%20UNION%20SELECT%20*"} {
		code, body = getSearchJSONWith(t, s.Routes(), "/api/v1/workspaces/"+ws+"/knowledge/items?q="+query)
		if code != http.StatusOK {
			t.Fatalf("malicious query %q returned %d: %v", query, code, body)
		}
		if items, _ := body["items"].([]any); len(items) != 0 {
			t.Fatalf("malicious query %q returned results: %v", query, body)
		}
	}
	for _, limit := range []string{"0", "201"} {
		code, _ = getSearchJSONWith(t, s.Routes(), "/api/v1/workspaces/"+ws+"/knowledge/items?q=检索&limit="+limit)
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid search limit %s returned %d", limit, code)
		}
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

func TestKnowledgeAgentRecordBindsIdentityToCurrentRun(t *testing.T) {
	s := newPlanTestServer(t)
	ws, _, worker := seedPlanHTTPEnv(t, s)
	builtin, err := s.svc.EnsureBuiltinKnowledgeLibrarian(context.Background(), ws)
	if err != nil {
		t.Fatalf("ensure builtin librarian: %v", err)
	}
	if _, err := s.svc.UpdateAgent(context.Background(), builtin.ID, application.AgentPatch{
		RuntimePreference: &domain.RuntimePreference{Preferred: "mock"}, ExpectedVersion: builtin.Version,
	}); err != nil {
		t.Fatalf("configure builtin runtime: %v", err)
	}
	configuredBuiltin, err := s.svc.Agent(context.Background(), builtin.ID)
	if err != nil {
		t.Fatalf("read configured builtin runtime: %v", err)
	}
	if configuredBuiltin.RuntimePreference.Preferred != "mock" {
		t.Fatalf("builtin runtime preference was not persisted: %+v", configuredBuiltin.RuntimePreference)
	}
	if _, err := s.svc.ConfigureKnowledgeLibrarian(context.Background(), ws, domain.KnowledgeLibrarianConfig{
		LibrarianAgentID: builtin.ID, Enabled: true, Version: 0,
	}); err != nil {
		t.Fatalf("configure librarian: %v", err)
	}
	s.svc.KnowledgeEndpoint = "http://127.0.0.1"
	s.svc.KnowledgeCLIPath = "/tmp/atw-knowledge"
	s.svc.KnowledgeAccessDir = t.TempDir()
	wi, err := s.svc.CreateWorkItem(context.Background(), ws, application.CreateWorkItemParams{Title: "Agent knowledge record", AgentProfileID: worker})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.svc.CreateRun(context.Background(), wi.ID, application.CreateRunParams{AgentProfileID: worker, Instruction: "record confirmed requirement"})
	if err != nil {
		t.Fatal(err)
	}
	access, ok := run.Input["knowledge_access"].(map[string]any)
	if !ok {
		t.Fatalf("ordinary Run did not receive knowledge capability: %+v", run.Input)
	}
	accessPath, _ := access["access_file"].(string)
	raw, err := os.ReadFile(accessPath)
	if err != nil {
		t.Fatal(err)
	}
	var capability struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &capability); err != nil || capability.Token == "" {
		t.Fatalf("invalid Run capability: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-agent/runs/"+run.ID+"/records",
		strings.NewReader(`{"content":"用户已确认退款必须附订单号。","title":"退款约定","publish_intent":"confirmed_requirement","client_key":"record-1"}`))
	request.Header.Set("Authorization", "Bearer "+capability.Token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	s.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("record status = %d: %s", response.Code, response.Body.String())
	}
	var result application.KnowledgeAgentRecordResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Submission == nil || result.Job == nil {
		t.Fatalf("record response missing submission/job: err=%v body=%s", err, response.Body.String())
	}
	if result.Submission.AgentID != worker || result.Submission.Request.AgentID != worker || result.Submission.Request.RunID != run.ID || result.Submission.Request.WorkItemID != wi.ID {
		t.Fatalf("record did not bind source identity to current Run: %+v", result.Submission)
	}
	source := result.Submission.Request.Changes[0].Sources[0]
	if source.Kind != domain.KnowledgeSourceAgent || source.Ref != worker || source.Metadata["origin_agent_id"] != worker || source.Metadata["origin_run_id"] != run.ID || source.Metadata["record_intent"] != application.KnowledgeRecordPublishIntentConfirmedRequirement {
		t.Fatalf("record source provenance is not Run-bound: %+v", source)
	}
	jobRequest := httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-agent/runs/"+run.ID+"/jobs/"+result.Job.ID, nil)
	jobRequest.Header.Set("Authorization", "Bearer "+capability.Token)
	jobResponse := httptest.NewRecorder()
	s.Routes().ServeHTTP(jobResponse, jobRequest)
	if jobResponse.Code != http.StatusOK {
		t.Fatalf("record Job cannot be read by its originating active Run: %d %s", jobResponse.Code, jobResponse.Body.String())
	}

	// Identity fields are not part of the raw record contract. The strict
	// decoder rejects attempts to let a model or UI choose another Agent/user.
	request = httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-agent/runs/"+run.ID+"/records",
		strings.NewReader(`{"content":"forged","agent_id":"agent_other","user_id":"user_other","client_key":"record-forged"}`))
	request.Header.Set("Authorization", "Bearer "+capability.Token)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	s.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("forged identity fields returned %d: %s", response.Code, response.Body.String())
	}
}
