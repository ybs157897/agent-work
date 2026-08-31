// entity_idempotency_integration_test.go 实体级幂等键（client_key）防回归：
// 同 key 重复创建返回既有实体（不重复建行、不重复分派），无 key/不同 key 行为不变。
package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// seedEntityIdemEnv 建 workspace + agent + binding + 一个任务，返回 (service, wsID, agentID, wiID)。
func seedEntityIdemEnv(t *testing.T) (*application.Service, *captureDispatcher, string, string, string) {
	t.Helper()
	ctx := context.Background()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())

	now := time.Now().UTC()
	if err := store.Workspaces().Create(ctx, &domain.Workspace{ID: "ws_ei", Name: "ei", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, "ws_ei")
	if err := store.Agents().Create(ctx, &domain.AgentProfile{
		ID: "agent_ei", WorkspaceID: "ws_ei", Name: "EI", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "codex_local"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{
		ID: "rb_ei", WorkspaceID: "ws_ei", RuntimeLabel: "codex_local", AdapterID: "codex-appserver",
		Capabilities: map[string]string{}, Status: domain.BindingReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	wi, err := svc.CreateWorkItem(ctx, "ws_ei", application.CreateWorkItemParams{Title: "宿主任务", AgentProfileID: "agent_ei"})
	if err != nil {
		t.Fatal(err)
	}
	return svc, dispatcher, "ws_ei", "agent_ei", wi.ID
}

func TestCreateWorkItemIdempotentReplay(t *testing.T) {
	svc, _, wsID, agentID, _ := seedEntityIdemEnv(t)
	ctx := context.Background()
	p := application.CreateWorkItemParams{Title: "分叉会话", AgentProfileID: agentID, ClientKey: "fork:wi_1:msg_1"}

	first, replayed, err := svc.CreateWorkItemIdempotent(ctx, wsID, p)
	if err != nil || replayed {
		t.Fatalf("首次创建应成功且非重放: wi=%v replayed=%v err=%v", first, replayed, err)
	}
	second, replayed, err := svc.CreateWorkItemIdempotent(ctx, wsID, p)
	if err != nil {
		t.Fatalf("重放应成功: %v", err)
	}
	if !replayed || second.ID != first.ID {
		t.Fatalf("同 key 应查回既有实体: first=%s second=%s replayed=%v", first.ID, second.ID, replayed)
	}

	// 无 key 与不同 key：各建各的。
	third, replayed, err := svc.CreateWorkItemIdempotent(ctx, wsID, application.CreateWorkItemParams{Title: "无 key", AgentProfileID: agentID})
	if err != nil || replayed || third.ID == first.ID {
		t.Fatalf("无 key 应独立创建: replayed=%v", replayed)
	}
	if _, _, err := svc.CreateWorkItemIdempotent(ctx, wsID, application.CreateWorkItemParams{Title: "无 key 2", AgentProfileID: agentID}); err != nil {
		t.Fatalf("两个无 key 实体不冲突: %v", err)
	}
	fourth, replayed, err := svc.CreateWorkItemIdempotent(ctx, wsID, application.CreateWorkItemParams{Title: "异 key", AgentProfileID: agentID, ClientKey: "fork:wi_1:msg_2"})
	if err != nil || replayed || fourth.ID == first.ID {
		t.Fatalf("不同 key 应独立创建: replayed=%v", replayed)
	}
}

func TestCreateRunIdempotentReplayNoDoubleDispatch(t *testing.T) {
	svc, dispatcher, _, agentID, wiID := seedEntityIdemEnv(t)
	ctx := context.Background()
	p := application.CreateRunParams{AgentProfileID: agentID, Instruction: "干活", ClientKey: "drain:wi:q0"}

	first, replayed, err := svc.CreateRunIdempotent(ctx, wiID, p)
	if err != nil || replayed {
		t.Fatalf("首次创建应成功且非重放: replayed=%v err=%v", replayed, err)
	}
	second, replayed, err := svc.CreateRunIdempotent(ctx, wiID, p)
	if err != nil {
		t.Fatalf("重放应成功: %v", err)
	}
	if !replayed || second.ID != first.ID {
		t.Fatalf("同 key 应查回既有 run: first=%s second=%s replayed=%v", first.ID, second.ID, replayed)
	}
	if len(dispatcher.runs) != 1 {
		t.Fatalf("重放不得重复分派: dispatch 次数=%d", len(dispatcher.runs))
	}
}

func TestCreateWorkItemParentValidation(t *testing.T) {
	svc, _, wsID, agentID, parentID := seedEntityIdemEnv(t)
	ctx := context.Background()

	// 合法 parent：落 parent_id（修 fork 链路 parent_id 被 DTO 丢弃的回归）。
	child, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "子任务", AgentProfileID: agentID, ParentID: parentID})
	if err != nil {
		t.Fatalf("合法 parent 应创建成功: %v", err)
	}
	if child.ParentID != parentID {
		t.Fatalf("parent_id 应持久化: 实际 %q", child.ParentID)
	}
	// 不存在的 parent：拒绝。
	if _, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "孤儿", ParentID: "wi_missing"}); err == nil {
		t.Fatal("不存在的 parent 应报校验错误")
	}
}
