# Codex 对齐第二刀：正文展示精修 + 发送消息队列 + 分叉对话

Status: implemented

## 决策与理由

### 正文展示（依据 codex-desktop-markdown-tags-inventory.md §6 低成本高价值清单）

- `.chat-markdown` 逐标签精修：标题分级（24/20/17/15px、600、20/10 margin）、引用透明底
  + 左 4px 竖条、列表嵌套子弹 disc→circle→square、任务列表 grid 两列、行内码
  `box-decoration-break:clone`；CJK 连续段落段距加大（`data-markdown-han-text`，
  `/\p{Script=Han}/u` 检测）。
- 表格包 `TableCard`：横向滚动 + 悬停复制（DOM→TSV）。
- Markdown 整树包 ErrorBoundary（resetKey=text），崩溃回落纯文本 pre 不炸页。
- 流式块级淡入（li/tr/blockquote/hr，含 prefers-reduced-motion 禁用）。
- **行内 code 的原 utility 类删除**：与 CSS 规格冲突（4px≠6px、底色不同），外观全部由
  CSS 接管；顺带修掉 react-markdown v10 `node` prop 泄到 DOM 的既有隐患。

### 消息队列（纯前端 v1）

- **steering 语义变更**：run 活跃时 send 一律入队，不再调 `sendRunInput`；前端
  endpoints.ts 的 sendRunInput 包装删除（服务端 commands/input 能力保留，将来做
  「立即发送」时再加一行）。sending 在途时也入队而非丢弃。
- 自动续发只在 `succeeded` 边沿触发（drainedEdgeRef 按 run id 去重，防 drain 失败
  后 sending 复位导致的原地重试风暴）；failed/cancelled/lost/interrupted 暂停，
  队列条出「继续发送」手动 drain。
- 终态发送保 FIFO：队非空时新文本先入队再出队首条。
- **内存级不落盘**：刷新丢失，注释留痕；服务端持久化队列是后续项。

### 分叉对话（零 schema 改动）

- 分叉 = `createWorkItem(parent_id=源会话, description=上下文包)`，复用既有字段；
  上下文包由 buildForkContext 截取锚点消息之前的 transcript（工具行折叠一行标记，
  ≤4000 字符保头截断）。
- 分叉首发注入上下文包（runs.length===0 且 description 带标记时），第二轮起不再注入。
- 入口在 assistant 消息悬停操作行（GitBranch 图标）；会话列表 parent_id 项带分叉图标。

## 放弃了什么

1. **服务端队列持久化**：v1 内存级（Codex 有服务端队列；我们要做需在控制面加会话级
   队列模型，超本任务面）。
2. **session 级真 fork**（agent 会话状态在分叉点回放）：需要协议层检查点，不支持；
   上下文包注入是务实的退化形态。
3. **drain 失败滚回队列**：失败仅 toast（与原 send 失败语义一致）；滚回会引入
   「边沿已消费+重试」的组合复杂度。
4. **行内码 `@路径`→文件引用 chip 的智能升级**：我们的数据源没有 cwd/文件解析上下文，
   且需打开文件的主机能力，web 端不做。

## 验证

- 测试：51/51（chat.store，含队列 FIFO/drain/边沿去重、buildForkContext、
  forkConversation action 直连）；全仓 239 通过；tsc -b 零错；lint 零 error；build 通过。
- 浏览器实测（vite dev + 8081）：队列全链路（运行中入队 → 队列条显示/可删 → run
  成功后自动出队续发 → 第二轮回复渲染）通过；fork 的悬停按钮在本机 IAB 浏览器
  自动化 harness 下无法被合成事件激活（opacity-0 悬停件的已知 harness 限制；
  同组件的复制按钮同样点不动），已用 store 级测试补位，人工点击请自验。
