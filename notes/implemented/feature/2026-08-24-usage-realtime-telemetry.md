# 用量实时遥测：OnUsage 过程观测 → usage.updated SSE → 前端就地 patch

Status: implemented

## 决策与理由

Run 进行中把 token 用量推到前端，不等终态。链路各环节现状审计后发现骨架已通、
只缺三段：

1. **adapter 段**：`Callbacks.OnUsage` 通道在 SPI 已有，三家真实 adapter
   （kimiapp/dsh/codexapp）都只累计不发射——在各自既有累计点追加发射：
   kimiapp `turn.step.completed`、dsh `assistant/message` 与 `assistant/chunk`
   兜底处、codexapp `thread/tokenUsage/updated`（即
   [codex-usage-telemetry](../architecture/2026-08-24-codex-usage-telemetry.md)
   预埋的复活条件，本 note 触发执行）。
2. **application 段**：进程内（ModuleRunner runnerCallbacks.OnUsage）与远程
   （runnergateway ingress 的 `usage.updated` 帧）两路**早已汇聚于**
   `RecordRunUsage`，它只写列不发事件——在事务内 Update 之后追加
   `usage.updated` 发射（aggregate=execution_run，data 四字段
   `usage_in/usage_out/usage_cached/usage_basis`，与 runDTO 字段名一致）。
3. **web 段**：`runs.store.applyEvent` 只 patch status/progress——加
   usage.updated 分支就地 patch 快照四字段；composer 用量提示读
   `useRunsStore((s) => s.runs)` 快照，响应式自动刷新，chat 层零改动。

### 语义口径

- **OnUsage 是过程观测，发的是各家累计口径的当前累计值**；application 覆盖写
  run 行四列（最后一次覆盖即最新水位）。终态 `ExecResult.Usage` 随行保留不动
  （结算口径不变）；task_sessions 输入 token 差值记账以 run 行为水位，天然兼容
  过程帧（末次差值 = 终值 − 最后过程值）。
- **dsh 过程帧与结算同源**：发射值取 `usageTotals()`（message.usage 权威、
  chunk 兜底），保证观测终帧 == 结算值，不因双通道出现口径跳变。
- **终态随行也发一条 usage.updated**：与远程路径对称（runnerd 的
  moduleEngine.RecordRunUsage 本来就对 OnUsage 与终态两路都发帧），web patch
  幂等，重复值无害。

## 放弃了什么

- **新建 RecordRunUsageProgress 独立方法**：两条传输路径已汇聚于
  RecordRunUsage，拆方法会造成进程内/远程语义分叉（远程侧无法只对过程帧发
  事件），且差值记账水位就得跟着拆两半；单点「一次上报一条事件」最简且两路
  自动对齐。
- **web 侧为 usage.updated 跳过时间线 append**：`run.progress_updated` 等观测
  帧同样入时间线，保持一致；usage 帧频率与 tool 事件同量级（单 turn 个位到
  几十），TIMELINE_CAP=500 足够。时间线消费侧（buildMessages/
  aggregateRunStream）按类型 switch，未知类型零渲染副作用。
- **SSE 层节流**：event_id 唯一、web patch 幂等，先不做；真出现风暴再加。

## 防回归断言

- application：模拟两次 RecordRunUsage → run 行四列为末次值 +
  stream_events 恰好两条 usage.updated（data 各帧对应）。
- 三家 adapter：OnUsage 逐帧累计值正确（kimiapp 两 step 中间值/终值；dsh 去重
  口径下末帧 == 结算值；codexapp 异 turn 帧不触发、累计值逐帧可观测）。
- web：applyEvent(usage.updated) 后快照四字段变化；后续 status-only 事件不清
  已知用量。

## 复活条件（妥协项）

- 若单 turn 用量通知频次显著上量（如逐 chunk 携带 usage 的 provider）导致
  usage.updated 风暴 → 返工点：application 层按 run 维度节流（如 500ms 窗口
  trailing 覆盖，终态帧必发）；adapter 发射面不动。
