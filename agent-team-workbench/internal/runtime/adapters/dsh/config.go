package dsh

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// cordis 配置渲染：ConfigPath 所在目录为 runtime 工程根（含 cordis.base.yml 与 tools/ 片段）。
// 每个 Run 按权限快照的工具白名单渲染独立配置，写入 .generated/（协议 §8.2 能力固化）。

// dshProviderRoute 把业务 provider 名映射为 DSH 路由名（unowned deepseek-official 自动挂载 llm-deepseek）。
func dshProviderRoute(provider string) string {
	if provider == "" || provider == "deepseek" {
		return "deepseek-official"
	}
	return provider
}

// policyToolsOf 从 run.Input 的 policy 快照取工具白名单；无快照返回 nil（用默认配置）。
func policyToolsOf(run *domain.ExecutionRun) []string {
	raw, ok := run.Input["policy"].(map[string]any)
	if !ok {
		return nil
	}
	list, ok := raw["tools"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, t := range list {
		if s, ok := t.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// modelOverrideOf 从 run.Input 的 model 快照取 per-run 模型覆盖；空字段表示无覆盖。
func modelOverrideOf(run *domain.ExecutionRun) (provider, model string) {
	raw, ok := run.Input["model"].(map[string]any)
	if !ok {
		return "", ""
	}
	provider, _ = raw["provider"].(string)
	model, _ = raw["model"].(string)
	return provider, model
}

var knownToolFragments = map[string]bool{
	"bash": true, "editor": true, "fs": true, "todo": true, "subagent": true,
}

// renderRunConfig 渲染 per-run cordis.yml；未知工具名裁剪并记录日志（不静默降级为全量工具）。
func renderRunConfig(configPath, runID string, tools []string) (string, error) {
	runtimeDir := filepath.Dir(configPath)
	base, err := os.ReadFile(filepath.Join(runtimeDir, "cordis.base.yml"))
	if err != nil {
		return "", fmt.Errorf("读取 cordis.base.yml 失败: %w", err)
	}
	out := append([]byte{}, base...)
	for _, tool := range tools {
		if !knownToolFragments[tool] {
			continue
		}
		frag, err := os.ReadFile(filepath.Join(runtimeDir, "tools", tool+".yml"))
		if err != nil {
			continue
		}
		out = append(out, '\n')
		out = append(out, frag...)
	}
	genDir := filepath.Join(runtimeDir, ".generated")
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(genDir, runID+".cordis.yml")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// EnsureDefaultConfig 在 ConfigPath 不存在时生成默认组合（base + 全部已知工具片段）。
func EnsureDefaultConfig(configPath string) error {
	if _, err := os.Stat(configPath); err == nil {
		return nil
	}
	if _, err := renderRunConfig(configPath, "default", []string{"bash", "editor", "fs", "todo", "subagent"}); err != nil {
		return err
	}
	// renderRunConfig 写入 .generated/；默认配置需位于 ConfigPath 本身。
	generated := filepath.Join(filepath.Dir(configPath), ".generated", "default.cordis.yml")
	data, err := os.ReadFile(generated)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0o644)
}
