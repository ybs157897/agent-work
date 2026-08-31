// claim_return_test.go M4 认领/打回命令端点的 HTTP 契约测试（设计 note
// 2026-08-24-m4-claim-join-guardrails.md §1、§5）：claim 200/409/幂等与
// return 200/409；plan 提交的 guardrails 透传与 max_dispatch 整单 400。
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// postCommand 对 work item 命令端点发 POST，返回状态码与 JSON 体。
func postCommand(t *testing.T, mux http.Handler, path, body, key string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
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

// TestClaimReturnEndpoints 验收：无 assignee 的 todo 被认领 → 200 assignee 落定；
// 已有 assignee → 409；同 agent 重复认领幂等 200。return：acceptance 打回 →
// 200 phase=execution；todo 打回 → 409。
func TestClaimReturnEndpoints(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, _, workerID := seedPlanHTTPEnv(t, s)
	ctx := context.Background()
	main, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "待领任务"})
	if err != nil {
		t.Fatal(err)
	}
	claimPath := "/api/v1/work-items/" + main.ID + "/commands/claim"

	// 认领成功：todo + 无 assignee → 200，assignee 落定。
	code, body := postCommand(t, mux, claimPath,
		`{"agent_id":"`+workerID+`","expected_version":`+strconv.Itoa(main.Version)+`}`, "claim-1")
	if code != http.StatusOK {
		t.Fatalf("认领应 200，实际 %d: %v", code, body)
	}
	if got, _ := body["agent_profile_id"].(string); got != workerID {
		t.Fatalf("认领后 assignee 应为 %s，实际 %v", workerID, got)
	}

	// 同 agent 重复认领：幂等 200。
	code, body = postCommand(t, mux, claimPath, `{"agent_id":"`+workerID+`"}`, "claim-2")
	if code != http.StatusOK {
		t.Fatalf("同 agent 重复认领应幂等 200，实际 %d: %v", code, body)
	}

	// 他人认领：已指派 → 409 state_conflict。
	code, body = postCommand(t, mux, claimPath, `{"agent_id":"agent_lead"}`, "claim-3")
	if code != http.StatusConflict {
		t.Fatalf("已指派任务认领应 409，实际 %d: %v", code, body)
	}
	if got, _ := body["code"].(string); got != "state_conflict" {
		t.Fatalf("错误码应为 state_conflict，实际 %v", got)
	}

	// 打回前置：主任务推到 in_progress/acceptance（评估通过终态，phase=acceptance）。
	wi, err := s.store.WorkItems().Get(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.svc.MoveWorkItem(ctx, wi.ID, domain.WorkItemInProgress, wi.Version); err != nil {
		t.Fatal(err)
	}
	wi, _ = s.store.WorkItems().Get(ctx, wi.ID)
	wi.EnterReview(time.Now().UTC())
	if err := s.store.WorkItems().Update(ctx, wi, wi.Version-1); err != nil {
		t.Fatal(err)
	}
	wi, _ = s.store.WorkItems().Get(ctx, wi.ID)
	wi.EnterAcceptance(time.Now().UTC())
	if err := s.store.WorkItems().Update(ctx, wi, wi.Version-1); err != nil {
		t.Fatal(err)
	}
	wi, _ = s.store.WorkItems().Get(ctx, wi.ID)
	returnPath := "/api/v1/work-items/" + wi.ID + "/commands/return"

	// acceptance 打回 → 200 phase=execution。
	code, body = postCommand(t, mux, returnPath,
		`{"reason":"验收意见未达成","expected_version":`+strconv.Itoa(wi.Version)+`}`, "return-1")
	if code != http.StatusOK {
		t.Fatalf("acceptance 打回应 200，实际 %d: %v", code, body)
	}
	if got, _ := body["phase"].(string); got != "execution" {
		t.Fatalf("打回后 phase 应为 execution，实际 %v", got)
	}

	// todo 任务打回：缺 reason → 422 review_feedback_required（RFC §7.9 reason 必填）；
	// 带 reason → 409。
	todo, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "todo 任务"})
	if err != nil {
		t.Fatal(err)
	}
	code, body = postCommand(t, mux, "/api/v1/work-items/"+todo.ID+"/commands/return", `{}`, "return-2")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("缺 reason 打回应 422，实际 %d: %v", code, body)
	}
	if got, _ := body["code"].(string); got != "review_feedback_required" {
		t.Fatalf("错误码应为 review_feedback_required，实际 %v", got)
	}
	code, body = postCommand(t, mux, "/api/v1/work-items/"+todo.ID+"/commands/return",
		`{"reason":"尚未开始"}`, "return-3")
	if code != http.StatusConflict {
		t.Fatalf("todo 打回应 409，实际 %d: %v", code, body)
	}
}

// TestPlanEndpointGuardrailsAndJoin plan 提交契约：guardrails 透传（PlanDTO 回显）、
// max_dispatch 超限整单 400、join 目标非子任务 400。
func TestPlanEndpointGuardrailsAndJoin(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, leadID, workerID := seedPlanHTTPEnv(t, s)
	main, err := s.svc.CreateWorkItem(context.Background(), wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}

	// guardrails 固化回显：单 dispatch + max_dispatch=1 → 201。
	body := `{"work_item_id":"` + main.ID + `","agent_profile_id":"` + leadID + `",` +
		`"guardrails":{"max_dispatch":1,"max_tokens":100000},` +
		`"steps":[{"verb":"dispatch","agent_id":"` + workerID + `","title":"A","instruction":"做 A"}]}`
	code, out := postPlan(t, mux, wsID, body)
	if code != http.StatusCreated {
		t.Fatalf("guardrails plan 应 201，实际 %d: %v", code, out)
	}
	gr, _ := out["guardrails"].(map[string]any)
	if gr == nil || gr["max_dispatch"] != float64(1) || gr["max_tokens"] != float64(100000) {
		t.Fatalf("PlanDTO 应回显 guardrails，实际 %v", out["guardrails"])
	}

	// max_dispatch=1 提交 2 个 dispatch → 整单 400。
	body = `{"work_item_id":"` + main.ID + `","agent_profile_id":"` + leadID + `",` +
		`"guardrails":{"max_dispatch":1},` +
		`"steps":[{"verb":"dispatch","agent_id":"` + workerID + `","title":"A","instruction":"做 A"},` +
		`{"verb":"dispatch","agent_id":"` + workerID + `","title":"B","instruction":"做 B"}]}`
	code, out = postPlan(t, mux, wsID, body)
	if code != http.StatusBadRequest {
		t.Fatalf("超出 max_dispatch 应 400，实际 %d: %v", code, out)
	}

	// join 目标非子任务 → 400。
	body = `{"work_item_id":"` + main.ID + `","agent_profile_id":"` + leadID + `",` +
		`"steps":[{"verb":"join","children":["wi_not_a_child"]}]}`
	code, out = postPlan(t, mux, wsID, body)
	if code != http.StatusBadRequest {
		t.Fatalf("join 目标非子任务应 400，实际 %d: %v", code, out)
	}
}
