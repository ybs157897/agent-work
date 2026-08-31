# 流式输出单一投影与临时追踪

Status: implemented

## 决策与理由

Chat 正文只保留一棵展示树：普通 Markdown 直接从累计 `message.delta` 进入节流后的
Markdown renderer；代码、LanguageGUI JSON、Mermaid 与数学等不能安全增量解析的尾部先缓冲，
闭合后原子渲染。完成事件只补齐 canonical 数据和交互能力，不再通过
`streaming -> settled` 强制重挂载把“源码节点”替换成“成品节点”。

时间线按阶段投影：reasoning/text delta 形成当前思考与正文阶段；下一条 `tool.started`
到达前先按 thinking → interim assistant 的顺序落下，再开启新的 ActivityGroup。interim assistant
始终是普通 Markdown 正文的独立 sibling，不属于 thinking。每段 thinking 默认折叠为可跟随最新推理
的摘要行，每个连续工具批次保持为一个默认折叠且跟随当前批次摘要的 ActivityGroup；Run 外层
WorkTimeline 才负责运行中展开、成功完成折叠、失败/中断展开。只有 `tool.progress/completed/failed`
时不切段，它们只更新已有工具行。这样同一 run 始终是
thinking → interim assistant/activity → thinking → interim assistant/activity → final assistant
的交替序列；只有终态才将前述工作段整体收进「已工作 X」，真正 final assistant 保持在时间线外
独立落段，既不整轮汇总 reasoning，也不按 progress 切碎。

时间线超过容量上限时，逐出的 reasoning 不再整轮挂到最后一个 message.completed；它按阶段
折回对应的 `tool.started` / `message.completed` 边界，并携带首 delta 时间与 phaseId。历史长 Run
因此仍保持原顺序，阶段耗时也不因截断退化为 0。

同一容量策略也适用于 text-delta，但正文与 reasoning 的预算不同：interim text 是用户可见输出，
必须以完整阶段文本粘滞保存到对应边界，不能套用 reasoning 的 4000 字符尾部预算。最后一个
message.completed 若携带累计全文，final 只剥离已完整投影的 interim 前缀；最后工具之后的当前
text 阶段直接作为 final candidate，不先进入 WorkTimeline。final 边界只取事件顺序，不以 Markdown
标题、语言或正文内容启发式判断。

工具 terminal 同样必须事实化：Kimi eventPump 以 call_id 跟踪 started/result；parent turn 结束仍未见
result 的调用在 adapter 内发 `tool.failed(status=interrupted)`，而不是等待一个已经无法消费的迟到帧，
也不在不了解 call_id 的 ModuleRunner 层伪造成功。

不使用“全部 active call_id 都 terminal”作为批次关闭条件：真实 Agent 工具可能只有 started
而没有 terminal，该策略会让后续阶段永久无法关闭。新 tool.started + 已有内容缓冲才是可靠边界。

为了定位 adapter、时间线、投影和 DOM 之间的内容/时序差异，新增独立的 DEV ring buffer。
它不进入 Zustand，也不参与 React state；只有显式 `outputTrace=1`（或对应 localStorage）
时记录时间、标识、长度和稳定 hash。正文内容需额外 `outputTraceContent=1`，默认不落日志，
控制台输出还需单独开关。

## 放弃了什么

不尝试从半截 LanguageGUI JSON 或 Mermaid 源码猜测结构，也不为流式 widget 扩展新的后端
事件协议；在 canonical `content_blocks` 只存在于 `message.completed` 的前提下，复杂块按
“完整后出现”处理。工具调用仍由真实 `tool.*` 事件渲染，不混入正文追踪协议。

追踪缓冲只服务本地诊断：固定容量、可清空/导出，不持久化到后端，不默认打印内容，不上传。

本次确认的负向保证：不把整轮 reasoning 汇总为一张思考大卡；不把所有工具调用堆进一个全局工具卡；不因
工具 progress 或 terminal 事件生成碎片思考段；不让最终正文替换或覆盖此前按顺序展示的 interim assistant/tool；
不以模型正文、Markdown 或静态 widget 数据伪造工具调用。

## 诊断入口

DEV 环境设置 `agent-workbench:output-trace=1` 后刷新，浏览器会暴露
`window.__LANGUAGEGUI_OUTPUT_TRACE__` 的 `get/export/clear/flags` 四个只读诊断入口。
正文采样和 console 输出分别由 `agent-workbench:output-trace-content=1`、
`agent-workbench:output-trace-console=1` 二次授权；前者单条最多保留 24000 字符，ring buffer
固定 1000 条。分析完成后删除开关，生产构建无论参数为何都不启用。
