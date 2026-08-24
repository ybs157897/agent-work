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
	"encoding/json"
	"errors"
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
var forbiddenToolNames = []string{"approval_resolve", "work_item_create", "work_item_delete", "session_reset"}

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
				mcp.WithString("work_item_id", mcp.Required(), mcp.Description("任务标识（wi_ 前缀）")),
			),
			handler: d.taskGet,
		},
		{
			name: "run_list",
			tool: mcp.NewTool("run_list",
				mcp.WithDescription("列出任务的执行 run（按创建序，含终态）"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("work_item_id", mcp.Required(), mcp.Description("任务标识（wi_ 前缀）")),
			),
			handler: d.runList,
		},
		{
			name: "run_get",
			tool: mcp.NewTool("run_get",
				mcp.WithDescription("按 id 取单个 run 详情（状态、用量、失败信息）"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithString("run_id", mcp.Required(), mcp.Description("run 标识（run_ 前缀）")),
			),
			handler: d.runGet,
		},
		{
			name: "run_events_tail",
			tool: mcp.NewTool("run_events_tail",
				mcp.WithDescription("取 run 事件尾部窗口（默认 50 条、上限 200；按 run_seq 正序返回）"),
				mcp.WithReadOnlyHintAnnotation(true),
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
				mcp.WithString("work_item_id", mcp.Required(), mcp.Description("任务标识（wi_ 前缀）")),
				mcp.WithString("reason", mcp.Description("打回原因（落 activity，建议写明评审意见）")),
				mcp.WithNumber("expected_version", mcp.Required(), mcp.Description("乐观锁版本（task_get 返回的 version）")),
			),
			handler: d.taskReturn,
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
	b, err := json.Marshal(v)
	if err != nil {
		return mcp.NewToolResultError("序列化输出失败: " + err.Error()), nil
	}
	return mcp.NewToolResultText(string(b)), nil
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
	filter := application.WorkItemFilter{}
	if status := req.GetString("status", ""); status != "" {
		st, ok := workItemStatuses[status]
		if !ok {
			return mcp.NewToolResultError("非法 status: " + status + "（合法值 todo/in_progress/blocked/completed/cancelled）"), nil
		}
		filter.Status = st
	}
	items, _, err := d.store.WorkItems().List(ctx, workspaceID, filter)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(map[string]any{"items": items})
}

func (d *toolDeps) taskGet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := req.RequireString("work_item_id")
	if err != nil {
		return mcp.NewToolResultError("work_item_id 必填: " + err.Error()), nil
	}
	wi, err := d.store.WorkItems().Get(ctx, id)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(wi)
}

func (d *toolDeps) runList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	wiID, err := req.RequireString("work_item_id")
	if err != nil {
		return mcp.NewToolResultError("work_item_id 必填: " + err.Error()), nil
	}
	runs, err := d.store.Runs().ListByWorkItem(ctx, wiID)
	if err != nil {
		return toolErr(err)
	}
	if runs == nil {
		runs = []*domain.ExecutionRun{}
	}
	return jsonResult(map[string]any{"runs": runs})
}

func (d *toolDeps) runGet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	runID, err := req.RequireString("run_id")
	if err != nil {
		return mcp.NewToolResultError("run_id 必填: " + err.Error()), nil
	}
	run, err := d.store.Runs().Get(ctx, runID)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(run)
}

func (d *toolDeps) runEventsTail(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	evs, err := d.store.Events().ListRunEvents(ctx, runID)
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
	runID, err := req.RequireString("run_id")
	if err != nil {
		return mcp.NewToolResultError("run_id 必填: " + err.Error()), nil
	}
	approvals, err := d.store.Runs().ListApprovals(ctx, runID)
	if err != nil {
		return toolErr(err)
	}
	if approvals == nil {
		approvals = []*domain.ApprovalRequest{}
	}
	return jsonResult(map[string]any{"approvals": approvals})
}

func (d *toolDeps) taskClaim(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	wi, err := d.svc.ClaimWorkItem(ctx, wiID, agentID, version)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(wi)
}

func (d *toolDeps) taskReturn(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	wiID, err := req.RequireString("work_item_id")
	if err != nil {
		return mcp.NewToolResultError("work_item_id 必填: " + err.Error()), nil
	}
	version, err := req.RequireInt("expected_version")
	if err != nil {
		return mcp.NewToolResultError("expected_version 必填: " + err.Error()), nil
	}
	wi, err := d.svc.ReturnWorkItem(ctx, wiID, req.GetString("reason", ""), version)
	if err != nil {
		return toolErr(err)
	}
	return jsonResult(wi)
}
