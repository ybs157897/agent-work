package runtime

import "github.com/ybs/agent-team-workbench/internal/domain"

// ModelSnapshot 是 run.Input["model"] 的编排快照（orchestrator.EffectiveModel 写入）。
// 各 Adapter 自行把统一快照映射为原生参数：
//   - dsh：provider 路由 + initialize.model；api_key_env/base_url 经 per-run env 渲染进 cordis
//   - kimi/claude-code：model 透传 CLI flag；凭据由 CLI 自身配置管理（继承进程环境）
//   - codex-appserver：model 透传 thread/start.model
type ModelSnapshot struct {
	Ref             string
	ProviderID      string
	ProviderLabel   string
	Provider        string
	API             string // 线协议：openai-completions | openai-responses | anthropic-messages
	Model           string
	BaseURL         string
	APIKeyEnv       string
	ContextWindow   int
	MaxTokens       int
	ReasoningEffort string
}

// ModelSnapshotOf 从 run.Input 读模型快照；无快照返回零值（adapter 回退自身默认配置）。
func ModelSnapshotOf(run *domain.ExecutionRun) ModelSnapshot {
	var snap ModelSnapshot
	if run == nil {
		return snap
	}
	raw, ok := run.Input["model"].(map[string]any)
	if !ok {
		return snap
	}
	snap.Ref, _ = raw["ref"].(string)
	snap.ProviderID, _ = raw["provider_id"].(string)
	snap.ProviderLabel, _ = raw["provider_label"].(string)
	snap.Provider, _ = raw["provider"].(string)
	snap.API, _ = raw["api"].(string)
	snap.Model, _ = raw["model"].(string)
	snap.BaseURL, _ = raw["base_url"].(string)
	snap.APIKeyEnv, _ = raw["api_key_env"].(string)
	snap.ContextWindow = intOf(raw["context_window"])
	snap.MaxTokens = intOf(raw["max_tokens"])
	snap.ReasoningEffort, _ = raw["reasoning_effort"].(string)
	return snap
}

func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
