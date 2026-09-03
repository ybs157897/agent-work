// plan_extract.go M2 lead-as-planner 入口：run 终态后从助手最终文本解码
// PlanDecisionV2，走 SubmitPlan 同一校验+执行路径（设计 note
// notes/implemented/orchestration/2026-08-23-m2-lead-planner-evaluation.md §1）。
// 门控 agent.Role=="lead"；无 plan 块为正常 no-op；解析/校验失败不静默，
// 主任务落 plan_parse_failed blocker。幂等由 plans.source_run_id 唯一索引兜底。
package application

import (
	"context"
	"errors"
	"log"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// AgentRoleLead plan 提取门控的显式角色约定（配置可见，不加 schema 列）。
const AgentRoleLead = "lead"

// maybeExtractPlan lead run 终态钩子（RecordRunStatus 终态提交后、事务外，
// maybeAdvancePlans 同款位置；尽力而为）。plan 落在该 run 所属 work item 上。
// 返回值（acted, err）只是给 run journal 埋点的信号（runs.go post 相位）：
// acted=落 blocker 或提交 plan 等实际动作；err=静默失败（证据读取/提交失败且
// 无 blocker 兜底）。控制流不变：失败依旧只记日志/落 blocker。
func (s *Service) maybeExtractPlan(ctx context.Context, r *domain.ExecutionRun) (bool, error) {
	if r == nil || r.Status != domain.RunSucceeded || r.AgentProfileID == "" {
		return false, nil
	}
	if action, _ := coordinatorContextOf(r)["action"].(string); action == "evaluation" {
		// Evaluation output is consumed by maybeProcessVerdict; it is not a
		// Planner decision, even when a delegated target Agent owns the Plan.
		return false, nil
	}
	wctx := context.WithoutCancel(ctx)
	wi, err := s.store.WorkItems().Get(wctx, r.WorkItemID)
	if err != nil || !isTaskWorkItem(wi) {
		return false, nil
	}
	agent, err := s.store.Agents().Get(wctx, r.AgentProfileID)
	if err != nil || (agent.Role != AgentRoleLead && !isGovernedCoordinatorRun(r)) {
		return false, nil
	}
	if isGovernedCoordinatorRun(r) {
		state, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(wctx, r.WorkItemID)
		if stateErr != nil {
			return false, nil
		}
		// Once a Handoff has been accepted, the source Run is fenced even if a
		// late terminal frame still carries a PlanDecision. Preserve that frame
		// as ordinary Run evidence only; the durable continuation owns the next
		// Plan and is activated by maybeAdvanceTaskCoordinator below.
		if handoffContinuationPending(state) &&
			stringValue(state.Data[coordinatorHandoffSourceRunIDKey]) == r.ID {
			return false, nil
		}
		if state.CurrentRunID != r.ID {
			return false, nil
		}
	}
	text, err := s.runFinalText(wctx, r.ID)
	if err != nil {
		log.Printf("plan: run %s 最终文本读取失败: %v", r.ID, err)
		if isGovernedCoordinatorRun(r) {
			action, _ := coordinatorContextOf(r)["action"].(string)
			if action != "evaluation" {
				s.blockForParseFailure(wctx, r, "plan_read_failed", "Coordinator 计划证据读取失败："+err.Error())
			}
		}
		return false, err
	}
	if isGovernedCoordinatorRun(r) {
		s.processSystemCoordinatorPlanDecision(wctx, r, text)
		return true, nil
	}
	decision, source, found, err := DecodeCoordinatorPlanText(text)
	if !found {
		return false, nil // lead 可以只聊不派
	}
	if err != nil {
		s.blockForParseFailure(wctx, r, "plan_parse_failed", err.Error())
		return true, nil
	}
	steps, err := PlanDecisionStepInputs(decision)
	if err != nil {
		s.blockForParseFailure(wctx, r, "plan_parse_failed", err.Error())
		return true, nil
	}
	if _, err := s.SubmitPlan(wctx, r.WorkspaceID, SubmitPlanParams{
		WorkItemID: r.WorkItemID, AgentProfileID: r.AgentProfileID,
		SourceRunID: r.ID, Steps: steps,
		DecisionAudit: &PlanDecisionAuditInput{
			SchemaVersion: decision.SchemaVersion, Candidate: source,
			Reason: decision.Reason, NextAction: decision.NextAction, StepCount: len(decision.Steps),
		},
	}); err != nil {
		switch {
		case errors.Is(err, domain.ErrIdempotencyConflict):
			return false, nil // source_run_id 唯一索引：同 run 二次终态事件不重复提取
		case errors.Is(err, domain.ErrValidation):
			s.blockForParseFailure(wctx, r, "plan_parse_failed", err.Error())
			return true, nil
		default:
			log.Printf("plan: run %s 提取的 plan 提交失败: %v", r.ID, err)
			return false, err
		}
	}
	return true, nil
}

// blockForParseFailure plan/verdict 提取失败的统一兜底：主任务落 blocker
// （code 见调用方），人可见可修。work item 已 blocked/终态等无法迁移时只记
// 日志，不重试（终态事件不会再来）。
func (s *Service) blockForParseFailure(ctx context.Context, r *domain.ExecutionRun, code, message string) {
	wi, err := s.store.WorkItems().Get(ctx, r.WorkItemID)
	if err != nil {
		log.Printf("%s: 读取 work item %s 失败: %v", code, r.WorkItemID, err)
		return
	}
	if _, err := s.BlockWorkItem(ctx, wi.ID, BlockParams{
		Code: code, Message: message, Source: "control_plane",
	}, wi.Version); err != nil {
		log.Printf("%s: 主任务 %s 落 blocker 失败: %v", code, wi.ID, err)
	}
}
