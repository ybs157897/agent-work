// Package agentwork 定义项目本地 Runtime 空间（默认 .agent-work/），
// 与全局 ~/.codex、~/.dsh、系统 Kimi 配置隔离。
package agentwork

import (
	"os"
	"path/filepath"
	"strings"
)

const RootDirName = ".agent-work"

// Root 是 workbench 项目下的本地 Runtime 空间根目录。
type Root struct {
	Workbench string // control-plane 工作目录（绝对路径）
	Root      string // .agent-work 绝对路径
}

// Resolve 解析项目空间根目录；可用 ATW_AGENT_WORK_DIR 覆盖整棵目录树位置。
func Resolve(workbenchRoot string) Root {
	base := strings.TrimSpace(os.Getenv("ATW_AGENT_WORK_DIR"))
	if base == "" {
		base = filepath.Join(workbenchRoot, RootDirName)
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		abs = base
	}
	wb, _ := filepath.Abs(workbenchRoot)
	return Root{Workbench: wb, Root: abs}
}

func (r Root) CodexHome() string { return resolveSubHome("ATW_CODEX_HOME", r.Root, "codex") }
func (r Root) KimiHome() string {
	// kimi CLI and kap-server must share one project-scoped config home. Keep
	// the explicit CLI override as the canonical knob; the app-server override
	// is accepted only when the former is absent for backwards compatibility.
	if home := strings.TrimSpace(os.Getenv("ATW_KIMI_HOME")); home != "" {
		return absolutePath(home)
	}
	if home := strings.TrimSpace(os.Getenv("ATW_KIMIAPP_HOME")); home != "" {
		return absolutePath(home)
	}
	return filepath.Join(r.Root, "kimi")
}
func (r Root) DSHHome() string { return resolveSubHome("ATW_DSH_HOME", r.Root, "dsh") }
func (r Root) CredentialsPath() string {
	return filepath.Join(r.Root, "credentials.local.yaml")
}

// LegacyCredentialsPath 旧版凭据路径（models/ 旁），只读回退。
func (r Root) LegacyCredentialsPath() string {
	return filepath.Join(r.Workbench, "models", "credentials.local.yaml")
}

// Ensure 创建项目空间子目录（凭据文件在首次写入时创建）。
func (r Root) Ensure() error {
	for _, dir := range []string{r.Root, r.CodexHome(), r.KimiHome(), r.DSHHome()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func resolveSubHome(envKey, root, sub string) string {
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
		return absolutePath(v)
	}
	return filepath.Join(root, sub)
}

func absolutePath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
