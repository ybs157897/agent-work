package httpapi

// handlers_review_test.go 评审面端点 HTTP 契约（任务控制面 RFC §9.5/§9.6）：
// GET review-queue 的形状（items/total_count/next_cursor/generated_at）、
// phase/priority 过滤与非法参数 422；GET delivery-brief 的全字段形状与
// Chat 边界 fail closed（422，不返回 partial）。

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// pinHTTPReviewPhase 把 Task 钉进 in_progress + review 投影（fixture 播种）。
func pinHTTPReviewPhase(t *testing.T, s *Server, ctx context.Context, workItemID string, at time.Time) {
	t.Helper()
	wi, err := s.store.WorkItems().Get(ctx, workItemID)
	if err != nil {
		t.Fatal(err)
	}
	expected := wi.Version
	wi.Status = domain.WorkItemInProgress
	wi.Phase = domain.PhaseReview
	wi.PhaseEnteredAt = &at
	if err := s.store.WorkItems().Update(ctx, wi, expected); err != nil {
		t.Fatal(err)
	}
}

func TestReviewQueueEndpointContract(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID := seedCommentsHTTPEnv(t, s)
	ctx := context.Background()

	base := time.Now().UTC().Add(-2 * time.Hour)
	first, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "评审A"})
	if err != nil {
		t.Fatal(err)
	}
	pinHTTPReviewPhase(t, s, ctx, first.ID, base)
	second, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "评审B"})
	if err != nil {
		t.Fatal(err)
	}
	pinHTTPReviewPhase(t, s, ctx, second.ID, base.Add(time.Hour))
	// 执行态任务不入队。
	exec, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "执行中"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.svc.CreateRun(ctx, exec.ID, application.CreateRunParams{
		AgentProfileID: "agent_cmt_worker", Instruction: "x",
	}); err != nil {
		t.Fatal(err)
	}

	path := "/api/v1/workspaces/" + wsID + "/review-queue"
	code, body := getJSON(t, mux, path)
	if code != http.StatusOK {
		t.Fatalf("review-queue 应 200，实际 %d: %v", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("应只有 review/acceptance 两个任务: %v", body)
	}
	if total, _ := body["total_count"].(float64); total != 2 {
		t.Fatalf("total_count 应为 2: %v", body)
	}
	if body["generated_at"] == nil || body["next_cursor"] != nil {
		t.Fatalf("全量页应无 next_cursor 且带 generated_at: %v", body)
	}
	item, _ := items[0].(map[string]any)
	wi, _ := item["work_item"].(map[string]any)
	if wi["id"] != first.ID {
		t.Fatalf("pending_since ASC 应先返回最早评审: %v", items)
	}
	if _, ok := item["pending_since"].(string); !ok {
		t.Fatalf("pending_since 应为字符串时间: %v", item)
	}
	watermark, _ := item["source_watermark"].(map[string]any)
	if watermark == nil {
		t.Fatalf("source_watermark 必填: %v", item)
	}
	if seq, _ := watermark["as_of_event_seq"].(float64); seq <= 0 {
		t.Fatalf("as_of_event_seq 应 >0: %v", watermark)
	}

	// 分页：limit=1 → next_cursor 非空，续页取第二行；total_count 恒定。
	code, body = getJSON(t, mux, path+"?limit=1")
	items, _ = body["items"].([]any)
	if code != http.StatusOK || len(items) != 1 {
		t.Fatalf("limit=1 应返回 1 条: %d %v", code, body)
	}
	next, _ := body["next_cursor"].(string)
	if next == "" {
		t.Fatalf("limit=1 应有 next_cursor: %v", body)
	}
	if total, _ := body["total_count"].(float64); total != 2 {
		t.Fatalf("total_count 独立于分页: %v", body)
	}
	code, body = getJSON(t, mux, path+"?limit=1&cursor="+next)
	items, _ = body["items"].([]any)
	row, _ := items[0].(map[string]any)
	wi, _ = row["work_item"].(map[string]any)
	if code != http.StatusOK || len(items) != 1 || wi["id"] != second.ID {
		t.Fatalf("cursor 续页不符: %d %v", code, body)
	}
	if body["next_cursor"] != nil {
		t.Fatalf("尾页不应有 next_cursor: %v", body)
	}

	// 过滤与非法参数。
	code, body = getJSON(t, mux, path+"?phase=acceptance")
	if items, _ = body["items"].([]any); code != http.StatusOK || len(items) != 0 {
		t.Fatalf("phase=acceptance 过滤应为空: %d %v", code, body)
	}
	code, body = getJSON(t, mux, path+"?phase=bogus")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("非法 phase 应 422: %d %v", code, body)
	}
	code, body = getJSON(t, mux, path+"?cursor=not-a-valid-cursor")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("非法 cursor 应 422: %d %v", code, body)
	}
}

func TestDeliveryBriefEndpointContract(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID := seedCommentsHTTPEnv(t, s)
	ctx := context.Background()

	root, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "简报HTTP", AcceptanceCriteria: []string{"AC-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.svc.CreateRun(ctx, root.ID, application.CreateRunParams{
		AgentProfileID: "agent_cmt_worker", Instruction: "干",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning, domain.RunSucceeding, domain.RunSucceeded} {
		if err := s.svc.RecordRunStatus(ctx, run.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}

	code, body := getJSON(t, mux, "/api/v1/work-items/"+root.ID+"/delivery-brief")
	if code != http.StatusOK {
		t.Fatalf("delivery-brief 应 200，实际 %d: %v", code, body)
	}
	for _, key := range []string{"work_item", "acceptance_criteria", "conclusion", "attempts",
		"runs", "artifacts", "risks", "comments", "freshness", "truncation"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("brief 缺少字段 %s: %v", key, body)
		}
	}
	criteria, _ := body["acceptance_criteria"].([]any)
	if len(criteria) != 1 || criteria[0] != "AC-1" {
		t.Fatalf("acceptance_criteria 不符: %v", body)
	}
	runs, _ := body["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs 应有 1 条: %v", body)
	}
	runRow, _ := runs[0].(map[string]any)
	if _, ok := runRow["run"].(map[string]any); !ok {
		t.Fatalf("RunEvidence.run 必填: %v", runRow)
	}
	if _, ok := runRow["evidence"].([]any); !ok {
		t.Fatalf("RunEvidence.evidence 必填: %v", runRow)
	}
	freshness, _ := body["freshness"].(map[string]any)
	if freshness["state"] != "current" {
		t.Fatalf("freshness.state 应 current: %v", freshness)
	}
	if _, ok := freshness["as_of_event_seq"].(float64); !ok {
		t.Fatalf("freshness.as_of_event_seq 必填: %v", freshness)
	}
	conclusion, _ := body["conclusion"].(map[string]any)
	if _, ok := conclusion["coordinator_status"]; !ok {
		t.Fatalf("conclusion.coordinator_status 必填（可为空串）: %v", conclusion)
	}
	if _, ok := conclusion["version"].(float64); !ok {
		t.Fatalf("conclusion.version 必填: %v", conclusion)
	}

	// Chat 边界 fail closed：422，不返回 partial brief。
	chat, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "对话", RecordKind: domain.RecordKindChat, AgentProfileID: "agent_cmt_worker",
	})
	if err != nil {
		t.Fatal(err)
	}
	code, body = getJSON(t, mux, "/api/v1/work-items/"+chat.ID+"/delivery-brief")
	if code != http.StatusUnprocessableEntity || body["state"] != nil {
		t.Fatalf("chat brief 应整体 fail closed: %d %v", code, body)
	}
}
