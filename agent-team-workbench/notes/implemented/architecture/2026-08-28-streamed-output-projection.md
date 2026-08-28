# 流式输出单一投影与临时追踪

Status: implemented

## 决策与理由

Chat 正文只保留一棵展示树：普通 Markdown 直接从累计 `message.delta` 进入节流后的
Markdown renderer；代码、LanguageGUI JSON、Mermaid 与数学等不能安全增量解析的尾部先缓冲，
闭合后原子渲染。完成事件只补齐 canonical 数据和交互能力，不再通过
`streaming -> settled` 强制重挂载把“源码节点”替换成“成品节点”。

时间线按阶段投影：任何可见 `tool.*` 边界前先把已经到达的 text-delta 正文连同其
reasoning 上下文落成临时正文段；只有 reasoning 时不切开并行工具。同阶段连续工具折为
一条 ActivityGroup，正文重新出现后，下一批工具另起一组。ActivityGroup 展开态是纵向
日志，不把整轮工具汇总成横向 Action 卡墙。

为了定位 adapter、时间线、投影和 DOM 之间的内容/时序差异，新增独立的 DEV ring buffer。
它不进入 Zustand，也不参与 React state；只有显式 `outputTrace=1`（或对应 localStorage）
时记录时间、标识、长度和稳定 hash。正文内容需额外 `outputTraceContent=1`，默认不落日志，
控制台输出还需单独开关。

## 放弃了什么

不尝试从半截 LanguageGUI JSON 或 Mermaid 源码猜测结构，也不为流式 widget 扩展新的后端
事件协议；在 canonical `content_blocks` 只存在于 `message.completed` 的前提下，复杂块按
“完整后出现”处理。工具调用仍由真实 `tool.*` 事件渲染，不混入正文追踪协议。

追踪缓冲只服务本地诊断：固定容量、可清空/导出，不持久化到后端，不默认打印内容，不上传。

## 诊断入口

DEV 环境设置 `agent-workbench:output-trace=1` 后刷新，浏览器会暴露
`window.__LANGUAGEGUI_OUTPUT_TRACE__` 的 `get/export/clear/flags` 四个只读诊断入口。
正文采样和 console 输出分别由 `agent-workbench:output-trace-content=1`、
`agent-workbench:output-trace-console=1` 二次授权；前者单条最多保留 24000 字符，ring buffer
固定 1000 条。分析完成后删除开关，生产构建无论参数为何都不启用。
