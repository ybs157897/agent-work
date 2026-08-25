# 对话渲染清单与样式规格

日期：2026-08-25
状态：implemented（与代码逐条核对后落档）
配套文档：
- `web/DESIGN.md` —— 全局 token 与组件契约（事实源）；
- `references/codex-desktop-markdown-tags-inventory.md` —— Markdown 格局的逆向来源；
- `frontend-design-md-redesign.md` —— 重设计方案与路线。

**真相源约定**：本文是渲染清单与视觉规格；数值生效值以
`web/src/index.css`（`.chat-*` 区）、`web/src/components/chat/*.module.css`、
`web/tailwind.config.js` 为准。文档与代码漂移时以代码为准修本文。
几何来源：blocks 族移植自 DSH ui-tool 几何，颜色全部改走本项目语义 token。

---

## 1. 段层（TranscriptSegment，8 种）

| 段 | 触发 | 容器/表面 | 排版 | 动效 | 实现 |
|---|---|---|---|---|---|
| user | 用户消息 | 右对齐胶囊 `rounded-3xl`；边 `brand-primary/20`；底 `brand-primary/7%`（用户侧唯一品牌浅底消费点） | `text-base leading-6`（16/24）`text-primary`；`px-snug py-1.5` | 悬停操作条 opacity 0→100 | transcript-view.tsx |
| assistant | 助手文本 | 无容器，通栏正文；回合头常显 | 头：20px 头像 + `caption` 600 `text-secondary` 名 + 11px `tabular-nums` `text-tertiary` 时间；正文 `.chat-markdown` 16/28 | 流式：末块光标 6px 品牌色 1s step-end 闪烁；新块 0.3s 淡入 | assistant-turn.tsx |
| thinking | reasoning 事件 | `.chat-reasoning-panel`：`rounded-lg`(12) 边 `border-subtle` 底 `surface-sunken/80` | 头 `caption`；体 `h-52` 纵滚 `px-3 py-2.5` | 流式扫光带 300px 2.6s ease-out | reasoning-activity-row.tsx |
| meta | error/system | 无容器，居中 | `caption`；错误 `status-error` 带 ✕ 前缀；时间戳 `tabular-nums` | — | transcript-view.tsx |
| meta-detail | meta 附详情 | `rounded-md` 底 `surface-base` 等宽块 `max-h-48` | mono 11/16 | — | transcript-view.tsx |
| activity | 同 run 连续工具行 | `.chat-activity`：`rounded-lg` 边 `border-subtle` 底 `surface-raised/70` | 头 `caption` `text-secondary`；体 `space-y-2` 顶分线 `p-2` | 终态自动收拢；头悬停 `surface-base` | tool-card.tsx |
| thinking-placeholder | run 进行中无正文 | 无容器行 | 14px 扫光圆点 + `caption`「Thinking」+ shimmer 渐变文字 | 扫光 2.6s；shimmer 2s ease-in-out | thinking-placeholder.tsx |
| turn-diff | 回合聚合 diff | 同 diff 卡（见 §6） | — | — | turn-diff-card.tsx |
| approval | 审批请求 | `rounded-xl`(16) 边 `status-warning/25` 底 `surface-raised` `shadow-sm` | 见 §6 | 容器查询 <28rem 动作纵排 | approval-card.tsx |

段间距：`.chat-thread space-y-3`（12px）；正文限宽 `min(56rem, 100%)` 居中。

---

## 2. Markdown 正文元素层（assistant 段内）

| 元素 | 格局规格 | 备注 |
|---|---|---|
| 段落 | 16/28；段距 11px | 连续汉字段（`data-markdown-han-text`）段距 16px |
| 标题 h1–h6 | 24/20/17/17/15/15px，600，行高 1.25；margin 20/10 | 首末子元素 margin 归零 |
| 引用 | 透明底 + 左 4px 圆角竖条（`border-strong`，::after 定位） | 子段 margin 0 |
| 无序列表 | 1.3125rem 缩进；三级子弹 disc→circle→square | 嵌套顶距 .5rem；相邻项 .5rem |
| 有序列表 | decimal，其余同无序 | — |
| 任务清单 | 去子弹；grid 两列（checkbox + 内容） | checkbox 品牌色，13px 高下移 7px 对齐首行 |
| 行内 code | pill 底 `surface-sunken` 6px 圆角 `px 1×6`，0.92em | 跨行不断壳（box-decoration-break: clone） |
| 代码块 | → CodeBlock（§3） | `:not(pre)` 排除 |
| 表格 | → TableCard（§3） | 行线制：表头 600 + `border-strong` 底线，行 `border-subtle`；td 10px/th 8px 上下、24px 列距（末列 0）；13px |
| 链接 | `brand-primary`，悬停下划线 | 新标签 + noreferrer noopener |
| 分割线 hr | 默认 | 流式块级淡入覆盖 |
| 崩溃兜底 | PlainTextFallback：`caption` 提示 + mono 13 纯文本块 | MarkdownErrorBoundary 按 resetKey 复位 |

---

## 3. 代码 / 表格专用块

### CodeBlock（块级代码）
- 高亮：highlight.js `lib/common` 懒加载；fence 语言声明优先，未注册/自动检测 relevance≤0/加载失败 → 纯文本降级；流式 120ms 防抖。
- 外观：token 色保留 github 主题，容器底色/基色压回本卡令牌；语言标签 + 复制（clipboard→execCommand 双兜底，1.5s Check 反馈）+ 换行切换（WrapText）。
- 行内 code 与块级 code 外观分离（行内走 §2 pill）。

### TableCard（markdown 表格容器）
- `div.chat-table-wrap`：`display:block` 横向滚动（GitHub 同款）；悬停浮现复制按钮；复制 = DOM 抽 TSV（空单元格保留占位，1s 反馈）。

---

## 4. 工具块层（activity 组内）

### 公共件
| 件 | 规格 |
|---|---|
| DisclosureRow 披露头 | 24px 行高；[16px 前导槽] 6px [标题 14/24 `text-secondary`]；悬停图标 100ms 交叉淡切（状态图标→chevron） |
| StateDot 状态点 | 实心态：同色 10% 外晕 + 6/10 实心核；done=success / warning / error；running=3×3 像素追逐 1s 硬保持关键帧（info 色），相位差内联 delay |
| 工具摘要行 | 24px 单行：[前导] [标题 400] [2×2 分隔点] [摘要 14/24 `text-tertiary` FILL 截断] [耗时徽章 11px 不截断]；错误摘要 `status-error`；运行中 300px 扫光 2.6s（页面底色 60%） |

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
| 工具行/推理扫光 | 2.6s ease-out（10% 收尾停顿） | ✅ |
| thinking shimmer | 2s ease-in-out | ✅ |
| 流式光标 | 1s step-end | ✅ |
| 块级淡入 | 0.3s ease-out | ✅ |
| StateDot 追逐 | 1s 硬保持阶梯 | —（纯指示，未覆盖，已知缺口） |
| status-pulse 会话点 | 2s ease-in-out | ✅（index.css） |
| 披露头图标交叉 | 100ms ease | —（低于 150ms 阈值，可接受） |
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
4. **StateDot reduced-motion**：追逐动画未进 reduce 覆盖，补一条 CSS 即可。
