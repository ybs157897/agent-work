// replay_integrity_integration_test.go 会话信息完整性（设计 note
// notes/proposed/architecture/2026-08-27-session-integrity-design.md）的集成防回归：
// 回放保真（工具轨迹 digest / steering 并入 / 审批排除）与 digest 档渐进压缩接线。
// 复用 runs_integration_test 的 openTestDB / captureDispatcher / noopNotifier /
// finishRun 与 orchestration_m2_integration_test 的 sessionDecisionEvent 基建。
package application_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// seedReplayEnv 回放测试环境：dsh binding 无 resume 能力（内联档三档路径生效）。
func seedReplayEnv(t *testing.T, ctx context.Context, store *sqlstore.Store, wsID string) (agentID, wiHint string) {
	t.Helper()
	now := time.Now().UTC()
	agent := &domain.AgentProfile{
		ID: "agent_replay_" + wsID, WorkspaceID: wsID, Name: "Replay", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "dsh_local"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{
		ID: "rb_replay_" + wsID, WorkspaceID: wsID, RuntimeLabel: "dsh_local", AdapterID: "dsh",
		Capabilities: map[string]string{"resume": "unavailable"}, Status: domain.BindingReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return agent.ID, ""
}

// conversationHistoryOf 从 run.Input 快照取回放历史。
func conversationHistoryOf(t *testing.T, run *domain.ExecutionRun) []map[string]any {
	t.Helper()
	raw, ok := run.Input["conversation"].(map[string]any)
	if !ok {
		t.Fatalf("run 缺 conversation 快照: %#v", run.Input)
	}
	items, ok := raw["history"].([]any)
	if !ok {
		// CreateRun 返回的是落库前的内存对象，history 仍是 []map 形态。
		if maps, ok := raw["history"].([]map[string]any); ok {
			return maps
		}
		t.Fatalf("conversation 缺 history: %#v", raw)
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, m)
	}
	return out
}

// historyText 拼接整个回放用于敏感内容排除断言。
func historyText(t *testing.T, run *domain.ExecutionRun) string {
	b := strings.Builder{}
	for _, m := range conversationHistoryOf(t, run) {
		text, _ := m["text"].(string)
		b.WriteString(text)
		b.WriteString("\n")
	}
	return b.String()
}

// TestConversationReplayFidelity 验收：工具轨迹 digest 与 steering 进入回放，
// 且参数被截断、审批内容永不进回放（负向保证）。
func TestConversationReplayFidelity(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_replay", Name: "replay", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, "ws_replay")
	agentID, _ := seedReplayEnv(t, ctx, store, ws.ID)
	wi, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "发布脚本"})
	if err != nil {
		t.Fatal(err)
	}

	first, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: agentID, Instruction: "修一下发布脚本",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, first.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	// 工具轨迹事实：t1 完成（args 超截断线）、t2 有始无终（崩溃形态）、孤儿失败帧。
	forceArgs := strings.Repeat("git push --force origin feature/x ", 3) // 102 runes > 80 截断线
	for _, e := range []struct {
		typ     string
		payload map[string]any
	}{
		{domain.EventToolStarted, map[string]any{"tool": "shell", "call_id": "t1", "args": forceArgs}},
		{domain.EventApprovalRequested, map[string]any{
			"kind": "command", "risk": "high", "summary": "rm -rf /tmp/secrets"}},
		{domain.EventToolCompleted, map[string]any{"call_id": "t1"}}, // kimi/dsh 形态：只有 call_id
		{domain.EventToolStarted, map[string]any{"tool": "edit", "call_id": "t2", "args_summary": "main.go"}},
	} {
		if err := svc.RecordRunEvent(ctx, first.ID, e.typ, e.payload); err != nil {
			t.Fatal(err)
		}
	}
	// 审批事件行落库后保持空载荷（现状）：决议只写 audit/activities，无干净来源。
	svc.InputForwarder = func(ctx context.Context, runID, instruction string) error { return nil }
	if err := svc.SendRunInput(ctx, first.ID, "别用 force push，普通推送即可"); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, first.ID, "已改用普通推送"); err != nil {
		t.Fatal(err)
	}

	second, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: agentID, Instruction: "继续核对产物",
	})
	if err != nil {
		t.Fatal(err)
	}

	history := conversationHistoryOf(t, second)
	if len(history) != 2 {
		t.Fatalf("单轮历史应产出 user+assistant 两条，实际 %d 条: %#v", len(history), history)
	}
	userText, _ := history[0]["text"].(string)
	wantUser := "修一下发布脚本\n\n[轮中追加输入]\n别用 force push，普通推送即可"
	if userText != wantUser {
		t.Fatalf("steering 未按时间序并入用户侧:\n got %q\nwant %q", userText, wantUser)
	}
	if history[0]["role"] != "user" || history[1]["role"] != "assistant" {
		t.Fatalf("角色序错乱: %#v", history)
	}
	assistantText, _ := history[1]["text"].(string)
	if !strings.Contains(assistantText, "已改用普通推送") {
		t.Fatalf("最终文本丢失: %q", assistantText)
	}
	if !strings.Contains(assistantText, "[本轮执行轨迹]") {
		t.Fatalf("工具轨迹附录缺失: %q", assistantText)
	}
	// 参数截断行格式钉死（80 runes + 省略号），完整原始入参不得进回放
	//（精确截断由 TestToolTraceLinesPinFormat 单测锁定）。
	wantLine := "- shell(" + strings.Join(strings.Fields(forceArgs), " ")[:application.DigestToolArgRunes] + "…): 完成"
	if !strings.Contains(assistantText, wantLine) {
		t.Fatalf("截断格式不符:\n got  %q\nwantContains %q", assistantText, wantLine)
	}
	if strings.Contains(assistantText, forceArgs) {
		t.Fatal("完整工具入参进入回放")
	}
	if !strings.Contains(assistantText, "- edit(main.go): 未完成") {
		t.Fatalf("有始无终的工具调用应标未完成: %q", assistantText)
	}

	// 负向保证：审批摘要绝不进回放。
	entire := historyText(t, second)
	if strings.Contains(entire, "secrets") || strings.Contains(entire, "rm -rf") {
		t.Fatal("审批敏感内容进入回放（违反负向保证）")
	}

	// 观测面：预算内的满档回放 → inline/fresh + history_tier=full + stats。
	data := sessionDecisionEvent(t, ctx, store, ws.ID, second.ID)
	if data["tier"] != "inline" || data["reason"] != "fresh" || data["history_tier"] != "full" {
		t.Fatalf("满档回放的 session.decision 异常: %#v", data)
	}
	stats, ok := data["history_stats"].(map[string]any)
	if !ok {
		t.Fatalf("full 档必须携带 history_stats: %#v", data)
	}
	if turns, _ := stats["turns"].(float64); turns != 1 {
		t.Fatalf("history_stats.turns = %v, want 1", stats["turns"])
	}
	if tokens, _ := stats["est_tokens"].(float64); tokens <= 0 {
		t.Fatalf("history_stats.est_tokens 异常: %#v", stats)
	}
}

// TestCreateRunDigestTierInlinesCompressedHistory 验收 digest 档接线：全量超预算
// 但压缩后可容纳时，请求注入压缩历史而非轮换，decision 标 history_tier=digest。
func TestCreateRunDigestTierInlinesCompressedHistory(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_digest", Name: "digest", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, "ws_digest")
	agentID, _ := seedReplayEnv(t, ctx, store, ws.ID)
	wi, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "长会话"})
	if err != nil {
		t.Fatal(err)
	}

	// 6 个历史轮：前两轮助手巨文（全量超窗口预算）、近四轮很小。
	// DigestRecentTurns=4 → 前两轮助手被压到 400+省略号，近四轮逐字保留。
	giant := strings.Repeat("长", 6000)
	for i := 0; i < 6; i++ {
		run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
			AgentProfileID: agentID, Instruction: fmt.Sprintf("第%d轮指令", i),
		})
		if err != nil {
			t.Fatal(err)
		}
		text := fmt.Sprintf("第%d轮结论", i)
		if i < 2 {
			text = giant + text
		}
		startRun(t, ctx, svc, run)
		if err := finishRun(ctx, svc, run.ID, text); err != nil {
			t.Fatal(err)
		}
	}
	next, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: agentID, Instruction: "第6轮指令",
	})
	if err != nil {
		t.Fatal(err)
	}

	conv, _ := next.Input["conversation"].(map[string]any)
	if rot, _ := conv["session_rotation"].(bool); rot {
		t.Fatalf("digest 可容纳不得升级轮换: %#v", conv)
	}
	history := conversationHistoryOf(t, next)
	if len(history) != 12 {
		t.Fatalf("6 轮历史应有 12 条消息，实际 %d", len(history))
	}
	oldAssistant, _ := history[1]["text"].(string)
	if runes := len([]rune(oldAssistant)); runes != application.DigestMaxAssistantRunes+1 ||
		!strings.HasSuffix(oldAssistant, "…") {
		t.Fatalf("旧助手文本未按 digest 截断: %d runes %q…", runes, oldAssistant[:20])
	}
	if user, _ := history[0]["text"].(string); user != "第0轮指令" {
		t.Fatalf("旧用户指令不得截断: %q", user)
	}
	recentAssistant, _ := history[11]["text"].(string)
	if recentAssistant != "第5轮结论" {
		t.Fatalf("最近一轮必须逐字保留: %q", recentAssistant)
	}

	data := sessionDecisionEvent(t, ctx, store, ws.ID, next.ID)
	if data["tier"] != "inline" || data["reason"] != "fresh" {
		t.Fatalf("digest 档仍是 fresh 内联: %#v", data)
	}
	if data["history_tier"] != "digest" {
		t.Fatalf("超预算但压缩可容纳必须标 history_tier=digest: %#v", data)
	}
	stats, ok := data["history_stats"].(map[string]any)
	if !ok {
		t.Fatalf("digest 档必须携带 history_stats: %#v", data)
	}
	if turns, _ := stats["turns"].(float64); turns != 6 {
		t.Fatalf("history_stats.turns = %v, want 6", stats["turns"])
	}
	if tokens, _ := stats["est_tokens"].(float64); tokens <= 0 {
		t.Fatalf("digest 档 est_tokens 应为正: %#v", stats)
	}
}
