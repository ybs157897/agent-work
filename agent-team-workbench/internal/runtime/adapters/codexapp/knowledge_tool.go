package codexapp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/knowledgeclient"
)

const (
	knowledgeDynamicTransport = "codex_dynamic"
	knowledgeDynamicSchema    = "atw_knowledge/v1"
	knowledgeDynamicTool      = "atw_knowledge"
)

// knowledgeDynamicTools is the only dynamic tool exposed by this adapter. It
// is installed from the trusted Run capability marker, never from user input
// or a model supplied tool definition. The host validates the same contract
// again before invoking the Run-bound client.
func knowledgeDynamicTools(run *domain.ExecutionRun) []map[string]any {
	if !knowledgeDynamicAccess(run) {
		return nil
	}
	return []map[string]any{{
		"type":        "function",
		"name":        knowledgeDynamicTool,
		"description": "在当前工作区查询或记录知识。使用 ask 查询，read 按知识条目和版本阅读，record 提交自然语言内容；管理员负责来源核验、整理和版本。",
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string", "enum": []string{"ask", "read", "record"},
					"description": "要执行的知识操作。",
				},
				"question": map[string]any{
					"type": "string", "description": "ask 要查询的问题。",
				},
				"content": map[string]any{
					"type": "string", "description": "record 要交给管理员整理的自然语言内容。",
				},
				"title": map[string]any{
					"type": "string", "description": "record 的可选标题。",
				},
				"publish_intent": map[string]any{
					"type": "string", "enum": []string{"confirmed_requirement"},
					"description": "只有用户明确确认的需求或约定才填写。",
				},
				"item_id": map[string]any{
					"type": "string", "description": "read 要读取的知识条目 ID。",
				},
				"version": map[string]any{
					"type": "integer", "minimum": 1, "description": "read 的可选正整数版本。",
				},
			},
			"required": []string{"action"},
		},
	}}
}

func knowledgeDynamicAccess(run *domain.ExecutionRun) bool {
	if run == nil || run.Input == nil {
		return false
	}
	access, ok := run.Input["knowledge_access"].(map[string]any)
	if !ok {
		return false
	}
	transport, _ := access["transport"].(string)
	schema, _ := access["tool_schema"].(string)
	path, _ := access["access_file"].(string)
	digest, _ := access["token_digest"].(string)
	return strings.TrimSpace(transport) == knowledgeDynamicTransport &&
		strings.TrimSpace(schema) == knowledgeDynamicSchema &&
		strings.TrimSpace(path) != "" && strings.TrimSpace(digest) != ""
}

func (s *execStream) knowledgeDynamicTools() []map[string]any {
	return knowledgeDynamicTools(s.ex.Run)
}

type dynamicToolCallParams struct {
	Arguments json.RawMessage `json:"arguments"`
	CallID    string          `json:"callId"`
	Namespace *string         `json:"namespace"`
	ThreadID  string          `json:"threadId"`
	Tool      string          `json:"tool"`
	TurnID    string          `json:"turnId"`
}

func parseDynamicToolCall(raw json.RawMessage) (dynamicToolCallParams, error) {
	var params dynamicToolCallParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return params, fmt.Errorf("invalid dynamic tool call: %w", err)
	}
	if params.CallID == "" || params.ThreadID == "" || params.Tool == "" || params.TurnID == "" || len(params.Arguments) == 0 {
		return params, fmt.Errorf("dynamic tool call is missing required fields")
	}
	if params.Namespace != nil && strings.TrimSpace(*params.Namespace) != "" {
		return params, fmt.Errorf("dynamic tool namespace is unsupported")
	}
	return params, nil
}

// handleKnowledgeDynamicTool handles item/tool/call without invoking a shell.
// The HTTP client receives the Run's cancellation context, so cancelling the
// Run also cancels an in-flight ask/record wait and cannot leave a hidden job
// polling after the provider turn has ended.
func (s *execStream) handleKnowledgeDynamicTool(frame *rpcFrame) {
	if frame == nil || frame.ID == nil {
		return
	}
	if !knowledgeDynamicAccess(s.ex.Run) {
		s.sendRPCError(*frame.ID, -32601, "knowledge dynamic tool is not enabled for this Run")
		return
	}
	params, err := parseDynamicToolCall(frame.Params)
	if err != nil {
		s.sendRPCError(*frame.ID, -32602, err.Error())
		return
	}
	if params.Tool != knowledgeDynamicTool {
		s.sendRPCError(*frame.ID, -32601, "unsupported dynamic tool: "+params.Tool)
		return
	}
	s.mu.Lock()
	threadID, turnID := s.threadID, s.turnID
	s.mu.Unlock()
	if params.ThreadID != threadID || params.TurnID != turnID {
		s.sendRPCError(*frame.ID, -32602, "dynamic tool call is not bound to the active thread turn")
		return
	}
	access, _ := s.ex.Run.Input["knowledge_access"].(map[string]any)
	accessPath, _ := access["access_file"].(string)
	tokenDigest, _ := access["token_digest"].(string)
	client, err := knowledgeclient.NewRunBound(accessPath, tokenDigest, s.ex.Run.ID)
	if err != nil {
		s.sendDynamicToolResponse(*frame.ID, false, "知识工具不可用，请稍后重试。")
		s.ex.Callbacks.OnLog("codexapp", "knowledge dynamic tool rejected: "+truncateMessage(err.Error()))
		return
	}
	key := knowledgeDynamicIdempotencyKey(s.ex.Run.ID, params.CallID)
	result, err := client.Invoke(s.ctx, params.Arguments, key)
	if err != nil {
		if s.ctx.Err() != nil {
			s.sendDynamicToolResponse(*frame.ID, false, "知识工具调用已取消。")
			return
		}
		s.sendDynamicToolResponse(*frame.ID, false, "知识工具调用失败："+truncateKnowledgeToolError(err.Error()))
		return
	}
	s.sendDynamicToolResponse(*frame.ID, true, string(result))
}

func (s *execStream) sendRPCError(id int64, code int, message string) {
	_ = s.send(map[string]any{"id": id, "error": map[string]any{"code": code, "message": message}})
}

func (s *execStream) sendDynamicToolResponse(id int64, success bool, text string) {
	if strings.TrimSpace(text) == "" {
		text = "{}"
	}
	if len([]rune(text)) > maxDynamicToolOutputRunes {
		success = false
		text = "知识工具返回结果过大，未完整送达。"
	}
	_ = s.send(map[string]any{
		"id": id, "result": map[string]any{
			"success": success,
			"contentItems": []map[string]any{{
				"type": "inputText", "text": truncateDynamicToolOutput(text),
			}},
		},
	})
}

func knowledgeDynamicIdempotencyKey(runID, callID string) string {
	digest := sha256.Sum256([]byte(runID + "\x00" + callID))
	return "codex-dynamic:" + hex.EncodeToString(digest[:])
}

func truncateDynamicToolOutput(text string) string {
	const max = maxDynamicToolOutputRunes
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}

const maxDynamicToolOutputRunes = 1 << 20

func truncateKnowledgeToolError(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "请求未完成"
	}
	const max = 800
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}
