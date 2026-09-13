# 仓库知识地图

> **文档定位**：本文件是 12 份并行只读盘点的合成结果，回答三个问题——仓库里有哪些知识、分别在哪、哪些能当事实源。它是索引与对账表，不产生新的产品、架构或安全结论；除显式标注「本次核对」的条目外，全部内容均可追溯到某一处仓库路径。
>
> **生成基线**：`main @ 531c59d`（本文件所在 worktree 分支 `docs/knowledge-map` 与主树同版本）。12 份素材的 `STATUS` 行全部为 `DONE`，**没有任何 `STATUS: PARTIAL` 条目**，因此下文不含「因盘点中断导致」的缺口；第七节列出的缺口均为盘点本身发现的仓库内事实问题。合入 main 时主树已推进（产品知识画布恢复、chat 左栏 chrome 删除等 3 个合并在盘点之后），涉及画布与 chat 侧栏的条目以最新代码为准。
>
> **维护方式**：本文件不逐条增量维护。新知识按其类型落到第四/八节给出的目录（`docs/`、`notes/{lifecycle}/{class}/`、`agent-team-workbench/` 的契约、代码与迁移）；当盘点范围变化（新增目录类别、某层被整体重建）时整体重生成。第二、六、七节强绑定生成基线，跨基线引用前应先核对当前提交。本文件自身已按一行索引登记进 `docs/README.md` 的「活跃文档」清单。

## 一、执行摘要

仓库知识呈五层结构，且各层的「权威来源」并不在同一个位置：**产品能力知识**（`docs/product/`，37 份需求与验收文档）描述要做什么，其现状判定靠代码与验收证据反查，其中知识域的判定已随 2026-09-12 的资料库重建整体换代；**仓库文档知识**（`docs/architecture/` 7 份、`docs/protocol/` 3 份、`docs/frontend/` 2 份、`agent-team-workbench/web/` 设计文档 2 份、`docs/testing/` 与 `docs/review/` 共 165 份）承载架构、协议与验收证据，其中 `docs/review/` 的 155 份以 `assets/` 下的 JSON 与截图为主，是「结论 ↔ 证据」配对的证据侧；**决策留痕**（`notes/implemented/` 97 篇 + `notes/{proposed,rejected,product,archived}` 11 篇，共 108 篇）按生命周期与 class 分层，是运行时硬约束最完整的成组记录；**机器契约与运行时知识**由 `agent-team-workbench/contracts/` 的 4 份机器可执行契约、66 个迁移 SQL（`0066` 为最新）、以及约 51 个代码内载体与 Agent 指令文件构成，是唯一可被门禁双向强制的层；**外部参考与归档**（`docs/references/` 9 份 + `docs/archive/` 24 份）锁定外部系统版本，按文档自述「不是本项目当前实现的事实源」。整体特征是：事实源分散在五层、且同一主题常同时存在「现行事实源」与「未标冻结的旧文」两份（详见第七节）。

各类别规模（文件数，均取自本次盘点的范围自述）：

- 产品能力知识：`docs/product/` 顶层 `*.md` 17 + `docs/product/workspace-ai-workbench/` 20 = 37；知识能力现状交叉核对覆盖约 74 个 `knowledge` 命名文件（文档/笔记 21、代码与测试 53）。
- 仓库文档知识：`docs/architecture/` 7、`docs/protocol/` 3、`docs/frontend/` 2、`agent-team-workbench/web/*.md` 2、`docs/testing/` 10、`docs/review/` 155（其中顶层 md 仅 2）。
- 决策留痕：`notes/implemented/` 97（architecture 40、feature 30、bug-fix 20、orchestration 4、process 2、simplification 1）；`notes/proposed/` 6、`notes/rejected/` 1、`notes/product/` 3、`notes/archived/` 1。
- 机器契约与运行时知识：`docs/protocol/` 3 篇 + `agent-team-workbench/contracts/` 4 份机器契约与 2 个 embed 门禁文件（合计 9 个盘点对象）；`agent-team-workbench/migrations/` 66 个 SQL；代码内与运行时载体约 51 个。
- 外部参考与归档：`docs/references/` 9（约 98KB）、`docs/archive/` 24（5 个子目录；二进制约 3.4MB，文本约 370KB——**本次核对**按 md/json/txt/html 逻辑字节合计 368KB，`du` 口径含块占用会更大；生成基线所用的文本体量口径无法复现）。

## 二、产品侧：知识能力现状

判定口径来自 `12-knowledge-capability`：以 `agent-team-workbench/` 内实际代码、HTTP/MCP 面、前端组件为「实现」，以 `notes/implemented/` 与 `docs/` 为「依据」。**本次盘点未对任何能力给出「部分实现」判定**——旧链条是整体退役（表被 DROP、CLI 被删），不是部分留存；因此下表状态取值为「已实现可用」「仅设计在途」「已否决」「已退役」四种，不含「部分实现」；已退役能力单列。

| 能力 | 状态 | 代码或文档依据路径 | 判定要点 |
|---|---|---|---|
| 统一资料库读写底座（`/library/*` HTTP 面）| 已实现可用 | `agent-team-workbench/internal/knowledgelib/*.go`（9 文件）、`agent-team-workbench/internal/httpapi/handlers_knowledge_library.go`、`agent-team-workbench/contracts/web/openapi.yaml`（tag `knowledge-library`）| 23 个 operation（14 `GET` + 7 `POST` + 1 `PATCH` + 1 `DELETE`）覆盖 20 条 `/api/v1/workspaces/{workspace_id}/library*` path；Markdown `content/**` 为写作真相、SQLite 为可重建投影（声明在 `agent-team-workbench/internal/knowledgelib/record.go:6-7`）；记录语法 `kb-note/0.2-draft` |
| 资料库写任务队列与单写者 | 已实现可用 | `agent-team-workbench/internal/domain/knowledge_library.go`、`agent-team-workbench/internal/application/knowledge_library_worker.go`、`agent-team-workbench/migrations/0057–0064` | 8 态状态机 `queued/running/awaiting_agent/retry_wait/blocked/completed/failed/cancelled`；`IsTerminal`/`OccupiesHead`；`max_attempts=3`、`max_repair_attempts=2`；唯一部分索引 `idx_knowledge_write_tasks_single_active` |
| 内置资料库管理员 Agent | 已实现可用 | `agent-team-workbench/internal/application/system_agents.go:55-90`（`EnsureBuiltinKnowledgeLibrarian`，slug `knowledge-librarian`、`InstructionsEditable=false`）、`agent-team-workbench/internal/application/knowledge_library_worker.go:519-525,990`、`agent-team-workbench/web/src/pages/agents.page.tsx:299` | 系统内置 profile，由写队列派 Run（`lib.LibrarianAgentID`）；harness prompt 固定、页面不可编辑，agents 页标注「系统内置 · 知识库管理员」|
| 知识库版本发布与投影 | 已实现可用 | `notes/implemented/architecture/2026-09-12-knowledge-library-rebuild.md`、`agent-team-workbench/migrations/0057–0064`、`agent-team-workbench/internal/knowledgelib/*.go`、`agent-team-workbench/internal/application/knowledge_library.go:1053-1078` | `knowledge_publications` prepared→committed、`knowledge_releases` published/superseded；`visibleReleasePredicate` 只读 committed；`SettlePublication` 单事务封口；未发布版本 404，固定 `release_id` 读取时版本不符 422（未固定 release 时同样 404）；`POST /library/reindex` 返回 202 |
| 全库知识检索 | 已实现可用 | `agent-team-workbench/migrations/0057–0064` 的 FTS5 `knowledge_search_index`、`docs/product/knowledge-reading.md` | 复用 SQLite/FTS5 + 中文子串 fallback；资料库接口参数为 `release_id/q/kind/limit/version/cursor`（`agent-team-workbench/contracts/web/openapi.yaml`），`/library` 页现行只读 `tab`（`agent-team-workbench/web/src/pages/knowledge.page.tsx:2586`）；`docs/product/knowledge-reading.md:17`「URL 表达查询、类型、条目和具体版本」是重建前的散文口径 |
| 知识咨询（计划动词消费面）| 已实现可用 | `agent-team-workbench/contracts/control/plan-decision-v2.schema.json`（`ConsultKnowledgeStep`）、`agent-team-workbench/internal/application/knowledge_library_retriever.go` | `consult_knowledge` 经 `KnowledgeLibraryRetriever` 读同一已发布 release；跨步约束由 Go 校验 |
| 需求冻结接入 | 已实现可用 | `agent-team-workbench/internal/application/knowledge_library_requirement.go`、`agent-team-workbench/migrations/0061–0064`（`knowledge_requirement_inputs`）| `requirement_input_id` 冻结需求全文，不读 `content_ref`；run 终态收件不新增执行权威 |
| 对话式知识录入 / Chat 自动检索 | 已实现可用 | `notes/implemented/feature/2026-09-13-featured-agents-chat-knowledge.md` | `chatKnowledgeContextLimit = 5` 冻结进 `run.Input.knowledge_context`；每轮在 `appendSourceContext` 拼装；系统 agent 注入为 NULL |
| 知识阅读与管理页 | 已实现可用 | `agent-team-workbench/web/src/pages/knowledge.page.tsx`、`agent-team-workbench/web/src/api/knowledge-library.ts` | 五 tab：资料库与来源 / 初始化与更新 / 知识浏览 / 查询与展开 / 版本与队列；旧 `/knowledge` 路由由 `/library` 取代且不加重定向 |
| 知识画布（只读，`role=pm` 专属）| 已实现可用（存在规范冲突，见第七节 #13）| `agent-team-workbench/web/src/components/knowledge-canvas/`（5 文件）、`notes/implemented/feature/2026-09-13-product-knowledge-canvas-restore.md` | 门禁 `role==='pm' && enabled && isUserManagedAgent`；数据层 `listDocuments/getDocument/getGraph`；`<atw-knowledge-reference-v1>` 引用卡片；React Flow 只画两端已渲染的事实边 |
| 逐条管理员条目模型（`kl_*`/item/version/submission/job 语义分类）| 已退役（不适用三分类）| `agent-team-workbench/migrations/0044/0046–0048`、`agent-team-workbench/migrations/0065`、提交 `8e8a389` | `0065` DROP 15 张旧表、仅 4 表有残留 39 行；`8e8a389` 同时删除 `cmd/atw-knowledge`、`agent-team-workbench/internal/application/knowledge_*`、`agent-team-workbench/contracts/control/knowledge-librarian-v1.schema.json`（均为删除前路径）|
| V1 人工操作台「交资料 / 提问题 / 看结果」| 已退役（不适用三分类）| `docs/product/knowledge-experience-acceptance.md` | 该文自述方向已被 2026-09-08 产品决策废止 |
| 角色级来源策略（`KL-SOURCE`）| 仅设计在途 | `docs/product/knowledge-librarian-requirements.md` | 自述「尚未实现」；随旧链条整体退役，无现行实现 |
| 远程 Worker 的知识工具注册与凭据传输 | **已否决（随旧链退役）** | 需求原文 `docs/product/knowledge-librarian-requirements.md`、`docs/protocol/knowledge-librarian.md`；放弃记录 `notes/implemented/architecture/2026-09-12-knowledge-library-rebuild.md:28,112-123`、`notes/implemented/feature/2026-09-13-featured-agents-chat-knowledge.md:24-26`（「放弃了什么」第 2 条）| 2026-09-06 两文自限「未实现」；09-12 重建明确放弃该需求——资料 agent 改用 staging 目录文件契约、不注册自定义 HTTP 工具、不走 runtime 适配层管凭据；两文描述的端点与 CLI 随 `8e8a389` 全删 |
| 资料库重建的设计正本（设计 5.9.3 / 5.9.4 / 5.14.1 / A8）| 仅设计在途（原文不在仓内）| `notes/implemented/architecture/2026-09-12-knowledge-library-rebuild.md` | 该笔记反复引用上述小节，全库检索无对应文档，仅两份笔记互相引用 |

### 设计文档与代码不一致之处

1. **旧设计/协议/需求未标冻结，却描述已被 DROP 的表**：`docs/product/knowledge-librarian-requirements.md:3`、`docs/protocol/knowledge-librarian.md:3` 标 `Status: implemented local scope`，而 `docs/architecture/knowledge-librarian-design.md:3` 用中文「状态：已实现本机闭环」；三文正文围绕 `knowledge_items/knowledge_versions/knowledge_submissions/knowledge_jobs` 展开，而这四类表已被 `agent-team-workbench/migrations/0065` 删除；`docs/protocol/knowledge-librarian.md` 描述的 23 个 `/knowledge/*`、`/knowledge-agent/*` 端点与 `atw-knowledge` CLI 桥同批删除，字段契约仍指向 `agent-team-workbench/contracts/web/openapi.yaml`。
2. **存储权威倒置**：`agent-team-workbench/knowledge/README.md:15` 称「SQLite 是知识条目的唯一有效状态入口；Markdown 仅用于显式导入/导出」，而 `agent-team-workbench/internal/knowledgelib/record.go:6-7` 声明「Markdown under the library root is the writing truth for knowledge. The SQLite rows are a projection that can be rebuilt」，`notes/implemented/architecture/2026-09-12-knowledge-library-rebuild.md` 亦写「把 Markdown 提升为知识正文的真相源」。README 未随 09-12 重建更新（无日期字段）。
3. **产品画布在 `/chat` 面的存废相反**：`agent-team-workbench/web/DESIGN.md:277-284` 声明知识画布连同 `?canvas=knowledge`、`?knowledge=` 引用尾注已退役，且「No canvas, graph, or excerpt-reference surface may be reintroduced under `/chat`」；但 `agent-team-workbench/web/src/pages/chat.page.tsx:10,1344` 仍 import 并渲染 `<KnowledgeCanvas>`，`agent-team-workbench/web/src/components/chat/transcript-view.tsx:8` 仍用 `parseCanvasMessage`，`agent-team-workbench/web/src/components/knowledge-canvas/`（5 文件）与 `agent-team-workbench/web/src/pages/chat-knowledge-canvas.render.test.tsx` 均在树上。
4. **管理页路由已迁移但文档未同步**：`docs/product/knowledge-experience.md`、`docs/product/knowledge-reading.md` 描述的 `/knowledge` 入口，现行由 `/library` 承载（`agent-team-workbench/internal/httpapi/handlers_knowledge_library.go`），旧路由返回 404 且不加重定向。
5. **陈旧二进制对不上「CLI 已删」**：`12-knowledge-capability` 在生成基线的主树中查到 `agent-team-workbench/bin/atw-knowledge`（8.6MB，mtime 2026-09-11）仍存在，而 `cmd/atw-knowledge` 已无源码。**本次核对补充**：该路径被 `agent-team-workbench/.gitignore:1`（`bin/`）忽略，未纳入版本控制，因此在全新 checkout 的 worktree 中不存在——它是本机构建残留，不属于仓库内容。

## 三、仓库知识资产地图

表头统一为：路径（仓库相对 POSIX）、主题、知识类型、权威级别、关键内容要点、时效。

### 3.1 产品与架构文档（`01`–`03`）

`docs/product/` 顶层 17 份（一次性覆盖，单维度不超 30 行）：

| 路径 | 主题 | 知识类型 | 权威级别 | 关键内容要点 | 时效 |
|---|---|---|---|---|---|
| `docs/product/end-goal.md` | 终局愿景：控制平面在 harness 之上 | 需求（愿景）| 现行事实源（自述「不描述当前完成度」）| 四层架构 Provider/Harness/会话/编排；历史预算 = `context_window×35%`；前缀缓存字节契约（动态只在当轮尾部）；OrchestrationPlan 五动词；现状对账 2026-09-06 + M1–M4 | 文件 2026-09-07，仍有效 |
| `docs/product/loopx-native-governance-goal.md` | LoopX 长程治理语义原生移植需求合同 | 需求 + 决策留痕 | 现行事实源（治理语义；自述事实源为 `docs/architecture/system-architecture-handbook.md`）| R0–R8；raw `PlanDecisionV2` + bounded repair；Goal/Todo/Claim/DecisionScope；TurnDecision 六值、TurnReceipt phase1–7；Quota/ShouldRun；Handoff/Evidence/ProjectionRepair；§8 不变式、§12 验收矩阵；迁移 0024–0035/0039–0042 | 确认 2026-09-03；WP0–WP7 落码，真实 Codex/Kimi Provider 与 Remote Host gate pending |
| `docs/product/product-agent-charter.md` | 产品 Agent 行为章程（§0–§5 粘贴即用）| 设计规范 | 现行事实源（该 agent system prompt 来源）| 澄清→立法→交付；需求树「前沿」规则 + 八维骨架；条目模板 `PRD-NNN` + 三值用词（应当/不得/可以）+ 禁用词；附录 A–D 提问格式/共识摘要/生命周期/树况记法 | 2026-08-31；配套架构/开发/测试章程未见于本目录 |
| `docs/product/knowledge-librarian-requirements.md` | 知识管理员产品契约（旧链）| 需求 / 机器契约 | 现行事实源（本地范围）但事实已被取代 | 9 组验收 ID `KL-WRITE/SOURCE/QUERY/SCOPE/CONFLICT/CONCURRENCY/PUBLISH/LOOP/UI-*`；SQLite 唯一状态源、投影可重建；§8 冻结衔接点 1–7；只信 `metadata.verification`；本机 0600 Run capability + `--access-file` | 2026-09-06；验收在 `docs/review/knowledge-librarian-acceptance.md`（跨目录）|
| `docs/product/knowledge-experience.md` + `docs/product/conversational-knowledge-acceptance.md` | 知识库只读浏览 V2 交互 / 内置管理员对话录入验收 | 需求 + 验收证据 | 参考（V2 基线，已被 `knowledge-reading.md` 补充）| `/knowledge` 只读四事；唯一跨页动作「与知识管理员对话」→ `kind=knowledge_librarian`；7 类语义分类；6 个最小 GET；`next_cursor` 分页 + 固定空态文案；验收记录 `run_01M1ZA6…` → `kbj_…` 3 轮 GLM → `kb_…` effective v1 | 2026-09-08，已合 main；交互方向已被 `/library` 取代 |
| `docs/product/knowledge-experience-acceptance.md` | V1 人工操作台验收 | 验收证据 / 归档 | 已冻结（自述方向被废止）| 「交资料/提问题/看结果」；Source→Submission→KnowledgeJob→Item/Version→Relation 数据流；`kss_` 候选、`kbj_` 作业；110 文件 900 测试 | 2026-09-08，明确不代表当前交互 |
| `docs/product/knowledge-reading.md` + `docs/product/knowledge-reading-acceptance.md` | 知识阅读与统一工作台主题 | 需求 + 验收证据 | 现行事实源 | 全库检索复用 SQLite/FTS5 + 中文子串 fallback；URL 表达查询/类型/条目/版本（该文 :17 为重建前散文口径，现行接口参数见第二节）；删除折叠导航；1440 正文 756px、768 为 455px；验收 113 文件 918 测试 | 2026-09-08，已授权合 main |
| `docs/product/product-agent-canvas.md` + `docs/product/product-agent-canvas-acceptance.md` | 产品 Agent 知识画布 | 需求 + 验收证据 | 现行事实源 | 显式 `role=pm` 识别（不猜身份）；`status=effective&owner_agent_id=`；React Flow 全景 ≤60 节点/并发 4/40 条一页；引用尾段 `atw-knowledge-reference-v1`；独立 `knowledge-graph` chunk 151kB | 2026-09-08 确认并合 main；文档 2026-09-13 仅指针改动 |
| `docs/product/product-agent-canvas-research.md` | 画布/图表选型调研 | 外部参考 | 参考（选型阶段，非事实源）| React Flow（MIT，12.11.6）首选、Excalidraw MIT、tldraw 默认许可仅开发环境；`diagram-design`；三阶段交付顺序；拟议验收契约明示未执行 | 2026-09-08，许可与维护快照需固定版本重查 |
| `docs/product/developer-code-workspace.md` + `docs/product/developer-code-workspace-acceptance.md` | Java 只读代码工作台（web-idea 集成）| 需求 + 验收证据 | 现行事实源（已合 main）| API `POST /workspaces/{ws}/agent-profiles/{id}/code-workspaces`、`GET /code-workspaces/{id}/bootstrap`、`view/`、`gateway/`；只读=关文件写入与 LSP 变更，非 OS 沙箱；`SOURCE_SNAPSHOT.json` 142 文件 SHA-256；LSP 每 2s 复核授权 | 2026-09-08；真实模型聊天未验收（Mock）|
| `docs/product/task-intake-project-binding.md` + `docs/product/task-intake-project-binding-acceptance.md` | 需求分析前项目/checkout 绑定 | 需求 + 验收证据 | 参考（未合 main，方案待重整）| `task-intake/projects`、`project/resolve`、`ProjectBindingInput{selection,expected_digest,include_uncommitted}`；5 个稳定错误 `analysis_project_*`；`binding_digest/location_version/mount_generation`；迁移 0049 | 2026-09-09，未提交未合 main，仅覆盖旧局部入口 |
| `docs/product/requirement-intake-workflow.md` | 需求分析确认闭环主流程 | 需求 | 参考（暂停）| 全局唯一 Workspace 入口；服务端存资料快照/分析版本/确认历史；一次一个问题；来源须带类型+版本+AI 读取状态；发布复用 WorkItem 幂等；7 条验收 | 2026-09-09 暂停，边界以 `docs/product/workspace-ai-workbench/README.md` U01 为准 |

`docs/product/workspace-ai-workbench/` 20 份（一次性提交 `bb11d30`，2026-09-09，均在 main）：

| 路径 | 主题 | 知识类型 | 权威级别 | 关键内容要点 | 时效 |
|---|---|---|---|---|---|
| `docs/product/workspace-ai-workbench/README.md` | 执行总清单与产品要求 | 需求 | 现行事实源 | 7 条已确认要求（一空间一项目目录、工作空间→Agent→知识三级、web-idea 作范本）；U01–U07 全 `[x]`；规则「总清单与单元不一致按未完成处理」；基线行 `main@651056c1` 已过期 | 2026-09-09，有效（基线段失效）|
| `docs/product/workspace-ai-workbench/unit-template.md` | 单元文档模板 | 设计规范 | 参考 | 五段结构（目标范围/输入输出依赖/开工记录/验收清单/验收交接）；「模拟/跳过/失败必须明确标注」；共享文件唯一 owner | 无日期，仍适用 |
| `docs/product/workspace-ai-workbench/units/U01–U07-*.md` | 七个单元规格与验收清单 | 需求 | 参考 | U01 唯一 `workspace_id`+切换 generation；U02 `human:shared` 空间内共享；U03 `input.source_refs`+SHA-256 固化路径；U04 `chat-analysis/v1` 一次一问；U05 outcome 三值；U06 drafts→publish；U07 删旧 stateless 链 | 2026-09-09 全 `[x]`，有效 |
| `docs/product/workspace-ai-workbench/U04-contract.md` | 分析版本与单题机器契约 | 机器契约 | 现行事实源 | `POST /work-items/{chat_id}/runs` + `output_contract=chat-analysis/v1`；闭合 `atw-analysis` fence（sources/items/questions 枚举）；上限 40 源/100 项/30 问/128KiB；`GET /analysis` status `idle\|analyzing\|needs_answer\|ready\|failed\|stale`；answers CAS+Idempotency-Key；不变式「answers 是开发上下文，永不等于产品批准」 | 2026-09-09，已实现 |
| `docs/product/workspace-ai-workbench/U05-contract.md` | 产品回填与变更契约 | 机器契约 | 参考 | `POST /analysis/decisions`，outcome 仅 `confirmed\|rejected\|needs_clarification`，ItemFingerprint 服务端派生；`/analysis/history` 不可变 revisions；`/analysis/recheck`；`preserve_item_ids` 从冻结 base_revision 展开 | 头部「实施中」未回填（冲突见第七节 #5）|
| `docs/product/workspace-ai-workbench/U06-contract.md` | 草案与显式发布契约 | 机器契约 | 参考 | drafts 仅收 `{expected_version,revision,item_ids,title,client_key}`；publish 为唯一创建 Task 入口；`Registry.ResolveProjectBaseline` 只从持久执行上下文解析代码根；Task/DevelopmentContext/publication/Coordinator state 同事务；DTO 指向 `.agent-work/u06-api-contract.md`（该目录由 `agent-team-workbench/.gitignore:12` 忽略、不在版本控制内；字段契约现行在 `agent-team-workbench/contracts/web/openapi.yaml:952-996`）| 同 U05 |
| `docs/product/workspace-ai-workbench/U07-contract.md` | 完整验收与旧链清理 | 机器契约 | 参考 | 删除 stateless `/task-intake/analyze` provider/HTTP/OpenAPI；保留 `/task-chat/*` 重定向与 v1/v2 localStorage 只读恢复；完成标准「旧直接模型入口不存在」；staged SHA `ff470141…` | 2026-09-09，有效 |
| `docs/product/workspace-ai-workbench/U01-acceptance.md`、`docs/product/workspace-ai-workbench/U02-acceptance.md`、`docs/product/workspace-ai-workbench/U03-acceptance.md` | 空间、知识隔离、原件读取验收 | 验收证据 | 参考（已冻结）| U01 48 项 HTTP/SQLite（34+9+5）；U02 45 项 + 16 项双空间 CLI token 不可互换；U03 49+11 项 + 32 项真实模型读 DOCX/PPTX/MD/源码/知识；已知失败：application 全包 race 600s 超时、DeepSeek 403 | 2026-09-09 |
| `docs/product/workspace-ai-workbench/U04-acceptance.md`、`docs/product/workspace-ai-workbench/U05-acceptance.md` | 分析问答与产品确认验收 | 验收证据 | 参考（已冻结）| U04 四类来源/五类事项/三问题、18 项检查、答案 2 answered+1 deferred、package.json 误推断被纠正；U05 13 项、7 条不可变产品记录、2 条重开、3 个有效事项、130 文件/1011 测试 | 2026-09-09 |
| `docs/product/workspace-ai-workbench/U06-acceptance.md`、`docs/product/workspace-ai-workbench/U07-acceptance.md` | 发布与端到端验收 | 验收证据 | 参考（已冻结）| U06 Task `wi_01M22P2JB25FX6VFDHMS94X042`、BaseRevision `2bbfb37a…`、0054→0055 追加迁移、Mock 无 PlanDecisionV2→blocked；U07 Run `run_01M22RPGQ4N8BM7NQPF5ZB5BK5`、10 次工具调用、nonce 仅存原文件、`POST /workspaces/{ws}/task-intake/analyze` 返回 404 | 2026-09-09 |

`docs/architecture/` 7 份（全 `.md`，无子目录）：

| 路径 | 主题 | 知识类型 | 权威级别 | 关键内容要点 | 时效 |
|---|---|---|---|---|---|
| `docs/architecture/system-architecture-handbook.md` | 系统现状事实源与源码导航 | 架构设计/设计规范 | 现行事实源 | 单一真相矩阵（WorkItem/Plan/Run/Coordinator/Goal/Todo/Quota/TurnReceipt/CanonicalUsage/ExecutionContext/TaskComment/Approval/Event/Artifact/DeliveryBrief）；Run 13 态，`createRunLocked`/`transitionRunLocked` 唯一写点；迁移时间线 0001–0042；裁决序 源码>测试>本文>note | 2026-09-03 @82665d8；迁移段落已落后仓库（冲突见第七节 #7）|
| `docs/architecture/c4-container-diagram.md` | C4 L2 容器图 | 架构设计 | 参考（非事实源，未覆盖治理层）| 6 容器（web/control-plane/runnerd/migrate/atw-mcp/SQLite）；三契约 `openapi.yaml`、`asyncapi.yaml`、`runner/v2/schema.json`；`/runner/v2/connect`、`atw_host_<id>_<secret>`；Runner v1 与 `contracts/runtime/v1` 已退役；认证仍 demo role | 2026-08-31；缺 0024–0042 治理表，明显滞后 |
| `docs/architecture/task-control-surface-context-design.md` | 执行上下文、评论与验收读模型 | 架构设计（implemented）| 现行事实源（专题）| `ExecutionContextSnapshot` 13 身份字段+digest、`ResolvedExecutionContext` 仅进程内；TaskComment revision+消费水位；Review Queue/Delivery Brief 确定性读模型；API `/review-queue`、`/delivery-brief`、`/execution-context`；迁移 0021/0022；§2.2 旧假设删除清单 | 2026-09-03；implemented |
| `docs/architecture/loopx-native-governance-implementation-plan.md` | 治理语义移植 WP0–WP7 | 架构设计/验收证据 | 参考（计划）| 不变式：Todo 只经 `TodoToPlanCompiler→SubmitPlan`、TurnReceipt append-only、`canonical_usage` terminal-only（0035 trigger+Go guard）、root blocker 四层同事务；迁移 0024–0035/0039–0042；WP 状态表与外部 gate | 2026-09-03；repository complete，real Provider/Host gate pending |
| `docs/architecture/2026-09-01-loopx-task-foundation-architecture-assessment.md` | LoopX 底座三路线评估 | 决策留痕/外部参考 | 已冻结（仅追溯）| A/B/C 评分 61/96/72，采纳 B；原生移植停止条件与整体迁移复活条件；JSON Plan 边界问题 | 2026-09-01，证据截止 08-31；自述「不承担当前实现状态的事实源」|
| `docs/architecture/clawteam-borrowings-design.md` | ClawTeam 借鉴 F1–F5 | 架构设计/决策留痕 | 参考 | F3 `client_key`(0013)、F5 `atw-mcp` 7 只读+claim/return、F1 任务级锁 0014+`sweepStaleLocks` 已入 main；F2 挂起、F4 砍；不借清单 | 2026-08-24 定案 |
| `docs/architecture/knowledge-librarian-design.md` | 知识管理员读写闭环 Harness（旧链）| 需求/架构设计 | 参考（自述非完成声明）| 版本发布/候选核实/双向往返关系/发布投影；AC01–AC12；Run 绑定 CLI bridge、`knowledge-access/` 0700/0600；不新增第二套 Run/Lease | 2026-09-06/07；仅本机闭环，描述的模型已被 09-12 重建取代（冲突见第七节 #27、#36）|

### 3.2 协议契约与前端规范（`04`–`05`）

`docs/protocol/` 3 篇 + `agent-team-workbench/contracts/` 4 份机器契约 + 2 个 embed 门禁文件：

| 路径 | 主题 | 知识类型 | 权威级别 | 关键内容要点 | 时效 |
|---|---|---|---|---|---|
| `docs/protocol/codex-app-server-v2.md` | Codex app-server adapter 契约 | 机器契约/设计规范 | 现行事实源（Status: implemented）| 基线 Codex CLI `0.149.0` + v2 schema SHA-256 `6f76cce2…`；`initialize`→`thread/start\|resume`→`turn/start`，`turn/completed` 为终态权威；配置映射表（approval_policy→never/on-request/untrusted）；11 条事件投影表；7 条显式限制（MCP elicitation、`item/tool/call` 回 `-32601`）| 无 Date，基线钉 0.149.0；升级流程在同目录 `references/` |
| `docs/protocol/mcp-tools.md` | atw-mcp stdio 工具面 | 机器契约/设计规范 | 现行事实源（代码事实源 `agent-team-workbench/internal/mcpserver/` + `agent-team-workbench/cmd/atw-mcp`）| 25 个工具；写面仅 5 类 Service 命令（task_claim/return、todo_claim/release、resolve_user_action）；红线 7 类刻意不暴露（approval resolve、WorkItem CRUD、会话重置、Handoff 写、ProjectionRepair、Delivery Brief snapshot、Quota reconciliation），`TestToolRegistryRedLine` 钉死 | 标注 2026-09-02 |
| `docs/protocol/knowledge-librarian.md` | 知识管理员部署与调用协议（旧链）| 设计规范+接口说明 | 声明以 openapi 为准，实为过期 | `/knowledge/*` 17 端点 + `/knowledge-agent/*` 6 端点；`KnowledgeJob` 状态机（7 终态）；来源核验层级 `run_output_verified`/`agent_identity_only`；`atw-knowledge` CLI + 0600 capability 桥；§1.1 Chat 预取写 `run.Input["knowledge_context"]` | Date 2026-09-06，实现已于 09-12 删除 |
| `agent-team-workbench/contracts/web/openapi.yaml` | 浏览器↔控制面 HTTP | 机器契约 | 现行事实源 | 141 paths / 约 460 schema；`/api/v1`、ID 前缀 `ws_/kb_/kbv_/kss_/kbj_`、写命令 Idempotency-Key、可变资源 version、错误 `application/problem+json`；`/library/*` 20 path / 23 operation（14 `GET` + 7 `POST` + 1 `PATCH` + 1 `DELETE`）= 单一 workspace 统一资料库；治理面 goal/todo/handoff/turn receipt/quota/projection 全量 | v0.2.0，最后提交 2026-09-13 |
| `agent-team-workbench/contracts/events/asyncapi.yaml` | SSE 事件契约 | 机器契约 | 现行事实源 | AsyncAPI 3.0.0；`GET /api/v1/workspaces/{workspace_id}/events`，`Last-Event-ID` 补发、15s heartbeat、游标越界 410 `cursor_expired`；`stream_seq`/`run_seq` 由控制面分配；29 个 `x-lifecycle: live` 事件（`message.delta`、`turn.receipt_appended`、`quota.*`、`handoff.*`、`projection.*`）| 最后提交 2026-09-09 |
| `agent-team-workbench/contracts/runner/v2/schema.json` | Runner WSS Protocol v2 信封 | 机器契约 | 现行事实源 | 9 方法 `runner.hello/server.welcome/run.offer/run.accept/run.reject/run.command/run.event/ack/heartbeat`；三重身份 `boot_id`+`connection_epoch`+`fencing_token`；事件身份 `(run_id, lease_id, runner_id, producer_seq)`；旧 epoch 帧 ACK 后丢弃 | 2026-09-03 |
| `agent-team-workbench/contracts/control/plan-decision-v2.schema.json` | Planner→控制面决策信封 | 机器契约 | 现行事实源 | `schema_version=plan-decision/v2`、`steps` 1..64、`oneOf` Dispatch/Defer/Join/Finish；自声明「通过本契约只代表 wire shape 合法」，workspace/owner/barrier/quota 仍由 Go 语义校验后经 `SubmitPlan` | 2026-09-03 |
| `agent-team-workbench/contracts/control_embed.go`（+`_test.go`）| 契约嵌入与摘要 | 机器契约（实现门禁）| 现行事实源 | `//go:embed control/plan-decision-v2.schema.json`，`contracts/` 源文件是唯一可编辑真相；导出 `PlanDecisionV2SchemaDigest()` | 2026-09-03 |
| `agent-team-workbench/contracts/control/knowledge-librarian-v1.schema.json`（已删除）| 知识管理员决策 schema | 归档（已删除）| 已冻结（仅追溯）| 562 行，随提交 `8e8a389`（2026-09-12）连同 `cmd/atw-knowledge`、`agent-team-workbench/internal/application/knowledge_*` 一并删除 | 2026-09-03→09-12 |

前端规范与门禁（`docs/frontend/` 2 篇 + `agent-team-workbench/web/` 下的规范、生效值与门禁）：

| 路径 | 主题 | 知识类型 | 权威级别 | 关键内容要点 | 时效 |
|---|---|---|---|---|---|
| `docs/frontend/chat-content-blocks-v1.md` | LanguageGUI ContentBlock v1 wire 契约 | 机器契约 | 现行事实源 | 信封 `{version:"languagegui/v1",blocks}`；12 类 block 及上限（table 12 列/100 行、chart 1–4 series、review-summary 固定 verdict/severity 枚举 + 30 findings/20 checks/12 next_steps）；`file.path` 仓库相对路径只读预览白名单；流式与 canonical `content_blocks` 同解析器 | 2026-08-27，implemented；09-13 仍在改，有效 |
| `docs/frontend/chat-rendering-spec.md` | 对话渲染清单与样式规格（9 层）| 设计规范 | 现行事实源 | 段层 TranscriptSegment、`TOOL_BODY_RENDERERS` 注册表、WorkTimeline 折叠行、`.chat-prose` 排版；自声明数值以 `index.css/.module.css/tailwind.config.js` 为准；第 9 节缺口与路线 | 2026-08-26，implemented；引用已失效小节（冲突见第七节 #14、#15）|
| `agent-team-workbench/web/DESIGN.md` | 前端设计事实源（frontmatter v2.0.0）| 设计规范 | 现行事实源 | 语义 token 全清单（brand/surface/text/status/`identity-1..8`）；主题唯一 store `workbench-theme.store.ts`、持久化 key `chat:theme`、门户层靠 `html[data-workbench-theme]`；920px chat rail、240px 固定侧栏；交付型文档拆分规则；§Forbidden presentation patterns | version 2.0.0，mtime 09-13，有效 |
| `agent-team-workbench/web/src/index.css`（`.chat-*` 区）、`agent-team-workbench/web/tailwind.config.js`、`agent-team-workbench/web/src/design/motion.ts` | token 生效值与工具类映射 | 机器契约 | 现行事实源（数值）| 384 处 `--color-*`；`surface-glass`、`status-standby`、`identity-1..8` 仅头像/归属；只允许 `hsl(var(--color-*))` 或 Tailwind 语义类 | mtime 09-13 20:00，有效 |
| `agent-team-workbench/web/src/design-tokens.test.ts`、`agent-team-workbench/web/src/tailwind-alpha-scale.test.ts` | 设计门禁（两道）| 机器契约 | 现行事实源 | 禁 hex/rgb/hsl 字面量与 `text-white`/默认调色板类；唯一豁免 `src/components/chat/blocks/ansi.ts` 且断言豁免清单不可扩容；透明度修饰须 5 的倍数（根因 2026-08-27 审批 composer 白框）| 08-30/09-08，有效 |
| `agent-team-workbench/web/scripts/assert-chat-output-layout.js` | 浏览器布局回归断言 | 验收证据 | 参考（非事实源）| 校验 `[data-content-block]` 类型与顺序；实测 `.chat-prose` 级联间距（首段 0、`p+p>0`、`h2>p`、引用内 `p+p>0`）| 09-10，有效 |
| `agent-team-workbench/web/design-qa.md` + `agent-team-workbench/web/design-qa-assets/`（32 图，16 png + 16 jpg）| LanguageGUI WorkTimeline 视觉验收 | 验收证据 | 已冻结（仅追溯）| Iteration 8 判定 + 7 项 fidelity surfaces；44px Run 行、`已编辑 N 个文件` 卡、760px 审核抽屉、LCS `+A/−D` | 2026-08-28/29 快照，非现行结论 |
| `docs/archive/design/frontend-design-md-redesign.md`、`docs/archive/design/design-audit-2026-08-25.md` | DESIGN.md 重设计方案 / 时点审计问题单 | 归档 | 已冻结（仅追溯）| 前者定义 frontmatter→Do's/Don'ts→Known Gaps 结构（v2 已改写，命名不可作现行依据）；后者原话「本文不再更新」，#1/#2/#8 已落地 | 2026-08-25/26 |
| `docs/references/design-resource-library.md`、`docs/references/design-asset-index.md`、`docs/references/zcode-desktop-interaction-report.md`、`docs/references/codex-desktop-*.md` | 设计素材检索与交互逆向基准 | 外部参考 | 参考（非事实源）| 选站库单 vs 素材级明细分工，付费站清单 2026-08-27 核验；`markdown-tags-inventory` 自声明「不再是当前视觉基线」| 2026-08-27，有效 |

### 3.3 外部参考 / 验收证据 / 归档（`06`–`08`）

`docs/references/` 9 份（无子目录，约 98KB），全部为「外部参考」类，无需求/契约/验收证据：

| 路径 | 主题 | 知识类型 | 权威级别 | 关键内容要点 | 时效 |
|---|---|---|---|---|---|
| `docs/references/codex-appserver-official-protocol-reference.md` | Codex app-server 官方协议锚点 | 外部参考（检索地图）| 参考 | Thread/Turn/Item 原语；`initialize`→`thread/start`→`turn/start`；过载 `-32001`；钉 CLI `0.149.0` + experimental schema SHA `6f76cce2…`；4 步升级规程 | 核订 2026-08-28；契约真相源在 `docs/protocol/codex-app-server-v2.md` |
| `docs/references/codex-desktop-rendering-comparison.md` | Codex 桌面渲染逆向（槽位模型）| 外部参考 | 参考 | bundle `com.openai.codex` 26.818.41509 asar；turn 槽位序 `agent-activity-collapsible`→`assistant-item`→`proposed-plan`→`thinking-placeholder`→`turn-diff`；`--thread-content-max-width:40rem`；Q1–Q10 | 2026-08-24；§2「我们的 Web 端现状」为当日快照，已过期（冲突见第七节 #17）|
| `docs/references/codex-desktop-markdown-tags-inventory.md` | Codex 正文 Markdown 标签清单 | 外部参考 / 归档 | 已冻结（仅追溯）| 预处理 `Kpa()`（GitHub alert→`**Note**`、`details`→`:::github-details`）；三层覆盖表 `Ora()`/`Pra()`/`Nra()`；`codexDirective` 指令表 | 2026-08-24；`chat-rendering-spec.md:12` 明示「不再是当前视觉基线」|
| `docs/references/zcode-desktop-interaction-report.md` | ZCode 交互逆向（时间序 row 模型）| 外部参考（交互基准）| 参考 | ZCode 3.10.1/6272；row 纵向追加；`autoCollapseKey` streaming→settled 自动收起；多段 reasoning 不合并；工具失败留卡 vs 模型失败走 Error Banner；§7 对齐清单 | 2026-08-28；仍被 `chat-rendering-spec.md` 引用为基准 |
| `docs/references/swarm-chat-body.md` | Kimi 蜂群正文索引 | 外部参考（指向生产事实）| 参考 | 仅 Kimi `AgentSwarm` 成员进蜂格；唯一阅读器 `AgentTranscriptReader`/`transcript-view.tsx`；投影 `agent-transcript-projection.ts`；`kimiapp.go` 事件面 | 2026-08-29 已生产落地，有效 |
| `docs/references/cli-prompt-engineering.md` | 提示词堆叠与输出纪律对照 | 外部参考 + 设计输入 | 参考 | Codex 消息层叠加 vs Kimi 模板槽位（`reply_style_guide`/`hostIdentity`）；三路径：codexapp `developerInstructions`、kimi `--agent-file`+`${base_prompt}`、kimiapp 首条用户消息前缀（最弱）；languagegui/v1 补丁建议 | 2026-08-28；§5 建议已落 `notes/implemented/feature/2026-08-28-output-contract-discipline.md` |
| `docs/references/clawteam-openclaw-comparison.md` | ClawTeam 竞品对比 | 外部参考（借鉴）| 参考 | 文件系统控制面（`~/.clawteam/`+flock）；4 态看板 + `blocked_by` DAG；`locked_by` 任务锁与死 owner 抢占；相位机 discuss→plan→execute→verify→ship；5 借 3 不借 | 2026-08-24（`v0.3.0+openclaw2`）；借用落地见 `docs/architecture/clawteam-borrowings-design.md` |
| `docs/references/design-resource-library.md` + `docs/references/design-asset-index.md` | 设计资源选站 / 选素材 | 设计规范（检索工具）| 参考 | AICSS 14 组件（`approval-card` 为审批卡工艺基准）；Curated `web-apps` 分类 + 2229 sections；ohwow 不设素材索引；LanguageGUI 5 类目 | 2026-08-26/27 核验；付费项标「判断」列 |

`docs/testing/` 10 份 + `docs/review/` 155 份（`review` 顶层仅 2 个 md，其余在 `assets/`）：

| 路径 | 主题 | 知识类型 | 权威级别 | 关键内容要点 | 时效 |
|---|---|---|---|---|---|
| `docs/testing/web-idea-java/README.md`、`docs/testing/web-idea-java/decisions.md` | 范本定位与取舍 | 规格 / 决策留痕 | 参考（非事实源），decisions 标注 `proposed` | 范本要证明的 5 点；强制分层 `user_input/repo_fact/reference_capability/proposal/synthetic_input/unverified`；首问「编辑/重构 vs 嵌入只读」仍未裁决 | 2026-09-09，有效；与 U07 验收时效冲突（第七节 #21）|
| `docs/testing/web-idea-java/scenario-contract.md` | 需求分析场景契约 | 机器契约（`proposed test fixture contract`）| 参考 | 8 场景；每轮只问一个问题；`initial/confirmed/changed/unreadable/stale_code/broken_attachment/untrusted_attachment` 阶段；来源不可读必须停手 | 2026-09-09，有效 |
| `docs/testing/web-idea-java/current-capabilities.md` | Java 能力现状对照 | 验收证据（限定快照事实）| 参考 | standalone `2bbfb37` 有编辑与 `PUT /fs/file`，嵌入面写死 `readOnly:true`；诊断/hover/rename 均 `not_exposed`；补全标 `partial_readonly` | 2026-09-09，对固定快照有效 |
| `docs/testing/web-idea-java/intellij-reference.md`、`docs/testing/web-idea-java/reference-verification.json` | IntelliJ 参考取证 | 外部参考 | 参考（自述「只负责参考仓库」）| 固定 SHA `5ada6f537896bdf5d549ac12cd7f3e38ff637093`；Community ≠ Ultimate；PSI/Index 不能嵌入浏览器 | 2026-09-08 采集 |
| `docs/testing/web-idea-java/capability-matrix.json`、`docs/testing/web-idea-java/source-manifest.json` | 能力骨架与源码指纹 | 机器契约 | 参考 | 20 能力族 `JAVA-001…`；12 来源 / 7 阶段；12 个关键源码文件 SHA-256 | 2026-09-08，有效 |
| `docs/testing/web-idea-java/verification.json`、`docs/testing/web-idea-java/office-fixture-qa.md` | 范本自检 | 验收证据 | 参考（自述「不代表产品完成率」）| `fixture_integrity` 12 源/7 阶段/8 用例；13 项 reader 回归；`SRC-DOCX-01`/`SRC-PPTX-01` 各 2 页 | 2026-09-08，有效 |
| `docs/review/2026-09-02-loopx-native-governance-completion-audit.md` | 原生治理 WP0–WP7 完成审计 | 归档（含验收证据）| 已冻结（仅追溯，WP 计划已落地）| 迁移 `0024–0035`、`0039–0042`；blocked/unblock 同事务原子；外部 Runner/Provider gate 未过 | 2026-09-02 建、09-03 复核 |
| `docs/review/knowledge-librarian-acceptance.md` | 知识管理员闭环验收（旧链）| 验收证据 | 参考（对实现有效，跨引用 architecture/product/protocol）| 测试名 `TestScopedRetriever…`、`TestKnowledgeFinishGateRejectsDroppedDiscoveredEndpoint`、`TestKnowledgeRunCapabilityCannotBeForgedOrUsedAfterCompletion`；不可变版本 + 废止留历史 | 2026-09-06/07 |
| `docs/review/assets/u01-global-workspace/`、`docs/review/assets/u02-agent-knowledge/` | U01 空间 / U02 知识隔离证据 | 验收证据 | 参考 | 48 项 HTTP/SQLite；`final-boundary-checks.json`（`workspace_location_ambiguous` 409）；U02 45 项 HTTP + 16 项真实 CLI 凭据 | 2026-09-09 |
| `docs/review/assets/u03-chat-sources/`、`docs/review/assets/u04-analysis-questions/` | 原件读取 / 逐题分析证据 | 验收证据 | 参考 | U03 49 HTTP + 11 完整性 + 32 真实模型；U04 逐题推进、`invalid-output-preserves-valid.json`、草稿恢复 | 2026-09-09 |
| `docs/review/assets/u05-decisions-changes/`、`docs/review/assets/u06-task-publication/` | 决定变更 / 任务发布证据 | 验收证据 | 参考 | U05 `evidence-provenance.json` 记首个真模型 turn 被拒后修正；U06 发布 0054→0055 升级、`go-project-baseline.json` 来自正式 Registry | 2026-09-09 |
| `docs/review/assets/u07-web-idea-acceptance/` | 端到端真实模型验收 | 验收证据 | 参考 | `verification.json`：12 项/4 问题/3 决定全保留、nonce 三方核验；`final-gates.md` 前端 129 文件/1004 测试 | 2026-09-09 |
| `docs/review/assets/*.png`（governance-*）| 治理面板浏览器截图 | 验收证据 | 参考 | blocked 态 1440/1024 × light/dark、治理链一致、无横向溢出 | 2026-09-03 |
| `docs/review/assets/knowledge-reading/`、`docs/review/assets/product-agent-canvas/`、`docs/review/assets/knowledge-final-inquiry.json` | 阅读画布 / 知识调研证据 | 验收证据 | 参考 | 双语主题与 768 断点截图；`kbj_01M1TNG6JGAD0DM926FJQCZENS` 有界调研（4 turn/1 search/6 read/14 relation）| 2026-09-07/08 |

`docs/archive/` 24 份 / 5 子目录（prompt-library 12、prototype 6、design 3、reviews 2、security 1）：

| 路径 | 主题 | 知识类型 | 权威级别 | 关键内容要点 | 时效 |
|---|---|---|---|---|---|
| `docs/archive/design/frontend-design-md-redesign.md` | DESIGN.md 法典化 + 组件库重构 | 架构设计/设计规范 | 已冻结 | M0/M1/M2 三段式迁移；`web/src/components/ui/` 基座；否决「换皮」（奶油底+珊瑚+衬线）；token 纪律硬门禁；§2.4 蜂群前端子任务提示词模板 | 2026-08-26 快照，视觉事实源已转 `agent-team-workbench/web/DESIGN.md` |
| `docs/archive/design/design-audit-2026-08-25.md` | redesign-skill 审计问题单 | 验收证据（时点快照）| 已冻结 | P1 七项（骨架屏、看板加载、原生 select、语义标签、空列、校验、404 静默重定向）；P2 六项；明列「不适用项」（hero/定价/移动端）| 2026-08-25；#1/#2/#8 已落地 |
| `docs/archive/design/swarm-chat-body-demo.html` | 蜂群 Chat 正文静态视觉稿 | 设计规范 | 参考 | `--hive/--ok/--warn/--err` token、`--rail:720px`；生产实现索引在 `docs/references/swarm-chat-body.md`（`swarm-chat-block.tsx`）| 2026-08-29 确认方向 |
| `docs/archive/prompt-library/codex/`（6 件，均 `.md`）| OpenAI Codex CLI 原始提示词 | 外部参考 | 参考（非事实源）| `base-instructions-generic.actual.md`（本机 rollout 实证，21KB 底座）、`gpt_5_2_prompt.upstream.md`（三档篇幅硬限额）、`gpt_5_codex_prompt.upstream.md`、`collab-family-extracted.md`（50–70 行硬顶）、`cloud-desktop-family-extracted.md`、`collab-agent.experimental.md`（`spawn_agent`/`followup_task`）| 2026-08-28 提取（Codex CLI 0.149.0）|
| `docs/archive/prompt-library/kimi-code/`（6 件）| MoonshotAI Kimi Code 原始 system 提示词 | 外部参考 | 参考（非事实源）| `system.{agent,coder,explore}.md`（v0.38.0 wire.jsonl 渲染）、`system.expert-{software-architect,software-product-manager,review-lead}.md`；模板槽位替换、final 交接 minChars=200、`path:line` 引用 | 2026-08-28（v0.38.0）|
| `docs/archive/prototype/`（6 件）| 原型包/早期协议/产品简报/IA/线框 | 归档（需求+早期架构）| 已冻结 | `product-brief.md`（三屏、60px 图标导航、280px 详情面板）、`confirmed-ia.json`（`frozen_structure`）、`agent-team-dashboard-web-protocol-go.docx`、`runtime-agnostic-…-system-design.docx`、`agent-team-dashboard-prototype.zip`（86 文件，supabase+rspack+ojo）、`wireframe.jpg` | 2026-04~08；「桌面端 SaaS/演示角色」定位已过时 |
| `docs/archive/reviews/2026-08-25-project-review-detailed.md`、`docs/archive/reviews/2026-08-27-project-quality-assessment.md` | 项目详细评审 / 质量评估 | 验收证据（时点快照）| 已冻结 | 五维 7.5/8.5/5/6.5/6；Run 13 态、`LockedByRunID`；78/100（B），P0 `turn-block.tsx:3` 未用导入、缺 `pnpm-lock.yaml`、CI 无前端门禁；覆盖 httpapi~29% / SQL~21% | 2026-08-25/27 |
| `docs/archive/security/security-findings-inventory-2026-08-29.md` | 安全高危处置台账 | 决策留痕/验收证据 | 已冻结 | Mimosa 10 findings；A：6 处 adapter `exec.Command`（参数注入，非 CWE-78）；B：`run_changes` 路径穿越不成立；C：测试夹具误报豁免；D：`tools/wan_video.py` 仓外挂账；E：4 包 35 条 advisory 未枚举 | 2026-08-29；2026-08-31 归档，当前状态以 `notes/implemented/bug-fix/2026-08-29-mimosa-gate-triage.md` 为准 |

### 3.4 代码内与运行时知识（`11`）

约 52 个文件（`agent-team-workbench/knowledge/` 3、`internal/knowledgelib/` 9、`internal/application/` 知识件 10（6 源码 + 4 测试）、`internal/httpapi/` 2、`internal/domain/` 1、`internal/persistence/sqlstore/` 4、`migrations/` 15、指令/README 8）：

| 路径 | 主题 | 知识类型 | 权威级别 | 关键内容要点 | 时效 |
|---|---|---|---|---|---|
| `agent-team-workbench/knowledge/README.md` | 知识层 Markdown 表达约定 | 设计规范 | 现行事实源（但陈述已滞后）| 与 `agents/<slug>/prompt.md` 分层；声明 SQLite 为「唯一有效状态入口」、Markdown 仅显式导入/导出；`prd/`/未来 `arch/`、`dev-std/` 为交换目录；条目规范指向 charter §3 | 2026-09-07；与 09-12 重建冲突（第七节 #36）|
| `agent-team-workbench/knowledge/prd/evolution-roadmap-next-phase.md` | 下一阶段产品路线图 | 需求＋决策留痕 | 参考（非事实源）| 三阶段闸门（CI/凭据/认证）、P0 遗留清单、RICE 72、12 周工期、PHASE 验收标准 | 2026-08-28 定稿；已过期 |
| `agent-team-workbench/internal/knowledgelib/*.go`（9 文件）| 统一资料库核心：记录语法/快照/证据/投影 | 机器契约 | 现行事实源 | `RecordSyntaxVersion="kb-note/0.2-draft"`；Markdown 为 writing truth、SQLite 为可重建投影；zone `content/` `catalog/` `_sources/` `_system/{releases,representations,staging,legacy-import}`；relation 谓词 14 个；perspective `normative/descriptive`，basis 4 值；evidence role `supports/contradicts/context` | 2026-09-12 |
| `agent-team-workbench/internal/domain/knowledge_library.go` | 写任务状态机 | 机器契约 | 现行事实源 | 8 态 `queued/running/awaiting_agent/retry_wait/blocked/completed/failed/cancelled`；`IsTerminal`/`OccupiesHead` 定义单写者槽；`max_attempts=3`、`max_repair_attempts=2` | 现行 |
| `agent-team-workbench/migrations/0057–0064` | 新资料库 DDL | 机器契约 | 现行事实源 | 表：`knowledge_libraries`、`knowledge_library_sources`、`knowledge_documents(+_versions)`、`knowledge_assertions(+_relations)`、`knowledge_evidence`、`knowledge_releases(+_documents)`、`knowledge_write_tasks`、`knowledge_snapshots`、`knowledge_requirement_inputs`；release `published/superseded`；publication `prepared/committed/abandoned`；FTS5 `knowledge_search_index` | 2026-09-12 |
| `agent-team-workbench/migrations/0044/0046–0048` | 旧 per-item 知识管理员 | 机器契约 | 已冻结（仅追溯）| `knowledge_items` status `candidate/draft/effective/superseded/repealed`；写任务 kind `initialize/incremental/remove/rename/invalidate/branch_view/legacy_import/reindex`；0048 librarian 唯一身份＋`prompt_version='knowledge-librarian-chat/v1'`、sandbox `read-only`、tools 空 | 已退役（0065 DROP）|
| `agent-team-workbench/migrations/0065` | 旧表退役决定 | 决策留痕 | 现行事实源 | DROP 15 张旧表；仅 4 表有残留 39 行（**本次核对**：`.acceptance/backups/2026-09-12-legacy-knowledge-tables/` 全仓不存在，该字符串只出现在 `migrations/0065` 与本机退役测试注释里，属本机产物而非仓库内容）；`agent-team-workbench/internal/persistence/sqlstore/knowledge_library_retirement_test.go` 钉死无引用 | 2026-09-12；有效 |
| `agent-team-workbench/internal/application/knowledge_library{,_worker,_requirement,_retriever}.go` | 应用层队列与需求冻结 | 机器契约 | 现行事实源 | 单一 FIFO 队列、`requirement_input_id` 冻结需求全文（不读 `content_ref`）；run 终态收件不新增执行权威 | 现行 |
| `agent-team-workbench/internal/httpapi/handlers_knowledge_library.go` | 知识层 HTTP 面 | 机器契约 | 现行事实源 | 23 条路由（14 `GET` + 7 `POST` + 1 `PATCH` + 1 `DELETE`）挂在 `/api/v1/workspaces/{workspace_id}/library*`：query/expand/graph/bridges/evidence/documents/releases/tasks/{id}/retry/cancel/reindex/events/sources | 现行 |
| `AGENTS.md`、`integrations/web-idea/AGENTS.md` | Agent 指令纪律 | 设计规范 | 现行事实源 | worktree 隔离＋一任务一分支；分刀提交；删除优先于垫片；门禁按风险选；web-idea 五条范围红线与负向保证 | 现行 |
| `agent-team-workbench/agents/`、`agent-team-workbench/runtimes/`、`agent-team-workbench/models/`、`docs/README.md` | 目录真相源说明 | 设计规范 | 现行事实源 | `agent.yaml`+`prompt.md` 为真相源、DB 只是投影；Kimi 本机补丁构建 SHA-256 与回滚包；`models/registry.yaml` 真相源；docs 声明运行时知识状态归 SQLite（与 09-12 重建冲突，第七节 #36）| 现行 |

### 3.5 第二棵笔记树：`agent-team-workbench/notes/`（**本次核对**补充）

仓库内还有一棵与根 `notes/` 平行的笔记树，22 个 tracked 文件（14 篇 md + 8 个架构图可视化资产；另有未跟踪的 `.DS_Store`，不计入）。本次核对其实测构成：

| 位置 | 篇数 | 主题 |
|---|---|---|
| `agent-team-workbench/notes/implemented/architecture/` | 2 | Handoff 委派 Run（`2026-09-03-handoff-delegated-coordinator.md`）、统一知识库契约（`2026-09-12-knowledge-library-rebuild.md`）|
| `agent-team-workbench/notes/implemented/bug-fix/` | 3 | Kimi 分离任务收尾（09-07）、待答问题期间 run 投影（09-09）、推理链不得进助手正文（09-09）|
| `agent-team-workbench/notes/implemented/arch-review/` | 1 | 架构耦合评审（08-28；class 名不在根 `notes/` 的六类中）|
| `agent-team-workbench/notes/implemented/simplification/` | 1 | 退役旧资料管理员表、管理页改挂 `/library`（09-12）|
| `agent-team-workbench/notes/proposed/`（含 `architecture/`）| 4 | 架构耦合分析与评审（08-28 ×2）、产品演进路线图 v1（08-28）、Session→组织愿景（08-30）|
| `agent-team-workbench/notes/product/` | 2 | 项目详细评审（08-25）、项目质量评估（08-27）|
| `agent-team-workbench/notes/archived/architecture/` | 1 | 知识服务新手体验（09-07，`Status: archived`）|
| `agent-team-workbench/notes/architecture/`（非 md）| 8 | 「agent-team-workbench 当前架构」图 `.html` + `.json` 与 1440×900 / 2048×1320 明暗视觉核对产物 |

- 日期范围：文件名日期 2026-08-25～09-12；按 `git log --diff-filter=A` 的入库日期，整棵树最早 2026-09-03、最晚 2026-09-12。
- 与根 `notes/` 的分工：根 `notes/` 是「跨项目决策留痕」的统一落点（`docs/README.md:5`；`notes/implemented/process/2026-08-29-project-documentation-tree.md:7` 记录原 `agent-team-workbench/notes/` 已全量上移）。本树在上移之后重新积累，只装聊天/知识域的实施留痕与产品评审副本，**没有 README、没有 proposed/rejected 判据说明**。
- 与根 `notes/` 的重叠很小：同名仅 2 篇，且都不是同一份文档——`2026-08-28-architecture-coupling-review.md`（本树在 `implemented/arch-review/`）与根 `notes/archived/architecture/` 版差在冻结批注（根版有 `Status: archived` 与归档说明，本树版没有）；`2026-09-12-knowledge-library-rebuild.md` 在本树是 41 行的《统一知识库（资料管理员）契约》，与根版 136 行的《资料库重建：设计与接口契约》不是同一份，引用必须带全路径。
- 另有 2 篇是 `docs/archive/reviews/` 冻结副本的前身（内容近同，缺「历史评审快照」批注）：`agent-team-workbench/notes/product/2026-08-25-project-review-detailed.md`、`agent-team-workbench/notes/product/2026-08-27-project-quality-assessment.md`。

## 四、决策留痕全貌（根 `notes/` 口径；第二棵树单列于 4.7）

### 4.1 class × 条数 × 主题簇

| 位置 | 条数 | 主题簇 |
|---|---|---|
| `notes/implemented/architecture/` | 40 | 运行时底座、会话完整性与正文投影、会话元模型、任务控制面、LoopX 治理与计费、前端契约与主题、知识与资料库、产品画布与知识阅读 |
| `notes/implemented/feature/` | 30 | Chat 皮肤/UX/设计基座（08-24～08-28 共 19 篇）、任务工作台与知识入口（09-03～09-13）|
| `notes/implemented/bug-fix/` | 20 | 治理控制线与配置（08-29～09-03 共 13 篇）、UI 缺陷与恢复（09-05～09-08）|
| `notes/implemented/orchestration/` | 4 | 编排里程碑 M1–M4（`OrchestrationPlan` 词汇表、lead planner 评估、claim/join 护栏、knowledge retriever）|
| `notes/implemented/process/` | 2 | 仓库文档树（基线 `main@81f88fb`）、合并前 ego 隔离自验（`:8090` + `mount_generation`）|
| `notes/implemented/simplification/` | 1 | SQLite 单一存储、`migrations/` 单一真相源 |
| `notes/proposed/` | 6 | ①run/runner 运行时可观测（ZCode 事件契约、Run Journal ×2、Kimi 冒烟）②外部架构借鉴（LoopX 控制面）③远期组织形态（Agent 自建/协作空间）|
| `notes/rejected/` | 1 | Chat 交付文档正文形态 |
| `notes/product/` | 3 | 项目整体评审、Codex 协议合规审计、多 Agent 协作空间命题 |
| `notes/archived/` | 1 | 架构耦合评审（8 个脆弱点）|

日期跨 2026-08-23～09-13；`notes/implemented/` 97 篇中，**本次核对**按 `Status: implemented` / `状态：已实施` 宽松匹配命中 92 篇（表格内、无冒号等其它写法未计入，故「显式标注」的精确篇数无法核定）。

### 4.2 已实现结论 Top 清单

| 结论 | 路径 |
|---|---|
| `session_unknown` 永不静默降级 fresh（运行时硬约束成组固化）| `notes/implemented/architecture/2026-08-23-resume-never-silent-degrade.md` |
| 会话信息完整性 = 请求可重建，压缩必须分档留痕（`full/digest/handoff` + `session.decision`）| `notes/implemented/architecture/2026-08-27-session-integrity-design.md` |
| 会话降级为实现细节，`work_items` 是用户唯一对象 | `notes/implemented/architecture/2026-08-29-session-meta-model.md` |
| 唯一内置 `task_coordinator`，重试有硬上限且禁止 Coordinator 套 Coordinator | `notes/implemented/architecture/2026-08-30-system-task-coordinator.md` |
| 治理层只持意图/准入/收据，执行底座唯一；TurnReceipt append-only（RFC8785+JCS+SHA-256）| `notes/implemented/architecture/2026-09-01-loopx-native-governance.md` |
| Markdown 升为知识正文真相源，SQLite 仅存可重建投影 | `notes/implemented/architecture/2026-09-12-knowledge-library-rebuild.md` |
| SQLite 单一存储、`migrations/` 单一真相源（只接受 `sqlite://`，强制 FK/WAL/5s busy timeout）| `notes/implemented/simplification/2026-08-31-sqlite-only-storage.md` |
| 合并前隔离自验，不再先合并后验 | `notes/implemented/process/2026-09-13-pre-merge-ego-verification.md` |
| 旧表成建制退役：`0065` DROP 15 个对象，`/knowledge`→`/library` 且不加重定向（该留痕的落点是第二棵笔记树，见 3.5）| `agent-team-workbench/notes/implemented/simplification/2026-09-12-retire-legacy-knowledge-tables.md` |
| Chat 自动检索资料库 + 双智能体入口（`chatKnowledgeContextLimit = 5` 冻结进 `run.Input.knowledge_context`）| `notes/implemented/feature/2026-09-13-featured-agents-chat-knowledge.md` |

### 4.3 proposed（6 条，逐条状态）

| 路径 | 一句话结论 |
|---|---|
| `notes/proposed/architecture/2026-08-28-zcode-activity-event-contract.md` | **未实现**；拟增 `tool_family`/`activity_group`/`response_id`/`parent_call_id`，复用 `tool.*` 不加 event name；禁按名称/时间戳伪造父子 Run |
| `notes/proposed/architecture/2026-09-01-loopx-control-plane-research.md` | **未实现**（部分被治理合入间接吸收）；结论选「参考借鉴」；四缺口＝quota、`delivery_outcome`、repair/replan typed 路由、内核投影 `llm=no_api`；负向保证不引 Python、不以 Markdown 为真相源 |
| `notes/proposed/architecture/2026-09-02-run-journal-lifecycle-logging.md` + `notes/proposed/architecture/2026-09-03-run-journal-d2-phase-frames.md` | **已实现但错放 proposed**；main 已有 `run.phase_entered/closed`、`run.log_chunk`(64KB/run)、`run.decision`、`GET /api/v1/runs/{id}/journal`、`ReconcileOrphanedLocalRuns`；D2 远程只放行相位帧 |
| `notes/proposed/bug-fix/2026-09-03-all-kimi-smoke-problem-points.md` | **已实现但错放 proposed**；F1 评估 run 继承 coordinator RuntimePreference、F2 `plan_execution_failed` 分流、F3 semantic 自动 repair、F4 Accept 级联子任务；未做＝放宽「禁止静默回退 mock」 |
| `notes/proposed/feature/2026-08-27-agent-self-creation-sovereignty.md` | **未实现**；`recruit`/`retire` 替 MCP 写面、`created_by` 谱系、bundle `onboarding/`、`.agent-work/spaces/<slug>/`、预算信封、六条负向保证 |

### 4.4 rejected（含否决理由）

| 路径 | 否决理由 |
|---|---|
| `notes/rejected/feature/2026-09-13-chat-delivered-document-body.md` | 否决「交付文档强制写正文」：正文与解释糊在一起、文档无独立形态不能折叠；只给复制路径、放开 `file://`、整篇抄进 block JSON 三种替代均否决；在线口径＝正文 md 与 file 块任选 |

### 4.5 product（3 条）

| 路径 | 一句话结论 |
|---|---|
| `notes/product/2026-08-24-project-review.md` | 13 态状态机、Accept 唯一完工路径、约 56 REST 路由、7 类 adapter；风险＝演示认证/凭据明文回显/CI 缺失；存储章已失效（`notes/implemented/simplification/2026-08-31-sqlite-only-storage.md` 取代）|
| `notes/product/2026-08-28-codex-protocol-conformance-audit.md` | 钉 0.149.0：kebab-case `sandbox`、`approvalPolicy=untrusted` 正确，main 漂移升级即爆；缺口＝`error.message`/`will_retry`、`turn/diff/updated` |
| `notes/product/2026-08-31-multi-agent-collaboration-space-design.md` | US-01..05 / AC-01..08、RICE 排序、墓碑式独立持久化、AST 冲突检测；不做实时协同编辑与 Agent 直连 |

### 4.6 archived（1 条）

| 路径 | 一句话结论 |
|---|---|
| `notes/archived/architecture/2026-08-28-architecture-coupling-review.md` | 8 个脆弱点：application 4928 行枢纽、`EngineSink` vs `runnergateway.Engine` 接口分裂、`validateAdapterModel` 硬编码 adapter；2026-08-31 归档，自述不代表现状 |

### 4.7 第二棵笔记树（`agent-team-workbench/notes/`，22 文件）

**本次核对**补充；4.1–4.6 的条数与分类口径只覆盖根 `notes/`，不含本树。

- class 分布：14 篇 md ＝ `implemented/` 7（architecture 2、bug-fix 3、`arch-review` 1、simplification 1）+ `proposed/` 4 + `product/` 2 + `archived/` 1；另有 8 个架构图 `.html`/`.json`/`.png` 核对产物。
- 与根 `notes/` 的关系：2 篇同名但内容不同（见 3.5）；其余 12 篇在根 `notes/` 无同名条目，其中两条是现行知识域结论的实际落点——`agent-team-workbench/notes/implemented/simplification/2026-09-12-retire-legacy-knowledge-tables.md`（`0065` DROP 15 表、`/knowledge`→`/library`）与 `agent-team-workbench/notes/implemented/architecture/2026-09-12-knowledge-library-rebuild.md`（41 行契约版）。
- 引用注意：两棵树各有一份 `2026-09-12-knowledge-library-rebuild.md`，彼此不是同一文档，跨树引用必须带全路径；本文件此前把前者的落点误记为根路径，本轮已更正。
- 维护缺口：该树无 `README`，无 proposed/rejected 判据与迁入 `implemented` 时机的说明；`arch-review` 是根 `notes/` 六类之外的 class。

## 五、检索入口索引

「要办什么事 → 先看哪个文件」。路径均为仓库相对 POSIX 路径。

| 要办什么事 | 先看哪个文件 | 备注 |
|---|---|---|
| 改前端视觉 / 主题 / 组件观感 | `agent-team-workbench/web/DESIGN.md` | 设计事实源；数值以 `agent-team-workbench/web/src/index.css`、`agent-team-workbench/web/tailwind.config.js` 为准；改前必读 |
| 确认前端颜色/间距门禁边界 | `agent-team-workbench/web/src/design-tokens.test.ts`、`agent-team-workbench/web/src/tailwind-alpha-scale.test.ts` | 禁内联色值与 `text-white`；豁免清单不可扩容；透明度只能 5 的倍数 |
| 改对话正文渲染 / 内容块 | `docs/frontend/chat-content-blocks-v1.md`（wire 契约）、`docs/frontend/chat-rendering-spec.md`（9 层渲染清单） | 契约层与渲染层分开；渲染文档自认「漂移时以代码为准」；其外部引用有失效项（第七节 #14、#15）|
| 改协议 / 接口字段 | `agent-team-workbench/contracts/web/openapi.yaml`（HTTP）、`agent-team-workbench/contracts/events/asyncapi.yaml`（SSE）、`agent-team-workbench/contracts/runner/v2/schema.json`（Runner WSS）、`agent-team-workbench/contracts/control/plan-decision-v2.schema.json`（Planner 决策）| 四者为字段级事实源，由 `agent-team-workbench/internal/httpapi/contract_guard_test.go` 双向门禁（路由↔paths、事件常量↔enum）|
| 改 HTTP 接口前看人类可读说明 | `docs/protocol/mcp-tools.md`、`docs/protocol/codex-app-server-v2.md` | 前者钉 atw-mcp 25 工具与 7 类红线；后者钉 Codex CLI 0.149.0；知识域的 `docs/protocol/knowledge-librarian.md` 已过期，勿用（第七节 #10）|
| 查系统主调用链 / 现状事实 | `docs/architecture/system-architecture-handbook.md` | 单一真相矩阵 + Run 13 态 + 源码导航；裁决序「源码>测试>本文>note」；迁移段落滞后（第七节 #7）|
| 查容器边界与跨容器契约 | `docs/architecture/c4-container-diagram.md` | 未覆盖 0024–0042 治理表，与 handbook §5 矛盾（第七节 #8），只作参考 |
| 查执行上下文 / 评论 / 验收读模型 | `docs/architecture/task-control-surface-context-design.md` | implemented，专题事实源 |
| 查历史决策与否决记录 | `notes/implemented/{architecture,feature,bug-fix,orchestration,process,simplification}/`（根树 108 篇）+ `agent-team-workbench/notes/`（第二棵树 22 文件）| 分类见第四节：4.1–4.6 为根 `notes/` 口径，第二棵树单列于 4.7；否决理由单列在 `notes/rejected/` |
| 查某条否决为什么被否 | `notes/rejected/feature/2026-09-13-chat-delivered-document-body.md` | 目前唯一 rejected 条目，含三种替代方案的否决记录 |
| 查尚未实现的提案 | `notes/proposed/`（6 篇）| 注意其中 3 篇实际已实现（第四节 4.3，第七节 #28）|
| 查验收证据（U01–U07）| `docs/product/workspace-ai-workbench/U0*-acceptance.md`（结论侧）+ `docs/review/assets/u0*/verification.json`（证据侧）| 二者是「结论 ↔ 证据」配对；`u01`、`u07` 的部分陈述未回填（第七节 #20、#21）|
| 查治理链 / 知识库验收证据 | `docs/review/2026-09-02-loopx-native-governance-completion-audit.md`、`docs/review/knowledge-librarian-acceptance.md`、`docs/review/assets/*.png` | 后者为旧链验收，对应实现已删（第七节 #34）|
| 改知识库（资料库）| `notes/implemented/architecture/2026-09-12-knowledge-library-rebuild.md` → `agent-team-workbench/contracts/web/openapi.yaml`（tag `knowledge-library`）→ `agent-team-workbench/internal/knowledgelib/`、`agent-team-workbench/migrations/0057–0065` | 重建笔记是现行事实源；`agent-team-workbench/knowledge/README.md` 的存储口径已过期（第七节 #36）；对应测试 `agent-team-workbench/internal/persistence/sqlstore/knowledge_library_retirement_test.go`；退役留痕在第二棵树 `agent-team-workbench/notes/implemented/simplification/2026-09-12-retire-legacy-knowledge-tables.md`（见 3.5）|
| 改知识库消费面 | `notes/implemented/feature/2026-09-13-featured-agents-chat-knowledge.md`（Chat 预取）、`notes/implemented/feature/2026-09-13-product-knowledge-canvas-restore.md`（画布）| 画布与 `web/DESIGN.md` 的退役声明冲突（第七节 #13）|
| 查外部系统行为（Codex / Kimi / ZCode / ClawTeam）| `docs/references/`（协议锚点 `codex-appserver-official-protocol-reference.md`、交互基准 `zcode-desktop-interaction-report.md`、提示词 `cli-prompt-engineering.md`）+ `docs/archive/prompt-library/` 原文 | 只作参考，不作本项目事实源；Codex 升级前必须重核 `notes/product/2026-08-28-codex-protocol-conformance-audit.md` |
| 改运行时 / 会话 / 取消 / 锁行为 | 根 `AGENTS.md`「会话/运行时硬约束」+ `notes/implemented/architecture/2026-08-23-*` 成组留痕 | 违反 `session_unknown`/13 态/墓碑等约束即为回归 |
| 加迁移 / 改存储 | 根 `AGENTS.md`（迁移触面）、`notes/implemented/simplification/2026-08-31-sqlite-only-storage.md`、`agent-team-workbench/migrations/`（到 `0066`）| `migrations/` 唯一真相源，禁第二套 DDL 与方言分支 |
| 查仓库流程 / 提交与验证纪律 | 根 `AGENTS.md`、`notes/implemented/process/2026-09-13-pre-merge-ego-verification.md` | 一任务一分支、分刀提交、门禁按风险选 |
| 查历史安全结论 | `notes/implemented/bug-fix/2026-08-29-mimosa-gate-triage.md` | 当前处置状态以此为准；`docs/archive/security/security-findings-inventory-2026-08-29.md` 只是快照 |

## 六、权威边界

### 可作事实源（结论依据）

| 载体 | 自述依据 |
|---|---|
| `agent-team-workbench/contracts/web/openapi.yaml`、`agent-team-workbench/contracts/events/asyncapi.yaml`、`agent-team-workbench/contracts/runner/v2/schema.json`、`agent-team-workbench/contracts/control/plan-decision-v2.schema.json` | 由 `agent-team-workbench/internal/httpapi/contract_guard_test.go` 门禁 A/B 双向强制；`plan-decision-v2` 自声明「通过本契约只代表 wire shape 合法」，语义仍由 Go 校验后经 `SubmitPlan` |
| `docs/architecture/system-architecture-handbook.md` | 自述「只描述当前源码已经实现的事实，不提前把目标设计写成现状」，并给出裁决序「源码>测试>本文>note」 |
| `agent-team-workbench/web/DESIGN.md` | 自述「This document describes the contract; the effective values are the CSS variables and Tailwind mappings」 |
| `agent-team-workbench/web/src/index.css`、`agent-team-workbench/web/tailwind.config.js` | DESIGN.md 指定的数值层 |
| `docs/frontend/chat-content-blocks-v1.md` | `languagegui/v1` wire 层事实源；流式与 canonical 同解析器 |
| `notes/implemented/architecture/2026-09-12-knowledge-library-rebuild.md`、`agent-team-workbench/internal/knowledgelib/record.go:6-7`、`agent-team-workbench/migrations/0057–0065` | 前者「把 Markdown 提升为知识正文的真相源」；后者「Markdown under the library root is the writing truth for knowledge. The SQLite rows are a projection that can be rebuilt」 |
| `docs/product/end-goal.md` | 「本文是全部演进的收口愿景，不描述当前完成度；后续设计/实现与本文冲突时，先修订本文再动代码」 |
| `docs/product/workspace-ai-workbench/README.md` | 「本文件是当前 U07 阶段的唯一执行总入口」「以本总清单为准」；规则 7「总清单与单元不一致按未完成处理」 |
| `docs/product/product-agent-charter.md` | 该产品 agent 的 system prompt 来源；`agent-team-workbench/knowledge/README.md` 声明条目规范「此处不重复定义」 |
| 根 `AGENTS.md`、`integrations/web-idea/AGENTS.md` | 工程事实与操作纪律；运行时硬约束的防回归共识 |
| `docs/review/assets/**/*.json` | 限「该次真实运行 / 该固定 SHA」时点的证据；`docs/testing/web-idea-java/README.md` 反向声明「不要将范本自检绿灯换算为产品完成率」 |

### 只能作参考（不可作现行结论）

| 载体 | 自述依据 |
|---|---|
| `docs/references/`（9 份）| `docs/README.md:47`「不是本项目当前实现的事实源」 |
| `docs/archive/`（24 份）| `docs/README.md:57`「归档只用于追溯，不作为现行产品、架构或安全结论」；各评审/安全文另有「只描述当日状态」声明 |
| `docs/testing/`（10 份）| `docs/testing/web-idea-java/README.md` 自述「工作台端到端、真实模型分析和 Java 功能移植均未运行或未实施」、「不要将范本自检绿灯换算为产品完成率」；`scenario-contract.md` 自述「不是 IntelliJ Java 功能实现清单，也不是现有 Agent Team Workbench 已支持能力的声明」且状态 `proposed` |
| `notes/proposed/`、`notes/rejected/`、`notes/product/`、`notes/archived/`（11 篇）| 本次盘点范围内无一条自声明为现行事实源；事实源在代码、`contracts/` 与 `docs/` |
| `docs/architecture/c4-container-diagram.md` | 参考（非事实源，未覆盖治理层）；`docs/README.md` 仍称其为「当前容器边界」与此冲突（第七节 #8）|
| `docs/product/product-agent-canvas-research.md` | 自述「本报告记录选型阶段」 |
| `docs/product/knowledge-experience-acceptance.md` | 自述「已被 2026-09-08 产品决策废止，不代表当前知识库 Tab 的交互」 |
| `docs/product/task-intake-project-binding.md` + 验收、`docs/product/requirement-intake-workflow.md` | 均声明「功能开发已暂停…历史验收不代表新要求已完成」；前者未合 main |
| `docs/architecture/2026-09-01-loopx-task-foundation-architecture-assessment.md` | 自述「继续作为决策前的研究证据，不承担当前实现状态的事实源」 |
| `docs/architecture/knowledge-librarian-design.md` | 「本文件规定应实现的行为，不是完成声明」；且其描述的表已 DROP（第七节 #27、#33、#34）|
| `docs/protocol/codex-app-server-v2.md` 之外的协议文 | `docs/protocol/knowledge-librarian.md` 自声明「机器可执行字段契约以 `agent-team-workbench/contracts/web/openapi.yaml` 为准」，即该文不是字段事实源 |
| `notes/implemented/` 中已自标降级的 2 篇 | `notes/implemented/architecture/2026-08-26-p0-hardening-and-contract-guards.md:5`、`notes/implemented/bug-fix/2026-08-30-session-meta-model-validation-fixes.md:5` 均声明被 SQLite 单一存储后端取代 |

## 七、缺口与冲突

37 条原裁决经复核后按三类重组：**真冲突**＝双方均为活跃且自述为现行来源、断言互斥（含对另一现行来源的错误引用）；**新旧并存**＝一方自述被废止/取代，或同主题共存不矛盾；**误报**＝所述冲突不成立（多为缺口、单侧事实或错误归因）。误报条目已整体删除、不再列出；为保持正文交叉引用有效，编号沿用原编号，因此不连续。#27/#33、#31/#36 是同一冲突的两次独立记录，合并成行。

### 7.1 真冲突（15 条）

每条并列双方依据（path:line），不代裁。

| # | 主题 | 依据 A（path:line）| 依据 B（path:line）| 冲突点 |
|---|---|---|---|---|
| 5 | 契约头部状态 | `docs/product/workspace-ai-workbench/U04-contract.md:3`「Status: implementing」、`docs/product/workspace-ai-workbench/U05-contract.md:3`「状态：实施中…不提交、不合并」、`docs/product/workspace-ai-workbench/U06-contract.md:3`「状态：实施中，2026-09-09…不提交、不合并、不推送」 | `docs/product/workspace-ai-workbench/units/U04-analysis-questions.md:3`、`docs/product/workspace-ai-workbench/units/U05-decisions-changes.md:3`、`docs/product/workspace-ai-workbench/units/U06-task-publication.md:3`「状态：已完成；功能完成：`[x]`」| 契约头部状态未回填，同一单元的实施状态两处相反 |
| 7 | 迁移时间线 | `docs/architecture/system-architecture-handbook.md:362` 迁移时间线止于 0042 | `agent-team-workbench/migrations/` 已 66 个、最新 `0066`（0055 task_publication、0056 native_questions、0057–0065 知识层均未进架构文档）| 系统现状事实源滞后于仓库 |
| 8 | 容器图与真相矩阵 | `docs/architecture/c4-container-diagram.md:3`（基线 2026-08-31）的 DB 容器未列 `goals`/`goal_todos`/`quota_reservations`/TurnReceipt 等 0024–0042 治理表 | `docs/architecture/system-architecture-handbook.md` §5 单一真相矩阵；且 `docs/README.md:19` 仍称 c4 为「当前容器边界」| 容器图与现状矩阵矛盾，入口索引却把它列为活跃文档 |
| 13 | 画布存废 | `agent-team-workbench/web/DESIGN.md:277-284` 声明画布与 `?canvas=knowledge`/`?knowledge=` 已退役，且「No canvas, graph, or excerpt-reference surface may be reintroduced under `/chat`」 | `agent-team-workbench/web/src/pages/chat.page.tsx:10,1344` 仍 import 并渲染 `<KnowledgeCanvas>`；`agent-team-workbench/web/src/components/chat/transcript-view.tsx:8` 仍用 `parseCanvasMessage`；`agent-team-workbench/web/src/components/knowledge-canvas/`（5 文件）与 `agent-team-workbench/web/src/pages/chat-knowledge-canvas.render.test.tsx` 均在树上 | 规范称已退役、代码未删 |
| 14 | 失效的小节引用 | `docs/frontend/chat-rendering-spec.md:289` 引「DESIGN.md Known Gaps 第 7 条」；`agent-team-workbench/web/src/design-tokens.test.ts:2` 引「DESIGN.md「Don'ts」第 1 条」 | `agent-team-workbench/web/DESIGN.md` v2.0.0 仅剩 `:313` 的 §Forbidden presentation patterns，已无 Known Gaps/Don'ts | 外部引用指向不存在的小节 |
| 15 | 路径漂移 | `docs/frontend/chat-rendering-spec.md:13` 把配套文档写作同级 `frontend-design-md-redesign.md` | 该文件实际在 `docs/archive/design/frontend-design-md-redesign.md` | 归档迁址后引用未回写 |
| 16 | 前端规范缺口 | `agent-team-workbench/web/DESIGN.md` frontmatter 未列 `--color-text-muted` | `agent-team-workbench/web/src/index.css:27,82` 存在该变量；另 `docs/frontend/chat-rendering-spec.md` 第 9 节声明的 §1、§2（Source 引用部件、artifact 内容预览）仍无对应实现端点 | 契约清单与实际 token 集合、声明路线与实现不一致 |
| 20 | 悬空验收证据 | `docs/product/task-intake-project-binding-acceptance.md:59-65` 引用 `../review/assets/task-intake-project-binding/{bound-alpha-1440,web-idea-dark-1024,worktree-640,new-head-invalidates-draft}.png` | `docs/review/assets/task-intake-project-binding/` 不存在（`git log --all` 无记录）；同时 `docs/review/assets/u07-web-idea-acceptance/u07-cleanup-review.md`、`u07-frontend.md` 声明旧 `internal/taskintake/`、`task-intake.*` 已删除 | 该验收结论既无证据也无对应实现 |
| 22 | IA 事实源悬空 | `docs/archive/design/frontend-design-md-redesign.md:39`、`notes/implemented/feature/2026-08-24-frontend-design-md-and-ui-foundation.md:13` 仍引 `references/confirmed-ia.json`（该路径不存在）| `docs/archive/prototype/confirmed-ia.json` 是唯一存在的 IA 冻结件 | 「IA 已冻结」的依据指向空路径 |
| 27、33 | 知识管理员设计文未同步重建、未标冻结（同一冲突的两次记录）| `docs/architecture/knowledge-librarian-design.md:3`（2026-09-06，基线 `main@12906d6`）标「已实现本机闭环」且无 superseded/冻结标记，通篇描述 item/version 与 `agent-team-workbench/internal/knowledge` 的 FileRetriever 模型（该目录现已不在树上，`find` 全仓无此目录）| `notes/implemented/architecture/2026-09-12-knowledge-library-rebuild.md` 已把 Markdown 定为真相源，`agent-team-workbench/migrations/0065` 已 DROP 其描述的表 | 设计文未同步、未标冻结，且仍是 `docs/README.md` 的活跃文档；#33 强调的是「未标冻结」这一形式缺陷 |
| 28 | 留痕错放生命周期 | `notes/proposed/architecture/2026-09-02-run-journal-lifecycle-logging.md`（「M1–M3 已实施完成…待合并」）、`notes/proposed/architecture/2026-09-03-run-journal-d2-phase-frames.md`（`Status: implemented`）、`notes/proposed/bug-fix/2026-09-03-all-kimi-smoke-problem-points.md`（`Status: implemented`）| `agent-team-workbench/internal/observability/journal.go`、`agent-team-workbench/contracts/web/openapi.yaml:1048`（`/runs/{run_id}/journal`）、`plan_execution_failed`、`CoordinatorRepairErrorSemantic` 均已存在 | 三处应在 `notes/implemented/`，目录与状态自相矛盾 |
| 31、36 | 知识存储真相源倒置（11、12 两个盘点各记一次）| `agent-team-workbench/knowledge/README.md:15`「SQLite 是知识条目…的唯一有效状态入口；Markdown 文件用于显式导入/导出」；`docs/README.md:3`「运行时知识状态由 `agent-team-workbench` 的 SQLite 知识层管理」 | `agent-team-workbench/internal/knowledgelib/record.go:6-7`「Markdown under the library root is the writing truth for knowledge. The SQLite rows are a projection that can be rebuilt」；`notes/implemented/architecture/2026-09-12-knowledge-library-rebuild.md`「Markdown 正文（`content/**`）是写作真相」「不再有『数据库是唯一权威』的保证」| 两个「入口文档」与代码、重建笔记相反；#36 的影响面比 #31 更大 |
| 34 | 旧需求/设计/协议未标冻结却描述已删对象 | `docs/product/knowledge-librarian-requirements.md:3`、`docs/protocol/knowledge-librarian.md:3` 标 `Status: implemented local scope`；`docs/architecture/knowledge-librarian-design.md:3` 标中文「状态：已实现本机闭环」 | `agent-team-workbench/migrations/0065` 已删除 `knowledge_items`/`knowledge_versions`/`knowledge_submissions`/`knowledge_jobs`，`/knowledge` 路由返回 404 | 旧链条整体退役但三份文档无冻结标记（且两种语言写法并存），`docs/README.md` 仍列其为活跃文档 |

### 7.2 新旧并存（9 条）

一方自述被废止/取代，或同主题共存不矛盾；按「旧 → 新」压缩记录。

| # | 主题 | 旧（path:line）| 新（path:line）| 处置 |
|---|---|---|---|---|
| 2 | 知识库方向残留 | `docs/product/knowledge-experience-acceptance.md:5,7-9` 保留「交原始资料/提问题/看结果」操作台并称通过 | `docs/product/knowledge-experience.md:9`「知识库 Tab 只用于看内容，不用于录入或管理」| 旧文自述「已被 2026-09-08 产品决策废止」，靠免责声明兜住，易被误引 |
| 4 | 主线合入状态 | `docs/product/workspace-ai-workbench/README.md:57`「本轮业务改动未合入主线」与基线行 `main@651056c1` | 同文 `:67` 与 `docs/product/workspace-ai-workbench/units/U07-web-idea-acceptance.md:42`「已授权合入本地 main」；main `531c59d` 已含相关迁移与 `chatanalysis` 包 | 基线行过期未清 |
| 9 | 迁移基线 | `docs/architecture/knowledge-librarian-design.md` 部署边界写「升级至 0047」 | `agent-team-workbench/migrations/` 已 66 个（`0066`）| 次要；旧链设计文基线过期 |
| 10 | 协议文过期与孤儿契约 | `docs/protocol/knowledge-librarian.md:7` 自述「字段契约以 openapi 为准」，正文仍描述 23 个 `/knowledge/*`、`/knowledge-agent/*` 端点与 `atw-knowledge` CLI 桥 | `agent-team-workbench/contracts/web/openapi.yaml` 的 `/library/*` 统一资料库；`cmd/atw-knowledge` 与 `agent-team-workbench/internal/application/knowledge_*` 随 `8e8a389` 全删 | 字段源以 openapi 为准；残留缺口＝openapi 仍留 `Knowledge*` schema 与 `bearerAuth: run-scoped-knowledge-token` 孤儿定义，而 `agent-team-workbench/internal/httpapi/contract_guard_test.go` 门禁 A 只对账 paths↔routes，抓不到无 path 的 schema |
| 17 | 外部逆向快照过期 | `docs/references/codex-desktop-rendering-comparison.md` §2「我们的 Web 端现状（2026-08-24 代码快照）」 | 现状已含 tx 皮肤、swarm 正文与 `531c59d` 删除 chat 左栏 chrome | 快照早于现状，其对比表组件名/行号已失效 |
| 21 | 时效未回填 | `docs/testing/web-idea-java/README.md:3,67`（2026-09-09）「真实模型分析……未运行/未实施」；`docs/review/assets/u01-global-workspace/verification.json`「U03/U07 real model pending」、`docs/product/workspace-ai-workbench/U01-acceptance.md:28`「以上单元仍未完成」 | `docs/product/workspace-ai-workbench/U07-acceptance.md:14`、`docs/review/assets/u07-web-idea-acceptance/real-output.md:6` 记录同一范本 `SRC-PPTX-01` 的真实模型闭环通过 | 多处过期陈述未回填，直接读会得出相反的完成度（**本次核对**：`SRC-PPTX-01` 并未出现在 `docs/review/assets/u07-web-idea-acceptance/verification.json`，该范本证据在 `U07-acceptance.md` 与 `real-output.md`）|
| 23 | 迁移目录口径相反 | `docs/archive/reviews/2026-08-27-project-quality-assessment.md:84`（P2 #9）按「双迁移目录（`migrations/` 与 `migrations/sqlite/`）语义等价」提出问题 | 根 `AGENTS.md`「`agent-team-workbench/migrations/` 是唯一 SQLite 迁移真相源，不得新增数据库方言分支或第二套 DDL」 | 评审快照已过期未清，按现行口径该条作废 |
| 25 | 画布笔记事实过期 | `notes/implemented/architecture/2026-09-08-product-agent-canvas.md` 称「复用知识正文、版本与来源」 | `notes/implemented/feature/2026-09-13-product-knowledge-canvas-restore.md` 记录旧 per-item API 随 `92a9b12` 成建制删除、数据层已重接新资料库 | 前者事实过期，未标 superseded |
| 29 | 过期存储口径未清 | `notes/product/2026-08-24-project-review.md:6-7`「PostgreSQL 是生产目标」「Postgres/SQLite 双方言等价冒烟」 | `notes/implemented/simplification/2026-08-31-sqlite-only-storage.md`（根 `AGENTS.md` 亦钉 SQLite-only、禁方言分支）| 仅靠页首一句「已被取代」兜住，正文未清理 |

### 本次盘点未能覆盖的范围

12 个盘点的范围自述如下，未列出的区域本次**没有**任何证据，本文件不对其作任何断言：

- 只读覆盖：`docs/product/`（含 `workspace-ai-workbench/`）、`docs/architecture/`、`docs/protocol/`、`docs/frontend/`、`docs/references/`、`docs/testing/`、`docs/review/`、`docs/archive/`、`notes/{implemented,proposed,rejected,product,archived}/`、`agent-team-workbench/contracts/`（04 列出的 9 个对象）、`agent-team-workbench/knowledge/`、知识域代码与迁移（`agent-team-workbench/internal/knowledgelib/`、`agent-team-workbench/internal/application/knowledge_*`、`agent-team-workbench/internal/httpapi/handlers_knowledge_library.go`、`agent-team-workbench/internal/domain/knowledge_library.go`、`agent-team-workbench/internal/persistence/sqlstore/knowledge_library*`、`agent-team-workbench/migrations/0057–0065`）、前端设计规范与知识前端件、根 `AGENTS.md`、`integrations/web-idea/AGENTS.md`、`docs/README.md`。
- **本次核对**追加覆盖：`agent-team-workbench/notes/`（第二棵笔记树，见 3.5、4.7）。它不属于 12 份素材的盘点范围，其内容全部来自本轮的 `ls`/`grep`/`git log`/`diff` 核对。
- 未覆盖：`agent-team-workbench/internal/` 中与知识无关的包（其行为只通过 `docs/architecture/system-architecture-handbook.md`、`docs/architecture/task-control-surface-context-design.md` 与 `notes/implemented/` 的二手描述间接出现）；`agent-team-workbench/cmd/`（除 `atw-mcp`、`atw-knowledge` 被提及外）；`agent-team-workbench/contracts/` 中 04 未列出的文件；`agent-team-workbench/web/src/` 中不属于设计规范与知识画布的实现代码；`agent-team-workbench/agents/`、`runtimes/`、`models/` 的实际内容（仅覆盖到 README 层面的真相源声明）；`integrations/web-idea/` 源码；`tools/`、`testdata/`、`.github/workflows/`；`.agent-work/`、`.mimosa/`、`.cursor/`、`.commandcode/`、`.sessions/` 等会话与工具态目录；根目录的 `*.html` 架构图与可视化核对产物。
- 方法边界：所有断言均为只读盘点所得，未运行构建、测试或迁移；`docs/review/assets/` 中的验收证据只核对了其被引用的路径与自述结论，未复跑。

## 八、维护约定

### 新增知识应落哪个目录

| 知识类型 | 落点 | 命名与要求 |
|---|---|---|
| 产品需求 | `docs/product/<topic>.md` | 与验收成对：`docs/product/<topic>-acceptance.md`；验收资产落 `docs/review/assets/<topic>/`，两处互相相对链接 |
| 架构现状（已实现事实）| `docs/architecture/` | 只写已实现的现状与源码导航；参考 `docs/architecture/system-architecture-handbook.md` 的裁决序声明 |
| 协议 | 人类可读说明 → `docs/protocol/`；字段契约 → `agent-team-workbench/contracts/` | 二者必须同步改，否则产生第七节 #10 那类漂移；字段级真相只在 `agent-team-workbench/contracts/`，接入 `agent-team-workbench/internal/httpapi/contract_guard_test.go` 门禁 |
| 前端视觉与交互 | 规范 → `agent-team-workbench/web/DESIGN.md`；数值 → `agent-team-workbench/web/src/index.css` + `agent-team-workbench/web/tailwind.config.js` | 新 token 必须同时进 DESIGN.md 清单与 `agent-team-workbench/web/src/design-tokens.test.ts` 门禁视野；历史方案落 `docs/archive/design/` |
| 外部系统资料 | `docs/references/` | 必须钉外部系统版本/CLI 版本/commit 或 SHA-256，并写明升级重核规程；原文证据落 `docs/archive/prompt-library/`，原文不改写 |
| 决策留痕 | 根 `notes/{lifecycle}/{class}/yyyy-mm-dd-slug.md`（不含 `agent-team-workbench/notes/` 这棵后成的第二棵树，见 3.5、4.7）| 依根 `AGENTS.md` 规则 5，格式见 `dsh-dev-workflow` skill 的 `references/agent-notes.md`；重要取舍、否决与负向保证才记，代码与文档已表达的不重复记 |
| 运行时知识/语料 | `agent-team-workbench/knowledge/`（Markdown 正文真相源）+ SQLite 可重建投影 | 条目规范以 `docs/product/product-agent-charter.md` §3 为准；不在此目录重复定义 |
| 存储结构变更 | `agent-team-workbench/migrations/`（唯一 SQLite 迁移真相源）| 不得新增方言分支或第二套 DDL |

### 归档触发条件

依 `docs/README.md:49-57` 的口径（「归档材料由原位置迁入上述目录，原路径不再保留。归档只用于追溯，不作为现行产品、架构或安全结论」）与本次盘点观察到的实际做法：

- **迁入**：材料被新方案取代（旧设计、旧交互方向）、时点评审快照、对外部系统原文的提取证据、历史安全扫描快照、早期原型与线框。落点分别为 `docs/archive/{design,reviews,prompt-library,security,prototype}/`。
- **迁入时必做**：① 同步全部反向引用（否则产生第七节 #22「IA 事实源悬空」那类断链）；② 文件首部写明冻结时点与「仅用于追溯」声明；③ 若其结论仍有现行权威（如提示词原文之于 `docs/references/cli-prompt-engineering.md`），在承接文档中指明「原文有效、结论无效」。
- **不适用迁入**：仍被 `docs/README.md` 列为活跃文档、或描述对象已被删除但尚未重写替代的文档（如第七节 #34 涉及的三份知识域旧文），正确动作是先重写或改写为冻结，而不是静默留下。

### 生命周期与清单维护

- `notes/implemented/` 与代码同寿：代码改动时同 commit 更新对应留痕；被取代的条目应在文首标 superseded 并指向替代者（当前有 2 处正确示例、多处缺席，见第七节 #25、#27）。
- `notes/proposed/` 中的条目在实现后应迁入 `notes/implemented/`（当前有 3 篇错放，见第七节 #28）。
- `notes/rejected/` 保留否决理由，不因时间推移删除（当前 1 篇）。
- 本文件在盘点范围变化时整体重生成；提交时只改本文件与 `docs/README.md` 中的那一行索引，不顺手改动其引用的其它文档——被引用的文档修正是独立任务。
