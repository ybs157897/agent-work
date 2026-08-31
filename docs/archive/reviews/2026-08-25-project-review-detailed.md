# 产品详细评审 - Agent Team Workbench

> 历史评审快照（2026-08-25）。文中的 CI、凭据、测试数量和产品完成度只描述当日状态；当前事实以代码、[`docs/README.md`](../../README.md) 与后续 implemented notes 为准。

日期：2026-08-25
范围：仓库代码、契约、测试、前端、安全面与交付门禁的逐层核查。本文区分"事实""判断"和"假设"；未经验证的说法不作为结论。

## 目录

1. 执行摘要
2. 后端架构与领域建模
3. API 契约与安全面
4. 前端结构与用户体验
5. 测试覆盖与交付门禁
6. 五维评分
7. 改进路线图（NOW / NEXT / LATER）
8. 待验证假设

## 执行摘要

```
项目详细评审 - Agent Team Workbench
=====================================
业务目标：为 AI 工程师和产品团队提供多 Agent 任务执行的控制面。
用户价值：任务可发布、Agent 可配置、Run 可观测、风险可审批。
技术可行性：高；核心控制面、状态机、事件流和多 Runtime 接入已经可用。
优先级：高（RICE 约 60：Reach 2 / Impact 3 / Confidence 3 / Effort 1）
预计工期：稳定化收口 1 周；多用户化 4-8 周。
```

总体判断：**工程骨架已越过原型期，进入"本地专家工具"阶段。后端领域建模和多 Runtime 抽象在同类项目中领先；但 CI 门禁缺失、凭据明文回显和产品文档缺位仍是交付短板。**

## 现状事实

### 后端架构与领域建模

- `internal/` 下共 18 个子包、约 200 个 Go 文件，分层清晰：domain → application → httpapi → persistence → runnergateway → runtime adapters。
- Run 有 13 态状态机（`queued` 到终态），终态不可逆，重试创建新 Run；状态迁移集中在 `internal/domain/run.go`。
- WorkItem 有独立看板状态（todo/in_progress/blocked/completed/cancelled）+ 三段相位（execution/review/acceptance）；人工 Accept 是唯一完工路径。
- 任务执行锁（LockedByRunID）防止同一任务双跑，锁归属 Run 而非 Agent，属主 Run 落终态即死锁可抢占。
- Runtime 适配器已接入 8 个：Codex app-server、Kimi app-server/Kimi CLI、DSH、Claude Code、mock、scripted、zcode。
- 数据库迁移有 14 个版本，SQLite 双目录语义等价。
- Outbox 模式、命令级幂等键、实体级 `client_key`、乐观锁版本号均已进入主链路。

### API 契约与安全面

- Control Plane 提供 56 条 REST 路由（`internal/httpapi/server.go`），覆盖工作区、Dashboard、Agent、任务树、计划、Run、审批、Artifact、模型凭据、Runtime binding 和 SSE。
- OpenAPI 3.1 规范存在于 `contracts/web/openapi.yaml`；AsyncAPI 存在于 `contracts/events/asyncapi.yaml`；Runner/Runtime schema 各自独立。
- RBAC 权限矩阵已实现 7 个权限点 × 5 个角色（Owner/Admin/Operator/Approver/Viewer），测试覆盖完整（`internal/security/security_test.go`）。
- **高风险事实**：`GET /api/v1/models/provider-credentials` 返回明文 API Key（`internal/httpapi/handlers_credentials.go:31`）。RBAC 注释声称"任何角色都没有 credential.read"，但实际该端点用 `PermRuntimeManage` 守卫，Admin/Owner 都可读取明文——权限设计与实现不一致。

### 前端结构与用户体验

- React 18 + TypeScript + Zustand + Vite + Tailwind CSS；6 个页面（Dashboard/Tasks/Chat/Agents/Models/Logs/Settings）。
- 组件库基座已建立：`web/src/components/ui/` 包含 Button/Card/Input/Field/StatusPill/EmptyState，配套测试。
- Chat 组件群承担运行观测核心：审批卡、Diff 卡、Plan 卡、工具行、推理活动、转写视图等约 20 个组件。
- 设计系统有正式法典 `web/DESIGN.md`（v1.0.0）：语义化 token、色彩/间距/圆角/阴影全部变量化，且有 `design-tokens.test.ts` 门禁禁止内联色值。
- Zustand store 层总计约 3000 行，chat.store.ts 单文件 758 行（最大复杂度集中点）。
- 测试文件 40 个，312 个测试用例，全部通过。

### 测试覆盖与交付门禁

- 后端：61 个 `_test.go` 文件、358 个测试函数；`go build` 和 `go vet` 通过；gofmt 干净。
- 前端：typecheck 失败（`derive-chat-dock.test.ts:116` 类型错误）；test 全绿（312 用例）；lint 零错误但有 6 个 React Hooks 警告。
- **CI workflow 缺失**：`.github/workflows/` 目录为空，门禁完全依赖本机。
- Makefile 提供 build/test/vet/fmt/migrate 和前端 dev/build 入口，但无统一 lint/typecheck 收口目标。

## 五维评分

| 维度 | 评分 | 关键依据 |
|---|---|---|
| 产品价值 | 7.5/10 | 多 Agent 编排控制面价值真实；但缺少首次成功向导和北极星旅程定义 |
| 技术架构 | 8.5/10 | 领域建模强、幂等/outbox/状态机成熟、Runtime 抽象正确 |
| 安全边界 | 5/10 | RBAC 框架好但凭据明文回显是高危面；认证仍为演示角色 |
| 用户体验 | 6.5/10 | 设计系统纪律好；概念负担高、Chat 信息密度接近上限 |
| 交付质量 | 6/10 | 后端质量高；前端 typecheck 断裂 + CI 缺失是最大短板 |

## 改进路线图

### NOW：0-1 周，先让门禁闭环

| # | 任务 | RICE | 工时 |
|---|---|---|---|
| 1 | 修复 `derive-chat-dock.test.ts:116` 类型错误，跑通 typecheck | R3/I3/C3/E1 = 27 | 0.5h |
| 2 | 建立 GitHub Actions CI：后端 build/vet/gofmt/race + 前端 typecheck/test/lint | R3/I3/C3/E1 = 27 | 2h |
| 3 | 凭据端点改为写后不可读（仅返回 masked hint） | R3/I3/C3/E1 = 27 | 2h |
| 4 | 补齐 Makefile `lint` 目标（golangci-lint 或 go vet + pnpm lint） | R2/I2/C3/E1 = 12 | 0.5h |

### NEXT：1-4 周，从专家工具走向可靠团队工具

1. **三条北极星旅程 PRD**：首次成功 Run / Lead 派工到验收 / 高风险审批处理。
2. **产品指标埋点**：Time to First Successful Run、Task Acceptance Rate、Run Failure Rate、Approval Median Latency。
3. **真实身份认证**：替换演示角色为真实登录源，绑定 Workspace 成员关系。
4. **错误分类文案**：区分用户可修复 / Agent 自愈 / 管理员介入三类失败。

### LATER：4 周后

1. 多用户/多工作区运营能力。
2. Runner 池、队列优先级、配额和健康熔断。
3. 知识检索升级为可评测服务。
4. 成本中心：模型用量、任务成本、预算告警。
5. 公共部署形态与备份恢复方案。

## 待验证假设

1. 目标用户愿意从 IDE/CLI 切换到 Workbench（未验证留存和使用频次）。
2. Lead/Worker 编排能降低总交付时间（需 A/B 对比单人单 Agent 与多 Agent 编排）。
3. 审批不会成为主要阻塞点（需统计等待时长、拒绝率和超时率）。
4. 多 Runtime 抽象在真实负载下稳定（按 Runtime 分别统计失败族、resume 成功率）。
5. SQLite 本地模式可以平滑迁到 PostgreSQL 团队模式（需一次真实迁移演练和并发压测）。
6. 用户理解当前概念模型（建议对 5 名目标用户做首次成功路径测试）。

## 结论

项目的核心竞争力是**控制面工程领先于大多数 Agent Demo**：状态机不可逆、幂等键互补、Outbox 事件流、8 个 Runtime 适配器和 358 个后端测试构成了认真产品的骨架。最大风险不是"能不能跑"而是"能不能持续安全地交付"——CI 缺失意味着每次合入都依赖本机自觉；凭据明文回显意味着一旦部署环境变化就是安全事故。建议未来一周不扩大功能范围，先把门禁和安全两个洞补上。
