# 长 Run 正文保留与工具终态收口

Status: implemented

## 决策与理由

前端事件容量只限制高频帧数量，不能删除用户已经看到的阶段正文。超过容量时，reasoning 与 text
分别聚合到对应的 `tool.started` / `message.completed` 边界：reasoning 允许显式尾部截断，interim
text 以完整阶段文本粘滞保留。最后一个 completed 的当前 text 阶段是 final candidate；累计 canonical
text 只剥离已完整投影的 interim 前缀，不使用 Markdown 标题或语言内容判断 final。

历史回放与 Workspace SSE 可能携带同一批 `run_seq`。前端按 run 维护已见事件 identity：重复事件既不
再次写 timeline，也不再次进入 reasoning/text 折叠缓冲，避免长 Run 因数百次等价 Zustand 更新反复
执行 Markdown/Transcript 投影。未知 run 的快照 GET 同时采用 single-flight，同一时刻只允许一个请求。
若终态在旧快照请求期间到达，SSE 终态先保护本地状态不被旧 `running` 响应倒退；在途请求结束后再排
一次权威快照刷新，补齐最终用量与时间字段。

Kimi KAP 的 `turn.ended` 可能先于后台 Agent 工具的 `tool.result`。Adapter 按 `toolCallId` 跟踪 pending
调用；真实 result 只允许产生一次 terminal。parent turn 结束时仍 pending 的调用产生一次
`tool.failed`，并以 `status=interrupted` / `failure_reason=turn_ended_before_tool_result` 表达“未观察到结果”，
但不改变成功 Run 的 Outcome。

## 放弃了什么

- 不提高 `TIMELINE_CAP` 掩盖正文淘汰；事件再多仍会复发并增加浏览器内存。
- 不从 `# Summary` 等 Markdown 标题猜 final；模型语言和标题结构不是协议边界。
- 不把未返回结果的后台 Agent 冒充 `tool.completed`；父 turn 成功不等于该工具已产生可观察结果。
- 不在 ModuleRunner 统一补工具 terminal；只有 adapter 掌握 provider call_id 与真实事件生命周期。

## 验证

- 真实 `run_01M149T6D0T4AT43FAHKPVMAGC`：1325 个事件冷启动约 1.8 秒，默认折叠摘要为
  `11 段思考 · 82 次工具 · 9 段过程正文`；展开后得到 30 个按序阶段、10 个独立工具批次。
- final 独立正文以 `Now I have a thorough understanding...` 开始，不再包含首段过程正文前缀。
- 前端全量测试、TypeScript、ESLint、生产构建通过；Kimi adapter race 测试及 Go build/vet 通过。
