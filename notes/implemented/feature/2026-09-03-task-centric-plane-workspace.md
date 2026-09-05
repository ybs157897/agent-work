# 以任务为基准的 Plane 风任务工作区

Status: implemented

## 决策与理由

任务工作区采用 [makeplane/plane](https://github.com/makeplane/plane) 的信息层级：列表或看板上的一级对象只有用户发布的总 Task（无 `parent_id`）。“进行中”只承载执行阶段，总任务进入 `review/acceptance` 且服务端 `review.ready=true` 后投影到独立“待验收”泳道，但领域状态仍保持 `in_progress`。卡片及列表从完整分页集合的根任务及后代任务归集已分派的非系统 Worker 并显示去重头像，不把 Coordinator 或 evaluation 当成参与者；筛选仅影响根任务可见性，空泳道默认收起。点击总任务使用保留看板上下文的右侧 side-peek，不再进入多层子任务页面；关闭按钮、遮罩、Escape 与浏览器返回共享同一退出语义。

列表不再并列展示复审队列、决策、产物、Coordinator 状态或执行锁；计数、搜索、看板和列表统一只匹配根 Task 集合。详情只直接展开 Worker Agent 的输入与最终输出，不显示子任务列表、系统/委派 Coordinator、evaluation、属性侧栏、编辑或阻塞操作。控制面和治理数据仍由原 store/API 保留，本次只收敛用户可见投影。

Agent 执行按派生 WorkItem 聚合到最新 Worker run，并直接展示“输入”和“最终输出”两块：输入来自 `run.created.data.instruction`；最终输出只在 Run 落 `succeeded` 后取 `agent_id=main` 的最后一条非 plan assistant `message.completed`，并保留 canonical ContentBlocks。运行中、失败或取消的阶段性说明不会冒充最终结果。Worker/Coordinator/evaluation 角色由服务端受保护的 Coordinator envelope 投影，前端不按名称猜测。

验收只发生在根 Task：详情以服务端 `review.can_accept/can_return` 提供“总任务验收通过 / 打回总任务”；只读投影统一 Coordinator、canonical evidence 和权限门槛，缺少投影时默认不可操作，最终写命令保留完整校验。子任务旧链接先解析 `root_work_item_id`（历史任务才沿 `parent_id` 回溯）并重定向到根任务；服务端根验收继续负责级联收口子任务。详情、创建及打回弹层使用相同 Task 皮肤；最顶层弹层独占交互，矮窗口操作区可达。创建只发布根任务，关闭表单保留草稿；Agent 历史失败显示局部重试而不是永久加载。

## 放弃了什么

- **在 Task 页面复用完整 Chat transcript**：它会带回思考、工具、阶段性说明和技术时间线，违背“只看输入与最终输出”。Chat 页继续保留完整过程。
- **按 Dispatch 批次分组 Agent**：批次是执行元模型，不是用户追踪目标；详情只按派生任务保留最新 Worker run 并直接展示，批次仍留在后端读模型。
- **逐个展开 Agent 或逐个验收子任务**：增加无意义的点击与错误权威；所有 Worker 输入/输出直接展开，最终只验收总任务一次。
- **列表内展示 Coordinator 接取/执行文案**：任务状态列已经表达用户需要的进度，额外执行层状态会造成双主语。
- **新增结果表或改动领域 WorkItem/Run 存储协议**：现有事件历史足以确定输入与最终输出；只在既有 Web Dispatch DTO 上投影受保护的执行角色，不新增第二套结果真相源。

## 负向保证

- Task 列表只显示 `record_kind=task` 且没有 `parent_id` 的总任务及其任务属性，不显示 Chat、派生子任务、决策、产物或运行时对象；`cancelled` 根任务仍是可追踪的任务列，不会被过滤消失。
- “待验收”只是一层确定性 UI 投影；不写回新状态，不让同一任务同时出现在“进行中”和“待验收”。卡片头像只来自根任务或后代任务绑定的非系统 Agent，并按 Agent ID 去重。
- 子任务页面永不暴露验收、打回、编辑或阻塞操作；系统/委派 Coordinator 与 evaluation 永不进入 Agent 输入输出区。
- 非 `succeeded` Run 永不展示所谓“最终输出”；最后一条阶段性 `message.completed` 也不会在运行中被误认成最终结果。
- 本改动不改变 Run 状态机、Coordinator 权威、派发存储、审批协议或任何后端写入语义。
