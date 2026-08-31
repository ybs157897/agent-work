# 对话输出采用 LeAgent 展示骨架

Status: implemented

## 决策与理由

对话板块的消息骨架、正文标签层级、工具调用标签、Callout、代码面板与流式解析行为，以 LeAgent `1f16badc834abbd829d3cb7e9f8fcb5b2d57f443` 为视觉和交互基线重新实现。上游的结构与尺寸直接对齐；颜色统一映射到本项目语义 token，保留审批、终端、Read、Search、Diff 等控制面专用内容体。

采用重新实现而不是逐文件复制：LeAgent 是 Apache-2.0 项目，本仓库保留来源与版本说明，但组件继续服从当前数据模型、无障碍约束与设计门禁。

## 放弃了什么

放弃继续扩展现有 Codex 风格的头像回合头、Tracing Beam 与完成态逐标签显影；这些效果会与 LeAgent 的小型角色标签、安静正文和工具 chip 形成两套视觉语法。也不直接引入 LeAgent 的暗色主题和硬编码调色板，因为本项目目前只定义浅色水墨主题，状态色必须保持语义用途。

## 复活条件

若产品重新确定以 Codex 桌面端为对话视觉事实源，则从 `assistant-turn.tsx`、`markdown-body.tsx` 和 `.chat-prose` 整体切换基线；不得把 Tracing Beam 或逐标签显影局部叠回 LeAgent 正文。
