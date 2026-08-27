# LanguageGUI 工作流只读投影

Status: implemented

## 决策与理由

生产对话页把会话目标、Plan 模式方案正文和本轮执行步骤收敛为一个只读的
LanguageGUI 工作流区域。目标始终作为摘要卡展示；执行步骤使用带序号、状态、
进度和连接线的 Action 卡；方案正文作为同一区域内可折叠的「方案草稿」展示。
三者继续消费真实运行数据，不引入 demo JSON 或前端伪状态。

这一投影借用 LanguageGUI multi-prompt 的视觉原语，但不把 Workbench 的 Goal、
Turn Plan 宣称为 LanguageGUI 官方组件。运行中的步骤状态必须同时用图标和文字
表达，DOM 顺序即执行顺序。

## 放弃了什么

不照搬 multi-prompt 设计稿中的设置、删除和新增 Action 控件。那些控件表达可编辑
工作流，而当前 Chat 只有只读运行状态；展示不可执行的按钮会形成错误 affordance。
也不再并排堆叠三个独立 Dock，因为它们会重复表达同一个任务上下文，并持续挤压
输入区。

## 复活条件

【后端提供可版本化的计划编辑、重排和删除命令，并定义运行中修改的并发语义】
→ 把工作流投影升级为编辑器 → 复用现有 Action 卡 DOM，补真实 mutation、失败回滚
与审批路径后才开放编辑控件。
