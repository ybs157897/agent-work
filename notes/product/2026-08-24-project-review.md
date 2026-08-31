# 产品评审 - Agent Team Workbench

日期：2026-08-24  
范围：当前仓库、外部设计文档与当前工作区验证结果。本文区分“事实”“判断”和“假设”；未经验证的数据不作为结论。

## 目录

1. 执行摘要
2. 现状事实
3. 四维评价
4. 关键风险
5. 改进优先级与路线图
6. 待验证假设

## 执行摘要

```
项目现状评审 - Agent Team Workbench
=====================================
业务目标：为 AI 工程师和产品团队提供多 Agent 任务执行的控制面。
用户价值：任务可发布、Agent 可配置、Run 可观测、风险可审批。
技术可行性：高；核心控制面、状态机、事件流和多 Runtime 接入已经可用。
优先级：高（RICE 约 60：Reach 2 / Impact 3 / Confidence 3 / Effort 1）
预计工期：稳定化 1-2 周；多用户化 4-8 周。
```

总体判断：**工程骨架已越过原型期，进入“本地专家工具”阶段；但产品定义、安全边界和交付门禁仍落后于实现速度。** 当前最适合继续定位为本机/团队内网的高权限工作台，不应直接对外多租户化。

## 现状事实

### 已实现能力

- 控制面提供约 56 条 REST 路由，覆盖工作区、Dashboard、Agent、任务树、计划、Run、审批、Artifact、模型凭据、Runtime binding 和 SSE。入口见 `internal/httpapi/server.go:82`。
- Run 有 13 态状态机，终态不可逆，重试创建新 Run；状态迁移集中在 `internal/domain/run.go:5`。
- 任务有独立看板状态和 execution / review / acceptance 三段相位，人工 Accept 是唯一完工路径；见 `internal/domain/workitem.go:5`。
- 后端分层清晰：domain、application、httpapi、persistence、runnergateway、runtime adapters、MCP server、knowledge retriever 相互职责明确。
- Runtime 面已包含 Codex app-server、Kimi app-server/Kimi CLI、DSH、Claude Code、mock、scripted 和 zcode 等适配器。
- 前端有 Dashboard、Tasks、Chat、Agents、Models、Logs、Settings 页面，使用 React 18、TypeScript、Zustand、Vite 和 Tailwind；见 `web/package.json:6`。
- 测试规模较大：后端有约 61 个 `_test.go` 文件、366 个测试函数；前端有约 34 个测试文件。
- 外部架构文档描述了 Web、Control Plane、数据库、Runner Daemon、Migrate CLI 的容器关系，并强调事务内同步写实体、事件、outbox 与幂等记录。

### 文档与流程事实

- 正式产品法典目录 `knowledge/prd/` 目前只有 `.gitkeep`，没有可引用的 PRD 条目。
- [`docs/product/end-goal.md`](../../docs/product/end-goal.md) 的 2026-08-23 对账表仍把子任务树、OrchestrationPlan、评估 Run、知识检索等列为待做，但仓库代码、迁移和提交显示这些能力已有落地痕迹。
- 架构借鉴文档记录 F3/F5/F1 已实施或合入主干，F2 挂起，F4 砍掉；说明团队有阶段性取舍记录。
- AGENTS.md 要求 CI 位于 `.github/workflows/ci.yml`，但该目录当前没有 workflow 文件。
- Git 工作区在 `main` 上有大量未提交修改，包含后端、前端、Agent 配置和多个新增前端模块，不符合“一任务一分支”和“分刀提交”约束。

## 四维评价

### 1. 产品价值：7 / 10

事实依据：产品简报最初定位是监控型 Dashboard，但当前系统已经覆盖任务发布、计划派工、Run 执行、会话续接、审批、模型/Runtime 管理和 MCP 只读/有限写面。

判断：

- 目标用户非常明确：需要同时管理多个 coding agent 的 AI 工程师、平台工程师和高阶产品用户。
- 核心价值不是“聊天界面”，而是**把 Agent 执行过程变成可治理的工作流**：任务状态、执行锁、审批、事件审计和失败恢复共同形成控制面价值。
- 当前体验偏向本机专家工具。若面向非技术产品团队，还需要更完整的引导、默认模板、失败解释和安全降级策略。

### 2. 技术与架构：8.5 / 10

优势：

- **领域建模强**：Run、WorkItem、Approval、Artifact、TaskSession 都有显式状态机和生命周期，而不是散落在 handler 中。
- **可靠性设计成熟**：命令级幂等键、实体级 `client_key`、乐观锁版本号、outbox、SSE 游标续传、runner 事件去重、任务执行锁均已进入主链路。
- **Runtime 抽象正确**：控制平面负责校验、执行和记账，harness 保持执行面，能降低多 Runtime 维护成本。
- **契约意识好**：OpenAPI、AsyncAPI、Runner schema 和 Codex 协议文档并存，便于前后端和 runner 并行演进。

短板：

- 单进程 Control Plane 的横向扩展、SSE 分片、事件压缩和长期运行容量仍未被产品化验证。
- PostgreSQL 是生产目标，SQLite 是本地验证，但缺少公开部署拓扑、备份恢复、迁移演练和升级兼容性说明。
- 模型凭据存在明文回显端点，代码注释也承认这是本机工作台定位；一旦多人使用就是高风险面。

### 3. 用户体验：6.5 / 10

判断：

- Chat 页承担了运行观测、审批、计划、Todo、Diff、终端输出等复杂信息，说明产品正在从“看板监控”走向“操作台”，方向合理。
- 但信息密度已经接近上限。新用户需要理解 Workspace、Agent、Model、Runtime Binding、Task、Plan、Run、Approval、Session 多层概念。
- 缺少清晰的首次成功路径：例如“添加模型 → 创建第一个任务 → 选择 Agent → 观察第一次 Run → 处理审批 → 验收完成”的向导。

### 4. 交付质量：6 / 10

本次验证结果：

- `go build ./...` 通过。
- `go vet ./...` 通过。
- 非沙箱环境下，除一个环境敏感测试外，其余 Go 包均通过 `go test -race`。
- `internal/agentwork/kimiconfig/TestApplyRequiresAPIKey` 在宿主机已导出 `MOONSHOT_API_KEY` 时失败。该测试未隔离继承的环境变量，属于测试隔离缺陷，不代表生产逻辑必然错误。
- 前端 `pnpm typecheck` 失败：
  - `web/src/components/chat/tool-card.tsx:5` 导入的 `toolActivityTitle` 未使用；
  - `web/src/utils/derive-chat-dock.test.ts:116` 将空数组传给 `latestRunId: string` 参数，参数顺序或类型不匹配。
- 由于类型检查先失败，本轮未获得可信的前端 test/lint 收口结果。
- `gofmt` 检测到 `internal/runtime/adapters/codexapp/codexapp.go` 和 `internal/orchestrator/orchestrator.go` 存在格式漂移。

## 关键风险

| 等级 | 风险 | 影响 | 建议 |
|---|---|---|---|
| 高 | 认证仍是演示角色，默认 Owner 权限 | 本地以外部署时任何人可能获得最高控制权 | 先做真实身份源、会话认证和工作区成员授权 |
| 高 | 凭据明文回显 | UI/XSS/代理日志可能泄露 API Key | 写后不可读，仅返回 masked hint；如需编辑走专用确认流 |
| 高 | 大量变更堆在 `main` 且无分刀提交 | 回滚困难，评审失真，违反仓库纪律 | 拆成文档、后端、前端重构/功能若干分支提交 |
| 高 | CI workflow 缺失 | 门禁只依赖本机，主干质量不可持续 | 建立后端 build/vet/gofmt/race test 与前端 typecheck/test/lint 流水线 |
| 中 | 产品文档落后于实现 | 新成员无法判断当前完成度和验收口径 | 用现状对账表替换过期 M1-M4 状态，补关键旅程 PRD |
| 中 | 测试环境泄漏导致误报 | 开发者本机能过、CI 不能过，反之亦然 | 所有凭据相关测试用 `t.Setenv` 清空变量 |
| 中 | UX 概念负担高 | 新用户难以上手，支持成本上升 | 建立首次成功路径、错误解释器和 Runtime 能力提示 |

## 改进优先级与路线图

### NOW：0-2 周，先收口可交付性

1. **修复当前门禁**
   - 删除未使用的 `toolActivityTitle` 导入。
   - 修正 `deriveChatDock` 测试参数顺序或函数签名。
   - 运行 `gofmt`。
   - 为 Kimi API Key 测试强制清空环境变量。
   - 恢复前端 `typecheck/test/lint` 全绿。
2. **恢复 CI**
   - 后端：build、vet、gofmt、race test。
   - 前端：typecheck、test、lint。
   - 迁移：Postgres/SQLite 双方言等价冒烟。
3. **拆分当前工作区**
   - 按 DSH 纪律拆成文档、后端、前端组件、测试辅助等独立提交。
4. **更新产品现状对账表**
   - 明确每个能力的状态：已上线 / 本地可用 / 部分 / 未做。

RICE 评分：Reach 3 / Impact 3 / Confidence 3 / Effort 1，总分 27。

### NEXT：2-6 周，从专家工具走向可靠团队工具

1. **定义三条北极星旅程**
   - 新用户首次成功 Run。
   - Lead 创建 Plan 后派工到 Worker 并完成人工验收。
   - 用户处理高风险审批并继续 Run。
2. **建立产品指标**
   - Time to First Successful Run。
   - Task Acceptance Rate。
   - Run Failure Rate / Retry Success Rate。
   - Approval Median Latency。
   - Resume 成功率与前缀缓存命中率。
3. **补齐安全最小集**
   - 真实登录身份。
   - 工作区成员角色绑定。
   - 审批人身份写入审计。
   - API Key 写后不可读。
4. **产品化错误处理**
   - 统一失败族文案。
   - 区分用户可修复、Agent 可自愈、管理员介入三类错误。

RICE 评分：Reach 2 / Impact 3 / Confidence 2.5 / Effort 1，总分 15。

### LATER：6 周后，再考虑规模化

1. 多用户/多工作区运营能力。
2. Runner 池、队列优先级、配额和健康熔断。
3. 知识检索升级为可评测的检索服务。
4. 更完整的成本中心：模型用量、任务成本、团队预算和异常消耗告警。
5. 公共部署形态与备份恢复方案。

## 待验证假设

1. **目标用户愿意从 IDE/CLI 切换到 Workbench**：目前只有产品判断，未验证留存和使用频次。
2. **Lead/Worker 编排能降低总交付时间**：需要对比单人单 Agent 与多 Agent 编排的实际耗时和质量。
3. **审批不会成为主要阻塞点**：需要统计审批等待时长、拒绝率和超时率。
4. **多 Runtime 抽象在真实负载下稳定**：需要按 Runtime 分别统计失败族、resume 成功率和长任务存活率。
5. **SQLite 本地模式可以平滑迁到 PostgreSQL 团队模式**：需要一次真实数据迁移和并发压测。
6. **用户理解当前概念模型**：建议对 5 名目标用户做首次成功路径测试后再决定是否增加向导。

## 结论

当前项目的最大优势是**控制面工程已经领先于大多数 Agent Demo**：状态机、幂等、事件流、审批、Runtime 抽象和测试覆盖都具备认真产品的雏形。最大问题不是“能不能跑”，而是**产品叙事、安全边界和交付门禁还没有追上代码能力**。

建议未来两周不要继续扩大编排能力，先把当前主干收口成可重复构建、可测试、可回滚的状态；随后以三条核心旅程为验收线，把本机工具推进到可靠的团队工具。
