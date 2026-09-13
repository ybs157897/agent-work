# Chat ContentBlock v1

日期：2026-08-27
状态：implemented

## 目标

用稳定、可回放、可校验的通用正文块承载 LanguageGUI 风格的图形化输出。协议名固定为
`languagegui/v1`，不把具体业务领域写进 wire type。

## 文档信封

```json
{
  "version": "languagegui/v1",
  "blocks": []
}
```

生产入口：

1. `message.completed.data.content_blocks` 直接携带上述文档；
2. Markdown fenced code 使用语言 `languagegui`，内容为上述 JSON；普通 `````lang```` 代码 fence 仍由
   CodeBlock 渲染，不会因为内容像 JSON 或包含 `review` 字样而自动升级为 ContentBlock。

生产 `/chat` 创建 Run 时显式发送 `output_contract=languagegui/v1`。后端把版本化
协议说明合并进该 Run 的 `system_prompt` 快照；用户 `instruction`、历史回放和非 Chat
运行保持原文。协议改变会进入 config digest，旧 provider session 不会被错误复用。

两条入口必须经过同一个解析器。流式 Markdown 在 fence 未闭合前保持源码；解析失败、
未知版本或没有任何有效 block 时显示普通代码块，不吞正文。

已结束的 LanguageGUI 正文允许补回信封末尾单独缺失的 `}` 或 `]}`，前提是
所有 block 已完整闭合，补回后仍通过同一白名单解析器。不得补写字符串、单元格、
行或 block 内部结构；流式阶段和 canonical `content_blocks` 不应用此恢复。
恢复仅影响展示，历史消息及复制的源文不变。

## 通用字段

- `id`：可选稳定标识，最多 80 字符；缺失时由渲染位置生成 React key。
- `title`：可选标题，最多 160 字符。
- `description`：可选说明，最多 500 字符。
- `source`：可选 `{ "label": string, "url"?: string }`；URL 只接受 http(s)、站内绝对/相对路径和锚点。

## Block union

### metric

`items` 为 1–8 项；每项包含 `label`、`value`，可选 `detail`、`delta`、
`tone=neutral|positive|warning|negative`。

### table

`columns` 为 1–12 列：`{ key, label, align? }`，`align=left|center|right`；
`rows` 最多 100 行，单元格只允许 string、有限 number、boolean 或 null。
渲染时长路径与说明按列宽换行；可用容器宽度不超过 640px 时按行纵向展示字段与值，
保留表头语义与完整单元格内容，无须左右拖动。图表附带的数据表保留原有布局。

### chart

`chart=bar|line`；`labels` 为 1–64 项；`series` 为 1–4 组，每组 `values` 数量必须与
labels 相等且全部为有限 number。可选 `unit`、`source`、`y_domain=zero|auto`；行情等
窄幅变化用 auto，其余默认从 0 起。颜色由渲染器语义序列决定，
wire 不接受任意色值。

### file

`files` 为 1–20 项；每项包含 `name`，可选 `size`、`mime`、`status`、`path`、`url`。
URL 校验规则同 source；没有安全 URL 时只展示元数据，不渲染假下载动作。

`path` 是仓库相对路径（POSIX 风格，相对运行绑定的仓库根，如
`notes/proposed/feature/2026-09-13-x.md`），用于站内打开：文件行渲染「打开」动作，在右侧抽屉里
按 Markdown 渲染文件内容（只读）。路径由服务端在「运行绑定的仓库根 + 同一仓库的 git worktree」
集合内解析，前端不拼宿主路径。`file://`、`data:`、`blob:` 等本地与协议 URL 一律被安全白名单
丢弃：浏览器禁止从 http 页面跳转本地文件，界面也不读宿主文件系统。

历史消息兼容：`name` 含路径分隔符但没有 `path` 时，界面把 `name` 当作仓库相对路径尝试打开；
解析不到就退回元数据-only，并在抽屉里说明「找不到该文件」，不猜路径。

### event

包含 `title` 与 ISO 日期/时间 `start`；可选 `end`、`location`、`description`、`url`。
无法解析的日期保留原始安全文本，不猜时区；没有安全 URL 时不渲染外部动作。

### image

`images` 为 1–8 项；每项包含安全的 http(s)/站内 `src` 与非空 `alt`，可选
`caption`。不接受 data/blob/file/javascript URL，不把 SVG 当作可信图片载荷。

### audio

`tracks` 为 1–8 项；每项包含安全 `src` 与 `title`，可选 `duration`。播放器禁止
autoplay，使用浏览器原生 controls 与 metadata 预载；单条加载失败不影响其他正文。

### map

包含 `location`，可选合法经纬度、静态 `image_url` 与外部 `url`。没有地图图片时展示
地点和坐标，不伪造地图；不使用任意 iframe/embed。

### search

可选 `query`，`results` 为 1–12 项；每项包含 `title` 与安全 `url`，可选 `snippet`、
`source`。结果使用真实链接和纯文本摘要，不接受 HTML 高亮片段。

### rating

包含 `question`，可选 `low_label`、`high_label`；固定 5 档、使用真实 radio 语义。
当前选择只保存在页面本地并明确说明，不伪造已上传反馈。

### review-summary

评审摘要是结构化 code review / verification 结果，不是普通 Markdown 的自动升级格式。
包含 `verdict=passed|passed_with_warnings|changes_requested|blocked|inconclusive` 与必填 `summary`；可选
`stats`（`files`、`findings`、`passed` 均为有限非负整数）、`findings` 和 `checks`。解析后
`stats.findings` 与 `stats.passed` 分别以实际保留的问题数和通过检查数为准，避免超限或坏项
被过滤后统计与可见内容不一致。

每条 finding 不要求 `id`，但必须有 `severity=critical|high|medium|low|info` 与 `title`，可选安全文本
`detail`、`file`、`line`、`evidence`、`suggestion`、`url`；每条 check 必须有 `label` 与
`status=passed|failed|warning|skipped|running`，可选 `detail`、`command`（不要求 `id`）。可选
`next_steps` 为 `{ label, detail? }`。解析器最多保留 30 条 findings、20 条 checks、12 条
next_steps，文本字段使用统一长度上限，`line` 只接受有限非负整数；findings/checks/next_steps
至少有一项有效内容，否则整个摘要块回落原始内容。
文件名仅作为展示文本，URL 必须通过通用安全 URL 白名单；不得把 file 自动解释为本地
路径或可执行动作。findings/checks/next_steps 均无有效项时，摘要块丢弃并回落原始内容。

渲染器使用独立的 review-summary 卡和状态文字，不从模型数据生成 CSS 类、HTML、事件
处理器或链接属性；单条 finding 渲染失败时保留其余摘要内容，整个 block 失败时使用
ContentBlock 的通用 fallback。

## 交付型文档

用户要的产出本身就是一份文档时（需求规格、PRD、方案、评审报告、notes 决策记录），正文 md 与文件卡
都是合法交付面，二者任选，也可以并存：

- **写在消息正文的 Markdown 就算交付**：正文本来就是 Markdown 渲染面，文档直接写在里面即可，长文档
  不受正文精简纪律约束。
- **落盘的文档用带 `path` 的 `file` 块指向**：文件行渲染「打开」，用户在抽屉里读渲染后的 Markdown；
  `file` 块必须给出可解析的仓库相对路径（`path`，并让 `name` 与之一致）。按仓库约定留痕的文档（例如
  notes 决策记录）应当用这条路径交付。
- 两者可以并存：既留痕又让读者在对话里直接读。取舍看读者要在哪读，不改变「正文精简纪律只约束解释性
  正文」这一点。
- 唯一不成立的交付是**只给一个打不开的指针**：只给文件名、只给 `file://` URL、或指向不在可读集合里的
  位置，等于没交付。
- 表格、指标、评审结论照旧走对应 block；文档内的表格可以是普通 Markdown。

## 领域模板

货币、天气、股票和比分不新增 wire type，由 `content-block-templates.ts` 组合上述通用
原语：currency→metric、weather→metric+table、stock→metric+chart、score→metric；
rating 使用通用交互块。模板输出仍是普通 `languagegui/v1` 文档。

## 负向保证

- 不接受 HTML、脚本、任意 React 组件名、className、style 或 SVG。
- `review-summary` 只接受固定 verdict/severity/check-status 枚举、有限统计和受限文本；
  普通 Markdown、代码 fence 或带有“评审”字样的文本不会自动升级为该 block。
- 不接受 NaN/Infinity、超限数组或任意嵌套对象作为表格单元格。
- 不按自然语言自动推断 block 类型。
- 无效 block 局部丢弃；整个文档无有效 block 时才回落源码。
- 站内文件预览只读：只接受仓库相对路径，覆盖范围限于运行绑定的仓库根与同一仓库的 git worktree；
  绝对路径、`..` 逃逸、符号链接穿透与 `.git` 内部文件一律拒绝，也不提供目录列举与搜索。
- 预览只返回文本内容：非法 UTF-8 或含 NUL 的二进制文件、超过大小上限的文件都拒绝，返回可读的
  problem 而不是截断内容；响应里不出现宿主绝对路径。
- 预览内容按不可信文本渲染，与聊天正文同一套 Markdown 管线：不执行 HTML/脚本，不因文件内容生成
  链接属性、组件名或样式。
