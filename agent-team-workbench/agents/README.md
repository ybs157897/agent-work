# Agent 配置目录

每个子目录是一个 Agent 的配置真相源；DB 只是运行时投影。

- `agent.yaml`：名称、角色、技能、Runtime 偏好、模型覆盖、权限（工具白名单 + 审批策略 + sandbox）
- `prompt.md`：系统提示词（作为 persona 注入 Runtime 会话）

同步时机：

1. control-plane 启动时自动导入（新增/更新 DB 投影）；
2. Web 端「智能体团队 → 详情 → 配置」保存后回写本目录；
3. 手动改完文件后调用 `POST /api/v1/workspaces/{id}/agent-config/reload` 重新导入。

字段说明：

```yaml
name: Forge            # 必填
role: developer        # 必填：pm / architect / ui / developer / reviewer 或自定义
skills: [Go, React]    # 技能标签
avatar: ""             # 可选头像 URL
runtime:
  preferred: dsh_local # dsh_local（DeepSeek Harness）或 codex_local（Codex app-server）
  fallbacks: []
  mode: default        # default | plan
  agent_preset: standard # 仅 DSH：SDK 兼容的 Agent preset
model:                 # 覆盖 binding 的 provider/model（可选）
  ref: deepseek-v4-flash
permissions:
  tools: [bash, editor, fs, todo]   # 工具白名单（对 DSH 映射为 cordis.yml 工具插件组合）
  approval_policy: approve_high_risk # auto | approve_high_risk | manual
  sandbox: workspace-write          # read-only | workspace-write | danger-full-access
  preset: workspace-write
```

注意：`approval_policy: manual` 在 DSH SDK 模式下会剔除高风险工具（SDK 线协议暂无审批通道，
能力声明如实标记为 unavailable，不静默降级）。

Codex Runtime 示例：

```yaml
runtime:
  preferred: codex_local
  mode: plan
model:
  provider: codex
  model: "" # 留空使用 Codex CLI 当前默认模型
permissions:
  approval_policy: approve_high_risk
  sandbox: workspace-write
  preset: workspace-write
```

`codex_local` 复用本机 Codex 登录态，以 app-server `thread/start` / `thread/resume`
维护多轮上下文。不要给 Codex Runtime 配置 DeepSeek、Kimi 或 OpenRouter 的模型 ref；
完整映射与协议边界见 `contracts/runtime/codex-app-server-v2.md`。
