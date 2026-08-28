# 对话渲染清单与样式规格

日期：2026-08-26
状态：implemented（LeAgent 展示骨架已落地并通过浏览器验收）
配套文档：
- `web/DESIGN.md` —— 全局 token 与组件契约（事实源）；
- `https://github.com/vixues/LeAgent/tree/1f16badc834abbd829d3cb7e9f8fcb5b2d57f443/frontend/src/components/chat` —— 消息骨架与 Markdown 交互基线（Apache-2.0）；
- `references/codex-desktop-markdown-tags-inventory.md` —— 历史 Markdown 格局来源，不再是当前视觉基线；
- `frontend-design-md-redesign.md` —— 重设计方案与路线。

**真相源约定**：本文是渲染清单与视觉规格；数值生效值以
`web/src/index.css`（`.chat-*` 区）、`web/src/components/chat/*.module.css`、
`web/tailwind.config.js` 为准。文档与代码漂移时以代码为准修本文。
几何来源：消息、正文与 Callout 保留 LeAgent 扫描节奏；代码面板和工具 Action 卡对齐 LanguageGUI，Terminal/Read/Search/Diff 内容体保留 DSH 几何。生产 `/chat` 的展示层采用已验收的 LanguageGUI 浅蓝/白/蓝强调 scoped skin；颜色全部改走本项目语义 token。真实 TranscriptSegment、Markdown、队列、usage、stop/send、SSE、审批、artifact 与 dock 行为不因换肤改变。

主题：生产 Chat 根使用 `data-theme=light|dark` 切换 LanguageGUI scoped 语义 token；默认 light，用户显式选择后本地记忆。组件禁止自行写 `dark:` 颜色分支。

---

## 1. 段层（TranscriptSegment，8 种 + meta-detail 为 meta 子变体）

| 段 | 触发 | 容器/表面 | 排版 | 动效 | 实现 |
|---|---|---|---|---|---|
| user | 用户消息 | `article` 内右对齐紧凑白卡 `.chat-user-card`；LanguageGUI 浅色面、20px 圆角、`surface-raised`、细 `border-subtle`、12×16px；不显示用户角色头 | 正文 15px/1.6，保留换行；可访问名称仍为「你的消息」 | 悬停操作条 opacity 0.36→100 | transcript-view.tsx |
| assistant | 助手文本 | `article` 内开放 `.chat-prose` Markdown 正文；不显示重复 assistant/模型角色头，不额外包白卡 | 正文 15px/1.75；身份不以重复可见头占位 | 流式：100ms Markdown 节流 + 2px caret；落定后静态 | assistant-turn.tsx |
| thinking | reasoning 事件；超帽时间线走 `reasoning_folded` 折叠锚点（runs.store capTimeline 把逐出推理聚合回 message.completed，尾部 4000 字符预算截断，截断带「早期推理已省略」前缀） | `.chat-reasoning-panel`：`rounded-lg`(12) 边 `border-subtle` 底 `surface-sunken/80` | 头 `caption`；体 `max-h-52` 纵滚 `px-3 py-2.5`（motion 展开落定后留 inline height:auto，固定高度会被覆盖，故帽用 max-h） | 流式扫光带 300px 2.6s ease-out | reasoning-activity-row.tsx |
| meta | error/system | 无容器，居中 | `caption`；错误 `status-error` 带 ✕ 前缀；时间戳 `tabular-nums` | — | transcript-view.tsx |
| meta-detail | meta 附详情（`msg.detail` 子变体，非独立段类型） | `rounded-md` 底 `surface-base` 等宽块 `max-h-48` | mono 11/16 | — | transcript-view.tsx MetaLine |
| activity | 同 run 连续工具行 | LanguageGUI `ActivityGroup`；组头汇总，Action 卡点击后在轨道下展开详情 | `Action N` + 工具徽章 + 摘要 + 耗时 + 状态文字 | 单选展开；运行态 Loader；横向滚动 | tool-card.tsx |
| thinking-placeholder | run 进行中无正文 | 无容器行 | 14px 扫光圆点 + `caption`「Thinking」+ shimmer 渐变文字 | 扫光 2.6s；shimmer 2s ease-in-out | thinking-placeholder.tsx |
| turn-diff | 回合聚合 diff | 同 DiffCard（见 §4） | — | — | turn-diff-card.tsx |
| approval | 审批请求 | `rounded-xl`(16) 边 `status-warning/25` 底 `surface-raised` `shadow-sm` | 见 §6 | 容器查询 <28rem 动作纵排 | approval-card.tsx |

段间距：`.chat-thread space-y-3`（12px）；正文与 composer 共用约 920px 居中阅读轨，窄屏吃满可用宽度。工具、审批、diff 仍在各自 segment 内部滚动，不搬 demo widget 协议。

---

## 2. Markdown 正文元素层（assistant 段内，LeAgent 基线）

| 元素 | 格局规格 | 备注 |
|---|---|---|
| 段落 | 15px/1.75；顶层相邻块间距 .85em | 首末块边距归零 |
| 标题 h1–h6 | 24/20/17.6/16/15/14px，600，行高 1.3，字距 -.011em；margin 1.5em/.5em | h1/h2 带 `border-subtle` 底线；标题 `text-wrap:balance` |
| 引用 | 左 3px brand/50 色条 + `surface-sunken/55` 软底；右侧 8px 圆角；8×16px | 文字降到 secondary，子块间距 .6em |
| 无序列表 | 1.5em 缩进；三级子弹 disc→circle→square，marker 用 tertiary | li 纵距 .2em；嵌套顶距 .25em |
| 有序列表 | decimal，其余同无序 | — |
| 任务清单 | 去子弹并左移 1.25em；checkbox 与首行中线对齐 | checked 整项降到 tertiary |
| 行内 code | `surface-sunken` + `border-subtle`，4px 圆角，1×5px，.875em | mono，不与 fenced code 混用 |
| 代码块 | → CodeBlock（§3） | `:not(pre)` 排除 |
| 表格 | → TableCard（§3） | 全宽 0.9em；表头凹面、uppercase；8×14px；偶数行凹面 35%，hover brand 5% |
| 链接 | `brand-primary` 常驻下划线（40%），hover 加深 | 新标签 + noreferrer noopener |
| 分割线 hr | `border-subtle` 1px；margin 1.6em | — |
| 强调族 | strong=650；del=tertiary；mark=brand/18 软底；kbd=凹面+边框+底边 2px | sub/sup=.75em；abbr dotted underline |
| Callout | `> [!TYPE]` 与 `:::type ... :::` | note/info/tip/success/warning/caution/danger/important；3px 语义色条、7% 软底、标题标记 |
| 数学 | `remark-math` + KaTeX | 流式期显示原文，落定后排版；display math 横向滚动 |
| Mermaid | `mermaid` fenced code | 动态加载、落定后渲染；失败回落源码而非丢内容 |
| 图片 | 标准 Markdown image | 最大宽度 100%、8px 圆角；无效 src 显示可读占位 |
| LanguageGUI ContentBlock | `languagegui` fenced JSON 或 canonical `content_blocks` | 统一走 `chat-content-blocks-v1.md`；落定后渲染 metric/table/chart/file/event/review-summary，流式期与解析失败回落源码 |
| details | 边框 + 凹面软底，summary 可聚焦/可展开 | 不开放危险 raw HTML；只消费解析器产生的安全节点 |
| 崩溃兜底 | MarkdownErrorBoundary 内联 fallback（`<pre>` mono 13 纯文本块，`resetKey` 复位） | 按 resetKey 清错误态重试 |

---

## 3. 代码 / 表格专用块

### CodeBlock（块级代码）
- 高亮：highlight.js `lib/common` 懒加载；fence 语言声明优先，未注册/自动检测 relevance≤0/加载失败 → 纯文本降级；流式期跳过，落定后 120ms 防抖处理。
- 语法：普通代码使用标准 `````lang```` fence，并支持 fence meta `filename=...`、`title=...`、行号高亮 `{3,5-7}` 与 `highlight=...`；`filename`/`title` 可作为展示 meta，下载时只取安全 basename，不能写入目录或路径。行号和高亮属于 chrome，不进入复制的代码正文。
- 外观：16px 圆角细边代码面板；44px toolbar；左侧文件名 + 11px mono 语言徽章；右侧「复制代码」与品牌色「导出」菜单（下载文件 / 复制 Markdown，clipboard→execCommand 双兜底，2s Check 反馈）；内容 13px/1.6 横向滚动，不软换行。
- 行内 code 与块级 code 外观分离（行内走 §2 pill）。

### TableCard（markdown 表格容器）
- `div.chat-table-wrap`：8px 圆角、`border-subtle` 细环、横向滚动；保留悬停复制 TSV（空单元格保留占位，1s 反馈），表格视觉按 §2 的 LeAgent 斑马表执行。

---

## 4. 工具块层（activity 组内）

### 公共件（LanguageGUI Action 卡 + 本地专用详情体）
| 件 | 规格 |
|---|---|
| ActivityGroup | 58px 汇总头；历史大组可折叠；横向 Action 卡轨，8px 间距、细滚动条 |
| ToolRow | 约 232×88px；`Action N` 与工具徽章在首行，双行摘要居中，耗时与图标+文字状态收尾；选中用品牌蓝边与 ring |
| 状态 | pending/running=Loader；success=Check；error=Alert；stopped=中断图标；图标之外有读屏文本，运行动画遵循 reduced-motion |
| 展开体 | 同一时间只展开一个 chip；复用 Terminal/Read/Search/Diff/IN-OUT 专用体，max-height 22rem 纵滚 |

**实现不变量**：`ActivityGroup` 是生产环境工具调用的唯一渲染入口。它只由
真实 transcript/run 事件聚合而成，工具数量、顺序、名称、参数摘要、耗时与终态都必须
来自同一个 run 的事件；不得根据助手正文、Markdown 或模型声称的“已调用工具”创建
工具卡。`tool-card.tsx` 负责组装组与 Action 卡，族渲染器只负责详情体，二者之间不允许再
出现 demo 专用的第二套工具协议。

历史回放仍受 500 帧预算约束，但裁剪必须先保留正文结构锚点与完整工具 bundle：
已结束调用保留 `started + completed/failed`，运行中调用保留 `started + 最新 progress`，
再用最新 `message.delta` 填充余量；禁止 terminal 脱离 started 单独留下无名称工具卡。

LanguageGUI 范式在这里落成四条可验收规则：

- **表面**：白色/浅蓝容器、细语义边框、紧凑动作行与卡片；活动轨不以黑色终端块
  作为默认外壳，终端底只属于 bash/code 详情体。
- **互动**：品牌蓝只表示当前选中、展开、聚焦和可操作入口；状态色只表示真实的
  pending/running/success/error/stopped 状态。
- **可读**：状态必须同时输出图标和文字；动画只是运行态辅助，`prefers-reduced-motion`
  下仍保留静态状态文字。
- **披露**：首屏只展示可扫描的工具 Action 卡；点击一张卡才展开对应详情体，同一组
  同时只展开一个，长输入/输出在各自滚动区域内截断而不撑破阅读轨。

Demo 页面若要演示工具调用，必须复用生产 `ActivityGroup`/`ToolRow` 与详情渲染器，
并使用可审计的 fixture 事件；禁止在 demo 内直接拼接 `content_blocks` 或用模型文本
伪造工具调用。没有真实事件时应显示空态，而不是“成功”的静态样例。

### 族专用渲染器（TOOL_BODY_RENDERERS 注册表）
| 族 | 块 | 几何/表面 | 排版 | 状态 |
|---|---|---|---|---|
| bash/code | TerminalBlock | 12px 圆角细边；raised body + `surface-sunken/58` header；左 30px 状态点槽；margin 0 | mono 13；行高 22；输出 `white-space:pre` 横滚不折行（对齐是载荷） | 运行中只画横幅无分隔线；退出码≠0 → 错误胶囊（`status-error` 边 40%/底 8%，11px，sticky） |
| read | ReadBlock | 12px 圆角细边；raised body + `surface-sunken/58` banner；48px 行号列；margin 0 | mono 13/22；行号右对齐 `user-select:none`（chrome 不是内容） | 长行横滚；行号列固定不随文件宽度动 |
| search | SearchBlock | 与 ReadBlock 同表面；body 8/14/12/0 | 13/22 `pre` 不折行；行号弱化；文件组头 600 路径 + 命中数，整行折叠开关 | matches/paths 两形态；截断标记 |
| write/edit | DiffCard | `.chat-diff`：`rounded-lg` 边 `border-subtle` 底 `surface-raised`；头 `surface-sunken/50` | mono 11/16；增行 `status-success/10`、删行 `status-error/10`；split 双栏等宽 | 按文件分组（`divide-y`）；文件头 `surface-base/60` |
| others | 通用 IN/OUT 卡 | 凹陷底 `surface-sunken` + 细边 + 12px 圆角；section 独立限高 150px（长输入不掩埋短输出） | mono 12/18；IN/OUT 标签 sticky | 错误输出 `status-error`；单文件工具不暴露 IN |

展开体行节奏：块形卡 `margin: 4px 0 4px 4px` 缩进挂入活动组。

---

### ReviewSummaryBlock
- 仅消费 `languagegui/v1` 的明确 `review-summary` block，不把普通 Markdown 或代码 fence 自动提升为评审卡。
- verdict、finding severity、check status 均为固定枚举；finding/check 数量、文本、line 与 URL 受 parser 上限和安全白名单约束。
- 文件名仅为展示文本；外链复用安全 URL 策略；没有安全 URL 时不生成动作。
- 单条 finding 失败不影响其他 findings；整个块渲染异常走独立 ContentBlock fallback。
- 评审卡展示 verdict、统计、问题清单和检查项，状态同时提供文字与语义色，不只依赖颜色。

## 5. 思考层

| 件 | 表面 | 排版 | 动效 |
|---|---|---|---|
| ReasoningProcessPanel | `rounded-lg` 边 `border-subtle` 底 `surface-sunken/80`；体 `max-h-52` 纵滚 | 头 `caption` 悬停 `surface-sunken` | 流式扫光 2.6s（作用域 `.chat-reasoning-panel .chat-reasoning-sweep`）；`aria-expanded`；defaultExpanded 可控；流式期间同样允许手动折叠 |
| ThinkingPlaceholder | 无容器 | 14px 扫光圆点 + 「Thinking」+ shimmer 文字（`text-tertiary`→`text-secondary` 80% 渐变裁字） | sweep 2.6s；shimmer 2s |

---

## 6. 决策 / 计划层

### ApprovalCard
- 卡：`rounded-xl` 边 `status-warning/25` 底 `surface-raised` `shadow-sm`；头 `status-warning/5` + 底分线。
- 风险徽章：pill 细边；risk 变体 `warning/30` 边 + `warning/10` 底 + `warning` 字。
- 详情块：mono 12/20 `surface-sunken` `rounded-lg` `max-h-40` 纵滚 `break-all`。
- 动作按钮：`min-h-9` `rounded-lg` `caption` 500；primary=success 底、danger=error 底、danger/warning outline、ghost=`border-strong`。
- memory 开关区：顶分线 `caption` `text-tertiary` 悬停 `secondary`。
- 响应：容器查询 `max-width:28rem` → 主/拒按钮纵排通栏。

### ChatWorkflow
- 外层：Goal、Plan 模式方案正文与本轮执行步骤收敛为一个 `rounded-xl` 工作流容器，边 `border-subtle`、底 `surface-raised/90`、`shadow-card`；三者全空时不渲染。
- Goal：目标正文始终显示，头部带状态文字；有执行步骤时显示 `done/total` 和可访问进度条。Goal 是 Workbench 自有语义，不冒充 LanguageGUI 官方组件。
- 方案草稿：同一容器内的可折叠 Markdown 区，标题明确为「方案草稿 / Plan 模式」，避免与执行步骤混淆；内容体限高纵滚。
- Action 链：`ol` 保留执行顺序；每步是独立白色 Action 卡，展示 `Action N`、状态图标、状态文字和步骤正文；当前步骤带 `aria-current=step`；卡间用 `border-subtle` 连接线。
- 只读边界：LanguageGUI multi-prompt 的设置、删除、加号属于编辑器能力；无真实 mutation、失败回滚与并发契约时不渲染伪编辑控件。
- 计划清除：合法的空 `run.plan_updated.steps=[]` 必须清掉同 run 的旧 Action 链；畸形载荷保持 fail-safe，不破坏上一份有效快照。

### Composer
- 位置：阅读轨底部固定区域，白色大圆角浮层（外壳 22px 圆角），浅蓝画布上与正文形成清晰层级；与正文共用约 920px 轨道，窄屏降为可用宽度。本次迁移不新增 `<1024px` 或 mobile 断点承诺。
- 输入：`textarea` 为 13px 圆角控件，默认白底细边，`focus-visible` 使用 brand 蓝色边框与 ring；Enter 发送，Shift+Enter 换行；高度随内容增长。
- PromptBox 工具：附件与图片支持本地选择/拖放、类型大小校验、预览和移除；存在本地附件时明确提示 Runtime 尚未接入并阻止发送。语音入口只在浏览器原生 speech-to-text 可用时启用，结果写入 draft；Library 选择 prompt 模板，Apps 只展示真实连接状态。
- 输入工具负向保证：不把 File/Blob/base64/本地路径拼进 instruction；没有 upload/user-content-part 协议时不显示上传成功，不让 Apps 选择伪造 Runtime tool 权限。
- 真实行为：待发送队列、usage 文案、运行中 stop、发送/禁用态、artifact 摘要、ChatWorkflow 与工作区开关继续由生产 ChatPage/store 驱动；PromptBox 的附件/语音/Library 均走上述明确边界，不复用 demo 本地消息或模型选择状态。
- 可访问性：textarea 保留 placeholder 与键盘路径；发送/停止等图标或按钮保留可读标签、禁用态和全局 `focus-visible` ring。

---

## 7. 页面级辅助面

| 面 | 位置 | 表面/排版 |
|---|---|---|
| ArtifactShelf 摘要卡 | composer 上方 | 单行摘要条：`rounded-lg` 边 `border-subtle` 底 `surface-raised`；数量 + 最新文件名 + 品牌色「打开工作区」；明细归工作区 |
| ArtifactWorkspace 面板 | 右缘 320px | `border-l` 不透明 `surface-warm`；头 52px；行 `rounded-card` 边 `border-subtle` 底 `surface-raised`；状态：草稿 `warning` / 已接受 `success`；空态 EmptyState |
| 待发送队列 | composer 上方 | `rounded-lg` 边 `border-subtle` 底 `surface-raised`；序号 11px、条目 `caption` 截断、移除 X 悬停变深 |
| 用量行 | composer 右下 | 11px `tabular-nums` `text-tertiary` |
| run 错误告警 | 流尾居中 | `caption` `status-error` ✕ 前缀 |
| 空态脚手架 | 空会话 | 居中 `caption` 引导 + chips：pill 细边 `surface-raised` `caption` `text-secondary`，悬停边 `brand-primary/35` + 字品牌色 |
| MessageActions | 消息悬停 | 复制/分叉 14px 图标 `text-tertiary` 悬停变深；时钟 11px；`group-hover` 显现 |
| 页头状态/开关 | 48px 页头右 | run 胶囊 + SSE 连接胶囊 + PanelRight 开关；LanguageGUI 对话皮肤下保留轻量白色/浅蓝顶栏，不重复显示 assistant 角色头 |

---

## 8. 动效与无障碍总表

| 动效 | 时长/曲线 | reduced-motion 覆盖 |
|---|---|---|
| Markdown 解析节流 | 100ms | 不适用 |
| thinking shimmer | 2s ease-in-out | ✅ |
| 流式光标 | 2s ease-in-out | ✅ |
| 角色/正文完成态 | 静态 | ✅ |
| StateDot 追逐 | 1s 硬保持阶梯 | —（纯指示，未覆盖，已知缺口） |
| status-pulse 会话点 | 2s ease-in-out | ✅（index.css） |
| 按钮/卡片过渡 | 150–200ms | 未全局覆盖（交互反馈类，可接受） |

无障碍约定：
- 扫光/状态点纯视觉 → `visually-hidden` 读屏文本承载状态（工具行、终端卡）；
- 折叠件一律 `aria-expanded`；图标钮一律 `aria-label` + `title`；
- 行号列 `user-select:none`（复制只带走源码）；
- 全局 `:focus-visible` ring 2 `brand-primary/40` offset 2。

---

## 9. 缺口与路线

1. **Source（来源引用）Part**：搜索类结果无引用出处部件；search 块已有 matches 数据，加引用行 + 段类型即可（下一候选）。
2. **成果内容预览**：后端无 artifact 内容端点，工作区只做清单（DESIGN.md Known Gaps 第 7 条）。
3. **artifact 内联段**：摘要卡是 M1 取舍；流内按 run 锚定的内联卡列 M2。
4. ~~**StateDot reduced-motion**：追逐动画未进 reduce 覆盖，补一条 CSS 即可。~~ **已实现**（`StateDot.module.css` line 58: `@media (prefers-reduced-motion: reduce)` 已覆盖，`animation: none; opacity: 0.6`）。
