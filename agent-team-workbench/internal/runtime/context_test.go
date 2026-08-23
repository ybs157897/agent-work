package runtime

import (
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestEffectiveInstructionReplaysHistoryWithoutNativeResume(t *testing.T) {
	run := &domain.ExecutionRun{Input: map[string]any{
		"instruction": "第二轮",
		"conversation": map[string]any{
			"id": "wi_1", "turn_index": 2,
			"history": []map[string]any{
				{"role": "user", "text": "第一轮"},
				{"role": "assistant", "text": "第一轮回复"},
			},
		},
	}}
	got := EffectiveInstruction(run)
	for _, want := range []string{"第一轮", "第一轮回复", "第二轮"} {
		if !strings.Contains(got, want) {
			t.Fatalf("回放提示缺少 %q: %s", want, got)
		}
	}
}

func TestEffectiveInstructionUsesRawTurnWhenResuming(t *testing.T) {
	run := &domain.ExecutionRun{Input: map[string]any{
		"instruction": "第二轮",
		"conversation": map[string]any{
			"resume_session_ref": "codex://thread_1",
			"history":            []any{map[string]any{"role": "user", "text": "第一轮"}},
		},
	}}
	if got := EffectiveInstruction(run); got != "第二轮" {
		t.Fatalf("native resume 不应重复回放历史: %q", got)
	}
}

// TestEffectiveInstructionRotationTier 轮换档：新会话收到 handoff 摘要 + 当轮输入，
// 而非全量历史回放（即使 history 存在）。
func TestEffectiveInstructionRotationTier(t *testing.T) {
	run := &domain.ExecutionRun{Input: map[string]any{
		"instruction": "第三轮：继续",
		"conversation": map[string]any{
			"id": "wi_1", "turn_index": 3,
			"session_rotation": true,
			"handoff_summary":  "【任务】rotate\n【近期对话】\n用户：暗号是什么\n助手：ECHO-7",
			"history": []map[string]any{
				{"role": "user", "text": "第一轮"},
				{"role": "assistant", "text": "第一轮回复"},
			},
		},
	}}
	got := EffectiveInstruction(run)
	for _, want := range []string{"会话已轮换", "ECHO-7", "第三轮：继续"} {
		if !strings.Contains(got, want) {
			t.Fatalf("轮换档提示缺少 %q: %s", want, got)
		}
	}
	if strings.Contains(got, "以下是同一会话此前已经确认的对话历史") {
		t.Fatalf("轮换档不应走全量回放: %s", got)
	}
}

func TestPolicySnapshotDefaultsAndFields(t *testing.T) {
	run := &domain.ExecutionRun{Input: map[string]any{
		"mode": "plan",
		"policy": map[string]any{
			"sandbox": "read-only", "approval_policy": "manual",
			"tools": []any{"fs", "todo"},
		},
	}}
	p := PolicySnapshotOf(run)
	if p.Mode != "plan" || p.Sandbox != "read-only" || p.ApprovalPolicy != "manual" || len(p.Tools) != 2 {
		t.Fatalf("policy snapshot: %+v", p)
	}
}

// TestEffectiveInstructionHistoryRegionByteStable 缓存契约（防回归）：provider
// 前缀缓存只认字节级一致的前缀——相邻两轮指令的「历史区」（固定头 + 全部
// 已定局消息的渲染）必须逐字节一致；动态内容只允许出现在当轮消息尾部。
func TestEffectiveInstructionHistoryRegionByteStable(t *testing.T) {
	turn2 := &domain.ExecutionRun{Input: map[string]any{
		"instruction": "第二轮",
		"conversation": map[string]any{
			"id": "wi_1", "turn_index": 2, "config_digest": "d",
			"history": []map[string]any{
				{"role": "user", "text": "第一轮"},
				{"role": "assistant", "text": "第一轮回复"},
			},
		},
	}}
	turn3 := &domain.ExecutionRun{Input: map[string]any{
		"instruction": "第三轮",
		"conversation": map[string]any{
			"id": "wi_1", "turn_index": 3, "config_digest": "d",
			"history": []map[string]any{
				{"role": "user", "text": "第一轮"},
				{"role": "assistant", "text": "第一轮回复"},
				{"role": "user", "text": "第二轮"},
				{"role": "assistant", "text": "第二轮回复"},
			},
		},
	}}
	i2 := EffectiveInstruction(turn2)
	i3 := EffectiveInstruction(turn3)
	marker := "[用户当前消息]"
	idx := strings.Index(i2, marker)
	if idx < 0 {
		t.Fatalf("当轮消息标记缺失: %q", i2)
	}
	if !strings.HasPrefix(i3, i2[:idx]) {
		t.Fatalf("历史区必须跨轮字节稳定（前缀缓存契约）:\n--- turn2 稳定区 ---\n%s\n--- turn3 开头 ---\n%s", i2[:idx], i3[:min(len(i3), idx+50)])
	}
	// 渲染确定性：同输入两次构造必须逐字节一致。
	if again := EffectiveInstruction(turn2); again != i2 {
		t.Fatal("同输入的指令渲染不确定")
	}
}
