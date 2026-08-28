# ZCode CUA 与子 Agent 活动事件契约

Status: proposed

## 决策与理由

当前 `tool.*` 已足够按真实顺序展示普通 Explore / Execute / Changes 调用，但 CUA 与
Agent/Task 子会话缺少父子关系和 response 归属。展示层只能诚实地给普通工具分组标题，不能把
reasoning、assistant message 或另一个 Run 猜成 CUA/subagent 子事件。

完整对齐 ZCode 前，canonical tool data 需要增加可选的 `tool_family`、`activity_group`、
`response_id`、`phase_id`；Agent 事件还需 `parent_call_id`、`parent_run_id`、`child_run_id`、
`child_work_item_id`、`child_agent_id` 与 `summary_text`。CUA 还需结构化 `action`、`target`、
`input`、截图/URL 等输出引用。现有 `tool.started/progress/completed/failed` 可以承载这些字段，
无需先新增 event name。

## 放弃了什么

- 不根据工具名、正文内容或相邻时间戳伪造父子 Run；名称启发式只用于普通展示组别。
- 不把当前 WorkItem `parent_id` 直接等同于某个 Agent tool call；实体关系不能证明事件因果。
- 不在没有截图/坐标/response_id 的情况下渲染假 CUA 轨迹。

## 复活条件

任一 Runtime 首次提供可验证的 child-run 或 computer-use 原生事件时：先在 adapter 写入上述
canonical data，并钉事件顺序/幂等测试；随后在 Web 投影 `cuaGroup` / `agentToolCall`，最后开放
对应显示设置。完成前 UI 仅展示通用工具行。
