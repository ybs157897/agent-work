# 工作区（Artifact 承载面）与工具渲染器注册表

Status: implemented

输入：知乎《从 Chat UI 到 Agent UI》第三/五/八章对照审计。方案见
`agent-team-workbench-docs/frontend-design-md-redesign.md` §7。

## 决策与理由

1. **Artifact 工作区只消费既有链路，不造数据面**：审计发现后端
   `GET /runs/{id}/artifacts`、`artifact.created/updated` 事件、outbox 投影、
   `runs.store.artifacts`（watchRun 自动拉取 + 事件刷新）**全部就绪**，缺的
   纯是呈现层。UI 只做清单：聊天区 `ArtifactShelf` 摘要卡（超 4 项折叠）+
   右侧 `ArtifactWorkspace` 面板 + 页头开关。
2. **内容预览是 Known Gap，不臆造**：后端只暴露元数据（logical_path/mime/
   size/sha256），无内容端点——面板是清单不是预览器，写进 DESIGN.md
   Known Gaps 第 7 条，等后端补内容面再立项。
3. **工具注册表形式化而非重建**：审计结论是注册表实质已存在
   （classifyTool 精确表+正则兜底 = 键解析；ExpandedBody 按族分派
   Terminal/Read/Search/Diff 块 = 专用渲染器；toolActivityTitle = 业务语义层）。
   只做两件事：隐式 switch 显式化为 `TOOL_BODY_RENDERERS`（行为逐字等价），
   补 `code` 族进终端卡。否决了推倒重建——codex 对齐渲染区是受保护区，
   形式化收益在扩展性，不在重写。
4. **演示数据即用即删**：浏览器验证往本地 sqlite 插了两条 artifact
   验证摘要卡/面板/状态徽章后删除，不留假数据。

## 放弃了什么

- 聊天流内联 artifact 段（transcript 是 codex 受保护区，摘要卡放 composer
  上方是 M1 取舍；内联段列 M2）。
- 工作区自动弹出（artifact.created 到达时不自动开面板，手动开——避免
  打断阅读；自动弹出待真实使用反馈再定）。

## 防回归断言

- `artifact-visuals.test.ts`：mime 归类边界（含 tab-separated-values 陷阱）、
  basename、formatBytes 九例。

## 验证

pnpm tsc --noEmit / 330 Vitest 全绿 / lint 0 errors；浏览器实测：摘要卡
（2 成果 + 打开工作区）、面板（草稿黄/已接受绿徽章）、页头开关、聊天区
自适应压缩全部生效。
