# 最终目标：Agent 自治团队工作台（End Goal）

> 状态：**最终目标文档**。本文是全部演进的收口愿景，不描述当前完成度；
> 现状对账与分期路线见文末。后续设计/实现与本文冲突时，先修订本文再动代码。

## 一句话概括

一个**控制平面在 harness 之上**的多 agent 团队系统：每个 agent 是"模型 × runtime × 提示词 × 模式"的自由配置单元；任务发布后由 lead agent 认领、规划、派工，worker agent 各自交付，lead 评估后送验收；会话由 agent 层自管理（何时续、何时换），所有发给模型的字节流满足前缀缓存契约。

## 分层架构（从下到上）

```
┌ 4. 编排角色层（lead / worker，agent 出谋，控制平面执政）
├ 3. 会话管理层（锚点 + 三层策略 + 缓存契约）
├ 2. Harness 层（codex / kimi / dsh 各自是一层 loop，哑执行面）
└ 1. Provider 层（DeepSeek / Kimi 等模型 API，无状态，只认字节前缀）
```

核心原则：**harness 保持哑执行面，规划/会话/路由决策全部上提到 workbench 控制平面**——这是多 harness 路线唯一可行解（不可能给 8 个 adapter 都内置控制原语；单 harness 内嵌路线——如 SpineCodex——会变成对上游的巨型 diff 维护负担）。控制平面不做模型决策，只做三件事：

1. **校验** plan（操作词汇表 schema）
2. **执行** plan（全部走现有 run 状态机 / task_sessions 应用服务，绝不旁路）
3. **记账**（预算、审计、守门、审批钩子）

## 1. 配置层：agent 是正交积

配置任意多个 agent（A/B/C/D/E…），每个独立配置四个轴：

- **模型**：引用 models 注册表条目（DeepSeek、Kimi、任意 OpenAI 兼容端点）；注册表带 `context_window` 等目录参数，是压缩预算的权威数据源
- **Runtime**：引用 runtime binding（kimi CLI、codex CLI、dsh…）；binding 声明 adapter 能力（resume / steering / approval / multi_vendor…）
- **提示词 + 模式**：角色定义、plan / 执行模式选择

示例：A = DeepSeek 模型 + kimi CLI + 知识文档提示词；B = Kimi 模型 + codex CLI + 代码开发提示词。

**目标：四轴完全正交**（任意模型配任意 runtime）。当前缺口：codexapp 未声明 multi_vendor，需补模型覆盖通道。

## 2. 会话管理层（agent 自管理会话）

### 三层会话策略

1. **确定性护栏**（永不删除的安全下限）：
   - 配置指纹变化 → 强制 fresh（提示词/模型/权限变了，旧 session 上下文不再匹配）
   - 锚点阈值（40 轮 / 1M input token / 72h）→ 轮换
   - **历史预算按模型窗口推导**：`context_window × 35%`，token 计量；超限走 rotation + handoff 摘要，**永不砍头截断**（截断会移动请求前缀、清零 provider 前缀缓存）
2. **Agent 自识别**（核心能力）：每轮入口用 agent 自己配置的模型做廉价分类——输入 = 近期对话尾部 + 新消息，输出 = `continue | new_topic`。仅在信号出现时触发（闲置 > 2h、历史逼近预算、累计轮次），活跃你来我往默认 continue。判为 new_topic → 走 rotation 通道（reason=topic_change）
3. **手动覆盖**：chat 页"开新会话"按钮直达锚点 reset

### 前缀缓存契约

Provider（DeepSeek 等）的上下文缓存只认**字节级一致前缀**，与 harness session 新旧无关——"新开一堆会话"本身不杀缓存，杀缓存的是字节流不稳定。契约钉死为回归测试：

- 相邻两轮指令的**历史区**（固定头 + 全部已定局消息的渲染）必须逐字节一致
- 动态内容只允许出现在当轮用户消息尾部
- 轮换 = 一次性前缀重置（handoff 摘要起头），之后恢复纯追加
- 每轮缓存命中率（`usage_cached / usage_in`）亮到 UI，形成反馈回路

### Runtime 差异如实上报

| Runtime | resume 能力 | continue 的含义 |
|---|---|---|
| codex（app-server） | supported | 真·同一 session 续下去 |
| kimi（kap-server） | supported（按 id 懒恢复） | 同上 |
| dsh | unavailable（SDK 限制） | 新 session + 控制平面内联历史补记忆 |

binding 如实声明能力；不声明就不假装能续（负向保证：resume 永不静默降级）。

## 3. 编排角色层（meta-loop：规划不执行）

### 驱动模型

事件驱动，不是轮询：

```
触发源（work item 创建 / run 终态 / blocker 解除 / wakeup）
  → 决策（确定性快路径 或 planner 模型调用）
  → 动作（派工 / fork / 知识检索 / 推迟）
  → 观察（事件回流，进入下一轮）
```

### 操作词汇表（OrchestrationPlan，schema 进 contracts 版本化）

```
use_session(mode: continue | fork | rotate)     — 会话策略
dispatch(agent_id, subtask, acceptance)         — 路由到具体 agent
consult_knowledge(query, top_k)                 — 知识检索（注入上下文，不是 run）
defer(until | wakeup_source)                    — 推迟再议（automation wakeup 的天然 producer）
join(step_ids)                                  — 等分支汇合
finish(summary)                                 — 计划收口
```

### Lead 是一等 agent（不是系统内置魔法）

Planner 不是裸的 provider 调用，而是**一个可配置的 agent**——它自己就是"模型 × runtime × 提示词"的组合，模型/提示词/模式都按 agent 配置走。**Agent 出谋，控制平面执政。**

### 任务全生命周期

状态机已为此预建（execution → review → acceptance 三段相位）：

```
发布任务(todo) → lead 认领(in_progress/execution)
  → lead 规划 → 子任务树派工（work_items 加 parent_id，artifacts + handoff 回流）
  → worker 各自完成交付 → lead 拿 acceptance criteria 评估
  → 待验收(acceptance 相位) → 人工验收
  ├─ 通过 → Accept() → completed（唯一收口路径）
  └─ 不理想 → 与单个 agent 对话调整（chat 页）
       → 主干重新交付（BeginExecution 切回执行态，重走评审）
```

### 守门与回退

- plan 过 schema 校验才执行；词汇表外的操作直接拒绝
- 步数 / planner 调用预算上限（防 planner 自我循环派活）
- 高风险操作（fork 超 N 层、跨 agent 派活）挂审批钩子（approvals 表现成）
- planner 失败 → 回退静态策略（直接派给 assigned agent + 阈值轮换），行为不劣于今天

## 4. 知识层

MVP：workspace 级 `docs/` 目录 + 关键词检索；接口定成 `KnowledgeRetriever` 一层，实现可换，后续升 embedding。知识 agent（如配置示例中的 A）负责沉淀，所有 agent 经 `consult_knowledge` 消费。

## 现状对账（2026-08-26 核订）

| 能力 | 状态 |
|---|---|
| 正交配置（agent × 模型 × runtime） | ✅ 已有；codexapp 的 manifest 仍未声明 multi_vendor，模型覆盖通道缺口保留 |
| 状态机（execution/review/acceptance、Accept 唯一收口、BeginExecution 打回） | ✅ 已有 |
| task_sessions 锚点 / 轮换 / handoff / 审计 | ✅ 已有 |
| codex / kimi 真·session 续接 | ✅ 已有（binding resume=supported） |
| 历史预算按模型窗口 + 超限轮换 + 前缀稳定契约 | ✅ 已合入 main（`12d5ea0`，merge `53982ed`） |
| 子任务树（work_items parent_id） | ✅ 已有 |
| OrchestrationPlan 词汇表 + 确定性执行器 + automation wakeup producer | ✅ 已有（`internal/orchestrator`，plans 自 migration 0009 起；HTTP wake 入口 `handlers_wake.go`） |
| lead agent 作为 planner / 评估 run | ✅ 已有（编排层 M1–M4，2026-08-24 合入 main `03a0b24`） |
| join / 审批钩子 / 步数与预算护栏 | ✅ 已有（plan executor guardrails，`35b55c5`） |
| 认领模式（含任务级执行锁 F1、MCP `task_claim`/`task_return` 写面） | ✅ 已有（执行锁迁移 0014，merge `9c3dd6c`） |
| consult_knowledge / KnowledgeRetriever（关键词检索 MVP） | ✅ 已有（`internal/knowledge` + plan 动词，migration 0009） |
| 缓存命中率面板（usage_cached → UI） | ⚠️ 部分：usage_cached 已随 usage.updated 进入前端 store，UI 面板未做 |
| Agent 自识别会话策略（分类器 + 信号触发） | ❌ 待做 |

## 分期路线（每期独立可用）

> **进度（2026-08-26）**：M1–M4 已全部完成并合入 main。剩余收口项——会话自识别分类器、缓存命中率 UI 面板、codexapp multi_vendor 能力声明。

- **M1**：plan 词汇表 + plans 表 + 确定性执行器（dispatch + defer）+ 子任务树 —— ✅ 完成
- **M2**：lead agent 作为 planner（会话策略 + fork）+ task_sessions 树形化（parent_anchor_id）+ 评估 run + 会话自识别分类器 —— 除分类器外完成
- **M3**：consult_knowledge + KnowledgeRetriever + 缓存命中率面板 —— 后两项前者完成、面板未做
- **M4**：多 agent 路由全编排 + 认领模式 + 审批/预算护栏全量 —— ✅ 完成

## 参考与决策留痕

- 会话预算/轮换决策：`notes/implemented/architecture/2026-08-23-model-context-history-budget.md`
- resume 永不静默降级：`notes/implemented/architecture/2026-08-23-resume-never-silent-degrade.md`
- 编排架构参考：Paperclip（orchestrator + taskKey 会话锚点）、SpineCodex（模型自决压缩、前缀稳定纪律——取其教训，弃其单 harness 内嵌路线）
