# Codex 桌面端正文（Markdown 主体）渲染体系逆向清单

> 研究对象：ChatGPT.app（Codex 桌面，bundle `com.openai.codex`，26.818.41509）asar 内
> `webview/assets/app-initial-DwVrCWuo.js`（主 bundle）+ `app-initial-CaQrAMKA.css`。
> 本文聚焦**正文描述层**（assistant 消息正文的 markdown 渲染）：标签清单、每标签的
> 样式与格局、内容填充管线。方法同 codex-desktop-rendering-comparison.md（纯静态分析）。
> 日期：2026-08-24。

---

## 1. 渲染管线（内容如何填充）

```
codex/event/agent_message_delta（token 流）
  → 回合 assistant-item 文本累计
  → 预处理 Kpa()：
      · 剥 HTML 注释 <!--...-->
      · GitHub alert 语法 > [!NOTE|TIP|IMPORTANT|WARNING|CAUTION] → 加粗 **Note** 前缀
      · <details open><summary>X</summary>Y</details> → :::github-details{summary="X" open="true"}Y::: 指令
      · 代码围栏先占位 @@CODEX_FENCED_CODE_BLOCK_N@@（防预处理破坏代码内容），解析后还原
      · \n{3,} 压缩为 \n\n
  → marked 系 tokenizer（GFM 全集 + 两个自定义扩展：math、codexDirective）
  → token 数组 → Uia() 类型分发 → components 覆盖表
  → React 树
```

关键机制：

- **components 覆盖表**（三层合成）：基础表 `Ora()`（p/h1-6/ul/ol/li/blockquote/hr/table 族/strong）
  + 会话级 `{a: Pra(), code: Nra(), img: nna}`（链接/代码块/图片）
  + 指令表 `zfa()`（artifact 模板/文件引用/task-stub/github-details/抑制组）。任何一层都可被调用方再覆盖。
- **流式增强**：`streamingTokenKeys` 按 token 内容哈希（`qia()`：code 取围栏符、link 取 href+text、
  其余取 raw 首词）做 key；`Intl.Segmenter` 按**词**切 fade 段，CSS `data-markdown-animated` 下
  `li/tr/blockquote/hr/.FadeIn` 元素 opacity 0→1 淡入，`--fade-delay` 逐项延迟；图片走
  `_image-enter` 缩放淡入。
- **开放围栏**：流式中途未闭合的 code fence 标 `isCodeFenceOpen`，代码块可按
  `renderCodeBlocksAsWritingBlocks` 渲染为"书写中"块。
- **错误兜底**：整个 Markdown 树包在 ErrorBoundary（`name:"Markdown"`，resetKey=children）里，
  渲染崩溃回落到重试卡而非炸掉整页。

## 2. 根容器格局（MarkdownRoot）

`._MarkdownRoot`（CSS module `_1ns57_`）：

- 字号三档由 `data-markdown-text-style` 驱动：`base`（16px=--text-base，默认）、
  `large`（--text-lg + 行高 spacing*7）、`small`（--text-sm + 行高 spacing*5）；
  根级字号 = `--codex-chat-font-size`（app 默认 **16px**），行高 = 字号+8px。
- `overflow-wrap:anywhere`；`dir=auto` 散布在每个块级标签上（RTL 支持）；
  色调变体 `data-markdown-text-tone=tertiary`（次要灰）。
- 段落间距：段落/块级 `margin:0 0 .6875rem`（11px）；连续两个中文段落
  （`data-markdown-han-text`，`\p{Script=Han}` 检测）间距改为 `--spacing`——**CJK 专门排版规则**。

## 3. 标签清单：样式与格局

| 标签/token | 渲染组件 | 样式与格局 | 内容填充 |
|---|---|---|---|
| `paragraph` | `<p class=Paragraph dir=auto>` | margin-bottom 11px；含 Han 字符打 `data-markdown-han-text`（连续 CJK 段距加大）；**媒体段检测**：整段全是图 → `MediaGridParagraph`（flex-wrap gap 12px 图片网格），单图 → `MediaWideBlock` 宽块 | token.tokens 递归 inline |
| `heading` h1-h6 | `<hN class=Heading dir=auto>` | 统一 `margin:20px 0 10px;font-weight:600;line-height:1.25`；字号按级：h1 24px / h2 20px / h3·h4 17px(22px 行高) / h5·h6 15px(20px 行高) | token.tokens inline |
| `list` (ul/ol) | `<ul/ol class=List+UnorderedList/OrderedList>` | margin:0；padding-inline-start 1.3125rem；**嵌套子弹三级轮换** disc→circle→square；ol `start` 属性保留；含任务项挂 `contains-task-list` | items 递归 |
| `list_item` | `<li class=ListItem(+TaskListItem)>` | 相邻项 margin-top .5rem；项内首尾子元素去 margin；项内嵌套列表 margin-top .5rem；**任务项**：grid 两列（checkbox+内容），checkbox disabled 只读 | token.tokens |
| `blockquote` | `<blockquote class=Blockquote dir=auto>` | **不是传统灰底**：透明背景，左侧 4px 圆角竖条（`::after` 定位条，--border-medium 色），padding-inline-start 24px，上下 padding 8px，margin-bottom 8px | token.tokens 递归 |
| `hr` | `<hr class=HorizontalRule>` | 1px --border-medium 顶线，margin 28px 0，clear:both | 无内容 |
| `table` | TableContainer 组件 | 容器比正文宽出两侧 thread-content-margin（出血布局），`TableScroller` 横向滚动（细滚动条），wide-block 模式可突破正文限宽居中；**悬停浮出 TableActions**（复制 TSV/表格 markdown 原文 `markdownSource`、放大预览弹窗 MarkdownTablePreview） | header/rows 的 cell tokens 递归；`markdownSource=raw.trim()` 原文随组件携带 |
| `thead/tbody/tr/th/td` | 对应类 | 表头 600 字重+底部分割线（--border-medium）；行间断发线（--border-light）；单元格上下 padding 10px/8px、inline-end 24px（末列 0）；**纯数字列**（`/^\d+$/`）加 NumericTableCell（nowrap）；列宽按 data-col-size sm/md/lg/xl 分档（thread-content-max-width 的 4/24~18/24） | 单元 tokens + align |
| `code`（围栏块） | `pre` 被拆壳（Fragment），`code` → Nra 工厂组件 | 语言从 `language-{lang}` class 提取；`data-code-block-index` 全文档递增序号；`isCodeFenceOpen` 流式开放围栏态；CodeBlock 本体（chatgpt-code-block chunk）：语言标签+复制+换行开关+粘性头+hljs 高亮（120ms 防抖懒加载）+SVG/mermaid/vega/html 预览切换+Python 运行（桌面） | token.text 原文 |
| `codespan`（行内码） | z9i 智能组件 | 基础形：`_InlineMarkdown_`（等宽字体、底色 pill：background-primary-ghost-hover 60% + text 6% 混合、radius 6px、padding 1px 6px、font-size .92em、`box-decoration-break:clone` 跨行不断壳） | **内容智能升级**：`@路径` → 文件引用 chip；疑似路径 → 文件链接；其余按启发式换组件；`dir=ltr` 强制 |
| `link` | Pra 工厂 → a4i 特判，否则外链组件 | 先尝试解析为**文件引用/插件 mention**（cwd+path 解析成功→文件 chip）；否则外链组件（externalResourcePolicy allow/restricted、oai_link_source 热线参数剥除、目标 external-browser、悬停 tooltip、favicon 懒加载 Google s2 兜底）；`data-markdown-raw-link-label` 保留原始文本 | token.href + tokens |
| `image` | nna 媒体组件 | blockRemoteMedia 按 externalResourcePolicy 拦截远程图；`animateEnter`（_ImageEnter 缩放淡入）；宽块/网格由 paragraph 层检测驱动 | token.href/text/title |
| `strong` | `<strong class=font-semibold>` | 仅字重 600（不依赖 UA 默认 bold） | inline 递归 |
| `em` | 原生 `<em>` | UA 默认斜体 | inline 递归 |
| `del` | 原生 `<del>` | UA 默认删除线 | inline 递归 |
| `math` | 懒加载 katex chunk，Suspense fallback=原文 | `renderToString(strict:ignore, throwOnError:false)`——渲染失败永远回落原文不炸 | token.text（TeX） |
| `codexDirective` | directives[name] 查表 | 见 §4 | `:::name{attrs}body:::` 语法的 attributes+body |
| `html` | 仅 `<br>` 原样放行，其余丢弃（allowBasicHtml 模式另有白名单路径） | — | raw |
| `space`/`def` | 不渲染（null） | — | — |
| `escape` | 纯文本 | — | text |
| `br` | 原生 `<br>` | — | — |
| `text` | renderText（流式 fade 分段）或原文 | 词级 fade（Intl.Segmenter） | text/tokens |

## 4. 指令（codexDirective）体系

`:::name{attrs} ... :::` 自定义语法，按 name 分发：

| 指令 | 渲染 | 用途 |
|---|---|---|
| `github-details` | `<details class="group my-3 rounded-xl border border-border/30 bg-surface-secondary/15 px-4 py-3">` + summary（chevron 旋转+500 字重） | 模型输出里的折叠详情（由 `<details>` HTML 预处理转换而来） |
| `codex-file-citation` | 文件引用卡（path + lineRangeStart/End，可点开侧栏） | 正文内引用代码位置 |
| `artifact-template` | 模板卡（zod 校验 attrs：artifact_kind/display_name/skill_*） | 产物模板入口 |
| `task-stub` | `{title, prompt}` 卡 | 任务快捷入口 |
| `automation-citation` | 自动化引用 | — |
| **抑制组**（inbox-item / archive-thread / thread-purpose-changed / created-thread / code-comment / git-stage / git-commit / git-create-branch / git-push / git-create-pr / pr-auto-fix-progress + 剥壳标记） | 统一渲染为 UB（不显示） | 模型输出的系统操作标记，正文里隐藏 |

未注册指令名 → 原样显示 raw 文本（`oia` 的 `i==null?e.raw` 回落）。

## 5. 内容填充的数据源对照（回合条目级）

回合（turn）内的条目类型（导出管线 `conversation-markdown` chunk 的权威清单）：
`user-message / assistant-message / reasoning / exec / patch / web-search / todo-list /
proposed-plan / plan-implementation / turn-diff / generated-image / image-view / userInput /
permission-request / approval / system-event / remote-task-created / model-changed /
model-rerouted / personality-changed / forked-from-conversation / automation-update /
mcp-server-elicitation`。

正文的 markdown 文本只来自 **assistant-message**（`agent_message_delta` 流式累计）与
少量卡片的内嵌 markdown（proposed-plan 正文等）；工具输出/状态不经过 markdown 渲染器
（走各自活动行组件）。即：**markdown 渲染器的服务对象单一（模型正文），工具类信息全部
走结构化卡片**——这是"正文不脏"的根本原因。

## 6. 与我们现状的对照（要点）

| 维度 | Codex | 我们（DSH 移植后） |
|---|---|---|
| 渲染器 | 自研 token 分发 + 三层组件覆盖表 + 指令体系 | react-markdown + remark-gfm，components 只覆盖 a/code/pre |
| 段落/标题/引用/表格 | CSS module 逐标签精修（CJK 段距、引用竖条、表格出血+悬停操作+数字列） | index.css 的 .chat-markdown 粗放规则 |
| 行内码 | 智能升级（@路径→文件 chip）+ pill 样式 | 灰色 pill（够用） |
| 代码块 | 语言标签/复制/换行/粘性头/hljs 懒加载/预览 | 语言标签/复制/换行/hljs 懒加载——**已对齐** |
| 流式 | 词级 fade（Segmenter）+ 开放围栏书写块 | 块状光标 caret；无块级淡入 |
| 指令/文件引用 | :::directive + codex-file-citation + @路径 chip | 无 |
| 容错 | Markdown ErrorBoundary + katex 失败回落原文 | 无 ErrorBoundary |

**低成本高价值借鉴**（若做）：
1. `.chat-markdown` 按 Codex 的 Heading/Blockquote/List 规则精修（引用改左竖条、标题
   20/10 margin、列表嵌套子弹轮换、任务列表样式）。
2. 段落 CJK 检测加 `data-markdown-han-text` 段距规则。
3. 表格：容器出血 + 横向滚动 + 悬停复制。
4. Markdown 树套 ErrorBoundary。
5. 流式块级淡入（`data-markdown-animated` 思路，注意 prefers-reduced-motion）。

## 附：证据路径

| 结论 | asar 内位置 |
|---|---|
| token 分发开关 `Uia` | app-initial-DwVrCWuo.js（搜 `case\`codespan\`` 所在函数） |
| 基础组件表 `Ora` | 同上（搜 `ZR.Paragraph`） |
| 链接工厂 `Pra` / 代码工厂 `Nra` / 行内码 `z9i` | 同上（搜 `function Pra(` 等） |
| 预处理 `Kpa`（alert/details/围栏占位） | 同上（搜 `@@CODEX_FENCED_CODE_BLOCK_`） |
| 指令表 `zfa`/抑制组 `wRt` | 同上（搜 `git-create-pr` 所在 Set） |
| CSS module（`_1ns57_` 全 99 规则） | app-initial-CaQrAMKA.css |
| 流式 fade | 同上 css（`data-markdown-animated` / `_fade-in_1ns57_1`） |
