# 蜂群模式 · Chat 正文展示草案

Status: proposed

静态视觉稿（用户 2026-08-29 确认「可以的」）：
[swarm-chat-body-demo.html](../../../../agent-team-workbench-docs/references/swarm-chat-body-demo.html)

对照调研：
[zcode-desktop-interaction-report.md](../../../../agent-team-workbench-docs/references/zcode-desktop-interaction-report.md)
（ZCode 子 agent 是单路 `agentToolCall`+子会话，不是多路并排巢）；
LanguageGUI / Ant Design X / CopilotKit 均无现成「蜂群正文」成品，故自拟草案。

## 决策与理由

在 **同一条 chat 时间线**里用「蜂巢巢」承载蜂群调度，而不是侧栏任务板或另开会话列表：

1. **巢头**：任务名 +「已完成/总数」+ 进度条——一眼知并行进度。
2. **蜂格网格**：每位 agent 一格；默认摘要行（角色 · 一行 ticker · 状态胶囊）；展开才露迷你思考 + 工具链。
3. **状态**：执行中 / 已完成 / 排队（依赖未齐）/ 失败——依赖用排队，不空转扫光。
4. **合流条**：已完成格亮、未完成灰；主 agent 写 final 时用小徽章回指蜂格，**不重放**子 transcript。
5. 完整子会话若需要，另开抽屉（草案未做）；正文只做投影。

选并排蜂格而非 ThoughtChain 单链：蜂群是并行 fan-out，单链会伪装成串行。
选正文内嵌而非纯侧栏：用户仍在读「这一轮回答怎么来的」，上下文不跳页。

## 放弃了什么

- **照搬 ZCode 子 session 抽屉当唯一形态**：适合一对一子 agent，不适四路并排扫进度。
- **Ant Design X ThoughtChain 当蜂群容器**：嵌套链表达调用层级，不表达并行调度与依赖排队。
- **LanguageGUI multi-prompt Action 链**：可编辑工作流控件，生产 chat 只要只读运行态。
- **CopilotKit DelegationLog 原样**：委派日志卡可以参考，但缺「格内迷你时间线 + 合流引用徽章」的正文节奏。
- **四路全文摊进 transcript**：会撑爆阅读轨；细节必须下沉到展开态。

## 负向保证（落地时）

- 蜂格不是第二套工具 ActivityGroup；工具仍在格内迷你链或既有 activity 语义，不伪装成 LanguageGUI ContentBlock。
- Final 不得复制粘贴各 agent 全文；只允许摘要 + 回指徽章。
- 未齐套的依赖 agent 显示排队，禁止假「思考中」扫光。

## 复活 / 落地条件

【产品确认蜂群 run 有稳定的 agent 身份、状态、一行摘要与可选 thinking/tool 投影事件】
→ 在 `web` 增加 transcript 段或嵌套投影组件，对齐
[chat-rendering-spec.md](../../../../agent-team-workbench-docs/chat-rendering-spec.md)
与 `DESIGN.md` LanguageGUI 阅读轨 → 用静态稿验收并行/排队/合流/引用四态。
