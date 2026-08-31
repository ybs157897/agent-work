# 审批授权粒度：resolve scope 三级 + grant 自动代答

Status: implemented

## 决策与理由

对照 codex 桌面端「本次允许 / 本会话总是允许 / 总是允许」：resolve 命令 body 扩展可选
`scope`（once|thread|workspace，缺省 once），scope≠once 且 approved 时落一行
`approval_grants`（thread 锚定 work item——我们会话≈work item 锚点；workspace 全局；均锚定
workspace+agent+kind）。后续 `Service.RequestApproval` 先查 grant：kind 相同 + pattern 前缀
命中（或 pattern 空）即代答批准，走既有 `ResolveApproval` 机器（事件/审计/activity/转发），
保证「自动批准必须可追溯」红线——activity 落「已按授权自动批准（grant 摘要）」。

**pattern 语义 = 同 kind + 摘要前缀**。grant.pattern 存被批准请求的 Summary 原文，匹配是对
新请求 Summary 的 `strings.HasPrefix`。application 层拿不到裸命令（Command 在 adapter 的
JSON-RPC params 里，SPI 只传 kind/risk/summary），剥 "Codex 请求执行命令：" 之类 adapter
前缀属脆弱字符串手术；摘要前缀跨 adapter 语义一致（"以同样方式开头的同类请求"）。边界含义：
`git push` 的授权同样前缀命中 `git push-evil`（无词边界）——prefix 白名单本就只承诺前缀，
卡片文案用「总是允许」明示后果。

**代答仍创建 ApprovalRequest，但由 grant 路径在事务提交后异步自决议**。任务原文「不建
ApprovalRequest」与现架构冲突（见「放弃了什么」第 1 条），实质红线是「不打扰用户 + 可追溯」：
请求行创建即被 grant 代答落 approved，UI 只会看到完成态行，不会出现可交互的待批卡。

## 放弃了什么

1. **grant 命中后不建 ApprovalRequest、直接向 adapter 投递 ControlApproval**：所有进程内
   adapter（codexapp/dsh/kimiapp）都在 `Callbacks.RequestApproval` 返回**之后**才登记待决
   approval 的消费通道（如 codexapp `s.approvals[approvalID]=ch`）；同步投递会在登记前被
   `resolveApproval` 静默丢弃 → adapter 永久悬挂，且无 ApprovalRequest 行意味着没有任何人工
   兜底重决路径。改为「创建后异步自决议」：异步路径隔着一个完整事务提交（毫秒级）而登记在
   回调返回后的直线代码（纳秒级），且最坏退化 = 控制被丢、审批保持 pending 由用户人工决议
   ——正好回到无 grant 的既有行为，优雅降级。残留的理论竞态与根治方案（SPI 增加同步自批
   应答）见「复活条件」。
2. **adapter 提取裸 Command 作为 pattern 匹配面**：需改 SPI 与全部 adapter（本任务禁触），
   且 pattern 语义收敛到摘要前缀已满足「命令前缀白名单」诉求。
3. **plan_dispatch/question 等非工具审批也可建 grant**：grant kind 闭集
   command|file_change|permissions（对齐 codex 官方三类审批请求）；plan_dispatch 放行是编排
   闸门，"总是允许"会让 plan 自动绕过人工护栏；scope≠once + 非 grantable kind 响亮报 422，
   不静默降级为 once。
4. **新增 approval.grant_created canonical 事件类型**：grant 创建落 audit 行 + resolve 事件
   data 已足够追溯；新增事件类型要动 IsKnownEventName/openapi/前端事件开关，收益不抵。

## 负向保证

- 拒绝路径永不建 grant（scope 只在 approved 分支消费；reject 带 scope 时枚举仍校验但不生效）。
- 幂等重放（重复相同决定）不重复建 grant；grant 只随首次 pending→resolved 变更创建。
- grant 永不跨 workspace/agent 生效：两列是匹配硬条件，与 scope 无关。
- 自动代答失败（转发错误/事务失败）不吞：日志留痕、审批保持 pending 走人工。

## 复活条件

- 进程内 adapter 出现自动代答被丢（审批卡住 pending 且 adapter 悬挂）→ 根治点是 SPI：
  `RequestApproval` 回调返回值扩展出同步「已代答」语义（adapter 不等 Controls 直接回
  accept），届时删除异步自决议 goroutine，本 note 随之更新。
- 需要 grant 管理面（列表/撤销 API + UI）时从 `approval_grants` 表直查起步，不新增事件类型。
