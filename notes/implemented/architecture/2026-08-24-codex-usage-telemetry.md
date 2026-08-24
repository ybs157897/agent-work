# codexapp 用量遥测：thread/tokenUsage/updated → per_run ExecResult.Usage

Status: implemented

## 决策与理由

三家 runtime 中 codexapp 的用量面从零补齐：pump 通知 switch 接入 token 用量通知，
turn 内累计、收尾一次写入 `ExecResult.Usage`（`Basis: per_run`，与 kimiapp 同款）；
`CachedTokens` 取 `cachedInputTokens`。httpapi `runDTO` 增补
`usage_in/usage_out/usage_cached/usage_basis` 四字段（snake_case、omitempty，
字段名即前端契约），openapi `ExecutionRun` schema 同步。

### 协议形状的证据链（为什么不是 token_count）

1. `contracts/runtime/codex-app-server-v2.md` 事件投影表与
   `testdata/providers/codex/fake_server.py` 回放桩均无 token 用量通知——文档/桩双双缺位，
   不能照抄。
2. vendored 二进制（`runtimes/codex/darwin-arm64/codex`，0.149.0）strings 抽查：
   app-server v2 的 ServerNotification 方法枚举里有 `thread/tokenUsage/updated`，
   **没有** `token_count` 方法名；`TokenCount`/`token_count` 是 codex-core 协议事件
   （桌面端/IDE 扩展协议，见 `agent-team-workbench-docs/references/codex-desktop-rendering-comparison.md`
   §Q9 的 `codex/event/token_count`）——两种协议共享同一 `TokenUsageInfo` 结构。
3. 决定性证据（契约文档钦定的 source of truth）：用 vendored 0.149.0 执行
   `codex app-server generate-json-schema --experimental` 得到的 v2 schema：

   ```text
   method: thread/tokenUsage/updated
   params: { threadId*, turnId*, tokenUsage*:
     { total*:  TokenUsageBreakdown   # thread 生命周期累计
     , last*:   TokenUsageBreakdown   # 最近一次模型响应的用量快照
     , modelContextWindow } }
   TokenUsageBreakdown: { inputTokens*, cachedInputTokens*, outputTokens*,
     reasoningOutputTokens*, totalTokens*, cacheWriteInputTokens }
   ```

4. codex 上游源码（rust-v0.149.0）交叉验证语义：
   - `protocol.rs` `TokenUsageInfo::append_last_usage`：`total += last; last = 本次快照`——
     `total` 是 thread 累计（resume 时从 rollout 恢复），`last` 是本次对 total 的增量；
   - `app-server/src/bespoke_event_handling.rs`：core `TokenCount` 事件 →
     `ThreadTokenUsageUpdated` 通知，`turnId` 取事件信封（= 当前 turn）；
   - `app-server/src/request_processors/token_usage_replay.rs`：thread/resume 挂接时
     向该连接**重放**持久化用量快照，归因到**上一个完成的 turn**（非本 turn）。

### 口径算法（per_run 增量）

一个 Workbench Run = 一个 turn。只累计 `turnId == 活动 turnId` 的通知的 `last`
三字段（input/cached/output）；turnId 不匹配或尚未知（空）的通知一律忽略——
resume 重放归因旧 turn、turn 开始前的通知不属于本轮，天然被过滤。
收尾时若见过用量帧则写 `Usage{Basis: per_run}`。

解析按容错键落地：camelCase（app-server v2 权威形状）为主，兼容 snake_case
（`token_usage`/`last_token_usage`/`input_tokens`…，core 协议形状）与
`tokenUsage`/`info` 两级包裹——同一 TokenUsageInfo 的两种序列化都收。

## 放弃了什么

- **直接上报 `total`（Basis: session_cumulative）**：省一次累计，但消费方
  （`application.RecordRunUsage` 的 task_sessions 输入累计）按 per_run 差值语义幂等，
  session_cumulative 要求每个消费方自己做差；且 fresh/resume 两形态不一，容易双计。
- **total 差值法（末 total − 首 total 基线）**：resume 场景首个 in-turn 通知的 total
  已含历史用量，干净基线不可得（「首 total − 首 last」虽可推历史，但
  `fill_to_context_window` 类非可加调整会破坏该假设）；sum(last) 在可加路径下与
  total 差值等价，且不受基线问题影响。
- **OnUsage 流式上报**：RecordRunUsage 是覆盖语义 + 差值幂等，流式每次都要写库事务，
  而用量没有实时消费面（agents 页锚点弹窗读 task_sessions.input_tokens_cum 投影，
  非 run 事件流）；单 turn 内通知次数 = 模型响应次数（个位到几十），收尾一次上报足够。
  kimiapp 同样只在收尾写 ExecResult.Usage。
  **（2026-08-24 翻转：实时消费面落地——见
  [usage-realtime-telemetry](../feature/2026-08-24-usage-realtime-telemetry.md)，
  pump 已在累计处追加 `Callbacks.OnUsage` 过程观测，终态结算口径不变。）**
- **把 `token_count` 方法名加进处理词表**：app-server v2 0.149.0 schema 证明该方法名
  不存在于本协议；未识别通知已有 warn 日志兜底（协议漂移可发现），不预埋死代码。
  容错只做在键名层（camelCase/snake_case），因为那是同一结构两种序列化的真实风险面。

## 翻转的锁定测试

`codexapp_test.go` 原 conformance 断言「codexapp 未接用量解析，不应上报 Usage」
（能力不静默捏造）改为零值对照：回放 fixture 不发用量帧 → 不产生 Usage 上报
（既防捏造、也防 OnUsage 流式绕过）。正向映射断言移至泵级测试：脚本化 reader
构造 `thread/tokenUsage/updated` 帧（含旧 turn 重放帧、异 turn 帧）→ 断言
`ExecResult.Usage` 三字段映射与 per_run Basis；另设无用量帧对照组断言 Usage 为零值。

## 复活条件

- ~~若 run 事件流需要实时 token 进度（如对话页实时上下文水位条）→ 返工点：pump 在
  累计处追加 `Callbacks.OnUsage`（RecordRunUsage 差值幂等已兼容），并在本 note 增补。~~
  （2026-08-24 已触发并执行：对话页 composer 用量提示要即时刷新，三家 adapter
  统一接 OnUsage，设计见
  [usage-realtime-telemetry](../feature/2026-08-24-usage-realtime-telemetry.md)。）
- 若 codex 后续版本改用其他通知名/形状 → 证据入口：重跑
  `codex app-server generate-json-schema --experimental` 比对
  `protocolSchemaSHA256`（`internal/runtime/adapters/codexapp/protocol.go`）。
