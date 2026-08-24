// runs_test.go Run DTO 的 HTTP 契约测试：getRun 下发 adapter 上报的本轮用量
// 四字段（usage_in/usage_out/usage_cached/usage_basis，snake_case、零值省略）——
// 字段名是前端契约（contracts/web/openapi.yaml ExecutionRun schema 同步）。
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
)

// getRunJSON 取 getRun 响应码与 JSON 体。
func getRunJSON(t *testing.T, mux http.Handler, runID string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应非 JSON: %s", rec.Body.String())
		}
	}
	return rec.Code, out
}

// seedRunWithUsage 直插一条终态 run（绕过 CreateRun 的执行副作用），用量按参数落列。
func seedRunWithUsage(t *testing.T, s *Server, wsID, wiID string, usage domain.ExecutionRun) string {
	t.Helper()
	now := time.Now().UTC()
	run := &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), WorkspaceID: wsID, WorkItemID: wiID,
		Status: domain.RunSucceeded, Version: 1,
		Input:   map[string]any{"instruction": "用量契约测试"},
		UsageIn: usage.UsageIn, UsageOut: usage.UsageOut,
		UsageCached: usage.UsageCached, UsageBasis: usage.UsageBasis,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Runs().Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

// TestGetRunExposesUsage 带 per_run 用量的 run → getRun 响应含四字段且值映射正确；
// 无用量 run → 四字段省略（omitempty，零值键不得出现）。
func TestGetRunExposesUsage(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, _, _ := seedPlanHTTPEnv(t, s)
	wi, err := s.svc.CreateWorkItem(context.Background(), wsID, application.CreateWorkItemParams{Title: "用量任务"})
	if err != nil {
		t.Fatal(err)
	}

	withUsage := seedRunWithUsage(t, s, wsID, wi.ID, domain.ExecutionRun{
		UsageIn: 250, UsageOut: 50, UsageCached: 100, UsageBasis: "per_run",
	})
	code, body := getRunJSON(t, mux, withUsage)
	if code != http.StatusOK {
		t.Fatalf("getRun 应 200，实际 %d: %v", code, body)
	}
	if got := body["usage_in"].(float64); got != 250 {
		t.Fatalf("usage_in 应 250，实际 %v", got)
	}
	if got := body["usage_out"].(float64); got != 50 {
		t.Fatalf("usage_out 应 50，实际 %v", got)
	}
	if got := body["usage_cached"].(float64); got != 100 {
		t.Fatalf("usage_cached 应 100，实际 %v", got)
	}
	if got := body["usage_basis"].(string); got != "per_run" {
		t.Fatalf("usage_basis 应 per_run，实际 %v", got)
	}

	bare := seedRunWithUsage(t, s, wsID, wi.ID, domain.ExecutionRun{})
	code, body = getRunJSON(t, mux, bare)
	if code != http.StatusOK {
		t.Fatalf("getRun 应 200，实际 %d: %v", code, body)
	}
	for _, key := range []string{"usage_in", "usage_out", "usage_cached", "usage_basis"} {
		if _, ok := body[key]; ok {
			t.Fatalf("无用量 run 不得下发 %s（omitempty）: %v", key, body)
		}
	}
}
