// Package dshcatalog 发现 DeepSeek Harness 的 Agent Preset 与权限预设（对齐 dsh-agent-presets / dsh-permission-presets）。
package dshcatalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	compositionFile = "agent.cordis.yml"
	metadataFile    = "preset.yml"
)

// AgentPresetEntry 对齐 apiproxy agentPreset.list 的单项（文件发现，无 session）。
type AgentPresetEntry struct {
	ID          string `json:"id"`
	Trust       string `json:"trust"` // system | user
	IsDefault   bool   `json:"is_default"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Broken      string `json:"broken,omitempty"`
	Order       int    `json:"order,omitempty"`
}

// PermissionPresetEntry 对齐 dsh-permission-presets 预设表（当前为部署默认 + read-only）。
type PermissionPresetEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type presetMetadata struct {
	Name        string  `yaml:"name"`
	Description string  `yaml:"description"`
	Order       float64 `yaml:"order"`
}

// DefaultPermissionPresets 与 DSH Web UI 三档权限一致（sandbox + approval 打包）。
func DefaultPermissionPresets() []PermissionPresetEntry {
	return []PermissionPresetEntry{
		{ID: "read-only", Name: "只读", Description: "禁止修改文件；适合审阅与只读分析。"},
		{ID: "workspace-write", Name: "工作区写入", Description: "可在工作区内写入；越界操作需审批。"},
		{ID: "danger-full-access", Name: "完全访问", Description: "无文件沙箱限制；高风险操作不再逐项确认。"},
	}
}

// ResolvePresetRoots 返回用于扫描的 preset 根目录（每个根下的一级子目录为一个 preset）。
func ResolvePresetRoots(workbenchRoot string) []string {
	if v := strings.TrimSpace(os.Getenv("ATW_DSH_PRESET_ROOTS")); v != "" {
		var roots []string
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				roots = append(roots, expandHome(p))
			}
		}
		if len(roots) > 0 {
			return roots
		}
	}
	candidates := []string{
		filepath.Join(workbenchRoot, "..", "..", "deepseek-harness", "apps", "cli", "config", "agent-presets"),
		filepath.Join(workbenchRoot, "runtimes", "dsh", "vendor", "agent-presets"),
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(resolveDshHome(home), ".agent-presets"))
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range candidates {
		abs, err := filepath.Abs(expandHome(c))
		if err != nil {
			continue
		}
		if seen[abs] {
			continue
		}
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			seen[abs] = true
			out = append(out, abs)
		}
	}
	return out
}

// ListAgentPresets 扫描 preset 根目录；后出现的根不覆盖先前根中的同名 id（对齐 DSH root 优先级）。
func ListAgentPresets(roots []string) ([]AgentPresetEntry, error) {
	byID := map[string]AgentPresetEntry{}
	for _, root := range roots {
		trust := "system"
		if strings.Contains(root, ".agent-presets") {
			trust = "user"
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			id := e.Name()
			if _, exists := byID[id]; exists {
				continue
			}
			dir := filepath.Join(root, id)
			item := AgentPresetEntry{ID: id, Trust: trust, IsDefault: id == "standard"}
			if meta, err := readMetadata(dir); err == nil {
				item.Name = meta.Name
				item.Description = meta.Description
				item.Order = int(meta.Order)
			}
			if item.Name == "" {
				item.Name = id
			}
			if broken := compositionHealth(dir); broken != "" {
				item.Broken = broken
			}
			byID[id] = item
		}
	}
	out := make([]AgentPresetEntry, 0, len(byID))
	for _, v := range byID {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			if out[i].Order == 0 {
				return false
			}
			if out[j].Order == 0 {
				return true
			}
			return out[i].Order < out[j].Order
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ForSDKAdapter 把 DSH Host preset 目录投影成当前 JSON-RPC SDK Adapter 的真实能力视图。
// Host-plane 依赖不能在 SDK 子进程内挂载，必须在选择前显式禁用，不能等运行时静默降级。
func ForSDKAdapter(items []AgentPresetEntry) []AgentPresetEntry {
	out := append([]AgentPresetEntry(nil), items...)
	for i := range out {
		switch out[i].ID {
		case "standard":
			out[i].Description = "SDK 标准模式：系统提示词 + fs/editor/bash/todo 工具集；不包含 DSH Web Host 的 Skills、Goal、工作流与子代理。"
		case "test-mode":
			out[i].Description = "SDK 测试模式：在标准 SDK 工具集上追加测试模式身份提示，用于受控实验。"
		case "minimal":
			out[i].Description = "SDK 极简模式：使用 preset 固定 bash/editor 工具集；支持工作台 Agent 系统提示词覆盖。"
		default:
			if out[i].Broken == "" {
				out[i].Broken = "依赖 DSH Web Host plane，当前 SDK Adapter 不支持"
			}
		}
	}
	return out
}

// ResolvePresetDir 按 id 在 roots 中定位 preset 目录；未找到返回错误。
func ResolvePresetDir(roots []string, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("agent_preset 不能为空")
	}
	for _, root := range roots {
		dir := filepath.Join(root, id)
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			if broken := compositionHealth(dir); broken != "" {
				return "", fmt.Errorf("agent preset %q 不可用: %s", id, broken)
			}
			return dir, nil
		}
	}
	return "", fmt.Errorf("agent preset %q 不在已配置的 preset 目录中", id)
}

// SandboxModeForPermission 把权限预设映射为 dsh-sandbox-policy mode。
func SandboxModeForPermission(preset string) string {
	switch strings.TrimSpace(preset) {
	case "read-only":
		return "read-only"
	case "workspace-write", "":
		return "workspace-write"
	case "danger-full-access":
		return "danger-full-access"
	default:
		return ""
	}
}

// ValidPermissionPreset 是否为已知权限预设 id。
func ValidPermissionPreset(id string) bool {
	for _, p := range DefaultPermissionPresets() {
		if p.ID == id {
			return true
		}
	}
	return false
}

func readMetadata(dir string) (presetMetadata, error) {
	var meta presetMetadata
	data, err := os.ReadFile(filepath.Join(dir, metadataFile))
	if err != nil {
		return meta, err
	}
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return meta, err
	}
	return meta, nil
}

func compositionHealth(dir string) string {
	path := filepath.Join(dir, compositionFile)
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return fmt.Sprintf("缺少 %s", compositionFile)
	}
	if st.Size() == 0 {
		return fmt.Sprintf("%s 为空", compositionFile)
	}
	return ""
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// resolveDshHome 对齐 DSH dshHomePath：DSH_HOME 优先，否则 ~/.dsh。
func resolveDshHome(fallbackHome string) string {
	if v := strings.TrimSpace(os.Getenv("DSH_HOME")); v != "" {
		return expandHome(v)
	}
	return filepath.Join(fallbackHome, ".dsh")
}
