# 蜂群模式 · Chat 正文展示草案

Status: implemented

静态视觉稿（用户 2026-08-29 确认「可以的」）：
[swarm-chat-body-demo.html](../../../docs/archive/design/swarm-chat-body-demo.html)

对照调研：
[zcode-desktop-interaction-report.md](../../../docs/references/zcode-desktop-interaction-report.md)
（ZCode 子 agent 是单路 `agentToolCall`+子会话，不是多路并排巢）；
LanguageGUI / Ant Design X / CopilotKit 均无现成「蜂群正文」成品，故自拟草案。

## 决策与理由

在 **同一条 chat 时间线**里用「蜂巢巢」承载蜂群调度，而不是侧栏任务板或另开会话列表：

1. **巢头**：任务名 +「已完成/总数」+ 进度条——一眼知并行进度。
2. **蜂格网格**：每位 agent 一格；默认摘要行显示 1-based 题号、原始任务、短结果 ticker 与状态；点击后在 Chat 右侧详情栏用通用 `AgentTranscriptReader` 展示真实 thinking、阶段正文、工具与 final。
3. **状态**：执行中 / 已完成 / 排队（依赖未齐）/ 失败——依赖用排队，不空转扫光。
4. **合流条**：已完成格亮、未完成灰；主 agent 写 final 时用小徽章回指蜂格，**不重放**子 transcript。
5. 右侧栏只负责成员身份与独立滚动；正文与主 Agent 完全复用同一套 Markdown、KaTeX、
   thinking、ActivityGroup 和 final 排版。旧 Run 尚无 agent-scoped transcript 时，明确回退为
   “生命周期摘要”，不伪造 thinking/tool 明细。

选并排蜂格而非 ThoughtChain 单链：蜂群是并行 fan-out，单链会伪装成串行。
选正文内嵌而非纯侧栏：用户仍在读「这一轮回答怎么来的」，上下文不跳页。

### 模式边界（实现定案）

- **蜂群是 Kimi 独有投影**：仅 Kimi `AgentSwarm` 批次进入蜂巢；KAP
  `subagent.spawned.swarmIndex` 是唯一成员判据。
- **普通子 agent 不是蜂群**：Kimi `Agent` 与 Codex collaboration 子 agent 都走单路
  `agentToolCall` / 子会话投影；即使同时出现多个，也不得按数量猜成蜂群。
- `runInBackground` 只表达后台执行，不参与蜂群判定；工具名、正文关键词、相邻时间戳也
  都不是身份依据。
- canonical 事件用 `subagent.updated` 保存完整快照；`role=member` 表示 Kimi 蜂群成员，
  `role=child` 表示普通子 agent。蜂巢只消费
  `runtime=kimi + role=member + 1-based 正整数 swarm_index`；非法 0-based 值 fail-closed。

### 生产事件契约

Kimi `AgentSwarm` 父工具开始时，`tool.started.data.swarm` 必须提供 `runtime/id/title/total/items`；
前端据此直接创建蜂巢，并用声明项显示真实排队格。随后 KAP 的
`subagent.spawned/started/suspended/completed/failed` 由 adapter 投影成自包含的
`subagent.updated`，至少携带：

`subagent_id/name/parent_tool_call_id/description/swarm_index/role/run_in_background/status`
以及终态 `summary/error`。状态映射为 `queued/running/waiting/completed/failed`；用户或父回合
中断可在展示层收为 `stopped`，不得伪装成成功。

KAP 的 turn id 只在单个 agent 内唯一；session 事件流必须按 `(agentId, turnId)` 复合过滤。
父 Run 只由 `agentId=main` 的 turn terminal 推进；child 的 delta/thinking/tool/usage/terminal
以 `agent_id=subagentId` 写入同一 Run 的 canonical 时间线，`subagent.*` 同时投影成员生命周期。
子 agent 复用父 turnId 或 callId 时，永不允许提前结束或污染父输出。

事件只有身份、状态和结果摘要时，展开格只展示这些事实；有 agent-scoped transcript 时，
按该成员 scope 投影真实 thinking/tool/final，没有就不生成假的迷你思考或工具日志。

## 放弃了什么

- **照搬 ZCode 子 session 抽屉当唯一形态**：适合一对一子 agent，不适四路并排扫进度。
- **Ant Design X ThoughtChain 当蜂群容器**：嵌套链表达调用层级，不表达并行调度与依赖排队。
- **LanguageGUI multi-prompt Action 链**：可编辑工作流控件，生产 chat 只要只读运行态。
- **CopilotKit DelegationLog 原样**：委派日志卡可以参考，但缺「格内迷你时间线 + 合流引用徽章」的正文节奏。
- **四路全文摊进 transcript**：会撑爆阅读轨；细节必须下沉到展开态。

## 负向保证

- 蜂格不是第二套工具 ActivityGroup；工具仍在格内迷你链或既有 activity 语义，不伪装成 LanguageGUI ContentBlock。
- Final 不得复制粘贴各 agent 全文；只允许摘要 + 回指徽章。
- 未齐套的依赖 agent 显示排队，禁止假「思考中」扫光。
- Kimi 普通 `Agent` 和 Codex 子 agent 永不因“数量大于一”自动升级成蜂巢。
- 不解析模型正文或 `AgentSwarm` XML 输出反推实时成员；实时与历史都以 canonical 事件为准。

## 落地结果

- 主/子共用正文与 `agent_id` 隔离决策见
  [Agent-scoped 通用正文](../architecture/2026-08-29-agent-scoped-transcript-reader.md)。
- Runtime 默认策略见 [Runtime Agent 模式默认值](../architecture/2026-08-29-runtime-agent-mode-defaults.md)：
  Kimi 默认 yolo + swarm，Codex 默认 multi-agent V2 + Ultra effort。
- Kimi app-server adapter 将 `AgentSwarm` 父工具和 `subagent.*` 生命周期投影为
  `tool.started.data.swarm` + `subagent.updated`；普通 Kimi child 保持 `role=child`。
- `run_events`、历史 API 与 SSE 顶层携带 `agent_id`；旧事件缺失身份时归为 main。Web 按
  Agent scope 独立 cap/fold/tool bundle，再从同一 Run 时间线重建主正文与成员正文；长历史
  只保留每个成员的最新生命周期快照，父工具终态缺成员证据时收为 stopped，不伪造 completed。
- 生产组件为 `web/src/components/chat/swarm-chat-block.tsx`；queued/running/waiting/
  completed/failed/stopped 六态、组级进度、题号/短结果摘要、右侧 `SwarmMemberWorkspace`、
  合流条与 reduced-motion 已落地；右栏 selection 用 run/swarm/member 复合键从实时 messages 派生，
  内部复用唯一 `AgentTranscriptReader`，父工具 items 的原始任务不会被通用 lifecycle 描述覆盖。
- 显式 `languagegui` fence 若仅漏冗余 version、其 blocks 仍通过 v1 白名单时可安全渲染；
  这让模型生成的十题汇总表不再退化成原始 JSON，显式未知版本仍 fail closed。
- `/languagegui` 展台直接复用生产组件做可审计只读 fixture；生产 `/chat` 浏览器验收覆盖蜂格选中、
  右栏打开/关闭、Markdown/KaTeX 输出、等待/失败文案与双主题，控制台无错误。
- 新 Run 会保存 KAP child 的真实 thinking、assistant delta、tool、usage 与 completed；迁移前的旧 Run
  没有这些 agent-scoped 事件时，右栏继续使用“生命周期摘要”兼容历史事实。
