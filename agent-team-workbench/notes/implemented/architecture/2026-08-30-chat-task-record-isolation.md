# Chat 与 Task 记录及执行边界隔离

Status: implemented

## 决策与理由

`work_items` 增加不可变的 `record_kind` 闭集（`chat | task`），作为存储、查询、
导航与终态副作用的持久化硬门。Chat 首发和分叉显式创建 `chat`；任务看板、seed、
Plan 派生显式创建或继承 `task`。父子记录必须同类，禁止用 parent_id 把 Chat 与 Task
连成一棵树。Chat 列表只查询 chat，Task 看板/bootstrap/search/tree 只查询 task；
URL 直接把 task id 塞进 `/chat` 也必须拒绝。

Session/Run/Adapter 是两种记录共用的执行基础：resume、自愈、task_sessions 锚点、
canonical 事件、工具与正文渲染均保留。会话元模型及任务状态机只属于 Task：
dispatch/@路由、Plan/评估、执行锁、review/acceptance、rolling ledger、decision、
settlement 与任务搜索不得在 Chat 上运行。Chat 维持单 Agent 多轮会话，不读取任何
Task execution session 参与线。

正文渲染拆成纯 `AgentOutput`（Markdown、代码、表格、Callout、LanguageGUI
ContentBlocks 等），不依赖 Chat store 或 Task store。Chat 的消息交互和 Task 的
Agent 时间线分别包在外层；Task 成员/子任务点击在任务详情内打开 Run 正文，不再导航
到 `/chat`。Task 布局仍可继续演进，但所有 Agent 输出必须复用同一正文渲染器。

历史迁移默认 `chat`，保护已经成熟的单聊记录；仅把有确定任务证据的记录回填为
`task`（Plan 根、Plan step 结果、非 fork 的显式父子任务等）。无法可靠判定的已执行
单 Agent 记录不做猜测，保留为 Chat；新数据从入口起即无歧义。

## 已实施的边界

- 存储层：双数据库迁移加入闭集、不变性和父子同类约束；查询、dashboard、搜索与
  wakeup 按 `record_kind` 分域。
- 应用层：Chat Run 只刷新会话排序时间，保留 Run/Session/resume/artifact；所有 Task
  状态机、锁、编排、台账与收口 hook 在进入时校验 `task`。
- 协议层：WorkItem DTO 与相关事件携带 `record_kind`；Chat 调用 Task-only 端点返回
  领域错误，Chat 详情不暴露 rolling digest。
- 前端：Chat 与 Task store 均 fail-closed；Task id 不能从 `/chat` 深链加载；Task 的
  dispatch 成员正文在任务详情内展开，Chat 不合并 Task 的参与线。
- 展示层：`AgentOutput` 是 Agent 正文唯一渲染入口，Chat 与 Task 只分别提供各自允许的
  外层操作。

## 验证证据

- 新增 application 闭环测试覆盖 Chat 不路由、不派发、不推进任务状态/锁，同时保留
  session resume 与 artifact；Task 仍完整执行 dispatch、ledger 与状态推进。
- 新增 migration、sqlstore 与 HTTP 测试覆盖历史回填、非法类型、类型不可变、父子异类、
  列表/详情/搜索/wakeup/Task-only 端点隔离。
- 新增前端测试覆盖 Chat/Task 创建参数、列表与 SSE 分流、Task 深链拒绝、Task 内正文
  展开、Chat 关闭 mentions 和 Task-only decision 操作。
- 当前分支和与最新 `main` 的合成树均通过后端 build/vet/race 定向测试与前端
  typecheck/test/lint/build。

## 放弃了什么

- 不新建 `chat_threads` 并把 execution_runs 改成多态外键：这会同时重写 Run、审批、
  artifact、session 锚点与事件协议；当前需要的是可靠领域隔离，不是复制执行内核。
- 不只在前端筛选：终态 hook、URL 直达和 API 客户端仍会越界，无法满足“不做混”。
- 不默认把历史记录全部当 Task：会吞掉已经可用的 Chat 历史。
- 不从标题、优先级或“是否只有一个 Agent”猜历史类型：这些信号都不是权威事实。
- 不让 Task 查看正文跳转 Chat：导航一旦共用，记录列表和 session 参与线会再次串线。
- 不复制一套 Task Markdown/代码渲染器：正文样式只有一个事实源。

## 复活条件

当 Chat 需要独立于 WorkItem 状态字段的保留/归档/分享生命周期，或 Run 必须同时绑定
Chat 与 Task 两种聚合根时，再拆 `chat_threads` 实体；触发后需迁移 execution_runs 的
宿主外键，但 `record_kind` 仍作为迁移期校验与审计事实保留。
