// plan_extract.go M2 lead-as-planner 入口：run 终态后从助手最终文本提取
// ```plan 围栏块，走 SubmitPlan 同一校验+执行路径（设计 note
// notes/implemented/orchestration/2026-08-23-m2-lead-planner-evaluation.md §1）。
// 门控 agent.Role=="lead"；无 plan 块为正常 no-op；解析/校验失败不静默，
// 主任务落 plan_parse_failed blocker。幂等由 plans.source_run_id 唯一索引兜底。
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// AgentRoleLead plan 提取门控的显式角色约定（配置可见，不加 schema 列）。
const AgentRoleLead = "lead"

// extractLastFencedBlock 提取文本中最后一个名为 name 的围栏代码块
// （```name 起、``` 止），返回围栏内原文（逐行保留）；不存在返回 false。
// 未闭合围栏宽容收尾（内容取到文本末尾），适配流式截断场景。
func extractLastFencedBlock(text, name string) (string, bool) {
	lines := strings.Split(text, "\n")
	var content []string
	inBlock, found := false, false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if strings.HasPrefix(trimmed, "```") {
				if strings.TrimSpace(strings.TrimPrefix(trimmed, "```")) == name {
					inBlock, found = true, true
					content = nil
				}
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			inBlock = false
			continue
		}
		content = append(content, line)
	}
	if !found {
		return "", false
	}
	return strings.Join(content, "\n"), true
}

// parsePlanBlock 把 ```plan 围栏内容解码为提交步骤：JSON 数组，每项含 verb
// 键 + 动词专属字段（与 openapi PlanStepSubmit 同构）。动词合法性交给
// SubmitPlan 的 parsePlanSteps 统一校验（同一执行路径）。
func parsePlanBlock(block string) ([]PlanStepInput, error) {
	var raw []map[string]any
	if err := json.Unmarshal([]byte(block), &raw); err != nil {
		return nil, fmt.Errorf("plan 块 JSON 解析失败: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("plan 块为空数组")
	}
	steps := make([]PlanStepInput, 0, len(raw))
	for i, item := range raw {
		verb, _ := item["verb"].(string)
		if verb == "" {
			return nil, fmt.Errorf("plan 步骤 %d 缺少 verb 键", i)
		}
		payload := make(map[string]any, len(item))
		for k, v := range item {
			if k == "verb" {
				continue
			}
			payload[k] = v
		}
		steps = append(steps, PlanStepInput{Verb: verb, Payload: payload})
	}
	return steps, nil
}

// maybeExtractPlan lead run 终态钩子（RecordRunStatus 终态提交后、事务外，
// maybeAdvancePlans 同款位置；尽力而为）。plan 落在该 run 所属 work item 上。
func (s *Service) maybeExtractPlan(ctx context.Context, r *domain.ExecutionRun) {
	if r == nil || r.Status != domain.RunSucceeded || r.AgentProfileID == "" {
		return
	}
	wctx := context.WithoutCancel(ctx)
	wi, err := s.store.WorkItems().Get(wctx, r.WorkItemID)
	if err != nil || !isTaskWorkItem(wi) {
		return
	}
	agent, err := s.store.Agents().Get(wctx, r.AgentProfileID)
	if err != nil || agent.Role != AgentRoleLead {
		return
	}
	text, err := s.runFinalText(wctx, r.ID)
	if err != nil {
		log.Printf("plan: run %s 最终文本读取失败: %v", r.ID, err)
		return
	}
	block, ok := extractLastFencedBlock(text, "plan")
	if !ok {
		return // lead 可以只聊不派
	}
	steps, err := parsePlanBlock(block)
	if err != nil {
		s.blockForParseFailure(wctx, r, "plan_parse_failed", err.Error())
		return
	}
	if _, err := s.SubmitPlan(wctx, r.WorkspaceID, SubmitPlanParams{
		WorkItemID: r.WorkItemID, AgentProfileID: r.AgentProfileID,
		SourceRunID: r.ID, Steps: steps,
	}); err != nil {
		switch {
		case errors.Is(err, domain.ErrIdempotencyConflict):
			return // source_run_id 唯一索引：同 run 二次终态事件不重复提取
		case errors.Is(err, domain.ErrValidation):
			s.blockForParseFailure(wctx, r, "plan_parse_failed", err.Error())
		default:
			log.Printf("plan: run %s 提取的 plan 提交失败: %v", r.ID, err)
		}
	}
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
