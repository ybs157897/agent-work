// tools_test.go MCP 工具面集成测试（F5）：夹具照 internal/httpapi/idempotency_test.go
// 的临时 sqlite + 全量迁移模式。断言：只读工具返回种子实体、run_events_tail
// 截断与顺序、approval_list 空跑不炸、注册表红线（无 approval_resolve /
// work_item_create / session_reset）、task_claim 对非 todo / 已指派报错。
package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// openTestDB 临时文件 sqlite + 全量迁移（动态发现 migrations/sqlite/*.sql，新增迁移免同步清单；
// 等价性由 cmd/migrate 的守卫测试兜底）。
func openTestDB(t *testing.T) *sqlstore.Store {
	t.Helper()
	db, err := sql.Open("sqlite",
		filepath.Join(t.TempDir(), "test.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, current, _, _ := runtime.Caller(0)
	migrationDir := filepath.Join(filepath.Dir(current), "..", "..", "migrations", "sqlite")
	names, err := filepath.Glob(filepath.Join(migrationDir, "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatalf("未在 %s 发现迁移文件", migrationDir)
	}
	for _, path := range names {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("migration %s: %v", filepath.Base(path), err)
		}
	}
	return sqlstore.New(db, sqlstore.SQLiteDialect())
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
		titles[it.(map[string]any)["Title"].(string)] = true
	}
	if !titles["MCP 只读面"] || !titles["第二个任务"] {
		t.Fatalf("task_list 应包含两个种子标题: %#v", titles)
	}

	one := requireNoError(t, callTool(t, ctx, d, "task_get", map[string]any{"work_item_id": first.ID}), "task_get")
	if one["ID"] != first.ID || one["Status"] != string(domain.WorkItemTodo) {
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
	if res := callTool(t, ctx, d, "task_get", map[string]any{"work_item_id": "wi_none"}); !res.IsError {
		t.Fatalf("task_get 不存在任务应报错: %#v", res)
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
	if !ok || len(items) != 1 || items[0].(map[string]any)["Title"] != task.Title {
		t.Fatalf("task_list 不得混入 Chat：%#v", list)
	}

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "task_get", args: map[string]any{"work_item_id": chat.ID}},
		{name: "run_list", args: map[string]any{"work_item_id": chat.ID}},
		{name: "run_get", args: map[string]any{"run_id": chatRun.ID}},
		{name: "run_events_tail", args: map[string]any{"run_id": chatRun.ID}},
		{name: "approval_list", args: map[string]any{"run_id": chatRun.ID}},
		{name: "task_claim", args: map[string]any{"work_item_id": chat.ID, "agent_id": agentID, "expected_version": 0}},
		{name: "task_return", args: map[string]any{"work_item_id": chat.ID, "expected_version": 0}},
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
		map[string]any{"run_id": runID, "limit": 2}), "run_events_tail")
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
	got = requireNoError(t, callTool(t, ctx, d, "run_events_tail", map[string]any{"run_id": runID}), "run_events_tail")
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

	got := requireNoError(t, callTool(t, ctx, d, "approval_list", map[string]any{"run_id": runID}), "approval_list")
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
	if len(seen) != 9 {
		t.Fatalf("工具表应恰为 9 个（7 只读 + 2 写面），实际 %d: %v", len(seen), seen)
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
		"work_item_id": todo.ID, "agent_id": agentA, "expected_version": 0,
	}), "task_claim")
	if claimed["AgentProfileID"] != agentA {
		t.Fatalf("认领后 assignee = %#v", claimed["AgentProfileID"])
	}

	// 已被认领 → tool error 且含状态冲突语义。
	if res := callTool(t, ctx, d, "task_claim", map[string]any{
		"work_item_id": todo.ID, "agent_id": "agent_other", "expected_version": 1,
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
		"work_item_id": wip.ID, "agent_id": agentA, "expected_version": 0,
	})
	if !res.IsError {
		t.Fatalf("非 todo 认领应报错: %#v", res)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if want := "仅 todo"; !strings.Contains(text, want) {
		t.Fatalf("非 todo 认领错误应含 %q 语义: %s", want, text)
	}

	// 必填缺失（agent_id 缺席）→ tool error 而非 panic。
	if res := callTool(t, ctx, d, "task_claim", map[string]any{"work_item_id": wip.ID}); !res.IsError {
		t.Fatalf("缺 agent_id 应报 tool error: %#v", res)
	}
}
