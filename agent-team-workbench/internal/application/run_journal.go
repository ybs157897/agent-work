package application

// run_journal.go Run Journal 查询面（设计：notes/proposed/architecture/
// 2026-09-02-run-journal-lifecycle-logging.md §4 playbook / §5 M3）。
// 把 run_events 里的 internal 环节事件（run.phase_entered/phase_closed/
// log_chunk/decision）装配成调试投影：环节时间线 + 原始输出统计 + 治理
// receipt 互链。只读——journal 是事件流的投影，不改任何写路径。

import (
	"context"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// RunJournal 是 GET /api/v1/runs/{run_id}/journal 的响应形状（openapi
// RunJournal schema 同形）。字段是调试面消费契约，不得漂移。
type RunJournal struct {
	RunID       string               `json:"run_id"`
	GeneratedAt time.Time            `json:"generated_at"`
	Phases      []RunJournalPhase    `json:"phases"`
	Log         RunJournalLogSummary `json:"log"`
	// Governance 治理互链：turn_receipt_phases.run_ids[] 含本 run 的最新
	// turn 的 receipt header 摘要；无治理引用的 run 为 null。
	Governance *RunJournalGovernance `json:"governance"`
	// Decisions 决策因果链（run.decision 投影；M3 前端暂不展示，API 先钉形状）。
	Decisions []RunJournalDecision `json:"decisions"`
}

// RunJournalPhase 是一对 phase_entered/phase_closed 的配对投影。
type RunJournalPhase struct {
	Phase   string `json:"phase"`
	Attempt int    `json:"attempt"`
	// EnteredAt/ClosedAt：ClosedAt 为 null = 只有 entered 没有配对 closed
	//（崩溃/卡死环节，即 playbook 的故障点形态）。
	EnteredAt time.Time  `json:"entered_at"`
	ClosedAt  *time.Time `json:"closed_at"`
	// Outcome 为 null = 未闭合；否则 "ok"|"failed"|"skipped"。
	Outcome *string `json:"outcome"`
	// DurationMS 取 closed 事件的 duration_ms；未闭合为 null。
	DurationMS *int64 `json:"duration_ms"`
	// Failure 只有 outcome=failed 的环节可能非 null；其余为 null。
	Failure *RunJournalPhaseFailure `json:"failure"`
	// Detail 环节证据：闭合取 closed 载荷除 phase/outcome/duration_ms/failure
	// 外的键；未闭合取 entered 载荷除 phase/attempt 外的键；无证据键为 null。
	Detail map[string]any `json:"detail"`
}

// RunJournalPhaseFailure 与 run 终态 failure 家族对齐（code/message/family/retryable）。
type RunJournalPhaseFailure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Family    string `json:"family,omitempty"`
	Retryable bool   `json:"retryable"`
}

// RunJournalLogSummary 是 run.log_chunk 的统计（D3 64KB 环形截断的可见性）。
type RunJournalLogSummary struct {
	Chunks    int  `json:"chunks"`
	Truncated bool `json:"truncated"`
}

// RunJournalGovernance 是 run 所属治理回合的 receipt 引用（header 摘要）。
type RunJournalGovernance struct {
	GoalID  string `json:"goal_id"`
	TodoID  string `json:"todo_id"`
	TurnSeq int64  `json:"turn_seq"`
	// Digest 是 receipt header 的 canonical_digest（校验/回放锚点）。
	Digest string `json:"digest"`
}

// RunJournalDecision 是 run.decision 的投影（非治理域决策因果链锚点）。
type RunJournalDecision struct {
	Kind       string    `json:"kind"`
	Reason     string    `json:"reason"`
	OccurredAt time.Time `json:"occurred_at"`
	// LinkRunID 跨 run 因果（原 run → 新 run）；空省略。
	LinkRunID string `json:"link_run_id,omitempty"`
	// Inputs 决策载荷除 kind/reason/link_run_id 外的关键输入证据；无输入为 null。
	Inputs map[string]any `json:"inputs"`
}

// GetRunJournal 装配单个 Run 的环节日志投影。run 不存在返回 ErrNotFound
// （HTTP 404）。环节按 (phase, attempt) 依 run_seq 配对：closed 匹配同键最近
// 一个未闭合 entered；只有 entered 没有 closed 的环节保留未闭合形态
// （closed_at/outcome/duration_ms 均为 null）。
func (s *Service) GetRunJournal(ctx context.Context, runID string) (*RunJournal, error) {
	if _, err := s.store.Runs().Get(ctx, runID); err != nil {
		return nil, err
	}
	events, err := s.store.Events().ListRunEventsIncludeInternal(ctx, runID)
	if err != nil {
		return nil, err
	}
	journal := &RunJournal{
		RunID:       runID,
		GeneratedAt: time.Now().UTC(),
		Phases:      []RunJournalPhase{},
		Decisions:   []RunJournalDecision{},
	}
	assembler := &runJournalAssembler{}
	for i := range events {
		ev := &events[i]
		switch ev.EventType {
		case domain.EventRunPhaseEntered:
			assembler.enter(ev)
		case domain.EventRunPhaseClosed:
			assembler.close(ev)
		case domain.EventRunLogChunk:
			journal.Log.Chunks++
			if v, ok := ev.Payload["truncated"].(bool); ok && v {
				journal.Log.Truncated = true
			}
		case domain.EventRunDecision:
			journal.Decisions = append(journal.Decisions, journalDecision(ev))
		}
	}
	for _, p := range assembler.phases {
		journal.Phases = append(journal.Phases, *p)
	}
	header, err := s.store.TurnReceipts().LatestTurnHeaderByRunID(ctx, runID)
	if err != nil {
		return nil, err
	}
	if header != nil {
		journal.Governance = &RunJournalGovernance{
			GoalID:  header.TurnKey.GoalID,
			TodoID:  header.TurnKey.TodoID,
			TurnSeq: header.TurnKey.TurnSeq,
			Digest:  header.CanonicalDigest,
		}
	}
	return journal, nil
}

// runJournalAssembler 装配环节时间线。phases 持指针以便 close 就地回填；
// open 是按 (phase, attempt) 待闭合的 LIFO 栈（同环节自愈重跑 attempt 递增，
// 配对取最近一次未闭合进入）。
type runJournalAssembler struct {
	phases []*RunJournalPhase
	open   []*RunJournalPhase
}

func (a *runJournalAssembler) enter(ev *RunEvent) {
	phase, _ := ev.Payload["phase"].(string)
	if phase == "" {
		return
	}
	entry := &RunJournalPhase{
		Phase:     phase,
		Attempt:   journalPayloadInt(ev.Payload["attempt"]),
		EnteredAt: ev.OccurredAt,
		Detail:    journalDetail(ev.Payload, "phase", "attempt"),
	}
	a.phases = append(a.phases, entry)
	a.open = append(a.open, entry)
}

func (a *runJournalAssembler) close(ev *RunEvent) {
	phase, _ := ev.Payload["phase"].(string)
	if phase == "" {
		return
	}
	// closed 载荷带 attempt 时按 (phase, attempt) 精确配对；canonical 生成器
	//（observability.PhaseClosedPayload）不带 attempt，此时匹配同环节最近一次
	// 未闭合进入（LIFO）。退化载荷（无配对 entered）直接忽略——journal 是
	// 投影不是校验器。
	wantAttempt, hasAttempt := journalPayloadNumber(ev.Payload["attempt"])
	for i := len(a.open) - 1; i >= 0; i-- {
		entry := a.open[i]
		if entry.Phase != phase || (hasAttempt && entry.Attempt != int(wantAttempt)) {
			continue
		}
		closedAt := ev.OccurredAt
		entry.ClosedAt = &closedAt
		if outcome, ok := ev.Payload["outcome"].(string); ok {
			entry.Outcome = &outcome
		}
		if d, ok := journalPayloadNumber(ev.Payload["duration_ms"]); ok {
			entry.DurationMS = &d
		}
		entry.Failure = journalPhaseFailure(ev.Payload["failure"])
		entry.Detail = journalDetail(ev.Payload, "phase", "outcome", "duration_ms", "failure")
		a.open = append(a.open[:i], a.open[i+1:]...)
		return
	}
}

func journalDecision(ev *RunEvent) RunJournalDecision {
	d := RunJournalDecision{
		Kind:       journalPayloadString(ev.Payload["kind"]),
		Reason:     journalPayloadString(ev.Payload["reason"]),
		OccurredAt: ev.OccurredAt,
		Inputs:     journalDetail(ev.Payload, "kind", "reason", "link_run_id"),
	}
	if link, ok := ev.Payload["link_run_id"].(string); ok && link != "" {
		d.LinkRunID = link
	}
	return d
}

func journalPhaseFailure(v any) *RunJournalPhaseFailure {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return &RunJournalPhaseFailure{
		Code:      journalPayloadString(m["code"]),
		Message:   journalPayloadString(m["message"]),
		Family:    journalPayloadString(m["family"]),
		Retryable: journalPayloadBool(m["retryable"]),
	}
}

// journalDetail 复制载荷中除保留键外的其余键（环节/决策证据）；没有剩余键
// 返回 nil（JSON null，不是空对象）。
func journalDetail(payload map[string]any, reserved ...string) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	reservedSet := make(map[string]struct{}, len(reserved))
	for _, k := range reserved {
		reservedSet[k] = struct{}{}
	}
	var out map[string]any
	for k, v := range payload {
		if _, skip := reservedSet[k]; skip {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(payload))
		}
		out[k] = v
	}
	return out
}

func journalPayloadString(v any) string {
	s, _ := v.(string)
	return s
}

func journalPayloadBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// journalPayloadInt 宽容读取整数（run_events 载荷经 JSON 解码后数值是
// float64；容忍 int/int64 便于进程内构造的载荷）。
func journalPayloadInt(v any) int {
	n, ok := journalPayloadNumber(v)
	if !ok {
		return 0
	}
	return int(n)
}

func journalPayloadNumber(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}
