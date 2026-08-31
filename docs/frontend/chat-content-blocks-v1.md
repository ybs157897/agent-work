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

### chart

`chart=bar|line`；`labels` 为 1–64 项；`series` 为 1–4 组，每组 `values` 数量必须与
labels 相等且全部为有限 number。可选 `unit`、`source`、`y_domain=zero|auto`；行情等
窄幅变化用 auto，其余默认从 0 起。颜色由渲染器语义序列决定，
wire 不接受任意色值。

### file

`files` 为 1–20 项；每项包含 `name`，可选 `size`、`mime`、`status`、`url`。
URL 校验规则同 source；没有安全 URL 时只展示元数据，不渲染假下载动作。

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
