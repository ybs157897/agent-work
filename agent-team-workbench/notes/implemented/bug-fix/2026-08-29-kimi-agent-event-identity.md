# Kimi session 事件按 Agent 隔离

Status: implemented

## 决策与理由

KAP 的 session WebSocket 会同时广播主 Agent 与所有子 Agent 的事件，`turnId` 只在单个
Agent 内唯一。Workbench 因此用 `(agentId, turnId)` 复合判断父 Run 归属：只有
`agentId=main` 的 turn、delta、usage 与 tool 事件能推进或结束父 Run；`subagent.*`
生命周期继续按 `subagentId` 投影蜂格。

真实回归中主 Agent 与十个蜂群成员均出现 `turnId=0`。仅按 turnId 过滤会把首个子
Agent 的 `turn.ended` 当成父终态，提前合成 `turn_ended_before_tool_result`，并把并发的
子输出拼进父正文。缺失 `agentId` 同样无法安全归属，故 fail closed。

## 放弃了什么

- **只修前端 stopped/乱码展示**：canonical 事件已经被污染且父 Run 提前终止，展示层无法恢复。
- **接受空 agentId 为 main**：当前固定 KAP 协议与真实 0.38 事件均携带 agentId；兼容空值会保留同类误归属入口。
- **统一丢弃所有 child 事件**：`subagent.*` 是蜂格状态与结果来源，子 Agent 审批也仍需控制面处理；只隔离父 turn 的输出和终态面。
