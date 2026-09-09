package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestNativeQuestionRoutesScopeAndWaitForForwarder(t *testing.T) {
	s := newPlanTestServer(t)
	wsID, agentID, _ := seedPlanHTTPEnv(t, s)
	wi, err := s.svc.CreateWorkItem(context.Background(), wsID, application.CreateWorkItemParams{Title: "question", RecordKind: domain.RecordKindChat, AgentProfileID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.svc.CreateRun(context.Background(), wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "ask"})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := s.svc.RecordRunStatus(context.Background(), run.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	forwarded := 0
	s.svc.QuestionForwarder = func(context.Context, string, string, domain.QuestionResponse) error { forwarded++; return nil }
	q, err := s.svc.RequestQuestion(context.Background(), run.ID, domain.QuestionRequest{SessionRef: "session_1", ProviderID: "provider_q", AgentID: "main", Questions: []domain.QuestionItem{{ID: "q_0", Question: "颜色？", Options: []domain.QuestionOption{{ID: "opt_0_0", Label: "蓝色"}, {ID: "opt_0_1", Label: "绿色"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	get := httptest.NewRecorder()
	mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+run.ID+"/questions", nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), q.ID) {
		t.Fatalf("list status=%d body=%s", get.Code, get.Body.String())
	}
	wrong := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_other/questions/"+q.ID+"/commands/resolve", strings.NewReader(`{"answers":{"q_0":{"kind":"single","option_id":"opt_0_0"}}}`))
	wrong.Header.Set("Idempotency-Key", "question-wrong")
	wrongRec := httptest.NewRecorder()
	mux.ServeHTTP(wrongRec, wrong)
	if wrongRec.Code != http.StatusNotFound {
		t.Fatalf("cross-run resolve status=%d body=%s", wrongRec.Code, wrongRec.Body.String())
	}
	resolve := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+run.ID+"/questions/"+q.ID+"/commands/resolve", strings.NewReader(`{"answers":{"q_0":{"kind":"single","option_id":"opt_0_0"}},"method":"click"}`))
	resolve.Header.Set("Idempotency-Key", "question-answer")
	resolved := httptest.NewRecorder()
	mux.ServeHTTP(resolved, resolve)
	if resolved.Code != http.StatusOK || !strings.Contains(resolved.Body.String(), `"status":"answered"`) || forwarded != 1 {
		t.Fatalf("resolve status=%d forwarded=%d body=%s", resolved.Code, forwarded, resolved.Body.String())
	}
	var dto questionDTO
	if err := json.Unmarshal(resolved.Body.Bytes(), &dto); err != nil || dto.Status != domain.QuestionAnswered {
		t.Fatalf("resolved dto=%+v err=%v", dto, err)
	}
	duplicate := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+run.ID+"/questions/"+q.ID+"/commands/resolve", strings.NewReader(`{"answers":{"q_0":{"kind":"single","option_id":"opt_0_0"}}}`))
	duplicate.Header.Set("Idempotency-Key", "question-answer-replay")
	duplicateRec := httptest.NewRecorder()
	mux.ServeHTTP(duplicateRec, duplicate)
	if duplicateRec.Code != http.StatusOK || forwarded != 1 {
		t.Fatalf("duplicate resolve status=%d forwarded=%d body=%s", duplicateRec.Code, forwarded, duplicateRec.Body.String())
	}
	different := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+run.ID+"/questions/"+q.ID+"/commands/resolve", strings.NewReader(`{"answers":{"q_0":{"kind":"single","option_id":"opt_0_1"}}}`))
	different.Header.Set("Idempotency-Key", "question-answer-different")
	differentRec := httptest.NewRecorder()
	mux.ServeHTTP(differentRec, different)
	if differentRec.Code == http.StatusOK || forwarded != 1 {
		t.Fatalf("different replay must conflict status=%d forwarded=%d body=%s", differentRec.Code, forwarded, differentRec.Body.String())
	}

	qFailed, err := s.svc.RequestQuestion(context.Background(), run.ID, domain.QuestionRequest{SessionRef: "session_1", ProviderID: "provider_q_failed", AgentID: "main", Questions: []domain.QuestionItem{{ID: "q_0", Question: "失败重试？", Options: []domain.QuestionOption{{ID: "opt_0_0", Label: "是"}, {ID: "opt_0_1", Label: "否"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	s.svc.QuestionForwarder = func(context.Context, string, string, domain.QuestionResponse) error { return context.DeadlineExceeded }
	if _, err := s.svc.ResolveQuestion(context.Background(), run.ID, qFailed.ID, domain.QuestionResponse{Answers: map[string]domain.QuestionAnswer{"q_0": {Kind: "single", OptionID: "opt_0_0"}}}, "user_demo"); err == nil {
		t.Fatal("failed provider delivery must be surfaced")
	}
	failedState, err := s.store.Questions().Get(context.Background(), qFailed.ID)
	if err != nil || failedState.Status != domain.QuestionPending || failedState.Response == nil {
		t.Fatalf("ambiguous delivery must preserve immutable pending response: %+v err=%v", failedState, err)
	}
	qExternal, err := s.svc.RequestQuestion(context.Background(), run.ID, domain.QuestionRequest{SessionRef: "session_1", ProviderID: "provider_q_external", AgentID: "main", Questions: []domain.QuestionItem{{ID: "q_0", Question: "颜色？", Options: []domain.QuestionOption{{ID: "opt_0_0", Label: "蓝色"}, {ID: "opt_0_1", Label: "绿色"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.svc.RecordRunEvent(context.Background(), run.ID, domain.EventQuestionAnswered, map[string]any{"provider_question_id": qExternal.ProviderID, "session_ref": qExternal.SessionRef, "answers": map[string]any{"颜色？": "蓝色"}}); err != nil {
		t.Fatal(err)
	}
	externalState, err := s.store.Questions().Get(context.Background(), qExternal.ID)
	if err != nil || externalState.Status != domain.QuestionAnswered {
		t.Fatalf("native answered replay should reconcile: %+v err=%v", externalState, err)
	}
	qSingle, err := s.svc.RequestQuestion(context.Background(), run.ID, domain.QuestionRequest{SessionRef: "session_1", ProviderID: "provider_q_singleflight", AgentID: "main", Questions: []domain.QuestionItem{{ID: "q_0", Question: "串行？", Options: []domain.QuestionOption{{ID: "opt_0_0", Label: "是"}, {ID: "opt_0_1", Label: "否"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	var forwardMu sync.Mutex
	forwardCount := 0
	started := make(chan struct{})
	release := make(chan struct{})
	s.svc.QuestionForwarder = func(context.Context, string, string, domain.QuestionResponse) error {
		forwardMu.Lock()
		forwardCount++
		if forwardCount == 1 {
			close(started)
		}
		forwardMu.Unlock()
		<-release
		return nil
	}
	answer := domain.QuestionResponse{Answers: map[string]domain.QuestionAnswer{"q_0": {Kind: "single", OptionID: "opt_0_0"}}}
	results := make(chan error, 2)
	go func() {
		_, err := s.svc.ResolveQuestion(context.Background(), run.ID, qSingle.ID, answer, "user_demo")
		results <- err
	}()
	<-started
	go func() {
		_, err := s.svc.ResolveQuestion(context.Background(), run.ID, qSingle.ID, answer, "user_demo")
		results <- err
	}()
	time.Sleep(50 * time.Millisecond)
	forwardMu.Lock()
	if forwardCount != 1 {
		t.Fatalf("same typed answer must singleflight, forwards=%d", forwardCount)
	}
	forwardMu.Unlock()
	close(release)
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	if err := <-results; err != nil {
		t.Fatal(err)
	}

	qTerminal, err := s.svc.RequestQuestion(context.Background(), run.ID, domain.QuestionRequest{SessionRef: "session_1", ProviderID: "provider_q_terminal", AgentID: "main", Questions: []domain.QuestionItem{{ID: "q_0", Question: "终态？", Options: []domain.QuestionOption{{ID: "opt_0_0", Label: "是"}, {ID: "opt_0_1", Label: "否"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := s.svc.RecordRunStatus(context.Background(), run.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.svc.ResolveQuestion(context.Background(), run.ID, qTerminal.ID, domain.QuestionResponse{Answers: map[string]domain.QuestionAnswer{"q_0": {Kind: "single", OptionID: "opt_0_0"}}}, "user_demo"); err == nil {
		t.Fatal("terminal run must reject question answer")
	}
}
