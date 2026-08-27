# tx：壳与暗面最终适配——收起宣纸顶栏，main 让给阅读面

Status: implemented

## 后继状态（2026-08-27）

壳层收起、ChatPage 独占内容槽与 `.tx-scope` fallback 均继续有效；生产可见面已由更具体的 LanguageGUI 浅色 skin 接管。正文/Composer 的新几何与运行架构边界见 [LanguageGUI 视觉迁入生产 Chat](2026-08-27-languagegui-production-chat-skin.md)。

决策依据：用户 2026-08-27「继续打磨对话页：壳和正文暗面的最终适配、布局和阈读」。
前序 `2026-08-26-tx-transcript-standalone-skin.md` 把最终适配定义为「处理壳与面的边界」；
本刀落地该边界。任务分支从 `zcode/chat-transcript-redesign` 分出。

## 决策与理由

- **断点在宣纸顶栏，不在侧栏。** 松烟墨侧栏已经是暗框架；真正切过 `.tx-scope` 的是
  layout-shell 的宣纸玻璃顶栏（`bg-surface-glass/90 backdrop-blur`）加上 `<main>` 的
  `mesh-bg` / `InkBackdrop`。对话页头还叠了一层 `bg-surface-raised/60 backdrop-blur`，
  纸纹从半透明铬件渗进暗面。适配 = 让 `/chat` 的内容槽变成同一块墨底，与侧栏对缝。
- **挂载点：`<main>` 在 `/chat` 上加 `.tx-scope`。** 页根仍保留该类（作品所有权），
  正文滚动区上的第二处挂载删掉（布局刀遗留）。`<main>` 挂载覆盖 Suspense 骨架与
  槽内 Toaster，避免加载瞬间露宣纸。品牌朱砂不进 `.tx-scope`：侧栏仍在作用域外，
  「策」印与导航激活条保持朱砂。
- **宣纸顶栏在 `/chat` 收起，不改成暗顶栏。** 暗顶栏仍是第二条铬件（壳 56px + 页头 48px），
  阈读要的是把垂直空间还给正文。工作区名由侧栏「案牍工作台」承担；SSE 胶囊下放到
  对话页头（空选 Agent 时同样有一条 48px 铬件，避免连接态丢失）。
- **页头去玻璃。** `backdrop-blur` + 半透明 raised 是水墨顶栏语法，不是暗阅读面语法。
  改不透明 `surface-base` + 底线，与 transcript / composer 同一材质。
- **阈读只动对比与叠距，不回 72ch。** 用户已定调「填充满」。tx 的 `text-tertiary`
  从 47% 抬到 56%（暗底 caption 过近 AA 下限）；回合 `py-3` 与 `.chat-thread space-y-3`
  叠距去掉一层。阅读槽与 composer 用顶部分割线分区，不靠第二套背景。
- **启动骨架跟路由走。** 硬刷新 `/chat` 时 `AppShellSkeleton` 原先画宣纸顶栏，
  会在 bootstrap 完成前闪一帧纸面。`chat` 路由改墨侧栏 + `.tx-scope` 空槽，
  不进宣纸顶栏。

## 放弃了什么

- **把 layout-shell 顶栏包进 `.tx-scope`：** `--color-brand-primary` 在作用域内是行动绿，
  顶栏朱砂条会变绿，品牌 DNA 被正文强调色吞掉。
- **保留顶栏只换暗色 token（不用 tx-scope）：** 纸面疤没了，但双顶栏还在，阈读不成立。
- **正文列重新钳 `72ch`：** 与 2026-08-27「填充满」定调相反。
- **给 `.tx-scope` 另起 `tx-*` 类族 / 写全局 `dark:`：** 架构不变；DESIGN.md 单浅色主题 +
  作用域倒置仍然有效。
- **改非对话页：** run-panel 审批面仍走水墨，不进本作用域。
- **布局刀回退：** Agent chip 排、任务列表、一体化 composer、dock 默认折叠、成果架单行
  全部保留；本刀只处理壳/面边界与阅读舒适，不重排分栏。

## 负向保证

- `/chat` 以外：顶栏、`mesh-bg`、`InkBackdrop` 行为为零变化。
- 侧栏不进 `.tx-scope`，朱砂身份色不被行动绿覆盖。
- 不发明 `ApprovalRequest` 字段；不写移动端断点（Known Gaps #7）。
