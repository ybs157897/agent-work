package application

import (
	"context"
	"encoding/json"
	"fmt"
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

// historyExceedsBudget 判定内联历史是否超出模型窗口预算（超限时先试 digest
// 压缩、仍超才轮换：见 planHistoryReplay——砍头会移动请求前缀，令 provider
// 前缀缓存持续清零）。
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

// ── 回放保真与渐进压缩（设计 note notes/proposed/architecture/
// 2026-08-27-session-integrity-design.md）─────────────────────────────
//
// 每个历史 run 在回放中贡献两侧内容：
//   - 用户侧：原始指令全文 + 轮中追加输入（steering，按时间序）；
//   - 助手侧：最终文本 + 工具轨迹 digest 附录（一行一次调用）。
//
// 负向保证：推理链不进回放（reasoning delta 只服务实时流）；审批内容永不进回放
// ——run_events 里 approval.requested/resolved 只有空载荷事件行（动作与同意/拒绝
// 只落在 stream data、audit_logs、activities），没有干净的裁决来源就不硬造，
// 整类审批事件维持排除。回放路径也永不承诺与 provider 上下文等价，只承诺
// ≥ 工作台台账可呈现的信息档位。
var (
	// DigestRecentTurns digest 压缩时最近 K 轮保留全文。K=4 覆盖 lead→worker→
	// 评估一类的即时往返依赖（当前 plan、文件清单、报错栈），又给更早轮次的
	// 规则压缩留出足够下探空间；包级变量便于测试覆写。
	DigestRecentTurns = 4
	// DigestMaxAssistantRunes digest 压缩时更早轮次助手正文的截断长度。取 400
	// 与 handoffMaxRunesPerMsg 同尺度（「逐条截断控制膨胀」的既有口径）；工具
	// 轨迹附录不参与该截断。用户指令永不截断（任务意图不可损）。
	DigestMaxAssistantRunes = 400
	// DigestToolArgRunes 单条工具参数摘要的截断上限（runes）。adapter 侧
	// args_summary 本就 ≤200 runes 一行摘要，这里再压到一行可见宽度；完整入参
	// 留在 run_events 台账供 UI 展开，不进回放正文。
	DigestToolArgRunes = 80
	// DigestMaxToolLines 单轮工具轨迹附录的行数上限。轨迹是附录不是正文：
	// 保留最近 N 行（与老化压缩同向：越晚发生的调用离答案越近），更早的折叠成
	// 一行计数，防止畸形 run 的海量 tool.*/progress 把回放挤爆。
	DigestMaxToolLines = 20
)

const (
	// historySteeringHeader 该轮用户侧追加输入的固定标记；digest 压缩时
	// 用户侧消息原样保留，标记恒不被切分。
	historySteeringHeader = "[轮中追加输入]"
	// historyToolTraceHeader 工具轨迹附录的固定标记；digest 压缩时靠它把
	// 助手消息切成「正文 + 轨迹」，正文截断而轨迹原样保留。
	historyToolTraceHeader = "[本轮执行轨迹]"
)

// conversationHistory 从 canonical run_events 构造 provider 无关的对话回放。
// 保真口径见文件头注释；历史形态仍是扁平 role/text 消息（runtime 层
// ConversationSnapshotOf / EffectiveInstruction 三档语义不变）。
func (s *Service) conversationHistory(ctx context.Context, runs []*domain.ExecutionRun) ([]map[string]any, error) {
	var messages []map[string]any
	for _, run := range runs {
		events, err := s.store.Events().ListRunEvents(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		events = mainAgentEvents(events)
		if user := userReplayText(run, events); user != "" {
			messages = appendHistoryMessage(messages, "user", user)
		}
		assistant := strings.TrimSpace(completedOrDeltaText(events))
		if trace := toolTraceSection(events); trace != "" {
			if assistant != "" {
				assistant += "\n\n"
			}
			assistant += trace
		}
		if assistant != "" {
			messages = appendHistoryMessage(messages, "assistant", assistant)
		}
	}
	return messages, nil
}

// replayStats 回放观测量（session.decision.history_stats 口径）：turns = 用户侧
// 消息数（每个历史 run 至多一轮），est_tokens = 全部回放文本估算 token 合计。
func replayStats(history []map[string]any) (turns int, tokens int64) {
	for _, message := range history {
		text, _ := message["text"].(string)
		tokens += estimateTokens(text)
		if role, _ := message["role"].(string); role == "user" {
			turns++
		}
	}
	return turns, tokens
}

// userReplayText 该轮用户侧回放文本：原始指令在前，中途追加输入（SendRunInput）
// 以 [轮中追加输入] 标记按时间序并入——模型能看到「指令发出后用户又说了什么」。
func userReplayText(run *domain.ExecutionRun, events []RunEvent) string {
	instruction, _ := run.Input["instruction"].(string)
	b := strings.Builder{}
	b.WriteString(strings.TrimSpace(instruction))
	if steers := steeringInputs(events); len(steers) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(historySteeringHeader)
		for _, text := range steers {
			b.WriteString("\n")
			b.WriteString(text)
		}
	}
	return strings.TrimSpace(b.String())
}

// steeringInputs 按 run_seq 收集 run 中途的用户追加输入（message.delta
// role=user）；实时流语义不受影响——assistant 文本提取仍跳过这些事件。
func steeringInputs(events []RunEvent) []string {
	var out []string
	for _, event := range events {
		if event.EventType != domain.EventMessageDelta {
			continue
		}
		if role, _ := event.Payload["role"].(string); role != "user" {
			continue
		}
		if text := strings.TrimSpace(eventText(event.Payload)); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// toolTraceSection 该轮助手侧的工具轨迹 digest 附录；无工具事实返回空串。
// 原始 payload 不进回放：每次调用折叠为一行「工具名(参数摘要): 结果状态」。
func toolTraceSection(events []RunEvent) string {
	lines := toolTraceLines(events)
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(historyToolTraceHeader)
	if skipped := len(lines) - DigestMaxToolLines; skipped > 0 {
		fmt.Fprintf(&b, "\n…另有 %d 次更早的工具调用（略）", skipped)
		lines = lines[len(lines)-DigestMaxToolLines:]
	}
	for _, line := range lines {
		b.WriteString("\n")
		b.WriteString(line)
	}
	return b.String()
}

// toolCall 单次工具调用的回放要素：started 提供名称与参数，completed/failed
// 以 call_id 关联补结果状态。kimi/dsh 的完成帧只带 call_id，名称只能来自
// started 帧；孤儿完成帧兜底建行（标未知工具），有 started 无终局的落
// 「未完成」——不丢事实也不硬造内容。
type toolCall struct {
	name   string
	args   string
	status string // 空 = 无终局事件（中断 / 崩溃 / 尚在途）
}

func toolTraceLines(events []RunEvent) []string {
	byID := make(map[string]*toolCall)
	var order []*toolCall
	callFor := func(id string) *toolCall {
		c, ok := byID[id]
		if !ok {
			c = &toolCall{}
			byID[id] = c
			order = append(order, c)
		}
		return c
	}
	for _, event := range events {
		switch event.EventType {
		case domain.EventToolStarted:
			id, _ := event.Payload["call_id"].(string)
			if id == "" {
				id = fmt.Sprintf("seq:%d", event.RunSeq)
			}
			c := callFor(id)
			if name, _ := event.Payload["tool"].(string); name != "" {
				c.name = name
			}
			if args := toolArgsDigest(event.Payload); args != "" {
				c.args = args
			}
		case domain.EventToolCompleted, domain.EventToolFailed:
			status := "完成"
			if event.EventType == domain.EventToolFailed {
				status = "失败"
			}
			id, _ := event.Payload["call_id"].(string)
			if id == "" {
				continue // 无法关联任何调用的终局帧：丢弃（无信息量且不可归属）
			}
			c := callFor(id)
			if name, _ := event.Payload["tool"].(string); name != "" && c.name == "" {
				c.name = name
			}
			if args := toolArgsDigest(event.Payload); args != "" && c.args == "" {
				c.args = args
			}
			c.status = status
		}
	}
	lines := make([]string, 0, len(order))
	for _, c := range order {
		name := c.name
		if name == "" {
			name = "未知工具"
		}
		status := c.status
		if status == "" {
			status = "未完成"
		}
		line := "- " + name
		if c.args != "" {
			line += "(" + c.args + ")"
		}
		lines = append(lines, line+": "+status)
	}
	return lines
}

// toolArgsDigest 参数摘要：优先 adapter 已生成的一行 args_summary，其次原文
// args；折叠空白保证单行，截断到 DigestToolArgRunes。
func toolArgsDigest(payload map[string]any) string {
	raw, _ := payload["args_summary"].(string)
	if raw == "" {
		raw, _ = payload["args"].(string)
	}
	raw = strings.Join(strings.Fields(raw), " ")
	if raw == "" {
		return ""
	}
	return truncateRunes(raw, DigestToolArgRunes)
}

// historyReplayPlan CreateRun 时对内联回放的确定性档位判定结果（全程无状态）。
type historyReplayPlan struct {
	// Replay 实际注入请求的历史；handoff 档为 nil（注入的是摘要文本）。
	Replay []map[string]any
	// Tier full | digest | handoff（session.decision.history_tier 输入）。
	Tier string
	// Rotated true 表示 digest 后仍装不下 → 升级轮换（reason=budget）。
	Rotated bool
}

// planHistoryReplay 三档渐进压缩替代「超预算即轮换」悬崖：full（预算内全量）
// → digest（老化压缩后可容纳）→ handoff（压缩仍超）。除预算终档外不允许悬崖；
// 每档都记录在 session.decision.history_tier。
func planHistoryReplay(history []map[string]any, spec orchestrator.ModelSpec) historyReplayPlan {
	if !historyExceedsBudget(history, spec) {
		return historyReplayPlan{Replay: history, Tier: "full"}
	}
	digest := compressHistoryDigest(history)
	if historyExceedsBudget(digest, spec) {
		return historyReplayPlan{Tier: "handoff", Rotated: true}
	}
	return historyReplayPlan{Replay: digest, Tier: "digest"}
}

// compressHistoryDigest 老化压缩：最近 DigestRecentTurns 轮保留全文；更早轮次
// 用户指令保留全文、助手正文截断到 DigestMaxAssistantRunes、工具轨迹附录原样
// 保留（结构化事实的信息密度远高于等长散文）。一期规则压缩不引入 LLM（复活
// 条件见设计 note）；最近 K 轮自身超预算时不缩小它们，由调用方落 handoff 终档。
func compressHistoryDigest(history []map[string]any) []map[string]any {
	total := 0
	for _, m := range history {
		if role, _ := m["role"].(string); role == "user" {
			total++
		}
	}
	firstFull := total - DigestRecentTurns
	if firstFull <= 0 {
		return history
	}
	turn := -1
	out := make([]map[string]any, 0, len(history))
	for _, m := range history {
		role, _ := m["role"].(string)
		if role == "user" {
			turn++
			out = append(out, m)
			continue
		}
		if turn < firstFull {
			text, _ := m["text"].(string)
			out = append(out, map[string]any{"role": "assistant", "text": compactAssistant(text)})
			continue
		}
		out = append(out, m)
	}
	return out
}

// splitAssistantTrace 把助手消息拆成正文与其后的工具轨迹附录（组装时的固定
// 标记切分）。只有轨迹没有正文的崩溃 run 由前缀分支识别。
func splitAssistantTrace(text string) (prose, trace string) {
	if strings.HasPrefix(text, historyToolTraceHeader+"\n") {
		return "", text
	}
	marker := "\n\n" + historyToolTraceHeader
	if idx := strings.Index(text, marker); idx >= 0 {
		return text[:idx], text[idx+2:]
	}
	return text, ""
}

// compactAssistant 单条旧助手消息的压缩形态：正文截断 + 轨迹附录保真。
func compactAssistant(text string) string {
	prose, trace := splitAssistantTrace(text)
	prose = strings.TrimSpace(truncateRunes(strings.TrimSpace(prose), DigestMaxAssistantRunes))
	trace = strings.TrimSpace(trace)
	switch {
	case trace == "":
		return prose
	case prose == "":
		return trace
	default:
		return prose + "\n\n" + trace
	}
}

// runFinalText 单个 run 的助手最终文本：message.completed 按序拼接，
// 无 completed 时以 delta 全量兜底（与 plan/verdict 提取同一文本来源）。
// 只服务提取类消费方——对话回放请走 conversationHistory（含轨迹/追加输入保真）。
func (s *Service) runFinalText(ctx context.Context, runID string) (string, error) {
	events, err := s.store.Events().ListRunEvents(ctx, runID)
	if err != nil {
		return "", err
	}
	return completedOrDeltaText(mainAgentEvents(events)), nil
}

// mainAgentEvents keeps provider history on the parent agent only. Empty AgentID
// is the legacy/main value for in-memory records and pre-0015 rows.
func mainAgentEvents(events []RunEvent) []RunEvent {
	out := make([]RunEvent, 0, len(events))
	for _, event := range events {
		if event.AgentID == "" || event.AgentID == "main" {
			out = append(out, event)
		}
	}
	return out
}

// completedOrDeltaText 助手最终文本提取核心（conversationHistory 与
// runFinalText 共用）：completed 缺失时以非用户 delta 全量兜底；role=user 的
// message.delta（steering）不混入助手文本，由 userReplayText 并入用户侧。
func completedOrDeltaText(events []RunEvent) string {
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
	return assistant
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
