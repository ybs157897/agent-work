package application

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

const maxConversationHistoryBytes = 48 * 1024

// conversationHistory 从 canonical run_events 构造 provider 无关的对话回放。
// 只保留用户原始输入与最终 assistant 文本，工具参数、推理和敏感审批内容不进入上下文。
func (s *Service) conversationHistory(ctx context.Context, runs []*domain.ExecutionRun) ([]map[string]any, error) {
	var messages []map[string]any
	used := 0
	for _, run := range runs {
		instruction, _ := run.Input["instruction"].(string)
		if text := strings.TrimSpace(instruction); text != "" {
			messages, used = appendHistoryMessage(messages, used, "user", text)
		}
		events, err := s.store.Events().ListRunEvents(ctx, run.ID)
		if err != nil {
			return nil, err
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
		if assistant != "" {
			messages, used = appendHistoryMessage(messages, used, "assistant", assistant)
		}
	}
	return trimRecentHistory(messages), nil
}

func appendHistoryMessage(messages []map[string]any, used int, role, text string) ([]map[string]any, int) {
	return append(messages, map[string]any{"role": role, "text": text}), used + len(text)
}

func trimRecentHistory(messages []map[string]any) []map[string]any {
	used := 0
	start := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		text, _ := messages[i]["text"].(string)
		if used+len(text) > maxConversationHistoryBytes {
			remaining := maxConversationHistoryBytes - used
			if remaining > 0 {
				cut := len(text) - remaining
				for cut < len(text) && !utf8.RuneStart(text[cut]) {
					cut++
				}
				copy := map[string]any{"role": messages[i]["role"], "text": text[cut:]}
				messages[i] = copy
				start = i
			}
			break
		}
		used += len(text)
		start = i
	}
	return messages[start:]
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
