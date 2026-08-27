---
version: 1.0.0
name: agent-team-workbench-web
description: >
  水墨案牍式多智能体工作台：宣纸画布、墨色侧栏、朱砂单强调色与克制山水留白。
  Aceternity UI 提供结构和动效原语，Ink Design System 统一材质、层级与交互。
  信息密集但扫描清晰，界面语言为简体中文；生产对话页另有 scoped LanguageGUI light/dark 阅读皮肤。
colors:
  brand:
    primary: "var(--color-brand-primary)"      # 朱砂，唯一装饰强调色
    accent: "var(--color-brand-accent)"        # 深朱砂，仅用于 primary 悬停/按压
    muted: "var(--color-brand-muted)"          # 淡朱砂水痕，选中态背景
  surface:
    base: "var(--color-surface-base)"          # 宣纸画布，叠加真实纸纤维资产
    warm: "var(--color-surface-warm)"          # 新纸表面，用于表单与安静区块
    sunken: "var(--color-surface-sunken)"      # 旧纸凹面，用于代码、表头、看板列
    raised: "var(--color-surface-raised)"      # 熟宣卡片与浮层
    glass: "var(--color-surface-glass)"        # 半透纸面，仅用于顶栏/浮层
  sidebar:
    base: "var(--color-sidebar)"               # 松烟墨侧栏，非纯黑
    hover: "var(--color-sidebar-hover)"        # 湿墨悬停面
    border: "var(--color-sidebar-border)"      # 墨层分界
  text:
    primary: "var(--color-text-primary)"       # hsl(222 24% 12%)，正文与标题
    secondary: "var(--color-text-secondary)"   # hsl(215 14% 38%)，次要信息
    tertiary: "var(--color-text-tertiary)"     # hsl(60 4% 41%)，辅助/时间戳（= text-muted）；宣纸底 AA 对比度下限，不得再调浅
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
    family: "STKaiti / Kaiti SC / KaiTi + font-zh 回退"
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
    display: "FZKai-Z03, STKaiti, Kaiti SC, KaiTi, serif"
    body: "chironHeiHK, PingFang SC, Hiragino Sans GB, Microsoft YaHei, sans-serif"
    zh: "chironHeiHK, PingFang SC, Hiragino Sans GB, Microsoft YaHei, sans-serif"   # 全局默认正文字体
    mono: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"
spacing:
  micro: "4px"; tight: "8px"; snug: "12px"; base: "16px"
  comfortable: "24px"; stack-sm: "24px"; stack-md: "32px"; loose: "32px"
  section: "64px"; stack-lg: "64px"; section-y: "96px"; macro: "128px"
rounded:
  control: "6px"     # 按钮、输入框、小徽章
  card: "8px"        # 卡片、浮层；纸张不使用肥厚大圆角
  container: "12px"  # 大容器、审批卡、dock 面板
  code-panel: "16px" # LeAgent 正文 fenced code 专用外壳
  pill: "9999px"     # 状态胶囊（rounded-full）
shadows:
  level-1: "墨褐色极淡投影 + 纸边 1px 高光"   # 卡片默认（= shadow-card）
  level-2: "同一光源下略深墨影"               # 交互卡悬停
  level-3: "浮层墨影，不使用纯黑阴影"         # 浮层/模态
  level-4: "屏风/抽屉最深层级"                # 仅顶层抽屉/模态
components:
  button:
    base: "inline-flex items-center justify-center gap-2 rounded-control font-medium transition-all duration-150; focus-visible ring-2 {colors.brand.primary}/40 offset-2; disabled opacity-50 cursor-not-allowed"
    primary: "bg {colors.brand.primary}; text {colors.text.inverse}; hover {colors.brand.accent}; active scale 0.98"
    secondary: "border {colors.border.strong}; bg {colors.surface.raised}; text {colors.text.secondary}; hover bg {colors.surface.base} + text {colors.text.primary}; active scale 0.98"
    ghost: "border {colors.brand.primary}/30; text {colors.brand.primary}; hover bg {colors.brand.primary}/5; active scale 0.98"
    success: "bg {colors.status.success}; text {colors.text.inverse}; hover {colors.status.success}/90; active scale 0.98 — 批准语义"
    danger: "bg {colors.status.error}; text {colors.text.inverse}; hover {colors.status.error}/90; active scale 0.98 — 破坏语义"
    danger-outline: "border {colors.status.error}/35; bg {colors.surface.base}; text {colors.status.error}; hover {colors.status.error}/5; active scale 0.98 — 破坏轮廓"
    warning-outline: "border {colors.status.warning}/40; bg {colors.surface.base}; text {colors.status.warning}; hover {colors.status.warning}/5; active scale 0.98 — 风险确认轮廓"
    sizes: "sm = padding {spacing.snug} × {spacing.micro}; md = padding {spacing.base} × {spacing.tight}; 字号固定 {typography.body}，不随尺寸变"
    status-usage: "状态档（success/danger/danger-outline/warning-outline）只允许出现在审批/破坏性语境，禁止当普通强调色用"
  card:
    base: "bg {colors.surface.raised}; rounded {rounded.card}; 墨线边框或纸面层级二选一；shadow {shadows.level-1}"
    padded: "card + padding {spacing.comfortable}"
    interactive: "card + cursor-pointer; hover -translate-y-0.5 + shadow {shadows.level-2} + border {colors.border.strong}; duration-200 ease-out"
  input:
    base: "rounded {rounded.control}; border 1px {colors.border.strong}; bg {colors.surface.raised}; padding {spacing.snug} × {spacing.tight}; text {typography.body}; focus border {colors.brand.primary}/40 + ring-2 {colors.brand.primary}/20"
  select:
    base: "与 input 同系 + appearance-none 去 OS 默认箭头；自定义 chevron（{colors.text.tertiary} 16px 右 12px 垂直居中，pointer-events-none）；padding-right 32px 让位箭头；新代码禁止裸用原生 select"
  validation:
    invalid-control: "input/select 错误态 = border 1px {colors.status.error}/60 + focus border {colors.status.error} + ring-2 {colors.status.error}/20；控件带 aria-invalid；由控件 invalid 属性承担，不手写类"
    error-text: "FieldError：role=alert；text {typography.caption} {colors.status.error}；顶替 hint 位置（不并存）"
    copy: "主动语态、说清事实与怎么改、不用感叹号（如「Base URL 需以 http:// 或 https:// 开头」）"
    success-state: "不做——没有错误即正常，不额外给绿色对勾"
  toast:
    base: "右下固定栈（w-320 gap-2）；rounded {rounded.card}; border 1px {colors.border.subtle}; bg {colors.surface.raised}; shadow {shadows.level-3}；图标取状态色（error {colors.status.error} / success {colors.status.success} / info {colors.brand.primary}）"
    behavior: "自动消失（error 6s、其余 3.5s）+ 显式关闭；最多同屏 5 条；只承载通知，破坏性操作一律走模态确认（见 Don'ts），不设 undo/action 槽（无消费场景）"
    a11y-motion: "容器 role=region aria-label=通知；error 卡 role=alert、其余 role=status；进出场 150ms easeOut + layout 平滑堆叠，reduced-motion 归零"
  drawer:
    base: "屏风式右侧滑入（spring 250/25）；rounded-l-container；宣纸面 + 墨影；portal 到 body；Escape 与遮罩点击关闭"
    titled: "表单容器形态：头行 h3 + 关闭钮（同 modal 头行规格）+ 可滚动 body；创建/编辑表单用它，宽度 480 起"
    untitled: "自由内容形态：绝对定位关闭钮，内容 pr-8 避让（任务详情沿用）"
    scope: "破坏性确认（删除/危险操作）一律 Modal，不进 drawer（见 Don'ts）"
  status-pill:
    base: "inline-flex gap-2 rounded {rounded.pill}; border 1px {colors.border.subtle}; bg {colors.surface.raised}; padding 12px × 4px; text {typography.caption} {colors.text.secondary}"
  user-card:
    base: "LanguageGUI 对话用户卡；右对齐白色紧凑卡；rounded 20px; bg {colors.surface.raised}; border 1px {colors.border.subtle}; padding 12px 16px；正文 15px/1.6；不显示重复角色头"
  turn-header:
    base: "仅供需要身份语义的非对话场景使用；生产 /chat 不显示用户或 assistant 重复角色头，身份由消息布局与可访问名称承载"
  prompt-chip:
    base: "rounded {rounded.pill}; border 1px {colors.border.subtle}; bg {colors.surface.raised}; text {typography.caption} {colors.text.secondary}; hover border {colors.brand.primary}/35 + text {colors.brand.primary}"
  session-dot:
    base: "会话状态点 6px 圆：执行中 {colors.brand.accent} + pulse；成功 {colors.status.success}；失败 {colors.status.error}；待审批 {colors.status.warning}；其余 {colors.status.standby}"
  skeleton:
    base: "animate-pulse 底 {colors.surface.sunken}；**300ms 后才显现**（快响应不闪烁；启动壳例外 delayMs=0，空白启动屏更糟）；reduced-motion 降静态色块；形状必须匹配目标布局（看板列/列表行/总览卡群/启动壳），禁用通用 spinner"
  artifact-shelf:
    base: "聊天区成果摘要卡：rounded {rounded.control}; border 1px {colors.border.subtle}; bg {colors.surface.raised}；头行「已生成 N 个成果」+ 品牌色「打开工作区」入口；内容行 = mime 图标 + 文件名 + 字节数；超 4 项折叠"
  workspace-panel:
    base: "右侧工作区 320px：border-left 1px {colors.border.subtle}; bg {colors.surface.raised}；成果行 = 图标 + 文件名 + 字节·时间·状态（草稿 {colors.status.warning} / 已接受 {colors.status.success}）；空态走 EmptyState。只承载元数据清单，不做内容预览（后端无内容端点，见 Known Gaps）"
  sidebar-nav:
    item: "text {colors.text.on-sidebar}; hover bg {colors.sidebar.hover}"
    item-active: "text {colors.text.on-sidebar-active}; bg {colors.sidebar.hover}"
  layout:
    page-shell: "layout-safe（max 1440px，两侧留白 48/40/32px 按断点）; space-y {spacing.stack-md}; padding-y {spacing.comfortable}"
    chat-page: "ChatPage 根挂 LanguageGUI scoped skin；浅蓝阅读画布 + 白色内容面 + 蓝色强调；正文与 composer 共用约 920px 居中阅读轨，窄屏吃满可用宽度；旧 .tx-scope 仅作 fallback，不改其他页面"
    config-split: "220–256px 左栏 + 流式主区（配置工作台专用布局语言）"
  chat-composer:
    base: "约 920px 阅读轨底部白色浮层；外壳 rounded 22px；border 1px {colors.border.subtle}; shadow {shadows.level-2}; textarea rounded 13px 并具 focus-visible brand ring；工具、队列、用量、停止、发送、dock、artifact 行为由真实 ChatPage 状态驱动"
    prompt-box: "expanded PromptBox：多行输入 + attachment/image/voice/Library&Apps 工具行 + 队列/用量/stop/send；拖放态用 brand ring；pending 附件为本地预览，后端无协议时明确阻止发送，不伪造上传成功"
  chat-workflow:
    base: "LanguageGUI 工作流只读投影；同一 rounded {rounded.container} 容器内依次展示目标摘要、方案草稿与 Action 步骤链；步骤卡 rounded {rounded.card}; 连接线用 {colors.border.subtle}; 状态同时用图标和文字表达"
    behavior: "目标正文始终可扫读；方案草稿可折叠；Action 顺序来自真实 run.plan_updated；无真实 mutation 契约时不展示设置、删除、新增等伪编辑控件"
  chat-tool-activity:
    base: "生产工具调用唯一入口 ActivityGroup：浅蓝/白色 LanguageGUI 表面、28px 紧凑 chip 轨、细语义边框、品牌蓝互动态；bash/code/read/search/write/edit/mcp/other 详情体挂在同一组内"
    behavior: "组与 chip 只由同一 run 的真实工具事件聚合；状态同时显示图标和文字，状态色只表达真实 pending/running/success/error/stopped；点击后单选展开详情，长输入/输出在详情体内渐进披露"
    demo-boundary: "Demo 只能复用生产 ActivityGroup/ToolRow/详情渲染器与可审计 fixture，不得从模型正文、Markdown 或静态 content block 伪造工具调用；无事件时显示空态"
  content-block:
    base: "LanguageGUI 结构化正文块；统一 rounded {rounded.container}、border {colors.border.subtle}、bg {colors.surface.raised} 与 {shadows.level-1}；metric/table/chart/file/event/image/audio/map/search/review-summary 共享标题、说明与来源栏"
    behavior: "只消费 languagegui/v1 白名单字段；review-summary 用固定 verdict/severity/check 状态同时表达结论、问题与验证证据；图表颜色由语义序列决定；安全 URL 才产生链接；无效 fenced JSON 回落 CodeBlock，不吞原文"
  code-panel:
    base: "LanguageGUI 正文代码面板；rounded {rounded.code-panel}、border {colors.border.subtle}、bg {colors.surface.raised}；工具栏展示受限文件名与语言，代码区固定行号列、13px/1.6 mono 与横向滚动"
    behavior: "支持 fence meta filename/title 与 {3,5-7}/highlight；复制只包含源码，下载只使用安全 basename；导出菜单只提供真实的下载和复制 Markdown 操作"
  aceternity:
    architecture: "Aceternity primitives → Ink wrappers → Workbench components → Pages"
    allowed: "Sidebar / Bento Grid / Text Generate Effect（非正文）/ hover-layout motion"
    banned-defaults: "霓虹、科技光束、强 3D、无限滚动与默认深色皮肤不得直接进入页面"
  motion:
    fast: "140ms"; normal: "220ms"; slow: "360ms"; atmospheric: "900ms"
    ratio: "80% 静态 / 15% UI motion / 5% 氛围动画"
    transcript-intensity: "LeAgent 正文 3/10：完成态静态；流式 100ms 节流重排 + 末尾 caret；工具状态只做必要旋转/展开，禁止按 token 重播"
    ae-boundary: "AE/Lottie/WebM 只用于印章、墨迹、山雾等低频资产；实时交互由 Motion/CSS 驱动"
---

# DESIGN.md — agent-team-workbench 前端设计事实源

## Overview

这是一个**多智能体团队的控制平面工作台**。产品仍是高密度监控与执行工具，视觉改为“水墨案牍”：像在一张可操作的宣纸卷宗上调度 Agent、任务与运行记录。设计语言的核心张力：

- **宣纸画布 + 松烟墨侧栏 + 单一朱砂强调**。纸纤维与山水为真实位图资产，只承载材质与气韵，不承载信息。
- **强调色只有朱砂一种**。它出现在主按钮、激活印记、链接和关键选中态；状态色仍只表达真实状态。
- **层次靠留白、墨线、纸面明度与同一方向的墨影**，避免每块内容都套“白卡 + 黑影”。
- **楷体只承担标题与印记**，正文继续使用可读的中文黑体栈；数字统一 `tabular-nums` 或等宽字体。
- **Aceternity UI 是交互底座，不是视觉成品**。所有 Aceternity 原语必须经过 Ink 层水墨化后进入页面。

与 AGENTS.md 的分工：AGENTS.md 管"怎么构建"，本文件管"应该长什么样"。本文件与代码的真相源关系：**颜色/字号/间距的生效值以 `src/index.css` `:root` 与 `tailwind.config.js` 为准**；本 frontmatter 只引用、不重造。发现漂移时以代码为准修正本文件。

## Colors

四组语义色（详见 frontmatter `colors:`）：

1. **Brand**：`primary` 是朱砂唯一强调色；`accent` 只做悬停/按压加深；`muted` 做淡朱砂选中底。
2. **Surface**：四种纸面：`base`（宣纸画布）→ `raised`（熟宣内容）→ `sunken`（旧纸分组）→ `sidebar`（松烟墨框架）。`warm` 用于安静表单区，不是第二主题。
3. **Text**：三级递减（primary → secondary → tertiary）+ `inverse`（品牌底上）+ 侧边栏两级。
4. **Status**：success / warning / error / info / standby。状态色**只用于表达状态**（运行态、校验、告警），不做装饰。`info` 与 `brand.primary` 同值是刻意的——"信息"与"品牌"在语义上同源。
5. **Identity**：`identity-1..8` 身份色阶，**只用于头像/归属标记**，不进入强调色与状态色语义（不参与按钮/边框/状态表达）。值冻结为原 avatar 调色板的 token 化版本，新增 hue 需先改本文件。

LanguageGUI 对话皮肤是局部视觉映射：只在 ChatPage 根将对话的 brand emphasis 映射为 LanguageGUI 蓝色，并通过 `data-theme=light|dark` 切换 scoped surface/text/border token；不修改全局朱砂 token，也不把该映射带到其他页面。状态色仍按真实运行语义表达。

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

- 标题族（display/h1–h3）用 `font-display` 楷体栈；正文一律落 `font-zh` 可读中文黑体。楷体是用户明确指定的传统水墨/卷宗语境，不进入长段正文。
- 强调用字号升级，不用字重堆叠（正文不加粗到 700 来强调，升到 body-lg 或 h3）。
- 小于 12px 的文字禁止出现（例外：diff/mono 场景与 diff 增删计数徽章，最小 11px）。

## Layout

- **8px 网格**：一切间距是 4 的倍数；语义刻度见 frontmatter `spacing:`（micro 4 → macro 128）。
- **页面容器**：`.page-shell` = `layout-safe` 限宽（最大 1440px，两侧留白按断点 48/40/32px）+ 纵向 `stack-md`（32px）节奏。
- **配置工作台**（智能体/模型页）：左栏 220–256px + 流式主区，主区内容再限 `max-w-6xl`。
- **对话页**：生产 `/chat` 在 ChatPage 根挂更具体的 LanguageGUI scoped skin：浅蓝画布、白色内容面与蓝色强调；正文和底部 composer 共用约 920px 居中阅读轨，窄屏吃满可用空间。旧 `.tx-scope` 保留为 fallback，不影响其他页面；SSE 连接态仍在对话页头，非对话页继续走宣纸壳。本次皮肤迁移不新增 `<1024px` 或 mobile 断点承诺。
- **对话工作流**：Goal、Plan 模式方案正文与本轮执行步骤只占一个工作流区域。Goal 是 Workbench 自有语义；Action 卡只读展示真实步骤状态，不复制 LanguageGUI multi-prompt 的编辑器按钮。

## Elevation & Depth

四级阴影阶梯（frontmatter `shadows:`），日常只用前两级：

- 卡片默认 `level-1`；交互卡悬停升到 `level-2`；浮层/模态 `level-3`；顶层抽屉/模态 `level-4`。所有阴影使用墨褐色，不使用纯黑。
- 不要给已有边框的卡片叠加重阴影；不要发明第五级阴影。

## Shapes

| 场景 | 圆角 |
|---|---|
| 按钮、输入框、小控件 | 6px（control） |
| 卡片、弹层内容 | 8px（card） |
| 大容器（审批卡、dock 面板） | 12px（container） |
| fenced code 面板 | 16px（code-panel） |
| 状态胶囊、徽章 | pill |

## Components

组件的**唯一入口是 `src/components/ui/`**（React 组件，变体为 props）。以下类名是历史遗留或布局语言：

- `.btn-*` / `.ui-card*` / `.input-field` / `.status-pill`：迁移中的遗留组件类。**新代码禁止使用**，用 `ui/` 组件替代；引用归零后删除。
- `.page-shell` / `.page-header` / `.config-*`：**布局语言**，保留使用。
- `.chat-*`：**对话渲染子系统**，渲染清单与逐件样式规格以 `../../agent-team-workbench-docs/chat-rendering-spec.md` 为准（当前基线为 LeAgent `1f16badc`），本文件不重复定义；改它之前先读规格文档。

`ui/` 组件的变体契约见 frontmatter `components:`；新增变体必须同时更新 frontmatter（变体作为独立条目）。

## Do's and Don'ts

**Do's**

- Do：颜色一律用语义名（Tailwind `brand-primary`/`surface-base`/… 或 `hsl(var(--color-*))`）。
- Do：交互元素实现四态——默认 / 悬停 / 按压（或激活）/ 禁用，外加 `focus-visible` ring。
- Do：按钮标签动词开头（"创建任务"、"保存配置"）；每个区域最多一个 `primary`。
- Do：空态说清"为什么空 + 下一步做什么"，并给一个动作入口。
- Do：加载超过 300ms 才显示骨架屏/加载态，避免闪烁。
- Do：Aceternity 组件先经 `components/ink/` 封装，再给业务页面消费。
- Do：氛围动画只使用 transform/opacity，并在 reduced-motion 下变成静态终态。
- Do：正文采用 LeAgent 的静态阅读节奏；流式 Markdown 约 100ms 节流解析并只保留 caret，KaTeX、Mermaid 与代码高亮在落定后处理。

**Don'ts**

- Don't：在 TS/TSX 中内联十六进制、`rgb()`、`hsl()` 色值（有测试门禁拦截；唯一豁免 `chat/blocks/ansi.ts` 的终端色协议映射）。
- Don't：引入第二装饰强调色，或把 `status-*` 当装饰色用。
- Don't：给卡片发明新的阴影/圆角组合；用 frontmatter 里已有的阶梯。
- Don't：把楷体用于长段正文；给标题加字重来强调（优先用字号与留白）。
- Don't：模态套模态；破坏性操作用模态 + 显式确认而不是 toast。
- Don't：在组件里新增散落的 `dark:` 变体；Chat 暗色只能通过 `.chat-languagegui-skin[data-theme]` 的语义 token 映射实现，且不得扩散到其他页面。
- Don't：为没有上传/音频/App 协议的输入控件伪造成功态，或静默丢弃用户选择的附件。
- Don't：直接复制 Aceternity 的霓虹、光束、黑底和强 3D 默认样式。
- Don't：在滚动容器上叠实时噪声滤镜；纸纹只用静态压缩位图。
- Don't：把 Tracing Beam、逐标签显影或标题打字动画叠回正文；角色标签、工具 chip 与正文必须保持同一套 LeAgent 视觉语法。
- Don't：在生产 ActivityGroup 之外新增第二套工具调用展示，也不要把工具调用伪装成 assistant 正文或 LanguageGUI ContentBlock；工具状态色不得作为装饰色使用。

## Responsive Behavior

**Desktop-first（≥1024px 为支持目标）**。`page-shell` 页面（总览/任务等）在 640/1024 断点有真实收折：留白 48/40/32px、网格降列、`page-header` 纵排、≥1440px 限宽封顶。**对话页与配置工作台的分栏（shell 220px + 会话/配置列 + 工作区）不收折，<1024px 不支持**（见 Known Gaps 第 7 条）——不要为这些分栏写移动端样式，本文档也不再承诺其断点行为。

桌面控件高度 ≥32px；44×44 触控目标仅在移动端立项后适用。对话页代码块与表格横向滚动、不折行。

## Accessibility

- **skip-to-content**：shell 首个可聚焦元素是跳转链接（`sr-only`，聚焦时以卡片样式显现在左上），目标 `#main-content`（`<main>`，`tabindex=-1`）。
- **未知路由 = 404 页**（`not-found.page`）：不静默重定向；一个 primary 返回入口，文案主动语态、无感叹号。
- **校验错误**：控件 `aria-invalid` + 错误文案 `role=alert`（见 frontmatter `validation`）。

## Iteration Guide

1. 一次只改一个组件/一个页面；改动引用其 frontmatter token key。
2. 处处使用语义 token——**never inline hex**。
3. 只描述默认态与按压/激活态；悬停态按既有组件编码，不额外发明。
4. 单一朱砂 + 墨/纸中性色 + 语义状态色是三位一体；不要引入第四类色彩角色。
5. 拿不准强调方式时：先升字号，再加字重，最后才考虑颜色。
6. 改完必跑：`pnpm tsc --noEmit && pnpm test && pnpm lint`；视觉改动需在浏览器复核。

## Known Gaps

以下范围**未定义**，不要臆造；需要时先在 `notes/` 立项：

1. **AE 动效资产**：当前只冻结目录与消费边界；真实 Lottie/WebM 需有可审计源资产后逐件接入。
2. **空态插画体系**：可用同一水墨资产族，但不得用不相关插画填空。
3. **对话渲染细节**：归 `agent-team-workbench-docs/chat-rendering-spec.md` 管，不在本文件范围。
4. **水墨素材扩展**：新增素材必须与宣纸/山水同一笔触体系并做体积预算，禁止随页随机生成。
5. **成果内容预览**：后端只暴露 artifact 元数据（无内容端点），工作区只做清单；预览器等后端补内容面后再立项。
6. **移动端**：对话/配置分栏不收折，<1024px 布局不成立；移动适配未立项前不承诺断点行为。
