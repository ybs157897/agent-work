# 架构耦合分析报告

> 分析师：资深产品经理
> 分析范围：agent-team-workbench 全量 Go 包
> 分析日期：2026-08-28

## 执行摘要

```
架构分析 - 耦合脆弱性评估
=====================================
业务目标：识别当前架构中最脆弱、最可能阻碍迭代的耦合点
技术可行性：Go 静态分析 + 包依赖图 + 接口契约分析
优先级：高
关联风险：M4 唤醒调度、M2 Runner WSS 网关、扩展新 Adapter 均受牵连
```

## 包依赖图

```
domain (纯类型，无内部依赖)
    ↕  ← 纯接口契约，无包导入
    ├── orchestrator (仅依赖 domain)
    ├── scheduling (仅依赖 domain)
    ├── runtime (仅依赖 domain)
    │
    ├── application ──────→ runtime (依赖 *runtime.Registry)
    │   ├── ←→ runnergateway (依赖 application.Service)
    │   ├── ←→ httpapi (依赖 application.Service)
    │   └── ←→ mcpserver (依赖 application.Service)
    │
    ├── persistence/sqlstore ──→ application, scheduling, domain
    ├── agentconfig ──→ domain, dshcatalog
    │
    └── cmd/control-plane (导入所有包，单点装配)
```

`domain` → `orchestrator`/`scheduling`/`runtime` 方向保持干净（单向依赖），但 `application` 作为
业务编排层 **反向依赖了 `runtime` 的具体类型**，破坏了领域驱动设计的分层原则。

## 脆弱性排名

### F1 — `application.Service` God 对象（最脆弱）

**位置：** `internal/application/`（10 个文件，~98 个方法）

**证据：**
- `service.go`：25 个方法（workspace/agent/workitem CRUD）
- `runs.go`：24 个方法（Run 状态机、审批、artifacts）
- `plan.go`：18 个方法（编排计划执行器）
- `sessions.go`：8 个方法（会话轮换、resume、自愈）
- `runtime_bindings.go` / `evaluate.go` / `reconcile.go` / `conversation.go` / `tasklock.go` / `claim_return.go` / `wakeup.go` / `plan_extract.go`：其余 23 个方法

**注入依赖（9 个）：**
```go
type Service struct {
    store      Store           // 10 个子接口
    dispatcher Dispatcher      // 实现：chainDispatcher（cmd/main）
    notifier   Notifier        // 实现：sse.Hub
    adapters   *runtime.Registry  // ← 具体类型，非接口
    ApprovalForwarder func(...)
    ControlForwarder  func(...)
    InputForwarder    func(...)
    ModelResolver     orchestrator.ModelResolver
    Knowledge         knowledge.Retriever
}
```

**脆弱性分析：**
1. **`*runtime.Registry` 是具体类型而非接口** — 即使 `runtime` 本身只依赖 `domain`，`application` 反向依赖了 `runtime` 的导出类型。这导致 `application` 无法在不导入 `runtime` 的情况下编译，两条本应独立的包路径被硬绑定。
2. **98 个方法散布在 10 个文件中** — 任何新功能几乎必然修改 `Service`，合并冲突概率高。
3. **同时承担主叫和被叫角色** — 既调用 `store` / `dispatcher`，又实现 `runtime.EngineSink` / `scheduling.RunStarter`，接口契约双向蔓延。

**影响范围：** `httpapi`、`mcpserver`、`runnergateway`、`cmd/control-plane` 均直接引用 `*application.Service`。

---

### F2 — `application` ↔ `runtime` 双向接口耦合（次脆弱）

**位置：** `internal/application/` ↔ `internal/runtime/`

**证据：**
- `application.Service` 持有 `*runtime.Registry`（具体类型导入）
- `application.Service` 必须实现 `runtime.EngineSink` 接口（7 个方法）
- `runtime.ModuleRunner` 必须实现 `application.Dispatcher` 接口（1 个方法）
- 编译期验证：`var _ runtime.EngineSink = (*Service)(nil)`（`sessions.go`）
- 编译期验证：`var _ Engine = (*application.Service)(nil)`（`runnergateway`）

**脆弱性分析：**
两个包各自定义对方需要的接口，形成「隐式双向依赖」——虽然 Go 编译层面 `runtime` 不导入 `application`，
但语义上 `application` 必须满足 `runtime.EngineSink`，`runtime.ModuleRunner` 必须满足 `application.Dispatcher`。
任何一方接口签名变化，另一方必须适配。

**影响范围：** 新增 EngineSink 方法 → 必须同时修改 Service、Mock、测试；修改 Dispatcher 签名 → 必须同时修改 ModuleRunner、chainDispatcher、Gateway。

---

### F3 — `runnergateway` 承上启下的扇入耦合

**位置：** `internal/runnergateway/gateway.go`

**导入：** `application`、`domain`、`runtime`

**证据：**
```go
type Engine interface {
    RecordRunStatus(...)       // 7 个方法，与 runtime.EngineSink 高度重叠
    ...
}
var _ Engine = (*application.Service)(nil)
```

**脆弱性分析：**
`runnergateway.Engine` 接口与 `runtime.EngineSink` 接口有 **6/7 个方法签名相同**（`RecordRunStatus`、`RecordRunProgress`、`RecordRunEvent`、`RecordRunSessionUpdate`、`RecordRunUsage`、`RequestApproval`、`Run`），但定义在两个不同的包中。这意味着：
- 新增一个回调方法需要同时在两处加接口方法
- `application.Service` 必须同时满足两个接口
- 代码重复（相同签名，不同文档注释）
- 一处修改很容易漏掉另一处

---

### F4 — `cmd/control-plane/main.go` 单点装配

**位置：** `cmd/control-plane/main.go`（542 行，导入 20+ 内部包）

**脆弱性分析：**
这是唯一的装配根。它负责：
1. 创建所有 adapter 实例并注册到 registry
2. 构造 `Service`、`ModuleRunner`、`Gateway`、`Scheduler`
3. 设置回调闭包（`ApprovalForwarder`、`ControlForwarder`、`InputForwarder`）
4. 注入 `ModelResolver`、`Knowledge` 等可选依赖
5. 启动后台 goroutine（outbox、scheduler、HTTP server）

**任何新 adapter、新注入依赖、新后台服务都在这里修改。** 这个文件是发散式变更的典型——它因为不同的原因（加 adapter、改依赖、加后台服务）而频繁修改。

---

### F5 — `chainDispatcher` 的分派逻辑脆弱

**位置：** `cmd/control-plane/main.go:348-378`

```go
type chainDispatcher struct {
    gw      *runnergateway.Gateway
    modules *runtime.ModuleRunner
    store   application.Store
}

func (c *chainDispatcher) Dispatch(ctx context.Context, run *domain.ExecutionRun) error {
    // 1. 查 binding 得 adapterID
    // 2. 校验 runner 端 digest 与控制面一致
    // 3. 匹配则走 WSS → Gateway
    // 4. 否则走进程内 ModuleRunner
    // 5. 都不匹配则报错
}
```

**脆弱性分析：**
分派逻辑散落在装配层，而非集中在 `application` 内部。`chainDispatcher` 不是包级导出类型，而是 `main.go` 的私有结构体——这意味着：
- 无法被单元测试直接覆盖（只能通过集成测试间接测）
- 分派策略（远程优先 vs 本地优先）硬编码在 `main.go` 中
- 新分派目标（如 k8s runner、云 MCP 代理）需要改 `main.go`

---

### F6 — `persistence/sqlstore` 的领域知识泄漏

**位置：** `internal/persistence/sqlstore/store.go`

**导入：** `application`、`domain`、`scheduling`

**脆弱性分析：**
`sqlstore` 直接导入 `application` 包（因为实现了 `application.Store` 接口）。这是 Go 接口隔离的常见做法，
但导致了：`sqlstore` 知道所有 `application.Store` 的子接口——共 14 个 repo 接口。任何一个 repo 接口变化
（加方法、改签名），`sqlstore` 必须同步实现。而 `sqlstore` 同时还要实现 `scheduling.Store`（8 个方法）。

---

## 风险量化

| 耦合点 | 影响范围 | 变更频率 | 测试覆盖度 | 风险等级 |
|--------|----------|----------|------------|----------|
| F1: Service God 对象 | 全系统 | 极高 | 中（集成测试为主） | 严重 |
| F2: app ↔ runtime 双向接口 | 4 个包 | 高 | 高（编译期验证） | 高 |
| F3: runnergateway 接口重复 | 3 个包 | 中 | 高 | 中 |
| F4: main.go 单点装配 | 全系统 | 高 | 低（无单元测试） | 高 |
| F5: chainDispatcher 不可测 | 2 个包 | 中 | 低 | 中 |
| F6: sqlstore 知识泄漏 | 3 个包 | 中 | 高 | 低 |

## 改进建议

### 短期（1-2 周）

1. **将 `*runtime.Registry` 替换为接口** — 在 `application` 包内定义 `AdapterRegistry` 接口（仅含 `Get` 方法），
   消除 `application` → `runtime` 的导入依赖。这是改动最小、收益最高的解耦。

2. **合并 `runnergateway.Engine` 与 `runtime.EngineSink`** — 两接口语义完全一致，将 `runnergateway.Engine`
   替换为引用 `runtime.EngineSink`，消除接口重复。

### 中期（2-4 周）

3. **拆分 `Service` 为多个关注点** — 按聚合边界拆分为 `RunService`、`PlanService`、`AgentService`、`SessionService`，
   各自持有 `Store` 子集，不再共享同一个 God 对象。`Service` 退化为外观（Facade）或直接删除。

4. **将 `chainDispatcher` 提升为包级导出类型** — 迁入 `application` 或独立 `dispatch` 包，使其可独立测试。

### 长期（1-2 月）

5. **引入 `internal/dispatch` 包** — 封装分派策略（远程 Runner / 进程内模块 / 未来扩展），
   从 `main.go` 剥离装配逻辑。`cmd/control-plane` 只负责提供配置，不负责编排策略。

## 待验证假设

- 上述接口合并不会引入循环依赖（已验证：`runtime` 不导入 `runnergateway`，合并安全）
- `Service` 拆分后不影响现有事务边界（拆分后各子 Service 共享同一个 `Store` 实例，事务由 `InTx` 统一管理）
- 接口提取后不影响现有测试（编译期验证确保接口契约不变）