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
		key, ok, err := s.Get(p.ID)
		if err != nil || !ok {
			// 读失败（≠未配置）此处无上报通道：跳过注入，子进程将以缺凭据显式失败。
			continue
		}
		os.Setenv(envName, key)
		n++
	}
	return n
}
