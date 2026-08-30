// settlement.go 会话元模型 S3：worker→lead 回流契约与派发收口。
// 纪律（plan.md「S3 回流/降级纪律」）：成员全终态 → running→collecting +
// automation 唤醒 lead → 汇总 run（trigger=wakeup，同 dispatch_id）终态 →
// completed/degraded/cancelled；只唤醒一次（MarkCollecting CAS 硬保证）；
// lead-only 与 @直达批（lead_run_id NULL）无唤醒直接收口；不设独立 dispatch 超时器——
// run 层 lease/超时保证成员必落终态，dispatch 跟随收口（取舍留痕见最终 note）。
package application

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/scheduling"
)

// settleDispatchContextKey automation 唤醒 context 里标记「本唤醒产生汇总 run」
// 的键：CreateRunForWakeup 据此把汇总 run 挂回原批（DispatchID）并固化进
// input.wakeup；终态钩子据此识别收口主体。存量 wakeup 路径（timer/assignment/
// on_demand）不带该键，dispatch_id 保持为空、不参与收口判定。
const settleDispatchContextKey = domain.WakeupContextSettlementDispatchID

// settlementDigestRunes 汇总材料单成员一行摘要的截断宽度（与派发卡片同源）。
const settlementDigestRunes = 120

// settlementRunID 非空表示 r 是 S3 汇总 run（收口唤醒产生、挂回该批）。
func settlementRunID(r *domain.ExecutionRun) string {
	if r == nil {
		return ""
	}
	wake, _ := r.Input["wakeup"].(map[string]any)
	id, _ := wake[settleDispatchContextKey].(string)
	return id
}

// maybeSettleDispatch 派发收口钩子（RecordRunStatus 终态提交后、事务外调用；
// 尽力而为）。run 带 dispatch_id 才参与。分支：
//   - 汇总 run（input.wakeup 带 settle 标记）终态：同批非 wakeup 成员已全终态
//     （正常流程必然成立，按契约防御）→ 直接收口，绝不再次唤醒（防循环唯一收口点）；
//   - 普通成员（user_message/lead_plan）：非 wakeup 成员未全终态 → 不动；
//     全终态 → @直达批直接收口；接诊/lead_plan 批 running→collecting（CAS）
//   - automation 唤醒 lead；
//   - 批已终态：一律 no-op——迟到的 lead_plan 成员不复活、不再汇总、不再
//     唤醒（v1 已知限制：收口后新落终态的 run 不进入任何批次收口）。
//
// 迁移、渲染、入队、事件同事务：enqueue 失败整体回滚 collecting 迁移，由下一
// 个成员终态事件重试，批不会卡死在 collecting。
func (s *Service) maybeSettleDispatch(ctx context.Context, r *domain.ExecutionRun) {
	if r == nil || r.DispatchID == "" || !r.Status.IsTerminal() {
		return
	}
	wctx := context.WithoutCancel(ctx)
	err := s.store.InTx(wctx, func(ctx context.Context) error {
		return s.settleDispatchTx(ctx, r)
	})
	if err != nil {
		log.Printf("settle: 派发收口失败（run %s / dispatch %s）: %v", r.ID, r.DispatchID, err)
	}
}

func (s *Service) settleDispatchTx(ctx context.Context, r *domain.ExecutionRun) error {
	d, err := s.store.Dispatches().Get(ctx, r.DispatchID)
	if err != nil {
		return err
	}
	wi, err := s.store.WorkItems().Get(ctx, d.WorkItemID)
	if err != nil {
		return err
	}
	if err := requireTaskWorkItem(wi); err != nil {
		return err
	}
	// 已终态批 no-op（见函数头 v1 已知限制）。
	if d.Status.IsTerminal() {
		return nil
	}
	members, err := s.store.Runs().ListByDispatch(ctx, d.ID)
	if err != nil {
		return err
	}
	// members 是批次的完整成员集合（lead、worker 与可能已经创建的汇总
	// run）。workers 只保留实际 worker：lead 的结果不是回流材料，且全取消
	// 判定也只看 worker，避免汇总 run 成功后破坏“全 worker 取消”语义。
	participants := make([]*domain.ExecutionRun, 0, len(members))
	workers := make([]*domain.ExecutionRun, 0, len(members))
	for _, m := range members {
		memberItem, err := s.store.WorkItems().Get(ctx, m.WorkItemID)
		if err != nil {
			return err
		}
		if err := requireTaskWorkItem(memberItem); err != nil {
			return err
		}
		if settlementRunID(m) == d.ID {
			continue
		}
		participants = append(participants, m)
		if d.LeadRunID == "" || m.ID != d.LeadRunID {
			workers = append(workers, m)
		}
	}
	// 只有当前终态 run 自己是该批汇总 run，才拥有唯一收口资格。不能仅因
	// 批次里已经存在一个 queued/running 汇总 run，就让迟到的普通成员抢跑
	// 关闭批次。
	if settlementRunID(r) == d.ID {
		// a. 汇总 run 终态 → 收口（唯一收口点，不再唤醒）。
		for _, w := range participants {
			if !w.Status.IsTerminal() {
				return nil // 防御：汇总前成员必已全终态；未齐不收口
			}
		}
		return s.closeDispatch(ctx, d, members, workers)
	}
	// b. 普通成员：非 wakeup 成员未全终态 → 不动。
	for _, w := range participants {
		if !w.Status.IsTerminal() {
			return nil
		}
	}
	// 没有实际 worker 时，lead 自己就是全部交付，不再创建第二轮汇总
	// run。这样普通的 lead-only 派发与 @直达一样在 lead 终态时直接收口。
	if len(workers) == 0 {
		return s.closeDispatchFor(ctx, d, members, participants, wi.WorkspaceID)
	}
	if d.LeadRunID == "" {
		// @直达批：无 lead 汇总环节，直接收口（无唤醒）。
		return s.closeDispatchFor(ctx, d, members, workers, wi.WorkspaceID)
	}
	// 接诊/lead_plan 批：CAS 抢占唤醒资格，0 行 = 已被并发方迁移/收口 → 放弃。
	ok, err := s.store.Dispatches().MarkCollecting(ctx, d.ID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	d.Status = domain.DispatchCollecting
	if err := s.wakeSettlementLead(ctx, d, workers, wi); err != nil {
		return err
	}
	return s.emitDispatchUpdated(ctx, wi.WorkspaceID, d)
}

// closeDispatch 批次收口（成员全终态后的唯一终态写入口）。
func (s *Service) closeDispatch(ctx context.Context, d *domain.Dispatch, members, cancellationRuns []*domain.ExecutionRun) error {
	wi, err := s.store.WorkItems().Get(ctx, d.WorkItemID)
	if err != nil {
		return err
	}
	if err := requireTaskWorkItem(wi); err != nil {
		return err
	}
	return s.closeDispatchFor(ctx, d, members, cancellationRuns, wi.WorkspaceID)
}

// closeDispatchFor 收口口径（a/b 共用）：cancellationRuns 全 cancelled → cancelled
// （用户整批喊停不是部分失败，优先于降级）；否则任一成员（含汇总 run 自身）
// failed/cancelled/lost/interrupted → degraded；否则 completed。多 worker 批次的
// cancellationRuns 只含实际 worker；lead-only 与 @直达批次传入全部参与成员。
// 领域层 Transition 校验合法边，存储层 CloseStatus CAS 防并发双写。
func (s *Service) closeDispatchFor(ctx context.Context, d *domain.Dispatch, members, cancellationRuns []*domain.ExecutionRun, workspaceID string) error {
	to := domain.DispatchCompleted
	cancelled, degraded := 0, 0
	for _, w := range cancellationRuns {
		if w.Status == domain.RunCancelled {
			cancelled++
		}
	}
	for _, m := range members {
		switch m.Status {
		case domain.RunCancelled, domain.RunFailed, domain.RunLost, domain.RunInterrupted:
			degraded++
		}
	}
	switch {
	case len(cancellationRuns) > 0 && cancelled == len(cancellationRuns):
		to = domain.DispatchCancelled
	case degraded > 0:
		to = domain.DispatchDegraded
	}
	now := time.Now().UTC()
	if err := d.Transition(to, now); err != nil {
		return err
	}
	ok, err := s.store.Dispatches().CloseStatus(ctx, d.ID, to, now)
	if err != nil {
		return err
	}
	if !ok {
		return nil // 并发方已收口：让位，不重复发事件
	}
	d.ClosedAt = &now
	return s.emitDispatchUpdated(ctx, workspaceID, d)
}

// wakeSettlementLead 以 automation 源唤醒 lead 做汇总：渲染各成员结局摘要
// （agent、终态、一行结果摘要，与派发卡片同源数据），taskKey = 主任务 id——
// 汇总 run 落 lead 在主任务上的参与线（task_sessions 谱系长期续接，提案
// Open Question 4 的倾向解，不引入新机制）。须在 MarkCollecting 成功后的
// 同一事务内调用。
func (s *Service) wakeSettlementLead(ctx context.Context, d *domain.Dispatch, workers []*domain.ExecutionRun, wi *domain.WorkItem) error {
	leadRun, err := s.store.Runs().Get(ctx, d.LeadRunID)
	if err != nil {
		return err
	}
	if leadRun.AgentProfileID == "" {
		return fmt.Errorf("%w: 批次 %s 的 lead run %s 无 agent 归属", domain.ErrValidation, d.ID, d.LeadRunID)
	}
	lead, err := s.store.Agents().Get(ctx, leadRun.AgentProfileID)
	if err != nil {
		return err
	}
	agentNames := map[string]string{}
	if list, err := s.store.Agents().List(ctx, wi.WorkspaceID); err == nil {
		for _, a := range list {
			agentNames[a.ID] = a.Name
		}
	}
	lines, err := s.settlementLines(ctx, workers, agentNames)
	if err != nil {
		return err
	}
	instruction := scheduling.RenderPrompt(scheduling.SettlementPromptTemplate, lead, wi.Title,
		map[string]any{"settlement_lines": lines})
	wakeContext := map[string]any{
		"work_item_title":        wi.Title,
		settleDispatchContextKey: d.ID,
		"instruction":            instruction,
	}
	_, err = scheduling.EnqueueWakeup(ctx, s.store.Wakeups(), domain.WakeupSourceAutomation,
		wi.WorkspaceID, lead.ID, wi.ID, wakeContext, time.Time{})
	return err
}

// settlementLines 成员结局行：实际 worker 的 agent 名 + 终态 + 一行结果摘要。
// 摘要优先取最后一条 assistant message.completed；失败 run 没有完成正文时
// 使用可读失败原因；两者都没有时明确标注无结果并附原 instruction，避免把
// 任务描述伪装成交付结果。
func (s *Service) settlementLines(ctx context.Context, workers []*domain.ExecutionRun, agentNames map[string]string) (string, error) {
	lines := make([]string, 0, len(workers))
	for _, w := range workers {
		name := agentNames[w.AgentProfileID]
		if name == "" {
			name = w.AgentProfileID
		}
		summary, err := s.settlementRunSummary(ctx, w)
		if err != nil {
			return "", err
		}
		lines = append(lines, fmt.Sprintf("- %s：%s（%s）", name, w.Status, summary))
	}
	if len(lines) == 0 {
		return "（无成员结局可汇总）", nil
	}
	return strings.Join(lines, "\n"), nil
}

func (s *Service) settlementRunSummary(ctx context.Context, run *domain.ExecutionRun) (string, error) {
	events, err := s.store.Events().ListRunEvents(ctx, run.ID)
	if err != nil {
		return "", err
	}
	// ListRunEvents 按 run_seq 正序返回；从尾部寻找最后一条有效的 assistant
	// 完成正文，兼容旧 adapter 未填 role 的 canonical payload。
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.EventType != domain.EventMessageCompleted {
			continue
		}
		if role, ok := e.Payload["role"].(string); ok && strings.TrimSpace(role) != "" && role != "assistant" {
			continue
		}
		if text, ok := e.Payload["text"].(string); ok && strings.TrimSpace(text) != "" {
			return truncateRunes(strings.TrimSpace(text), settlementDigestRunes), nil
		}
	}

	// 失败原因是 Runtime 的可读事实，优先于 instruction 兜底，避免把任务
	// 描述误报为结果。Code 仅在 Message 缺失时作为次级可读原因。
	if run.Failure != nil {
		message := strings.TrimSpace(run.Failure.Message)
		code := strings.TrimSpace(run.Failure.Code)
		switch {
		case message != "" && code != "":
			return truncateRunes(fmt.Sprintf("失败：%s（%s）", message, code), settlementDigestRunes), nil
		case message != "":
			return truncateRunes("失败："+message, settlementDigestRunes), nil
		case code != "":
			return truncateRunes("失败："+code, settlementDigestRunes), nil
		}
	}

	instruction, _ := run.Input["instruction"].(string)
	instruction = truncateRunes(strings.TrimSpace(instruction), settlementDigestRunes)
	if instruction == "" {
		instruction = "（空）"
	}
	return "无结果正文；原指令：" + instruction, nil
}

// emitDispatchUpdated 发布 dispatch.updated（S3 起有生产者）：批次行变更
// （running→collecting 与收口）。payload 带 work_item_id/status，收口附
// closed_at；dispatch id 在信封 aggregate。
func (s *Service) emitDispatchUpdated(ctx context.Context, workspaceID string, d *domain.Dispatch) error {
	data := map[string]any{"work_item_id": d.WorkItemID, "status": string(d.Status),
		"record_kind": string(domain.RecordKindTask)}
	if d.ClosedAt != nil {
		data["closed_at"] = *d.ClosedAt
	}
	return s.emit(ctx, workspaceID, domain.EventDispatchUpdated,
		domain.AggregateDispatch, d.ID, 1, nil, data)
}
