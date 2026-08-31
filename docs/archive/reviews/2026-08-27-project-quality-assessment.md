# 项目质量评估报告

> 历史评审快照（2026-08-27）。文中的构建、依赖、安全和完成度结论只描述当日状态；当前事实以代码、[`docs/README.md`](../../README.md) 与后续 implemented notes 为准。

> 评估日期：2026-08-27
> 评估范围：agent-team-workbench（Go 后端 + Web 前端 + 多模块结构）
> 综合评分：**B（约 78/100）**

---

## 一、评估方法

| 维度 | 方法 |
|---|---|
| 工程纪律 | 检查提交历史、分支策略、CI 配置 |
| 可构建性 | `go build ./...` + `go vet ./...` + 前端类型检查 |
| 测试覆盖 | 测试文件数与用例数统计，运行全量测试 |
| 架构质量 | 关键模块（状态机、持久化、并发安全）代码审查 |
| 安全面 | 凭据存储、密钥管理、敏感文件权限 |
| 可维护性 | 文件大小分布、依赖管理、文档一致性 |

---

## 二、亮点

### 2.1 运行状态机建模清晰

`internal/domain/run.go:8` —— 13 态状态机，终态不可变，取消/重连路径都有显式定义：

- 13 个运行状态为唯一权威模型
- `ModuleRunner` 是进程内唯一推进点
- 任何 `Outcome` 都必须能落入终态（不许卡死）
- `session_unknown` 自愈路径有显式回归测试

### 2.2 后端测试规模较好

- **61 个 Go 测试文件**
- `go build ./...` 和 `go vet ./...` 全部通过
- Race 测试覆盖并发路径

### 2.3 前端单测表现很好

- **30 个测试文件，264 个用例全部通过**
- 组件级与 Hook 级测试覆盖较为完整

### 2.4 凭据设计方向正确

`internal/modelconfig/modelconfig.go:563` —— 注册表保存 `api_key_env` 引用而非明文密钥：

- 模型配置注册表只保存环境变量名引用
- 凭据文件要求 `0600` 权限（设计意图）
- 不做字符串拼接配密钥，减少泄露面

### 2.5 提交历史符合纪律

分刀提交 + conventional commits 格式，CI 覆盖后端、契约和迁移验证。

---

## 三、阻塞问题

### P0 — 立即修复

| # | 问题 | 文件 | 严重程度 |
|---|---|---|---|
| 1 | 未使用导入阻塞前端类型检查与 Lint | `web/src/components/chat/turn-block.tsx:3`：`PlanCard` 被导入但未使用 | P0 |
| 2 | 前端缺少 `pnpm-lock.yaml` | `web/` 目录无 lock 文件，依赖安装不可复现，无法做安全 audit | P0 |
| 3 | CI 完全没有前端门禁 | `.github/workflows/ci.yml` 只覆盖 Go、契约、Postgres 迁移 | P0 |

### P1 — 高优先级

| # | 问题 | 文件 | 严重程度 |
|---|---|---|---|
| 4 | 两个 Go 文件未格式化 | `internal/orchestrator/orchestrator.go`、`internal/runtime/adapters/codexapp/codexapp.go` | P1 |
| 5 | Race 测试失败：测试未隔离宿主机环境变量 | `internal/agentwork/kimiconfig/kimiconfig_test.go:72`：本机 `MOONSHOT_API_KEY` 导致 `TestApplyRequiresAPIKey` 误通过校验 | P1 |
| 6 | 凭据文件权限过宽 | `models/credentials.local.yaml` 权限为 `0644`（应为 `0600`）；虽已被 `.gitignore` 忽略且未跟踪，但存在同级泄露风险 | P1 |

### P2 — 应修复

| # | 问题 | 说明 |
|---|---|---|
| 7 | 4 个 Hook 警告 | 集中在 `chat.page.tsx` 与 `agents.page.tsx` 的依赖数组管理 |
| 8 | 大文件偏多 | 后端最大业务文件超过 1000 行；前端 `models.page.tsx` ~972 行、`agents.page.tsx` ~920 行 |
| 9 | CI 缺少 SQLite 迁移验证 | 双迁移目录（`migrations/` 与 `migrations/sqlite/`）需确保语义等价 |
| 10 | 工作区未提交改动过多 | 约 44 个修改/新增条目，混合文档、后端模型配置和前端聊天重构，不符合"一任务一分支 + 分刀提交"纪律 |

---

## 四、覆盖缺口

| 包/模块 | 测试覆盖率 | 评估 |
|---|---|---|
| `httpapi` | ~29% | 偏低，需补集成级回归断言 |
| SQL store 层 | ~21% | 偏低，核心持久化路径需覆盖 |
| `control-plane` 装配层 | ~3.5% | 极低，但该层以胶水代码为主，可接受 |

---

## 五、整改建议顺序

### 第 1 步：打通前端 CI 门禁（P0）

```
1. 移除 web/src/components/chat/turn-block.tsx 中未使用的 PlanCard 导入
2. 在 web/ 下执行 pnpm install 生成 pnpm-lock.yaml
3. 在 .github/workflows/ci.yml 中添加前端 job：
   - pnpm tsc --noEmit
   - pnpm test
   - pnpm lint
```

### 第 2 步：修复本地验证失败（P1）

```bash
# 修复 Go 格式化
gofmt -w internal/orchestrator/orchestrator.go
gofmt -w internal/runtime/adapters/codexapp/codexapp.go

# 修复测试环境隔离
# 在 TestApplyRequiresAPIKey 中加 t.Setenv("MOONSHOT_API_KEY", "")
```

### 第 3 步：清理工作区

按文档、后端 reasoning effort 配置、前端聊天重构拆分为独立分支和提交，恢复"一任务一分支"纪律。

### 第 4 步：加固 CI

- 增加 SQLite 迁移验证步骤，确保 `migrations/` 与 `migrations/sqlite/` 双目录语义等价
- 增加 `pnpm audit` 步骤

### 第 5 步：收敛代码复杂度

- 拆分超过 900 行的前端页面组件
- 优先补 `httpapi` 和 SQL store 层的集成级回归断言
- 修复 Hook 依赖数组警告

---

## 六、维护性风险

| 风险项 | 说明 | 缓解措施 |
|---|---|---|
| 工作区污染 | main 上混合多个未提交改动 | 拆分支、分刀提交 |
| 大文件 | 部分文件 > 900 行 | 拆分组件 |
| 依赖锁定缺失 | 前端无 lock 文件 | `pnpm install` 生成 |
| 架构文档 | 无集中式架构决策记录 | 考虑 ADR 文件夹 |
| 凭据文件权限 | 本地凭据 0644 | `chmod 0600` |

---

## 七、安全面总结

| 检查项 | 状态 | 备注 |
|---|---|---|
| 凭据存储 | ✅ 设计正确 | 注册表只存环境变量引用，不存明文 |
| 凭据文件权限 | ⚠️ 本地需修复 | `models/credentials.local.yaml` 应为 0600 |
| 密钥泄露 | ✅ 无风险 | 文件已被 .gitignore 忽略且未跟踪 |
| 依赖安全 | ⚠️ 无法 audit | 缺少 lock 文件，无法做 `pnpm audit` |
| 并发安全性 | ✅ 有测试覆盖 | 并发取消、进程组缓存、事务 Outbox 都有实现与测试 |

---

## 八、评分汇总

| 维度 | 满分 | 得分 | 扣分原因 |
|---|---|---|---|
| 可构建性 | 20 | 17 | 前后端都有阻塞项（未格式化、未使用导入） |
| 测试质量 | 25 | 20 | 覆盖缺口明显，race 有失败 |
| 架构设计 | 20 | 18 | 状态机亮点，但部分文件过大 |
| 安全性 | 15 | 12 | 凭据权限、无 lock 文件 |
| 工程纪律 | 10 | 7 | 工作区混合改动、无前端 CI 门禁 |
| 可维护性 | 10 | 4 | 缺少 lock 文件、大文件偏多、Hook 警告 |
| **合计** | **100** | **78** | **B** |
