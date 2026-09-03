// run_journal_test.go GET /api/v1/runs/{run_id}/journal 的 HTTP 契约测试：
// 200 响应形状与 contracts/web/openapi.yaml RunJournal schema 一致
// （字段名/可空形态是前端调试面契约），run 不存在走 problem+json 404。
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/observability"
)

// seedJournalHTTPRun 直插一条 queued run 并播种一条完整的环节链：
// dispatch（闭合 ok）→ handshake（未闭合=故障点）+ log_chunk + decision。
func seedJournalHTTPRun(t *testing.T, s *Server, wsID, wiID string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	run := &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), WorkspaceID: wsID, WorkItemID: wiID,
		Status: domain.RunQueued, Version: 1,
		Input:     map[string]any{"instruction": "journal HTTP 契约"},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Runs().Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	record := func(evType string, data map[string]any) {
		t.Helper()
		if err := s.svc.RecordRunEvent(ctx, run.ID, evType, data); err != nil {
			t.Fatal(err)
		}
	}
	record(domain.EventRunPhaseEntered, observability.PhaseEnteredPayload(observability.PhaseDispatch, 1,
		map[string]any{"host_id": "host_local"}))
	record(domain.EventRunPhaseClosed, observability.PhaseClosedPayload(observability.PhaseDispatch, observability.PhaseOK,
		nil, 12, map[string]any{"lease_id": "lease_1"}))
	record(domain.EventRunPhaseEntered, observability.PhaseEnteredPayload(observability.PhaseHandshake, 1,
		map[string]any{"session_ref": "session_1"}))
	record(domain.EventRunLogChunk, observability.LogChunkPayload("stderr", "probe failed", false))
	record(domain.EventRunLogChunk, observability.LogChunkPayload("stderr", "tail cut", true))
	record(domain.EventRunDecision, observability.DecisionPayload(observability.DecisionSelfHealRetry,
		"session_unknown triggers fresh retry", map[string]any{"failure_code": "session_unknown"}, "run_prev"))
	return run.ID
}

func TestGetRunJournalHTTPShape(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, _, _ := seedPlanHTTPEnv(t, s)
	wi, err := s.svc.CreateWorkItem(context.Background(), wsID, application.CreateWorkItemParams{Title: "journal 任务"})
	if err != nil {
		t.Fatal(err)
	}
	runID := seedJournalHTTPRun(t, s, wsID, wi.ID)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID+"/journal", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("journal 应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应非 JSON: %s", rec.Body.String())
	}

	if body["run_id"] != runID {
		t.Fatalf("run_id 不对: %v", body["run_id"])
	}
	if _, err := time.Parse(time.RFC3339, body["generated_at"].(string)); err != nil {
		t.Fatalf("generated_at 应为 RFC3339: %v", body["generated_at"])
	}
	// 响应必须显式携带可空键（null 而不是缺键）——未闭合形态是消费者契约。
	for _, key := range []string{"phases", "log", "governance", "decisions"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("响应缺少键 %q: %v", key, body)
		}
	}
	if body["governance"] != nil {
		t.Fatalf("无 receipt 引用的 run governance 应为 null: %v", body["governance"])
	}

	phases, ok := body["phases"].([]any)
	if !ok || len(phases) != 2 {
		t.Fatalf("phases 应为 2 元素数组: %v", body["phases"])
	}
	dispatch, _ := phases[0].(map[string]any)
	if dispatch["phase"] != string(observability.PhaseDispatch) || dispatch["attempt"] != float64(1) {
		t.Fatalf("dispatch 环节头不对: %v", dispatch)
	}
	if dispatch["closed_at"] == nil || dispatch["outcome"] != "ok" || dispatch["duration_ms"] != float64(12) {
		t.Fatalf("dispatch 应闭合 ok/12ms: %v", dispatch)
	}
	if dispatch["failure"] != nil {
		t.Fatalf("ok 环节 failure 应为 null: %v", dispatch["failure"])
	}
	detail, _ := dispatch["detail"].(map[string]any)
	if detail["lease_id"] != "lease_1" {
		t.Fatalf("closed detail 应带 lease_id: %v", dispatch["detail"])
	}
	if _, err := time.Parse(time.RFC3339, dispatch["entered_at"].(string)); err != nil {
		t.Fatalf("entered_at 应为 RFC3339: %v", dispatch["entered_at"])
	}

	handshake, _ := phases[1].(map[string]any)
	if handshake["phase"] != string(observability.PhaseHandshake) {
		t.Fatalf("第二环节应为 handshake: %v", handshake)
	}
	if handshake["closed_at"] != nil || handshake["outcome"] != nil || handshake["duration_ms"] != nil {
		t.Fatalf("未闭合环节三键应全 null: %v", handshake)
	}
	handshakeDetail, _ := handshake["detail"].(map[string]any)
	if handshakeDetail["session_ref"] != "session_1" {
		t.Fatalf("未闭合 detail 应取 entered 载荷: %v", handshake["detail"])
	}

	log, _ := body["log"].(map[string]any)
	if log["chunks"] != float64(2) || log["truncated"] != true {
		t.Fatalf("log 统计应 chunks=2 truncated=true: %v", log)
	}

	decisions, _ := body["decisions"].([]any)
	if len(decisions) != 1 {
		t.Fatalf("decisions 应为 1 元素数组: %v", body["decisions"])
	}
	decision, _ := decisions[0].(map[string]any)
	if decision["kind"] != string(observability.DecisionSelfHealRetry) || decision["link_run_id"] != "run_prev" {
		t.Fatalf("decision 形状不对: %v", decision)
	}
	if _, err := time.Parse(time.RFC3339, decision["occurred_at"].(string)); err != nil {
		t.Fatalf("decision.occurred_at 应为 RFC3339: %v", decision["occurred_at"])
	}
	inputs, _ := decision["inputs"].(map[string]any)
	if inputs["failure_code"] != "session_unknown" {
		t.Fatalf("decision inputs 应带 failure_code: %v", decision["inputs"])
	}
}

func TestGetRunJournalHTTPNotFound(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	seedPlanHTTPEnv(t, s)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_missing/journal", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("缺失 run 应 404，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("404 应为 problem+json，实际 %q", got)
	}
	var problem Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("404 响应非 problem+json: %s", rec.Body.String())
	}
	if problem.Code != "not_found" || problem.Status != http.StatusNotFound {
		t.Fatalf("problem 形状不对: %+v", problem)
	}
}
