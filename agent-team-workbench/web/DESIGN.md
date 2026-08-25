---
version: 1.0.0
name: agent-team-workbench-web
description: >
  冷静的蓝调指挥舱工作台：冷灰白画布、深蓝侧边栏、单一品牌蓝强调色、
  语义化状态色。信息密集但层级清晰，克制的阴影与边框承担层次，
  界面语言为简体中文。设计意图：让"多智能体团队的运行状态"一眼可读。
colors:
  brand:
    primary: "var(--color-brand-primary)"      # hsl(199 89% 48%) = #0EA5E9，唯一强调色
    accent: "var(--color-brand-accent)"        # hsl(199 95% 42%)，仅用于 primary 悬停/按压加深
    muted: "var(--color-brand-muted)"          # hsl(199 76% 94%)，品牌色浅底（选中态背景）
  surface:
    base: "var(--color-surface-base)"          # hsl(214 32% 97%)，页面画布（冷灰白，非纯白）
    warm: "var(--color-surface-warm)"          # hsl(40 20% 98%)，暖调表面（保留锚点，当前基本未用）
    sunken: "var(--color-surface-sunken)"      # hsl(214 22% 93%)，凹陷面（代码底/表头底/分组底）
    raised: "var(--color-surface-raised)"      # 纯白，卡片与浮层
    glass: "var(--color-surface-glass)"        # 纯白，配合透明度的毛玻璃面
  sidebar:
    base: "var(--color-sidebar)"               # hsl(222 28% 11%)，深蓝近黑侧边栏
    hover: "var(--color-sidebar-hover)"        # hsl(222 22% 16%)
    border: "var(--color-sidebar-border)"      # hsl(222 18% 18%)
  text:
    primary: "var(--color-text-primary)"       # hsl(222 24% 12%)，正文与标题
    secondary: "var(--color-text-secondary)"   # hsl(215 14% 38%)，次要信息
    tertiary: "var(--color-text-tertiary)"     # hsl(215 10% 52%)，辅助/时间戳（= text-muted）
    inverse: "var(--color-text-inverse)"       # 白，品牌色底上的文字
    on-sidebar: "var(--color-text-on-sidebar)"            # hsl(215 16% 72%)
    on-sidebar-active: "var(--color-text-on-sidebar-active)"  # 白
  border:
    subtle: "var(--color-border-subtle)"       # hsl(214 20% 90%)，卡片描边/分隔线
    strong: "var(--color-border-strong)"       # hsl(214 18% 82%)，输入框描边/强调分隔
  status:
    success: "var(--color-status-success)"     # hsl(152 60% 40%)
    warning: "var(--color-status-warning)"     # hsl(38 92% 50%)
    error: "var(--color-status-error)"         # hsl(0 72% 55%)
    info: "var(--color-status-info)"           # hsl(199 89% 48%)，与 brand.primary 同值
    standby: "var(--color-status-standby)"     # hsl(215 10% 65%)，待机/未激活
typography:
  display:
    family: "Outfit + font-zh 回退"
    size: "42px"; lineHeight: "1.05"; weight: "700"; letterSpacing: "-0.02em"
  h1:
    size: "29px"; lineHeight: "1.15"; weight: "700"; letterSpacing: "-0.015em"
  h2:
    size: "24px"; lineHeight: "1.25"; weight: "600"; letterSpacing: "-0.01em"
  h3:
    size: "20px"; lineHeight: "1.3"; weight: "600"; letterSpacing: "-0.005em"
  body-lg:
    size: "17px"; lineHeight: "1.5"; weight: "400"
  body:
    size: "14px"; lineHeight: "1.55"; weight: "400"
  caption:
    size: "12px"; lineHeight: "1.4"; weight: "400"; letterSpacing: "0.01em"
  font-stacks:
    display: "Outfit, ui-sans-serif, system-ui, sans-serif"
    body: "Inter, ui-sans-serif, system-ui, sans-serif"
    zh: "chironHeiHK, PingFang SC, Hiragino Sans GB, Microsoft YaHei, sans-serif"   # 全局默认正文字体
    mono: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"
spacing:
  micro: "4px"; tight: "8px"; snug: "12px"; base: "16px"
  comfortable: "24px"; stack-sm: "24px"; stack-md: "32px"; loose: "32px"
  section: "64px"; stack-lg: "64px"; section-y: "96px"; macro: "128px"
rounded:
  control: "8px"     # 按钮、输入框、小徽章（--rounded-button / --rounded-input / rounded-sm|md）
  card: "12px"       # 卡片、浮层（rounded-card / rounded-lg）
  container: "16px"  # 大容器（审批卡、dock 面板，rounded-xl）
  pill: "9999px"     # 状态胶囊（rounded-full）
shadows:
  level-1: "0 1px 3px rgba(0,0,0,0.06), 0 4px 12px rgba(0,0,0,0.04)"   # 卡片默认（= shadow-card）
  level-2: "0 4px 12px rgba(0,0,0,0.10), 0 8px 24px rgba(0,0,0,0.06)"  # 交互卡悬停
  level-3: "0 10px 30px rgba(0,0,0,0.15)"                               # 浮层/模态
  level-4: "0 20px 40px rgba(0,0,0,0.2)"                                # 仅顶层抽屉/模态
components:
  button:
    base: "inline-flex items-center justify-center gap-2 rounded-control font-medium transition-all duration-150; focus-visible ring-2 {colors.brand.primary}/40 offset-2; disabled opacity-50 cursor-not-allowed"
    primary: "bg {colors.brand.primary}; text {colors.text.inverse}; hover {colors.brand.accent}; active scale 0.98"
    secondary: "border {colors.border.strong}; bg {colors.surface.raised}; text {colors.text.secondary}; hover bg {colors.surface.base} + text {colors.text.primary}; active scale 0.98"
    ghost: "border {colors.brand.primary}/30; text {colors.brand.primary}; hover bg {colors.brand.primary}/5; active scale 0.98"
    sizes: "sm = padding {spacing.snug} × {spacing.micro}; md = padding {spacing.base} × {spacing.tight}; 字号固定 {typography.body}，不随尺寸变"
  card:
    base: "bg {colors.surface.raised}; rounded {rounded.card}; border 1px {colors.border.subtle}; shadow {shadows.level-1}"
    padded: "card + padding {spacing.comfortable}"
    interactive: "card + cursor-pointer; hover -translate-y-0.5 + shadow {shadows.level-2} + border {colors.border.strong}; duration-200 ease-out"
  input:
    base: "rounded {rounded.control}; border 1px {colors.border.strong}; bg {colors.surface.raised}; padding {spacing.snug} × {spacing.tight}; text {typography.body}; focus border {colors.brand.primary}/40 + ring-2 {colors.brand.primary}/20"
  status-pill:
    base: "inline-flex gap-2 rounded {rounded.pill}; border 1px {colors.border.subtle}; bg {colors.surface.raised}; padding 12px × 4px; text {typography.caption} {colors.text.secondary}"
  user-bubble:
    base: "右对齐胶囊气泡（rounded {rounded.pill} 级）；bg {colors.brand.primary}/7%；border 1px {colors.brand.primary}/20%；text {typography.body} {colors.text.primary}——用户侧唯一允许的品牌浅底用法"
  turn-header:
    base: "助手回合头：20px 头像 + 名字 {typography.caption} 600 {colors.text.secondary} + 时间 tabular-nums {colors.text.tertiary}"
  prompt-chip:
    base: "rounded {rounded.pill}; border 1px {colors.border.subtle}; bg {colors.surface.raised}; text {typography.caption} {colors.text.secondary}; hover border {colors.brand.primary}/35 + text {colors.brand.primary}"
  session-dot:
    base: "会话状态点 6px 圆：执行中 {colors.brand.accent} + pulse；成功 {colors.status.success}；失败 {colors.status.error}；待审批 {colors.status.warning}；其余 {colors.status.standby}"
  sidebar-nav:
    item: "text {colors.text.on-sidebar}; hover bg {colors.sidebar.hover}"
    item-active: "text {colors.text.on-sidebar-active}; bg {colors.sidebar.hover}"
  layout:
    page-shell: "layout-safe（max 1440px，两侧留白 48/40/32px 按断点）; space-y {spacing.stack-md}; padding-y {spacing.comfortable}"
    config-split: "220–256px 左栏 + 流式主区（配置工作台专用布局语言）"
---

# DESIGN.md — agent-team-workbench 前端设计事实源

## Overview

这是一个**多智能体团队的控制平面工作台**：监控型仪表盘定位，"轻盈亲和指挥舱"视觉方向（见 `../../agent-team-workbench-docs/references/product-brief.md`）。设计语言的核心张力：

- **冷灰白画布 + 深蓝侧边栏 + 单一品牌蓝**。画布不是纯白（`surface.base` 带 32% 冷灰），侧边栏深蓝近黑——亮/暗两区形成稳定框架感。
- **强调色只有一种**（`brand.primary` #0EA5E9）。它出现在主按钮、激活指示、链接、品牌头像上，其余一切是中性色与语义状态色。
- **层次靠边框与克制的阴影**，不靠色块堆叠。卡片 = 白底 + 1px `border.subtle` + level-1 阴影，仅此而已。
- **界面语言为简体中文**，全局默认字体是 `font-zh`（chironHeiHK 栈）；Outfit/Inter 只承担展示与数字场景。

与 AGENTS.md 的分工：AGENTS.md 管"怎么构建"，本文件管"应该长什么样"。本文件与代码的真相源关系：**颜色/字号/间距的生效值以 `src/index.css` `:root` 与 `tailwind.config.js` 为准**；本 frontmatter 只引用、不重造。发现漂移时以代码为准修正本文件。

## Colors

四组语义色（详见 frontmatter `colors:`）：

1. **Brand**：`primary` 是唯一强调色；`accent` 只做 primary 的悬停加深，不独立使用；`muted` 做品牌浅底（选中/激活背景）。
2. **Surface**：四种表面——`base`（画布）→ `raised`（卡片）→ `sunken`（凹陷区分组）→ `sidebar`（深色框架）。`warm` 是预留暖调锚点，当前无消费场景，不要为它发明用法。
3. **Text**：三级递减（primary → secondary → tertiary）+ `inverse`（品牌底上）+ 侧边栏两级。
4. **Status**：success / warning / error / info / standby。状态色**只用于表达状态**（运行态、校验、告警），不做装饰。`info` 与 `brand.primary` 同值是刻意的——"信息"与"品牌"在语义上同源。

## Typography

| Token | 字号 | 字重 | 行高 | 字距 | 用途 |
|---|---|---|---|---|---|
| display | 42px | 700 | 1.05 | -0.02em | 仅首屏大标题 |
| h1 | 29px | 700 | 1.15 | -0.015em | 页面主标题 |
| h2 | 24px | 600 | 1.25 | -0.01em | 区块标题（`page-title`） |
| h3 | 20px | 600 | 1.3 | -0.005em | 卡片/分组标题 |
| body-lg | 17px | 400 | 1.5 | 0 | 强调正文 |
| body | 14px | 400 | 1.55 | 0 | 默认正文与控件文字 |
| caption | 12px | 400 | 1.4 | 0.01em | 辅助信息、时间戳、徽章 |

原则：

- 标题族（display/h1–h3）用 `font-display`（Outfit）+ `tracking-tight`；正文一律落 `font-zh` 中文栈——**不要给中文正文指定 Outfit/Inter**。
- 强调用字号升级，不用字重堆叠（正文不加粗到 700 来强调，升到 body-lg 或 h3）。
- 小于 12px 的文字禁止出现（表格/代码除外，最小 11px 且仅限 diff/mono 场景）。

## Layout

- **8px 网格**：一切间距是 4 的倍数；语义刻度见 frontmatter `spacing:`（micro 4 → macro 128）。
- **页面容器**：`.page-shell` = `layout-safe` 限宽（最大 1440px，两侧留白按断点 48/40/32px）+ 纵向 `stack-md`（32px）节奏。
- **配置工作台**（智能体/模型页）：左栏 220–256px + 流式主区，主区内容再限 `max-w-6xl`。
- **对话页**：正文区限宽 `min(56rem, 100%)` 居中（Codex 对齐）。

## Elevation & Depth

四级阴影阶梯（frontmatter `shadows:`），日常只用前两级：

- 卡片默认 `level-1`；交互卡悬停升到 `level-2`；浮层/模态 `level-3`；顶层抽屉/模态 `level-4`。
- 不要给已有边框的卡片叠加重阴影；不要发明第五级阴影。

## Shapes

| 场景 | 圆角 |
|---|---|
| 按钮、输入框、小控件 | 8px（control） |
| 卡片、弹层内容 | 12px（card） |
| 大容器（审批卡、dock 面板） | 16px（container） |
| 状态胶囊、徽章 | pill |

## Components

组件的**唯一入口是 `src/components/ui/`**（React 组件，变体为 props）。以下类名是历史遗留或布局语言：

- `.btn-*` / `.ui-card*` / `.input-field` / `.status-pill`：迁移中的遗留组件类。**新代码禁止使用**，用 `ui/` 组件替代；引用归零后删除。
- `.page-shell` / `.page-header` / `.config-*`：**布局语言**，保留使用。
- `.chat-*`：**对话渲染子系统**，规格以 `../../agent-team-workbench-docs/references/codex-desktop-markdown-tags-inventory.md` 为准，本文件不重复定义；改它之前先读该文档。

`ui/` 组件的变体契约见 frontmatter `components:`；新增变体必须同时更新 frontmatter（变体作为独立条目）。

## Do's and Don'ts

**Do's**

- Do：颜色一律用语义名（Tailwind `brand-primary`/`surface-base`/… 或 `hsl(var(--color-*))`）。
- Do：交互元素实现四态——默认 / 悬停 / 按压（或激活）/ 禁用，外加 `focus-visible` ring。
- Do：按钮标签动词开头（"创建任务"、"保存配置"）；每个区域最多一个 `primary`。
- Do：空态说清"为什么空 + 下一步做什么"，并给一个动作入口。
- Do：加载超过 300ms 才显示骨架屏/加载态，避免闪烁。

**Don'ts**

- Don't：在 TS/TSX 中内联十六进制、`rgb()`、`hsl()` 色值（有测试门禁拦截；唯一豁免 `chat/blocks/ansi.ts` 的终端色协议映射）。
- Don't：引入第二强调色，或把 `status-*` 当装饰色用。
- Don't：给卡片发明新的阴影/圆角组合；用 frontmatter 里已有的阶梯。
- Don't：给中文正文换字体；给标题加字重来强调（用字号升级）。
- Don't：模态套模态；破坏性操作用模态 + 显式确认而不是 toast。
- Don't：为暗色模式写任何样式（见 Known Gaps）。

## Responsive Behavior

| 断点 | 行为 |
|---|---|
| <640px | `layout-safe` 两侧留白 32px；`page-header` 纵排 |
| 640–1023px | 留白 40px；网格列数收缩 |
| ≥1024px | 留白 48px；配置工作台双栏完全展开 |
| ≥1440px | 内容限宽封顶（`--layout-safe-width`） |

触控目标最小 44×44px（移动端）；桌面控件高度 ≥32px。对话页代码块与表格横向滚动、不折行。

## Iteration Guide

1. 一次只改一个组件/一个页面；改动引用其 frontmatter token key。
2. 处处使用语义 token——**never inline hex**。
3. 只描述默认态与按压/激活态；悬停态按既有组件编码，不额外发明。
4. 单一品牌蓝 + 中性色 + 语义状态色是三位一体；不要引入第四类色彩角色。
5. 拿不准强调方式时：先升字号，再加字重，最后才考虑颜色。
6. 改完必跑：`pnpm tsc --noEmit && pnpm test && pnpm lint`；视觉改动需在浏览器复核。

## Known Gaps

以下范围**未定义**，不要臆造；需要时先在 `notes/` 立项：

1. **暗色模式**：只有单浅色主题；不要写 `dark:` 变体。
2. **动画时长体系**：对话子系统以外的动效时长未定义（现有 150/200ms 过渡是既成事实，不是体系）。
3. **表单校验态**：输入框错误/成功样式未定义（`ui/field` 的 error 槽位是占位约定，视觉规格待补）。
4. **空态插画体系**：空态只用图标 + 文案，不引入插画。
5. **对话渲染细节**：归 codex 逆向规格文档管，不在本文件范围。
6. **`surface.warm` 的用法**：预留锚点，无现网消费场景。
