# Task intake conversation 前端 owner 状态

Status: proposed

## 负责范围

前端 owner 负责任务对话页、task-intake 分析 API 封装、未发布 intake 状态存储及触面测试。实现留在 `web/src/pages/task-chat.page.tsx`、`web/src/components/chat/task-intake-question-card.tsx`、`web/src/api/task-intake.ts`、`web/src/stores/task-intake.store.ts` 与对应测试中；任务看板入口由页面 owner 接入 `/task-chat`。

## 交互决定

任务对话页展示空的对话轨和 composer。用户消息发送到独立的 stateless analyze 请求，助手正文复用 `AgentOutput`，并在服务端明示 `questions` 时渲染真实单选/多选问题卡；返回草案后才显示可编辑的标题、描述和逐行验收标准。只有「确认发布」才调用现有 `createWorkItem`，并携带 `record_kind=task`、`status=todo` 和验收契约。发布成功后保留本轮消息，在正文显示任务回执与详情链接；开始下一轮使用显式确认清除。

继续对话会在发送前清掉当前草案及其发布幂等身份；发布失败保留同一草案和 `client_key`，同一 payload 的再次发布复用该 key，编辑后的 payload 生成新 key。消息、草案和发布选项按 workspace key 持久化，切换 workspace 时通过 generation fencing 丢弃旧分析响应。

## 待后端确认

当前按 `POST /workspaces/{id}/task-intake/analyze`、请求 `{messages, draft?}`、响应 `{reply, draft?}` 实现。`acceptance_criteria` 按现有 WorkItem 契约使用 `string[]`；若后端最终字段或错误 envelope 变化，只调整 API 边界解析，不把分析结果直接当作发布授权。


## 最终交付状态

实现与验收完成，等待用户决定合并；合并后清除此 owner 工作工件。最终决策与验收事实见 `notes/implemented/feature/2026-09-05-task-intake-chat.md`。root 已完成真实 UI 发送、草案编辑与刷新恢复、确认发布、丢失响应后同任务重放、超限输入与清除确认验收；1024×700 无横向溢出。临时 18080/15173 服务已停止、端口释放，临时凭据副本和数据库已清除；截图与非敏感验收 JSON 保存在本轮可视化工件目录。


## 正文问答收口

正文单选、多选、逐题补充、历史题失效、pending composer 保存及分析草案上下文已完成并验收。最终前端 104 文件/861 测试与 build/lint 通过，后端 build/vet/race 通过；真实浏览器 4 次分析请求均未发布任务。临时 questions 环境、服务和凭据副本均已清理，等用户明确合并指示。
