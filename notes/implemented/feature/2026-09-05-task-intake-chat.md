# 发布前通过对话澄清任务

Status: implemented

## 决策与理由

用户从侧边栏“任务对话”直接进入 `/task-chat` 独立页面描述想法，由模型追问目标、背景、范围、约束和可验证的验收标准。正文复用 `web/src/components/chat/agent-output.tsx`，用户消息和输入区沿用既有 LanguageGUI 对话样式。对话完成后显示可编辑任务草案；用户点击“确认发布”才调用现有 `createWorkItem`，创建 `record_kind=task,status=todo` 的总任务。

澄清请求不创建 WorkItem、Goal、Run 或 Coordinator，也不进入任务调度。发送消息与发布使用两个独立 API 操作；任何模型回复，包括“已确认”“开始执行”，都没有发布权限。继续讨论会使先前待确认草案失效，发布失败保留内容并使用同一请求身份重试；成功后留在对话页、保存任务链接与本轮对话，移除发布资格。开启新对话才清理当前记录。未发布对话草稿按 workspace 隔离并保留，避免离开页面或刷新丢失需求。

## 源码导航

- `web/src/pages/task-chat.page.tsx`：独立任务对话页、正文问答、草案确认与发布结果。
- `web/src/pages/tasks.page.tsx`：任务看板新建入口，跳转到任务对话。
- `web/src/App.tsx`、`web/src/components/layout-shell.tsx` 与 `web/src/utils/route-layout.ts`：常驻侧栏入口、深链路由与全高对话阅读面。
- `web/DESIGN.md` 与 `docs/frontend/chat-rendering-spec.md`：共享正文和任务皮肤约束。
- `web/src/api/endpoints.ts`：现有 createWorkItem 发布契约。
- `internal/httpapi/handlers_task_intake.go`：澄清 API 的权限、工作区与模型配置读取、输入校验与安全错误投影。
- `internal/taskintake/client.go`：仅有文本模型 HTTP 请求的客户端，支持注册表已有的 Responses 和 Chat Completions 协议；无 Store、Dispatcher 或 Adapter 执行能力。

## 接口与配置

`POST /api/v1/workspaces/{workspace_id}/task-intake/analyze` 接收 `messages: [{role: user|assistant, content: string}]` 和可选 `draft: {title, description, acceptance_criteria: string[]}`；返回 `reply`、`questions` 和可为 null 的 `draft`。模型输出始终是建议，不是发布命令。默认读取工作区已保存的 Coordinator 模型引用；用户也可在“分析模型”中选择注册表里支持文本 HTTP 调用的模型，通过可选 `model_ref` 传入。凭据复用已有服务，配置不可用时显示明确错误，不自动换用另一模型。分析模型选择不修改 Coordinator 或最终任务的执行配置。

分析按单轮非流式请求返回，等待期间只显示分析状态；不制造虚假逐字流式事件。对话和草案暂存于当前浏览器，按工作区分键，不是跨设备共享记录。最终发布仍使用原 WorkItem 端点与 `client_key` 实体幂等；遇到发布结果不确定时冻结原内容并沿用原请求身份重试，防止重复创建。

## 放弃了什么

不先创建 todo 任务来保存讨论，因为已有任务发布语义会自动执行，无法满足用户确认前不启动任务。不通过前端固定问答冒充模型分析，也不根据对话关键词或模型自行判断自动发布。不复制 Agent 正文渲染树，不增设任务状态机或第二套任务调度存储。

## 正文问答与排版修正

用户反馈初版正文无法点选、多个编号问题挤在一段。原因是只复用了 `AgentOutput(reply)` 的阅读样式，没有可提交答案的问答协议；既有 ContentBlocks 的评分控件只维护本地星级，不能充当需求澄清交互。

分析响应增加可选 `questions: [{id, title, options: string[], multiple: boolean}]`，每轮最多 3 题、每题 2–5 个选项。模型输出简短 Markdown 引导，问题由独立 UI 控件按题垂直排列，不从文本或编号正则猜测选项。每题可单选或多选，始终提供自由补充；选择本身不请求模型、不发布任务，只有“提交回答”才把答案组织成用户消息继续分析。存在待答问题时不展示可发布草案。

问题、选择和补充按消息与工作区保存；提交回答或另发自由消息后，旧问题成为只读历史，不能重复提交或覆盖新草案。发给模型的历史包含原问题与选项，避免界面有问题但模型回放只剩引导语。最终发布继续由原来的确认按钮控制。

欢迎说明只在空态出现，分析模型和重新开始收进紧凑工具区，把阅读空间留给用户消息、分析正文和问题卡。保留现有语义 token、焦点与键盘操作，正文渲染不分叉。

## 验收

- 多轮真实模型澄清能生成目标、范围、约束和验收标准清晰的草案。
- 打开入口、发送消息、生成草案均不触发任务创建；只有确认发布进入原执行队列。
- 继续澄清、错误重试、关闭重开、刷新及 workspace 切换不导致误发或串稿。
- 前端 `tsc -b`、测试、lint 和浏览器真实交互；后端 build、vet 和触面 race 测试。
- 实现与验证在隔离工作树中完成，按用户明确指示合入主线。


## 初版验收结果（正文问答修正前）

- 前端 `pnpm typecheck`、`pnpm lint`、全量 `pnpm test`、`pnpm build` 通过；新状态与恢复分支有针对性断言。
- 后端 `go build ./...`、`go vet ./...` 通过；`internal/httpapi` 完整 race 测试通过（223.679s），最终 task-intake 客户端和 HTTP 触面 race 再次通过；gofmt 与 diff whitespace 干净。
- 真实 Kimi 首轮追问，多轮澄清后返回完整、可编辑草案。单独重放正式分析接口也返回 10 条验收标准，前后 8 张控制面表计数相同。
- 浏览器验证：修改标题、关闭重开、刷新后，标题、历史、未发送输入和分析模型均保留；空标题阻止发布；发送 8001 字符被本地拒绝，原文保留且历史和网络请求均未新增；取消重新开始保留原文，确认才清除。
- 浏览器实际点击确认发布才使隔离库任务数 3 → 4、Coordinator state 数 0 → 1。模拟服务端创建成功但客户端丢失响应，刷新后重试返回 HTTP 200、`Idempotent-Replayed: true`、同一任务 ID、同一 payload 与 client_key；任务总数仍为 4，成功后草稿清空。
- 验证使用独立 SQLite 与 Runtime home；隔离 Coordinator 的原生 Runtime 未配置，发布任务入队后按已有机制显示 Runtime 阻塞，未声称执行 Worker 或完成任务。原用户 8080 服务、主库和 main 原有未提交内容均未改动。
- 对话暂存于当前浏览器，按工作区隔离；不提供跨浏览器同步。分析使用已注册、支持直接文本 HTTP 调用的模型；其配置独立选择不会修改任务执行配置。


## 正文问答修正验收

- `AgentOutput` 继续渲染模型引导正文，`task-intake-question-card.tsx` 渲染明确的单选、多选和逐题自由补充；欢迎说明仅空态显示，模型选择与重新开始收在 composer 工具行。
- 真实 Kimi 返回单选场景题、多选功能题及后续导出范围题。浏览器实际选择一个场景和两个功能，模型请求数量不增加；选择、补充和底部未发送内容在刷新后均保留。
- 显式“提交回答”发送完整题目和选中值，模型历史包含问题、选项及回答方式；选项提交不会夹带底部未发送内容。已答题锁为只读，后续追问结束后生成可编辑草案，保留独立“确认发布”。
- 在提交回答的传输等待阶段刷新，未发送 composer 与 pendingAnalysis 仍保留；重试只继续同一份答案，不重复追加用户消息。
- 回归断言覆盖：零操作旧题被自由消息取代后不能再选择/提交覆盖新草案；底部草稿从请求发起时即保持；编辑后的草案通过独立 analysisContextDraft 跨问题轮次与刷新保留，但该上下文不获得发布资格。
- 4 次真实浏览器请求全部进入 task-intake/analyze，无 work-items 发布请求。隔离库仍为 3 条种子任务，Run、Coordinator state、Dispatch、Plan、Wakeup、Coordinator config 均为 0。
- 最终前端全量 104 个测试文件、861 条测试通过，typecheck、lint 与生产 build 通过。Go build/vet 通过，HTTP 包完整 race 通过（228.292s），最终文本客户端 race 通过。1024×700 视窗未出现横向溢出。
- 本轮截图及非敏感交互证据保存在当前任务的可视化工件目录；临时服务与凭据副本在交付前清理，主树与 main 保持原状。


## 侧栏独立页面定位

用户已明确：“我要的是在侧边栏直接打开对话，并且交互还可以在对话里提交任务”。因此入口调整为侧边栏常驻“任务对话”，路由 `/task-chat`；页面打开时直接显示对话、正文选项和输入区，不依赖点击新建任务，也不使用 Drawer 遮罩。看板原新建按钮保留为同页导航快捷入口。

发布在对话正文中确认，成功后保持当前页面与本轮对话，显示静态“已发布”结果和 `/tasks/:id` 链接。该结果不是实时执行状态，不借用缓存 status 宣称任务正在运行。原草案失去再次发布资格，开始下一任务时显式开启新对话；pendingPublication 未确定前仍禁止清除或重发不同内容。旧创建 Drawer 及其兼容入口删除，问题组件和分析/发布 API 继续复用。


## 独立页面交付验证

2026-09-06 合并前复验：前端 105 个测试文件、870 条测试、生产构建与 lint 通过；后端 build、vet 及 task-intake/httpapi 触面 race 通过。实际浏览器已验证侧栏导航、`/task-chat` 深链刷新、看板新建快捷入口；正文确认发布返回 201，页面保持 `/task-chat`，本轮消息和发布结果在刷新及查看任务后仍保留，同一草案没有再次发布入口。开发 owner 工件在交付清理提交中删除，设计与验证结论保留于本记录。
