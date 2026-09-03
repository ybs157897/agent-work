# 架构评审：最脆弱耦合点分析

评审日期：2026-08-28
评审范围：agent-team-workbench 全量 Go 包（19 个 internal 包 + 4 个入口点）
评审方法：有向依赖图分析，扇入/扇出统计，层违例检测

---

## 执行摘要

本架构整体层结构清晰，Go 编译器强制无循环依赖，接口使用（`application.Store`、`runtime.EngineSink`、`scheduling.Store`）良好。但存在 **3 类脆弱耦合**，前两类在新增运行时适配器时会产生直接修改成本，属于架构层面的交付摩擦。

---

## 评审结果

### 发现摘要

| 严重度 | 数量 | 核心问题 |
|--------|------|----------|
| Critical | 1 | 应用层硬编码运行时适配器校验逻辑 |
| High | 2 | 配置层依赖应用层；传输层扇出过高 |
| Medium | 3 | 应用层扇入扇出双高；运行时 SPI 扇入过高；配置包间耦合 |
| Low | 2 | 知识库与 SSE 已解耦但可提取性未验证 |

---

### Critical 发现

#### CRIT-01：`application` 硬编码 `codexconfig` / `kimiconfig` 调用

**文件**：`internal/application/runs.go:411-438`

**问题**：`validateAdapterModel` 函数使用 `switch binding.AdapterID` 硬编码分派到 `codexconfig.ResolveBaseURL()` 和 `kimiconfig.ResolveBaseURL()`。这意味着：

- 添加一个新的运行时（如 `claude-code`、`zcode` 等）需要修改 `application` 包
- 添加一个新的运行时也可能需要修改 `application/runs.go` 的 import 块
- 违反 Open/Closed 原则：应用层应对扩展开放、对修改封闭

**根因**：`validateAdapterModel` 是适配器特定的模型校验逻辑，但被放在了通用用例层。`application` 不应该知道有哪些运行时适配器存在。

**影响**：每次新增运行时都必须修改核心应用层代码，增加回归风险。

**建议修复**：
1. 在 `orchestrator` 包中定义 `ModelValidator` 接口，由各适配器注册自己的校验函数
2. 或把 `validateAdapterModel` 下沉到 `runtime` 包，作为 `AdapterCapability` 的一部分

---

### High 发现

#### HIGH-01：`agentconfig` 导入 `application`（层违例）

**文件**：`internal/agentconfig/importer.go:8`

```go
import "github.com/ybs/agent-team-workbench/internal/application"
```

**问题**：`agentconfig` 是一个配置管理包（读取 `agents/<slug>/` 目录），理论上应只依赖 `domain`。但它直接导入 `application.Store` 接口，导致：

- `agentconfig` 无法在没有 `application` 包的情况下独立使用或测试
- 违反分层原则：下层（配置）依赖上层（应用）

**根因**：`Importer` 需要持久化接口，但直接引用了 `application.Store` 这个"大接口"。

**影响**：低（编译时正确），但阻碍模块化提取。要把 `agentconfig` 提取为独立模块，必须先解决此依赖。

**建议修复**：在 `agentconfig` 包内定义本地迷你接口，只包含 `Importer` 需要的 `Agents().List()`、`Agents().Create()`、`Agents().Update()` 方法。`application.Store` 满足该接口即可。

#### HIGH-02：`httpapi` 扇出 11 个内部包

**文件**：`internal/httpapi/server.go:15-20` 及 handlers 文件的导入

**依赖**：`agentconfig`、`application`、`domain`、`modelconfig`、`security`、`sse`、`agentwork/codexconfig`、`agentwork/kimiconfig`、`dshcatalog`、`orchestrator`、`agentwork`

**问题**：传输层（HTTP API）直接知道：

- 运行时配置路径（`agentwork`）
- 特定适配器的配置格式（`codexconfig`、`kimiconfig`）
- 目录结构（`dshcatalog`）
- 编排函数（`orchestrator.SupportsOutputContract`）

**根因**：散落在各 handler 文件中的辅助函数（DTO 转换、Slugify、权限预设校验等）没有被归集到共享位置。

**影响**：中。每次下层包接口变更，`httpapi` 都可能需要修改。当前规模下尚可管理，但会随系统增长而恶化。

**建议修复**：
1. `dto.go` 中的 DTO 转换逻辑可留在 `httpapi` 内（这是传输层的职责）
2. 工具函数（`slugify`、`ValidPermissionPreset` 等）提取到 `domain` 或新包 `internal/xutil`
3. 将 `httpapi` 对 `codexconfig`/`kimiconfig` 的依赖改为通过 `application` 间接调用

---

### Medium 发现

#### MED-01：`application` 扇入 6 + 扇出 7——中心枢纽

- 被导入：`httpapi`、`mcpserver`、`runnergateway`、`agentconfig`、`cmd/control-plane`、`cmd/atw-mcp`
- 导入：`domain`、`orchestrator`、`runtime`、`knowledge`、`scheduling`、`codexconfig`、`kimiconfig`

**问题**：`application` 是整个系统的交通枢纽。它既是底层依赖的汇聚点，又是上层调用的焦点。任何接口变更都会波及 6 个导入方和 7 个被导入方。

**风险**：`application.Store` 接口变更影响最大（`httpapi`、`mcpserver`、`runnergateway`、`agentconfig` 都依赖它）。

**建议**：保持 `Store` 接口稳定；新增方法时优先扩展而非修改。

#### MED-02：`runtime` 包扇入 11

**被导入**：8 个适配器 + `application` + `orchestrator` + `cmd/runnerd`

**问题**：`runtime` 是 SPI 定义点，任何接口变更（如 `EngineSink`、`Usage`、`SessionUpdate` 的结构变更）都会级联到所有适配器实现。

**影响**：低（SPI 本应稳定）。但要注意 `runtime` 包中若混入具体类型（如 `ModelSnapshot` struct），变更成本会高于纯接口变更。

#### MED-03：`agentconfig` 导入 `dshcatalog`

**文件**：`internal/agentconfig/agentconfig.go:14`

**问题**：两个领域相邻的配置包之间存在耦合。`agentconfig` 使用 `dshcatalog.ValidPermissionPreset` 做校验，但这个校验也可以放在 `domain` 或由调用方注入。

**影响**：低。在 19 个包的上下文中属于可接受耦合。

---

### Low 发现

#### LOW-01：`knowledge` 零内部依赖——最佳提取候选

**文件**：`internal/knowledge/`

**现状**：零内部依赖，仅标准库 + 通用接口。`knowledge.Retriever` 接口定义清晰。

**建议**：这是提取为独立 Go 模块的最佳候选，解耦成本为零。

#### LOW-02：`sse` 零内部依赖——已解耦

**文件**：`internal/sse/`

**现状**：纯 sync 标准库实现的事件 Hub。零内部依赖。

**建议**：无需操作。

---

## 依赖图摘要

```
Layer 0 (无内部依赖)        domain  knowledge  sse  outbox  agentwork
                                |
Layer 1 (仅依赖 domain)     scheduling  orchestrator  security  runtime
                                |
Layer 2 (依赖 L0+L1)        agentconfig*  modelconfig  codexconfig  kimiconfig
                                |
Layer 3 (应用层)            application**  ← codexconfig/kimiconfig (违例)
                                |
Layer 4 (传输/持久化)       httpapi***  sqlstore  mcpserver  runnergateway
                                |
Layer 5 (入口点)            cmd/control-plane  cmd/runnerd  cmd/atw-mcp
```

- `*` agentconfig → application（层违例）
- `**` application → codexconfig/kimiconfig（硬编码开关）
- `***` httpapi 扇出 11 包（高扇出）

---

## 风险矩阵

| 耦合点 | 变更频率 | 影响范围 | 修复难度 | 优先级 |
|--------|----------|----------|----------|--------|
| CRIT-01: validateAdapterModel | 新增运行时触发 | 高（改应用层） | 低（接口提取） | **P0** |
| HIGH-01: agentconfig→application | 偶发 | 中（阻碍提取） | 低（本地接口） | **P1** |
| HIGH-02: httpapi 高扇出 | 持续 | 中 | 低（提取工具函数） | **P1** |
| MED-01: application 枢纽 | 持续 | 高 | 高（需重构 Store） | P2 |
| MED-02: runtime 扇入 | 低（SPI 稳定） | 中 | N/A（需保持） | P3 |

---

## 修复建议与工期估算

### P0——提取 validateAdapterModel（1-2 人天）

**方案**：在 `orchestrator` 中定义 `ModelValidator` 接口，各适配器在 `init()` 或注册时注入校验函数。`application` 改为遍历注册列表调用。

**步骤**：
1. `orchestrator` 包新增 `ModelValidator func(spec ModelSpec) error` 类型
2. `orchestrator` 包新增 `RegisterModelValidator(adapterID string, fn ModelValidator)`
3. `codexconfig` 和 `kimiconfig` 各自实现并在 `init()` 中注册
4. `application/runs.go` 删除 `validateAdapterModel` 函数，改为调用 `orchestrator.ValidateModel(binding, spec)`

### P1——agentconfig 本地接口（0.5 人天）

**方案**：在 `agentconfig` 中定义 `AgentStore` 迷你接口，只包含 `List` / `Create` / `Update` 方法。

### P1——httpapi 工具函数提取（1 人天）

**方案**：识别 `httpapi` 中所有不依赖 `Server` 状态的纯函数，提取到 `domain` 或 `internal/xutil`。

---

## 待验证假设

- **假设**：`validateAdapterModel` 在 CRIT-01 修复后，新增运行时不再需要修改 `application` 包。
  **验证方法**：新增一个 mock 运行时，确认仅需在适配器包中添加注册代码。
- **假设**：`agentconfig` 的本地迷你接口不会引入与 `application.Store` 的方法签名不一致问题。
  **验证方法**：提取后编译检查，确认 `application.Store` 隐式满足该接口。