package modelconfig

import (
	"os"
	"strings"
)

// HydrateEnv 把凭据写入当前进程环境（api_key_env），供 DSH/Codex/Kimi 子进程继承。
// 返回成功注入的变量数量。
func (s *CredentialsStore) HydrateEnv(providers []ProviderDef) int {
	if s == nil {
		return 0
	}
	n := 0
	for _, p := range providers {
		envName := strings.TrimSpace(p.APIKeyEnv)
		if envName == "" {
			continue
		}
		key, ok := s.Get(p.ID)
		if !ok {
			continue
		}
		os.Setenv(envName, key)
		n++
	}
	return n
}
