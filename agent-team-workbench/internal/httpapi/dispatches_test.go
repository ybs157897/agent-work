// dispatches_test.go 派发卡片端点的 HTTP 契约测试（会话元模型 S1）：
// GET /api/v1/work-items/{id}/dispatches 下发批次新→旧、成员 runs（会话组）
// 摘要与触发消息摘录；字段名是前端契约（contracts/web/openapi.yaml
// DispatchCard schema 同步）。
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func getDispatchesJSON(t *testing.T, mux http.Handler, wiID string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/work-items/"+wiID+"/dispatches", nil)
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

// TestListWorkItemDispatches 卡片形状：@直达批次（lead_run_id 省略 + 触发摘录
// 取最早成员）、plan 子 run 同批成组（成员带 agent_name 与一行摘要）、新→旧
// 排序、未知任务 404。
func TestListWorkItemDispatches(t *testing.T) {
	ctx := context.Background()
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, leadID, workerID := seedPlanHTTPEnv(t, s)
	wi, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "卡片任务", AgentProfileID: leadID})
	if err != nil {
		t.Fatal(err)
	}

	// 第一批：@worker 直达 → 批次无接诊 run；指令原文进触发摘录。
	first, err := s.svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: leadID, Instruction: "@worker 帮我查一下构建失败的原因",
		DispatchTrigger: domain.DispatchTriggerUserMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	// lead 出 plan 派生 worker 子 run：继承第一批批次。
	if _, err := s.svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: wi.ID, AgentProfileID: leadID, SourceRunID: first.ID,
		Steps: []application.PlanStepInput{{
			Verb:    "dispatch",
			Payload: map[string]any{"agent_id": workerID, "title": "排查构建", "instruction": "复现构建失败"},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	code, body := getDispatchesJSON(t, mux, wi.ID)
	if code != http.StatusOK {
		t.Fatalf("dispatches 应 200，实际 %d: %v", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("第一批应只有 1 张卡片，实际 %v", body)
	}
	card := items[0].(map[string]any)
	if card["id"] == "" || card["work_item_id"] != wi.ID || card["status"] != "running" {
		t.Fatalf("卡片基础字段异常: %v", card)
	}
	if card["trigger"] != "user_message" {
		t.Fatalf("trigger 应为 user_message: %v", card)
	}
	if _, has := card["lead_run_id"]; has {
		t.Fatalf("@直达批次不得下发 lead_run_id: %v", card)
	}
	if _, has := card["closed_at"]; has {
		t.Fatalf("running 批次不得下发 closed_at: %v", card)
	}
	tm, _ := card["trigger_message"].(map[string]any)
	if tm == nil || tm["run_id"] != first.ID || tm["excerpt"] != "@worker 帮我查一下构建失败的原因" {
		t.Fatalf("触发摘录应取 @直达 run 指令原文: %v", tm)
	}
	runs, _ := card["runs"].([]any)
	if len(runs) != 2 {
		t.Fatalf("批次成员应为 lead run + 子 run: %v", card["runs"])
	}
	leadCard := runs[0].(map[string]any)
	if leadCard["id"] != first.ID || leadCard["agent_profile_id"] != workerID {
		// @直达：触发 run 本身归 worker（升序首位），agent_name 随之解析。
		t.Fatalf("成员（升序首位）应为 @直达 run 且归 worker: %v", leadCard)
	}
	childCard := runs[1].(map[string]any)
	if childCard["agent_profile_id"] != workerID || childCard["agent_name"] != "Worker" {
		t.Fatalf("子 run 成员应带 agent 归属: %v", childCard)
	}
	if childCard["work_item_id"] == wi.ID || childCard["work_item_id"] == "" {
		t.Fatalf("子 run 应挂子任务: %v", childCard)
	}
	if childCard["summary"] != "复现构建失败" {
		t.Fatalf("成员一行摘要应为指令摘录: %v", childCard["summary"])
	}

	// 第二批：普通消息接诊 → 独立卡片，时间线新→旧。
	second, err := s.svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: leadID, Instruction: "再看看依赖版本",
		DispatchTrigger: domain.DispatchTriggerUserMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	code, body = getDispatchesJSON(t, mux, wi.ID)
	if code != http.StatusOK {
		t.Fatalf("dispatches 应 200，实际 %d", code)
	}
	items, _ = body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("两条消息应两张卡片: %v", body)
	}
	newest := items[0].(map[string]any)
	if newest["lead_run_id"] != second.ID {
		t.Fatalf("时间线应新→旧且接诊批次带 lead_run_id: %v", newest)
	}
	if newest["id"] == card["id"] {
		t.Fatal("第二批不得与第一批同卡")
	}

	// 未知任务 404。
	if code, _ := getDispatchesJSON(t, mux, "wi_missing"); code != http.StatusNotFound {
		t.Fatalf("未知任务应 404，实际 %d", code)
	}
}
