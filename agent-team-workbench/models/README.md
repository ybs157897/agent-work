# 模型注册表（models/registry.yaml 为真相源）

`registry.yaml` 按**供应商**分组，每组包含连接配置和多个模型。
Web「模型」页的增删改直接回写此文件；Agent 配置（`agents/<slug>/agent.yaml`）
以 `model.ref` 引用模型 `id`，创建 Run 时编排层把条目解析为模型快照固化进
run.Input——运行中修改注册表不影响已启动的 Run。

API Key 保存在 `credentials.local.yaml`（gitignore），不在注册表里写密钥明文。

## 文件结构

```yaml
providers:
  - label: DeepSeek              # UI 展示分类
    provider: deepseek-official  # DSH 路由名
    models:
      - id: deepseek-v4-flash    # model.ref 引用此 id
        display_name: DeepSeek V4 Flash
        model: deepseek-v4-flash # 发给 API 的模型名
        notes: ...
  - label: OpenRouter
    provider: openrouter
    api: openai-completions
    base_url: https://openrouter.ai/api/v1
    models:
      - id: ox-alpha
        display_name: OpenRouter Ox Alpha
        model: ox-alpha
```

## 供应商级字段

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| label | 否 | Web 页面展示分类 |
| provider | 是 | provider 路由名 |
| api | 否 | 线协议：`openai-completions`（默认）/ `openai-responses` / `anthropic-messages` |
| base_url | 否 | OpenAI 兼容端点 |
| api_key_env | 否 | 遗留字段；推荐用 Web UI 保存到 credentials.local.yaml |

## 模型级字段

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| id | 是 | 注册表唯一标识（小写 slug），Agent `model.ref` 引用 |
| display_name | 是 | 页面展示名 |
| model | 是 | 传给 Runtime 的模型名 |
| context_window | 否 | 上下文容量（token） |
| max_tokens | 否 | 输出上限（token） |
| notes | 否 | 备注 |

## 迁移说明

若目录下仍有旧版 `models/<id>.yaml` 单文件条目，首次读取时会自动合并写入 `registry.yaml`。
