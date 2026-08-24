package agentwork

import (
	"os"
	"strings"
)

// WithEnv 在 base 环境块上设置/覆盖单个变量（去掉同名旧项）。
func WithEnv(base []string, key, value string) []string {
	if strings.TrimSpace(value) == "" {
		return base
	}
	prefix := key + "="
	out := make([]string, 0, len(base)+1)
	for _, e := range base {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return append(out, prefix+value)
}

// CodexEnv 返回带 CODEX_HOME 隔离的子进程环境。
func (r Root) CodexEnv() []string {
	return WithEnv(os.Environ(), "CODEX_HOME", r.CodexHome())
}

// KimiEnv 返回带 KIMI_CODE_HOME 隔离的子进程环境。
func (r Root) KimiEnv() []string {
	return WithEnv(os.Environ(), "KIMI_CODE_HOME", r.KimiHome())
}

// DSHEnv 返回带 DSH_HOME 隔离的子进程环境。
func (r Root) DSHEnv() []string {
	return WithEnv(os.Environ(), "DSH_HOME", r.DSHHome())
}

// WithCredential 若 value 非空，向环境块注入单个凭据变量。
func WithCredential(base []string, envName, value string) []string {
	return WithEnv(base, strings.TrimSpace(envName), strings.TrimSpace(value))
}
