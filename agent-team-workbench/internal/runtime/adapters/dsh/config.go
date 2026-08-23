// config.go — dsh 网关 adapter 的 provider 路由映射。
//
// stdio per-run 路径（per-run cordis 渲染、DSH_* env 注入、进程组终止）
// 已随 M2 网关化删除：网关模型/工具/凭据由 harness 侧（~/.dsh 设置与
// credentials）统一管理，adapter 只负责会话编排与 selectModel 路由。
package dsh

import "strings"

// dshProviderRoute 把业务 provider 名映射为 DSH 路由名（unowned deepseek-official 自动挂载 llm-deepseek）。
func dshProviderRoute(provider string) string {
	if isDeepSeekRoute(provider) {
		return "deepseek-official"
	}
	return strings.TrimSpace(provider)
}

// isDeepSeekRoute 判断走 llm-deepseek 插件的路由；其余厂商由 harness 侧
// provider 设置承接（pi-ai / 自定义网关）。
func isDeepSeekRoute(provider string) bool {
	p := strings.ToLower(strings.TrimSpace(provider))
	return p == "" || p == "deepseek" || p == "deepseek-official"
}
