# 以任务为基准的 Plane 风任务工作区

Status: implemented

## 决策与理由

任务工作区采用 [makeplane/plane](https://github.com/makeplane/plane) 的信息层级：列表或看板上的一级对象只有用户发布的总 Task（无 `parent_id`），详情页先展示任务标题、描述、子任务与属性，再在任务内部展示执行 Agent。Coordinator 派生的子任务无论处于进行中还是完成态，都只属于总任务详情，不能占据看板状态列。

列表不再并列展示复审队列、决策、产物、Coordinator 状态或执行锁；计数、搜索、看板和列表统一只匹配根 Task 集合。详情页不再把 Coordinator、Goal/Todo/Plan、派发批次、技术日志、交付简报和运行产物抬成与任务平级的面板。控制面和治理数据仍由原 store/API 保留，本次只收敛用户可见投影。

Agent 执行按 run 平铺。每个 Agent 展开后严格只有“输入”和“最终输出”两块：输入来自 `run.created.data.instruction`；最终输出只在 Run 落 `succeeded` 后取 `agent_id=main` 的最后一条非 plan assistant `message.completed`，并保留 canonical ContentBlocks。运行中、失败或取消的阶段性说明不会冒充最终结果。折叠行只预取轻量 Run 快照，使实时状态保持新鲜；完整事件历史仍延迟到展开后加载。

## 放弃了什么

- **在 Task 页面复用完整 Chat transcript**：它会带回思考、工具、阶段性说明和技术时间线，违背“只看输入与最终输出”。Chat 页继续保留完整过程。
- **按 Dispatch 批次分组 Agent**：批次是执行元模型，不是用户追踪目标；详情只按 Agent run 展开，批次仍留在后端读模型。
- **列表内展示 Coordinator 接取/执行文案**：任务状态列已经表达用户需要的进度，额外执行层状态会造成双主语。
- **改后端 WorkItem/Run 协议**：现有事件历史足以确定输入与最终输出，无需新增第二套结果字段。

## 负向保证

- Task 列表只显示 `record_kind=task` 且没有 `parent_id` 的总任务及其任务属性，不显示 Chat、派生子任务、决策、产物或运行时对象；`cancelled` 根任务仍是可追踪的任务列，不会被过滤消失。
- 非 `succeeded` Run 永不展示所谓“最终输出”；最后一条阶段性 `message.completed` 也不会在运行中被误认成最终结果。
- 本改动不改变 Run 状态机、Coordinator 权威、派发存储、审批协议或任何后端写入语义。
