# Agent-scoped 通用正文

Status: implemented

## 决策与理由

主 Agent 与子 Agent 使用同一套 canonical transcript 和同一个 `AgentTranscriptReader`；两者
只有 `agent_id` 数据归属不同。`run_events` 保留跨 Agent 的全局 `run_seq`，事件同时携带
稳定 `agent_id`；旧事件缺失身份时只按 `main` 回放。进入 UI 前先按 Agent 过滤，再在该
scope 内执行 delta cap、thinking fold、tool bundle 和 final 投影。

Kimi app-server 的 session 流会同时广播 main 与 child。child 的 thinking、assistant、tool、
usage 与 terminal 被记录为同类 canonical 事件，但 child terminal 永不推进父 Run 终态。
主 Chat 与右侧子 Agent 正文都消费同一种 `PresentedTranscriptSegment`；右栏只负责身份、选择、
关闭与独立滚动，不实现第二套 Markdown、数学公式、工具卡或 thinking UI。

审批仍是 session/run 级控制面事实：KAP 当前审批事件没有可安全归属 child 的 agent identity，
因此继续在主 Run 工作时间线中处理，不把它猜测性复制到某个子 Agent 正文。

## 放弃了什么

- **继续把 `resultSummary` 当“完整输出”**：它只有终态摘要，无法还原 thinking、tool 和阶段顺序。
- **为子 Agent 新建专用事件名/表/渲染器**：会造成父子正文语义分叉，后续 Kimi 普通 Agent 与 Codex 子 Agent 还要重复接入。
- **混合事件先做 500 帧 cap 再按 agent 过滤**：高频 main 或其他 child delta 会把目标 Agent 的结构事件挤掉；cap 必须在 agent scope 内完成。
- **让 child `turn.ended` 推进父 Run**：KAP 的 turnId 只在 Agent 内唯一，父终态只接受 `agent_id=main`。
- **按 call ID 全 Run 合并工具**：不同 Agent 可以复用同一个 call ID；工具生命周期必须带 Agent scope。

## 验收边界

- Kimi AgentSwarm 与 Codex collaboration child 均提供 agent-scoped transcript；Codex child
  使用标准 app-server `thread/list + thread/turns/list` 回收历史。普通 Kimi Agent 后续只需
  产出同一 `agent_id` canonical 事件。
- 旧 Run 没有 agent-scoped 事件时，右栏明确显示“生命周期摘要”fallback，不伪造 thinking/tool。
- 第一阶段只读；子 Agent steering/stop/retry 等交互另立控制契约。
