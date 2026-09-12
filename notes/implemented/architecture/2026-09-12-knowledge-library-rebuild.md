# 资料库（知识管理员）重建：设计与接口契约

日期：2026-09-12
状态：已实施
范围：以统一资料库设计替换旧「知识库管理员」（item/version/submission/job 模型）。

## 1. 为什么重建

旧管理员把知识存成 SQLite 里的 item/version 行，Markdown 只是可选的展示产物：
`knowledge_items` + `knowledge_versions` + `knowledge_submissions` + `knowledge_jobs`
四层状态由一条多轮 JSON decision 循环推进。它无法表达设计已确认的四件事：

1. 一个工作空间里的**多仓库 + common** 是**一个**资料库，跨服务关系是库内关系而不是跨库拼接。
2. Markdown 主题文档内嵌**稳定 ID 的 Assertion 块**（`id/about/perspective/basis/statement/scope/evidence`），
   且一条知识只有一个主编写位置。
3. **程序采集**证据（来源快照绑定、内容表示、locator、摘要），模型不能生成 hash 或自行宣称批准。
4. 查询固定读一个**已发布 release**，区分条件/依据/覆盖缺口/未知/新鲜度。

因此本次把 Markdown 提升为知识正文的真相源，SQLite 只保留来源账本、证据、发布清单、
队列与**可重建**索引。

## 2. 关键取舍

| 决定 | 理由 | 放弃了什么 |
|---|---|---|
| Markdown（`content/**`）是知识正文真相源；SQLite 行是可重建投影 | 设计 5.9.3/5.9.4 要求 Markdown 可读、可移植、索引可重建；A8 要求索引能从正式内容与账本恢复 | 不再有「数据库是唯一权威」的保证；每次发布必须同时写文件与行 |
| 库根默认 `<workspace_root>/.agent-work/knowledge`，可按 workspace 配置 | 目录在 Run 授权的 workspace root 内（sandbox 允许读写），同时不污染业务仓库工作树 | 用户不能直接在仓库根看到 `knowledge/`；由页面显式展示绝对路径 |
| 资料 agent 用 **staging 目录文件契约**，不用自定义 HTTP 工具 | codex/kimi CLI 原生就有文件工具；无需为每个 adapter 增加动态工具；产出天然是设计要求的 Markdown | 失去「每次工具调用都过 harness」的细粒度闸门；改由「只采信 staging 产物 + 程序校验」把关 |
| 证据 ID 由收集器从 `(binding, path, locator, content_digest)` 确定性派生 | 满足「资料 harness 分配证据身份」「定位变化即新证据」，同时让重复采集幂等 | 模型在 staging 里写的 evidence key 只是本地别名，发布后文档里的 ID 可能被改写 |
| 单写入由**数据库部分唯一索引**保证（`status='running'` 每库至多一行） | 并发写入是必须防住的回归，不能只靠应用约定 | 队列领取失败要走重试，而不是排队等待 |
| `retry_wait` / `awaiting_agent` / `blocked` 仍占队头 | 设计 5.14.1「重试继续占用同一队列位置」 | 队头阻塞时后续任务确实等待 |
| dirty/untracked 文件在**快照捕获时**固化字节到 `_system/snapshots/` | 开发视图必须可复现，不能用「读的时候再读一次工作树」 | 库会为脏文件占额外磁盘 |
| `consult_knowledge` Plan 动词**改指向新资料库**而不是删除 | 它不是资料管理员，是任务协调器的既有读取能力；删它会削弱无关业务 | 需要保留一个适配层 |
| 旧表 `RENAME` 为 `legacy_knowledge_*` 而不是 DROP | 用户明确要求旧资料可恢复；brief 允许保留历史迁移资料 | 库里会长期留着 13 张只读旧表 |

## 3. 目录骨架

```text
<library_root>/
  INDEX.md                     总入口（发布时生成）
  catalog/                     目录视图（by-domain / by-component / glossary）
  content/                     正式知识正文（唯一主编写位置）
    business/{rules,flows}/
    components/
    contracts/{apis,events,data}/
    operations/
    decisions/
  _sources/manifest.yaml       来源登记
  _system/
    snapshots/<snapshot_id>/<source>/   固化的 dirty/untracked 字节
    representations/<algo>/<hex>.bin   冻结的内容表示
    releases/<release_id>/manifest.json 发布清单
    staging/<task_id>/          资料 agent 的工作目录
    legacy-import/              旧管理员数据导出
```

## 4. 写入流程（FIFO）

```text
外部 POST /library/events         → 持久化受理，返回轻量回执（业务不等）
受理事务                          → 生成 knowledge_write_tasks(seq=下一个序号, status=queued)
worker tick                       → 领取 min(seq) 非终态任务，CAS 置 running（DB 唯一索引兜底）
                                  → 固定输入快照（git commit / dirty 字节 / 制品绑定）
                                  → 写 staging brief，派 Run 给内置资料 agent
Run 终态                          → 读 staging：plan.json + evidence.yaml + content/**/*.md
                                  → 解析 Markdown（未知字段/重复 ID/伪造 hash/伪造批准一律拒绝）
                                  → 程序采集证据（冻结表示 + 计算摘要 + 抽取 locator）
                                  → 校验引用、实体、关系端点
                                  → 不通过：bounded repair turn（最多 2 轮）后阻塞
                                  → 通过：发布事务（release 行 + 文档版本 + Assertion/Relation + 索引 + 文件）
后续任务                          → 队头终态后才可领取
```

发布中断恢复：`knowledge_publications` 先写 `prepared`（含 projection_digest），
同一事务内切换 `knowledge_releases` 与 `knowledge_libraries.current_release_id`，
再置 `committed`。恢复循环读到 `prepared` 时先核实 release 行与文件清单是否一致，
不猜测成功、也不重复发布。

## 5. HTTP 接口

前缀 `/api/v1/workspaces/{workspace_id}/library`。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `` | 资料库总览：root、当前 release、计数、索引修订、队列深度 |
| GET | `/sources` | 已登记来源 |
| POST | `/sources` | 登记来源（name/kind/repo_path/artifact/consumer/globs） |
| PATCH | `/sources/{source_id}` | 修改来源（版本 CAS） |
| DELETE | `/sources/{source_id}` | 停用来源（保留来源账本） |
| POST | `/events` | 异步事件受理（`client_key` 幂等），返回回执 |
| GET | `/events` | 事件列表（状态、关联任务） |
| POST | `/initialize` | 便捷入口：提交一次 `initialize` 事件 |
| GET | `/tasks` | 写队列（序号、状态、尝试、阻塞原因） |
| GET | `/tasks/{task_id}` | 任务详情：轮次、覆盖、诊断、staging |
| POST | `/tasks/{task_id}/retry` | 运维重试阻塞任务 |
| POST | `/tasks/{task_id}/cancel` | 显式取消未发布任务 |
| GET | `/releases` | 发布列表 |
| GET | `/releases/{release_id}` | 发布详情 + 覆盖 + 文档清单 |
| GET | `/documents` | 按 release 浏览文档（`release_id` 可选，默认当前） |
| GET | `/documents/{document_id}` | 文档版本、Assertion、Relation |
| GET | `/evidence/{evidence_id}` | 证据：来源绑定、locator、摘录、摘要、可取性 |
| GET | `/graph` | 实体 + 关系图（供浏览与桥接） |
| GET | `/bridges` | 派生桥接视图（来源 + 依赖版本 + 缺口） |
| POST | `/query` | 固定 release 查询（条件/依据/覆盖/未知/新鲜度/截断） |
| GET | `/expand` | 按句柄展开证据或条目 |
| GET | `/status` | 观察面：队头、阻塞原因、最后错误、新鲜度 |
| POST | `/reindex` | 从正式 Markdown + 账本重建索引（A8 证据） |

资料 agent 不使用 HTTP 工具：harness 把 `brief.md` + `task.json` 写进 staging 目录，
Run 的指令给出 staging 与各来源仓库的绝对路径，agent 用 CLI 原生文件工具读写。

## 6. 与旧实现的边界

删除：旧 item/version/submission/job 的全部活动代码、HTTP 路由、CLI（`cmd/atw-knowledge`）、
Codex 动态工具 `atw_knowledge`、Web 知识画布与聊天交接。
保留：`consult_knowledge` Plan 动词（改指向新库）、`chat_analysis` 的 knowledge 来源种类
（改指向新库文档/release）。
数据：`migrations/0057_knowledge_library.sql` **不改名、不删除**任何旧表——旧表保持原名继续存在，
行数据逐字保留；新库使用独立命名空间（`knowledge_libraries`/`knowledge_write_tasks`/…）。
退役由代码级测试证明：`internal/persistence/sqlstore/knowledge_library_retirement_test.go`
同时断言「Go 源码对旧表零引用」与「旧表仍在」，而不是靠 RENAME/DROP。
（早期设计曾计划改名为 `legacy_knowledge_*`；SQLite 的 `ALTER TABLE RENAME` 会重新解析全库触发器，
  触发 `record_kind_migration_test.go` 失败，故否决该方案。）

## 7. 第三轮修正（2026-09-12 续）

| 主题 | 决定 |
|---|---|
| 需求正文入口 | `requirement.imported` / `document.revised` 的 `content_ref` 与 `payload` 一律在队头重新读取事件行，写成 staging 的 `requirement.md` 并在 `brief.md` 内联；`content_ref` 指向的文档优先于 payload 摘要。取不到正文时 brief 明说「原文没有取到」，要求写进 `coverage.gaps`，不得凭标题猜测 |
| 需求 vs 现状 | 需求正文是 `basis: source_statement`、`perspective: normative` 的**声明输入**；brief 明文禁止写「已实现/已上线」，禁止写 `approved`/`review_state`（写了整篇被拒） |
| 登记 ref 语义 | 来源登记了 `DefaultRef` 就按该 ref 解析提交；解析失败即拒绝本轮（保存来源时先做一次可验证性检查），绝不静默退回工作树 HEAD。当登记 ref 与工作树检出提交不同，本轮不叠加未提交改动，并在来源表里写明原因 |
| 视图 | 一个资料库只有一个活动视图（`baseline`）。事件声明其它 `view_id` 直接拒绝并说明「分支视图需要独立历史链，本版本未实现」；`branch.switched` 视为同一视图内的新版本增量读取 |
| 索引重建 | `POST /library/reindex` 只入队并返回 `202` + 队列回执（`task_id`/`queue_seq`/`status`/`enqueued_at`），由 worker 在队头执行；崩溃中断的重建任务下次 tick 直接重跑（派生数据幂等），不占用模型轮次、不产生新 release |
| 历史可读性 | 文档版本行是路径/标题的权威：按 release 读取永远返回该版本固定的 path/title。版本同时保存发布时的 staged-key → 规范 evidence ID 映射（`0058_knowledge_evidence_aliases.sql`），历史引用用自己那版的映射解析 |
| 客户端形状 | 领域响应结构（`KnowledgeQueryHit`/`KnowledgeCoverage`/`KnowledgeFreshness`/`KnowledgeExpandHandle`）补齐 json tag：展开句柄此前序列化成 `undefined:undefined` |
| 证据读取边界 | 固定版本后的证据读取优先读冻结副本（`<snapshot>/tree/...`），来源仓库被移动或不可读时既定证据仍然可取；活仓库只作为副本已被释放时的兜底 |
