package application

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
)

// 注册表缺 context_window 时的保守回退窗口；预算按窗口比例推导。
const historyBudgetFallbackWindow = int64(32768)

// historyBudgetTokens 内联历史回放的 token 预算：模型上下文窗口的 35%，
// 其余留给系统提示、工具定义、当轮指令与回答。窗口来自 models/ 注册表。
func historyBudgetTokens(spec orchestrator.ModelSpec) int64 {
	window := int64(spec.ContextWindow)
	if window <= 0 {
		window = historyBudgetFallbackWindow
	}
	return window * 35 / 100
}

// estimateTokens 粗估文本 token 量：CJK 一字一 token，其余四字符一 token。
// 只作轮换触发信号用；provider 实际计量以 usage 回报为准。
func estimateTokens(text string) int64 {
	var cjk, other int64
	for _, r := range text {
		if r >= 0x4E00 && r <= 0x9FFF {
			cjk++
		} else {
			other++
		}
	}
	return cjk + other/4
}

// historyExceedsBudget 判定内联历史是否超出模型窗口预算（触发会话轮换
// 而非截断：砍头会移动请求前缀，令 provider 前缀缓存持续清零）。
func historyExceedsBudget(history []map[string]any, spec orchestrator.ModelSpec) bool {
	budget := historyBudgetTokens(spec)
	var used int64
	for _, message := range history {
		if text, ok := message["text"].(string); ok {
			used += estimateTokens(text)
		}
	}
	return used > budget
}

// conversationHistory 从 canonical run_events 构造 provider 无关的对话回放。
// 只保留用户原始输入与最终 assistant 文本，工具参数、推理和敏感审批内容不进入上下文。
func (s *Service) conversationHistory(ctx context.Context, runs []*domain.ExecutionRun) ([]map[string]any, error) {
	var messages []map[string]any
	for _, run := range runs {
		instruction, _ := run.Input["instruction"].(string)
		if text := strings.TrimSpace(instruction); text != "" {
			messages = appendHistoryMessage(messages, "user", text)
		}
		assistant, err := s.runFinalText(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		if assistant != "" {
			messages = appendHistoryMessage(messages, "assistant", assistant)
		}
	}
	return messages, nil
}

// runFinalText 单个 run 的助手最终文本：message.completed 按序拼接，
// 无 completed 时以 delta 全量兜底（与 plan/verdict 提取同一文本来源）。
func (s *Service) runFinalText(ctx context.Context, runID string) (string, error) {
	events, err := s.store.Events().ListRunEvents(ctx, runID)
	if err != nil {
		return "", err
	}
	var completed []string
	var deltas strings.Builder
	for _, event := range events {
		switch event.EventType {
		case domain.EventMessageCompleted:
			if text := eventText(event.Payload); text != "" {
				completed = append(completed, text)
			}
		case domain.EventMessageDelta:
			if role, _ := event.Payload["role"].(string); role == "user" {
				continue
			}
			if text := eventDeltaText(event.Payload); text != "" {
				deltas.WriteString(text)
			}
		}
	}
	assistant := strings.TrimSpace(strings.Join(completed, "\n"))
	if assistant == "" {
		assistant = strings.TrimSpace(deltas.String())
	}
	return assistant, nil
}

func appendHistoryMessage(messages []map[string]any, role, text string) []map[string]any {
	return append(messages, map[string]any{"role": role, "text": text})
}

func eventText(payload map[string]any) string {
	if text, _ := payload["text"].(string); strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text)
	}
	return eventDeltaText(payload)
}

func eventDeltaText(payload map[string]any) string {
	raw, ok := payload["raw"]
	if !ok {
		if text, _ := payload["text"].(string); text != "" {
			return text
		}
		return ""
	}
	// Codex app-server 的 delta params 在不同版本可能是对象或 JSON 文本。
	var value any = raw
	if encoded, ok := raw.(string); ok {
		if json.Unmarshal([]byte(encoded), &value) != nil {
			return encoded
		}
	}
	return findText(value)
}

func findText(value any) string {
	switch v := value.(type) {
	case map[string]any:
		if text, _ := v["text"].(string); text != "" {
			return text
		}
		if delta, _ := v["delta"].(string); delta != "" {
			return delta
		}
		for _, key := range []string{"chunk", "message", "item", "content"} {
			if text := findText(v[key]); text != "" {
				return text
			}
		}
	case []any:
		var b strings.Builder
		for _, item := range v {
			b.WriteString(findText(item))
		}
		return b.String()
	}
	return ""
}

func resumablePreviousRun(runs []*domain.ExecutionRun, adapterID, runtimeLabel, configDigest string) *domain.ExecutionRun {
	if len(runs) == 0 {
		return nil
	}
	previous := runs[len(runs)-1]
	if previous.Status != domain.RunSucceeded || strings.TrimSpace(previous.SessionRef) == "" {
		return nil
	}
	if adapterID != "" && previous.AdapterID != "" && previous.AdapterID != adapterID {
		return nil
	}
	if previous.RuntimeLabel != runtimeLabel {
		return nil
	}
	conversation, _ := previous.Input["conversation"].(map[string]any)
	previousDigest, _ := conversation["config_digest"].(string)
	if previousDigest == "" || previousDigest != configDigest {
		return nil
	}
	return previous
}
