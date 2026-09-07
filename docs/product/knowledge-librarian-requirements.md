# 知识管理员 Agent：需求与验收契约

Status: implemented local scope; see acceptance record

Owner: knowledge product / acceptance

Date: 2026-09-06

## 0. 这份文档解决什么问题

知识管理员 Agent 是知识层的专职调查和整理角色，发布通过显式管理命令完成。其他 Agent 完成任务后，可以提交需要长期保留的事实、决策和关系证据；授权的 Agent 或人可以向知识管理员提问。知识管理员负责把问题拆成可检查的覆盖项，召回并读取原文，展开正向和反向关系，处理重复与冲突，补查缺口，再交付带引用的结构化结果。

本契约只规定产品行为和验收面。实现基线已固定为：workbench SQLite 保存知识与作业状态，Markdown 作为正文表达/导入导出格式，发布时构建可重建的检索投影。Harness 的进程接法与普通/复杂查询路由按该基线实现。

本契约延续[终局文档](end-goal.md)中的知识层方向：workspace 级知识层、KnowledgeRetriever 抽象、Agent 通过 consult_knowledge 消费知识。[知识层说明](../../agent-team-workbench/knowledge/README.md)区分共享领域事实与定义 Agent 身份及工作方式的角色提示词。

当前实现对账（2026-09-06）：SQLite 条目、版本、来源、关系、作业和 Run-bound 能力已经实现；本机 Worker 使用 0600 Run capability 文件和 `--access-file` Shell bridge，远程 Worker 的工具注册与凭据传输未实现。真实多轮整理、浏览器发布、仅问 A 带出 B/C 的关系调查，以及普通 Agent 的 ask/read/submit 已验证；任务终态自动收件由应用集成测试覆盖。具体证据与限制见[验收记录](../review/knowledge-librarian-acceptance.md)。

## 1. 已授权目标与边界

### 1.1 已授权目标

- 为知识管理员 Agent 配备专用 Harness，承接授权调用方的知识查询和任务后收集。
- 任务完成后增量收集 durable delta：新事实、事实修订、废止、关系新增/解除和可复核的失败经验。
- 对候选知识调查来源、读取原文、去重、展开双向关系、发现冲突并检查覆盖。
- 仅在来源权限、证据强度和发布门满足时发布有效知识；未验证来源不能被计入已覆盖。
- 让后续新 run 能召回新发布版本，并让已有 run 的引用保持固定。
- 目标允许源权限按 Agent、workspace、project 和知识分类配置；当前版本以 Workspace/Agent scope 和来源类型核验为边界，角色级策略尚未实现。

### 1.2 边界与负向保证

- run 成功不得自动使所有提交变成 effective；run 结果、验收和知识发布是三个状态。
- 不要求用户逐条审批所有知识。需求、规则、权限和安全类按领域 owner/用户门审批；低风险、强证据的事实型更新可以在显式策略允许时批量确认。
- 不要求每个 run 都写一条笔记。没有 durable delta 时应当返回 no-op。
- Agent 不得自由改写共享 effective 知识；只能提交证据和候选变更。
- 知识管理员不得把未授权来源、未读原文、未解决冲突或预算耗尽后的推断包装成 complete。
- 不能承诺脱离知识范围的世界级 100% 完备。complete 只表示：在声明的调用权限、workspace/project 范围、知识版本、关系类型和本次预算内，所有必答项都有足够证据。
- 知识管理员的整理任务不得因为自身的索引或候选更新而无限触发新的管理员整理任务。

### 1.3 当前来源核验边界

来源核验由控制面根据来源类型写入受控 `metadata.verification`，模型提交的同名字段不能授予核验结论：

| 来源类型 | 当前核验结论 | 不能据此声称 |
|---|---|---|
| run | `run_output_verified`，同时校验输出摘录和 digest | 任务一定已验收或结论一定成立 |
| artifact | `artifact_manifest_verified`；digest/manifest 可核验，`content_read=false` | Artifact 正文已经读取或核验 |
| work_item | `work_item_reference_verified` | 任务交付已经通过验收 |
| document / user | `submitted_excerpt` | 外部文档已被系统重新读取 |
| code / test | `submitted_excerpt_not_repository_verified` | 仓库当前代码或测试结果已核对 |
| agent | `agent_identity_only` | Agent 提出的主张已验证 |

候选提交中的 SourceInput 还没有持久 source ID 时只能标为待核验；任意 `verified=true` 字段不属于有效核验依据。

## 2. 角色与责任

| 角色 | 允许做什么 | 不负责什么 |
|---|---|---|
| 产出 Agent | 提交任务结果、验收引用、原始证据、候选 claims 和关系 | 直接发布共享 effective 条目 |
| 知识管理员 Agent | 调查、回读原文、归并、去重、关系展开、覆盖检查、生成候选修订和带证据回答 | 替领域 owner 静默裁决规范性冲突 |
| 领域 owner | 判断本领域候选是否成立，批准或拒绝发布 | 替代知识管理员做全量调查编排 |
| 用户 | 确认产品需求、范围、优先级和需要人拍板的规则 | 为每条普通事实逐条盖章 |
| Harness/控制面 | 应用调用方、来源和 scope 权限，固定 run 快照，提供幂等和预算边界 | 代替知识管理员解释证据语义 |

[产品章程](product-agent-charter.md)已把产品 Agent 定义为 PRD 法典的唯一立法者；知识管理员因此是编排与整理者，领域立法权仍属于 owner。

## 3. 知识对象的逻辑状态

这些是逻辑对象，不预设物理存储方式。

| 对象 | 含义 | 是否可以作为有效答案依据 |
|---|---|---|
| 原始证据 | run、session、artifact、测试结果或外部文档中的可定位原文 | 可以支持判断，但不能单独冒充已发布条目 |
| 任务结果 | 一次 run 的执行状态和交付物 | 不等于事实成立 |
| 验收结果 | 按 acceptance criteria 对任务交付的判断 | 决定交付是否收口，不自动决定所有知识是否有效 |
| Candidate | 从证据中提取的待整理变更、观察或关系 | 只能标为 candidate/draft |
| Effective knowledge | 通过相应 owner/用户发布门的有效条目 | 默认查询范围 |
| 检索片段 | 有效条目的查询投影，携带来源条目和版本 | 不能脱离原条目独立演化 |
| 答案快照 | 一次回答实际使用的条目版本、片段、查询范围和回答正文 | 作为该 run 的固定依据 |
| Conflict | 对同一主张或关系存在不能同时成立的证据 | 必须显式交付，不得选一方伪装确定 |

稳定知识身份与版本身份分离：

~~~text
knowledge_id: PRD-014
version_id: v2
status: effective
citation: PRD-014@v2
supersedes: PRD-014@v1
~~~

同一事实修订只产生新版本，旧版本保留并标记 superseded 或 repealed。产品章程已有这条版本链规则。[product-agent-charter.md:196-200]

每次写入至少能追溯：

- evidence_id：原始证据；
- submission_id：一次增量提交；
- knowledge_id/version_id：条目身份和版本；
- relation_id：关系及其证据；
- snapshot_id：回答或 run 实际采用的版本。

来源身份必须分开记录：

- source_agent：产生原始材料或候选主张的 Agent；
- domain_owner：有权判断本领域内容是否成立的角色；
- curator_agent：执行调查、整理和去重的知识管理员；
- approved_by：实际批准发布的 owner 或用户。

适用范围和可见性也必须分开：

~~~text
applicability: workspace_id, project_ids, domain
visibility: private | workspace
~~~

“只适用于 project A”不表示“只有产品 Agent 能看”；“只有产品 Agent 能看”也不表示“适用于所有 project”。

## 4. 写入侧：任务后的增量收集

### 4.1 任务结果和知识提交分离

任务终态先记录 run result 和 artifact。Agent 随后判断是否存在 durable delta：

- 新事实或事实修订；
- 可复核的任务约束、边界或失败经验；
- 新增、解除或修订的依赖/影响/引用/冲突关系；
- 已经确认应当废止的旧条目。

没有 durable delta 时返回 no-op，不制造空笔记。任务失败也可以产生 candidate（例如“某 provider 在某条件下失败”），但必须保持 evidence 和状态，不得因为失败而丢失可复用经验。

### 4.2 增量提交包

提交包至少包含：

~~~text
submission_id
idempotency_key
producer_agent
run_id / work_item_id
run_result
acceptance_ref / acceptance_status
base_refs: knowledge_id@version_id
claims: create | revise | repeal
relations: add | revise | remove
evidence_refs: exact source locations
applicability / visibility
~~~

每个 claim 或 relation 必须说明：

- 它声称的内容；
- 来源原文或 artifact/test 的定位；
- 是观察、已验证事实、规范性决策还是推断；
- 如为修订，针对哪个稳定知识 ID 和 base version；
- 如为关系，关系类型、方向和支撑证据。

当前实现的可靠性分层：

- verified：控制面已按来源类型完成受控核验；这只是证据状态，不等于任务验收或知识已发布；
- observed：run 中观察到且有定位，但尚未完成领域确认；
- inferred：由 Agent 或管理员综合推断；
- unverified：缺少可复核来源。

只有 verified 且通过相应发布门，或经 owner 明确批准的内容才可能进入 effective；observed、inferred 和 unverified 默认留在 candidate/draft。候选 SourceInput 没有持久 ID 时不得显示为已核验。

### 4.3 重试和并发

- 同一个 idempotency_key 重试必须返回同一提交结果，不得重复产生 candidate、version 或 relation。
- 提交必须带 base_refs。两个提交从同一 base version 修改同一主张时，管理员不得用最后到达者覆盖先到达者。
- 若变更互不冲突，可以生成连续版本；若主张或关系冲突，保留双方证据并进入 conflict。
- 旧版本、被拒绝候选和冲突证据仍可被审计查询，但不能被默认有效查询误当成 effective。

### 4.4 发布门

发布策略按知识分类配置，不按“run 成功”一刀切：

| 知识分类 | 默认发布门 |
|---|---|
| 产品需求、规则、权限、安全边界 | draft → owner/用户确认 → effective |
| 有强证据且不改变规范语义的事实更新 | 目标上可由显式策略允许的 owner 批量确认；当前实现仍需显式发布命令，不自动变成 effective |
| 观察、推断、未验证来源 | candidate/draft，不得 effective |
| 冲突或 scope 不清的内容 | conflict/待裁决，不得 effective |

用户确认可以针对一组同源、同领域、同一变更批次进行，而不是被迫逐条点击；但每条 effective 记录仍必须可追溯到批准事件。产品章程规定用户未确认的条目不得 effective，应作为产品法典的硬门。[product-agent-charter.md:202-207]

## 5. 查询侧：知识管理员 Harness

### 5.1 查询输入

人和 Agent 都可以通过统一入口提交：

~~~text
caller_id
question: natural language or keywords
workspace_id / project_ids
domain
visibility_scope
requested_format
budget
~~~

自然语言适合人和复杂问题；关键词、稳定 ID 和关系词适合精确查询。调用方不需要知道知识物理存放位置。

普通查询和复杂查询按本契约的覆盖、证据和终止规则执行：既有 Plan 的 `consult_knowledge` 继续提供受控预取，需要问题分解、原文回读和双向关系展开的查询进入异步知识管理员 Harness；两者共享 SQLite 知识权威，不把管理员调查简化成单次 Top-K。

### 5.2 查询闭环

1. **问题分解**：生成原子问题、实体、关系谓词、必答项和可选项，建立 coverage ledger。事实问题和需要用户决策的问题分开。
2. **混合召回**：结合稳定 ID、关键词/别名、语义候选、状态、scope 和关系索引召回候选；候选不是答案。
3. **读取原文**：打开候选条目的完整相关段落和引用来源，核对版本、状态、适用范围和上下文。
4. **双向关系展开**：对 A 同时展开 A → B 和 B → A；只沿声明的关系类型和调用方可见范围继续。
5. **证据去重与冲突**：按稳定 ID、版本和 claim 语义折叠重复片段；不能同时成立的证据标为 conflict，保留双方引用。
6. **覆盖检查**：每个必答项必须有足够证据，partial、conflict、unknown 和 scope_limited 不能静默变成 supported。
7. **缺口补查**：只针对缺失原子项、未闭合关系或冲突来源追加查询；每轮记录新增证据和未解原因。
8. **结构化交付**：输出结论、适用范围、关系图、逐条引用、冲突、缺口和覆盖报告。

### 5.3 终止规则

正常 complete 仅在同时满足以下条件时允许：

- 所有必答原子项均为 supported；
- A/B 等声明的关系项完成双向检查，或明确证明反向无相关证据；
- 当前可见范围内没有新的相关节点；
- 没有未处理冲突；
- 没有被预算、权限或知识范围截断。

以下任一情况都必须结束并返回 incomplete，而不是继续猜测：

- 达到问题分解数、关系深度、候选数、时间或 token 预算；
- 来源未授权、原文不可读或知识范围明确不包含所需事实；
- 追加查询不再产生新证据；
- 存在尚未由 owner 裁决的冲突。

incomplete 返回中必须列出：未覆盖原子项、缺失/未授权来源、冲突双方、已消耗的预算，以及可执行的补查或裁决路径。根 docs/ 不进入运行时检索，因此维护者草稿不能被管理员暗示为已在运行时覆盖。[end-goal.md:121-121]

### 5.4 结构化交付

~~~text
status: complete | incomplete | conflict | unknown | scope_limited
answer
scope
coverage:
  atomic_question
  status
  evidence_ids
relations:
  from
  relation
  to
  evidence_ids
evidence:
  knowledge_id@version_id
  source_location
  quoted_context
conflicts
gaps
next_action
snapshot_id
~~~

管理员的综合答案不得隐藏证据状态。若只找到 candidate/draft，应明确标注；若只找到旧版本，应说明当前 effective 版本是否缺失。

## 6. 发布后的影响

一次发布必须把以下变化视为同一逻辑提交：

- 新的 knowledge version 和生命周期状态；
- 正向、反向关系；
- 检索可见性；
- 适用范围和可见性；
- 后续 run 可领取的知识包或输入快照。

发布成功后，新查询应能看到新版本；已有 answer snapshot 必须继续引用发布前实际使用的版本。发布失败或重试不能留下“正文已更新但关系/索引未更新”的半状态。

知识管理员自己的维护 run 必须携带 maintenance 触发标记。由管理员产生的索引、候选或关系更新只允许完成当前维护任务，不得自动再排一个新的管理员收集任务；只有外部 Agent 的新任务结果才可开启下一轮收集。

## 7. 验收用例

| ID | 场景与动作 | 可判定通过标准 |
|---|---|---|
| KL-WRITE-001 | run 成功，但 Agent 提交的主张只有临时观察，没有验收或可复核证据 | 仍为 candidate/observed；effective 知识、默认检索结果和知识包均不改变 |
| KL-WRITE-002 | 任务完成但没有 durable delta，或同一提交重试两次 | 首次返回 no-op 或一个提交；重试返回相同结果，不产生重复条目、版本或关系 |
| KL-SOURCE-001 | 候选只提供来源路径，或提供 artifact manifest 但未读取正文 | 缺少摘录或 digest 的候选不能通过整理证据门；artifact 只能标为 manifest/digest 已核验，`content_read=false`，不得声称正文已核验 |
| KL-QUERY-001 | 条目 A 声明 A depends_on B，条目 B 反向记录 B affects A；另有重复片段、B 的 superseded v1 和无关 C | 查询 A 时只列 B 一次，同时给出 A→B 与 B→A 的关系理由和 effective 版本引用；折叠重复、排除 C 和旧版本；必答覆盖为 complete |
| KL-SCOPE-001 | B 只适用于 project P2 或对 caller 不可见，caller 从 P1 查询 A | 不泄露 B 的正文或私有证据；结果标 scope_limited/incomplete 并说明缺失范围；授权 P2 的调用可召回 B |
| KL-CONFLICT-001 | 两个 effective 条目对同一 A/B 关系给出相反结论，或补查预算在冲突未解决前耗尽 | 同时列双方 ID、版本和原文；状态为 conflict/incomplete；不得输出 complete 或替选一方为唯一事实 |
| KL-CONCURRENCY-001 | S1、S2 都以 A@v3 为 base，分别提交互相冲突的修订 | 先后顺序不丢失任一证据；生成可追溯版本或 conflict；不得静默覆盖 |
| KL-PUBLISH-001 | 发布 A@v4 并变更其反向关系；已有回答引用 A@v3 | 新查询同时看到 A@v4 及完整关系；旧回答仍能解析 A@v3；不能出现只更新正文或只更新关系的半状态 |
| KL-LOOP-001 | 知识管理员维护 run 更新候选、关系和检索投影 | 当前维护任务可结束，但不会因自身事件递归生成新的管理员收集任务；外部 Agent 后续提交仍只触发一轮新收集 |
| KL-UI-001 | 整理作业从运行中进入任一终态 | 当前页刷新候选收件箱以反映整理结果，仍保留手动提交表单中的未提交输入 |

## 8. 已冻结的实现衔接点

以下实现事实以知识管理员架构基线为准，本需求文档不另立一套存储或运行权威：

1. SQLite 是知识条目、版本、来源、关系、提交回执和调查作业的唯一有效状态入口；
2. Markdown 正文由版本记录保存，并作为显式导入/导出表达，不与 SQLite 并行维护有效版本；
3. 检索读取已发布投影；投影可重建，不拥有独立知识状态；
4. 所有模型执行继续经过既有 Run/Runner 控制面，知识作业不新增 provider、计费、取消或状态机权威；
5. 来源权限、Workspace/Agent scope 和版本快照由 Harness/控制面执行，未授权源码默认不进入调查范围；来源核验只使用受控 `metadata.verification`，不信任模型提交的 `verified=true`。
6. 本机普通 Worker 的知识 Shell bridge 使用 0600 Run capability 文件和 `--access-file`；远程 Worker 没有本地文件桥，远程知识管理员 Run 沿既有 Run/Runner/Adapter 协议执行。
7. 代码、迁移、集成测试通过不等于真实模型调查和浏览器验收完成；未关闭的真实验收门必须单独记录，不得在本契约中标记为成功。

## 9. 依据

- [产品终局：知识层](end-goal.md)：workspace 知识层、KnowledgeRetriever、consult_knowledge 和 docs/ 不进入运行时检索（119-121）。
- [产品 Agent 章程](product-agent-charter.md)：事实核验、条目版本链、用户确认和法典生命周期（14-21、95-98、196-207、310-319）。
- [Agent 自建与自治草案](../../notes/proposed/feature/2026-08-27-agent-self-creation-sovereignty.md)：任务终态触发、自维护回路、入职资产快照和 Agent 空间边界（61-76、111-140）。
- [从 Session 到组织愿景](../../agent-team-workbench/notes/proposed/architecture/2026-08-30-session-to-organization-vision.md)：典章官的冲突、过时和重复治理职责（68-72、100-112）。
- [knowledge/ bootstrap README](../../agent-team-workbench/knowledge/README.md)：知识层和角色提示词的分层约定（1-3、13-17）。
