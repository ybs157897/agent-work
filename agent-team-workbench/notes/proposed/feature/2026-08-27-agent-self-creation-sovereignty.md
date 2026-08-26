# Agent 自建与自治（画框）终局草案：嵌套编排域 + recruit 词汇 + 无 MCP 终局

Status: proposed（讨论进行中，未定稿；实现细节与架构设计后续专题讨论）

讨论时间线：2026-08-27 与用户对谈沉淀。三个话题拼的是同一个东西的三个面——
**招聘**（框里的 agent 从哪来）、**运转**（框怎么自治）、**经济**（框花谁的钱、归谁管）。

## 愿景一句话

主体 agent 在运行中意识到某类任务对该用户高频出现，遂"画框"创建一个专职 agent
（包工头）：入职包由创建者起草（提示词/app-server 选择/模型配置/业务资产），框内
完全自治（可给自己写 Go 工具、可修订自己的提示词与模板），对外只暴露
dispatch/acceptance 契约——"我调用你，内部如何实现我不管，交付一个结果"。

## 核心模型三层

### 1. 框的本质 = 一棵子树（不是技术容器）

"框内随便写代码自己弄"不需要新机制——agent 在 harness 会话里写代码、跑代码是
执行面的日常。框 = **一块空间 + 一份预算 + 一个契约 + 一条谱系**。控制平面永远
不读框内、不热载框内代码（进程边界即沙箱边界）。

### 2. 包工头 = 子树内的 lead（模式可递归）

现架构的 lead 模式就是包工头模式：认领→规划（plan 5 动词）→派工（子任务树
parent_id）→评估（acceptance criteria）→返工（defer/join/supersede）→交付。
缺口不是新层，是**把 lead 从顶层专属推广到任意子树可嵌套**，配递归深度上限。

关键抉择（已定向）：框 agent "调 codex"的方式是产出 dispatch 动词由控制平面
代起 run（**经控制平面递归**），绝不框内直连 app-server 自管进程——后者等于
每个框长一个模型手搓的迷你控制平面，13 态状态机/resume 纪律/取消/审计全部失控。

### 3. 经济 = 预算信封 + 谱系 + 配额

- agents 加 `created_by` 谱系（work_items parent_id 先例）；
- 预算信封：usage 按 lineage 聚合，agent 级累计预算，超限框冻结（落 blocker
  不杀数据）；
- 创建配额 + 递归深度上限（防套娃）；
- 权限降级继承：自建 agent 权限 ≤ 父。

## 入职包（agent bundle）目录契约

招聘的产物不是一条配置，是一个完整目录：

```
agents/<slug>/
  agent.yaml            # 四轴：runtime(preferred/fallbacks/mode) + model ref + 权限 + approval_policy
  prompt.md             # 角色提示词（创建者起草）
  onboarding/           # 业务资产区（新增）
    plan-templates/     # 任务规划模板（如"开发者的任务规划"实例）
    thinking/           # 方法论资产（如"架构 agent 的架构设计思维图"）
    examples/           # 示例实例（few-shot 种子）
    knowledge.md        # 种子知识 / workspace 知识库引用清单
```

注入点唯一：onboarding 资产经 orchestrator 快照进 run.Input 或经
consult_knowledge 检索，无第二条私设通道——快照纪律（目录可变、运行不可变）
自动覆盖业务资产。

## 创建生命周期六环节

1. **意识**：任务模式频次信号（最弱一环；从同类 dispatch 计数/用户明示起步，
   远期做聚类面板）；
2. **起草**：创建者产出入职包草稿。runtime/模型选择有据可依——AdapterManifest
   能力矩阵与模型注册表（context_window）都是机器可读目录；
3. **评审**：草稿作为 artifact 走审批钩子（ApprovalRequest 复用，首期全人审，
   后按风险分级）；
4. **入职**：批准后 Importer 投影、指纹注册、立即可 dispatch（现有机制零新增）；
5. **试用**：前 N 次派工的 acceptance 通过率/成本/返工记账（usage 聚合到
   agent 维度）；不达标反馈创建者修订入职包——ConfigDigest 指纹规则自动强制
   新会话，自我改进循环的安全底座已存在；
6. **转正/解雇**：高频达标→常驻；低频超标→archive（墓碑停用）。招聘解雇对称、
   皆有审计。

## 无 MCP 终局（用户明确约束）

**终局不存在 MCP，也不是本项目范畴；不承诺任何工具协议兼容性（负向保证）。**

接口总数封闭为四条：

1. agent → 控制平面：**plan 词汇表**（出谋唯一通道）；
2. 控制平面 → agent：**dispatch + acceptance 契约**；
3. agent ↔ agent：**artifact 中转**（经审计）；
4. agent 内部：框内文件与二进制，纯私事。

由此替代原 MCP 挂点的两个设计：

- **创建写面 → `recruit` 词汇动词**：lead 在 plan 围栏里写
  `recruit(slug, bundle: artifact_ref, purpose)`；plan_extract 提取 → schema
  校验 → 审批 → Importer 实例化。词汇表 5→6 动词是一次正经 contracts 版本化
  变更，比旁开工具口更合"Agent 出谋，控制平面执政"。另配 `retire`（对称解雇）。
- **能力共享 → 能力即 agent / 产物即文件**：复用能力 = dispatch 给拥有它的
  agent（包工头递归）；或二进制+用法说明作为版本化 artifact 入共享区，消费方
  harness 直接 exec。发现机制 = 知识层（工具即知识条目），无注册中心、无协议层。

## agent 写 Go 代码的三条去处（全部无协议）

1. 一次性计算：框内 `go run` 拿输出（今日即有）；
2. 框内常驻工具：`go build` 产物存自己空间，harness 反复 exec；取消/超时/资源
   走执行面子进程管理（pgid 现成）；
3. 跨框共享：见上（能力即 agent / 产物即文件）。

共同点：Go 代码永远活在子进程里，从不进控制平面进程。编译型语言这个最不利
情况都无热加载地接住，架构对"agent 写代码"的容纳度即完备。

护栏：构建供应链（框内 vendored 模块/内部 GOPROXY，go.sum 变更进审批）；
源码+二进制（记 digest）一起 artifact 版本化；二进制无凭据，能摸到什么由框的
权限信封决定。

## 两把锁（仅当需要程序化策略时启用）

框内逻辑若要超出"数据模板"表达力（如 continue/new_topic 判定、子任务拆分
启发式），上**纯决策函数**沙箱：Starlark（首选，确定性）/ expr / wazero(WASM)。
输入=自己子树上下文快照，输出=动词序列，照常过执行器校验。

- 锁 1 执行隔离：解释器/WASM 无 I/O 无网络、可限时限量；
- 锁 2 权限隔离：作用域钉死自己子树、预算切块、降级继承、随父生命周期。

注意：多数场景不需要它——plan 模板（数据）覆盖八成需求，先模板后解释器。

## 与现状的距离（映射表）

| 愿景要素 | 现有机制 | 缺口 |
|---|---|---|
| 主 agent 派任务收结果 | dispatch/acceptance/artifacts | ✅ |
| 框内自治写代码 | harness 执行面 + 子进程管理 | ✅（零改动） |
| 包工头规划/评估/返工 | plan 5 动词 + 评估 run + defer/join/supersede | ✅ |
| 框可嵌套（任意子树 lead） | parent_id 树 + plans 表（数据模型支持） | ⚠️ 需验证/补全子树 lead 执行路径 |
| 画框一等化（recruit/retire） | plan_extract + 审批钩子 + Importer | ❌ 词汇表扩展 + bundle schema |
| 入职包业务资产 | orchestrator 快照 + 知识层 | ❌ onboarding 目录契约 |
| 预算信封/谱系/配额 | usage 按 run 记账 + 护栏模式 | ❌ lineage 聚合 + 信封扣减 |
| 自改入职包闭环 | ConfigDigest 指纹→强制 fresh（安全底座已在） | ⚠️ 修订审批 + 审计事件 |
| 高频意识 | usage 记账 | ❌ agent 维度聚合 + 频次信号 |

## 负向保证（钉死，不随实现讨论松动）

1. 词汇表封闭；扩张只经 contracts 版本化（recruit/retire 亦然）；
2. 控制平面不热载、不解释、不理解任何 agent 代码；agent 代码只活在子进程；
3. 框再自治，进出必走 run 状态机与审计——不留暗门；
4. 权限降级继承，自建 agent 永不越权于创建者；
5. 无 MCP、无工具协议、无注册中心（终局接口四条封闭）；
6. 墓碑纪律延伸：agent 停用不物理删（archive），谱系可追溯。

## 已登记的迁移问题（到时先改 end-goal 再动代码）

1. **atw-mcp（F5）退役路径**：现 task_claim/task_return 支撑认领模式；终局无
   MCP 意味着认领要么动词化、要么被直接派工模型吸收；
2. **agent 维度 usage 聚合**："意识"自动化的前提，也是预算信封的数据面。

## 未决问题（后续讨论清单）

- recruit/retire 动词 schema 细节（bundle artifact 的引用与校验形态）；
- 入职包审批的分级策略（什么条件免人审）；
- 预算信封扣减模型（预扣/实扣/透广策略）与深度上限取值；
- 沙箱技术选型（Starlark vs expr vs wazero）与启用时机；
- 子树 lead 现有执行路径的验证范围；
- 频次信号形态（dispatch 相似度怎么算）；
- 认领模式与 recruit 派工模型的关系（合并还是并存）。
