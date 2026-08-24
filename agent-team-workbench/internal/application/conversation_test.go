package application

import (
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

func TestValidateRequiredCapabilities(t *testing.T) {
	binding := &domain.RuntimeBinding{Capabilities: map[string]string{"resume": "supported", "approval": "unavailable"}}
	if err := validateRequiredCapabilities(map[string]string{"resume": "required"}, binding); err != nil {
		t.Fatal(err)
	}
	if err := validateRequiredCapabilities(map[string]string{"approval": "required"}, binding); err == nil {
		t.Fatal("unavailable capability 必须失败")
	}
}

func TestCodexRuntimeAcceptsRegistryModelWithCredentials(t *testing.T) {
	binding := &domain.RuntimeBinding{AdapterID: "codex-appserver"}
	if err := validateAdapterModel(binding, orchestrator.ModelSpec{
		Ref: "deepseek-v4-flash", Provider: "deepseek-official", Model: "deepseek-v4-flash",
		APIKeyEnv: "DEEPSEEK_API_KEY",
	}); err != nil {
		t.Fatal(err)
	}
	if err := validateAdapterModel(binding, orchestrator.ModelSpec{
		Provider: "openrouter", Model: "ox-alpha", APIKeyEnv: "OPENROUTER_API_KEY",
		BaseURL: "https://openrouter.ai/api/v1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := validateAdapterModel(binding, orchestrator.ModelSpec{Provider: "codex", Model: ""}); err != nil {
		t.Fatal(err)
	}
	if err := validateAdapterModel(binding, orchestrator.ModelSpec{
		Provider: "openrouter", Model: "ox-alpha", BaseURL: "https://openrouter.ai/api/v1",
	}); err == nil {
		t.Fatal("missing api_key_env should fail")
	}
}
