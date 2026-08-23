# M2：lead 作为 planner（plan 提取）+ 评估 run + 会话决策事件 + task_sessions 树形化 + consult_knowledge

日期：2026-08-23 · 状态：已实施（随实现同分支落地）· 对应 end-goal.md M2 + M3 动词接线
前置：M1（notes/implemented/orchestration/2026-08-23-m1-plan-executor.md）

## 决策

### 1. Plan 提取（agent 出谋，控制平面执政 的入口）

lead agent 的普通 run 产出文本即「谋」：控制平面在 run succeeded 时从助手最终文本
提取**最后一个** ```plan 围栏代码块，JSON 解码为 steps 数组，走 M1 SubmitPlan 同一
校验+执行路径（source_run_id=产出它的 run）。

- **门控**：仅当 agent.Role == "lead" 时尝试提取（显式角色约定，配置可见）。
- **解析失败**：不静默——在主任务上落 blocker（code=plan_parse_failed，message 带
  解析错误），work item 进 blocked，人可见可修。
- **无 plan 块**：正常 no-op（lead 可以只聊不派）。
- **幂等**：同一 run 的终态事件只提取一次（plan.source_run_id 唯一约束兜底）。

### 2. 评估 run 与 verdict

plan 的 `finish` step 扩展可选字段 `evaluation: true`：plan 落 finished 时，控制平面
自动在主任务上为 plan owner 创建一条**评估 run**——instruction 为确定性模板（主任务
验收标准 + 各子任务结果摘要 + verdict 输出格式要求），Input 固化 `evaluation=true`。

lead 的评估回复以 ```verdict 围栏块收尾：`{"pass": bool, "reasons": [...]}`。
- pass → 主任务 phase → acceptance（待验收，人等 Accept()——既有唯一完工路径）。
- fail → 主任务回 execution phase（BeginExecution），verdict reasons 落 activity；
  打回重做由 lead 下一份 plan 表达（M1 机制），控制平面不擅自重派。
- verdict 缺失/解析失败 → blocker（code=verdict_parse_failed），同 plan_parse_failed 策略。

### 3. 会话决策事件

CreateRun 会话决议（resume/rotate/fresh）目前只隐式体现在 conversation 键里。
新增事件 `session.decision`（AggregateExecutionRun）：data={tier: resume|rotation|inline,
reason: resume_hit|threshold|budget|config_drift|fresh|session_unknown, session_ref?}。
纯观测面，让「为什么换了会话」可查可审计（会话自识别分类器的 MVP 形态：规则已在，
本步把决策显式化）。事件白名单 + asyncapi + web EVENT_NAMES 三处登记。

### 4. task_sessions 树形化

迁移 0007：task_sessions 加 `parent_anchor_id TEXT NULL`。锚点创建时：work item 有
parent 且 parent 的（同 agent+adapter）锚点存在 → 记入 parent_anchor_id。
让会话树镜像任务树，轮换谱系可查。双方言迁移 + openTestDB 登记 0007。

### 5. consult_knowledge 动词（M3 检索包接线）

plan steps 新增第四个 verb：`consult_knowledge`，step 级字段
`{corpus: "prd", terms: [...], limit?: int}`。语义=**预取注入**：执行器调用
internal/knowledge.FileRetriever（root 从配置/环境变量 ATW_KNOWLEDGE_ROOT，
缺省 <workbenchRoot>/knowledge）检索，结果（条目 ID+标题+snippet）写入该 step 的
payload 执行结果；**与 dispatch 同 plan 时**，dispatch step 可引用前面的检索结果：
dispatch 增可选字段 `knowledge_from: <seq>`——把 seq 步的检索结果条目全文拼进
子任务 instruction 的「## 参考条目」节（确定性拼装，不经模型）。

Service 新增 `Knowledge knowledge.Retriever` 可选依赖（nil 时 consult_knowledge
step 落 failed，error=no_retriever——响亮失败）。

## 否决方案

- **plan 用 function-calling 通道而非文本围栏**：否决。adapter 层工具协议五家不一，
  文本围栏是最小公共面；提取失败有 blocker 兜底可见。
- **评估独立 run 类型列**：否决。Run 无 kind 列是现状惯例，evaluation 固化进 Input
  （对齐 wakeup/auto_heal_of）。
- **verdict fail 自动重派子任务**：否决。控制平面不替 lead 做决策（出谋/执政分界）。
- **lead 门控加 agent 新列**：否决。Role=="lead" 复用现有字段，不加 schema 变更。

## 验收

- plan 提取：lead run 文本含 plan 块 → 子任务+run 落库（source_run_id 正确）；
  非法 JSON → 主任务 blocked+blocker；非 lead agent 含 plan 块 → no-op。
- 评估：finish{evaluation:true} → 评估 run 自动创建（Input.evaluation=true、模板含
  验收标准）；verdict pass → phase=acceptance；fail → phase 回 execution + reasons 落
  activity；缺 verdict → blocker。
- session.decision：resume 命中/预算轮换/fresh 三路径各发一条正确 tier+reason。
- task_sessions：子任务锚点落 parent_anchor_id。
- consult_knowledge：检索结果注入 dispatch instruction；retriever nil 时 step failed。
- 触面验证：gofmt/build/vet + `go test -race ./internal/...` 触面包 +
  cmd/migrate 双跑 + web tsc（事件类型）。
