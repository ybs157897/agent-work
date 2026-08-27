# LanguageGUI 工具调用 ActivityGroup 视觉契约

Status: proposed

## 决策与理由

生产工具调用统一由 `ActivityGroup` 负责展示。它把同一个 run 的真实工具事件按事件
顺序聚合为可扫描的 chip 轨，再将当前 chip 的详情渐进披露为 Terminal、Read、Search、
Diff 或 IN/OUT 内容体。这样既保留了工具执行的审计顺序，也让默认正文维持
LanguageGUI 的白色/浅蓝阅读表面，不会被一串终端输出打断。

视觉上固定四个约束：白色/浅蓝容器和细语义边框；品牌蓝只表示互动；状态色只表示真实
执行状态；状态同时提供图标和文字。运行中的 loader、展开与焦点必须支持 reduced-motion，
长输入/输出必须在详情体内滚动或截断，不能撑破正文阅读轨。

## 事件边界

- 工具 chip 的名称、类型、参数摘要、耗时、顺序和终态只能来自同一 run 的真实
  `tool.*`/transcript 事件。
- 助手 Markdown、模型自报的“我执行了某工具”、普通 `content_blocks` 都不能创建
  ActivityGroup 或改变其状态。
- bash/code、read、search、write/edit、mcp/other 只改变详情渲染器，不改变组协议。
- 同一组同时只展开一个 chip；没有真实事件时展示空态，不渲染“成功”的静态调用。

## Demo 边界与放弃项

`/languagegui` 的演示只能复用生产 `ActivityGroup`、`ToolRow` 和详情渲染器，并使用可
审计的 fixture 事件做视觉回归。不会保留 demo 专用工具 schema，也不会通过静态卡片或
模型正文伪造工具成功态。

在后端为工具事件提供稳定的 typed payload、权限边界和历史回放迁移前，不新增工具卡
上的任意执行按钮、重跑按钮或文件打开动作；详情区保持只读，避免视觉演示暗示不存在
的 Runtime 能力。
