package modelconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// ProviderConnectionKey 标识供应商连接（仅用于文档/测试；凭据按 provider_id 匹配）。
func ProviderConnectionKey(provider, baseURL, api string) string {
	return provider + "\x00" + baseURL + "\x00" + api
}

var asciiEnvSlug = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// SuggestAPIKeyEnv 根据 provider 路由名建议环境变量名（写入 registry.api_key_env）。
func SuggestAPIKeyEnv(provider string) string {
	s := strings.TrimSpace(provider)
	if s == "" {
		return "ATW_API_KEY"
	}
	if !isASCIIProviderName(s) {
		h := sha256.Sum256([]byte(s))
		return "ATW_" + strings.ToUpper(hex.EncodeToString(h[:4])) + "_API_KEY"
	}
	slug := strings.ToUpper(strings.ReplaceAll(s, "-", "_"))
	var b strings.Builder
	for _, r := range slug {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_':
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" || !asciiEnvSlug.MatchString(out+"_X") {
		h := sha256.Sum256([]byte(s))
		return "ATW_" + strings.ToUpper(hex.EncodeToString(h[:4])) + "_API_KEY"
	}
	return out + "_API_KEY"
}

func isASCIIProviderName(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}
