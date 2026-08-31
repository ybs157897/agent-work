package httpapi

// handlers_comments_test.go 评论端点 HTTP 契约（任务控制面 RFC §9.4/§9.7）：
// GET 分页与 cursor 校验、POST 201/幂等重放、comment 族错误码映射。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// seedCommentsHTTPEnv 准备带系统 Coordinator 的 workspace（mock runtime 可用）。
func seedCommentsHTTPEnv(t *testing.T, s *Server) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_cmt_http", Name: "comments", Timezone: "UTC",
		Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := application.SeedWorkspaceLocation(ctx, s.store, ws.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Bindings().Create(ctx, &domain.RuntimeBinding{
		ID: "rb_cmt_mock", WorkspaceID: ws.ID, RuntimeLabel: "mock", AdapterID: "mock",
		Provider: "mock", Model: "mock", Status: domain.BindingReady,
		Capabilities: map[string]string{"resume": "supported"},
		Version:      1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	// 自动接取要求花名册存在已启用的普通 Agent（coordinatorWorkerRoster）。
	if err := s.store.Agents().Create(ctx, &domain.AgentProfile{
		ID: "agent_cmt_worker", WorkspaceID: ws.ID, Name: "Worker", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return ws.ID
}

func TestTaskCommentEndpointsContract(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID := seedCommentsHTTPEnv(t, s)
	ctx := context.Background()

	root, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "评论任务", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "历史任务"})
	if err != nil {
		t.Fatal(err)
	}
	// 等首轮 Coordinator Run 落定后回到 waiting_user，评论可控。
	runs, err := s.store.Runs().ListByWorkItem(ctx, root.ID)
	if err != nil || len(runs) == 0 {
		t.Fatalf("前置：自动接取应创建 Coordinator Run: %v", err)
	}

	base := "/api/v1/work-items/" + root.ID + "/comments"

	// POST note → 201，body 回显、revision=1。
	code, body := postCommand(t, mux, base, `{"kind":"note","body":"只是备注","client_key":"c1"}`, "cmt-1")
	if code != http.StatusCreated {
		t.Fatalf("POST note 应 201，实际 %d: %v", code, body)
	}
	if body["revision"].(float64) != 1 || body["kind"] != "note" || body["body"] != "只是备注" {
		t.Fatalf("POST 响应字段不符: %v", body)
	}
	if _, ok := body["source_run_id"]; ok {
		t.Fatalf("空 source 字段不应出现（omitempty）: %v", body)
	}

	// 同 Idempotency-Key 重放：原 comment/revision + Idempotent-Replayed。
	req := httptest.NewRequest(http.MethodPost, base, strings.NewReader(`{"kind":"note","body":"只是备注","client_key":"c1"}`))
	req.Header.Set("Idempotency-Key", "cmt-1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("同 Idempotency-Key 应重放原响应: %d %s", rec.Code, rec.Body.String())
	}
	var replayed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &replayed)
	if replayed["id"] != body["id"] || replayed["revision"].(float64) != 1 {
		t.Fatalf("重放应返回原 comment/revision: %v vs %v", replayed, body)
	}

	// POST requirement → 201 revision=2（note 不触发联动，requirement 在
	// waiting_user 时才回 queued；此处首轮 Run 仍 queued→状态保持，不校验联动）。
	code, body = postCommand(t, mux, base,
		`{"kind":"requirement","body":"请补充测试","expected_work_item_version":`+
			fmt.Sprint(root.Version)+"}", "cmt-2")
	if code != http.StatusCreated {
		t.Fatalf("POST requirement 应 201，实际 %d: %v", code, body)
	}
	if body["revision"].(float64) != 2 {
		t.Fatalf("revision 应=2: %v", body)
	}

	// 错误码契约（§9.7 comment 族）。
	code, body = postCommand(t, mux, base, `{"kind":"review_feedback","body":"伪造"}`, "cmt-3")
	if code != http.StatusUnprocessableEntity || body["code"] != "comment_kind_invalid" {
		t.Fatalf("伪造 review_feedback 应 422 comment_kind_invalid: %d %v", code, body)
	}
	code, body = postCommand(t, mux, base, `{"kind":"note","body":"  "}`, "cmt-4")
	if code != http.StatusUnprocessableEntity || body["code"] != "comment_body_empty" {
		t.Fatalf("空正文应 422 comment_body_empty: %d %v", code, body)
	}
	code, body = postCommand(t, mux, base, `{"kind":"note","body":"`+strings.Repeat("x", 16385)+`"}`, "cmt-5")
	if code != http.StatusRequestEntityTooLarge || body["code"] != "comment_body_too_large" {
		t.Fatalf("超长正文应 413 comment_body_too_large: %d %v", code, body)
	}
	code, body = postCommand(t, mux, "/api/v1/work-items/"+legacy.ID+"/comments", `{"kind":"note","body":"x"}`, "cmt-6")
	if code != http.StatusConflict || body["code"] != "comment_coordinator_required" {
		t.Fatalf("历史任务应 409 comment_coordinator_required: %d %v", code, body)
	}
	code, body = getJSON(t, mux, "/api/v1/work-items/"+legacy.ID+"/comments")
	if code != http.StatusConflict || body["code"] != "comment_coordinator_required" {
		t.Fatalf("历史任务 GET 应 409 comment_coordinator_required: %d %v", code, body)
	}
	code, body = getJSON(t, mux, base+"?after_revision=-1")
	if code != http.StatusBadRequest || body["code"] != "comment_cursor_invalid" {
		t.Fatalf("非法 cursor 应 400 comment_cursor_invalid: %d %v", code, body)
	}

	// GET 分页：items/next_revision/latest_revision。
	code, body = getJSON(t, mux, base+"?limit=1")
	if code != http.StatusOK {
		t.Fatalf("GET 应 200: %d %v", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("limit=1 应返回 1 条: %v", body)
	}
	if next, ok := body["next_revision"].(float64); !ok || next != 1 {
		t.Fatalf("next_revision 应等于本页最大 revision=1: %v", body)
	}
	if latest, ok := body["latest_revision"].(float64); !ok || latest != 2 {
		t.Fatalf("latest_revision 应=2: %v", body)
	}
	code, body = getJSON(t, mux, base+"?after_revision=1")
	items, _ = body["items"].([]any)
	if code != http.StatusOK || len(items) != 1 || body["next_revision"] != nil {
		t.Fatalf("尾页应为 1 条且 next_revision 为空: %d %v", code, body)
	}
}
