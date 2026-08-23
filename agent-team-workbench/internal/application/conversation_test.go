package application

import (
	"strings"
	"testing"
	"unicode/utf8"

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

func TestTrimRecentHistoryKeepsLatestUTF8(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "text": strings.Repeat("旧", maxConversationHistoryBytes)},
		{"role": "assistant", "text": "最新回复"},
	}
	trimmed := trimRecentHistory(messages)
	last := trimmed[len(trimmed)-1]["text"]
	if last != "最新回复" {
		t.Fatalf("必须保留最新历史，实际 %v", last)
	}
	for _, message := range trimmed {
		if !utf8.ValidString(message["text"].(string)) {
			t.Fatalf("历史裁剪破坏 UTF-8: %q", message["text"])
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
}

func TestCodexRuntimeRejectsForeignProviderModel(t *testing.T) {
	binding := &domain.RuntimeBinding{AdapterID: "codex-appserver"}
	if err := validateAdapterModel(binding, orchestrator.ModelSpec{Provider: "deepseek-official", Model: "deepseek-v4"}); err == nil {
		t.Fatal("Codex Runtime 不得接收 DeepSeek provider 模型")
	}
	if err := validateAdapterModel(binding, orchestrator.ModelSpec{Provider: "codex", Model: ""}); err != nil {
		t.Fatal(err)
	}
	if err := validateAdapterModel(binding, orchestrator.ModelSpec{Provider: "openai", Model: "gpt-test"}); err != nil {
		t.Fatal(err)
	}
}
