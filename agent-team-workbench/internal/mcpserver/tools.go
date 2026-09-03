// tools.go MCP 工具注册表与 handler（F5）。注册表是单一真源：
// RegisterTools 遍历它注册到 MCPServer，测试对它做红线断言。
//
// 安全红线（刻意不暴露，测试 ToolNamesForbiddenAbsent 钉死）：
//   - approval resolve——agent 不能批自己的审批；
//   - work item 创建/删除——看板结构变更只归人（HTTP API + UI）；
//   - 会话重置——会话生命周期只归 ModuleRunner。
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// eventsTailDefaultLimit / eventsTailMaxLimit 是 run_events_tail 的窗口约定。
const (
	eventsTailDefaultLimit = 50
	eventsTailMaxLimit     = 200
	governanceClaimTTL     = 15 * time.Minute
)

type toolDeps struct {
	store application.Store
	svc   *application.Service
}

type toolDef struct {
	name    string
	tool    mcp.Tool
	handler server.ToolHandlerFunc
}

// RegisterTools 把全部工具注册到 MCPServer。
func RegisterTools(s *server.MCPServer, d *toolDeps) {
	for _, td := range toolDefs(d) {
		s.AddTool(td.tool, td.handler)
	}
}

// forbiddenToolNames 是绝不允许出现在注册表里的名字（红线回归断言用）。
var forbiddenToolNames = []string{
	"approval_resolve", "work_item_create", "work_item_delete", "session_reset",
	"handoff_create", "handoff_accept", "handoff_reject", "handoff_cancel", "projection_repair",
}

// workItemStatuses task_list 的 status 入参枚举。
var workItemStatuses = map[string]domain.WorkItemStatus{
	"todo":        domain.WorkItemTodo,
	"in_progress": domain.WorkItemInProgress,
	"blocked":     domain.WorkItemBlocked,
	"completed":   domain.WorkItemCompleted,
	"cancelled":   domain.WorkItemCancelled,
}

func toolDefs(d *toolDeps) []toolDef {
	return []toolDef{
		{
			name: "workspace_list",
			tool: mcp.NewTool("workspace_list",
				mcp.WithDescription("列出全部 workspace（看板入口，先拿 workspace_id 再查任务）"),
				mcp.WithReadOnlyHintAnnotation(true)),
			handler: d.workspaceList,
		},
		{
			name: "task_list",
			tool: mcp.NewTool("task_list",
				mcp.WithDescription("列出 workspace 内的任务（默认 50 条，按创建序）"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识（ws_ 前缀）")),
				mcp.WithString("status", mcp.Description("可选状态过滤：todo/in_progress/blocked/completed/cancelled")),
			),
			handler: d.taskList,
		},
		{
			name: "task_get",
			tool: mcp.NewTool("task_get",
				mcp.WithDescription("按 id 取单个任务详情（含 assignee、phase、version）"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("work_item_id", mcp.Required(), mcp.Description("任务标识（wi_ 前缀）")),
			),
			handler: d.taskGet,
		},
		{
			name: "run_list",
			tool: mcp.NewTool("run_list",
				mcp.WithDescription("列出任务的执行 run（按创建序，含终态）"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("work_item_id", mcp.Required(), mcp.Description("任务标识（wi_ 前缀）")),
			),
			handler: d.runList,
		},
		{
			name: "run_get",
			tool: mcp.NewTool("run_get",
				mcp.WithDescription("按 id 取单个 run 详情（状态、用量、失败信息）"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("run_id", mcp.Required(), mcp.Description("run 标识（run_ 前缀）")),
			),
			handler: d.runGet,
		},
		{
			name: "run_events_tail",
			tool: mcp.NewTool("run_events_tail",
				mcp.WithDescription("取 run 事件尾部窗口（默认 50 条、上限 200；按 run_seq 正序返回）"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("run_id", mcp.Required(), mcp.Description("run 标识（run_ 前缀）")),
				mcp.WithNumber("limit", mcp.Description("尾部窗口大小，缺省 50，最大 200")),
			),
			handler: d.runEventsTail,
		},
		{
			name: "approval_list",
			tool: mcp.NewTool("approval_list",
				mcp.WithDescription("列出 run 的审批请求（agent 只能查不能批）"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("run_id", mcp.Required(), mcp.Description("run 标识（run_ 前缀）")),
			),
			handler: d.approvalList,
		},
		{
			name: "task_claim",
			tool: mcp.NewTool("task_claim",
				mcp.WithDescription("认领任务池中无主 todo 任务（已被认领或非 todo 报错；同 agent 重复认领幂等）"),
				// claim 无副作用（同 agent 重复认领幂等），harness 可安全重试。
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("work_item_id", mcp.Required(), mcp.Description("任务标识（wi_ 前缀）")),
				mcp.WithString("agent_id", mcp.Required(), mcp.Description("认领者 agent 标识（agent_ 前缀）")),
				mcp.WithNumber("expected_version", mcp.Required(), mcp.Description("乐观锁版本（task_get 返回的 version）")),
			),
			handler: d.taskClaim,
		},
		{
			name: "task_return",
			tool: mcp.NewTool("task_return",
				mcp.WithDescription("把 review/acceptance 态任务打回重做（回到 execution；其他状态报错）"),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("work_item_id", mcp.Required(), mcp.Description("任务标识（wi_ 前缀）")),
				mcp.WithString("reason", mcp.Description("打回原因（落 activity，建议写明评审意见）")),
				mcp.WithNumber("expected_version", mcp.Required(), mcp.Description("乐观锁版本（task_get 返回的 version）")),
			),
			handler: d.taskReturn,
		},
		{
			name: "goal_list",
			tool: mcp.NewTool("goal_list",
				mcp.WithDescription("列出 workspace 的治理 Goal（只读服务端投影）"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识"))),
			handler: d.goalList,
		},
		{
			name: "governance_metrics_get",
			tool: mcp.NewTool("governance_metrics_get",
				mcp.WithDescription("读取 Workspace 治理指标（从 canonical event stream 重算）"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识"))),
			handler: d.governanceMetricsGet,
		},
		{
			name: "goal_get",
			tool: mcp.NewTool("goal_get",
				mcp.WithDescription("读取一个治理 Goal（仅同 workspace）"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("goal_id", mcp.Required(), mcp.Description("Goal 标识"))),
			handler: d.goalGet,
		},
		{
			name: "todo_list",
			tool: mcp.NewTool("todo_list",
				mcp.WithDescription("列出一个 Goal 的治理 Todo"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("goal_id", mcp.Required(), mcp.Description("Goal 标识"))),
			handler: d.todoList,
		},
		{
			name: "todo_get",
			tool: mcp.NewTool("todo_get",
				mcp.WithDescription("读取一个治理 Todo（仅同 workspace）"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("todo_id", mcp.Required(), mcp.Description("Todo 标识"))),
			handler: d.todoGet,
		},
		{
			name: "todo_claim",
			tool: mcp.NewTool("todo_claim",
				mcp.WithDescription("通过治理 Service 认领 Todo（不创建 Run 或 Runner lease）"),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("todo_id", mcp.Required(), mcp.Description("Todo 标识")),
				mcp.WithString("owner_agent_id", mcp.Required(), mcp.Description("认领者 Agent 标识")),
				mcp.WithNumber("expected_version", mcp.Required(), mcp.Description("Todo version")),
				mcp.WithString("expires_at", mcp.Description("可选 RFC3339，到期后释放认领"))),
			handler: d.todoClaim,
		},
		{
			name: "todo_release",
			tool: mcp.NewTool("todo_release",
				mcp.WithDescription("通过治理 Service 释放 Todo claim"),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("todo_id", mcp.Required(), mcp.Description("Todo 标识")),
				mcp.WithString("owner_agent_id", mcp.Required(), mcp.Description("当前 claim owner")),
				mcp.WithNumber("expected_version", mcp.Required(), mcp.Description("Todo version"))),
			handler: d.todoRelease,
		},
		{
			name: "turn_receipt_get",
			tool: mcp.NewTool("turn_receipt_get",
				mcp.WithDescription("读取治理 Turn 的 immutable Receipt Header/Phase"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("goal_id", mcp.Required(), mcp.Description("Goal 标识")),
				mcp.WithString("todo_id", mcp.Required(), mcp.Description("Todo 标识")),
				mcp.WithNumber("turn_seq", mcp.Required(), mcp.Description("Turn 序号"))),
			handler: d.turnReceiptGet,
		},
		{
			name: "quota_get",
			tool: mcp.NewTool("quota_get",
				mcp.WithDescription("读取 Goal 的 quota policy、已用、冻结与 unresolved"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("goal_id", mcp.Required(), mcp.Description("Goal 标识"))),
			handler: d.quotaGet,
		},
		{
			name: "quota_turn_get",
			tool: mcp.NewTool("quota_turn_get",
				mcp.WithDescription("读取一个治理 Turn 的 reservation 与 per-Run spend"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("goal_id", mcp.Required(), mcp.Description("Goal 标识")),
				mcp.WithString("todo_id", mcp.Required(), mcp.Description("Todo 标识")),
				mcp.WithNumber("turn_seq", mcp.Required(), mcp.Description("Turn 序号"))),
			handler: d.quotaTurnGet,
		},
		{
			name: "handoff_list",
			tool: mcp.NewTool("handoff_list",
				mcp.WithDescription("列出 Goal 的治理 Handoff"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("goal_id", mcp.Required(), mcp.Description("Goal 标识"))),
			handler: d.handoffList,
		},
		{
			name: "handoff_get",
			tool: mcp.NewTool("handoff_get",
				mcp.WithDescription("读取一个治理 Handoff（仅同 workspace）"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("handoff_id", mcp.Required(), mcp.Description("Handoff 标识"))),
			handler: d.handoffGet,
		},
		{
			name: "evidence_list",
			tool: mcp.NewTool("evidence_list",
				mcp.WithDescription("读取 Goal 的服务端治理证据摘要"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("goal_id", mcp.Required(), mcp.Description("Goal 标识"))),
			handler: d.evidenceList,
		},
		{
			name: "projection_get",
			tool: mcp.NewTool("projection_get",
				mcp.WithDescription("读取可重建的 Goal 治理投影；不读取第二状态库"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("goal_id", mcp.Required(), mcp.Description("Goal 标识"))),
			handler: d.projectionGet,
		},
		{
			name: "projection_repairs_list",
			tool: mcp.NewTool("projection_repairs_list",
				mcp.WithDescription("读取 Goal 的 projection repair checkpoint"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("goal_id", mcp.Required(), mcp.Description("Goal 标识"))),
			handler: d.projectionRepairsList,
		},
		{
			name: "todo_resolve_user_action",
			tool: mcp.NewTool("todo_resolve_user_action",
				mcp.WithDescription("清除 Todo 的 durable user-action checkpoint；Coordinator 仍是唯一 Run 创建者"),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithString("workspace_id", mcp.Required(), mcp.Description("workspace 标识")),
				mcp.WithString("todo_id", mcp.Required(), mcp.Description("Todo 标识")),
				mcp.WithString("resolution", mcp.Required(), mcp.Description("用户动作解决说明")),
				mcp.WithString("actor_id", mcp.Required(), mcp.Description("操作者 identity")),
				mcp.WithNumber("expected_version", mcp.Description("Todo version；缺省表示不使用版本条件")),
				mcp.WithString("client_key", mcp.Description("业务幂等键"))),
			handler: d.todoResolveUserAction,
		},
	}
}

// runEventSlim run_events_tail 的精简投影：只带 agent 排障要看的三个字段。
type runEventSlim struct {
	EventType  string         `json:"event_type"`
	OccurredAt time.Time      `json:"occurred_at"`
	Data       map[string]any `json:"data,omitempty"`
}

// toolErr 把仓储/用例错误统一转 MCP tool error（进程不 panic）。
func toolErr(err error) (*mcp.CallToolResult, error) {
	msg := err.Error()
	switch {
	case errors.Is(err, domain.ErrNotFound):
		msg = "未找到: " + msg
	case errors.Is(err, domain.ErrVersionConflict):
		msg = "版本冲突（expected_version 已过期，重新 task_get 拿最新 version）: " + msg
	case errors.Is(err, domain.ErrValidation):
		msg = "参数不合法: " + msg
	}
	return mcp.NewToolResultError(msg), nil
}

// jsonResult 输出统一走结构化 JSON 文本 content。
func jsonResult(v any) (*mcp.CallToolResult, error) {
	result, err := mcp.NewToolResultJSON(v)
	if err != nil {
		return mcp.NewToolResultError("序列化输出失败: " + err.Error()), nil
	}
	return result, nil
}

// requireTaskWorkItem makes the MCP task surface fail closed for Chat records.
// MCP exposes task-board semantics only; Chat keeps its Run/Session contract on
// the Chat surface and must never be discovered or mutated through task tools.
func (d *toolDeps) requireTaskWorkItem(ctx context.Context, workspaceID, workItemID string) (*domain.WorkItem, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(workspaceID) != workspaceID {
		return nil, fmt.Errorf("%w: workspace_id 必须为非空且无首尾空格", domain.ErrValidation)
	}
	wi, err := d.svc.WorkItem(ctx, workItemID)
	if err != nil {
		return nil, err
	}
	if wi.WorkspaceID != workspaceID {
		return nil, domain.ErrNotFound
	}
	kind := wi.RecordKind
	if kind == "" {
		// Direct in-process callers predating record_kind are historical tasks;
		// persisted migration rows are normalized before reaching this surface.
		kind = domain.RecordKindTask
	}
	if kind != domain.RecordKindTask {
		return nil, fmt.Errorf("%w: record_kind=%s 不是 Task 记录", domain.ErrValidation, kind)
	}
	return wi, nil
}

func (d *toolDeps) requireTaskRun(ctx context.Context, workspaceID, runID string) (*domain.ExecutionRun, error) {
	run, err := d.svc.Run(ctx, runID)
	if err != nil {
		return nil, err
	}
	if _, err := d.requireTaskWorkItem(ctx, workspaceID, run.WorkItemID); err != nil {
		return nil, err
	}
	return run, nil
}

func (d *toolDeps) workspaceList(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ids, err := d.store.Workspaces().ListIDs(ctx)
	if err != nil {
		return toolErr(err)
	}
	workspaces := make([]*domain.Workspace, 0, len(ids))
	for _, id := range ids {
		ws, err := d.store.Workspaces().Get(ctx, id)
		if err != nil {
			return toolErr(err)
		}
		workspaces = append(workspaces, ws)
	}
	return jsonResult(map[string]any{"workspaces": workspaces})
}

func (d *toolDeps) taskList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	filter := application.WorkItemFilter{RecordKind: domain.RecordKindTask}
	if status := req.GetString("status", ""); status != "" {
		st, ok := workItemStatuses[status]
		if !ok {
			return mcp.NewToolResultError("非法 status: " + status + "（合法值 todo/in_progress/blocked/completed/cancelled）"), nil
		}
		filter.Status = st
	}
	items, _, err := d.svc.WorkItems(ctx, workspaceID, filter)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(map[string]any{"items": workItemDTOs(items)})
}

func (d *toolDeps) taskGet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	id, err := req.RequireString("work_item_id")
	if err != nil {
		return mcp.NewToolResultError("work_item_id 必填: " + err.Error()), nil
	}
	wi, err := d.requireTaskWorkItem(ctx, workspaceID, id)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(newWorkItemDTO(wi))
}

func (d *toolDeps) runList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	wiID, err := req.RequireString("work_item_id")
	if err != nil {
		return mcp.NewToolResultError("work_item_id 必填: " + err.Error()), nil
	}
	if _, err := d.requireTaskWorkItem(ctx, workspaceID, wiID); err != nil {
		return toolErr(err)
	}
	runs, err := d.svc.RunsByWorkItem(ctx, wiID)
	if err != nil {
		return toolErr(err)
	}
	if runs == nil {
		runs = []*domain.ExecutionRun{}
	}
	return jsonResult(map[string]any{"runs": executionRunDTOs(runs)})
}

func (d *toolDeps) runGet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	runID, err := req.RequireString("run_id")
	if err != nil {
		return mcp.NewToolResultError("run_id 必填: " + err.Error()), nil
	}
	run, err := d.requireTaskRun(ctx, workspaceID, runID)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(newExecutionRunDTO(run))
}

func (d *toolDeps) runEventsTail(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	runID, err := req.RequireString("run_id")
	if err != nil {
		return mcp.NewToolResultError("run_id 必填: " + err.Error()), nil
	}
	limit := req.GetInt("limit", eventsTailDefaultLimit)
	if limit <= 0 {
		limit = eventsTailDefaultLimit
	}
	if limit > eventsTailMaxLimit {
		limit = eventsTailMaxLimit
	}
	if _, err := d.requireTaskRun(ctx, workspaceID, runID); err != nil {
		return toolErr(err)
	}
	evs, err := d.svc.RunEvents(ctx, runID)
	if err != nil {
		return toolErr(err)
	}
	// ListRunEvents 按 run_seq 正序回放；取尾部窗口后仍保持正序。
	if len(evs) > limit {
		evs = evs[len(evs)-limit:]
	}
	out := make([]runEventSlim, 0, len(evs))
	for _, e := range evs {
		out = append(out, runEventSlim{EventType: e.EventType, OccurredAt: e.OccurredAt, Data: e.Payload})
	}
	return jsonResult(map[string]any{"events": out})
}

func (d *toolDeps) approvalList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	runID, err := req.RequireString("run_id")
	if err != nil {
		return mcp.NewToolResultError("run_id 必填: " + err.Error()), nil
	}
	if _, err := d.requireTaskRun(ctx, workspaceID, runID); err != nil {
		return toolErr(err)
	}
	approvals, err := d.svc.Approvals(ctx, runID)
	if err != nil {
		return toolErr(err)
	}
	if approvals == nil {
		approvals = []*domain.ApprovalRequest{}
	}
	return jsonResult(map[string]any{"approvals": approvals})
}

func (d *toolDeps) taskClaim(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	wiID, err := req.RequireString("work_item_id")
	if err != nil {
		return mcp.NewToolResultError("work_item_id 必填: " + err.Error()), nil
	}
	agentID, err := req.RequireString("agent_id")
	if err != nil {
		return mcp.NewToolResultError("agent_id 必填: " + err.Error()), nil
	}
	version, err := req.RequireInt("expected_version")
	if err != nil {
		return mcp.NewToolResultError("expected_version 必填: " + err.Error()), nil
	}
	if _, err := d.requireTaskWorkItem(ctx, workspaceID, wiID); err != nil {
		return toolErr(err)
	}
	wi, err := d.svc.ClaimWorkItem(ctx, wiID, agentID, version)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(newWorkItemDTO(wi))
}

func (d *toolDeps) taskReturn(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	wiID, err := req.RequireString("work_item_id")
	if err != nil {
		return mcp.NewToolResultError("work_item_id 必填: " + err.Error()), nil
	}
	version, err := req.RequireInt("expected_version")
	if err != nil {
		return mcp.NewToolResultError("expected_version 必填: " + err.Error()), nil
	}
	if _, err := d.requireTaskWorkItem(ctx, workspaceID, wiID); err != nil {
		return toolErr(err)
	}
	wi, err := d.svc.ReturnWorkItem(ctx, wiID, req.GetString("reason", ""), version)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(newWorkItemDTO(wi))
}

func (d *toolDeps) requireGoal(ctx context.Context, workspaceID, goalID string) (*domain.Goal, error) {
	if workspaceID == "" || goalID == "" {
		return nil, fmt.Errorf("%w: workspace_id and goal_id are required", domain.ErrValidation)
	}
	goal, err := d.svc.GetGoal(ctx, goalID)
	if err != nil {
		return nil, err
	}
	if goal.WorkspaceID != workspaceID {
		return nil, domain.ErrNotFound
	}
	return goal, nil
}

func (d *toolDeps) requireTodo(ctx context.Context, workspaceID, todoID string) (*domain.Todo, *domain.Goal, error) {
	if workspaceID == "" || todoID == "" {
		return nil, nil, fmt.Errorf("%w: workspace_id and todo_id are required", domain.ErrValidation)
	}
	todo, err := d.svc.GetTodo(ctx, todoID)
	if err != nil {
		return nil, nil, err
	}
	goal, err := d.requireGoal(ctx, workspaceID, todo.GoalID)
	if err != nil {
		return nil, nil, err
	}
	return todo, goal, nil
}

func (d *toolDeps) goalList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	goals, err := d.svc.ListGoals(ctx, workspaceID)
	if err != nil {
		return toolErr(err)
	}
	if goals == nil {
		goals = []*domain.Goal{}
	}
	return jsonResult(map[string]any{"items": goals})
}

func (d *toolDeps) governanceMetricsGet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	metrics, err := d.svc.GetGovernanceMetrics(ctx, workspaceID)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(metrics)
}

func (d *toolDeps) goalGet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	goalID, err := req.RequireString("goal_id")
	if err != nil {
		return mcp.NewToolResultError("goal_id 必填: " + err.Error()), nil
	}
	goal, err := d.requireGoal(ctx, workspaceID, goalID)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(goal)
}

func (d *toolDeps) todoList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	goalID, err := req.RequireString("goal_id")
	if err != nil {
		return mcp.NewToolResultError("goal_id 必填: " + err.Error()), nil
	}
	if _, err := d.requireGoal(ctx, workspaceID, goalID); err != nil {
		return toolErr(err)
	}
	todos, err := d.svc.ListTodos(ctx, goalID)
	if err != nil {
		return toolErr(err)
	}
	if todos == nil {
		todos = []*domain.Todo{}
	}
	return jsonResult(map[string]any{"items": todos})
}

func (d *toolDeps) todoGet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	todoID, err := req.RequireString("todo_id")
	if err != nil {
		return mcp.NewToolResultError("todo_id 必填: " + err.Error()), nil
	}
	todo, _, err := d.requireTodo(ctx, workspaceID, todoID)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(todo)
}

func (d *toolDeps) todoClaim(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	todoID, err := req.RequireString("todo_id")
	if err != nil {
		return mcp.NewToolResultError("todo_id 必填: " + err.Error()), nil
	}
	ownerID, err := req.RequireString("owner_agent_id")
	if err != nil {
		return mcp.NewToolResultError("owner_agent_id 必填: " + err.Error()), nil
	}
	version, err := req.RequireInt("expected_version")
	if err != nil {
		return mcp.NewToolResultError("expected_version 必填: " + err.Error()), nil
	}
	if _, _, err := d.requireTodo(ctx, workspaceID, todoID); err != nil {
		return toolErr(err)
	}
	expiresAt := time.Now().UTC().Add(governanceClaimTTL)
	if raw := req.GetString("expires_at", ""); raw != "" {
		expiresAt, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return mcp.NewToolResultError("expires_at 必须为 RFC3339"), nil
		}
		expiresAt = expiresAt.UTC()
	}
	todo, err := d.svc.ClaimTodo(ctx, todoID, ownerID, version, expiresAt)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(todo)
}

func (d *toolDeps) todoRelease(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	todoID, err := req.RequireString("todo_id")
	if err != nil {
		return mcp.NewToolResultError("todo_id 必填: " + err.Error()), nil
	}
	ownerID, err := req.RequireString("owner_agent_id")
	if err != nil {
		return mcp.NewToolResultError("owner_agent_id 必填: " + err.Error()), nil
	}
	version, err := req.RequireInt("expected_version")
	if err != nil {
		return mcp.NewToolResultError("expected_version 必填: " + err.Error()), nil
	}
	if _, _, err := d.requireTodo(ctx, workspaceID, todoID); err != nil {
		return toolErr(err)
	}
	todo, err := d.svc.ReleaseTodo(ctx, todoID, ownerID, version)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(todo)
}

func (d *toolDeps) turnKey(ctx context.Context, req mcp.CallToolRequest) (domain.TurnKey, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return domain.TurnKey{}, err
	}
	goalID, err := req.RequireString("goal_id")
	if err != nil {
		return domain.TurnKey{}, err
	}
	todoID, err := req.RequireString("todo_id")
	if err != nil {
		return domain.TurnKey{}, err
	}
	turnSeq, err := req.RequireInt("turn_seq")
	if err != nil || turnSeq < 1 {
		return domain.TurnKey{}, fmt.Errorf("%w: turn_seq must be positive", domain.ErrValidation)
	}
	todo, goal, err := d.requireTodo(ctx, workspaceID, todoID)
	if err != nil {
		return domain.TurnKey{}, err
	}
	if goal.ID != goalID || todo.GoalID != goalID {
		return domain.TurnKey{}, domain.ErrNotFound
	}
	return domain.TurnKey{GoalID: goalID, TodoID: todoID, TurnSeq: int64(turnSeq)}, nil
}

func (d *toolDeps) turnReceiptGet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key, err := d.turnKey(ctx, req)
	if err != nil {
		return toolErr(err)
	}
	receipt, err := d.svc.GetTurnReceipt(ctx, key)
	if err != nil {
		return toolErr(err)
	}
	phases := receipt.Phases
	if phases == nil {
		phases = []domain.TurnReceiptPhase{}
	}
	return jsonResult(map[string]any{"header": receipt.Header, "phases": phases})
}

func (d *toolDeps) quotaGet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	goalID, err := req.RequireString("goal_id")
	if err != nil {
		return mcp.NewToolResultError("goal_id 必填: " + err.Error()), nil
	}
	goal, err := d.requireGoal(ctx, workspaceID, goalID)
	if err != nil {
		return toolErr(err)
	}
	summary, err := d.svc.GetGoalQuotaSummary(ctx, goal.ID)
	if err != nil {
		return toolErr(err)
	}
	policies := summary.Policies
	if policies == nil {
		policies = []domain.QuotaPolicy{}
	}
	reservations := summary.Reservations
	if reservations == nil {
		reservations = []*domain.QuotaReservation{}
	}
	unresolved := summary.Unresolved
	if unresolved == nil {
		unresolved = []*domain.QuotaSpendEntry{}
	}
	committed := summary.Committed
	if committed == nil {
		committed = map[domain.QuotaKind]int64{}
	}
	activeReserved := summary.ActiveReserved
	if activeReserved == nil {
		activeReserved = map[domain.QuotaKind]int64{}
	}
	return jsonResult(map[string]any{
		"goal_id": summary.GoalID, "policies": policies, "reservations": reservations,
		"unresolved": unresolved, "committed": committed, "active_reserved": activeReserved,
		"active_workers": summary.ActiveWorkers,
	})
}

func (d *toolDeps) quotaTurnGet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key, err := d.turnKey(ctx, req)
	if err != nil {
		return toolErr(err)
	}
	reservations, spend, err := d.svc.GetTurnQuota(ctx, key)
	if err != nil {
		return toolErr(err)
	}
	if reservations == nil {
		reservations = []*domain.QuotaReservation{}
	}
	if spend == nil {
		spend = []*domain.QuotaSpendEntry{}
	}
	return jsonResult(map[string]any{"turn_key": key, "reservations": reservations, "spend": spend})
}

func (d *toolDeps) requireHandoff(ctx context.Context, workspaceID, handoffID string) (*domain.Handoff, error) {
	if workspaceID == "" || handoffID == "" {
		return nil, fmt.Errorf("%w: workspace_id and handoff_id are required", domain.ErrValidation)
	}
	handoff, err := d.svc.Handoff(ctx, handoffID)
	if err != nil {
		return nil, err
	}
	goal, err := d.requireGoal(ctx, workspaceID, handoff.GoalID)
	if err != nil {
		return nil, err
	}
	if goal.ID != handoff.GoalID {
		return nil, domain.ErrNotFound
	}
	return handoff, nil
}

func (d *toolDeps) handoffList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	goalID, err := req.RequireString("goal_id")
	if err != nil {
		return mcp.NewToolResultError("goal_id 必填: " + err.Error()), nil
	}
	if _, err := d.requireGoal(ctx, workspaceID, goalID); err != nil {
		return toolErr(err)
	}
	handoffs, err := d.svc.HandoffsByGoal(ctx, goalID)
	if err != nil {
		return toolErr(err)
	}
	if handoffs == nil {
		handoffs = []*domain.Handoff{}
	}
	return jsonResult(map[string]any{"items": handoffs})
}

func (d *toolDeps) handoffGet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	handoffID, err := req.RequireString("handoff_id")
	if err != nil {
		return mcp.NewToolResultError("handoff_id 必填: " + err.Error()), nil
	}
	handoff, err := d.requireHandoff(ctx, workspaceID, handoffID)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(handoff)
}

func (d *toolDeps) evidenceList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	goalID, err := req.RequireString("goal_id")
	if err != nil {
		return mcp.NewToolResultError("goal_id 必填: " + err.Error()), nil
	}
	if _, err := d.requireGoal(ctx, workspaceID, goalID); err != nil {
		return toolErr(err)
	}
	evidence, err := d.svc.GetGoalEvidence(ctx, goalID)
	if err != nil {
		return toolErr(err)
	}
	if evidence == nil {
		evidence = []domain.GovernanceEvidenceItem{}
	}
	return jsonResult(map[string]any{"items": evidence})
}

func (d *toolDeps) projectionGet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	goalID, err := req.RequireString("goal_id")
	if err != nil {
		return mcp.NewToolResultError("goal_id 必填: " + err.Error()), nil
	}
	if _, err := d.requireGoal(ctx, workspaceID, goalID); err != nil {
		return toolErr(err)
	}
	projection, err := d.svc.GetGovernanceProjection(ctx, goalID)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(projection)
}

func (d *toolDeps) projectionRepairsList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	goalID, err := req.RequireString("goal_id")
	if err != nil {
		return mcp.NewToolResultError("goal_id 必填: " + err.Error()), nil
	}
	if _, err := d.requireGoal(ctx, workspaceID, goalID); err != nil {
		return toolErr(err)
	}
	repairs, err := d.svc.GetProjectionRepairs(ctx, goalID)
	if err != nil {
		return toolErr(err)
	}
	if repairs == nil {
		repairs = []*domain.ProjectionRepair{}
	}
	return jsonResult(map[string]any{"items": repairs})
}

func (d *toolDeps) todoResolveUserAction(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workspaceID, err := req.RequireString("workspace_id")
	if err != nil {
		return mcp.NewToolResultError("workspace_id 必填: " + err.Error()), nil
	}
	todoID, err := req.RequireString("todo_id")
	if err != nil {
		return mcp.NewToolResultError("todo_id 必填: " + err.Error()), nil
	}
	resolution, err := req.RequireString("resolution")
	if err != nil {
		return mcp.NewToolResultError("resolution 必填: " + err.Error()), nil
	}
	actorID, err := req.RequireString("actor_id")
	if err != nil {
		return mcp.NewToolResultError("actor_id 必填: " + err.Error()), nil
	}
	todo, _, err := d.requireTodo(ctx, workspaceID, todoID)
	if err != nil {
		return toolErr(err)
	}
	version := req.GetInt("expected_version", 0)
	resolved, err := d.svc.ResolveTodoUserAction(ctx, application.ResolveTodoUserActionParams{
		TodoID: todo.ID, Resolution: resolution, ActorID: actorID,
		ExpectedVersion: version, ClientKey: req.GetString("client_key", ""),
	})
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(resolved)
}
