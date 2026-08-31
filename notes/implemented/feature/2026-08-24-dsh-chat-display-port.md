# 对话页展示层移植 DSH：工具卡片族 + 披露行 chrome + 流式输出接入

Status: implemented

## 背景与决策

对话页的信息输出此前是「所有工具输出一律等宽 pre」，用户评价"乱"。对照同机
deepseek-harness（DSH）web 端的展示层（ui-primitives / ui-tool / ui-conversation），
按内容类型分卡片移植，全部落在 `web/src/components/chat/blocks/` + `tool-card.tsx` 重写：

1. **blocks 基座**：StateDot（done/warning/ongoing 像素追逐/error 四态点）、
   DisclosureRow（24px 披露行：图标↔chevron 悬停替换、整行点击、Enter/Space）、
   ansi（SGR→CSS span，含 256 色/truecolor/光标回放）、head-tail-cap（头 ceil(n/2)+尾
   高度帽算术）、clipboard/use-copy-feedback（复制+1s 反馈）、cx。几何字面值照抄 DSH，
   颜色一律映射到既有 `hsl(var(--color-*))` 通道（不引 --dsw-* token 体系）。
2. **工具卡片族**（tool-model.ts 按工具名分族：bash/read/write/edit/search/code/others）：
   - bash → TerminalBlock：prompt 行（状态点+cwd 末段+多行命令逐行）+ ANSI 上色输出
     + 退出码/信号胶囊 + 复制 + 头尾折叠「… 其余 N 行」+ 横向滚动不软换行 + 无输出占位。
   - write/edit → 输出像 unified diff 时走既有 DiffCard（已升级：复制按钮、unified 视图
     16 行头尾帽、`└ +N −M · N 个文件` footer；split 视图保留不戴帽）。
   - read → ReadBlock：文件行号 gutter + highlight.js 整段高亮（异步、失败回落纯文本）
     + 路径/语言横幅 + 复制。
   - search → SearchBlock：grep 输出 `path:line:content` 解析为按文件分组（组可折叠、
     头尾帽 tail 落组中间时补组头），路径列表（glob 形态）单列；两个解析器都有防误判
     门槛（≥2 行命中且 ≥60% 非空行占比 / 全行像路径），判不出回落通用卡。
   - 其余 → IN/OUT 通用卡（IN=args pretty JSON，OUT=输出，错误红字，各自独立限高滚动）。
3. **ToolRow chrome**：族标题 + 圆点 + FILL 截断摘要 + 耗时徽章；错误行折叠摘要 =
   失败首行红字；running 行扫光动画；任意终态 run 里仍 running 的挂起行按 stopped
   （琥珀点、无扫光）——对齐 DSH 的 interruption 投影，同时兜住历史数据缺 completed
   帧的永久扫光问题。
4. **数据面**：canonical 契约 `tool.started` 新增 `args`（≤2000 字符截断的参数 JSON
   原文透传、键序不重排、非对象/空不携带；kimiapp/dsh/codexapp 三适配器 + 测试；
   scripted 是纯回放不改）。前端 store 新增 `argsSummary/args/exitCode/liveOutput`
   字段；**接入此前被丢弃的 `tool.progress`**（codex shell 流式输出），liveOutput
   上限 4000 截尾保留最新，落定后由 detail 承接并清除。
5. **消息操作行**：assistant/user 消息悬停浮现 复制+时间戳（group-hover，不悬停不占视觉）。

## 放弃了什么

1. **DSH 的自研 Markdown 管线**（shiki/增量解析/katex）：我们的 react-markdown +
   highlight.js CodeBlock（横幅+复制+换行开关）能力相当，替换成本高收益低。
2. **ToolCallTree 嵌套子调用树**：我们的 canonical 事件没有父子调用关系，无可渲染数据。
3. **WebBlock 引用卡**：我们的 web_fetch/web_search 输出是纯文本，无结构化引用数据；
   web_fetch 归 read 族、web_search 归 search 族走现有解析。
4. **StatsLine 会话级统计条**：需要 DSH 的 token-meter/session-stats 投影层；我们 composer
   下已有 usage 提示，不重复建设。
5. **Inspect 跳轨迹视图**：我们没有 trajectory 视图可跳。
6. **diff split 视图戴头尾帽**：split 行与 unified 行计数语义不同，帽子只切 unified
   （注释留痕）；复制始终复制 unified 文本形态。
7. **ansi.ts 不引 anser 依赖**：SGR→run 折叠自写，颜色表对照 anser 2.3.5 源码移植。

## 验证

- 前端：tsc 零错、vitest 219 全绿（新增 blocks/tool-model/search-parse/read-model/
  terminal-block/diff 增补用例）、eslint 零 error（3 个存量 warning 不动）。
- 后端：`go build ./... && go vet` + `go test -race ./internal/runtime/adapters/...` 8 包全绿。
- 浏览器实测（vite dev + 8081 控制面）：Bash 行展开终端卡、ANSI 列对齐输出、
  「… 其余 14 行」展开/收起、复制按钮、Think 折叠、消息悬停操作浮现均正常。
- 环境修障：本机 workbench.db 台账漂移（no such column: parent_id），已跑
  `go run ./cmd/migrate -dsn "sqlite://workbench.db"` 应用 0009–0012 修复。
