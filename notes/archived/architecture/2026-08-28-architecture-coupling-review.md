# 架构耦合评审报告

Status: archived

Archived: 2026-08-31

> 这是 2026-08-28 的代码规模与耦合快照。Task Coordinator、任务控制面和 Runner v2 随后已经合入；文中的行数、包数量和优先级不代表当前架构状态。

## 执行摘要

```
架构评审 - agent-team-workbench 耦合点分析
=====================================
业务目标：识别架构中最脆弱的耦合点，降低变更风险与维护成本
评审范围：internal/ 全部 19 个包 + runtime/adapters 7 个 adapter
优先级：高 — 当前架构在 application 包形成单点故障，多处耦合待重构
产出：8 个脆弱点（含风险等级、影响面、建议方案）
```

---

## 结论

**最脆弱的耦合点是 `internal/application` 包**：它既是领域编排的中心枢纽（被 5 个外部调用方依赖），又是 `runtime.EngineSink` 和 `scheduling.RunStarter` 的实现者，单包 4928 行代码横跨 Runs、Plans、Sessions、WorkItems 四个子领域，且内部硬编码了特定 adapter 的校验逻辑。其次，`runtime.EngineSink` 与 `runnergateway.Engine` 两条几乎相同的接口定义构成了历史遗留的接口分裂风险。

---

## 脆弱点详细分析

### P0 — `application` 包上帝枢纽

| 维度 | 数据 |
|---|---|
| 总行数 | 4928 行（13 个文件） |
| 内部包依赖数 | 5 个（domain, orchestrator, runtime, knowledge, scheduling） |
| 外部调用方 | 5 个（httpapi, mcpserver, runnergateway, persistence/sqlstore, cmd/control-plane） |
| `Service` 结构体字段 | 4 个必需字段 + 4 个可选函数字段 + 1 个可选接口字段 |
| 子领域混杂 | runs.go (1149行), plan.go (1018行), service.go (683行), sessions.go (374行), conversation.go (477行) |

**风险**：任何对 `application` 包的改动都可能波及全部 5 个调用方。`Service` 结构体通过可选字段（`ApprovalForwarder`, `ControlForwarder`, `InputForwarder`, `ModelResolver`, `Knowledge`）承载了多个可选职责，这些字段在运行时注入但编译期无法验证是否已设置。

**建议**：按子领域拆分为 `application/runs/`, `application/plans/`, `application/sessions/` 等子包，或将 `Service` 拆为多个聚焦接口面。

---

### P0 — `EngineSink` vs `Engine` 接口冗余

`runtime/spi.go:168` 定义 `EngineSink`（7 方法），`runnergateway/gateway.go:23` 定义 `Engine`（8 方法），两者共享 7 个完全相同的方法签名，仅 `Engine` 多一个 `RecordArtifact`。两个接口由同一个 `*application.Service` 实现，但 `runnergateway` 不引用 `runtime.EngineSink`，而是独立定义了自己的副本。

**风险**：如果 `EngineSink` 新增方法（如 `RecordArtifact`），`Engine` 需要手动同步，反之亦然。这是已实现的 M2 功能留下的接口碎片。

**建议**：将 `EngineSink` 移到共享位置（如 `runtime` 包），让 `runnergateway.Engine` 内嵌或直接复用；`RecordArtifact` 作为可选方法或独立接口附加。

---

### P1 — `application` 层硬编码 adapter 特定校验

`internal/application/runs.go:411` 的 `validateAdapterModel` 函数通过 `switch binding.AdapterID` 直接引用 `codexconfig.ResolveBaseURL` 和 `kimiconfig.ResolveBaseURL`。添加新 adapter 需要在此处新增 case 分支，违反开闭原则。

**风险**：`application` 层本应是 adapter 无关的。当前设计意味着每引入一个新 adapter 都需要修改应用层代码。

**建议**：将 adapter 校验逻辑抽象为 SPI 接口（如 `runtime.ModelValidator`），由各 adapter 实现，应用层通过 `Registry` 查询。

---

### P1 — `application` ↔ `runtime` 准循环依赖

`application.Service` → `runtime.Registry` + `runtime.EngineSink`（实现者）
`runtime.ModuleRunner` → `application.Dispatcher`（实现者） + `EngineSink`（消费者）

构造环通过 `SetDispatcher` 延迟注入打破，但 `EngineSink` 接口的 7 个方法构成了 `application.Service` 对 `runtime` 包的"宽接口"依赖。`EngineSink` 的任何变化都需要同时修改 `application.Service` 和 `runnergateway`。

**建议**：考虑将 `EngineSink` 拆分为更细粒度的接口（如 `RunStatusRecorder`, `RunEventRecorder`, `SessionRecorder`, `ApprovalRequester`），让 `ModuleRunner` 只依赖它实际需要的子接口。

---

### P1 — `httpapi` 绕过 `Service` 直接调用 `store`

`httpapi.Server` 持有 `application.Store` 字段，在 `enrichWorkItem` 等 handler 中直接调用 `store.WorkItems().ActiveBlocker()` 和 `store.WorkItems().LatestRunID()`。

**风险**：绕过 `Service` 意味着跳过了事务边界、事件发布、权限校验等横切关注点，可能导致数据不一致。

**建议**：所有 store 访问应通过 `Service` 方法进行，或至少确保读操作的一致性（如使用 `Service` 提供的 read model 方法）。

---

### P2 — `Store` 接口定义在 `application` 包中

`internal/application/repositories.go` 定义了完整的 `Store` 接口（14 个子接口），由 `persistence/sqlstore` 实现。

**风险**：存储接口定义在应用层意味着：
1. 更换存储实现（SQLite → PostgreSQL 或其他）需要引入 `application` 包依赖
2. `application` 包无法在不影响存储实现的情况下独立演进
3. 测试替身（mock store）与 `application` 在同一包边界内

**建议**：将 `Store` 接口移出 `application` 到独立的 `internal/store` 或 `internal/persistence` 包。

---

### P2 — `domain` 包过载

`domain` 包 12 个文件定义了 9 个实体类型、5 个值对象、5 个状态机、~50 个事件常量、8 个哨兵错误。被 8 个内部包直接依赖。

**风险**：`RunStatus` 的 13 态尤其脆弱——添加新状态需要同步更新 `runTransitions` 映射表、`IsTerminal()` 方法，以及所有调用 `CanTransitionTo` 的代码路径。状态机变更需要跨包协调。

**建议**：考虑将状态机定义与转换逻辑拆分为独立的 `domain/runstatus` 或 `domain/fsm` 子包，减少核心实体包的变更频率。

---

### P2 — `runnergateway` 与 `ModuleRunner` 职责重叠

`runnergateway.Gateway` 和 `runtime.ModuleRunner` 都实现了 `application.Dispatcher` 接口（通过 `chainDispatcher` 串联）。两者都通过 `Engine`/`EngineSink` 接口回写 `application.Service`，但 `runnergateway` 的 `Engine` 接口多了一个 `RecordArtifact` 方法，而 `ModuleRunner` 的 `Callbacks` 接口没有对应的 `OnArtifact` 方法。

**风险**：`RecordArtifact` 功能只在远程 runner 路径可用，进程内执行路径（`ModuleRunner`）无法上报 artifact，功能不一致。

**建议**：统一两条路径的事件上报能力，确保进程内执行也支持 artifact 上报。

---

## 依赖关系汇总

```
                ┌──────────────────────────────────────────────┐
                │               cmd/control-plane              │
                ├──────────────────────────────────────────────┤
                │  httpapi   mcpserver   runnergateway  sqlstore│
                │    │           │            │             │   │
                │    └─────┬─────┘            │             │   │
                │          │                  │             │   │
                │    ┌─────┴───────┐   ┌──────┴──────┐     │   │
                │    │ application │◄──│ EngineSink  │     │   │
                │    │  (4928行)   │──►│  (7 methods)│     │   │
                │    └──┬──┬──┬───┘   └─────────────┘     │   │
                │       │  │  │                            │   │
                │       │  │  └──► scheduling              │   │
                │       │  └────► knowledge                 │   │
                │       └──────► runtime (通过 Registry)    │   │
                │                                          │   │
                │  domain  (8 个包依赖)                     │   │
                └──────────────────────────────────────────────┘
```

---

## 建议优先级排序

| 优先级 | 建议 | 影响面 | 预计工期 |
|---|---|---|---|
| P0 | 将 `EngineSink` 与 `runnergateway.Engine` 合并为单一接口 | 2 个文件修改，低风险 | 1-2 天 |
| P0 | 按子领域拆分 `application` 包 | 5 个调用方需适配，高风险 | 2-3 周 |
| P1 | 将 adapter 校验抽象为 SPI 接口，移出 `application` | 解耦 `codexconfig`/`kimiconfig` 依赖 | 3-5 天 |
| P1 | 将 `EngineSink` 拆分为细粒度子接口 | 减少 `ModuleRunner` 的宽接口依赖 | 2-3 天 |
| P1 | 消除 `httpapi` 对 `store` 的直接调用 | 确保事务边界一致性 | 3-5 天 |
| P2 | 将 `Store` 接口移出 `application` 包到独立 `persistence` 包 | 涉及全局重命名，需协调 | 1 周 |
| P2 | 拆分 `domain` 包中的状态机逻辑 | 低风险，可增量重构 | 3-5 天 |
| P2 | 统一 `runnergateway` 与 `ModuleRunner` 的功能覆盖面 | 消除 artifact 上报的不一致性 | 2-3 天 |

---

## 待验证假设

1. 拆分为 `EngineSink` 子接口后，`ModuleRunner` 是否真的只需要 subset 而非全部 7 个方法 — 需审计 `ModuleRunner` 中 `EngineSink` 的调用点
2. `application` 包拆分后，`chainDispatcher` 的构造逻辑是否需要重新设计 — 当前 `Service` 持有 `dispatcher` 字段，拆分后各子服务可能共享同一 dispatcher
3. 将 `Store` 移出 `application` 包后，`application` 包是否需要引入新的依赖管理策略 — 当前 `Store` 接口引用了 `scheduling` 包（`Wakeups()` 方法）
