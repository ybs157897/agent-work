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
几何来源：消息、正文、Callout、代码面板与工具 chip 对齐 LeAgent；Terminal/Read/Search/Diff 内容体保留 DSH 几何。颜色全部改走本项目语义 token。

---

## 1. 段层（TranscriptSegment，8 种 + meta-detail 为 meta 子变体）

| 段 | 触发 | 容器/表面 | 排版 | 动效 | 实现 |
|---|---|---|---|---|---|
| user | 用户消息 | 通栏 `article`；角色标签下接 `.chat-user-card`：12px 圆角、`surface-sunken/55`、`border-subtle/60`、12×16px | 角色标签：6px tertiary 点 + 11px/600「你」+ 时间；正文 15/1.75 | 悬停操作条 opacity 0.36→100 | transcript-view.tsx |
| assistant | 助手文本 | 通栏 `article`；角色标签下接无容器 `.chat-prose` 正文 | 角色标签：6px brand 点 + 11px/600 Agent 名 + 时间；正文 15px/1.75 | 流式：100ms Markdown 节流 + 2px caret；落定后静态 | assistant-turn.tsx |
| thinking | reasoning 事件 | `.chat-reasoning-panel`：`rounded-lg`(12) 边 `border-subtle` 底 `surface-sunken/80` | 头 `caption`；体 `h-52` 纵滚 `px-3 py-2.5` | 流式扫光带 300px 2.6s ease-out | reasoning-activity-row.tsx |
| meta | error/system | 无容器，居中 | `caption`；错误 `status-error` 带 ✕ 前缀；时间戳 `tabular-nums` | — | transcript-view.tsx |
| meta-detail | meta 附详情（`msg.detail` 子变体，非独立段类型） | `rounded-md` 底 `surface-base` 等宽块 `max-h-48` | mono 11/16 | — | transcript-view.tsx MetaLine |
| activity | 同 run 连续工具行 | LeAgent 式横向 `ActivityGroup`；chip 点击后在轨道下展开详情 | chip 28px 高，编号/图标/单行工具名/耗时/状态 | 单选展开；运行态 Loader；横向滚动 | tool-card.tsx |
| thinking-placeholder | run 进行中无正文 | 无容器行 | 14px 扫光圆点 + `caption`「Thinking」+ shimmer 渐变文字 | 扫光 2.6s；shimmer 2s ease-in-out | thinking-placeholder.tsx |
| turn-diff | 回合聚合 diff | 同 DiffCard（见 §4） | — | — | turn-diff-card.tsx |
| approval | 审批请求 | `rounded-xl`(16) 边 `status-warning/25` 底 `surface-raised` `shadow-sm` | 见 §6 | 容器查询 <28rem 动作纵排 | approval-card.tsx |

段间距：`.chat-thread space-y-3`（12px）；正文限宽 `min(72ch, 100%)` 居中。

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
| details | 边框 + 凹面软底，summary 可聚焦/可展开 | 不开放危险 raw HTML；只消费解析器产生的安全节点 |
| 崩溃兜底 | MarkdownErrorBoundary 内联 fallback（`<pre>` mono 13 纯文本块，`resetKey` 复位） | 按 resetKey 清错误态重试 |

---

## 3. 代码 / 表格专用块

### CodeBlock（块级代码）
- 高亮：highlight.js `lib/common` 懒加载；fence 语言声明优先，未注册/自动检测 relevance≤0/加载失败 → 纯文本降级；流式期跳过，落定后 120ms 防抖处理。
- 外观：16px 圆角细边代码面板；36px toolbar；11px mono 小写语言标签；下载 + 复制（clipboard→execCommand 双兜底，2s Check 反馈）；内容 13px/1.6 横向滚动，不软换行。
- 行内 code 与块级 code 外观分离（行内走 §2 pill）。

### TableCard（markdown 表格容器）
- `div.chat-table-wrap`：8px 圆角、`border-subtle` 细环、横向滚动；保留悬停复制 TSV（空单元格保留占位，1s 反馈），表格视觉按 §2 的 LeAgent 斑马表执行。

---

## 4. 工具块层（activity 组内）

### 公共件（LeAgent 工具 chip + 本地专用详情体）
| 件 | 规格 |
|---|---|
| ActivityGroup | 28px 横向滚动轨；左侧 28px detail toggle；chip 间距 2px、无滚动条 |
| ToolRow | 高 28px，宽 132–224px；序号 10px mono + 12px 工具图标 + 单行工具名 + 10px 耗时 + 状态图标；hover 凹面 40%，active 50% |
| 状态 | pending/running=Loader；success=Check；error=Alert；stopped=中断图标；图标之外有读屏文本，运行动画遵循 reduced-motion |
| 展开体 | 同一时间只展开一个 chip；复用 Terminal/Read/Search/Diff/IN-OUT 专用体，max-height 22rem 纵滚 |

### 族专用渲染器（TOOL_BODY_RENDERERS 注册表）
| 族 | 块 | 几何/表面 | 排版 | 状态 |
|---|---|---|---|---|
| bash/code | TerminalBlock | 12px 圆角；底 `surface-sunken`；左 30px 状态点槽（卡自身 padding）；margin 16/0 | mono 13；行高 22；输出 `white-space:pre` 横滚不折行（对齐是载荷） | 运行中只画横幅无分隔线；退出码≠0 → 错误胶囊（`status-error` 边 40%/底 8%，11px，sticky） |
| read | ReadBlock | 12px 圆角；底 `surface-sunken/55`；banner `surface-sunken` 9×14；48px 行号列 | mono 13/22；行号右对齐 `user-select:none`（chrome 不是内容） | 长行横滚；行号列固定不随文件宽度动 |
| search | SearchBlock | 同 ReadBlock 几何；body 8/14/12/0 | 13/22 `pre` 不折行；行号弱化；文件组头 600 路径 + 命中数，整行折叠开关 | matches/paths 两形态；截断标记 |
| write/edit | DiffCard | `.chat-diff`：`rounded-lg` 边 `border-subtle` 底 `surface-raised`；头 `surface-sunken/50` | mono 11/16；增行 `status-success/10`、删行 `status-error/10`；split 双栏等宽 | 按文件分组（`divide-y`）；文件头 `surface-base/60` |
| others | 通用 IN/OUT 卡 | 凹陷底 `surface-sunken` + 细边 + 12px 圆角；section 独立限高 150px（长输入不掩埋短输出） | mono 12/18；IN/OUT 标签 sticky | 错误输出 `status-error`；单文件工具不暴露 IN |

展开体行节奏：块形卡 `margin: 4px 0 4px 4px` 缩进挂入活动组。

---

## 5. 思考层

| 件 | 表面 | 排版 | 动效 |
|---|---|---|---|
| ReasoningProcessPanel | `rounded-lg` 边 `border-subtle` 底 `surface-sunken/80`；体 `h-52` 纵滚 | 头 `caption` 悬停 `surface-sunken` | 流式扫光 2.6s；`aria-expanded`；defaultExpanded 可控 |
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

### PlanCard
- 卡：`rounded-lg` 边 `border-subtle` 底 `surface-raised/70` `px-snug py-tight`；头 `caption` `text-secondary` + 进度 n/m。
- 步骤盒 14px：pending=空框 `border-strong`；done=success 底白勾；active=info 边 + `info/40` 环。
- compact 模式供 dock 复用。

### ChatBottomDock
- 面板：`rounded-xl` 边 `border-subtle` 底 `surface-raised/90` `shadow-sm`；头 `caption` 悬停 `surface-base/60`（static 变体无悬停）；体 `max-h-40` 纵滚。
- 三槽 goal/todoPlan/proposedPlan 全空不渲染。

---

## 7. 页面级辅助面

| 面 | 位置 | 表面/排版 |
|---|---|---|
| ArtifactShelf 摘要卡 | composer 上方 | `rounded-lg` 边 `border-subtle` 底 `surface-raised` `px-3 py-2`；头 `caption` 500 + 品牌色「打开工作区」；行 = mime 图标 + `caption` 文件名 + 11px 字节数；超 4 项折叠 |
| ArtifactWorkspace 面板 | 右缘 320px | `border-l` `surface-raised`；头 52px 同页头；行 `rounded-lg` 边 `border-subtle` 底 `surface-base/60` `px-3 py-2`；状态：草稿 `warning` / 已接受 `success`；空态 EmptyState |
| 待发送队列 | composer 上方 | `rounded-lg` 边 `border-subtle` 底 `surface-raised`；序号 11px、条目 `caption` 截断、移除 X 悬停变深 |
| 用量行 | composer 右下 | 11px `tabular-nums` `text-tertiary` |
| run 错误告警 | 流尾居中 | `caption` `status-error` ✕ 前缀 |
| 空态脚手架 | 空会话 | 居中 `caption` 引导 + chips：pill 细边 `surface-raised` `caption` `text-secondary`，悬停边 `brand-primary/35` + 字品牌色 |
| MessageActions | 消息悬停 | 复制/分叉 14px 图标 `text-tertiary` 悬停变深；时钟 11px；`group-hover` 显现 |
| 页头状态/开关 | 52px 页头右 | 状态文 `caption` 500 按 runStatusColor；PanelRight 开关同图标钮规范 |

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
