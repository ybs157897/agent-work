# codexapp 计划清单与上下文压缩事件：turn/plan/updated + contextCompaction → run.plan_updated / session.compacted

Status: implemented

## 决策与理由

前端（并行分支）按固定契约定了两个 execution_run 聚合事件：`run.plan_updated`
（data=`{"steps":[{"step","status"}]}`，同 run 内新帧替换旧帧）与
`session.compacted`（data 允许空对象或带 turnId）。codexapp 侧各接一条
app-server v2 通知源，事件名登记三处：`internal/domain/events.go` 白名单、
`contracts/events/asyncapi.yaml` 白名单注释行、web EVENT_NAMES（前端分支自理）。

- `run.plan_updated` ← 通知 `turn/plan/updated`（pump switch 直发；
  每帧携带完整 steps，替换语义由通知天然保证）。
- `session.compacted` ← **双路径**：`item/completed` 且 `item.type ==
  "contextCompaction"`（0.149.0 v2 的实际发射路径），以及 `thread/compacted`
  通知（schema 在册、0.149.0 不发射，防协议漂移的兜底路径）。

## 协议形状的证据链

1. `contracts/runtime/codex-app-server-v2.md` 与
   `testdata/providers/codex/fake_server.py` 均无这两个通知——文档/桩缺位，
   不能照抄；桌面研究文档的名字（`turn/plan/updated`）本波经权威 schema 证实。
2. 决定性证据（vendor 契约文档钦定的 source of truth）：vendored 0.149.0 执行
   `codex app-server generate-json-schema --out <dir> --experimental`，生成文件
   sha256 与 `protocol.go protocolSchemaSHA256` 钉定的基线一致。ServerNotification
   枚举含两个方法：

   ```text
   turn/plan/updated  -> TurnPlanUpdatedNotification
     { threadId*, turnId*, plan*: [TurnPlanStep{step*, status*}], explanation?: string|null }
     TurnPlanStepStatus enum: pending | inProgress | completed
   thread/compacted   -> ContextCompactedNotification { threadId*, turnId* }
     （params 描述："Deprecated: Use `ContextCompaction` item type instead."）
   ThreadItem oneOf 含 type=contextCompaction，形状仅 { id* }
   ```

3. 二进制 strings 抽查（`runtimes/codex/darwin-arm64/codex`）：方法名字面量
   `turn/plan/updated`（3 处）、`thread/compacted`（1 处）均在册。
4. 上游源码（openai/codex rust-v0.149.0）语义交叉验证：
   - `app-server/src/bespoke_event_handling.rs` `EventMsg::PlanUpdate` →
     `handle_turn_plan_update` → `TurnPlanUpdatedNotification`，上游注释原文：
     *"`update_plan` is a todo/checklist tool; it is not related to plan-mode
     updates"*——即该通知就是 agent 的 todo 清单（前端要的语义），且每帧携带
     全量 plan（core `UpdatePlanArgs.plan` 整表替换）。
   - 同文件 `EventMsg::ContextCompacted(..)` 分支为**空操作**，注释原文：
     *"Core still fans out this deprecated event for raw-event and rollout
     compatibility consumers; v2 clients receive the canonical ContextCompaction
     item instead."*——0.149.0 不向 v2 客户端发 `thread/compacted`，压缩信号走
     `item/started`+`item/completed`（`item.type="contextCompaction"`，
     `CoreTurnItem::ContextCompaction → ThreadItem::ContextCompaction{id}`，
     见 app-server-protocol/src/protocol/v2/item.rs）。
   - resume 无重放陷阱：`thread/resume`/fork 挂接时只向连接重放 token 用量
     快照（token_usage_replay.rs）与 goal 快照，**不重放历史 item 通知**——
     不同于 thread/tokenUsage/updated 的 turnId 过滤前提。

## 语义与映射决策

- **status 归一**：schema 枚举 `inProgress`（camelCase）→ canonical 契约
  `in_progress`；`pending`/`completed` 原样；未知值原样透传（协议新增枚举
  漂移在消费侧可见，不静默吞）。
- **空 steps 合法**：`plan: []` 是 update_plan 清空清单，照发（替换语义下
  = 清屏）；`plan` 键缺失/null 视为畸形帧跳过。
- **session.compacted 只在 item/completed 发**：item/started 仅表示压缩
  进行中，completed 才是事实成立，天然去重。
- **data 形状**：run.plan_updated 只放 `steps`（前端契约固定）；
  session.compacted 尽量带 `turnId`，不可得时空对象。

## 放弃了什么

- **命中 turn/plan/updated 时抑制 plan item 的文本化映射**（任务预设的
  双通道担忧）：**证伪**。上游注释明说 update_plan（todo 清单）与 plan-mode
  无关；`plan` item 是 plan 协作模式的提案正文流（schema：
  "EXPERIMENTAL - proposed plan item content"），现行映射
  `item/plan/delta → message.delta`、`item/completed(plan) → message.completed`
  承载的是 plan 模式的最终答案。抑制会让 plan 模式跑丢答案——两条通道是
  不同信息，全部保留。
- **从 item/completed(plan) 提取结构化 steps**（任务预设备选路径）：
  `PlanThreadItem` 只有 `{id*, text*}`，无任何结构化 steps 字段，不可行；
  且通知已确有，无需降级。
- **turnId 过滤（对齐 tokenUsage 模式）**：两通知都无 resume 重放路径
  （见证据链第 4 条），过滤只剩「turnID 尚未知的竞态窗口丢真实事件」的
  风险面，不加。
- **explanation 进 data**：前端契约钉死 `{"steps":[...]}`，不加冗余字段；
  需要时前端契约先行。

## 锁定测试

- `TestPumpEmitsPlanUpdatedFromTurnPlanUpdated`：inProgress→in_progress 归一、
  两帧替换（非追加）、空清单帧照发、收尾不受影响。
- `TestPumpEmitsSessionCompactedFromContextCompactionItem`：started 不发、
  completed 发一次且带 turnId。
- `TestPumpEmitsSessionCompactedFromThreadCompactedNotification`：弃用通知
  路径同样落事件（防协议漂移）。
- `TestEventNameWhitelistCoversPlanAndCompaction`（domain）：两名字在白名单，
  防登记被误删。

## 复活条件

- 若 codex 后续版本恢复/改变 `thread/compacted` 发射或引入新压缩通知：
  证据入口重跑 `generate-json-schema --experimental` 比对
  `protocolSchemaSHA256`；双路径各自独立发事件，若某版本开始双发同一压缩，
  再按 itemId/turnId 关联去重（当前无双发证据，不预埋）。
