package claudecode

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// stream-json 帧（只解析映射需要的字段）。
type streamFrame struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	Message   json.RawMessage `json:"message"`
	Result    string          `json:"result"`
	IsError   bool            `json:"is_error"`
	SessionID string          `json:"session_id"`
	Usage     *frameUsage     `json:"usage"`
}

// frameUsage 是 result 帧内 CLI 报告的本轮 token 用量。
type frameUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// usage 换算为统一口径：print mode 每次调用一份 result 帧，故为 per_run 增量。
func (f *streamFrame) usage() *runtime.Usage {
	if f.Usage == nil {
		return nil
	}
	return &runtime.Usage{
		InputTokens:  f.Usage.InputTokens,
		OutputTokens: f.Usage.OutputTokens,
		CachedTokens: f.Usage.CacheReadInputTokens + f.Usage.CacheCreationInputTokens,
		Basis:        runtime.UsagePerRun,
	}
}

// frameTooLargeError 超过 MaxFrameBytes 的帧（协议违例 → FamilyInternal）。
type frameTooLargeError struct{ limit int }

func (e frameTooLargeError) Error() string { return fmt.Sprintf("frame exceeds %d bytes", e.limit) }

func assistantText(f *streamFrame) string {
	var msg struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(f.Message, &msg) != nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range msg.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

func resultError(f *streamFrame) string {
	if f.Result != "" {
		return f.Result
	}
	return "claude result subtype=" + f.Subtype
}

func readFrame(r *bufio.Reader, maxBytes int) (*streamFrame, error) {
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	if len(line) > maxBytes {
		return nil, frameTooLargeError{limit: maxBytes}
	}
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		if err != nil {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, nil
	}
	var f streamFrame
	if err := json.Unmarshal([]byte(trimmed), &f); err != nil {
		return nil, nil // 非 JSON 行：隔离不执行
	}
	return &f, nil
}
