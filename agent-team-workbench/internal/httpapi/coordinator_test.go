package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestCoordinatorSettingsAreCanonicalAndPromptLocked(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, _, _ := seedPlanHTTPEnv(t, s)

	get := httptest.NewRecorder()
	mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+wsID+"/coordinator", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET coordinator config = %d: %s", get.Code, get.Body.String())
	}
	var config map[string]any
	if err := json.Unmarshal(get.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"runtime_label", "model_ref", "reasoning_effort", "prompt_version", "prompt_locked", "instructions_editable", "version"} {
		if _, ok := config[key]; !ok {
			t.Fatalf("Coordinator config 缺 canonical 字段 %q: %#v", key, config)
		}
	}
	for _, key := range []string{"runtime", "model", "fallback_runtime", "fallback_model", "instructions"} {
		if _, ok := config[key]; ok {
			t.Fatalf("Coordinator config 不应暴露兼容别名/提示词 %q: %#v", key, config)
		}
	}
	if config["prompt_locked"] != true || config["instructions_editable"] != false {
		t.Fatalf("Coordinator prompt lock 元数据错误: %#v", config)
	}

	patchPrompt := httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/"+wsID+"/coordinator", strings.NewReader(`{"instructions":null,"expected_version":1}`))
	patchPrompt.Header.Set("Idempotency-Key", "coordinator-prompt-lock")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, patchPrompt)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "提示词") {
		t.Fatalf("prompt 修改应拒绝: %d %s", rec.Code, rec.Body.String())
	}

	patchMock := httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/"+wsID+"/coordinator", strings.NewReader(`{"runtime_label":"mock","expected_version":1}`))
	patchMock.Header.Set("Idempotency-Key", "coordinator-mock-rejected")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, patchMock)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "不允许 mock") {
		t.Fatalf("公开设置不得选择 mock: %d %s", rec.Code, rec.Body.String())
	}

	now := time.Now().UTC()
	if err := s.store.Bindings().Create(context.Background(), &domain.RuntimeBinding{
		ID: "rb_http_coordinator", WorkspaceID: wsID, RuntimeLabel: "codex_local",
		AdapterID: "codex-appserver", Status: domain.BindingReady, Provider: "openai", Model: "gpt-test",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/"+wsID+"/coordinator", strings.NewReader(`{"runtime_label":"codex_local","model_ref":"codex-fast","reasoning_effort":"high","expected_version":1}`))
	patch.Header.Set("Idempotency-Key", "coordinator-canonical-update")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, patch)
	if rec.Code != http.StatusOK {
		t.Fatalf("canonical Coordinator PATCH = %d: %s", rec.Code, rec.Body.String())
	}
	var updated map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated["runtime_label"] != "codex_local" || updated["model_ref"] != "codex-fast" || updated["reasoning_effort"] != "high" {
		t.Fatalf("canonical Coordinator PATCH 响应异常: %#v", updated)
	}
	persisted, err := s.store.TaskCoordinators().GetConfig(context.Background(), wsID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ModelRef.Ref != "codex-fast" || persisted.ModelRef.Provider != "" || persisted.ModelRef.Model != "" {
		t.Fatalf("model_ref 必须保持注册表引用语义，不能残留旧 provider/model 覆盖: %+v", persisted.ModelRef)
	}
	aliasPatch := httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/"+wsID+"/coordinator", strings.NewReader(`{"model":"not-supported","expected_version":2}`))
	aliasPatch.Header.Set("Idempotency-Key", "coordinator-alias-rejected")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, aliasPatch)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("兼容 model alias 应被拒绝: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCoordinatorDispatchExcerptDoesNotExposeInternalControlPayload(t *testing.T) {
	run := &domain.ExecutionRun{Input: map[string]any{
		"instruction":      "TASK_DATA_JSON_V1_LENGTH:999 secret internal payload",
		"task_coordinator": map[string]any{"role": "coordinator", "action": "recover"},
	}}
	got := runInstructionExcerpt(run)
	if got != "系统 Coordinator 正在诊断失败并重新规划" || strings.Contains(got, "TASK_DATA_JSON") {
		t.Fatalf("Coordinator dispatch 摘录应展示产品语义而非内部控制载荷: %q", got)
	}
}

func TestCoordinatorSettingsRejectRuntimeLabelAdapterMismatch(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, _, _ := seedPlanHTTPEnv(t, s)
	ctx := context.Background()
	if _, err := s.store.TaskCoordinators().EnsureConfig(ctx, wsID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.store.Bindings().Create(ctx, &domain.RuntimeBinding{
		ID: "rb_http_coordinator_mismatch", WorkspaceID: wsID,
		RuntimeLabel: "codex_local", AdapterID: "kimi-appserver",
		Status: domain.BindingReady, Provider: "kimi", Model: "kimi-test",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/"+wsID+"/coordinator",
		strings.NewReader(`{"runtime_label":"codex_local","expected_version":1}`))
	patch.Header.Set("Idempotency-Key", "coordinator-runtime-adapter-mismatch")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, patch)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "不匹配") {
		t.Fatalf("runtime label/adapter mismatch 应拒绝: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCoordinatorSnapshotRejectsChatAndAggregatesAttempts(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, agentID, _ := seedPlanHTTPEnv(t, s)
	ctx := context.Background()
	now := time.Now().UTC()
	config, err := s.store.TaskCoordinators().EnsureConfig(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	root := &domain.WorkItem{ID: "wi_http_coord_root", WorkspaceID: wsID, RecordKind: domain.RecordKindTask,
		Title: "Task", Status: domain.WorkItemTodo, Priority: domain.PriorityMedium, Version: 1, CreatedAt: now, UpdatedAt: now}
	chat := &domain.WorkItem{ID: "wi_http_coord_chat", WorkspaceID: wsID, RecordKind: domain.RecordKindChat,
		Title: "Chat", Status: domain.WorkItemTodo, Priority: domain.PriorityMedium, Version: 1, CreatedAt: now, UpdatedAt: now}
	for _, wi := range []*domain.WorkItem{root, chat} {
		if err := s.store.WorkItems().Create(ctx, wi); err != nil {
			t.Fatal(err)
		}
	}
	state := &domain.TaskCoordinatorState{ID: domain.NewID(domain.PrefixCoordinatorState), WorkspaceID: wsID,
		RootWorkItemID: root.ID, CoordinatorAgentID: config.AgentProfileID, Status: domain.CoordinatorRunning,
		Phase: "attempt", CurrentAction: "观察 Worker", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.store.TaskCoordinators().CreateState(ctx, state); err != nil {
		t.Fatal(err)
	}
	for _, event := range []*domain.TaskCoordinatorEvent{
		{ID: "coordevt_started", WorkspaceID: wsID, RootWorkItemID: root.ID, WorkItemID: root.ID,
			Kind: "coordinator.attempt.started", Summary: "Worker started", RunID: "run_attempt_1", AgentID: agentID, Attempt: 1,
			Data: map[string]any{"stage": "attempt", "status": "running", "max_attempts": 4}, OccurredAt: now},
		{ID: "coordevt_failed", WorkspaceID: wsID, RootWorkItemID: root.ID, WorkItemID: root.ID,
			Kind: "coordinator.attempt.failed", Summary: "Worker failed", RunID: "run_attempt_1", AgentID: agentID, Attempt: 1,
			Reason: "自动诊断失败", Data: map[string]any{"stage": "failure", "failure_code": "timeout", "failure_message": "worker timeout", "retryable": true, "next_action": "自动重试"}, OccurredAt: now.Add(time.Second)},
		{ID: "coordevt_user_pause", WorkspaceID: wsID, RootWorkItemID: root.ID, WorkItemID: root.ID,
			Kind: domain.EventCoordinatorBlocked, Summary: "用户暂停任务", AgentID: agentID, Attempt: 1,
			Data: map[string]any{"stage": "failure", "status": "blocked", "failure_code": "user_pause", "failure_message": "等待资料"}, OccurredAt: now.Add(2 * time.Second)},
	} {
		if err := s.store.TaskCoordinators().AppendEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/work-items/"+root.ID+"/coordinator", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("Task coordinator snapshot = %d: %s", rec.Code, rec.Body.String())
	}
	var snapshot struct {
		RootWorkItemID string `json:"root_work_item_id"`
		Attempts       []struct {
			RunID  string `json:"run_id"`
			Status string `json:"status"`
		} `json:"attempts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.RootWorkItemID != root.ID || len(snapshot.Attempts) != 1 || snapshot.Attempts[0].RunID != "run_attempt_1" || snapshot.Attempts[0].Status != "failed" {
		t.Fatalf("同一 run 的 started/failed 应聚合为一次 attempt，run-less 控制节点不得污染尝试链: %+v", snapshot)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/work-items/"+chat.ID+"/coordinator", nil))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "record_kind") {
		t.Fatalf("Chat coordinator snapshot 应 fail-closed: %d %s", rec.Code, rec.Body.String())
	}

}
