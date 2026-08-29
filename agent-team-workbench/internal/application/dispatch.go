// dispatch.go 会话元模型 S1 的派发路由面：用户消息 @直达/接诊解析、批次创建
// 事件、plan 派生子 run 的批次归属解析（契约见
// notes/implemented/architecture/2026-08-29-session-meta-model.md）。
package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// resolveAtMentionAgent 解析 instruction 的 @名字 前缀（首个空白词），大小写
// 不敏感匹配本 workspace 的 agent_profiles.name；命中返回 agent id，无 @ 或
// 未命中返回空串（回退接诊）。名字含空格的 agent 无法经 @ 指名（只取首词），
// 属已知限制。指令原文不改写，@ 前缀随 instruction 进入 run。
func (s *Service) resolveAtMentionAgent(ctx context.Context, workspaceID, instruction string) (string, error) {
	trimmed := strings.TrimLeft(instruction, " \t")
	if !strings.HasPrefix(trimmed, "@") {
		return "", nil
	}
	token := strings.TrimPrefix(trimmed, "@")
	if i := strings.IndexAny(token, " \t\n\r"); i >= 0 {
		token = token[:i]
	}
	if token == "" {
		return "", nil
	}
	agents, err := s.store.Agents().List(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	for _, a := range agents {
		if strings.EqualFold(a.Name, token) {
			return a.ID, nil
		}
	}
	return "", nil
}

// emitDispatchCreated 发布 dispatch.created（aggregate=dispatch）：载荷带
// work_item_id/trigger/status，接诊与 lead_plan 批次附 lead_run_id。
func (s *Service) emitDispatchCreated(ctx context.Context, workspaceID string, d *domain.Dispatch) error {
	data := map[string]any{
		"work_item_id": d.WorkItemID,
		"trigger":      string(d.Trigger),
		"status":       string(d.Status),
	}
	if d.LeadRunID != "" {
		data["lead_run_id"] = d.LeadRunID
	}
	return s.emit(ctx, workspaceID, domain.EventDispatchCreated,
		domain.AggregateDispatch, d.ID, 1, nil, data)
}

// resolvePlanDispatchID 返回 plan dispatch verb 派生子 run 的批次归属：
// 优先继承 source run（lead 主线 run）的批次——子 run 与触发 plan 的用户消息
// 同批次；source run 无批次或批次不属于 plan 主任务（API 手动提交 plan、存量
// run）时落 trigger=lead_plan 兜底批次：同一 source run 幂等复用（审批续跑不再
// 重复建），无 source run 的手动 plan 每次提交独立成批。须在创建成员 run 的
// 同一事务内调用。
func (s *Service) resolvePlanDispatchID(ctx context.Context, plan *domain.Plan) (string, error) {
	if plan.SourceRunID != "" {
		src, err := s.store.Runs().Get(ctx, plan.SourceRunID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return "", err
		}
		if err == nil && src.DispatchID != "" {
			d, derr := s.store.Dispatches().Get(ctx, src.DispatchID)
			if derr != nil {
				return "", derr
			}
			if d.WorkItemID == plan.WorkItemID {
				return d.ID, nil
			}
		}
	}
	if plan.SourceRunID != "" {
		existing, err := s.store.Dispatches().ListByWorkItem(ctx, plan.WorkItemID)
		if err != nil {
			return "", err
		}
		for _, d := range existing {
			if d.Trigger == domain.DispatchTriggerLeadPlan && d.LeadRunID == plan.SourceRunID {
				return d.ID, nil
			}
		}
	}
	d := &domain.Dispatch{
		ID: domain.NewID(domain.PrefixDispatch), WorkItemID: plan.WorkItemID,
		Trigger: domain.DispatchTriggerLeadPlan, LeadRunID: plan.SourceRunID,
		Status: domain.DispatchRunning, CreatedAt: time.Now().UTC(),
	}
	if err := s.store.Dispatches().Create(ctx, d); err != nil {
		return "", err
	}
	if err := s.emitDispatchCreated(ctx, plan.WorkspaceID, d); err != nil {
		return "", err
	}
	return d.ID, nil
}
