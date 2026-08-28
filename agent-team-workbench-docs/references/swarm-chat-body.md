# 蜂群模式 Chat 正文草案（索引）

> 静态 HTML 视觉稿 + 提案 note。用户 2026-08-29 确认方向可用；**尚未进生产组件**。

| 产物 | 路径 |
| --- | --- |
| 静态展示 | [swarm-chat-body-demo.html](./swarm-chat-body-demo.html) |
| 决策 note（proposed） | [../../agent-team-workbench/notes/proposed/feature/2026-08-29-swarm-chat-body.md](../../agent-team-workbench/notes/proposed/feature/2026-08-29-swarm-chat-body.md) |
| 待实现总览 | [../待实现/README.md](../待实现/README.md) |
| 渲染规格入口 | [../chat-rendering-spec.md](../chat-rendering-spec.md) |
| ZCode 交互对照 | [zcode-desktop-interaction-report.md](./zcode-desktop-interaction-report.md) |

## 一句话

主 agent 调度多路并行时，在 chat 正文里放「蜂巢巢」：格=agent、摘要默认/展开看思考与工具、合流后再写 final（徽章回指蜂格）。

## 本地预览

```bash
cd agent-team-workbench-docs/references
python3 -m http.server 8768 --bind 127.0.0.1
# 打开 http://127.0.0.1:8768/swarm-chat-body-demo.html
```

勿依赖 Cursor 对本地 `file://` 的预览；用 HTTP 打开。
