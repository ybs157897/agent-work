# Codex 桌面 App 对话渲染逆向研究 — 与本仓 Web 前端对比

> 研究对象：OpenAI Codex 桌面 app（即 ChatGPT.app，bundle id `com.openai.codex`，版本 26.818.41509，macOS arm64 Electron）。
> 对照对象：本仓 `agent-team-workbench/web`（React + zustand + react-markdown）。
> 方法：纯静态分析（asar 索引解析 + minified bundle 字符串/CSS 证据抽取），未运行、未修改任何专有软件；本文只描述行为与极小示意片段，不复制其源码。
> 日期：2026-08-24。

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

---

## 1. Codex App 渲染分析（十问十答）

### Q1 对话流整体形态：气泡还是全宽 transcript？

**混合形态：用户消息是右对齐圆角气泡；assistant 及全部工具活动是全宽 transcript。**

证据：
- 用户消息外层 `flex w-max max-w-full ... rounded-3xl border ... px-3 py-1.5 backdrop-blur-sm`（`local-conversation-turn-*.js` 中模板字符串，`rounded-3xl` 大圆角 + 毛玻璃边框，明显气泡）；另有 `relative z-10 w-fit ... overflow-hidden rounded-3xl` 的动画容器。
- assistant 与工具区是 `flex w-full flex-col gap-3` 的纵向流；整条线程有 `--thread-content-max-width` 最大宽度约束并 `justify-center` 居中（`flex w-full max-w-(--thread-content-max-width) min-w-0 justify-center`）。
- 回合内条目顺序（`local-conversation-thread-turn-entries` + `local-conversation-turn` 编排）：user items → `agent-activity-collapsible`（可折叠工具活动组，头部带 `worked-for` 时长）→ `assistant-item`（正文）→ `tool-outputs` → `proposed-plan`（计划卡）→ `thinking-placeholder`（思考占位）→ `turn-diff`（回合一键 diff）→ 尾部动作行（复制/fork/反馈）。

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

**默认折叠进"工具活动组"头部，以活动摘要句流式滚动显示；有独立的 thinking 占位卡。**

- 回合内的 reasoning 映射为 `type: reasoning` 条目（`presentation: thought`），归入 `agent-activity-collapsible` 组（`local-conversation-turn` 中 `agent-activity-collapsible` 分支，可折叠、`persistedCollapsed`、完成后自动折叠 `auto-collapse`）。
- 活动标题是**现在进行时动宾短语**，由 `reasoning-item-heading` 生成：`Running <detail>{command}</detail>` / `Reading {target}` / `Searching for {query}` / `Listing files in {folder} folder` / `Ran command` / `Stopped command` 等（i18n 前缀 `localConversation.toolActivity.active.*`）。
- 无任何内容时的占位：i18n id `thinkingShimmer.default`，默认文案 **"Thinking"**，带 shimmer 动画（`thinkingShimmer.default`）；用户输入等待态文案 "Waiting for your answer"。
- 原始思维链走 `codex/event/agent_reasoning*`（raw/delta/section_break）事件流，summary 与 raw 分离（`reasoning_content_delta` vs `reasoning_raw_content_delta`）。

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

## 2. 我们的 Web 端现状（代码引用）

前端栈：React 18 + zustand + react-markdown(remark-gfm) + Tailwind，SSE 事件流驱动。核心文件：

- `web/src/pages/chat.page.tsx` — 对话页骨架与消息编排
- `web/src/components/chat/assistant-turn.tsx` — assistant 回合（reasoning 折叠 + 正文）
- `web/src/components/chat/reasoning-disclosure.tsx` — 思考折叠条
- `web/src/components/chat/markdown-body.tsx` — markdown 渲染
- `web/src/stores/chat.store.ts` — 事件→消息推导（buildMessages）
- `web/src/stores/events.ts` — SSE 路由
- `web/src/index.css` — chat 样式（294-440 行）

### 现状逐项

1. **对话流形态**：用户右对齐圆角气泡（`chat.page.tsx:330-341`，`rounded-[22px]` 最大宽 525px/82%），assistant 全宽左侧（`assistant-turn.tsx:23-24`）；工具/错误/系统消息是**居中细行**（`MetaLine`，`chat.page.tsx:343-357`）。
2. **Markdown**：react-markdown + remark-gfm（`markdown-body.tsx:8-9`），自定义 `a`（新窗打开）、`code`（行内样式）、`pre`（`overflow-x-auto rounded-md bg-surface-sunken`，`markdown-body.tsx:31-35`）。标题/列表/表格/引用样式在 `index.css:294-356`。**无复制按钮、无语言标签、无语法高亮、无换行开关**。
3. **Reasoning**：折叠条（`reasoning-disclosure.tsx`），默认折叠，流式时显示最后一行摘要并横向滚动（`latestLine` + `scrollLeft` 跟随，26-31 行）+ sweep 微光动画（`index.css:362-375`）；标题文案 "Think"；展开后 `max-h-[50vh]` 纯文本。与 assistant 正文合并为一个回合组件（`chat.page.tsx:297-319` thinking+assistant 合并渲染）。
4. **工具调用**：`tool.started` 生成居中细行「调用工具 {tool}：{args_summary}」（`chat.store.ts:106-118`），`tool.completed/failed` 按 `call_id` 折叠回同一行，输出挂 `detail` 等宽块（`chat.store.ts:120-146`；渲染 `chat.page.tsx:350-355`，`max-h-48` 滚动）。失败把前缀改成「工具失败」红色居中行。**无状态图标、无耗时、无每工具卡片化、无折叠组**。
5. **命令输出**：无独立终端概念；输出即工具 detail（适配器截断 2000 字符、等宽 `<pre>`）。无 exit code、无 stdout/stderr 区分。
6. **Diff**：无。后端有 `apply_patch` 类工具时只会以工具行+detail 文本出现。
7. **审批**：消息流下方固定卡片（`chat.page.tsx:243-255`）：警告图标 + 「审批请求 · {kind} · {risk}」+ summary + 批准/拒绝两按钮；拒绝时 `prompt()` 弹窗收理由（`chat.page.tsx:379-383`）；操作后 toast。**不在消息流内、无授权粒度选项、无批准后状态卡**。
8. **流式**：`aggregateRunStream` 聚合 delta（`chat.store.ts:46-57`），awaitingReply 时渲染流式回合（`chat.page.tsx:232-239`）；正文末尾 6px 闪烁块状光标（`index.css:377-396` `chat-caret-blink`）；三点 typing 动画（`assistant-turn.tsx:26-30`）；容器 smooth 滚动到底（`chat.page.tsx:186-190`）。
9. **错误/中断/空态/用量**：错误居中红行 + detail 块（`MetaLine`）；空态文案「输入第一条消息…」（`chat.page.tsx:226-230`）；**无 token 用量展示、无重连进度、无中断按钮**（中断态只有状态徽标文案）。
10. **计划/todo**：无。

---

## 3. 逐项对比表

| 维度 | Codex App | 我们 Web | 差距/可借鉴点 |
| --- | --- | --- | --- |
| 用户消息 | 右对齐 `rounded-3xl` 毛玻璃气泡 | 右对齐 `rounded-[22px]` 气泡 | 基本对齐；Codex 有进入动画 |
| Assistant 排版 | 全宽 transcript，`--thread-content-max-width` 居中限宽 | 全宽左对齐 | 可借鉴：内容最大宽度变量，长行可读性 |
| 工具活动 | 独立"活动组"：可折叠、组头 worked-for 计时、行级动宾摘要+状态图标+耗时 | 居中细行文字 | **最大差距**：改卡片化+折叠组+耗时 |
| Reasoning | 折叠进活动组，动宾摘要实时滚动，"Thinking" shimmer | 独立折叠条+最后一行滚动+sweep | 我们已接近；可补动宾摘要与完成后自动折叠 |
| Markdown | 自研渲染器，GFM+math+directive，流式淡入 | react-markdown+gfm | 够用；可补流式淡入与 math |
| 代码块 | 复制/语言标签/换行开关/粘性头/highlight.js(45 语言+自动检测,120ms 防抖)/预览运行 | 纯 pre+行内 code | **高价值差距**：复制按钮+语言标签+高亮 |
| 命令输出 | xterm 终端 tab + 内联摘要 + 20k 尾部截断 + `[output truncated]` 标记 + 控制字符处理 | 等宽 pre，2000 字符截断 | 可借鉴：尾部保留截断+显式截断标记 |
| Diff | turn-diff + unified/split + 文件折叠阈值(25 文件/2000 行) + 行内字符高亮 + merge 冲突采纳 | 无 | **高价值差距**：至少做 unified diff 卡 |
| 审批 | 流内 form 卡，Allow once/Always/This conversation + Reason + 权限维度行 | 流外固定卡，批准/拒绝 | 可借鉴：入流+授权粒度+拒绝理由字段化 |
| 流式 | token 级 + 块淡入 + 二段式高亮 + shimmer | 块状闪烁光标 + typing 点 + smooth 滚动 | 我们已具备基本态；可补块级淡入 |
| 错误/重连 | stream-error 自动重连进度文案、usage limit modal | 红色居中行 | 可借鉴：重连进度文案 |
| Token 用量 | composer tooltip `{used}k / {window}k tokens used` | 无 | 低成本高感知，可借鉴 |
| 计划/todo | todo-list 复选清单 + proposed-plan 卡(反馈) | 无 | 中期可借鉴 |

---

## 4. 可借鉴清单（按价值排序）

1. **工具调用卡片化 + 可折叠活动组**（对齐 Q4）：把 `tool.*` 居中细行改为行卡片：图标（按工具类别）+ 动宾摘要（进行时→完成时文案切换）+ 状态色 + 耗时；同回合工具收进一个可折叠组，组头显示总耗时。数据侧只需 `started_at`/duration（事件时间线已有 `occurred_at` 可差值）。
2. **代码块增强**（对齐 Q2）：复制按钮 + 语言标签 + highlight.js（按需异步、120ms 防抖、未知语言降级纯文本）。react-markdown 下用 `pre` 自定义组件包一层即可，零后端改动。
3. **Unified diff 卡**（对齐 Q6）：对 `apply_patch`/edit 类工具输出解析 unified diff，渲染文件名头 + `+N/−M` 徽章 + 行背景色（对接现有 `--color-status-*` 令牌）；>25 文件或 >2000 行默认折叠。
4. **输出截断策略显式化**（对齐 Q5）：截断保留尾部并前置 `[output truncated]` 标记（现在是适配器 2000 字符静默截断，前端无感知）；顺带过滤 ANSI/控制字符。
5. **审批卡入流 + 粒度**（对齐 Q7）：审批卡移进消息流对应位置；按钮扩为「本次允许 / 本会话总是允许」，拒绝理由改内联输入而非 `prompt()`；批准后卡片转为完成态而非消失。
6. **Token 用量 tooltip**（对齐 Q9）：composer 角落常驻 `{used}k / {contextWindow}k`，数据可由后端 run 维度 usage 投影（协议已按 run 幂等累计 usage）。
7. **重连进度文案**（对齐 Q9）：`reconnecting` 状态时显示 `Reconnecting {n}/{max}`，替代静默。
8. **流式块级淡入**（对齐 Q8）：对流式 markdown 新增块加 `@keyframes fade-in`（我们已有 caret，补块淡入成本低，注意 `prefers-reduced-motion`，Codex 同样处理了）。
9. **线程内容最大宽度**（对齐 Q1）：`--thread-content-max-width` 居中限宽，宽屏下长行可读性。
10. **todo/plan 卡**（对齐 Q10）：若后端后续发 `plan_update` 类事件，可复用审批卡的卡片骨架做复选清单。

> 注：Codex 的 xterm 终端 tab、Python 沙箱运行、Edit with AI、mermaid/vega 预览依赖桌面端能力（node-pty、codex 引擎、iframe 沙箱 CSP），Web 短期不必对齐，列为长期可选。

---

## 附：证据复查路径速查

| 结论 | 证据文件（asar 内路径） |
| --- | --- |
| 用户气泡 / 线程限宽 | `webview/assets/local-conversation-turn-Bhd6WQLo.js`（`rounded-3xl`、`--thread-content-max-width`） |
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
