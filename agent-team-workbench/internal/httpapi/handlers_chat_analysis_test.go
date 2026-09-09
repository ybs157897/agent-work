package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestChatAnalysisHTTPProjectionAndAnswer(t *testing.T) {
	server := newPlanTestServer(t)
	wsID, leadID, _ := seedPlanHTTPEnv(t, server)
	chat, err := server.svc.CreateWorkItem(context.Background(), wsID, application.CreateWorkItemParams{
		Title: "HTTP 需求分析", RecordKind: domain.RecordKindChat, AgentProfileID: leadID,
	})
	if err != nil {
		t.Fatal(err)
	}
	createBody := `{"agent_profile_id":"` + leadID + `","output_contract":"chat-analysis/v1","client_key":"analysis-run-1","input":{"instruction":"整理需求，逐项确认"}}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/work-items/"+chat.ID+"/runs", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Idempotency-Key", "analysis-run-1")
	createRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create analysis Run status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil || created.RunID == "" {
		t.Fatalf("create response=%s err=%v", createRec.Body.String(), err)
	}
	run, err := server.store.Runs().Get(context.Background(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var catalog []struct {
		Kind   string `json:"kind"`
		Ref    string `json:"ref"`
		SHA256 string `json:"sha256"`
	}
	analysisInput, _ := run.Input["analysis"].(map[string]any)
	rawCatalog, _ := json.Marshal(analysisInput["source_catalog"])
	if err := json.Unmarshal(rawCatalog, &catalog); err != nil || len(catalog) == 0 {
		t.Fatalf("analysis source catalog=%s err=%v", string(rawCatalog), err)
	}
	conversation := catalog[0]
	if conversation.Kind != "conversation" {
		t.Fatalf("first catalog source should be conversation: %#v", catalog)
	}
	if err := server.svc.RecordRunStatus(context.Background(), run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	text := `{"version":"chat-analysis/v1","summary":"HTTP 摘要","sources":[{"id":"conversation-source","kind":"conversation","ref":"` + conversation.Ref + `","sha256":"` + conversation.SHA256 + `","read_status":"read"}],"items":[{"id":"scope","kind":"requirement","title":"范围","detail":"需要确认范围。","source_ids":["conversation-source"],"basis":"observed"}],"questions":[{"id":"scope-choice","prompt":"选择范围","selection":"single","options":[{"id":"read","label":"只读"},{"id":"edit","label":"编辑"}],"item_ids":["scope"]}]}`
	if err := server.svc.RecordRunEvent(context.Background(), run.ID, domain.EventMessageCompleted, map[string]any{"role": "assistant", "text": "```atw-analysis\n" + text + "\n```"}); err != nil {
		t.Fatal(err)
	}
	if err := server.svc.RecordRunStatus(context.Background(), run.ID, domain.RunSucceeding, nil); err != nil {
		t.Fatal(err)
	}
	if err := server.svc.RecordRunStatus(context.Background(), run.ID, domain.RunSucceeded, nil); err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	server.Routes().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/work-items/"+chat.ID+"/analysis", nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"status":"needs_answer"`) || !strings.Contains(get.Body.String(), `"current_question"`) {
		t.Fatalf("analysis GET status=%d body=%s", get.Code, get.Body.String())
	}
	var projection struct {
		Version  int   `json:"version"`
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	answerBody := `{"expected_version":` + jsonNumber(projection.Version) + `,"revision":` + jsonInt64(projection.Revision) + `,"question_id":"scope-choice","selected_option_ids":["read"],"disposition":"answered","client_key":"answer-http-1"}`
	answerReq := httptest.NewRequest(http.MethodPost, "/api/v1/work-items/"+chat.ID+"/analysis/answers", strings.NewReader(answerBody))
	answerReq.Header.Set("Content-Type", "application/json")
	answerReq.Header.Set("Idempotency-Key", "answer-http-1")
	answerRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(answerRec, answerReq)
	if answerRec.Code != http.StatusOK || !strings.Contains(answerRec.Body.String(), `"status":"ready"`) || !strings.Contains(answerRec.Body.String(), `"client_key":"answer-http-1"`) {
		t.Fatalf("answer POST status=%d body=%s", answerRec.Code, answerRec.Body.String())
	}
	crossRevisionBody := `{"expected_version":` + jsonNumber(projection.Version+1) + `,"revision":` + jsonInt64(projection.Revision+1) + `,"question_id":"scope-choice","selected_option_ids":["read"],"disposition":"answered","client_key":"answer-http-1"}`
	crossRevisionReq := httptest.NewRequest(http.MethodPost, "/api/v1/work-items/"+chat.ID+"/analysis/answers", strings.NewReader(crossRevisionBody))
	crossRevisionReq.Header.Set("Content-Type", "application/json")
	crossRevisionReq.Header.Set("Idempotency-Key", "answer-http-2")
	crossRevisionRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(crossRevisionRec, crossRevisionReq)
	if crossRevisionRec.Code != http.StatusConflict || !strings.Contains(crossRevisionRec.Body.String(), `"code":"idempotency_conflict"`) {
		t.Fatalf("same client key across revisions must conflict: status=%d body=%s", crossRevisionRec.Code, crossRevisionRec.Body.String())
	}
	list := httptest.NewRecorder()
	server.Routes().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/work-items/"+chat.ID+"/runs", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"output_contract":"chat-analysis/v1"`) {
		t.Fatalf("Run list must expose output contract metadata: status=%d body=%s", list.Code, list.Body.String())
	}
}

func jsonNumber(value int) string  { return strconv.Itoa(value) }
func jsonInt64(value int64) string { return strconv.FormatInt(value, 10) }
