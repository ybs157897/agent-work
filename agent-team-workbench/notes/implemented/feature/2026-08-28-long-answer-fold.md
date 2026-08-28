# chat 长回答渐进披露折叠（长文落定态默认收起）

Status: implemented

任务分支 `zcode/chat-long-answer-fold`（worktree 隔离）。决策依据：生产对话中
模型长回答（如 review 报告）落成整面 markdown 长文糊在正文流里，扫描性差；
按 LanguageGUI/Ant Design X 的渐进披露思路，把落定长文默认收起为「开头预览 +
展开全文」。

## 触发阈值

- 仅对**落定（非流式）**的 assistant markdown 正文生效；流式期间永远完整渲染
  （`LongAnswerFold` 内 `streaming ? null : splitLongAnswer(text)`）。
- 纯文本长度 **> 1600 字符**（`LONG_ANSWER_THRESHOLD`）才折叠；恰好 1600 不折。
- contentBlocks（canonical `languagegui/v1` 文档）始终完整渲染，折叠只作用于
  markdown 正文部分；带 contentBlocks 时的正文（已 `stripLanguageGuiFences`）
  照常参与折叠判定。

## 截断规则（`splitLongAnswer` 纯函数，返回 `{ preview, truncated } | null`）

1. 优先在**第一个二级/三级标题**（`\n## ` / `\n### `，四级及更深不匹配）处截断，
   前提是该标题出现在 **300 字符之后**；早于 300 的第一个标题直接否决，不向后
   找第二个标题。
2. 无合适标题时，在**不超过 800 字符的最后一个段落边界**（`\n\n`）处截断；
   连段落边界都找不到时返回 null——**宁可不折叠，不硬切**。
3. **围栏代码块保护**：候选截断点落在未闭合的 ``` 围栏内时（截取段内围栏线
   计数为奇），回退到该围栏开启行之前。该保护对标题规则同样生效——300 字符后
   的第一个「标题」若是围栏内注释行，则连同围栏一起让位。
4. 回退链全程要求截断点 > 0，否则视为无合法截断点，不折叠。

## 为什么这些部分不动

- **流式**：折叠只对完成态生效；流式期用户需要看到内容持续增长，且
  streaming→settled 由 assistant-turn 既有的 key 重挂载兜底，展开态自然复位为
  默认收起，不引入额外状态同步。
- **contentBlocks**：结构化块是独立渲染面（`ContentBlockList`），不在 markdown
  折叠区里；有独立体量与交互，收起正文不应隐藏块。
- **未折叠路径零包裹**：`LongAnswerFold` 不满足折叠条件时直接透传
  `renderBody(text)`，不插额外 div，`.chat-prose > * + *` 的直接子元素节奏与
  旧 DOM 结构完全一致（流式路径逐字节不变）。

## 实现要点

- 折叠态：预览 + 底部 `h-16` 渐变淡出（`transparent → hsl(var(--color-surface-base))`，
  与 transcript 同款 token，LanguageGUI 浅/暗 scoped 皮肤自动贴合）+
  「展开全文（约 N 字）」幽灵按钮（caption、tertiary → hover primary，对齐
  message-actions 按钮面）；N = 全文长度 − 预览长度（剩余体量）。
- 展开态：全文 + 「收起」；展开/收起是组件本地 state。
- 动效：AnimatePresence `mode="wait"` + height 0→auto（对齐
  reasoning-activity-row 的既有 motion 模式；`mode="wait"` 避免预览/全文两份
  长文同时占位）；`useReducedMotion` 下 initial=false、exit 只留 opacity、
  duration 0——完成态静态，动画只动 height/opacity，chevron 旋转加
  `motion-reduce:transition-none`。
- 可访问性：按钮带 `aria-expanded`；`aria-controls` 只在展开态输出（折叠态
  全文不在 DOM，不输出悬空引用，对齐思考面板先例）。
- 折叠态 body 复用 `chat-prose` 类：折叠时 markdown 块多包了一层 motion.div，
  挂同名类让逐元素排版与 `> * + *` 节奏原样生效（后代选择器重复匹配无害）。

## 已知取舍（备查）

- 围栏保护回退后预览可能很短（如正文以超长代码块开头）：预览体量让位于
  「绝不腰斩代码块」的硬约束；阈值判断仍以全文 > 1600 为准。
- 全文 > 1600 但标题与段落规则都给不出截断点（如 1600+ 字符的单段落）时不折叠，
  完整渲染——按规则集找不到合法截断点即放弃，不做无语义硬切。
- 标题规则只认全文第一个 h2/h3（含 300 字符前提），不在被否决后向后搜索后续
  标题；后续标题若在折叠区，属于预期隐藏内容。

## 验证

- `npx tsc -b` / `pnpm test`（458 全过，含新增 11 条钉）/ `pnpm lint` 全绿；
  另跑 `pnpm build` 确认 Tailwind @apply 编译通过（vitest/tsc/CI 均不编译
  index.css，首轮曾用错 `rounded-control`（仓内实际 token 是 `rounded-button`）
  仅 build 暴露，已修）。
- 纯函数钉：短文本/恰好阈值 null、h2/h3 标题截断、四级标题不充当截断点、
  早于 300 的标题否决与段落回落、段落边界截断、围栏保护（段落/标题两路）。
- render 钉（renderToStaticMarkup）：长文落定态折叠（按钮文案带「约 N 字」、
  `aria-expanded=false`、无 aria-controls、预览不含尾部标记）、流式态全文直出
  无按钮、短回答无折叠、contentBlocks 照常渲染。

## 遗留

- 展开态的真实点击交互未在浏览器演练（本环境点击合成受限）；交互为裸 React
  onClick + 本地 state，结构语义由 render 测试钉死，风险与上一轮思考面板折叠
  同级。
