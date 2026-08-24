package codexapp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// 本适配器以 Codex CLI 0.149.0 生成的 experimental v2 schema 为基线。
// 协议是省略 jsonrpc 字段的双向 JSON-RPC 2.0；stdio 每行一个 JSON frame。
const (
	adapterVersion       = "2.0.0"
	protocolSchemaSHA256 = "6f76cce25156d405f1da54f205751e38f7b9eb42246ac0742b9958dd60275350"
)

func initializeParams() map[string]any {
	return map[string]any{
		"clientInfo": map[string]any{
			"name": "agent_team_workbench", "title": "Agent Team Workbench", "version": adapterVersion,
		},
		// collaborationMode 属于 experimental v2；显式协商后才允许发送。
		"capabilities": map[string]any{"experimentalApi": true},
	}
}

func initializedNotification() map[string]any {
	return map[string]any{"method": "initialized", "params": map[string]any{}}
}

type itemEvent struct {
	Type   string
	ID     string
	Status string
	Tool   string
	Text   string
	Raw    map[string]any
}

func parseItemEvent(raw json.RawMessage) itemEvent {
	var envelope struct {
		Item map[string]any `json:"item"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Item == nil {
		return itemEvent{}
	}
	item := envelope.Item
	e := itemEvent{Raw: item}
	e.Type, _ = item["type"].(string)
	e.ID, _ = item["id"].(string)
	e.Status, _ = item["status"].(string)
	e.Text, _ = item["text"].(string)
	switch e.Type {
	case "commandExecution":
		e.Tool = "shell"
	case "fileChange":
		e.Tool = "file_change"
	case "mcpToolCall":
		server, _ := item["server"].(string)
		tool, _ := item["tool"].(string)
		e.Tool = strings.Trim(strings.Join([]string{server, tool}, "/"), "/")
	case "dynamicToolCall":
		e.Tool, _ = item["tool"].(string)
	case "webSearch":
		e.Tool = "web_search"
	}
	return e
}

func (e itemEvent) isTool() bool {
	return e.Tool != ""
}

// canonicalPayload 工具事件的 canonical 契约（与 kimiapp 对齐）：call_id/
// tool/status + item_type（codex 特有）。started 可附 args_summary（输入
// 摘要），completed/failed 可附 output（结果文本，截断防 run_events 膨胀）。
func (e itemEvent) canonicalPayload() map[string]any {
	out := map[string]any{"call_id": e.ID, "item_type": e.Type}
	if e.Tool != "" {
		out["tool"] = e.Tool
	}
	if e.Status != "" {
		out["status"] = e.Status
	}
	return out
}

// argsSummary 工具输入的一行摘要：commandExecution.command 必填且 started
// 即带；mcp/dynamic 工具回退到截断的 arguments JSON。
func (e itemEvent) argsSummary() string {
	switch e.Type {
	case "commandExecution":
		if cmd, _ := e.Raw["command"].(string); strings.TrimSpace(cmd) != "" {
			return truncateSummary(cmd)
		}
	case "mcpToolCall", "dynamicToolCall":
		if args, ok := e.Raw["arguments"]; ok && args != nil {
			if b, err := json.Marshal(args); err == nil {
				return truncateSummary(string(b))
			}
		}
	}
	return ""
}

// resultOutput 工具结果文本：commandExecution.aggregatedOutput；mcp/dynamic
// 工具的 result（失败时 error）紧凑 JSON。
func (e itemEvent) resultOutput() string {
	switch e.Type {
	case "commandExecution":
		out, _ := e.Raw["aggregatedOutput"].(string)
		return truncateOutput(out)
	case "mcpToolCall", "dynamicToolCall":
		if errVal, ok := e.Raw["error"]; ok && errVal != nil {
			if b, err := json.Marshal(errVal); err == nil {
				return truncateOutput(string(b))
			}
		}
		if res, ok := e.Raw["result"]; ok && res != nil {
			if b, err := json.Marshal(res); err == nil {
				return truncateOutput(string(b))
			}
		}
	}
	return ""
}

// truncateOutput 工具输出上限 2000 字符（完整输出可达 MB 级，不进事件流）。
func truncateOutput(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 2000 {
		return s[:2000] + "…"
	}
	return s
}

func toolCompletionEvent(e itemEvent) string {
	switch e.Status {
	case "failed", "declined":
		return domain.EventToolFailed
	default:
		return domain.EventToolCompleted
	}
}

type approvalRequestParams struct {
	ThreadID               string         `json:"threadId"`
	TurnID                 string         `json:"turnId"`
	ItemID                 string         `json:"itemId"`
	Reason                 string         `json:"reason"`
	Command                string         `json:"command"`
	CWD                    string         `json:"cwd"`
	Permissions            map[string]any `json:"permissions"`
	NetworkApprovalContext *struct {
		Host     string `json:"host"`
		Protocol string `json:"protocol"`
	} `json:"networkApprovalContext"`
}

func parseApprovalParams(raw json.RawMessage) approvalRequestParams {
	var p approvalRequestParams
	_ = json.Unmarshal(raw, &p)
	return p
}

func approvalKind(method string) string {
	switch method {
	case "item/fileChange/requestApproval":
		return "file_change"
	case "item/permissions/requestApproval":
		return "permissions"
	default:
		return "command"
	}
}

func approvalSummary(method string, p approvalRequestParams) string {
	if p.NetworkApprovalContext != nil && p.NetworkApprovalContext.Host != "" {
		return fmt.Sprintf("Codex 请求网络访问：%s://%s", p.NetworkApprovalContext.Protocol, p.NetworkApprovalContext.Host)
	}
	if p.Command != "" {
		return truncateSummary("Codex 请求执行命令：" + p.Command)
	}
	if p.Reason != "" {
		return truncateSummary("Codex 请求批准：" + p.Reason)
	}
	switch method {
	case "item/fileChange/requestApproval":
		return "Codex 请求应用文件变更"
	case "item/permissions/requestApproval":
		return "Codex 请求额外文件系统或网络权限"
	default:
		return "Codex 请求执行高风险操作"
	}
}

func truncateSummary(s string) string {
	if len(s) > 500 {
		return s[:500] + "…"
	}
	return s
}

func approvalResponse(method string, approved bool, p approvalRequestParams) map[string]any {
	switch method {
	case "item/permissions/requestApproval":
		permissions := map[string]any{}
		if approved && p.Permissions != nil {
			permissions = p.Permissions
		}
		return map[string]any{"permissions": permissions, "scope": "turn"}
	case "item/fileChange/requestApproval", "item/commandExecution/requestApproval":
		decision := "cancel"
		if approved {
			decision = "accept"
		}
		return map[string]any{"decision": decision}
	default:
		return map[string]any{}
	}
}

func isApprovalMethod(method string) bool {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval":
		return true
	default:
		return false
	}
}

// ── token 用量通知（thread/tokenUsage/updated）──────────────────────

// tokenUsageEvent 用量通知中构成本轮增量的 last 快照（input/cached/output 三计数）。
type tokenUsageEvent struct {
	TurnID string
	Input  int64
	Cached int64
	Output int64
}

// parseTokenUsageEvent 容错解析 token 用量通知 params：app-server v2 权威形状为
// camelCase（tokenUsage.last.{inputTokens,cachedInputTokens,outputTokens}，证据见
// notes/implemented/architecture/2026-08-24-codex-usage-telemetry.md）；core 协议
// 形状为 snake_case（info.last_token_usage.input_tokens…）。同一 TokenUsageInfo
// 的两种序列化都收：包裹键取 tokenUsage/token_usage/info（缺省视为 params 平铺），
// 增量键取 last/last_token_usage。ok=false 表示无增量快照（不计入用量）。
func parseTokenUsageEvent(raw json.RawMessage) (tokenUsageEvent, bool) {
	var params map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &params) != nil {
		return tokenUsageEvent{}, false
	}
	ev := tokenUsageEvent{TurnID: firstString(params, "turnId", "turn_id")}
	usage := firstMap(params, "tokenUsage", "token_usage", "info")
	if usage == nil {
		usage = params
	}
	last := firstMap(usage, "last", "last_token_usage")
	if last == nil {
		return ev, false
	}
	ev.Input = firstInt(last, "inputTokens", "input_tokens")
	ev.Cached = firstInt(last, "cachedInputTokens", "cached_input_tokens")
	ev.Output = firstInt(last, "outputTokens", "output_tokens")
	return ev, true
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, _ := m[k].(string); s != "" {
			return s
		}
	}
	return ""
}

func firstMap(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		if v, ok := m[k].(map[string]any); ok {
			return v
		}
	}
	return nil
}

func firstInt(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return int64(v)
		case int64:
			return v
		}
	}
	return 0
}

// ── JSONL 帧（Codex 省略 jsonrpc 字段；响应/请求/通知统一解析）────────

type rpcFrame struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func readFrame(r *bufio.Reader, maxBytes int) (*rpcFrame, error) {
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	if len(line) > maxBytes {
		return nil, fmt.Errorf("frame exceeds %d bytes", maxBytes)
	}
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		if err != nil {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, nil
	}
	var f rpcFrame
	if err := json.Unmarshal([]byte(trimmed), &f); err != nil {
		return nil, nil // 非 JSON 行：隔离不执行
	}
	return &f, nil
}

func rawString(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func codexDeltaText(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return nestedText(value)
}

func nestedText(value any) string {
	switch v := value.(type) {
	case map[string]any:
		for _, key := range []string{"delta", "text"} {
			if text, _ := v[key].(string); text != "" {
				return text
			}
		}
		for _, key := range []string{"item", "message", "content", "chunk"} {
			if text := nestedText(v[key]); text != "" {
				return text
			}
		}
	case []any:
		var b strings.Builder
		for _, item := range v {
			b.WriteString(nestedText(item))
		}
		return b.String()
	}
	return ""
}
