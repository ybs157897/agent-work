# zcode 工作活动时间线

日期：2026-08-28
状态：implemented

## 已确认需求

ZCode 对话中的思考、正文和工具调用必须按 Run 的真实事件顺序呈现，而不是把一轮中的所有 reasoning 或工具统一汇总到末尾。目标序列为：

`thinking → interim assistant/tool → thinking → interim assistant/tool → final assistant`

其中每个连续工具批次是一个独立 ActivityGroup；正文出现后，后续工具必须开始新的批次。interim assistant 始终是普通 Markdown 正文的独立 sibling，不属于 thinking；最终结果正文始终独立展示在 WorkTimeline 外。

## 展示契约

- 运行中的 WorkTimeline 默认展开；每段 thinking 默认折叠为显示最新推理的摘要行，展开后纵向滚动并跟随最新推理；便于看到过程正文与工具摘要的实时状态。
- 成功完成的 WorkTimeline 默认折叠为「已工作 X」；用户点击后查看完整工作日志。
- 失败或中断的 WorkTimeline 默认展开，直接暴露失败工具和错误信息；内部 ActivityGroup 仍默认折叠成跟随当前批次摘要的单行命令摘要，用户可展开查看日志。
- 只有终态才把 thinking、interim assistant、tools、approval 和错误证据整体收进「已工作 X」WorkTimeline；final answer 永远在外部独立展示。
- `tool.progress`、`tool.completed`、`tool.failed` 只更新既有工具行，不创建新的阶段。
- reasoning 阶段使用稳定的 phaseId/renderKey，live 到 settled 时保持同一面板身份和展开状态。

## ZCode 精确交互对齐

- 折叠思考只滚动展示最后一条非空行；运行中标题使用扫光，摘要过宽带左右 fade mask，内容换行有轻量过渡。
- 思考体上限 240px，只有用户仍贴近底部时才随流跟滚；用户上翻即停止抢滚。折叠退场保留 300ms 后再卸载。
- reasoning 的 streaming→settled 自动收起必须尊重用户点击；工具的 running→terminal 则按 ZCode `autoCollapseOnComplete` 收成摘要。
- 用户明确否决 ZCode 的长 final 高度帽：Final Answer 默认完整展开，继续按安全 Markdown 段落流式渲染，不显示展开/收起控制。
- 工具展示层允许按 Explore / Execute / Changes 类别投影，但阶段正文与 reasoning 仍是硬边界；没有 CUA/subagent 子事件协议时不伪造嵌套轨迹。
- 保留 Run 级「已工作 X」总收口层：这是用户已确认的 ZCode 桌面效果；内部 row 仍以稳定 key 独立存在，不能合并成一段文本。

## 实时输出密度

- WorkTimeline 内工具批次与 thinking 使用同一 44px 轻量行；timeline 变体不显示「工具执行」标题或卡片外壳，只保留小工具图标、当前动作 ticker、数量/状态与展开箭头。
- `tool.started` 是可见事件：首个 started 到达就必须出现折叠行，后续同组调用增量加入展开列表；不等待整组 terminal。
- 正文 Markdown 采用稳定块流：已完成段落保持稳定，当前段继续流式更新，未闭合代码/数学/结构块仍安全缓冲；不得先显示 raw 再替换为 rendered。
- Run 活跃且当前没有 reasoning、running tool 或 pending approval 时，在最新正文后显示一个无背景的小 loading 图标；正文继续流式时 loading 保持，终态立即移除。
- reasoning 末 delta 后连续 700ms 无新内容时，思考行暂时落定并显示后置 loading；真实耗时只算首末 delta，不把下一节点前的静默等待计入「持续了 X」。
- live final draft 不再留在 WorkTimeline 内等待 `message.completed`：它以普通 assistant 正文立即投影到外层；若后续出现新工具，该段落在下一次投影中自然落定为对应阶段的过程正文，事件顺序不变。

## 取舍

阶段边界采用新的 `tool.started` 与消息阶段内容，而不是等待所有 call_id 都完成。真实 Agent 可能缺少 terminal 事件，等待全量 terminal 会让后续正文永久无法进入时间线。连续工具仍由 ActivityGroup 聚合，以保持扫描密度；正文和 thinking 则保留为可定位的独立 segment。

## 负向保证

本实现明确不做以下事情：

- 不把整轮 reasoning 汇总为一张思考大卡；
- 不把所有工具调用堆进一个跨阶段 ActivityGroup；
- 不按 progress/terminal 事件切碎思考或工具批次；
- 不让最终正文覆盖、替换或重新排序此前已展示的 interim 正文/工具；
- 不从模型正文、Markdown 或静态 widget 数据推断并伪造工具调用。

## 验收依据

纯投影测试覆盖 `thinking → tool → thinking + interim assistant → tool → thinking → final`，并验证连续工具只生成一个 ActivityGroup、重复 `message.completed` 不重复输出，以及 phaseId 支持 live→settled key 稳定。视觉与交互规则以 `web/DESIGN.md` 和 `agent-team-workbench-docs/chat-rendering-spec.md` 为事实源。

真实 1325 事件会话验证：11 段 reasoning 保持独立；ZCode 默认分组把 82 个工具投影为
14 个 Agent/Explore/Execute 组；reasoning 折叠体在 300ms 退场后卸载；Final Answer
默认完整展示，不再使用 120px 高度帽。关闭「显示全部思考」后每个 Run 恰好保留
第一段，恢复开关后回到默认全量。完整 CUA/subagent 嵌套仍受
[ZCode CUA 与子 Agent 活动事件契约](../../proposed/architecture/2026-08-28-zcode-activity-event-contract.md)所述事件字段约束。

本轮实时密度验收：真实页面中前八个工具折叠行与全部七个思考折叠行均为 44px；
六调用组展开后保持六条独立列表项；终态页面没有残留 loading。前端 67 个测试文件、
552 条测试与 TypeScript/ESLint/Vite build 全部通过；后端 gofmt、build、vet 与 Kimi adapter race test 通过。
