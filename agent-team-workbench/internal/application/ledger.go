// ledger.go 会话元模型 S2 的任务台账（Task Ledger）：片段关闭自动摘要与决策
// 原话台账。纪律（plan.md「S2 摘要纪律」）：无 LLM 调用——rolling_digest 为
// 确定性滚动摘要（状态行 + 最近 N 条摘录 + 计数，长度封顶）；决策原话 quote
// 禁止任何转述。台账不属于任何会话（D4：横向协作只走任务台账）。
package application

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// 台账摘要的确定性参数（包级变量便于测试覆写）。
var (
	// RollingDigestMaxMessages 摘录保留的最近消息条数（用户+助手合计，取尾部）。
	RollingDigestMaxMessages = 12
	// RollingDigestMaxRunesPerMsg 单条摘录截断宽度（runes），一行卡片可读；
	// 与 handoffMaxRunesPerMsg 同思路取更紧的宽度。
	RollingDigestMaxRunesPerMsg = 160
	// RollingDigestMaxRunes 摘要总长封顶（runes），超限整体截断。
	RollingDigestMaxRunes = 4000
)

// maybeSummarizeSegment 片段关闭自动摘要（RecordRunStatus 终态提交后、事务外
// 调用；尽力而为）：run 落终态即视为一段会话片段关闭，确定性重建 rolling_digest
// 整体覆盖写。重算语义天然幂等——同一 run 重放终态不产生重复内容（重放事务
// 本身会被状态机拒绝，钩子不执行；即便钩子被重复触发，重算输入不变则输出不变）。
// 并发终态经 work_items.version 守卫互斥，冲突方重读重算（有界重试，收敛到含
// 全部已关闭轮次的摘要）；失败只记日志，下一个终态钩子会整体重算自愈。
func (s *Service) maybeSummarizeSegment(ctx context.Context, r *domain.ExecutionRun) {
	if r == nil || !r.Status.IsTerminal() {
		return
	}
	wctx := context.WithoutCancel(ctx)
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := s.store.InTx(wctx, func(ctx context.Context) error {
			return s.refreshRollingDigest(ctx, r.WorkItemID)
		})
		if err == nil {
			return
		}
		if !errors.Is(err, domain.ErrVersionConflict) {
			log.Printf("ledger: rolling_digest 刷新失败（work item %s）: %v", r.WorkItemID, err)
			return
		}
		if attempt == maxAttempts {
			log.Printf("ledger: rolling_digest 刷新放弃（work item %s，版本冲突重试耗尽）", r.WorkItemID)
		}
	}
}

// refreshRollingDigest 事务内重算：素材与 CreateRun 的会话历史同源
// （conversationHistory，含指令/追加输入/助手正文/工具轨迹保真），整体重算后
// 以 version 守卫覆盖写。
func (s *Service) refreshRollingDigest(ctx context.Context, workItemID string) error {
	wi, err := s.store.WorkItems().Get(ctx, workItemID)
	if err != nil {
		return err
	}
	runs, err := s.store.Runs().ListByWorkItem(ctx, workItemID)
	if err != nil {
		return err
	}
	history, err := s.conversationHistory(ctx, runs)
	if err != nil {
		return err
	}
	digest := buildRollingDigest(wi, history, len(runs), lastTerminalAt(runs))
	return s.store.WorkItems().UpdateRollingDigest(ctx, workItemID, digest, wi.Version)
}

// buildRollingDigest 确定性滚动摘要：状态行（任务状态 + 轮数 + 最近活动时刻）
// + 最近 N 条消息摘录 + 总长封顶。时间锚取最近终态 run 的 FinishedAt（状态
// 派生，非挂钟）——同输入重算产出逐字节一致，重放不产生增量。
func buildRollingDigest(wi *domain.WorkItem, history []map[string]any, turnCount int, lastTerminal time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "【任务台账】%s\n", wi.Title)
	status := string(wi.Status)
	if wi.Phase != "" {
		status += "/" + string(wi.Phase)
	}
	b.WriteString("【状态】" + status + " · 已执行 " + strconv.Itoa(turnCount) + " 轮")
	if !lastTerminal.IsZero() {
		b.WriteString(" · 最近活动 " + lastTerminal.UTC().Format(time.RFC3339))
	}
	b.WriteString("\n")
	if len(history) > 0 {
		b.WriteString("【最近进展】\n")
		start := len(history) - RollingDigestMaxMessages
		if start < 0 {
			start = 0
		}
		for _, m := range history[start:] {
			role, _ := m["role"].(string)
			text, _ := m["text"].(string)
			if strings.TrimSpace(text) == "" {
				continue
			}
			label := "助手"
			if role == "user" {
				label = "用户"
			}
			fmt.Fprintf(&b, "- %s：%s\n", label, truncateRunes(text, RollingDigestMaxRunesPerMsg))
		}
	}
	out := b.String()
	if runes := []rune(out); len(runes) > RollingDigestMaxRunes {
		out = string(runes[:RollingDigestMaxRunes]) + "…"
	}
	return out
}

// lastTerminalAt 全部 run 中最近的终态时刻（无终态返回零值）。
func lastTerminalAt(runs []*domain.ExecutionRun) time.Time {
	var last time.Time
	for _, r := range runs {
		if r.FinishedAt != nil && r.FinishedAt.After(last) {
			last = *r.FinishedAt
		}
	}
	return last
}

// RecordDecisionParams 决策台账写入参数（POST /work-items/{id}/decisions）。
type RecordDecisionParams struct {
	// Quote 用户原话，必填（trim 后非空）；禁止 LLM 转述进 quote。
	Quote string
	// SourceRunID 可选：钉出来源轮次，必须属于该 work item。
	SourceRunID string
	// SourceRef 可选：链回片段内位置（消息序号等），原样存储不校验。
	SourceRef string
}

// RecordDecision 决策原话落台账（会话元模型 S2）：decision_entries 行与
// decision.created 事件同事务；请求级幂等由命令的 Idempotency-Key 负责。
func (s *Service) RecordDecision(ctx context.Context, workItemID string, p RecordDecisionParams) (*domain.DecisionEntry, error) {
	quote := strings.TrimSpace(p.Quote)
	if quote == "" {
		return nil, fmt.Errorf("%w: quote required（决策原话不得为空）", domain.ErrValidation)
	}
	var entry *domain.DecisionEntry
	var workspaceID string
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		wi, err := s.store.WorkItems().Get(ctx, workItemID)
		if err != nil {
			return err
		}
		workspaceID = wi.WorkspaceID
		if p.SourceRunID != "" {
			run, err := s.store.Runs().Get(ctx, p.SourceRunID)
			if err != nil {
				return fmt.Errorf("%w: source_run_id %s 不存在", domain.ErrValidation, p.SourceRunID)
			}
			if run.WorkItemID != wi.ID {
				return fmt.Errorf("%w: source_run_id %s 不属于该 work item", domain.ErrValidation, p.SourceRunID)
			}
		}
		entry = &domain.DecisionEntry{
			ID: domain.NewID(domain.PrefixDecision), WorkItemID: wi.ID,
			Quote: quote, SourceRunID: p.SourceRunID, SourceRef: p.SourceRef,
			CreatedAt: time.Now().UTC(),
		}
		if err := s.store.DecisionEntries().Create(ctx, entry); err != nil {
			return err
		}
		data := map[string]any{"work_item_id": entry.WorkItemID, "quote": entry.Quote}
		if entry.SourceRunID != "" {
			data["source_run_id"] = entry.SourceRunID
		}
		if entry.SourceRef != "" {
			data["source_ref"] = entry.SourceRef
		}
		if err := s.emit(ctx, workspaceID, domain.EventDecisionCreated,
			domain.AggregateDecision, entry.ID, 1, nil, data); err != nil {
			return err
		}
		return s.activityFor(ctx, workspaceID, wi.ID, "decision.created",
			"钉为决策："+truncateRunes(quote, 80))
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(workspaceID)
	return entry, nil
}

// DecisionsByWorkItem 台账决策列表（详情页冷启动）；任务不存在时 404。
func (s *Service) DecisionsByWorkItem(ctx context.Context, workItemID string) ([]*domain.DecisionEntry, error) {
	if _, err := s.store.WorkItems().Get(ctx, workItemID); err != nil {
		return nil, err
	}
	return s.store.DecisionEntries().ListByWorkItem(ctx, workItemID)
}
