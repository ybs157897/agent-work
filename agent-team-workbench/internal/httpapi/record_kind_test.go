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

func TestWorkItemRecordKindHTTPBoundary(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, agentID, _ := seedPlanHTTPEnv(t, s)
	seedHTTPCoordinatorBinding(t, s, wsID)

	chat := postWorkItem(t, mux, wsID,
		`{"title":"普通对话","record_kind":"chat","agent_profile_id":"`+agentID+`"}`)
	if chat.Code != http.StatusCreated {
		t.Fatalf("Chat 创建应 201，实际 %d: %s", chat.Code, chat.Body.String())
	}
	var chatBody map[string]any
	if err := json.Unmarshal(chat.Body.Bytes(), &chatBody); err != nil {
		t.Fatal(err)
	}
	if chatBody["record_kind"] != "chat" {
		t.Fatalf("Chat 响应应携带 record_kind=chat: %#v", chatBody)
	}
	manualTask := postWorkItem(t, mux, wsID,
		`{"title":"错误手工指派","record_kind":"task","agent_profile_id":"`+agentID+`"}`)
	if manualTask.Code != http.StatusUnprocessableEntity || !strings.Contains(manualTask.Body.String(), "Coordinator") {
		t.Fatalf("发布 Task 不得手工选择 Agent: %d %s", manualTask.Code, manualTask.Body.String())
	}

	task := postWorkItem(t, mux, wsID,
		`{"title":"发布任务","record_kind":"task"}`)
	if task.Code != http.StatusCreated {
		t.Fatalf("Task 创建应 201，实际 %d: %s", task.Code, task.Body.String())
	}
	var taskBody map[string]any
	if err := json.Unmarshal(task.Body.Bytes(), &taskBody); err != nil {
		t.Fatal(err)
	}
	if taskBody["record_kind"] != "task" {
		t.Fatalf("Task 响应应携带 record_kind=task: %#v", taskBody)
	}
	if taskBody["status"] != string(domain.WorkItemInProgress) || taskBody["agent_profile_id"] == "" || taskBody["agent_profile_id"] == agentID {
		t.Fatalf("根 Task 应由隐藏系统 Coordinator 自动接取: %#v", taskBody)
	}
	taskID, _ := taskBody["id"].(string)
	dispatches := httptest.NewRecorder()
	mux.ServeHTTP(dispatches, httptest.NewRequest(http.MethodGet,
		"/api/v1/work-items/"+taskID+"/dispatches", nil))
	if dispatches.Code != http.StatusOK {
		t.Fatalf("Coordinator dispatch timeline 应可读: %d %s", dispatches.Code, dispatches.Body.String())
	}
	var dispatchBody struct {
		Items []struct {
			Runs []struct {
				AgentName string `json:"agent_name"`
				Summary   string `json:"summary"`
			} `json:"runs"`
		} `json:"items"`
	}
	if err := json.Unmarshal(dispatches.Body.Bytes(), &dispatchBody); err != nil {
		t.Fatal(err)
	}
	if len(dispatchBody.Items) != 1 || len(dispatchBody.Items[0].Runs) != 1 ||
		dispatchBody.Items[0].Runs[0].AgentName != "Task Coordinator" ||
		strings.Contains(dispatchBody.Items[0].Runs[0].Summary, "TASK_DATA_JSON") {
		t.Fatalf("Task 时间线应显示系统身份与产品摘要，不暴露内部控制载荷: %#v", dispatchBody.Items)
	}

	for _, tc := range []struct {
		kind string
		want string
	}{
		{kind: "chat", want: "普通对话"},
		{kind: "task", want: "发布任务"},
	} {
		req := httptest.NewRequest(http.MethodGet,
			"/api/v1/workspaces/"+wsID+"/work-items?record_kind="+tc.kind, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("record_kind=%s 列表应 200，实际 %d: %s", tc.kind, rec.Code, rec.Body.String())
		}
		var body struct {
			Items []struct {
				Title      string `json:"title"`
				RecordKind string `json:"record_kind"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Items) != 1 || body.Items[0].Title != tc.want || body.Items[0].RecordKind != tc.kind {
			t.Fatalf("record_kind=%s 列表混线: %#v", tc.kind, body.Items)
		}
	}
	defaultList := httptest.NewRecorder()
	mux.ServeHTTP(defaultList, httptest.NewRequest(http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/work-items", nil))
	if defaultList.Code != http.StatusOK {
		t.Fatalf("缺省任务列表应 200，实际 %d: %s", defaultList.Code, defaultList.Body.String())
	}
	var defaultBody struct {
		Items []struct {
			Title      string `json:"title"`
			RecordKind string `json:"record_kind"`
		} `json:"items"`
	}
	if err := json.Unmarshal(defaultList.Body.Bytes(), &defaultBody); err != nil {
		t.Fatal(err)
	}
	if len(defaultBody.Items) != 1 || defaultBody.Items[0].Title != "发布任务" || defaultBody.Items[0].RecordKind != "task" {
		t.Fatalf("缺省列表必须 task-only: %#v", defaultBody.Items)
	}

	badList := httptest.NewRecorder()
	mux.ServeHTTP(badList, httptest.NewRequest(http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/work-items?record_kind=other", nil))
	if badList.Code != http.StatusUnprocessableEntity || !strings.Contains(badList.Body.String(), "record_kind") {
		t.Fatalf("非法列表 record_kind 应 422 且说明字段，实际 %d: %s", badList.Code, badList.Body.String())
	}
	badCreate := postWorkItem(t, mux, wsID,
		`{"title":"非法类型","record_kind":"other","agent_profile_id":"`+agentID+`"}`)
	if badCreate.Code != http.StatusUnprocessableEntity || !strings.Contains(badCreate.Body.String(), "record_kind") {
		t.Fatalf("非法创建 record_kind 应 422 且说明字段，实际 %d: %s", badCreate.Code, badCreate.Body.String())
	}

	bootstrap := httptest.NewRecorder()
	mux.ServeHTTP(bootstrap, httptest.NewRequest(http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/bootstrap", nil))
	if bootstrap.Code != http.StatusOK {
		t.Fatalf("bootstrap 应 200，实际 %d: %s", bootstrap.Code, bootstrap.Body.String())
	}
	var boot struct {
		WorkItems struct {
			Items []struct {
				Title      string `json:"title"`
				RecordKind string `json:"record_kind"`
			} `json:"items"`
		} `json:"work_items"`
	}
	if err := json.Unmarshal(bootstrap.Body.Bytes(), &boot); err != nil {
		t.Fatal(err)
	}
	if len(boot.WorkItems.Items) != 1 || boot.WorkItems.Items[0].Title != "发布任务" ||
		boot.WorkItems.Items[0].RecordKind != "task" {
		t.Fatalf("bootstrap 只能携带 Task: %#v", boot.WorkItems.Items)
	}
}

func TestPublicTaskCreateDefaultsToTaskAndRejectsUnsafeInitialState(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, agentID, _ := seedPlanHTTPEnv(t, s)
	seedHTTPCoordinatorBinding(t, s, wsID)

	// Omitting record_kind is the public Task contract. It must still enter the
	// Coordinator path, and a caller cannot smuggle in a worker assignment.
	defaultTask := postWorkItem(t, mux, wsID, `{"title":"缺省类型任务"}`)
	if defaultTask.Code != http.StatusCreated {
		t.Fatalf("缺省 record_kind 的 Task 创建应 201，实际 %d: %s", defaultTask.Code, defaultTask.Body.String())
	}
	var defaultBody map[string]any
	if err := json.Unmarshal(defaultTask.Body.Bytes(), &defaultBody); err != nil {
		t.Fatal(err)
	}
	if defaultBody["record_kind"] != string(domain.RecordKindTask) || defaultBody["agent_profile_id"] == "" {
		t.Fatalf("缺省 record_kind 必须归一为 Coordinator-owned Task: %#v", defaultBody)
	}

	manualDefaultTask := postWorkItem(t, mux, wsID,
		`{"title":"缺省类型手工指派","agent_profile_id":"`+agentID+`"}`)
	if manualDefaultTask.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(manualDefaultTask.Body.String(), "Coordinator") {
		t.Fatalf("省略 record_kind 的 Task 也不得手工指派: %d %s", manualDefaultTask.Code, manualDefaultTask.Body.String())
	}

	for _, status := range []string{"in_progress", "completed", "cancelled", "blocked"} {
		rec := postWorkItem(t, mux, wsID,
			`{"title":"非法初始状态","status":"`+status+`"}`)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "status") {
			t.Fatalf("根 Task 初始 status=%s 应 fail-closed 422: %d %s", status, rec.Code, rec.Body.String())
		}
	}

	// Chat keeps its explicit legacy creation semantics: opting into chat may
	// still preserve a caller-provided non-todo status.
	chat := postWorkItem(t, mux, wsID,
		`{"title":"保留旧语义的 Chat","record_kind":"chat","status":"in_progress","agent_profile_id":"`+agentID+`"}`)
	if chat.Code != http.StatusCreated {
		t.Fatalf("显式 Chat 的旧 status 语义不应被 Task 校验拦截: %d %s", chat.Code, chat.Body.String())
	}
	var chatBody map[string]any
	if err := json.Unmarshal(chat.Body.Bytes(), &chatBody); err != nil {
		t.Fatal(err)
	}
	if chatBody["record_kind"] != string(domain.RecordKindChat) || chatBody["status"] != "in_progress" {
		t.Fatalf("Chat status 应原样保留: %#v", chatBody)
	}
}

func seedHTTPCoordinatorBinding(t *testing.T, s *Server, wsID string) {
	t.Helper()
	if _, err := s.store.Bindings().GetByLabel(context.Background(), wsID, "mock"); err == nil {
		return
	}
	now := time.Now().UTC()
	if err := s.store.Bindings().Create(context.Background(), &domain.RuntimeBinding{
		ID: "rb_http_coordinator_mock", WorkspaceID: wsID, RuntimeLabel: "mock", AdapterID: "mock",
		Provider: "mock", Model: "mock", Status: domain.BindingReady,
		Capabilities: map[string]string{"resume": "supported"},
		Version:      1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatedChildAcceptHTTPIsRejected(t *testing.T) {
	ctx := context.Background()
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, _, _ := seedPlanHTTPEnv(t, s)
	seedHTTPCoordinatorBinding(t, s, wsID)

	rootRec := postWorkItem(t, mux, wsID, `{"title":"根任务","record_kind":"task"}`)
	if rootRec.Code != http.StatusCreated {
		t.Fatalf("根 Task 创建失败: %d %s", rootRec.Code, rootRec.Body.String())
	}
	var rootBody map[string]any
	if err := json.Unmarshal(rootRec.Body.Bytes(), &rootBody); err != nil {
		t.Fatal(err)
	}
	rootID, _ := rootBody["id"].(string)
	if rootID == "" {
		t.Fatalf("根 Task 响应缺少 id: %#v", rootBody)
	}

	now := time.Now().UTC()
	child := &domain.WorkItem{
		ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: wsID,
		RecordKind: domain.RecordKindTask, ParentID: rootID, Title: "子任务",
		Status: domain.WorkItemInProgress, Phase: domain.PhaseAcceptance,
		Priority: domain.PriorityMedium, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.WorkItems().Create(ctx, child); err != nil {
		t.Fatal(err)
	}

	acceptReq := httptest.NewRequest(http.MethodPost,
		"/api/v1/work-items/"+child.ID+"/commands/accept", strings.NewReader(`{}`))
	acceptReq.Header.Set("Idempotency-Key", "accept-coordinated-child")
	accept := httptest.NewRecorder()
	mux.ServeHTTP(accept, acceptReq)
	if accept.Code != http.StatusUnprocessableEntity || !strings.Contains(accept.Body.String(), "coordinated child") {
		t.Fatalf("coordinated child 验收应由 HTTP fail-closed 422: %d %s", accept.Code, accept.Body.String())
	}
}

func TestChatCannotUseTaskMetaHTTPEndpoints(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, agentID, _ := seedPlanHTTPEnv(t, s)
	chat := postWorkItem(t, mux, wsID,
		`{"title":"普通对话","record_kind":"chat","agent_profile_id":"`+agentID+`"}`)
	if chat.Code != http.StatusCreated {
		t.Fatalf("Chat 创建失败: %d %s", chat.Code, chat.Body.String())
	}
	var chatBody map[string]any
	if err := json.Unmarshal(chat.Body.Bytes(), &chatBody); err != nil {
		t.Fatal(err)
	}
	chatID, _ := chatBody["id"].(string)
	if chatID == "" {
		t.Fatalf("Chat 响应缺少 id: %#v", chatBody)
	}
	// 构造非空台账字段，详情也不得把任务摘要泄漏给 Chat。
	if err := s.store.WorkItems().UpdateRollingDigest(context.Background(), chatID, "不应出现在 Chat 详情", 1); err != nil {
		t.Fatal(err)
	}
	detail := httptest.NewRecorder()
	mux.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/v1/work-items/"+chatID, nil))
	if detail.Code != http.StatusOK || strings.Contains(detail.Body.String(), "rolling_digest") {
		t.Fatalf("Chat 详情不得携带 rolling_digest: %d %s", detail.Code, detail.Body.String())
	}

	getPaths := []string{
		"/api/v1/work-items/" + chatID + "/dispatches",
		"/api/v1/work-items/" + chatID + "/plan",
		"/api/v1/work-items/" + chatID + "/tree",
		"/api/v1/work-items/" + chatID + "/decisions",
	}
	for _, path := range getPaths {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "record_kind") {
			t.Fatalf("Chat GET %s 应被 record_kind 拒绝: %d %s", path, rec.Code, rec.Body.String())
		}
	}

	planReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+wsID+"/plans", strings.NewReader(
		`{"work_item_id":"`+chatID+`","agent_profile_id":"`+agentID+`","steps":[{"verb":"finish"}]}`))
	planReq.Header.Set("Idempotency-Key", "chat-plan-rejected")
	planRec := httptest.NewRecorder()
	mux.ServeHTTP(planRec, planReq)
	if planRec.Code != http.StatusBadRequest || !strings.Contains(planRec.Body.String(), "record_kind") {
		t.Fatalf("Chat POST plan 应被 record_kind 拒绝: %d %s", planRec.Code, planRec.Body.String())
	}

	decisionReq := httptest.NewRequest(http.MethodPost, "/api/v1/work-items/"+chatID+"/decisions", strings.NewReader(`{"quote":"不应写入"}`))
	decisionReq.Header.Set("Idempotency-Key", "chat-decision-rejected")
	decisionRec := httptest.NewRecorder()
	mux.ServeHTTP(decisionRec, decisionReq)
	if decisionRec.Code != http.StatusUnprocessableEntity || !strings.Contains(decisionRec.Body.String(), "record_kind") {
		t.Fatalf("Chat POST decision 应被 record_kind 拒绝: %d %s", decisionRec.Code, decisionRec.Body.String())
	}
}

func TestTaskSessionHTTPStaysTaskOnlyAndChatResetIsRejected(t *testing.T) {
	ctx := context.Background()
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, agentID, _ := seedPlanHTTPEnv(t, s)
	chat, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "普通对话", AgentProfileID: agentID, RecordKind: domain.RecordKindChat,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "任务会话", AgentProfileID: agentID, RecordKind: domain.RecordKindTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, session := range []*domain.TaskSession{
		{ID: domain.NewID(domain.PrefixTaskSess), WorkspaceID: wsID, AgentProfileID: agentID,
			AdapterID: "mock", TaskKey: chat.ID, SessionParams: map[string]any{"__ref": "chat://session"},
			CreatedAt: now, UpdatedAt: now},
		{ID: domain.NewID(domain.PrefixTaskSess), WorkspaceID: wsID, AgentProfileID: agentID,
			AdapterID: "mock", TaskKey: task.ID, SessionParams: map[string]any{"__ref": "task://session"},
			CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.store.TaskSessions().Upsert(ctx, session); err != nil {
			t.Fatal(err)
		}
	}

	list := httptest.NewRecorder()
	mux.ServeHTTP(list, httptest.NewRequest(http.MethodGet,
		"/api/v1/agent-profiles/"+agentID+"/task-sessions", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("Task session 列表应 200，实际 %d: %s", list.Code, list.Body.String())
	}
	var listed struct {
		Items []struct {
			TaskKey string `json:"task_key"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 || listed.Items[0].TaskKey != task.ID {
		t.Fatalf("Task session 列表不得返回 Chat anchor: %#v", listed.Items)
	}

	reset := httptest.NewRequest(http.MethodPost,
		"/api/v1/agent-profiles/"+agentID+"/task-sessions/reset",
		strings.NewReader(`{"task_key":"`+chat.ID+`","adapter_id":"mock"}`))
	reset.Header.Set("Idempotency-Key", "chat-session-reset-rejected")
	resetRec := httptest.NewRecorder()
	mux.ServeHTTP(resetRec, reset)
	if resetRec.Code != http.StatusUnprocessableEntity || !strings.Contains(resetRec.Body.String(), "record_kind") {
		t.Fatalf("Chat reset 应返回 422 且说明 record_kind，实际 %d: %s", resetRec.Code, resetRec.Body.String())
	}
	anchor, err := s.store.TaskSessions().Get(ctx, wsID, agentID, "mock", chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if anchor.SessionRef() != "chat://session" || anchor.SessionParams["__cleared_reason"] != nil {
		t.Fatalf("Chat reset 失败后不得写墓碑: %+v", anchor)
	}
}
