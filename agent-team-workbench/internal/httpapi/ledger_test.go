// ledger_test.go 任务台账 HTTP 契约测试（会话元模型 S2）：决策台账端点校验
// （quote 必填、跨任务 source_run_id 400）、rolling_digest 随 work item 详情
// 响应携带；字段名是前端契约（contracts/web/openapi.yaml 同步）。
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

// postDecision POST /work-items/{id}/decisions（带幂等键）。
func postDecision(t *testing.T, mux http.Handler, wiID, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/work-items/"+wiID+"/decisions", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "decision-key-"+time.Now().Format("150405.000000000"))
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

func getJSON(t *testing.T, mux http.Handler, path string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
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

// finishSvcRun 驱动 svc run 到 succeeded（queued→starting→…→终态；终态触发
// S2 摘要钩子）。
func finishSvcRun(t *testing.T, svc *application.Service, runID string) {
	t.Helper()
	for _, status := range []domain.RunStatus{
		domain.RunStarting, domain.RunRunning, domain.RunSucceeding, domain.RunSucceeded,
	} {
		if err := svc.RecordRunStatus(context.Background(), runID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDecisionEndpointsAndDigestDetail 防回归：决策端点校验（空 quote 400、
// 跨任务 source_run_id 400）、台账列表形状、rolling_digest 仅详情携带。
func TestDecisionEndpointsAndDigestDetail(t *testing.T) {
	ctx := context.Background()
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, leadID, _ := seedPlanHTTPEnv(t, s)

	wi, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "台账任务", AgentProfileID: leadID})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: leadID, Instruction: "第一问：数据库选什么",
		DispatchTrigger: domain.DispatchTriggerUserMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.svc.RecordRunEvent(ctx, run.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": "第一答：用 SQLite"}); err != nil {
		t.Fatal(err)
	}
	finishSvcRun(t, s.svc, run.ID)

	// 列表/bootstrap 不携带 rolling_digest（防大载荷）。
	code, listBody := getJSON(t, mux, "/api/v1/workspaces/"+wsID+"/work-items")
	if code != http.StatusOK {
		t.Fatalf("work-items 列表应 200，实际 %d", code)
	}
	if raw, _ := json.Marshal(listBody); strings.Contains(string(raw), "rolling_digest") {
		t.Fatal("列表响应不得携带 rolling_digest")
	}

	// 详情携带 rolling_digest（终态钩子已重算）。
	code, detail := getJSON(t, mux, "/api/v1/work-items/"+wi.ID)
	if code != http.StatusOK {
		t.Fatalf("work item 详情应 200，实际 %d", code)
	}
	digest, _ := detail["rolling_digest"].(string)
	if !strings.Contains(digest, "第一问：数据库选什么") || !strings.Contains(digest, "第一答：用 SQLite") {
		t.Fatalf("详情 rolling_digest 应含台账内容: %q", digest)
	}

	// 决策写入：201 + 行内容回显。
	code, created := postDecision(t, mux, wi.ID,
		`{"quote":"  就用 SQLite  ","source_run_id":"`+run.ID+`","source_ref":"msg:1"}`)
	if code != http.StatusCreated {
		t.Fatalf("决策写入应 201，实际 %d: %v", code, created)
	}
	if created["quote"] != "就用 SQLite" || created["source_run_id"] != run.ID {
		t.Fatalf("决策行回显异常: %v", created)
	}

	// 空 quote → 422（ErrValidation 平台映射）；跨任务 source_run_id → 422。
	if code, _ := postDecision(t, mux, wi.ID, `{"quote":"   "}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("空 quote 应 422，实际 %d", code)
	}
	other, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "别的任务"})
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := s.svc.CreateRun(ctx, other.ID, application.CreateRunParams{
		AgentProfileID: leadID, Instruction: "别的任务首跑",
	})
	if err != nil {
		t.Fatal(err)
	}
	code, _ = postDecision(t, mux, wi.ID, `{"quote":"x","source_run_id":"`+otherRun.ID+`"}`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("跨任务 source_run_id 应 422，实际 %d", code)
	}
	// 未知任务 → 404。
	if code, _ := postDecision(t, mux, "wi_missing", `{"quote":"x"}`); code != http.StatusNotFound {
		t.Fatalf("未知任务应 404，实际 %d", code)
	}

	// 台账列表：升序、形状齐全。
	if _, err := s.svc.RecordDecision(ctx, wi.ID, application.RecordDecisionParams{Quote: "第二条决策"}); err != nil {
		t.Fatal(err)
	}
	code, list := getJSON(t, mux, "/api/v1/work-items/"+wi.ID+"/decisions")
	if code != http.StatusOK {
		t.Fatalf("决策列表应 200，实际 %d", code)
	}
	items, _ := list["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("台账应有 2 条: %v", list)
	}
	first := items[0].(map[string]any)
	for _, key := range []string{"id", "work_item_id", "quote", "created_at"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("决策行缺字段 %s: %v", key, first)
		}
	}
	if first["quote"] != "就用 SQLite" {
		t.Fatalf("列表应升序（首条为最早写入）: %v", first)
	}
}
