// tools_test.go MCP 工具面集成测试（F5）：夹具照 internal/httpapi/idempotency_test.go
// 的临时 sqlite + 全量迁移模式。断言：只读工具返回种子实体、run_events_tail
// 截断与顺序、approval_list 空跑不炸、注册表红线（无 approval_resolve /
// work_item_create / session_reset）、task_claim 对非 todo / 已指派报错，并覆盖
// Goal/Todo/Receipt/Quota 治理查询和 Todo claim/release 的 workspace 作用域。
package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/migtest"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// openTestDB 临时文件 sqlite + 全量迁移（动态发现 migrations/*.sql，新增迁移免同步清单；
// 历史版本与幂等性由 cmd/migrate 的守卫测试兜底）。
func openTestDB(t *testing.T) *sqlstore.Store {
	t.Helper()
	db, err := sql.Open("sqlite",
		filepath.Join(t.TempDir(), "test.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	return sqlstore.New(db)
}

// newTestDeps 构造与生产同形的 toolDeps（stderr no-op dispatcher/notifier）。
func newTestDeps(t *testing.T) *toolDeps {
	t.Helper()
	store := openTestDB(t)
	svc := application.NewService(store, stderrDispatcher{}, stderrNotifier{}, atwruntime.NewRegistry())
	return &toolDeps{store: store, svc: svc}
}

// seedBoard 建 workspace + agent，返回 ids。
func seedBoard(t *testing.T, ctx context.Context, d *toolDeps) (wsID, agentID string) {
	t.Helper()
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_mcp", Name: "mcp", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := d.store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	// Run 创建必须冻结 context snapshot（RFC §4.6）：补默认 Location。
	if _, err := application.SeedWorkspaceLocation(ctx, d.store, ws.ID); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{
		ID: "agent_mcp", WorkspaceID: ws.ID, Name: "MCP", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := d.store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	return ws.ID, agent.ID
}

// callTool 按名字从注册表拿 handler 直接调用（不经 stdio 往返）。
func callTool(t *testing.T, ctx context.Context, d *toolDeps, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	for _, td := range toolDefs(d) {
		if td.name != name {
			continue
		}
		req := mcp.CallToolRequest{}
		req.Params.Arguments = args
		res, err := td.handler(ctx, req)
		if err != nil {
			t.Fatalf("tool %s 返回 Go error（应转 tool error）: %v", name, err)
		}
		return res
	}
	t.Fatalf("工具 %s 不在注册表", name)
	return nil
}

// resultJSON 解析工具输出的 JSON 文本 content。
func resultJSON(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatalf("结果缺 content: %#v", res)
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] 不是 TextContent: %#v", res.Content[0])
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("输出不是 JSON 对象: %q (%v)", text.Text, err)
	}
	return out
}

func requireNoError(t *testing.T, res *mcp.CallToolResult, tool string) map[string]any {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool %s 意外报错: %#v", tool, res.Content)
	}
	return resultJSON(t, res)
}

func structuredResultJSON(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res.StructuredContent == nil {
		t.Fatalf("结果缺 structuredContent: %#v", res)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("structuredContent 序列化失败: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("structuredContent 不是 JSON 对象: %q (%v)", b, err)
	}
	return out
}

func assertEmptyJSONArrayInResults(t *testing.T, res *mcp.CallToolResult, tool, field string) {
	t.Helper()
	for _, result := range []struct {
		name string
		body map[string]any
	}{
		{name: "text", body: resultJSON(t, res)},
		{name: "structured", body: structuredResultJSON(t, res)},
	} {
		value, ok := result.body[field].([]any)
		if !ok || len(value) != 0 {
			t.Fatalf("tool %s %s.%s must be [] (not null): %#v", tool, result.name, field, result.body)
		}
	}
}

// TestTaskListAndGetReturnSeedEntity task_list / task_get 返回种子实体。
func TestTaskListAndGetReturnSeedEntity(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	wsID, _ := seedBoard(t, ctx, d)

	first, err := d.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "MCP 只读面"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "第二个任务"}); err != nil {
		t.Fatal(err)
	}

	got := requireNoError(t, callTool(t, ctx, d, "task_list", map[string]any{"workspace_id": wsID}), "task_list")
	items, ok := got["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("task_list 应返回 2 个种子任务: %#v", got)
	}
	// 同秒创建时 created_at 相同、次序不保证，按标题集合断言。
	titles := map[string]bool{}
	for _, it := range items {
		titles[it.(map[string]any)["title"].(string)] = true
	}
	if !titles["MCP 只读面"] || !titles["第二个任务"] {
		t.Fatalf("task_list 应包含两个种子标题: %#v", titles)
	}

	one := requireNoError(t, callTool(t, ctx, d, "task_get", map[string]any{"workspace_id": wsID, "work_item_id": first.ID}), "task_get")
	if one["id"] != first.ID || one["status"] != string(domain.WorkItemTodo) {
		t.Fatalf("task_get 应返回种子实体: %#v", one)
	}

	// status 过滤命中 / 非法枚举报错。
	got = requireNoError(t, callTool(t, ctx, d, "task_list",
		map[string]any{"workspace_id": wsID, "status": "todo"}), "task_list")
	if len(got["items"].([]any)) != 2 {
		t.Fatalf("status=todo 应命中 2 条: %#v", got)
	}
	if res := callTool(t, ctx, d, "task_list", map[string]any{"workspace_id": wsID, "status": "done"}); !res.IsError {
		t.Fatalf("非法 status 应报 tool error: %#v", res)
	}

	// 不存在的任务 → tool error（含未找到语义）。
	if res := callTool(t, ctx, d, "task_get", map[string]any{"workspace_id": wsID, "work_item_id": "wi_none"}); !res.IsError {
		t.Fatalf("task_get 不存在任务应报错: %#v", res)
	}
}

func TestMCPTaskAndRunDTOsUseSnakeCaseAndHideSessionMaterial(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	wsID, agentID := seedBoard(t, ctx, d)
	task, err := d.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "DTO contract"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.svc.CreateRun(ctx, task.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "inspect DTO"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.svc.RecordRunSessionRef(ctx, run.ID, "provider-private-session"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		args       map[string]any
		collection string
	}{
		{name: "task_get", args: map[string]any{"workspace_id": wsID, "work_item_id": task.ID}},
		{name: "run_get", args: map[string]any{"workspace_id": wsID, "run_id": run.ID}},
	} {
		result := requireNoError(t, callTool(t, ctx, d, tc.name, tc.args), tc.name)
		for key := range result {
			if key == "session_ref" || key == "session_before" || key == "session_after" || key == "input" {
				t.Fatalf("%s must not expose private session/input material: %#v", tc.name, result)
			}
			if key != "failure" && key != "created_at" && key != "updated_at" &&
				(key != "finished_at" && key != "due_date" && key != "phase_entered_at") &&
				key != "acceptance_criteria" && key != "parent_id" && key != "phase" &&
				key != "agent_profile_id" && key != "runtime_label" && key != "adapter_id" &&
				key != "provider" && key != "capability_snapshot_id" && key != "context_snapshot_id" &&
				key != "usage_in" && key != "usage_out" && key != "usage_cached" && key != "usage_basis" &&
				key != "error_family" && key != "client_key" && key != "dispatch_id" && key != "progress" &&
				key != "retry_of" && key != "version" && key != "id" && key != "workspace_id" &&
				key != "work_item_id" && key != "record_kind" && key != "title" && key != "description" &&
				key != "status" && key != "priority" && key != "locked_by_run_id" &&
				key != "locked_at" && key != "rolling_digest" {
				t.Fatalf("%s has an undocumented DTO field %q: %#v", tc.name, key, result)
			}
		}
	}
}

func TestTaskToolsRejectChatRecords(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	wsID, agentID := seedBoard(t, ctx, d)

	chat, err := d.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "MCP Chat only", AgentProfileID: agentID, RecordKind: domain.RecordKindChat,
	})
	if err != nil {
		t.Fatal(err)
	}
	chatRun, err := d.svc.CreateRun(ctx, chat.ID, application.CreateRunParams{
		AgentProfileID: agentID, Instruction: "普通 Chat run",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := d.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "MCP Task"})
	if err != nil {
		t.Fatal(err)
	}

	list := requireNoError(t, callTool(t, ctx, d, "task_list", map[string]any{"workspace_id": wsID}), "task_list")
	items, ok := list["items"].([]any)
	if !ok || len(items) != 1 || items[0].(map[string]any)["title"] != task.Title {
		t.Fatalf("task_list 不得混入 Chat：%#v", list)
	}

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "task_get", args: map[string]any{"workspace_id": wsID, "work_item_id": chat.ID}},
		{name: "run_list", args: map[string]any{"workspace_id": wsID, "work_item_id": chat.ID}},
		{name: "run_get", args: map[string]any{"workspace_id": wsID, "run_id": chatRun.ID}},
		{name: "run_events_tail", args: map[string]any{"workspace_id": wsID, "run_id": chatRun.ID}},
		{name: "approval_list", args: map[string]any{"workspace_id": wsID, "run_id": chatRun.ID}},
		{name: "task_claim", args: map[string]any{"workspace_id": wsID, "work_item_id": chat.ID, "agent_id": agentID, "expected_version": 0}},
		{name: "task_return", args: map[string]any{"workspace_id": wsID, "work_item_id": chat.ID, "expected_version": 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if res := callTool(t, ctx, d, tc.name, tc.args); !res.IsError {
				t.Fatalf("Chat 不得通过 MCP %s：%#v", tc.name, res)
			}
		})
	}
}

// seedRunEvents 建 wi + run 并直插 n 条 run 事件（CreateRun 自带 1 条 run.created，
// 共 n+1 条、run_seq 1..n+1）。
func seedRunEvents(t *testing.T, ctx context.Context, d *toolDeps, wsID, agentID string, n int) string {
	t.Helper()
	wi, err := d.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "带事件的run"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "干活"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		evType := domain.EventRunStatusChanged
		if i%2 == 0 {
			evType = domain.EventRunProgressUpdated
		}
		err := d.store.InTx(ctx, func(ctx context.Context) error {
			ev, err := domain.NewCanonicalEvent(wsID, evType, domain.AggregateExecutionRun, run.ID, 1, nil)
			if err != nil {
				return err
			}
			_, err = d.store.Events().Append(ctx, ev, &application.RunEventRecord{
				RunID: run.ID, EventType: evType, Payload: map[string]any{"i": i},
			})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return run.ID
}

// TestRunEventsTailLimitAndOrder 尾部窗口截断 + 按 run_seq 正序返回。
func TestRunEventsTailLimitAndOrder(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	wsID, agentID := seedBoard(t, ctx, d)
	runID := seedRunEvents(t, ctx, d, wsID, agentID, 4) // run_seq 1..5

	got := requireNoError(t, callTool(t, ctx, d, "run_events_tail",
		map[string]any{"workspace_id": wsID, "run_id": runID, "limit": 2}), "run_events_tail")
	events := got["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("limit=2 应只返回尾部 2 条: %#v", got)
	}
	// 共 5 条（run.created + i=0..3），尾部 = run_seq 4,5（i=2,3），正序 i=2 在前。
	if events[0].(map[string]any)["data"].(map[string]any)["i"].(float64) != 2 ||
		events[1].(map[string]any)["data"].(map[string]any)["i"].(float64) != 3 {
		t.Fatalf("尾部窗口顺序/取值不对: %#v", events)
	}
	slim := events[0].(map[string]any)
	if _, ok := slim["event_type"]; !ok {
		t.Fatalf("精简投影缺 event_type: %#v", slim)
	}
	if _, ok := slim["occurred_at"]; !ok {
		t.Fatalf("精简投影缺 occurred_at: %#v", slim)
	}
	if len(slim) != 3 {
		t.Fatalf("run_events_tail 应只带 event_type/occurred_at/data 三字段: %#v", slim)
	}

	// 缺省 limit=50 → 全量 5 条正序（首条 run.created，其后 i=0..3）。
	got = requireNoError(t, callTool(t, ctx, d, "run_events_tail", map[string]any{"workspace_id": wsID, "run_id": runID}), "run_events_tail")
	events = got["events"].([]any)
	if len(events) != 5 {
		t.Fatalf("缺省 limit 应返回全部 5 条: %#v", got)
	}
	if events[0].(map[string]any)["event_type"] != domain.EventRunCreated {
		t.Fatalf("全量首条应为 run.created: %#v", events[0])
	}
	if events[1].(map[string]any)["data"].(map[string]any)["i"].(float64) != 0 {
		t.Fatalf("全量应按 run_seq 正序: %#v", events[1])
	}
	if events[4].(map[string]any)["data"].(map[string]any)["i"].(float64) != 3 {
		t.Fatalf("全量末条应为 i=3: %#v", events[4])
	}
}

// TestApprovalListEmptyRunNotBlow 空跑（无审批）返回空数组且不报错。
func TestApprovalListEmptyRunNotBlow(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	wsID, agentID := seedBoard(t, ctx, d)
	runID := seedRunEvents(t, ctx, d, wsID, agentID, 0)

	got := requireNoError(t, callTool(t, ctx, d, "approval_list", map[string]any{"workspace_id": wsID, "run_id": runID}), "approval_list")
	if approvals, ok := got["approvals"].([]any); !ok || len(approvals) != 0 {
		t.Fatalf("空跑应返回空数组（非 null）: %#v", got)
	}
}

// TestToolRegistryRedLine 红线回归：注册表不得出现审批决议 / 看板结构变更 / 会话重置。
func TestToolRegistryRedLine(t *testing.T) {
	d := newTestDeps(t)
	defs := toolDefs(d)
	seen := map[string]bool{}
	for _, td := range defs {
		if seen[td.name] {
			t.Fatalf("工具名重复注册: %s", td.name)
		}
		seen[td.name] = true
	}
	for _, forbidden := range forbiddenToolNames {
		if seen[forbidden] {
			t.Fatalf("红线违规：工具 %s 出现在注册表", forbidden)
		}
	}
	if len(seen) != 25 {
		t.Fatalf("工具表应恰为 25 个（20 只读 + 5 写面），实际 %d: %v", len(seen), seen)
	}
}

func TestLegacyTaskRunApprovalToolsRequireWorkspaceScope(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	wsID, agentID := seedBoard(t, ctx, d)
	wi, err := d.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "workspace scope"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "scope"})
	if err != nil {
		t.Fatal(err)
	}

	missingWorkspace := []struct {
		name string
		args map[string]any
	}{
		{name: "task_get", args: map[string]any{"work_item_id": wi.ID}},
		{name: "run_list", args: map[string]any{"work_item_id": wi.ID}},
		{name: "run_get", args: map[string]any{"run_id": run.ID}},
		{name: "run_events_tail", args: map[string]any{"run_id": run.ID}},
		{name: "approval_list", args: map[string]any{"run_id": run.ID}},
		{name: "task_claim", args: map[string]any{"work_item_id": wi.ID, "agent_id": agentID, "expected_version": 0}},
		{name: "task_return", args: map[string]any{"work_item_id": wi.ID, "expected_version": 0}},
	}
	for _, tc := range missingWorkspace {
		t.Run(tc.name+"/missing_workspace", func(t *testing.T) {
			if res := callTool(t, ctx, d, tc.name, tc.args); !res.IsError {
				t.Fatalf("%s 缺 workspace_id 必须报错: %#v", tc.name, res)
			}
		})
	}

	wrongWorkspace := map[string]any{"workspace_id": "ws_other"}
	for _, tc := range missingWorkspace {
		args := make(map[string]any, len(tc.args)+1)
		for key, value := range tc.args {
			args[key] = value
		}
		for key, value := range wrongWorkspace {
			args[key] = value
		}
		t.Run(tc.name+"/wrong_workspace", func(t *testing.T) {
			if res := callTool(t, ctx, d, tc.name, args); !res.IsError {
				t.Fatalf("%s 跨 workspace 必须报错: %#v", tc.name, res)
			}
		})
	}
}

// TestTaskClaimSemantics 写面语义对齐 claim_return：无主 todo 可领、
// 已被他人认领 / 非 todo 报 tool error、必填缺失报 tool error。
func TestTaskClaimSemantics(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	wsID, agentA := seedBoard(t, ctx, d)

	todo, err := d.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "池中任务"})
	if err != nil {
		t.Fatal(err)
	}
	claimed := requireNoError(t, callTool(t, ctx, d, "task_claim", map[string]any{
		"workspace_id": wsID, "work_item_id": todo.ID, "agent_id": agentA, "expected_version": 0,
	}), "task_claim")
	if claimed["agent_profile_id"] != agentA {
		t.Fatalf("认领后 assignee = %#v", claimed["agent_profile_id"])
	}

	// 已被认领 → tool error 且含状态冲突语义。
	if res := callTool(t, ctx, d, "task_claim", map[string]any{
		"workspace_id": wsID, "work_item_id": todo.ID, "agent_id": "agent_other", "expected_version": 1,
	}); !res.IsError {
		t.Fatalf("已被认领任务再认领应报错: %#v", res)
	}

	// 非 todo（in_progress 无主）→ tool error。
	wip, err := d.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "进行中"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.svc.MoveWorkItem(ctx, wip.ID, domain.WorkItemInProgress, 0); err != nil {
		t.Fatal(err)
	}
	res := callTool(t, ctx, d, "task_claim", map[string]any{
		"workspace_id": wsID, "work_item_id": wip.ID, "agent_id": agentA, "expected_version": 0,
	})
	if !res.IsError {
		t.Fatalf("非 todo 认领应报错: %#v", res)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if want := "仅 todo"; !strings.Contains(text, want) {
		t.Fatalf("非 todo 认领错误应含 %q 语义: %s", want, text)
	}

	// 必填缺失（agent_id 缺席）→ tool error 而非 panic。
	if res := callTool(t, ctx, d, "task_claim", map[string]any{"workspace_id": wsID, "work_item_id": wip.ID}); !res.IsError {
		t.Fatalf("缺 agent_id 应报 tool error: %#v", res)
	}
}

func TestGovernanceToolsUseWorkspaceScopedService(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	wsID, agentID := seedBoard(t, ctx, d)
	root, err := d.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "治理根任务", AgentProfileID: agentID, RecordKind: domain.RecordKindTask,
		AcceptanceCriteria: []string{"治理读面可查询"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := d.svc.GetGoalForWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := d.svc.GetTodo(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}

	listed := requireNoError(t, callTool(t, ctx, d, "goal_list", map[string]any{"workspace_id": wsID}), "goal_list")
	if items, ok := listed["items"].([]any); !ok || len(items) != 1 {
		t.Fatalf("goal_list 应返回一个 Goal: %#v", listed)
	}
	got := requireNoError(t, callTool(t, ctx, d, "goal_get", map[string]any{
		"workspace_id": wsID, "goal_id": goal.ID,
	}), "goal_get")
	if got["id"] != goal.ID || got["workspace_id"] != wsID {
		t.Fatalf("goal_get 作用域/身份错误: %#v", got)
	}
	if res := callTool(t, ctx, d, "goal_get", map[string]any{
		"workspace_id": "ws_other", "goal_id": goal.ID,
	}); !res.IsError {
		t.Fatalf("跨 workspace goal_get 必须拒绝: %#v", res)
	}

	todos := requireNoError(t, callTool(t, ctx, d, "todo_list", map[string]any{
		"workspace_id": wsID, "goal_id": goal.ID,
	}), "todo_list")
	if items, ok := todos["items"].([]any); !ok || len(items) != 1 {
		t.Fatalf("todo_list 应返回一个 Todo: %#v", todos)
	}
	gotTodo := requireNoError(t, callTool(t, ctx, d, "todo_get", map[string]any{
		"workspace_id": wsID, "todo_id": todo.ID,
	}), "todo_get")
	if gotTodo["id"] != todo.ID || gotTodo["goal_id"] != goal.ID {
		t.Fatalf("todo_get 作用域/身份错误: %#v", gotTodo)
	}
	if res := callTool(t, ctx, d, "todo_get", map[string]any{
		"workspace_id": "ws_other", "todo_id": todo.ID,
	}); !res.IsError {
		t.Fatalf("跨 workspace todo_get 必须拒绝: %#v", res)
	}

	claimed := requireNoError(t, callTool(t, ctx, d, "todo_claim", map[string]any{
		"workspace_id": wsID, "todo_id": todo.ID, "owner_agent_id": agentID,
		"expected_version": todo.Version,
	}), "todo_claim")
	claimedVersion := int(claimed["version"].(float64))
	if claimed["status"] != string(domain.TodoClaimed) {
		t.Fatalf("todo_claim 应返回 claimed: %#v", claimed)
	}
	released := requireNoError(t, callTool(t, ctx, d, "todo_release", map[string]any{
		"workspace_id": wsID, "todo_id": todo.ID, "owner_agent_id": agentID,
		"expected_version": claimedVersion,
	}), "todo_release")
	if released["status"] != string(domain.TodoPending) || released["claim"] != nil {
		t.Fatalf("todo_release 应清理 claim 并回到 pending: %#v", released)
	}

	quota := requireNoError(t, callTool(t, ctx, d, "quota_get", map[string]any{
		"workspace_id": wsID, "goal_id": goal.ID,
	}), "quota_get")
	if quota["goal_id"] != goal.ID {
		t.Fatalf("quota_get 身份错误: %#v", quota)
	}
	metrics := requireNoError(t, callTool(t, ctx, d, "governance_metrics_get", map[string]any{
		"workspace_id": wsID,
	}), "governance_metrics_get")
	if metrics["workspace_id"] != wsID || metrics["plan_decode_errors"] == nil {
		t.Fatalf("governance_metrics_get 应返回 workspace 与错误族 map: %#v", metrics)
	}
}

func TestGovernanceListToolsEncodeEmptyCollectionsInJSONAndStructuredResults(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	wsID, _ := seedBoard(t, ctx, d)

	// A workspace with no Goal exercises the empty goal_list result directly.
	assertEmptyJSONArrayInResults(t,
		callTool(t, ctx, d, "goal_list", map[string]any{"workspace_id": wsID}),
		"goal_list", "items")

	// CreateGoal intentionally stays draft, so it has no Todo yet. The same
	// Goal has no Handoff or projection-repair rows either.
	root, err := d.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "MCP 空治理集合根任务", RecordKind: domain.RecordKindTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := d.svc.CreateGoal(ctx, wsID, application.CreateGoalParams{
		RootWorkItemID: root.ID, Objective: "空治理集合契约",
		AcceptanceContract: []string{"空列表保持数组"},
	})
	if err != nil {
		t.Fatal(err)
	}

	assertEmptyJSONArrayInResults(t,
		callTool(t, ctx, d, "todo_list", map[string]any{
			"workspace_id": wsID, "goal_id": goal.ID,
		}), "todo_list", "items")
	assertEmptyJSONArrayInResults(t,
		callTool(t, ctx, d, "handoff_list", map[string]any{
			"workspace_id": wsID, "goal_id": goal.ID,
		}), "handoff_list", "items")
	assertEmptyJSONArrayInResults(t,
		callTool(t, ctx, d, "projection_repairs_list", map[string]any{
			"workspace_id": wsID, "goal_id": goal.ID,
		}), "projection_repairs_list", "items")
	assertEmptyJSONArrayInResults(t,
		callTool(t, ctx, d, "quota_get", map[string]any{
			"workspace_id": wsID, "goal_id": goal.ID,
		}), "quota_get", "policies")
}

func TestGovernanceReceiptAndQuotaTurnToolsRejectCrossScope(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	wsID, agentID := seedBoard(t, ctx, d)
	root, err := d.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "治理回执任务", AgentProfileID: agentID, RecordKind: domain.RecordKindTask,
		AcceptanceCriteria: []string{"回执可回放"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := d.svc.GetGoalForWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := d.svc.GetTodo(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := d.svc.ClaimTodo(ctx, todo.ID, agentID, todo.Version, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	header, err := d.svc.AdmitTurn(ctx, application.AdmitTurnParams{
		GoalID: goal.ID, TodoID: todo.ID, OwnerAgentID: agentID, ExpectedTodoVersion: claimed.Version,
		Attempt: 1, SchemaVersion: "plan-decision/v2", InputSnapshotDigest: digest,
		AdmissionClientKey: "mcp-receipt-turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"workspace_id": wsID, "goal_id": goal.ID, "todo_id": todo.ID, "turn_seq": float64(header.TurnKey.TurnSeq)}
	receipt := requireNoError(t, callTool(t, ctx, d, "turn_receipt_get", args), "turn_receipt_get")
	if receipt["header"] == nil || receipt["phases"] == nil {
		t.Fatalf("turn_receipt_get 应返回 header/phases: %#v", receipt)
	}
	turnQuota := requireNoError(t, callTool(t, ctx, d, "quota_turn_get", args), "quota_turn_get")
	if turnQuota["turn_key"] == nil || turnQuota["reservations"] == nil || turnQuota["spend"] == nil {
		t.Fatalf("quota_turn_get 应返回 turn_key/reservations/spend: %#v", turnQuota)
	}
	args["workspace_id"] = "ws_other"
	if res := callTool(t, ctx, d, "turn_receipt_get", args); !res.IsError {
		t.Fatalf("跨 workspace turn_receipt_get 必须拒绝: %#v", res)
	}
	if res := callTool(t, ctx, d, "quota_turn_get", args); !res.IsError {
		t.Fatalf("跨 workspace quota_turn_get 必须拒绝: %#v", res)
	}
}
