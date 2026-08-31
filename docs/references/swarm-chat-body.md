# Kimi 蜂群模式 Chat 正文（索引）

> 静态 HTML 视觉稿 + 生产实现索引。用户 2026-08-29 确认方向；同日完成事件契约、生产组件与浏览器验收。

| 产物 | 路径 |
| --- | --- |
| 静态展示 | [swarm-chat-body-demo.html](../archive/design/swarm-chat-body-demo.html) |
| 决策 note（implemented） | [../../notes/implemented/feature/2026-08-29-swarm-chat-body.md](../../notes/implemented/feature/2026-08-29-swarm-chat-body.md) |
| 通用正文架构 | [../../notes/implemented/architecture/2026-08-29-agent-scoped-transcript-reader.md](../../notes/implemented/architecture/2026-08-29-agent-scoped-transcript-reader.md) |
| 生产组件 | [../../agent-team-workbench/web/src/components/chat/swarm-chat-block.tsx](../../agent-team-workbench/web/src/components/chat/swarm-chat-block.tsx) |
| 子 Agent 右栏 | [../../agent-team-workbench/web/src/components/chat/swarm-member-workspace.tsx](../../agent-team-workbench/web/src/components/chat/swarm-member-workspace.tsx) |
| 通用正文投影 | [../../agent-team-workbench/web/src/utils/agent-transcript-projection.ts](../../agent-team-workbench/web/src/utils/agent-transcript-projection.ts) |
| 唯一正文阅读器 | [../../agent-team-workbench/web/src/components/chat/transcript-view.tsx](../../agent-team-workbench/web/src/components/chat/transcript-view.tsx) |
| Kimi 事件投影 | [../../agent-team-workbench/internal/runtime/adapters/kimiapp/kimiapp.go](../../agent-team-workbench/internal/runtime/adapters/kimiapp/kimiapp.go) |
| 渲染规格入口 | [chat-rendering-spec.md](../frontend/chat-rendering-spec.md) |
| ZCode 交互对照 | [zcode-desktop-interaction-report.md](./zcode-desktop-interaction-report.md) |

## 一句话

只有 Kimi `AgentSwarm` 的显式成员进入 chat「蜂巢巢」；蜂格默认显示题号、原始任务与短结果，点击后在 Chat 右侧栏用和主 Agent 完全相同的 `AgentTranscriptReader` 阅读真实 thinking、工具、Markdown/KaTeX 与 final。Kimi/Codex 普通子 agent 后续沿用同一 agent-scoped 正文能力，但不会被误判为蜂群；父 final 用结构化汇总表收口。

## 本地预览

```bash
cd docs/archive/design
python3 -m http.server 8768 --bind 127.0.0.1
# 打开 http://127.0.0.1:8768/swarm-chat-body-demo.html
```

勿依赖 Cursor 对本地 `file://` 的预览；用 HTTP 打开。
