// evaluate.go M2 评估 run 与 verdict 处理（设计 note §2）：
// plan finish{evaluation:true} 落 finished 时自动为 plan owner 在主任务上创建
// 评估 run（确定性模板 + input.evaluation=true）；评估 run 终态后提取
// ```verdict 围栏块推动主任务 phase。打回重做由 lead 下一份 plan 表达，
// 控制平面不擅自重派。
package application

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// buildEvaluationInstruction 评估 run 的确定性指令模板：主任务说明与验收标准 +
// 各子任务结果摘要 + verdict 输出格式要求。全部来自库内权威状态，不经模型。
func (s *Service) buildEvaluationInstruction(ctx context.Context, plan *domain.Plan) (string, error) {
	wi, err := s.store.WorkItems().Get(ctx, plan.WorkItemID)
	if err != nil {
		return "", err
	}
	children, err := s.store.WorkItems().ListByParent(ctx, plan.WorkItemID)
	if err != nil {
		return "", err
	}
	for _, child := range children {
		if err := requireTaskWorkItem(child); err != nil {
			return "", err
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "你是任务「%s」的负责人，请评估该任务是否达到验收标准。\n\n", wi.Title)
	if wi.Description != "" {
		fmt.Fprintf(&b, "## 任务说明\n%s\n\n", wi.Description)
	}
	if criteria := s.mainTaskAcceptance(ctx, wi.ID); len(criteria) > 0 {
		b.WriteString("## 验收标准\n")
		for _, c := range criteria {
			fmt.Fprintf(&b, "- %s\n", c)
		}
		b.WriteString("\n")
	}
	b.WriteString("## 子任务结果\n")
	if len(children) == 0 {
		b.WriteString("（本任务无子任务）\n")
	}
	for _, c := range children {
		fmt.Fprintf(&b, "- 「%s」：%s\n", c.Title, s.childResultSummary(ctx, c))
	}
	b.WriteString("\n## 输出要求\n")
	b.WriteString("请逐条对照验收标准给出评估结论，回复必须以如下围栏块收尾（pass 为布尔，reasons 为字符串数组）：\n\n")
	b.WriteString("```verdict\n{\"pass\": true, \"reasons\": [\"...\"]}\n```\n")
	return b.String(), nil
}

// mainTaskAcceptance 主任务验收标准来源：最近一次携带非空 acceptance_criteria
// 的 run 快照（权威写入，按 created_at 升序取最新）；无则空。
func (s *Service) mainTaskAcceptance(ctx context.Context, workItemID string) []string {
	runs, err := s.store.Runs().ListByWorkItem(ctx, workItemID)
	if err != nil {
		return nil
	}
	for i := len(runs) - 1; i >= 0; i-- {
		var out []string
		switch raw := runs[i].Input["acceptance_criteria"].(type) {
		case []any:
			for _, item := range raw {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					out = append(out, text)
				}
			}
		case []string:
			out = raw
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// childResultSummary 单个子任务的结果摘要：看板状态 + 最终 run 状态 + 最终
// 助手文本截断。读取失败降级为状态行（摘要缺失不阻断评估）。
func (s *Service) childResultSummary(ctx context.Context, c *domain.WorkItem) string {
	runs, err := s.store.Runs().ListByWorkItem(ctx, c.ID)
	if err != nil || len(runs) == 0 {
		return fmt.Sprintf("状态 %s，无 run", c.Status)
	}
	last := runs[len(runs)-1]
	text, err := s.runFinalText(ctx, last.ID)
	if err != nil || strings.TrimSpace(text) == "" {
		return fmt.Sprintf("状态 %s，最终 run %s", c.Status, last.Status)
	}
	return fmt.Sprintf("状态 %s，最终 run %s。摘要：%s", c.Status, last.Status, truncateRunes(text, 200))
}

// parseVerdict 解码 ```verdict 围栏内容：{"pass": bool, "reasons": [..]}。
// pass 缺失或非布尔视为解析失败（缺字段的 JSON 不能默认判为不通过）。
func parseVerdict(block string) (pass bool, reasons []string, err error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(block), &raw); err != nil {
		return false, nil, fmt.Errorf("verdict 块 JSON 解析失败: %w", err)
	}
	pass, ok := raw["pass"].(bool)
	if !ok {
		return false, nil, fmt.Errorf("verdict 块缺少布尔 pass 字段")
	}
	if list, ok := raw["reasons"].([]any); ok {
		for _, item := range list {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				reasons = append(reasons, text)
			}
		}
	}
	return pass, reasons, nil
}

// maybeProcessVerdict 评估 run 终态钩子（RecordRunStatus 终态提交后、事务外）：
// 提取 ```verdict 围栏块并推动主任务 phase——pass → acceptance（人等 Accept()
// 既有唯一完工路径）；fail → 回 execution + reasons 落 activity；缺失/解析失败 →
// verdict_parse_failed blocker（同 plan_parse_failed 策略）。
func (s *Service) maybeProcessVerdict(ctx context.Context, r *domain.ExecutionRun) {
	if r == nil || r.Status != domain.RunSucceeded {
		return
	}
	if eval, _ := r.Input["evaluation"].(bool); !eval {
		return
	}
	wctx := context.WithoutCancel(ctx)
	wi, err := s.store.WorkItems().Get(wctx, r.WorkItemID)
	if err != nil || !isTaskWorkItem(wi) {
		return
	}
	if isSystemCoordinatorRun(r) {
		state, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(wctx, r.WorkItemID)
		if stateErr != nil || state.CurrentRunID != r.ID {
			return
		}
	}
	text, err := s.runFinalText(wctx, r.ID)
	if err != nil {
		log.Printf("evaluation: run %s 最终文本读取失败: %v", r.ID, err)
		s.blockForParseFailure(wctx, r, "verdict_read_failed", "评估证据读取失败："+err.Error())
		return
	}
	block, ok := extractLastFencedBlock(text, "verdict")
	if !ok {
		s.blockForParseFailure(wctx, r, "verdict_parse_failed", "评估回复缺少 ```verdict 围栏块")
		return
	}
	pass, reasons, err := parseVerdict(block)
	if err != nil {
		s.blockForParseFailure(wctx, r, "verdict_parse_failed", err.Error())
		return
	}
	if pass {
		s.applyVerdictPass(wctx, wi)
		return
	}
	s.applyVerdictFail(wctx, wi, reasons)
}

// applyVerdictPass 主任务 review → acceptance（评估 run succeeded 已先经
// EnterReview 既有联动）。phase 迁移与事件同事务；失败记日志（人可重评估）。
func (s *Service) applyVerdictPass(ctx context.Context, wi *domain.WorkItem) {
	now := time.Now().UTC()
	if err := s.store.InTx(ctx, func(ctx context.Context) error {
		if err := wi.EnterAcceptance(now); err != nil {
			return err
		}
		if err := s.store.WorkItems().Update(ctx, wi, wi.Version-1); err != nil {
			return err
		}
		return s.emit(ctx, wi.WorkspaceID, domain.EventWorkItemUpdated,
			domain.AggregateWorkItem, wi.ID, wi.Version, nil,
			map[string]any{"phase": string(wi.Phase), "record_kind": string(workItemRecordKind(wi))})
	}); err != nil {
		log.Printf("evaluation: verdict pass 迁移 acceptance 失败（work item %s）: %v", wi.ID, err)
		return
	}
	// activity 归因 work_item_id（M4：verdict 级事件回溯到评估的主任务）。
	_ = s.activityFor(ctx, wi.WorkspaceID, wi.ID, "plan.verdict_passed",
		fmt.Sprintf("评估通过，任务「%s」进入待验收", wi.Title))
}

// applyVerdictFail 主任务回 execution（BeginExecution），verdict reasons 落
// activity；打回重做由 lead 下一份 plan 表达（M1 机制）。
func (s *Service) applyVerdictFail(ctx context.Context, wi *domain.WorkItem, reasons []string) {
	now := time.Now().UTC()
	if err := s.store.InTx(ctx, func(ctx context.Context) error {
		expected := wi.Version
		wi.BeginExecution(now)
		if err := s.store.WorkItems().Update(ctx, wi, expected); err != nil {
			return err
		}
		return s.emit(ctx, wi.WorkspaceID, domain.EventWorkItemUpdated,
			domain.AggregateWorkItem, wi.ID, wi.Version, nil,
			map[string]any{"phase": string(wi.Phase), "record_kind": string(workItemRecordKind(wi))})
	}); err != nil {
		log.Printf("evaluation: verdict fail 回 execution 失败（work item %s）: %v", wi.ID, err)
		return
	}
	message := "评估未通过，任务回到执行阶段"
	if len(reasons) > 0 {
		message += "：" + strings.Join(reasons, "；")
	}
	// activity 归因 work_item_id（M4：verdict 级事件回溯到评估的主任务）。
	_ = s.activityFor(ctx, wi.WorkspaceID, wi.ID, "plan.verdict_rejected", message)
}
