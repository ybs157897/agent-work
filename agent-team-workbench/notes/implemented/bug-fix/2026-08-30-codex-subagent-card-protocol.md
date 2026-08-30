# Codex 子 Agent 卡片协议闭环

Status: implemented

## 决策与理由

Codex 0.149.0 的协作 item 是 `collabAgentToolCall`，子线程身份来自
`receiverThreadIds`；完整正文不在父工具摘要里，而在独立 child thread。Adapter 因此把
协作生命周期投影为 `subagent.updated(runtime=codex, role=child)`，并在父 turn terminal 后
通过 app-server `thread/list` 与分页 `thread/turns/list(itemsView=full)` 回收 child reasoning、
agentMessage 和 tool，统一写为带 child thread `agent_id` 的 canonical transcript。

app-server 会在同一 stdout 上交错父、子实时通知，且父子 `turnId` 可能相同；事件作用域只认
通知信封的 `threadId`。实时 child reasoning、message、tool 已到达时，历史回收按内容类别跳过
补发，避免 app-server 的实时 item ID 与持久化 item ID 不一致造成重复正文。

Chat 的 Codex 子 Agent 卡片只负责身份、状态和选择；右侧正文继续使用既有
`buildAgentTranscriptProjection + AgentTranscriptReader`。主、子 Agent 没有第二套 Markdown、
thinking、工具或数学公式渲染器。右栏把 lifecycle description 投影成 user segment，与 child
transcript 一起进入同一个 reader，保持用户靠右、Agent 靠左；状态、类型、Subagent ID 与父级
Run/Thread 保留为内部事件和 selection 数据，不作为正文元数据重复展示。

## 放弃了什么

- **从 rollout JSONL 扫描子输出**：JSONL 是 Codex 持久化实现，不是 Workbench runtime 协议；
  app-server 已提供权威 thread API。
- **只把协作调用当普通 tool 行**：它无法提供可选择的 child identity，也无法呈现完整正文。
- **把 Codex child 伪装成 Kimi 蜂巢成员**：两者调度语义不同；Codex 使用普通子 Agent 卡片，
  只共享右侧正文阅读器。
- **父 turn/completed 后立即 SIGINT**：会抢在 app-server flush 前终止进程，使已成功的 provider
  thread 被持久化为 interrupted；正常路径先等待 EOF 自然退出，超时才升级信号。
- **用 `turnId` 区分父子事件**：该字段不是 Agent 身份，复用时会把 child Bash 与思考串进主正文；
  `threadId` 才是唯一 scope authority。
