# 全员 Kimi 冒烟两轮复演：问题点标记与修复契约

Status: implemented（验证轮通过，见文末「验证轮实测」）

## 证据来源

两轮独立复演（均全员 kimi_local + kimi-2-7），问题点全部从工件取证：

- 用户轮（2026-09-03 07:07–07:09 UTC）：任务 `wi_01M1K1GJ59XCDVVX618BCHJBKA`，
  Coordinator runs `run_01M1K1GJ5G…` / `run_01M1K1JS03…` / `run_01M1K1KZ43…`，
  Forge worker run `run_01M1K1KAEQ…`（子任务 `wi_01M1K1KAEP…`）。
- 基线轮（2026-09-03 08:02–08:11 UTC，本分支自驱动 API 发布）：任务
  `wi_01M1K4NHT2NSXWD9SS3CSD1RCQ`，runs `run_01M1K4NHTJ…` / `run_01M1K4QRDKECA…`
  / `run_01M1K548Z4QR…`，Nova worker run `run_01M1K4R74C…`（子任务
  `wi_01M1K4R74A…`）。

两轮均走完整链路：intake → 阻塞#1 → 人工解除阻塞 → 重规划 → dispatch → worker
成功 → settlement 唤醒 → finish{evaluation:true} → 阻塞#2。

## 问题点

### P1 kimi-2-7 首轮决策稳定违反 barrier 终态规则（2/2 复现）

两轮的首个 Coordinator 决策都输出 `[dispatch, join, finish]` 三步，被
`validatePlanDecisionSemantics`（plan_decisions.go:189）以
`join/defer barrier must be the final step`（/steps/1）拒绝。

- 证据：blocker `blk_01M1K1HAS3…`（07:08:07）、`wi_01M1K4NH…`  blocker（08:03:20），
  两轮 run1 的 message.completed 原文结构完全一致。
- 提示词已写明规则（coordinator_prompt.go:34 "finish and barriers are terminal
  steps"），模型仍稳定违反——属模型契约遵循问题，但当前代价是**每个任务首轮
  必然浪费一个 turn 并阻塞等人工**。
- 正确姿势是两段式：本轮 `[dispatch, join]`，settlement 唤醒后再 `[finish]`。
  两轮恢复轮 Kimi 都能在拿到校验错误反馈后自我纠正（coordinator.plan_updated
  事件的 reason 均明确复述规则）。

### P2 finish{evaluation:true} 评估 run 缺 RuntimePreference（2/2 复现，本轮主修复）

main 的 plan.go finish 分支创建评估 run 时 `CreateRunParams` 不传
RuntimePreference。评估 run 的 owner 是系统 Coordinator（plan.agent_profile_id），
候选解析（orchestrator.ResolveRuntimeCandidates）在显式偏好 nil + Coordinator
profile `runtime_preference='{}'` 下只剩兜底 `mock`，撞 runs.go:441 的
「禁止静默回退」守卫（config runtime_label=kimi_local ≠ mock）→
`创建评估 run: domain validation failed: Coordinator 配置的 Runtime 不可用，禁止静默回退到 mock`。
事务回滚，评估 run 不落库（两轮均实测 eval_runs=0），任务 blocked。

- 证据：blocker `blk_01M1K1M5JZ…`（07:09:40，未解决）、`wi_01M1K4NH…` blocker
  （08:11:06）；task_coordinator_configs 取证 runtime_label=kimi_local；
  runtime_bindings 取证 kimi_local binding status=ready（带上偏好即可建成）。
- 对比：Coordinator 自身轮次由 startCoordinatorTurn 显式传
  coordinatorRuntimePreference(config)，所以三个 Coordinator run 正常。

### P3 语义校验错误无自动修复，必须人工解除阻塞（2/2 连带复现）

`handleCoordinatorPlanDecisionFailure`（coordinator_plan_repair.go:205）只对
plan_json_syntax / plan_schema_validation 走 `scheduleCoordinatorPlanRepair`
自动修复；plan_semantic_validation 直接 blockCoordinatorPlanDecision 等人工。
P1 这类屏障位置错误是模型高频可自愈错误（反馈即可纠正），现状却是
「每任务至少人工介入一次」，无人值守主路径不成立。

- domain 已预留 `CoordinatorRepairErrorSemantic` 常量（task_coordinator.go:122），
  但 `ValidateRepair` 的 pending/exhausted 分支只允许 syntax/schema——
  设计预留了语义类修复但被门禁关掉。
- 修复预算既有：coordinatorMaxPlanRepairAttempts=2，耗尽才落
  coordinator_plan_repair_exhausted 人工阻塞。

### P4 评估 run 创建失败被误分类为 plan_semantic_validation（2/2 复现）

runs.go 守卫抛 `ErrValidation`（基础设施/配置类），经
`classifyPlanSubmissionError`（coordinator_plan_repair.go:148）默认桶标成
`plan_semantic_validation`——把控制面配置缺陷伪装成「模型计划语义错误」，
误导排查，且在 P3 修复后会被错误地送进模型自动修复（模型修不了配置问题）。

### P5 dispatch 子任务无任何完成路径，滞留 in_progress/review（2/2 复现）

- worker run succeeded → runs.go:1426 通用联动把子任务带入 in_progress/**review**
  投影（两轮子任务实测均停在此态）；
- `handleCoordinatorWorkerTerminal`（coordinator_engine.go:1509）只更新
  coordinator state，不动子任务；
- `finishPlan`（plan.go:854）只落 plan 终态，不级联；
- 根任务 `AcceptWorkItem`（service.go:1312）明确「coordinated child Task 不能
  单独验收」，但验收根任务时也不级联子任务。
- 结果：子任务永远滞留，看板累积僵尸 in_progress。

## 修复契约（本分支范围）

- **F1（治 P2）**：finish 分支评估 run 的 RuntimePreference 从
  `coordinatorRuntimePreference(config, useFallback)` 派生，与
  startCoordinatorTurn 完全同源；useFallback 由
  `planEvaluationCoordinatorContext` 单一事实面携带（state.Data["use_fallback"]），
  runtime 与 ModelRef/reasoning 成对固化，不引入分叉；Preferred binding 行缺失时
  fail-closed（ErrValidation "未配置"），不做 adapter 探测、不静默改选。
  旧分支（codex/fix-eval-run-runtime-preference，已删）的 v1/v2 方案与决策
  note 已完整取证，本分支按当前 Task Coordinator 架构重新实现。
- **F2（治 P4）**：步骤执行期的基础设施错误与计划语义错误分流——评估 run 创建
  失败经 classifiedPlanSubmissionError 新类（execution）映射为独立 blocker code
  `plan_execution_failed`，走人工阻塞（不自动修复），message 保留原始原因。
- **F3（治 P3+P1）**：plan_semantic_validation 纳入自动 repair（复用既有两次
  预算与 exhausted 落点）；ValidateRepair 放行 semantic 类；同步强化提示词
  barrier 条款（明示「join/defer 与 finish 不得同决策，finish 在屏障 settle
  后的后续轮次单独发出」）。authority/quota 维持人工阻塞不变。
- **F4（治 P5）**：根任务 AcceptWorkItem 验收通过时级联验收 dispatch 子任务
  （status=in_progress 且 phase∈{review,acceptance} 的直系子任务走 Accept()，
  同事务 + 完成事件），遵守「Accept 是唯一完工路径」的领域不变量；
  非 review 投影的子任务不强行关闭。

## 明确不做

- 旧分支的 ```plan/```json 围栏解析两刀（820ef69/cff1a64）针对的是 lead-planner
  的 plan_extract.go 链路，与本轮 Task Coordinator（PlanDecisionV2）链路无关，
  不带入本分支。
- 不放宽「禁止静默回退 mock」守卫；不改 orchestrator.DefaultRuntimeLabel。
- worker 选型措辞敏感（基线轮 Coordinator 选了 Nova/pm 而非 Forge/developer）
  与 worker run 时长随 instruction 膨胀（6.5min vs 16.8s）属任务描述问题，
  非系统缺陷，不立项。

## 验证

- 每刀配防回归测试（F1 四用例精神移植：继承主 runtime / 跟随 use_fallback /
  主 binding 未就绪 fail-closed / Preferred binding 缺失 fail-closed；
  F2 分类断言；F3 语义错误自动 repair + 耗尽落点；F4 级联验收）。
- 触面：`go build ./... && go vet ./... && go test -race -count=1 ./internal/...`。
- 修复后重启 control-plane 跑第二轮全员 Kimi 冒烟（同 API 发布路径），
  对照 turn 日志确认 P1 自愈（无人工 unblock）、P2 评估 run 建成、
  P5 子任务随验收关闭。

## 验证轮实测（2026-09-03 18:43–18:45，修复后二进制 + 主库副本）

环境：worktree 构建的 control-plane（本分支 HEAD）跑主库 `sqlite3 .backup`
副本（已应用 0043 迁移），运行时/凭据/host-registry 均自主树解析，无污染主库。

任务 `wi_01M1KDVRBRQ85HXQS88CHRDB8C`（干净冒烟，同验收标准）全程 **57 秒**
（10:43:32 创建 → 10:44:29 waiting_user），**零人工介入**：

| 环节 | 结果 | 证据 |
|---|---|---|
| intake 首个决策 | ✅ 一次通过严格校验（无 barrier 违规） | `coordinator.plan_updated`（run `run_01M1KDVRC0…`） |
| dispatch → Worker | ✅ Sentinel run 5.5s 成功 | `run_01M1KDYWDYM…`（子任务 `wi_01M1KDWDYK…`） |
| settlement 唤醒 | ✅ | `run_01M1KDWTMP…` |
| finish{evaluation:true} | ✅ 评估 run 以 **kimi_local** 建成并 succeeded | `run_01M1KDX3SV…`（input.evaluation=1，runtime_label=kimi_local） |
| 评估 verdict | ✅ `{"pass": true}`，逐条对照验收标准 | message.completed |
| 验收级联 | ✅ Accept 根任务后子任务同步 completed | 两 work_items 行 + coordinator state completed/acceptance |

P1 说明：本轮 Kimi 首发决策即合规（提示词强化生效的单一数据点，不能证明
根除）；F3 自动修复链路与 F2 fail-closed 分类由集成测试覆盖（语义修复/耗尽
落点、binding 未就绪/缺失四用例），本轮冒烟未触发属预期。

遗留观察（非本分支范围）：首个被环境污染的验证任务里，Coordinator 把「运行时
凭据缺失」（实为验证 harness cwd 错误导致的瞬态故障）按合同判为非可重试并
defer 35 分钟——Coordinator 无法区分环境瞬态故障与真实认证失败，其自身轮次
刚用同一凭据成功也未被用作反证。记为后续改进候选。
