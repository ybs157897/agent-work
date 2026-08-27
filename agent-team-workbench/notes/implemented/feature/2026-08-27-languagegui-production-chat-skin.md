# LanguageGUI 视觉迁入生产 Chat，运行架构保持不变

Status: implemented

## 决策与理由

把已由用户验收的 `/languagegui` 视觉迁入生产 `/chat`，但只迁移展示层：在 ChatPage 根增加专用的 `chat-languagegui-skin` 作用域，用浅蓝画布、白色浮层、蓝色强调、紧凑用户气泡、无重复角色头、结构化正文卡与圆角 composer 重映射现有语义 token 和 `.chat-*` 组件。正文与 composer 共用一条约 920px 的阅读轨；窄于该宽度时吃满可用空间。

生产运行架构保持原样：`TranscriptView` 继续按 user / assistant / thinking / activity / approval / diff / meta 分段，`MarkdownBody` 继续承担 GFM、代码、表格、数学、Mermaid 与流式节流；发送、停止、队列、审批、成果、SSE、usage 和会话恢复仍由现有 store 与 `ConversationPane` 控制。

## 放弃了什么

- 不把 `/languagegui` 的本地 `messages`、`parseSegments()`、`SYSTEM_PROMPT` 或 ` ```lgui ` JSON 协议搬进生产聊天；它们是 demo 契约，不是运行证据模型。
- 不全局改写 `.tx-scope`。旧暗色作用域仍是可回退的基底，新皮肤用更具体的 ChatPage 根作用域覆盖，避免影响其他页面和启动壳。
- 不为追求 720px 像素一致性压窄真实工具、审批和 diff；生产阅读轨放宽到约 920px，长载荷继续在各自组件内滚动。
- 不复制 demo 的模型选择、附件、语音等占位控件；生产模型来自 Agent profile，底部只保留真实可用的队列、停止和发送控制面。

## 复活条件

若真实工具/审批/diff 在 920px 阅读轨中出现经浏览器确认的不可用溢出，再为对应 segment 增加受控 breakout 宽度；不得因此绕过 `TranscriptView` 或把运行状态降级成 demo widget。
