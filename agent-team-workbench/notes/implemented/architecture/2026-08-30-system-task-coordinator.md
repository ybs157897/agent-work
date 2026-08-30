# 系统级 Task Coordinator

Status: implemented

## 决策与理由

Task 不再由用户选择普通 Agent 后手工启动。系统提供唯一、内置且不可编辑的
Task Coordinator 身份；每个根 Task 创建独立的 Coordinator 实例与持久会话，负责
规划、拆分、选择 Worker、派发、跟踪、失败恢复、重新规划和结果收口。Coordinator
是会话元模型的控制线，不替代 `WorkItem → Plan → Dispatch → Run/Session → Ledger`
持久化底座，也不进入普通 Chat。

创建根 Task 成功后必须立即自动接取并排队启动首个 Coordinator Run。用户不再传
`agent_profile_id`，任务不会静默停留在 todo；若 Coordinator Runtime/模型配置不可用，
Task 进入带明确原因和恢复入口的 blocked 状态。自动接取、首轮入队和可观测事件必须
具备幂等与崩溃恢复语义，不能依赖浏览器继续在线。

Plan/Coordinator 派生的子 WorkItem 不创建第二个 Coordinator；它们继承根 Task 的
控制线并由该 Coordinator 派发 Worker。用户显式新增子任务时只唤醒根 Coordinator
重新规划，禁止 Coordinator 套 Coordinator 或与 worker Run 双重执行。

Coordinator 的提示词、工具协议、输出 schema 和失败策略由源码内置并带版本号，API
拒绝修改；Workspace 只可配置主 Runtime、备用 Runtime、模型和 reasoning effort，底座
支持 Codex/Kimi。普通 Agent 列表、Chat、手工指派和普通 prompt 编辑面均不暴露
Coordinator。

Worker 的 retryable failure 由 Coordinator 控制面处理：同一 Worker 最多自动重试 2 次，
仍失败则允许调整指令或换 Agent；同一执行方案总尝试上限 3 次。认证、权限、无效输入等
non-retryable failure 立即阻塞并请求用户；禁止无限重试，禁止覆盖终态 Run。每次尝试、
退避、换人理由和下一步都进入 Task 时间线。

`stream disconnected before completion`、Transport/network error 与 response body decode
失败即使未被 Adapter 正确标记，也按可恢复传输故障进入上述退避重试；进程或客户端连接
中断不能让 Task 控制线消失，queued/waiting_retry/running checkpoint 由后台 due-scan 恢复。

Task 追踪从窄抽屉的多个割裂区块升级为任务级 read model：首屏同时展示整体进度、
当前阶段、Coordinator 状态、当前执行 Agent、下一动作与阻塞原因；主时间线按因果顺序
展示规划、派发、执行、失败、重试/换人、汇总和验收，技术日志按需展开。

## 验收约束

- 从 UI 或 HTTP 创建根 Task 后，无额外点击即可观察到 Coordinator 已接取并产生首个 Run。
- Coordinator 派生子任务不会自动启动第二条 Coordinator 控制线，也不会重复创建 worker Run。
- 创建 Task、任务详情和启动 Run 均不再提供普通 Agent 选择器。
- Coordinator 能根据可用 Agent 的角色、技能、状态与能力生成并执行结构化 Plan。
- retryable worker failure 自动产生带 `retry_of`/attempt/reason 的新 Run；达到预算后可解释地阻塞。
- stream disconnected/transport decode failure 会先退避重试，再回到 Coordinator 重新规划，不静默停住。
- Coordinator Runtime 可在 Codex/Kimi 间切换、模型可修改，prompt 修改请求必须失败。
- Task 页面展示完整尝试链和下一动作；Chat 列表与会话不出现 Coordinator 或 Task 参与线。
- Task 最终完成仍需用户验收，Coordinator 不替用户接受任务。

## 放弃了什么

- 不把普通 `AgentProfile(role=lead)` 当 Coordinator：它可被编辑、删除、出现在 Chat，无法
  提供系统身份与失败恢复保证。
- 不继续让用户在创建、详情或 Run 面板手工选 Agent：这把统筹责任推回用户。
- 不在 Runtime Adapter 内实现业务重试：Adapter 只落 Outcome，重试/换人属于 Task 控制面。
- 不把一个全局 LLM 会话复用于所有 Task：每 Task 独立会话与状态，避免上下文污染。
- 不用单一百分比掩盖失败：进度来自已验证的计划步骤和尝试状态，并保留失败因果。

## 复活条件

只有当产品明确要求人工强制指派 Worker，且具备权限、审计和 Coordinator 状态重算契约时，
才增加高级 override；默认路径继续由 Coordinator 全权调度。

## 实施与验证

- 0020 双方言迁移落地受保护的系统 Agent、Workspace 配置、根 Task 状态与追加式事件；
  数据库约束禁止改写系统身份、提示词和跨根控制线。
- 根 Task 的公开创建入口无条件生成持久 Coordinator state 并立即启动首个 Run；同一
  `client_key` 重放不会重复建 Task 或 Run，子 Task 只唤醒根控制线。
- Coordinator 引擎实现有界 Worker 重试、退避 due-scan、一次重新规划、Codex/Kimi
  主备路由与用户阻塞/解除/打回恢复；首次接取会懒探测尚未就绪的内置 Runtime，
  标签/adapter 不匹配或探测失败会明确阻塞。传输流和 body decode 错误有防回归闭环。
- 恢复扫描只消费真实 control action/due checkpoint；running 观察态无法通过 due-scan
  或幂等重放重复启动。Worker retry 未完成前 dispatch 不进入 collecting，汇总使用最新
  retry 结果；evaluation 未终态不进入验收，verdict 拒绝会自动回到重新规划。
- 系统 plan timer/children_quiet 属控制面 automation，不受普通 heartbeat 门控；有 dispatch
  的 coordinated plan 只走单一 settlement 唤醒。settlement 事务失败会落持久 checkpoint
  并由 due-scan 重试，blocked/waiting_user/终态控制线会安全消费旧 wake 而不复活任务。
- `defer/join wake_at` 入队失败同样转为持久 `plan_timer` checkpoint；settlement checkpoint
  绑定精确 dispatch/run 代次，旧回调不能覆盖新代次。用户手工 Block 与不可重试失败会把
  旧 dispatch 明确收口为 degraded，避免任务停止后卡片仍显示运行中。
- 多 Worker 已提交 Run 即使中途一个 dispatch 失败仍全部尝试派发；重启对账得到的 lost
  Worker、一次性 self-heal 后再次 `session_unknown` 都重新进入有界 retry/replan；并行
  Worker 的迟到终态不能清除其他 retry checkpoint 或覆盖新一代 Coordinator Run。
- HTTP/SSE 读模型按 `run_id` 聚合尝试链，无 Run 的暂停/恢复节点只进入主时间线；终态
  失败绑定对应 Run，根/子 Task 共用同一根快照，Chat fail-closed。系统控制载荷不会作为
  用户消息暴露在派发卡片或滚动台账，展示为可读的 Coordinator 动作。
- Task title/description/验收标准、Worker 元数据与失败上下文统一编码进单行不可信 JSON；
  内置 system prompt 明确禁止把其中的 Agent 名称、提示词覆盖或验收指令当作控制命令。
  Plan 层禁止系统 Coordinator 充当 Worker，并要求 coordinated dispatch 后存在 join/defer。
  wake/settlement envelope 校验完整长度与受信后缀，任意尾部文本会被再次包入不可信 JSON。
- 前端已移除 Task 创建、详情与 Run 面板的 Agent 选择，增加 `/tasks/:taskId` 全页追踪、
  系统 Coordinator 设置和 prompt 锁定展示；Chat、@ 提及和普通 Agent 设置过滤系统身份。
- 2026-08-30 收口验证：Go build/vet、触面 race tests、SQLite 0020 迁移、前端 typecheck/
  674 tests/lint/build 全部通过；全新数据库的 HTTP 与浏览器实测覆盖自动接取、幂等、
  Chat 隔离、提示词拒改、阻塞恢复和用户最终验收；验收时 WorkItem 与 Coordinator
  state/event 在同一事务收口，证据读取失败 fail-closed。Codex/Kimi 本地底座留空模型时
  跟随运行时默认，显式模型引用仍进入不可变 Run snapshot。
