# Runtime Agent 模式默认值

Status: implemented

## 决策与理由

Kimi app-server 的每次执行默认采用 `permission_mode=yolo` 并开启 swarm：fresh 只负责
创建会话，随后 fresh 与 resume 都在本轮 prompt 前通过 `POST /sessions/{id}/profile`
写入两项，并立即 GET `/status` 核验。prompt 只重复携带当前 KAP 真正消费的
`permission_mode`，不发送静默 no-op 的 `swarm_mode`。显式 `approval_policy=manual`
仍映射为 Kimi `manual`，其余 Workbench 策略走产品默认 `yolo`。

Codex app-server 始终显式启用稳定 `multi_agent` feature；原生 OpenAI/Codex provider
额外启用 `multi_agent_v2`，第三方 Responses-compatible provider 则显式关闭 v2。
原因是 v2 子线程使用 OpenAI/Codex 的 `agent_message` Responses 扩展，而第三方只声明
标准 Responses 兼容，不能推定支持该扩展。未显式配置 reasoning effort 时，
`turn/start.effort` 默认 `ultra`；用户显式选择的 medium/high/xhigh 等仍保留，
`collaborationMode` 继续只表达 Workbench 的 default/plan 模式，不承担子 Agent 开关。

## 放弃了什么

- **发送 Kimi create.agent_config 或 prompt.swarm_mode**：main 与 0.38 的 schema 接受，
  路由却静默忽略；只写它会形成“请求看似正确、运行态仍关闭”的假闭环。
- **把 Kimi 全部策略强制 YOLO**：显式 manual 是用户安全边界，默认值不能覆盖它。
- **发送 Codex `multiAgentMode=proactive`**：当前协议明确忽略，不能依赖废弃字段。
- **用 Codex `collaborationMode=default` 表示子 Agent**：该字段是 plan/default 交互模式，
  与 multi-agent feature、Ultra effort 是不同维度。
- **对所有 Responses-compatible provider 强制 `multi_agent_v2`**：v2 不是标准 Responses
  兼容层的一部分；Kimi 主线程可用不代表其接受子线程的 `agent_message` 输入。

## 验收

- fresh 与 resume Kimi 请求均有真实 profile 配置证据，status 可观察到 yolo + swarm。
- Kimi prompt 只带有效的 permission override，manual 回归不被破坏。
- Codex 原生 provider 显式启用 `multi_agent + multi_agent_v2`；Kimi 等第三方 Responses
  provider 显式启用 `multi_agent` 并关闭 v2。未配置 effort 时 `turn/start.effort=ultra`，
  显式 effort 不被默认值覆盖。
- Kimi 蜂巢与 Kimi/Codex 普通子 Agent 的现有投影边界不改变。

## 落地证据

- 本机 Kimi 0.38 与 MoonshotAI/kimi-code main `9619277` / 0.38.0 `0999454` 源码均确认：
  [create 路由](https://github.com/MoonshotAI/kimi-code/blob/961927739ef34819d67d76fa5870cbe4ba7a01ff/packages/kap-server/src/routes/sessions.ts#L233-L246)
  会接受 `agent_config` 但状态仍是
  `manual + swarm=false`；profile 更新后 `/status` 权威返回 `permission=yolo`、
  `swarm_mode=true`，因此 fresh 也强制走
  [profile](https://github.com/MoonshotAI/kimi-code/blob/0999454bdcb5ddd98f39bffee434dcf0a810f394/packages/kap-server/src/routes/sessionAgentConfig.ts#L48-L52)。
- Kimi fake KAP 回归覆盖 fresh create/profile/prompt、resume profile 与 manual 例外。
- openai/codex 当前 main `6478a75` 与 rust-v0.149.0 源码均显示
  [feature 默认值](https://github.com/openai/codex/blob/6478a751fde8884b2fdc76486fe23175a8e795d4/codex-rs/features/src/lib.rs#L1205-L1221)：`multi_agent` 默认 V1、
  `multi_agent_v2` 默认关闭；Workbench 只对原生 provider 显式启用 V2。生成 schema 明确
  `multiAgentMode` 已忽略，而
  [V2 + Ultra effort](https://github.com/openai/codex/blob/6478a751fde8884b2fdc76486fe23175a8e795d4/codex-rs/core/src/session/multi_agents.rs#L165-L176)
  才触发 proactive multi-agent。
- Kimi Responses 真实对照中，同一模型启用 v2 时子线程首请求返回 `Invalid request`；
  只启用稳定 `multi_agent` 时成功完成 `spawn_agent → wait → CHILD_OK=4`。
- Workbench 真实 Run `run_01M17484SGF4ETK2W6NJNE5S60` 使用
  `codex-appserver + kimi-k2.7-code` 成功完成；父线程调用 `spawn_agent`，子线程
  `01a04e44-3c29-7f21-b48e-80e4e43860d8` 返回 `579`，父线程 `wait_agent` 后正常终态。
- Codex 回放桩从真实 `Execute` 路径分别断言缺省 effort → `ultra`、显式 high → `high`，
  且请求中没有废弃 `multiAgentMode`。
