package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// TestChatAndTaskRecordKindsStayOnTheirOwnProjectionSurface locks the product
// boundary at the application layer: Chat keeps ordinary Run/Session behavior,
// while Task alone owns dispatch, task state, ledger, and task wakeups.
func TestChatAndTaskRecordKindsStayOnTheirOwnProjectionSurface(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())

	chat := seedRunEnv(t, ctx, svc, store)
	// seedRunEnv already persisted the record; create a dedicated Chat row so the
	// test does not rely on mutating the historical task fixture.
	chat, err := svc.CreateWorkItem(ctx, chat.WorkspaceID, application.CreateWorkItemParams{
		Title: "独立对话", AgentProfileID: chat.AgentProfileID, RecordKind: domain.RecordKindChat,
	})
	if err != nil {
		t.Fatal(err)
	}
	other := &domain.AgentProfile{
		ID: "agent_chat_other", WorkspaceID: chat.WorkspaceID, Name: "Other", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: chat.CreatedAt, UpdatedAt: chat.CreatedAt,
	}
	if err := store.Agents().Create(ctx, other); err != nil {
		t.Fatal(err)
	}

	chatBefore, err := store.WorkItems().Get(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	chatRun, err := svc.CreateRun(ctx, chat.ID, application.CreateRunParams{
		AgentProfileID: chat.AgentProfileID,
		Instruction:    "@Other 请保持普通对话，不要创建任务",
		// HTTP 的历史入口会带这个值；Chat 必须由应用层剥离。
		DispatchTrigger: domain.DispatchTriggerUserMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if chatRun.AgentProfileID != chat.AgentProfileID {
		t.Fatalf("Chat 不应执行 @ 路由：实际 agent=%q", chatRun.AgentProfileID)
	}
	if chatRun.DispatchID != "" {
		t.Fatalf("Chat run 不应挂 dispatch：%q", chatRun.DispatchID)
	}
	if dispatches, err := store.Dispatches().ListByWorkItem(ctx, chat.ID); err != nil {
		t.Fatal(err)
	} else if len(dispatches) != 0 {
		t.Fatalf("Chat 不应创建 dispatch：%d", len(dispatches))
	}
	chatAfterCreate, err := store.WorkItems().Get(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if chatAfterCreate.Status != chatBefore.Status || chatAfterCreate.Phase != chatBefore.Phase ||
		chatAfterCreate.LockedByRunID != chatBefore.LockedByRunID || chatAfterCreate.Version != chatBefore.Version {
		t.Fatalf("Chat 建 run 不应推进任务状态/锁/version：before=%+v after=%+v", chatBefore, chatAfterCreate)
	}

	// Base session behavior remains available to Chat and is resumable across runs.
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, chatRun.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.RecordRunSessionRef(ctx, chatRun.ID, "codex://chat-thread"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, chatRun.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": "这是普通 Chat 回复"}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, chatRun.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	chatEvents, err := store.Events().ListRunEvents(ctx, chatRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	chatStarted := false
	for _, event := range chatEvents {
		if event.EventType == domain.EventRunStarted {
			chatStarted = true
			if event.Payload["record_kind"] != string(domain.RecordKindChat) {
				t.Fatalf("Chat run.started 必须携带 record_kind=chat：%#v", event.Payload)
			}
		}
	}
	if !chatStarted {
		t.Fatal("Chat 应产生 run.started 事件")
	}
	chatAfterTerminal, err := store.WorkItems().Get(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if chatAfterTerminal.Status != chatBefore.Status || chatAfterTerminal.Phase != chatBefore.Phase ||
		chatAfterTerminal.LockedByRunID != "" || chatAfterTerminal.RollingDigest != "" {
		t.Fatalf("Chat 终态不应产生任务投影：%+v", chatAfterTerminal)
	}
	secondChatRun, err := svc.CreateRun(ctx, chat.ID, application.CreateRunParams{
		AgentProfileID: chat.AgentProfileID, Instruction: "继续普通对话",
	})
	if err != nil {
		t.Fatal(err)
	}
	conversation, ok := secondChatRun.Input["conversation"].(map[string]any)
	if !ok || conversation["resume_session_ref"] != "codex://chat-thread" {
		t.Fatalf("Chat 应保留 Session resume：%#v", secondChatRun.Input["conversation"])
	}
	if err := svc.ResetTaskSession(ctx, chat.WorkspaceID, chat.AgentProfileID, "codex-appserver", chat.ID); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("Task session 管理面不应重置 Chat：%v", err)
	}

	// Task-only write surfaces must reject a Chat record, while Chat artifacts are
	// still persisted for the shared output/artifact UI (only Task gets S4 index).
	if _, err := svc.RecordDecision(ctx, chat.ID, application.RecordDecisionParams{Quote: "不应进入任务台账"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("Chat decision 应被拒绝，实际 %v", err)
	}
	if _, err := svc.SubmitPlan(ctx, chat.WorkspaceID, application.SubmitPlanParams{
		WorkItemID: chat.ID, AgentProfileID: chat.AgentProfileID,
		Steps: []application.PlanStepInput{{Verb: "finish", Payload: map[string]any{}}},
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("Chat plan 应被拒绝，实际 %v", err)
	}
	agent, err := store.Agents().Get(ctx, chat.AgentProfileID)
	if err != nil {
		t.Fatal(err)
	}
	agent.WakeOnDemand = true
	agent.UpdatedAt = chat.CreatedAt
	if err := store.Agents().Update(ctx, agent, agent.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RequestAgentWake(ctx, chat.AgentProfileID, chat.ID, "唤醒 Chat", nil); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("Chat wakeup 应被拒绝，实际 %v", err)
	}
	artifactRun, err := svc.CreateRun(ctx, chat.ID, application.CreateRunParams{
		AgentProfileID: chat.AgentProfileID, Instruction: "生成普通对话附件",
	})
	if err != nil {
		t.Fatal(err)
	}
	art := &domain.Artifact{LogicalPath: "chat-output.md", Mime: "text/markdown", Sha256: "chat-sha"}
	if err := svc.RecordArtifact(ctx, artifactRun.ID, art); err != nil {
		t.Fatal(err)
	}
	if artifacts, err := store.Runs().ListArtifacts(ctx, artifactRun.ID); err != nil {
		t.Fatal(err)
	} else if len(artifacts) != 1 {
		t.Fatalf("Chat artifact 应保留，实际 %d", len(artifacts))
	}
	if hits, err := store.Search().Search(ctx, chat.WorkspaceID, "chat-output", "", "", 20); err != nil {
		t.Fatal(err)
	} else if len(hits) != 0 {
		t.Fatalf("Chat artifact 不应进入 Task 搜索：%#v", hits)
	}

	// The same Run entrypoint remains a Task entrypoint when the record is Task.
	task, err := svc.CreateWorkItem(ctx, chat.WorkspaceID, application.CreateWorkItemParams{
		Title: "任务发布", AgentProfileID: chat.AgentProfileID, RecordKind: domain.RecordKindTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	taskRun, err := svc.CreateRun(ctx, task.ID, application.CreateRunParams{
		AgentProfileID: chat.AgentProfileID, Instruction: "普通任务发布消息",
		DispatchTrigger: domain.DispatchTriggerUserMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if taskRun.DispatchID == "" {
		t.Fatal("Task user_message 应创建 dispatch")
	}
	taskCreated, err := store.WorkItems().Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if taskCreated.Status != domain.WorkItemInProgress || taskCreated.Phase != domain.PhaseExecution {
		t.Fatalf("Task 建 run 应推进任务状态：%+v", taskCreated)
	}
	if _, err := svc.RecordDecision(ctx, task.ID, application.RecordDecisionParams{Quote: "Task 决策"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionRef(ctx, taskRun.ID, "codex://task-thread"); err != nil {
		t.Fatal(err)
	}
	settleDriveRun(t, ctx, svc, taskRun.ID, "Task 完成", true)
	taskEvents, err := store.Events().ListRunEvents(ctx, taskRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	taskStarted := false
	for _, event := range taskEvents {
		if event.EventType == domain.EventRunStarted {
			taskStarted = true
			if event.Payload["record_kind"] != string(domain.RecordKindTask) {
				t.Fatalf("Task run.started 必须携带 record_kind=task：%#v", event.Payload)
			}
		}
	}
	if !taskStarted {
		t.Fatal("Task 应产生 run.started 事件")
	}
	taskAfter, err := store.WorkItems().Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(taskAfter.RollingDigest, "Task 完成") {
		t.Fatalf("Task 应刷新 rolling ledger：%q", taskAfter.RollingDigest)
	}
	taskSessions, err := svc.TaskSessionsByAgent(ctx, chat.WorkspaceID, chat.AgentProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(taskSessions) != 1 || taskSessions[0].TaskKey != task.ID {
		t.Fatalf("Task session 管理面只能返回 Task，实际：%#v", taskSessions)
	}
}

func TestCreateWorkItemRejectsMixedRecordKindParent(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	now := time.Now().UTC()
	workspace := &domain.Workspace{ID: "ws_record_kind_parent", Name: "kind", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, "ws_record_kind_parent")
	task, err := svc.CreateWorkItem(ctx, workspace.ID, application.CreateWorkItemParams{
		Title: "Task root", RecordKind: domain.RecordKindTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	chat, err := svc.CreateWorkItem(ctx, workspace.ID, application.CreateWorkItemParams{
		Title: "Chat root", RecordKind: domain.RecordKindChat, ClientKey: "shared-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := svc.CreateWorkItemIdempotent(ctx, workspace.ID, application.CreateWorkItemParams{
		Title: "same key as task", RecordKind: domain.RecordKindTask, ClientKey: "shared-key",
	}); !errors.Is(err, domain.ErrIdempotencyConflict) || replayed {
		t.Fatalf("不同 record_kind 不得重放同一 client_key：replayed=%v err=%v", replayed, err)
	}
	for _, tc := range []struct {
		name   string
		parent string
		kind   domain.WorkItemRecordKind
	}{
		{name: "task child under chat", parent: chat.ID, kind: domain.RecordKindTask},
		{name: "chat child under task", parent: task.ID, kind: domain.RecordKindChat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateWorkItem(ctx, workspace.ID, application.CreateWorkItemParams{
				Title: "mixed", ParentID: tc.parent, RecordKind: tc.kind,
			})
			if !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("混合 record_kind 应拒绝，实际 %v", err)
			}
		})
	}
}
