# Task intake backend owner

Status: proposed

## 决策与理由

发布前需求澄清采用无状态 `POST /api/v1/workspaces/{workspace_id}/task-intake/analyze`。客户端每次携带完整的 `messages` 与可编辑 `draft`；服务端只调用模型注册表中明确声明 endpoint 的 `openai-completions` 或 `openai-responses` 文本接口，返回 `reply` 与可选 `draft`。请求带对应协议的 JSON Schema 约束（Completions `response_format.json_schema`、Responses `text.format`）；provider 即使附带无权威根字段也只在内部投影丢弃，HTTP 响应严格只输出 `reply/draft`。凭据只从 `CredentialsStore` 或已声明的环境变量取用，模型原始错误和密钥不进入响应；`draft` 内部仍按严格 JSON 与边界校验，未完成草案不返回给确认发布 UI。

分析 handler 不调用 `CreateWorkItem`、`CreateRun`、`StartCoordinator`、`EnsureConfig`、Dispatcher 或调度器。`model_ref` 显式选择优先；省略时只读取已经存在的 Coordinator `ModelRef.Ref`，或使用唯一精确匹配的 provider/model registry 条目。原生登录 runtime、缺 endpoint、缺凭据和不明确的 model mapping 都 fail closed。

## 放弃了什么

不借用普通 Chat 的 `record_kind=chat` + Run API，因为 `CreateRun` 必须落库并经 Dispatcher，无法满足用户确认前不创建/执行任务。不用伪 Run 驱动现有 `ModuleRunner`，因为 AdapterModule 依赖 Run 状态、事件、会话和 EngineSink，包装它会形成第二条运行轨。不自动挑选 registry 第一模型，也不把 `EnsureConfig` 当读取 API，避免分析请求隐式创建系统 Coordinator 或使用错误模型。Schema 约束是 provider 协议层的提示与约束，不取代服务端边界：provider 根字段只投影已知语义，draft 仍严格解析和校验，任何模型回复都没有发布权限。

## 复活条件

若未来需要可恢复的长期需求讨论，新增独立、审计清晰的 intake 持久化模型与状态机，并在设计中重新审查配额、权限和过期草案；当前 stateless 契约不应悄然扩展为任务 Run。

## 结构化澄清问题

当关键缺口适合有限选择时，分析响应同时返回 `questions`，每题包含稳定 `id`、简短 `title`、2–5 个唯一 `options` 和 `multiple`；`questions` 非空时服务端强制不返回可发布 `draft`。问题答案由前端序列化为下一条普通用户消息，服务端不从自由文本猜选项。问题数量限制为 1–3 题，字段与选项均有长度和唯一性校验，并保留“暂不确定”作为模型可提供的选项。含结构化问题时 `reply` 限制为 1000 字符，保证题目序列化进下一轮消息后仍能回放。

provider 请求使用严格 JSON Schema，HTTP 响应只投影 `reply/questions/draft` 三个有权威语义的字段；根级展示字段可被丢弃，`draft` 和 `questions` 内部仍严格解析。任何模型输出都不具备发布权限，发布继续由用户单独确认后走原 Task 创建接口。


## 最终交付状态

实现与验收完成，等待用户决定合并；合并后清除此 owner 工作工件。最终决策与验收事实见 `notes/implemented/feature/2026-09-05-task-intake-chat.md`。root 已完成真实 UI 发送、草案编辑与刷新恢复、确认发布、丢失响应后同任务重放、超限输入与清除确认验收；1024×700 无横向溢出。临时 18080/15173 服务已停止、端口释放，临时凭据副本和数据库已清除；截图与非敏感验收 JSON 保存在本轮可视化工件目录。


## 正文问答收口

正文单选、多选、逐题补充、历史题失效、pending composer 保存及分析草案上下文已完成并验收。最终前端 104 文件/861 测试与 build/lint 通过，后端 build/vet/race 通过；真实浏览器 4 次分析请求均未发布任务。临时 questions 环境、服务和凭据副本均已清理，等用户明确合并指示。
