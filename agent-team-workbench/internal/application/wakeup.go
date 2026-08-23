// wakeup.go M4 唤醒调度的应用面接线：
// Service 实现 scheduling.RunStarter（消费 → CreateRun），
// 并提供 on_demand 手动唤醒入口与 assignment 指派入队钩子。
package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/scheduling"
)

// 编译期保证 Service 可直接作为调度循环的 RunStarter 注入。
var _ scheduling.RunStarter = (*Service)(nil)

// CreateRunForWakeup 把一次唤醒消费转成 CreateRun（taskKey 即 work item id）。
// instruction 已由调度侧渲染（wakeContext["instruction"] 显式指令优先在此兜底）。
func (s *Service) CreateRunForWakeup(ctx context.Context, workspaceID, agentProfileID, taskKey, instruction string, wakeContext map[string]any) (string, error) {
	if strings.TrimSpace(instruction) == "" {
		instruction, _ = wakeContext["instruction"].(string)
	}
	if strings.TrimSpace(instruction) == "" {
		return "", fmt.Errorf("%w: wakeup instruction empty", domain.ErrValidation)
	}
	run, err := s.CreateRun(ctx, taskKey, CreateRunParams{
		AgentProfileID: agentProfileID,
		Instruction:    instruction,
		WakeContext:    wakeContext,
	})
	if err != nil {
		return "", err
	}
	return run.ID, nil
}

// WakeResult 手动唤醒端点的响应。
type WakeResult struct {
	WakeupID string    `json:"wakeup_id"`
	Status   string    `json:"status"`
	WakeAt   time.Time `json:"wake_at"`
}

// RequestAgentWake 是 on_demand 手动唤醒入口（POST commands/wake）。
// 入队后由调度循环统一消费（≤一个 tick），保证 coalescing 判定单点一致。
func (s *Service) RequestAgentWake(ctx context.Context, agentProfileID, taskKey, instruction string, extra map[string]any) (*WakeResult, error) {
	if strings.TrimSpace(taskKey) == "" {
		return nil, fmt.Errorf("%w: task_key required（唤醒需锚定 work item）", domain.ErrValidation)
	}
	agent, err := s.store.Agents().Get(ctx, agentProfileID)
	if err != nil {
		return nil, err
	}
	if !agent.Heartbeat().WakeOnDemand {
		return nil, fmt.Errorf("%w: agent 未开启手动唤醒（wake_on_demand）", domain.ErrValidation)
	}
	wakeContext := map[string]any{}
	for k, v := range extra {
		wakeContext[k] = v
	}
	if strings.TrimSpace(instruction) != "" {
		wakeContext["instruction"] = instruction
	}
	// work item 标题供模板 {{work_item.title}}；task_key 非 work item 时忽略。
	if wi, err := s.store.WorkItems().Get(ctx, taskKey); err == nil && wi != nil {
		wakeContext["work_item_title"] = wi.Title
	}
	w, err := scheduling.EnqueueWakeup(ctx, s.store.Wakeups(), domain.WakeupSourceOnDemand,
		agent.WorkspaceID, agentProfileID, taskKey, wakeContext, time.Time{})
	if err != nil {
		return nil, err
	}
	_ = s.activity(ctx, agent.WorkspaceID, "agent.wake_requested",
		fmt.Sprintf("手动唤醒 agent %s（task %s，wakeup %s）", agentProfileID, taskKey, w.ID))
	return &WakeResult{WakeupID: w.ID, Status: string(domain.WakeupStatusQueued), WakeAt: w.WakeAt}, nil
}

// enqueueAssignmentWake 是指派事件的唤醒钩子（AssignWorkItem 提交后调用，尽力而为）：
// agent 开启 wake_on_assignment 时入队 assignment 源唤醒，由调度循环消费。
func (s *Service) enqueueAssignmentWake(ctx context.Context, wi *domain.WorkItem, agentProfileID string) {
	agent, err := s.store.Agents().Get(ctx, agentProfileID)
	if err != nil || !agent.Heartbeat().WakeOnAssignment {
		return
	}
	w, err := scheduling.EnqueueWakeup(ctx, s.store.Wakeups(), domain.WakeupSourceAssignment,
		wi.WorkspaceID, agentProfileID, wi.ID,
		map[string]any{"work_item_title": wi.Title}, time.Time{})
	if err != nil {
		return
	}
	_ = s.activity(ctx, wi.WorkspaceID, "agent.wake_enqueued",
		fmt.Sprintf("指派唤醒入队（agent %s / task %s / wakeup %s）", agentProfileID, wi.ID, w.ID))
}
