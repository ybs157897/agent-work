package application

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
)

func TestResumablePreviousRunRequiresSameConfigAndSuccessfulSession(t *testing.T) {
	previous := &domain.ExecutionRun{
		ID: "run_1", Status: domain.RunSucceeded, AdapterID: "codex-appserver",
		RuntimeLabel: "codex_local", SessionRef: "codex://thread_1",
		Input: map[string]any{"conversation": map[string]any{"config_digest": "same"}},
	}
	if got := resumablePreviousRun([]*domain.ExecutionRun{previous}, "codex-appserver", "codex_local", "same"); got != previous {
		t.Fatalf("应复用同配置成功会话: %#v", got)
	}
	if got := resumablePreviousRun([]*domain.ExecutionRun{previous}, "kimi", "kimi_local", "same"); got != nil {
		t.Fatal("切换 Runtime 不得复用 provider 私有会话")
	}
	if got := resumablePreviousRun([]*domain.ExecutionRun{previous}, "codex-appserver", "codex_local", "changed"); got != nil {
		t.Fatal("提示词/模型/权限变化不得复用旧会话")
	}
	previous.Status = domain.RunFailed
	if got := resumablePreviousRun([]*domain.ExecutionRun{previous}, "codex-appserver", "codex_local", "same"); got != nil {
		t.Fatal("失败轮次不得成为 resume 基线")
	}
}

// TestHistoryBudgetDerivation 预算随注册表窗口推导；缺窗口回退保守值。
func TestHistoryBudgetDerivation(t *testing.T) {
	if got := historyBudgetTokens(orchestrator.ModelSpec{ContextWindow: 128000}); got != 44800 {
		t.Fatalf("128K 窗口预算应为其 35%%: %d", got)
	}
	fallback := historyBudgetTokens(orchestrator.ModelSpec{})
	if fallback != historyBudgetFallbackWindow*35/100 || fallback <= 0 {
		t.Fatalf("缺窗口回退预算异常: %d", fallback)
	}
}

// TestEstimateTokens CJK 一字一 token、其余四字符一 token 的粗估。
func TestEstimateTokens(t *testing.T) {
	if got := estimateTokens("你好世界"); got != 4 {
		t.Fatalf("CJK 估 Token 异常: %d", got)
	}
	if got := estimateTokens("abcdefgh"); got != 2 {
		t.Fatalf("ASCII 估 Token 异常: %d", got)
	}
	if got := estimateTokens("你好abcd"); got != 3 {
		t.Fatalf("混合估 Token 异常: %d", got)
	}
}

// TestHistoryExceedsBudget 超预算判定：防回归——内联历史超模型窗口预算
// 必须触发轮换而非头部截断（截断会移动请求前缀、清零 provider 缓存）。
func TestHistoryExceedsBudget(t *testing.T) {
	spec := orchestrator.ModelSpec{} // 回退窗口 32768 → 预算 11468 token
	over := []map[string]any{
		{"role": "assistant", "text": strings.Repeat("长", 12000)},
	}
	if !historyExceedsBudget(over, spec) {
		t.Fatal("超预算历史必须判定为超限")
	}
	under := []map[string]any{
		{"role": "user", "text": "你好"},
		{"role": "assistant", "text": "在"},
	}
	if historyExceedsBudget(under, spec) {
		t.Fatal("未超预算不得误判")
	}
}

// TestPlanHistoryReplayTiers 三档边界各一档（防回归：除预算终档外不允许悬崖，
// 信息降级必须分档）。spec 用缺省回退窗口（32768 → 预算 11468 token）。
func TestPlanHistoryReplayTiers(t *testing.T) {
	spec := orchestrator.ModelSpec{}
	turn := func(user, assistant string) []map[string]any {
		return []map[string]any{
			{"role": "user", "text": user},
			{"role": "assistant", "text": assistant},
		}
	}

	// 档位一：预算内 → full 全量原样。
	full := append(turn("第一问", "第一答"), turn("第二问", "第二答")...)
	plan := planHistoryReplay(full, spec)
	if plan.Tier != "full" || plan.Rotated || &plan.Replay[0] != &full[0] {
		t.Fatalf("预算内必须落 full 且不复制: %#v", plan)
	}

	// 档位二：两条旧巨轮超预算，近 4 轮很小 → digest 压缩后容纳。
	history := append([]map[string]any{},
		turn("旧指令零", strings.Repeat("长", 6000))...,
	)
	history = append(history, turn("旧指令一", strings.Repeat("长", 6000))...)
	for i := 0; i < 4; i++ {
		history = append(history, turn(fmt.Sprintf("新指令%d", i), fmt.Sprintf("新答%d", i))...)
	}
	if !historyExceedsBudget(history, spec) {
		t.Fatal("fixture 必须先超预算再谈压缩")
	}
	plan = planHistoryReplay(history, spec)
	if plan.Tier != "digest" || plan.Rotated {
		t.Fatalf("压缩可容纳应落 digest: %#v", plan)
	}
	old := plan.Replay[1]
	if text, _ := old["text"].(string); len([]rune(text)) != DigestMaxAssistantRunes+1 ||
		!strings.HasSuffix(text, "…") {
		t.Fatalf("旧助手文本应截断到 %d runes+省略号，实际 %d runes", DigestMaxAssistantRunes, len([]rune(text)))
	}
	if user, _ := plan.Replay[0]["text"].(string); user != "旧指令零" {
		t.Fatalf("旧用户指令不得截断: %q", user)
	}
	for i := 4; i < len(plan.Replay); i++ {
		recent, _ := plan.Replay[i]["text"].(string)
		origin, _ := history[i]["text"].(string)
		if recent != origin {
			t.Fatalf("最近 K 轮必须逐字保留: msg#%d %q != %q", i, recent, origin)
		}
	}
	var tokens int64
	for _, m := range plan.Replay {
		text, _ := m["text"].(string)
		tokens += estimateTokens(text)
	}
	if historyExceedsBudget(plan.Replay, spec) {
		t.Fatalf("digest 结果仍超预算: %d", tokens)
	}

	// 档位三：单条巨轮属于最近 K 轮、无法收缩 → 压缩后仍超 → handoff 终档。
	giant := turn("继续", strings.Repeat("长", 15000))
	plan = planHistoryReplay(giant, spec)
	if plan.Tier != "handoff" || !plan.Rotated || plan.Replay != nil {
		t.Fatalf("压缩仍超必须落 handoff 终档: %#v", plan)
	}
}

// TestCompressHistoryDigestPreservesTrace 防回归：旧轮次压缩只截助手正文，
// 工具轨迹附录与用户侧内容（含轮中追加标记）必须保真。
func TestCompressHistoryDigestPreservesTrace(t *testing.T) {
	trace := "[本轮执行轨迹]\n- shell(git status): 完成\n- edit(main.go): 失败"
	long := strings.Repeat("正文很长。", 200) // 1200 runes，超 DigestMaxAssistantRunes
	assistantMsg := long + "\n\n" + trace

	got := compactAssistant(assistantMsg)
	wantLen := DigestMaxAssistantRunes + 1 + len([]rune("\n\n"+trace))
	if runes := len([]rune(got)); runes != wantLen {
		t.Fatalf("压缩后长度异常: %d runes，want %d（正文 %d+省略号+轨迹原样）", runes, wantLen, DigestMaxAssistantRunes)
	}
	if !strings.Contains(got, trace) {
		t.Fatalf("工具轨迹附录必须原样保留: %q", got)
	}

	// 只有轨迹没有正文的崩溃 run：轨迹不被吃掉。
	if got := compactAssistant(trace); got != trace {
		t.Fatalf("纯轨迹消息应原样保留: %q", got)
	}
}

// TestToolTraceLinesPinFormat 钉死轨迹行格式与状态词；孤儿帧兜底不丢事实。
func TestToolTraceLinesPinFormat(t *testing.T) {
	ev := func(seq int64, typ string, payload map[string]any) RunEvent {
		return RunEvent{RunSeq: seq, EventType: typ, Payload: payload}
	}
	events := []RunEvent{
		ev(1, domain.EventToolStarted, map[string]any{"tool": "shell", "call_id": "c1",
			"args": strings.Repeat("x", 100)}),
		ev(2, domain.EventToolCompleted, map[string]any{"call_id": "c1"}), // kimi/dsh 形态：只有 call_id
		ev(3, domain.EventToolStarted, map[string]any{"tool": "edit", "call_id": "c2"}),
		ev(4, domain.EventToolFailed, map[string]any{"call_id": "c2"}),
		ev(5, domain.EventToolStarted, map[string]any{"tool": "grep", "call_id": "c3",
			"args_summary": "pattern todo src"}),
		// c3 无终局事件（中断/崩溃）：未完成
		ev(6, domain.EventToolFailed, map[string]any{"call_id": "ghost"}), // 孤儿终局帧
	}
	lines := toolTraceLines(events)
	want := []string{
		"- shell(" + strings.Repeat("x", DigestToolArgRunes) + "…): 完成",
		"- edit: 失败",
		"- grep(pattern todo src): 未完成",
		"- 未知工具: 失败",
	}
	if len(lines) != len(want) {
		t.Fatalf("行数不符: %#v", lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line[%d] = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestValidateRequiredCapabilities(t *testing.T) {
	binding := &domain.RuntimeBinding{Capabilities: map[string]string{"resume": "supported", "approval": "unavailable"}}
	if err := validateRequiredCapabilities(map[string]string{"resume": "required"}, binding); err != nil {
		t.Fatal(err)
	}
	if err := validateRequiredCapabilities(map[string]string{"approval": "required"}, binding); err == nil {
		t.Fatal("unavailable capability 必须失败")
	}
	for _, capability := range []string{
		"structured_transport", "schema_constrained_output", "control_tool_call",
	} {
		for _, level := range []string{"experimental", "adapter_translated", "unavailable", ""} {
			t.Run(capability+"/"+level, func(t *testing.T) {
				candidate := &domain.RuntimeBinding{Capabilities: map[string]string{capability: level}}
				if err := validateRequiredCapabilities(map[string]string{capability: "required"}, candidate); err == nil {
					t.Fatalf("planner control capability %s=%q must not satisfy a native requirement", capability, level)
				}
			})
		}
		t.Run(capability+"/supported", func(t *testing.T) {
			candidate := &domain.RuntimeBinding{Capabilities: map[string]string{capability: "supported"}}
			if err := validateRequiredCapabilities(map[string]string{capability: "required"}, candidate); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCodexRuntimeRequiresResponsesForCustomProvider(t *testing.T) {
	binding := &domain.RuntimeBinding{AdapterID: "codex-appserver"}
	if err := validateAdapterModel(binding, orchestrator.ModelSpec{
		Ref: "kimi-2-7", Provider: "moonshot", Model: "kimi-k2.7-code",
		API: "openai-responses", APIKeyEnv: "MOONSHOT_API_KEY", BaseURL: "https://api.kimi.com/coding/v1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := validateAdapterModel(binding, orchestrator.ModelSpec{
		Provider: "openrouter", Model: "ox-alpha", APIKeyEnv: "OPENROUTER_API_KEY",
		API: "openai-responses", BaseURL: "https://openrouter.ai/api/v1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := validateAdapterModel(binding, orchestrator.ModelSpec{Provider: "codex", Model: ""}); err != nil {
		t.Fatal(err)
	}
	if err := validateAdapterModel(binding, orchestrator.ModelSpec{
		Provider: "openrouter", Model: "ox-alpha", API: "openai-completions", APIKeyEnv: "OPENROUTER_API_KEY", BaseURL: "https://openrouter.ai/api/v1",
	}); err == nil {
		t.Fatal("completions provider should fail")
	}
	if err := validateAdapterModel(binding, orchestrator.ModelSpec{
		Provider: "openrouter", Model: "ox-alpha", API: "openai-responses", BaseURL: "https://openrouter.ai/api/v1",
	}); err == nil || !strings.Contains(err.Error(), "api_key_env") {
		t.Fatal("missing api_key_env should fail")
	}
}

func TestMainAgentEventsExcludesChildTranscript(t *testing.T) {
	events := []RunEvent{
		{RunSeq: 1, AgentID: "main", EventType: domain.EventMessageDelta, Payload: map[string]any{"text": "主"}},
		{RunSeq: 2, AgentID: "child-1", EventType: domain.EventMessageDelta, Payload: map[string]any{"text": "子"}},
		{RunSeq: 3, EventType: domain.EventMessageCompleted, Payload: map[string]any{"text": "旧主"}},
	}
	got := mainAgentEvents(events)
	if len(got) != 2 || got[0].AgentID != "main" || got[1].AgentID != "" {
		t.Fatalf("main/legacy events should remain while child is excluded: %#v", got)
	}
	if text := completedOrDeltaText(got); text != "旧主" {
		t.Fatalf("history text must not include child transcript: %q", text)
	}
}
