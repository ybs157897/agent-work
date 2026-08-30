# Codex 自定义 Provider 仅使用 Responses 协议

Status: implemented

## 决策与理由

Codex app-server 的自定义 provider 配置固定生成 `wire_api = "responses"`，因此创建层与执行前配置层都要求模型快照明确声明 `api=openai-responses`。这样 DeepSeek 等只提供 Completions 线协议的注册表模型会在进入 Codex runtime 前失败，而不会生成一个表面可用、运行时才报错的配置。

Kimi runtime 不受此门禁影响，仍可使用其注册表声明的 Responses 协议；DeepSeek 注册表恢复为 `openai-completions`，避免把不兼容的模型误标为 Responses。

## 放弃了什么

- **根据 provider 名称猜测协议**：同一 provider 可能由不同网关实现，名称不能替代明确的 `api` 声明。
- **仅在 app-server 执行时拦截**：晚拦截会让 Run 已创建、配置已写入后才失败，故创建校验与配置生成双重 fail-closed。
- **让 Codex 自动把 completions 转换为 responses**：转换能力不属于本项目的 Codex provider contract，不能用隐式兼容掩盖 endpoint 不支持。
