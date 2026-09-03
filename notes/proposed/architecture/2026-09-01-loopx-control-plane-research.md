# LoopX 调研报告：长任务控制面架构对 agent-team-workbench 的价值

> 日期：2026-09-01
> 调研对象：[huangruiteng/loopx](https://github.com/huangruiteng/loopx)
> 事实底稿：`/Users/yin/orca/workspaces/gh-research/loopx-long-task-review/RESEARCH.md`（gh-research 通道产出，含逐条源码 URL）
> 触发问题：我们的 coordinator（lead planner）在规划任务时反复遇到模型 JSON 输出问题；loopx 同为长任务项目，评估其架构对我们是"迁移当底座"还是"参考借鉴"。

---

## TL;DR（结论先行）

**推荐方案 B：参考借鉴，不做整体迁移。**

1. loopx 对我们最有价值的一句话：**规划的真相不要经过不可信的 LLM JSON**。它的 coordinator 不让模型吐计划 JSON 当真相——权威计划由内核从已提交状态**确定性投影**（`llm=no_api`），模型输出只是建议；JSON 解析失败被设计成 typed 路由（`contract_error` → repair/replan 义务），而不是"重试解析同一段 JSON"。这直接治我们的病。
2. **迁移当底座不成立**：loopx 是 3 个月大（2026-05-31 创建）、单作者约 95% 提交占比的 Python+TS 双语言项目；迁移等于换掉我们已验证的 Go 13 态状态机、ModuleRunner、codex/kimi adapter 层、SSE 恢复链路和整个前端——强弱倒挂。
3. 按投入产出排序，值得借的四个机制：**规划投影与调度权威分离、确定性 action packet 取代 LLM 计划 JSON、contract_error/repair/replan 一等路由、delivery_outcome 分类防假进展**。全部能在现有 Go+SQLite 栈内落地，且与在建的任务控制面（worktree `codex/task-control-surface`）同向。

---

## 1. loopx 是什么

跨 harness 的 **local-first 长程 Agent 控制面**：objectives、todos、gates、evidence、quota、handoff 全部外置为持久状态，harness（codex-cli / claude-code / generic-cli）只跑有界 Turn。

核心架构（据 RESEARCH.md §1）：

- **六层持久面**：Registry / Goal state / Run log / Run history / Status / Quota。
- **Effect interpreter 循环**：每 Turn = `quota should-run` 决策 → 内核编译 typed packet → host 有界执行 → settlement（validation → writeback → spend 分阶段）→ 纯函数 Loop Controller 给出六种 disposition（`run_now | wait | user_action_required | repair | replan | terminal`）。
- **coordinator 不是持久 Leader**：多 agent 时由 canonical work key 哈希选出**临时 task coordinator**，合同只绑定该 task bundle，随 bundle 失效。
- **决策层是纯函数**：`authority_core.decide(snapshot, command) -> APPLY|CONFLICT|REJECTED`，不调模型、不写状态，可单测。

项目基本面（2026-09-01 GitHub API 实测）：5356 stars / 480 forks，Python 为主（内含 TS effect runtime），Apache-2.0，创建仅 3 个月但 push 频繁（最近 push 即当日）；贡献者头部集中——huangruiteng 约 4754/~4980 提交（≈95%），实质单作者项目。

## 2. 它怎么治"规划 JSON 病"（我们的直接痛点）

我们的病状：lead planner 让模型输出计划 JSON，解析失败/字段缺失/结构漂移反复发生，修复方向一度是"提示词加约束 + 解析容错 + 重试"。

loopx 的方针是四层，层层都值得对照（RESEARCH.md §2）：

| # | 方针 | loopx 实现 | 对我们的映射 |
|---|------|-----------|-------------|
| 1 | **规划真相不经过不可信 JSON** | `protocol_action_packet_v0` 由内核从 `execution_obligation`、`work_lane_contract` 等已提交字段确定性推导，显式标记 `llm=no_api`；envelope 内做重建校验，不通过则降级为 retain summary 而非静默信任 | planner 的**权威字段**（选哪个任务、依赖 frontier、lease/占用状态、可执行动作集）应由 Go 代码从 DB 算出；模型只产出**建议层**（任务拆解草案、说明文本），入库前必须经内核校验改写为权威记录 |
| 2 | **必须过 JSON 的边界，严格 decode + 版本号** | TS 侧 `runtime_decode.ts` 一律 `requireJsonObject`/`requireNonEmptyString`/`requireStringLiteral`，失败在边界抛错，不进业务逻辑；所有跨进程包带 `schema_version` | 我们的 planner 输出契约加 `schema_version`，Go 侧 `json.Decoder.DisallowUnknownFields()` + 必填校验，**在 handler 边界拒绝**，不把半成品 plan 传给执行器 |
| 3 | **失败走 typed 路由，不重试解析** | `_typed_route` 对 schema 不符/签名不符/超预算直接返回 `contract_error`（不交给 host）；`REPAIR_ACTIONS`/`REPLAN_ACTIONS` 驱动 `repair_required`/`replan_required` 路由，repair 维度本身也是枚举（`repair_delta_contract_v0`） | 解析失败不应在原地 retry 同一段 JSON；应落为 run 的一个 typed 结果（如 `plan_contract_error`），触发**换提示/换模型/缩小任务范围/人工介入**的明确分支——这与我们"任何 Outcome 都必须能落终态"的硬约束同构 |
| 4 | **预算硬上限** | turn envelope 8192 bytes 硬预算，超限即 contract_error | 给 planner 输出设 token/字节预算，超限即 typed 失败而非截断后带病解析 |

一句话：**它把"模型 JSON 不靠谱"当作架构前提来设计，而不是当作 prompt 工程问题来对抗。**

## 3. 能力对照：我们已有 vs loopx 有

| 能力 | workbench 现状 | loopx | 差距判断 |
|------|---------------|-------|---------|
| Run 状态机 | 13 态唯一权威，ModuleRunner 进程内唯一推进点，Outcome 必落终态 | Turn route/disposition 六分 + journal | **我们不弱**，语义不同但都完备 |
| claim/lease | F1 任务锁已合 main | `task_lease`（TTL 45min、幂等键、文件锁）+ soft_claim/hard_lease 分模式 | 各有，loopx 的**模式分离**更清晰 |
| 幂等 | 实体幂等键、runs_count/usage 按 run 幂等 | settlement 分阶段幂等 + 事件溯源契约 | 相当 |
| 编排 | lead/worker：plan 执行器/lead planner/评估/认领/join/护栏已合 main | 临时 coordinator 哈希选举 + adaptive admission | 我们更像"团队"，它更像"对等网络"，方向不同 |
| **quota/预算** | 无系统层 | compute quota 层 + envelope 字节预算 + 小步进展 BLOCKED_QUOTA_STATES | **缺口** |
| **假进展防护** | 无 | `DeliveryOutcome` 区分 `surface_only` vs `outcome_progress` | **缺口**，直接可借 |
| **repair/replan** | 无 typed 路由（失败靠重试/人工） | 一等路由 + repair 维度枚举 | **缺口**，治 JSON 病的关键 |
| **事件溯源** | 无（状态直写） | append-only 事件契约（可用 SQLite 实现），Markdown/UI 只是投影 | 可选，非必须 |
| 恢复 | SSE 快照+游标、session_unknown 自愈、墓碑语义 | turn journal recovery | 我们更贴 chat 场景，它更贴批任务场景 |

## 4. 方案 A：把 loopx 架构整体迁移为任务执行底座

**做法**：以 loopx 的六层 + effect interpreter 为蓝本重写我们的任务执行层，或直接以其代码为底座分叉。

优点：

- 一次性获得整套长任务语义：quota、gate、evidence、handoff、lease、settlement 全是 typed 合同，不用自己发明词汇。
- anti-JSON 失败的机制是**原生**的，不是后补的。
- 事件溯源契约带来完整审计与回放能力。
- Apache-2.0，法律上可分叉。

缺点（逐条都有实证支撑）：

1. **换栈重写**：loopx = Python 内核 + TS effect runtime 双语言桥接。我们是 Go + SQLite-only + React，迁移意味着放弃已验证资产：13 态状态机、ModuleRunner 唯一推进点、codex/kimi adapter（含 resume/session_unknown 自愈）、SSE 恢复链路、前端全套、CI 门禁。这些是数月沉淀且刚经过多轮评审加固的。
2. **底座本身年轻且单作者**：创建仅 3 个月，~95% 提交来自一人。把任务执行底座押在单作者项目上，Bus factor = 1；它自己的双语言桥接复杂度（Python 经 `effect_runtime_result` 调 TS）也是未偿的债。
3. **人格不合**：loopx 的 `ACTIVE_GOAL_STATE.md` Markdown 工作台是人机共写的真相源之一；我们的硬约束是 DB 唯一真相源。两套真相源哲学冲突，迁移意味着我们先解决它的内部矛盾。
4. **schema 爆炸**：数百个 `*_v0` 合同。全量接过来等于把它的表面积也接过来，维护成本远超我们当前体量所需。
5. **定位错位**：它是"跨 harness 控制面"，不是"多 agent 团队协作产品"。我们的看板、审批、团队语义、chat 正文、前端设计体系它都没有——迁移后这些还是要我们自己扛，而我们已经扛出来了。
6. **宿主绑定深**：大量 codex-cli/claude-code 专用 settlement 与 child host ops，裁掉之后剩下的通用内核并不比我们现有的厚多少。

## 5. 方案 B：参考借鉴

**做法**：读它的契约与决策层设计，把对症的机制在现有 Go+SQLite 栈内重新实现，代码一行不抄。

优点：

1. **保住全部已有资产**，改动是增量而非置换；与 dsh-dev-workflow 的"删除优先于垫片、分刀提交"纪律兼容。
2. **精确对症**：JSON 病由方针 1–4 直接治疗（见 §2），不需要引入它的整个运行时。
3. **单语言单一决策内核**：我们可以把纯函数决策层直接写成 Go 包（`decide(snapshot, cmd)` 风格），避免它的双语言桥接债。
4. **可裁剪**：只取 todo/lease/gate/run/quota/handoff 子集，schema 数量控制在我们自己手里。
5. 与在建工作同向：任务控制面正在 worktree `codex/task-control-surface` 实施，这些机制可以**作为设计输入**进入该面，而不是另起炉灶。

缺点：

1. **理解成本**：它的设计散落在数百个合同里，读偏了会只抄词汇不抄纪律（比如学了 `contract_error` 的名字，却仍让模型 JSON 当真相）。
2. **自己实现就有自己出错的空间**：纯函数决策层、分阶段 settlement 的正确性要靠我们的测试与门禁保证，没有现成实现可对照跑。
3. **持续跟踪成本**：它 3 个月演化极快，借鉴是一次性快照，后续它的好想法需要定期回访。

## 6. 对比结论

| 维度 | A 整体迁移 | B 参考借鉴 |
|------|-----------|-----------|
| 对 JSON 病的疗效 | 彻底但附带全身移植 | 对症，药效相同 |
| 已有资产 | 全部置换 | 全部保留 |
| 栈一致性 | Python+TS 嫁接 Go | 纯 Go |
| 底座风险 | 单作者、3 个月、双语言债 | 无新增外部依赖 |
| 工作量 | 月级重写 + 回归风险 | 周级增量，可分刀 |
| 可逆性 | 几乎不可逆 | 每个机制独立成刀，可独立回滚 |
| 与我们愿景（团队/看板/审批/chat）的契合 | 错位，需二次开发 | 原生契合 |

**推荐 B。** 迁移派最强的论据是"它的长任务语义完整"，但 §3 的对照显示：状态机、lease、幂等、编排我们都不缺甚至更强，真缺口只有 quota、delivery 分类、repair/replan 路由、规划投影纪律这四块——**为四块缺口换掉整个地基不成立**。

## 7. 若采纳 B：落地路线建议

按投入产出排序，每条都可独立成刀（分支/提交粒度按 dsh-dev-workflow）：

1. **planner 契约硬化**（治 JSON 病，最先做）
   - plan 输出加 `schema_version` + Go 边界严格 decode（拒绝未知字段、必填校验）。
   - 权威字段（selected task、依赖、lease 状态、可执行动作）由内核从 DB 计算，模型输出降级为"建议草案"，入库前内核改写。
   - 解析失败 = typed 结果 `plan_contract_error` 落 run，路由到换提示/缩小范围/人工介入分支，**禁止原地重试同一段 JSON**。
   - 落点：`internal/orchestration` planner 相关包；可先查任务控制面 worktree 是否已有接缝。
2. **repair/replan 一等路由**：在 Run outcome 词汇中增加 repair/replan 分支（13 态不一定加态，可在 outcome/result 维度扩展），与"Outcome 必落终态"约束对齐。
3. **delivery_outcome 分类**：run 结算区分 `surface_only`（跑了但没交付）与 `outcome_progress`，进评估链路（我们已有评估器，缺这个维度）。
4. **quota/预算层**：先做最薄的——planner 输出字节预算 + 每 run 的 turn/token 上限，超限 typed 失败；完整 quota 层后置。
5. **（可选，后置）事件溯源**：loopx 证明可用 SQLite append-only 实现；但我们的墓碑+幂等已覆盖主要审计需求，不急于上。

**明确不搬的**：双语言 effect runtime、Markdown 工作台真相源、数百 `*_v0` schema 全集、host 适配器海（我们的 adapter 层已覆盖 codex/kimi）、对等网络式 coordinator 选举（我们的 lead/worker 团队模型是已定方向）。

## 8. 风险与负向保证

- **只抄词汇不抄纪律**是最大风险：借鉴的每一条都必须落到"权威数据由内核计算"这个原则上，否则白借。
- 本报告对 loopx 的判断基于其当前 HEAD（`9a9526a`）与文档/源码抽样；它演化极快，落地前若间隔超过数周，应回访关键文件（`turn_driver/driver.py`、`interaction_contract.py`、`authority_core.py`）。
- 我们不承诺：不引入 Python 运行时、不把任何 Markdown 文件当真相源、不照搬其 schema 命名全集、不改变 lead/worker 编排方向。
- loopx 为 Apache-2.0；本路线不复制其代码，无许可证义务；若未来引用其文档段落需署名。

## 附录：来源与核验状态

| 论断 | 状态 |
|------|------|
| 六层架构、effect interpreter、turn 路由/disposition、coordinator 临时选举、anti-JSON 四层方针、能力矩阵各条目 | 取自 RESEARCH.md（cursor agent 实地读码产出，每条附源码 URL），未逐条复核 |
| stars 5356、Python、Apache-2.0、2026-05-31 创建、当日有 push | 已抽验（GitHub API 实测，2026-09-01） |
| 单作者 ≈95% 提交占比 | 已抽验（contributors API top10：huangruiteng 4727+27，其余均 ≤78） |
| workbench 侧现状（13 态/任务锁/幂等/编排 M1–M4/任务控制面在建） | 取自本仓库代码与 notes 台账的既有共识，未在本报告中重新逐文件取证 |

---

# 源码实证补充（2026-09-01 第二轮：coordinator 从提示词到返回处理的全链）

> 方法：`git clone --depth 1 git@github.com:huangruiteng/loopx.git /tmp/loopx-src`（HEAD 即当日 main），实地精读以下文件并给出处行号。本节所有行号以该克隆为准。

## A. 总发现：coordinator 大部分时候根本不调 LLM

loopx 的"下一步做什么"是**纯函数决策树**，不是模型输出：

- `loopx/control_plane/work_items/interaction_contract.py:276-437` `protocol_action_packet_fields`：从已提交状态（execution_obligation / work_lane / capability_gate）走 if-elif 决策树，算出 actor（agent/user/agent_with_user_gate）与动作文案——文案是**内核模板**（"advance one bounded segment"、"quiet no-op; no material transition"）截断到 80 字符，末尾显式打标 `fields["llm"] = "no_api"`（:411，常量定义 :57）。
- `loopx/control_plane/turn_driver/driver.py:67-104` `_typed_route`：纯函数把 envelope 映射为七路由（ready_for_host/repair/replan/user_action/wait/blocked/contract_error）；schema 不符、签名哈希不符、超预算直接 `CONTRACT_ERROR`。
- `loopx/host_mode_planner.py`（991 行）名为 planner，实为**零 LLM 的宿主模式选择器**：`build_host_mode_plan`（:706）纯 Python 枚举五种模式，输出的是内核预渲染好的 CLI 命令清单（`loopx turn plan ...`、quota guard 命令），注释明言 "the selector never launches a host, writes state, validates work, or spends quota"。

**推论**：它把"规划"从模型手里拿走的程度，比我们 lead planner 彻底得多。LLM 只在三处出现，且每处有一套**独立调校的输出处理纪律**。

## B. LLM 出现的三处与三种处理纪律

### B1. 执行 Turn（`loopx/dsh_goal_mode/turn_host_adapter.py`）——宽进严出，fail-closed 到 `wait`

- **提示词**（`render_prompt` :127-159）：内核把签名过的 authority packet（primary_action ≤1200 字符 + required_reads + write_scope + workspace_guard）序列化进提示词，然后**逐字段教返回契约**：`result_kind` 五选一（validated_progress/repair_required/replan_required/user_action_required/wait）、每个文本字段的字符上限（classification ≤120、summary ≤400、next_action/recommended_action ≤1200、vision_unchanged_reason ≤240）、"何时用 repair 何时用 replan"的教学、"return only one JSON object (no Markdown fence)"。
- **解析**（`parse_model_json` :162-195）：先 `json.loads` 全文；失败则剥 ``` 围栏；再失败取最外层 `{...}`；仍失败返回 None。**一次尝试，不重试**。
- **定形**（`build_result` :198-290）：None 或 result_kind 不在枚举内 → 不抛错，**降级为 typed `wait`**（classification=`no_typed_host_result`，诊断文本进 next_action）。注释原话（:208-209）："Fail closed: no typed material claim means a stop, never fabricated progress. This spends no quota." 合法 material 结果则强制 `delivery_outcome=outcome_progress`，稀疏字段从相邻字段回填，所有文本截断到上限。

### B2. 规划（`loopx/experiments/planner_worker/contract.py` + `runtime.py`）——最严："validate without repairing its shape"

这是唯一让 LLM 产出**计划**（即写权威的来源）的地方，纪律最重：

- **提示词**（`build_planner_prompt` contract.py:376-402）：角色声明 + "Return one worker-ready plan as strict JSON" + **把验收政策预先写进提示词**（"The runtime rejects markdown fences, prose, missing fields, illegal enums, invalid dependencies, and non-positive budgets"）+ **完整 JSON skeleton 当 few-shot**（≤8 步、每步 20 个字段的示例值）+ objective + task。
- **解析**（`parse_planner_worker_plan_text` :321-333）：裸 `json.loads` 一次——**没有**围栏剥离、没有大括号提取，比执行通道严一个量级。失败即 ValueError。
- **校验**（`validate_planner_worker_plan` :148-318，docstring :149 "Validate untrusted Planner output without repairing its shape"）：精确字段集（缺字段与**未知字段都拒绝** :93-99）、schema_version 全等、枚举校验、跨字段一致性（executor↔model_tier 强绑定 :222-231、worker_ready 与 blockers 互斥 :243-246）、正整数预算、仓库相对路径（禁绝对路径与 `..` :109-112）、step_id/planner_order 唯一、依赖图闭合 + **环检测**（:115-145）。
- **运行时 gating**（`run_planner_worker_once` runtime.py:211-391）：planner 回合后先查工作区 diff——planner 动了文件 → failed receipt（:241-258）；`validation_commands` 必须在调用方 allowlist 内，否则 failed（:283-304）；worker 执行后 workspace diff 对照 plan 契约核对（target_files 之外的改动、max_files、max_bytes_per_file、文件类型，:105-147）；验证后再核一遍。全程产出 typed receipt（status/reason/usage/cost）。
- **失败不自动重试**：strict 一次，typed failed receipt，决定权交回调用方。

### B3. Chat 对话（`loopx/chat_agent.py` + `loopx/chat.py`）——标签信封 + 优雅降级

- **提示词**（`_turn_prompt` chat_agent.py:171-241）：要求先写人类可读正文（结论先行、短句可流式、≤5 个 action item），**最后**追加唯一一个 `<loopx-review-json>{...}</loopx-review-json>` 信封（message 字段重复正文 + proposals + protected_action + gate）；对 protected_action 反复强调 "only an untrusted proposal for LoopX typed preview and never authority to execute"。
- **解析**（`parse_agent_response` chat.py:258-295）：取**最后一个**标签对；JSON 坏了走 **salvage**——正则定位 `"message":` 键，`raw_decode` 只把 message 字符串抠出来，降级为无 proposal 的信封；再不行整段原文当 message。缺信封则发 `protocol.warning {"error_code":"missing_review_envelope"}` 事件（chat_agent.py:707-708）+ 原文回退——**UI 永远有内容可显示**。
- normalize 后 proposal 一律走 dry-run preview → fingerprint → apply，带 `expected_revision` 乐观并发（chat.py:269-283、:372-388）。

## C. 提示词与处理的共性手法（最值得抄的三件套）

1. **验收器前置进提示词**：把"runtime 会拒绝什么"写成明文（B2 的 rejection 政策句、B1 的字段上限清单），并用完整 JSON skeleton 当 few-shot——让模型第一次就照着验收器的要求输出。
2. **模型输出永远只是 proposal**：无论 result_kind 还是 protected_action 还是 plan，到达内核后都要过第二道 typed 校验/预览；写权威永远在内核命令（writeback、quota spend、todo apply 全是内核渲染的 CLI 命令，`project_prompt.py:926-1109` 的 onboarding 提示词通篇是"执行我给你的命令，不要自拟协议"）。
3. **失败分级而非统一重试**：执行轮失败 → 降级 `wait` 带诊断（可观测、不卡死、不造假进展）；规划失败 → typed failed receipt 交回调用方（不静默重试同一段 JSON）；对话缺信封 → 降级显示 + warning 事件。**没有一处是"原地重试解析"**。

## D. 硬预算清单

turn envelope 8192 bytes（`turn_envelope.py:24`）、host stdout 12_000 bytes（`executor.py:64`）、agent_vision_json 3200 chars（`executor.py:67`）、各文本字段独立 char 上限、plan ≤8 steps、chat todo text ≤400 chars。超限要么 typed 失败（kernel 侧校验）要么截断（adapter 侧定形），从无"超长输入带病解析"。

## E. 对我们 lead planner 的直接映射

| loopx 手法 | 我们可落的做法 |
|-----------|---------------|
| B2 提示词三件套（政策前置 + skeleton + 枚举教学） | 直接抄进 planner 提示词构造，一天工作量 |
| B2 严格校验（精确字段集/环检测/allowlist） | plan JSON 的 Go 校验器按此清单实现，失败落 `plan_contract_error` 终态 |
| B1 降级语义（wait + 诊断，不造假进展） | 解析失败不重试，落带诊断的 typed 结果，路由 repair/replan/人工 |
| B3 信封 + salvage | chat 正文场景已在用类似思路（我们的 markdown 结构块），salvage 兜底值得抄 |
| A 决策树收权 | 长期方向：selected task/依赖/lease 状态等权威字段逐步从 planner 输出移到内核计算 |
