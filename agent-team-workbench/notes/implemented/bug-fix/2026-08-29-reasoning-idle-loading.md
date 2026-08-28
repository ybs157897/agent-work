# reasoning 静默空窗的耗时与 loading 修复

日期：2026-08-29
状态：implemented

## 现场证据

会话 `wi_01M14GZNJW5ZR8P686BEH6M93N` 的 Run
`run_01M14GZP47M8XQ60M90CJX9R1P` 共 615 个事件。倒数第二段 reasoning：

- 首 delta：`2026-08-28T15:50:01.400673Z`
- 末 delta：`2026-08-28T15:50:01.973207Z`
- 下一事件：`tool.started`，`2026-08-28T15:50:54.368966Z`
- reasoning 实际输出：573ms；无事件空窗：52.396s

旧投影以“下一节点开始时间”作为 settled reasoning 的结束时间，因此显示「持续了 52 秒」；
live 投影又用 `!answerDraft` 等同于“reasoning 仍在流式”，整段空窗都禁止 loading。

## 修复契约

- thinking 消息以最后一条 reasoning delta 写入 `completedAt`；普通边界、最终消息与超帽
  `reasoning_folded` 都保留该时间。
- 思考耗时优先计算 `startedAt → completedAt`，不把等待工具/正文的无事件空窗算入。
- 活跃 Run 的 reasoning 连续 700ms 没有新 delta 时，当前思考行停止 streaming 视觉，后置一个
  小型 output loading；新 delta 到达后恢复 streaming 并移除 loading。
- running tool、pending approval 与 terminal Run 不显示该 idle loading。

## 取舍与负向保证

当前事件契约没有 reasoning-ended 帧。700ms 静默只用于展示层的“输出暂歇”判断，不写入时间线、
不伪造模型阶段结束，也不改变 Run 状态机。权威耗时仍来自真实 delta 时间戳；后续若协议提供显式
phase end，应删除静默启发式并直接消费该事件。

## 回归钉

- 精确使用现场三个时间戳，断言 thinking 的 `completedAt` 为末 delta，区间为 573ms。
- 渲染断言耗时显示 `<1 秒`，绝不回退到 `52 秒`。
- 静默 reasoning 渲染为 settled 行并紧跟可访问 loading；新流式、工具、审批、终态状态矩阵保留。
