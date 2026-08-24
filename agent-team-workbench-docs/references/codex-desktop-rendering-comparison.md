# Codex 桌面 App 对话渲染逆向研究 — 与本仓 Web 前端对比

> 研究对象：OpenAI Codex 桌面 app（即 ChatGPT.app，bundle id `com.openai.codex`，版本 26.818.41509，macOS arm64 Electron）。
> 对照对象：本仓 `agent-team-workbench/web`（React + zustand + react-markdown）。
> 方法：纯静态分析（asar 索引解析 + minified bundle 字符串/CSS 证据抽取），未运行、未修改任何专有软件；本文只描述行为与极小示意片段，不复制其源码。
> 日期：2026-08-24；**二次核对**：同日对本机 `/Applications/ChatGPT.app` asar 重抽 `local-conversation-turn-*.js` 与 CSS，修正回合展示顺序与 Reasoning 归属（见 §0.3）。

---

## 0. 概览

### 0.1 分析对象与主渲染路径结论

| 项 | 结论 |
| --- | --- |
| Bundle | `/Applications/ChatGPT.app/Contents/Resources/app.asar`（284MB，8562 个文件） |
| 主渲染路径 | **Web（Chromium/Electron renderer 内的 React SPA）**，不是原生 AppKit/SwiftUI |
| 框架证据 | `Contents/Frameworks/Codex Framework.framework` 版本号 `151.0.7922.170`（Chromium 风格）+ 内含 Chromium proto/字符串与 `Failed to launch Electron app` 字符串 → 即改名的 Electron Framework（strings 可查） |
| Renderer 入口 | `app.asar::webview/index.html` → `assets/index-D0rj-UsY.js` → 主 bundle `assets/app-initial-DwVrCWuo.js`（14.6MB，rolldown/vite 产物，React + Tailwind + radix） |
| 附属原生件 | `Resources/codex`（220MB Mach-O，本地 Codex 引擎/CLI）、`codex-code-mode-host`、`codex_chronicle` 均为 arm64 可执行文件；`Resources/com.openai.codex.manifest` 是目录（内含 Localizable.strings） |

**分析方法**：写了一个 80 行 Python 脚本（stdlib）解析 asar header pickle 列出全量索引（`/tmp/asar_tool.py`），按需 seek 抽取 20 余个 renderer chunk 到 `/tmp` 分析。判断依据均为可复查的 asar 内路径（下文证据列）。

### 0.2 关键 asar 文件索引（证据地图）

- `webview/index.html` — renderer HTML（CSP、module 入口）
- `webview/assets/app-initial-DwVrCWuo.js` — 主 bundle（14.6MB），含事件路由、turn 状态机、审批卡、diff 组件
- `webview/assets/local-conversation-turn-Bhd6WQLo.js`（62KB）— 对话"回合"渲染骨架（用户/助手/工具/计划/思考的编排）
- `webview/assets/agent-activity-item-DtLa1Ph4.js`（32KB）— 工具活动行卡片
- `webview/assets/reasoning-item-heading-BfIxDFSo.js` — 活动/思考标题文案
- `webview/assets/chatgpt-code-block-CfL_-YzH.js`（36KB）— 代码块组件（复制/预览/编辑/下载）
- `webview/assets/highlight-code-bx-gqOKs.js` + `core-CJQI62Vb.js` + `core-tMVMyPoU.js` — highlight.js 精简集（~45 语言 + `detectCodeLanguage` 自动检测）
- `webview/assets/editor-diff-page-BJOK7SsZ.js` — diff 视图页（unified/split 切换）
- `webview/assets/command-execution-command-B6mTNUBq.js` + `xterm-output-panel-*.js` — 命令输出（xterm.js）
- `webview/assets/plan-summary-item-content-DB5EiBcB.js` — 计划/反馈卡
- `webview/assets/app-initial-CaQrAMKA.css`、`app-BY4JAmsE.css` — 样式（Tailwind + 设计令牌）

### 0.3 回合布局与展示顺序（二次核对结论）

Codex **不在时间线上原样排列**，而是在 `local-conversation-turn-*.js` 里把同一 turn 的 items **重排为固定槽位**（`X(\`slot-key\`, …)` 注册顺序，本机 bundle 实测）：

```
┌─ 单 turn 纵向流（flex flex-col gap-3，居中限宽） ─────────────────────┐
│ 1. user-item              用户气泡（右对齐，见下表）                    │
│ 2. agent-activity-collapsible   工具活动组（含 reasoning 条目）         │
│    └─ 或 agent-activity-summary  （无明细时仅组级 worked-for 摘要）    │
│ 3. automation-update      自动化/MCP 小条目（若有）                    │
│ 4. assistant-item         助手正文 Markdown（全宽 transcript）          │
│ 5. tool-outputs           正文附带的工具输出块（若有）                  │
│ 6. post-assistant-items   正文落定后追加的活动切片                      │
│ 7. mcp-server-elicitation MCP 追问卡（若有）                           │
│ 8. proposed-plan          计划 Markdown 卡（可反馈）                    │
│ 9. thinking-placeholder   进行中 shimmer 占位（见 Q3，非 reasoning 正文）│
│10. turn-diff              回合 diff 汇总（无 assistant 正文时也可先出）  │
│11. remote-task / personality-changed / forked-from-conversation …     │
│12. end-resource / thread-handoff-operation                           │
└──────────────────────────────────────────────────────────────────────┘
```

**思考 / 正文 / 工具 三者的 Codex 顺序（关键修正）**

| 内容 | Codex 位置 | 形态 |
| --- | --- | --- |
| **工具 + reasoning** | turn **顶部** `agent-activity-collapsible` | `type: reasoning` 与 exec/read/search 等同列在活动组；组头滚动 **动宾摘要**（`Running {cmd}` / `Reading {target}`），不是独立 "Think" 块 |
| **助手正文** | 活动组 **之后** `assistant-item` | 全宽 Markdown transcript |
| **进行中占位** | 正文/计划 **之后** `thinking-placeholder` | shimmer + 文案 **"Thinking"**（`thinkingShimmer.default`）；turn 仍在进行且无 pending items 时显示 |
| **计划** | 正文 **之后** `proposed-plan` | Markdown 计划卡；`todo-list` 步骤清单是另一类 item |

**布局令牌（本机 CSS 实测）**

| 区域 | Codex 类名 / 变量 | 值或行为 |
| --- | --- | --- |
| 线程列宽 | `--thread-content-max-width` | **40rem（640px）**，`justify-center` 居中 |
| 用户气泡 | `rounded-3xl border border-border/80 bg-background-primary-soft/70 px-3 py-1.5 backdrop-blur-sm` | 右对齐 `w-max max-w-full`，毛玻璃边框 |
| 助手区 | `flex w-full flex-col gap-3` | 全宽 transcript，无气泡壳 |
| 正文 Markdown | `--markdown-font-size` + `data-markdown-text-style` | 16px 基准，三档字号 |
| turn-diff 行 | `py-[var(--turn-diff-row-padding-y)]` | 独立汇总行样式 |

**本仓 Web 编排（对照）**

`buildMessages` **按事件时间线 chronological 推消息**，`renderTranscript` + `groupActivity` 只做工具折叠，**不做 turn 级重排**：

```
用户 → [计划?] → [工具活动组×N，按事件顺序] → [thinking+assistant 合并轮次] → 系统/错误 MetaLine
审批卡挂在消息流尾部（非 turn 槽位）
```

| 差异点 | Codex | 本仓 Web |
| --- | --- | --- |
| 工具 vs 正文顺序 | 固定：**先活动组、后正文**（正文后还可有 post-assistant） | **按 SSE 到达顺序**；通常工具先于 `message.completed`，但无强制 |
| Reasoning 载体 | 活动组内 `reasoning` item + 组头摘要行 | `ReasoningDisclosure` **"Think"** 独立折叠条，叠在 assistant 正文 **上方** |
| 进行中占位 | 正文后的 `thinking-placeholder` shimmer | 流式 `AssistantTurn`：无正文时有 typing 三点 / reasoning sweep |
| 计划位置 | 正文 **之后** `proposed-plan` | `run.plan_updated` **时间线位置**（常在工具与正文之间） |
| 内容限宽 | 整列 40rem 居中 | 用户 `max-w-[min(525px,82%)]`；助手/工具 **scroll 区全宽**（≈32.8rem vs 40rem） |

---

## 1. Codex App 渲染分析（十问十答）

### Q1 对话流整体形态：气泡还是全宽 transcript？

**混合形态：用户消息是右对齐圆角气泡；assistant 及全部工具活动是全宽 transcript；整列 40rem 居中限宽。**

证据：
- 用户消息外层 `flex w-max max-w-full ... rounded-3xl border border-border/80 bg-background-primary-soft/70 px-3 py-1.5 backdrop-blur-sm`（`local-conversation-turn-*.js`）；动画容器 `relative z-10 w-fit max-w-full overflow-hidden rounded-3xl`。
- assistant 与工具区是 `flex w-full flex-col gap-3`；线程列 `flex w-full max-w-(--thread-content-max-width) min-w-0 justify-center`，CSS 变量 **`--thread-content-max-width: 40rem`**（`app-initial-CaQrAMKA.css`）。
- **回合内展示顺序**见 §0.3（二次核对修正）：非旧稿写的「thinking-placeholder 在 assistant 之前」——placeholder 在正文/计划 **之后**；**reasoning 与工具同在顶部活动组**。

### Q2 assistant 正文 Markdown 支持哪些元素？代码块有什么功能？

**自研 marked-token 渲染器 + 设计系统组件，元素覆盖 GFM 级别；代码块功能远超基础渲染。**

- 支持的 token 类型集合（主 bundle 内 `paa` Set）：`blockquote/br/code/codespan/def/del/em/escape/heading/hr/html/image/link/list/list_item/paragraph/space/strong/table/text`，另有 `math`（KaTeX 风格 inline/block）与 `codexDirective`（自定义指令）token。表格/任务列表（TaskList/TaskListItem 组件）齐全。
- 代码块（`chatgpt-code-block-*.js` + 主 bundle `CodeBlock` 组件）：
  - **复制按钮**（`showCopyButton`、`onCopyCode`，复制行为埋点 `location: code-block`）；
  - **语言标签**：头部 title 默认取语言显示名，无语言时回落 `Code`（i18n id `chatgpt.codeBlocks.language.fallback`）；
  - **行号**：对话内代码块无行号（`data-line-numbers` 在对话 code block 中 0 命中；行号仅出现在 diff 视图 gutter 与 CodeMirror 编辑器 `cm-lineNumbers`）；
  - **换行开关**：`Enable word wrap / Disable word wrap` 按钮；
  - **粘性头部**：`stickyHeader`，滚动时语言栏+按钮吸顶；
  - **语法高亮**：highlight.js 按需异步加载（懒 chunk `highlight-code-bx-gqOKs.js`，注册 arduino/bash/c/cpp/csharp/css/diff/go/…/yaml 约 45 种语言 + `detectCodeLanguage` 语言自动检测），**120ms debounce** 后高亮，流式期间先纯文本后增强；`Unknown language` 异常静默降级为纯文本；
  - **可执行/预览增强**（桌面特性）：SVG/mermaid/vega/react/html 代码块有 Preview⇄Code 切换、Python `Run code` 沙箱执行、`Edit code`（行内编辑）、`Edit with AI`（flag 控制）、`Download code`、分享链接（flag 控制）。SVG 预览限高 `max-h-96`。
- 主题化：代码块/表格等通过 CSS 变量（`--markdown-font-size` 等）与 `data-markdown-text-style=base|large|small` 三档字号。

### Q3 reasoning/思考过程怎么展示？

**Reasoning 不是 assistant 正文上方的独立块，而是归入顶部「工具活动组」；另有正文后的 shimmer 占位。**

- `type: reasoning` 条目与 exec/read/search 等 **同列**于 `agent-activity-collapsible`（`local-conversation-turn` 中 `Ba` selector 读 `reasoning` 的 `summary`，经 `reasoning-item-heading` 转成组头 **动宾摘要**）。
- 活动标题模板（`reasoning-item-heading-*.js`）：`Running {command}` / `Reading {target}` / `Searching for {query}` / `Ran command` / `Stopped command` 等（i18n 前缀 `localConversation.toolActivity.active.*`）；**不是**固定文案 "Think"。
- 原始思维链事件：`codex/event/agent_reasoning*`（raw/delta/section_break）；summary 与 raw 分离（`reasoning_content_delta` vs `reasoning_raw_content_delta`）。组头滚动的是 **summary 句流**，不是全文 dump。
- **thinking-placeholder**（`Lo` 组件，槽位在 `proposed-plan` **之后**）：turn 进行中且无 pending items 时显示 shimmer + 默认文案 **"Thinking"**（`thinkingShimmer.default`）；与 reasoning 条目 **并存但职责不同**——placeholder 是等待态，reasoning item 是已产生的推理/活动摘要。
- 活动组完成后可 `auto-collapse`（`persistedCollapsed`）；组头显示 `worked-for` 总耗时。

### Q4 工具调用怎么展示？

**每次调用一行紧凑卡片（row），统一折叠在"agent activity"组里；行内含状态图标、动宾摘要、耗时。**

- 结构（`agent-activity-item-*.js`）：`row` 布局 + `min-w-0 truncate` 摘要 + `icon-xs` 状态图标；每行是一个 `button`（可点开详情）。
- 显示字段：工具类别（`exec`/`patch`/`mcp-tool-call`/`web-search`/`read`/`todo-list`/`turn-diff` 等 30+ 类型）、命令文本或目标、状态（`queued/running/in_progress/success/failed/error/timedOut/interrupted/stopped/declined`…）、`durationMs`（exec 卡片数据里有 `startedAtMs`/`durationMs`）、`processId`、`cwd`。
- 状态→文案用三态动宾模板：进行时/完成时/失败各有独立 i18n（如 `writeActive`→`Updating settings`、`writeCompleted`→`Updated settings`、`writeFailed`→`couldn't update settings`；`threadsCreateActive`→`Creating chat`、`threadsCreateCompleted`→`Created chat`…，共数十组）。
- 成功/失败视觉差异：状态枚举里 `success` 与 `failed/error/warning` 分列，图标与颜色跟随（`text-text/60` 弱化完成态、错误态有专门 `error` 色类）；失败行文案带否定语义。
- 组级头部：`worked-for` 计时（`workedDurationMs`），子代理活动显示 `and {count} other subagents` 折叠计数；完成回合的活动组可整体折叠，只留摘要行。

### Q5 命令执行输出怎么展示？

**双轨：对话流内联摘要行 + 独立终端面板（xterm.js 真终端渲染）；输出滚动截断到 20000 字符。**

- 对话流内：exec 条目带 `aggregatedOutput` + `exitCode`（事件归一化代码中 `output: {aggregatedOutput, exitCode}`），多命令拆成多行（`commandActions` 每条一个 `{cmd}`）；shell 元命令（裸 bash/zsh/pwsh 包装）会被剥掉只显示真实命令（正则 `/^(?:.*[/\\])?(?:bash|cmd|zsh...)(?:\s|$)/i`）。
- 输出聚合函数 `uct({current, delta, maxChars=20000})`：**保留尾部**（`t.slice(-n)`），截断后在开头加 `[output truncated] ` 标记；同时过滤控制字符（`\r`/`\x03`/`\b`/`\x7f`，支持 backspace 退格编辑模拟）。
- 后台终端 tab：`command-execution-command-*.js` 打开 durable tab，内容用懒加载的 `XtermOutputPanel`（xterm.js，VSCode terminal 主题变量 `--vscode-terminal-ansi*`、字体 `font-vscode-editor`）；空输出占位 "No output yet"。
- 输出区域限高滚动：`max-h-96 overflow-y-auto`。

### Q6 diff / 文件修改怎么展示？

**回合级"turn-diff"汇总 + 专业 diff 视图页：unified/split 双模式、按文件折叠、行内评论、merge-conflict 处理。**

- 回合完成时生成 `turn-diff` 条目（统一 diff 文本 + 每 patch 的 changes 归并），未回答前的 diff 即时可见（`q==null&&Ni!=null&&X('turn-diff',Ni)`）。
- `editor-diff-page-*.js`：头部显示 `{fileCount, plural, one {# file changed} other {# files changed}}` + `+N/−M` 行数徽章（`linesAdded/linesRemoved` 组件）；工具栏 **unified⇄split 切换**（tooltip "Switch to unified diff"）与 "Toggle rich preview"。
- 文件数 ≤25 且增删行 ≤2000 时默认展开，否则折叠（`xe=25,Se=2e3` 阈值常量）。
- diff 主体是自绘 web component（主 bundle 内嵌 CSS）：`[data-line-type="change-addition/deletion"]` 行背景色 + `[data-diff-span]` 行内变更字符级高亮（`--diffs-bg-addition-emphasis`）、行号 gutter（`data-line-number`）、吸顶文件头（`[data-diffs-header][data-sticky]`）、hunk 分隔、`expand`/`expand-all` 图标、**merge conflict 标记渲染**（"(Current Change)/(Incoming Change)" + current/incoming 快速采纳按钮）。
- 增删配色对接 VSCode 令牌：`--color-codex-diff-added → --vscode-diffEditor-insertedLineBackground` 等（`app-BY4JAmsE.css`）。
- 还支持行内评论（`enableComments`、`modelComments`——模型自评 review 评论也有专门卡片）。

### Q7 审批（approval）交互？

**内嵌对话流的审批卡（form），三级授权粒度 + 拒绝理由 + 供应商化权限行。**

- 卡片容器带 `data-codex-approval-surface` 属性，`@container/approval-card` 容器查询适配窄屏（按钮变纵向排列）。
- 事件源：`codex/event/exec_approval_request` 与 `codex/event/apply_patch_approval_request`。
- 按钮（i18n `approvalRequestCard.*`）：主按钮 **Allow once**；下拉 **Always allow** / **Allow this conversation**；次按钮 **Deny**；附 **Reason** 标签（拒绝理由）与 "Approval options"。
- 命令审批提示语：**"Allow ChatGPT to run this command?"**（子代理场景 "Do you want {actor} to run this command?"）；子代理目标用 destination 名。
- 权限维度行：**Terminal**、**Edit files**、**Internet access**（网络理由 "{host} isn't on the current network allowlist"）、文件移动 `{sourcePath} → {targetPath}`。
- patch 审批额外选项：**Allow all edits**（说明文案 "Allow this and future file edits in this conversation without asking again"）、"Allow similar commands"（命令前缀白名单 `Allow commands that start with {command}`）。
- 批准后：状态枚举 `approved/denied`，对应工具行文案切换（approved 系列文案），卡片收起转为活动行。
- 另有自动审批审查卡（`item/autoApprovalReview/*`）与 nudge 提示（`auto-review-approval-nudge-*.js`）。

### Q8 流式输出？

**token 级流式 + 淡入动画 + shimmer 思考占位；markdown 流式时先渲染后增强。**

- assistant 正文流式：`agent_message_delta` / `reasoning_content_delta` 等事件直接 append；markdown 渲染器带 `streamingTokenKeys` + `animateMarkdown`，配合 CSS `data-markdown-animated` 对新块（`li/tr/blockquote/hr` 及 `_FadeIn_` 元素）做 opacity 0→1 淡入（`@keyframes _fade-in`，`--fade-delay` 逐项延迟），图片有 `_image-enter_` 缩放淡入。
- 思考中：`thinkingShimmer` 微光动画 + "Thinking" 占位；活动行实时刷新当前动作。
- 高亮增强是异步二段式（先纯文本、120ms 防抖后换高亮 HTML），避免流式期间卡顿。
- 自动滚动/视口策略：turn 列表有 `estimatedHeightPx` 缓存与离屏延迟渲染（`deferOffscreenRendering`、IntersectionObserver `rootMargin: 600px`）。

### Q9 错误、中断、空态、token 用量等辅助元素？

- **错误**：`stream-error`（自动重连显示 `Reconnecting {attempt}/{maxAttempts}`）与 `system-error` 两类；usage limit 有专门 modal（"Usage limit resets" 展开节 + 绿色 pill）；`turn_aborted`、`interrupted`、`declined`、`timedOut` 等终态各有独立枚举与文案。
- **中断**：Stop 按钮 + 中断的命令标 `interrupted`（`interruptedCommandExecutionItemIds`）。
- **空态**：终端 tab "No output yet"；线程空态有 startup loader（OpenAI blossom shimmer logo，`index.html` 内联）。
- **token 用量**：composer 底部 tooltip **"{usedTokens}k / {contextWindow}k tokens used"**（i18n `composer.contextWindowUsageTooltip`）；`codex/event/token_count` 事件；`thread/compacted` 上下文压缩事件。

### Q10 计划 / todo 列表怎么展示？

**两类：turn 级 `todo-list`（步骤复选清单）与 `proposed-plan` 卡（markdown 计划，可反馈）。**

- `todo-list`：数据是 `plan: [{step, status}]`（`turn/plan/updated` 事件更新，同 turn 内新条目**替换**旧条目而非追加）；渲染为复选清单，全部 `completed` 才算完成；回滚时 inProgress 步骤自动改回 `pending`。
- `proposed-plan`：markdown 内容卡（`plan-summary-item-content-*.js`），标题 "Plan"/"Writing plan"，未完成时默认展开、完成后默认折叠（`defaultCollapsed:!Gt.completed`）；卡片带 **👍/👎 反馈**（"Positive feedback option for following instructions" 等十余种反馈原因枚举：followedMyInstructions/goodCodeOrOutputQuality/slowOrBuggy/lostContext/offTrackOrWrongScope…）与举报入口。
- `plan-implementation`：计划的实施进度条目（planContent + isCompleted）。

---

## 2. 我们的 Web 端现状（2026-08-24 代码快照）

前端栈：React 18 + zustand + react-markdown(remark-gfm) + highlight.js（懒加载），SSE 事件流驱动。

**核心文件**

| 文件 | 职责 |
| --- | --- |
| `web/src/pages/chat.page.tsx` | 消息编排、`renderTranscript`、`groupActivity`、停止按钮、60s 超时 |
| `web/src/components/chat/assistant-turn.tsx` | 助手轮：Think 折叠 + Markdown 正文 + 流式 typing |
| `web/src/components/chat/reasoning-disclosure.tsx` | "Think" 折叠条（Codex 无对应独立块，见 §0.3） |
| `web/src/components/chat/tool-card.tsx` | 活动组 + `ToolRow` + 终端/diff/read/search/IN-OUT 分派 |
| `web/src/components/chat/markdown-body.tsx` | GFM + `CodeBlock` + `TableCard` |
| `web/src/components/chat/plan-card.tsx` | `run.plan_updated` 复选清单 |
| `web/src/components/chat/approval-card.tsx` | 流内审批 + 三级 allow scope |
| `web/src/stores/chat.store.ts` | `buildMessages` 时间线推导、`formatTokenUsage` |
| `web/src/index.css` | `.chat-markdown` / `.chat-activity` / `.chat-reasoning` / `.chat-plan` |

### 现状逐项

1. **对话流形态**：用户右对齐 `rounded-[22px]` 气泡，`max-w-[min(525px,82%)]`（`chat.page.tsx` `UserBubble`）；assistant / 工具 **全宽左对齐**，**无** `--thread-content-max-width` 居中列。工具连续行折叠为 **「活动 · N 次调用」** 组（`ActivityGroup`）。错误/系统仍为居中 `MetaLine`。
2. **展示顺序**：**时间线顺序**（§0.3 对照表）；`thinking`+`assistant` 在 `renderTranscript` 中合并为单个 `AssistantTurn`（Think 在上、正文在下）。计划卡随 `run.plan_updated` 事件位置插入，**不在**正文之后强制重排。
3. **Markdown**：react-markdown + remark-gfm；`.chat-markdown` 逐标签对齐 Codex 格局（16px/28px、CJK 段距、标题/列表/引用/表格）。`CodeBlock`：**复制 + 语言标签 + 换行开关 + highlight.js 120ms 防抖**；`TableCard` 悬停复制 TSV。无 math/directive/预览运行。
4. **Reasoning**：独立 `ReasoningDisclosure`（"Think"），默认折叠，流式时最后一行摘要 + sweep；展开 `max-h-[50vh]` 纯文本。**与 Codex 活动组内 reasoning 模型不同**。
5. **工具调用**：`ToolRow` 卡片：族图标 + 摘要 + 耗时 + 状态点；展开分派 `TerminalBlock` / `DiffCard` / `ReadBlock` / `SearchBlock` / IN-OUT。同 run 连续工具进 `ActivityGroup`，组头 worked-for。
6. **命令输出**：`TerminalBlock`（ANSI 上色、exit code、prompt 行）；适配器仍可能 2000 字符截断，前端无 `[output truncated]` 标记。
7. **Diff**：`DiffCard` unified 子集（文件头、+N/−M、25 文件/2000 行折叠阈值、复制）；无 split/merge 冲突/行内评论。
8. **审批**：消息流内 `ApprovalCard`；allow once / thread / workspace；拒绝内联理由；决议后转完成态行。
9. **流式**：`aggregateRunStream` + 流式 `AssistantTurn`；正文末块光标 + 块级 fade-in（`index.css` `.chat-streaming`）；Reasoning sweep；smooth scroll。
10. **错误/中断/用量**：`MetaLine` 错误行 + `formatRunFailureMessage`；header **停止** 按钮 + 60s 首响超时；composer 右下 `{used}k / {window}k tokens`（`formatTokenUsage`）。无 stream-error 重连进度 modal。

---

## 3. 逐项对比表

| 维度 | Codex App | 我们 Web（当前） | 差距 / 下一步 |
| --- | --- | --- | --- |
| **turn 展示顺序** | 固定槽位：活动组 → 正文 → 计划 → shimmer → diff（§0.3） | 时间线顺序 + 工具组合并 | **架构差**：若要像素级对齐需 turn 级重排层 |
| 用户消息 | `rounded-3xl` 毛玻璃边框，`px-3 py-1.5`，列宽 40rem 内右对齐 | `rounded-[22px]`，`px-4 py-2.5`，max 525px/82% | 圆角/边框/列宽略异；可统一到 40rem 列 |
| Assistant 排版 | 全宽 transcript，**40rem 居中列** | 全宽，**无限宽居中** | 补 `--thread-content-max-width: 40rem` |
| 工具活动 | 顶部活动组，动宾三态文案，完成后 auto-collapse | `ActivityGroup`+`ToolRow`，静态族标题 | 可补动宾文案切换 + 完成后默认折叠 |
| **Reasoning** | **活动组内** reasoning item + 组头摘要；正文后 shimmer | **独立 "Think"** 在正文上方 | **模型不同**；对齐需把 reasoning 迁入活动组或改 UX  spec |
| Markdown | 自研 GFM+math+directive，流式块淡入 | react-markdown GFM，`.chat-markdown` 已对齐格局 | 缺 math/directive；块淡入已有部分 |
| 代码块 | 复制/语言/换行/粘性头/hljs 45 语言/预览运行 | 复制/语言/换行/hljs 120ms 防抖 | 基本对齐；缺粘性头与预览运行 |
| 命令输出 | xterm tab + 内联 20k 尾截断 + 显式标记 | `TerminalBlock` + 适配器 2k 静默截断 | 补 `[output truncated]` + 尾保留策略 |
| Diff | turn-diff + unified/split + merge 冲突 | `DiffCard` unified 子集 | 缺 split/turn 级汇总/merge |
| 审批 | 流内 form，Allow once/Always/Conversation | 流内卡，三级 scope + 内联拒绝 | 基本对齐；缺权限维度行文案 |
| 流式 | token 淡入 + 二段式高亮 + shimmer 占位 | caret + 块 fade-in + Think sweep + typing | 可补正文后 shimmer 占位语义 |
| 计划 | 正文**后** `proposed-plan` + todo-list | `PlanCard` 随时间线；无 proposed-plan 反馈 | 位置与反馈 UI 未对齐 |
| Token 用量 | composer tooltip | composer 右下 `{used}k/{window}k` | **已具备**（缺 tooltip 交互） |
| 错误/重连 | stream-error 重连进度 modal | MetaLine + 停止/超时 | 缺重连进度文案 |

---

## 4. 可借鉴清单（按价值排序，2026-08-24 修订）

**已完成（本文 §2 快照）**：工具卡片化 + 活动组、代码块增强、Diff 卡 unified 子集、审批入流 + 三级 scope、Token 用量、停止/超时、Plan 复选清单、Markdown 格局对齐。

**仍值得做**

1. **Turn 级展示重排**（对齐 §0.3）：在 `buildMessages` 之上增加 per-run 槽位编排——活动组置顶、正文居中、计划置后；或文档化「时间线顺序」为 intentional 差异。
2. **Reasoning 模型决策**（对齐 Q3）：二选一——(A) 把 `reasoning-delta` 迁入 `ActivityGroup` 组头动宾摘要；(B) 保留 "Think" 块但在 spec 中标注与 Codex 差异。当前实现是 B。
3. **线程列宽 40rem 居中**（对齐 Q1）：引入 `--thread-content-max-width: 40rem` + `mx-auto`，用户气泡/助手/活动同列。
4. **输出截断显式化**（对齐 Q5）：尾保留 + `[output truncated]` 前缀；与 Codex 20k 上限对齐协商。
5. **工具行动宾三态文案**（对齐 Q4）：`Running…` → `Ran…` / 失败否定式，替换静态 `Bash`/`Read` 族标题。
6. **Turn-diff 汇总行**（对齐 Q6）：run 完成时聚合 patch 为单条 diff 卡（现仅 per-tool `DiffCard`）。
7. **Thinking shimmer 占位**（对齐 Q3/Q8）：正文/计划之后、turn 未完成时的 shimmer 行（区别于 Think 折叠全文）。
8. **重连进度文案**（对齐 Q9）：`reconnecting` → `Reconnecting {n}/{max}`。
9. **Proposed-plan 反馈**（对齐 Q10）：计划卡 👍/👎（依赖后端事件）。
10. **代码块粘性头 / math / directive**（对齐 Q2）：中长期。

> Codex 的 xterm 终端 tab、Python 沙箱、Edit with AI、mermaid/vega 预览依赖桌面能力，Web 短期不对齐。

---

## 附：证据复查路径速查

| 结论 | 证据文件（asar 内路径） |
| --- | --- |
| **turn 槽位顺序** | `webview/assets/local-conversation-turn-Bhd6WQLo.js`（`X(\`agent-activity-collapsible\`)` → `assistant-item` → `post-assistant-items` → `proposed-plan` → `thinking-placeholder` → `turn-diff`） |
| 用户气泡 / 线程限宽 | 同上（`rounded-3xl`）；`app-initial-CaQrAMKA.css`（`--thread-content-max-width: 40rem`） |
| reasoning 在活动组 | `local-conversation-turn-*.js`（`type===\`reasoning\`` + `reasoning-item-heading-*.js`） |
| 回合条目编排 | `webview/assets/local-conversation-thread-turn-entries-Bq-nBZ66.js` |
| 活动行/三态文案 | `webview/assets/agent-activity-item-DtLa1Ph4.js`、`reasoning-item-heading-BfIxDFSo.js` |
| 代码块功能 | `webview/assets/chatgpt-code-block-CfL_-YzH.js`、`highlight-code-bx-gqOKs.js` |
| exec 输出/截断 | `webview/assets/command-execution-command-B6mTNUBq.js`、主 bundle `uct()` 函数（`[output truncated]`、20000 上限） |
| diff 视图 | `webview/assets/editor-diff-page-BJOK7SsZ.js`（unified/split、25/2000 阈值）；主 bundle 内嵌 diffs web component CSS（`data-line-type`/`data-diff-span`/merge conflict） |
| 审批卡 | 主 bundle（`approvalRequestCard.*`、`execApprovalRequest.*`、`patchApprovalRequest.*` i18n；`data-codex-approval-surface`） |
| 流式淡入 | `webview/assets/app-initial-CaQrAMKA.css`（`data-markdown-animated` / `_fade-in_` keyframes） |
| token 用量 | 主 bundle i18n `composer.contextWindowUsageTooltip` |
| 事件目录 | 主 bundle `codex/event/*` 字符串全集（70+ 种） |
| 终端 | `xterm-output-panel-*.js`、`--vscode-terminal-*` 变量（`app-BY4JAmsE.css`） |

未确认项：Codex 桌面 app 实际运行时的像素级样式（颜色具体值随主题令牌运行时解析）未逐一还原；`codex` 主二进制与 renderer 的进程协议（IPC schema）未逆向（超本任务范围）。
