# 知识管理员 Agent：部署与调用协议

Status: implemented local scope; see acceptance record

Date: 2026-09-06

机器可执行字段契约以 [contracts/web/openapi.yaml](../../agent-team-workbench/contracts/web/openapi.yaml) 为准；本文件说明身份、部署、调用顺序和错误语义。

## 1. 定位

知识管理员是 workspace 内的专职调查和整理 Agent；发布由显式管理命令完成。需要关系展开和原文核验的查询入口是知识管理员 Harness；既有 Plan 的 `consult_knowledge` 仍提供受控预取。普通 Agent 不直接改共享有效知识。

实现基线已经固定：

- workbench SQLite 保存知识条目、版本、来源、关系、提交回执和调查作业；
- Markdown 是知识版本的正文表达和导入/导出格式；
- 发布构建可重建的检索投影，读端只看已发布版本；
- 知识作业复用既有 WorkItem → ExecutionRun → Runner 控制面，不创建第二套 provider、计费、取消或状态机。

知识层只纳入当前授权范围内的已入库知识和提交证据。代码、测试或其他外部源码读取默认关闭；来源策略未显式授权时，管理员必须把该部分视为缺口，不能声称已覆盖。

检索 `terms` 采用任一词匹配的候选召回语义（OR）；Workspace、可见性、scope 和状态限制仍共同生效（AND）。多主体检索中某个词未命中，不会抹掉其他主体的候选；结果上限及截断标识仍约束调查完整性。

实现状态（2026-09-06）：知识 SQLite/版本发布、Run-bound Harness、候选收件箱和查询关系闭环已有自动化及真实模型/浏览器验证，见[验收记录](../review/knowledge-librarian-acceptance.md)。远程 Worker 的工具注册与凭据传输未实现。功能验收通过不等于知识内容对任意问题已经完备。

## 2. 身份与范围

### 2.1 人和管理界面

当前本地 HTTP server 使用配置的 `demoRole` 执行路由权限守卫；OpenAPI 保留 session cookie 形状，但真实登录会话身份尚未接入。现有权限点语义为：

- 普通读取需要 PermRead；
- 配置、候选提交、整理和发布需要 PermAgentWrite；
- 创建调查和取消作业需要 PermRunControl。

Workspace 路径是第一层隔离。默认读取是共享视图。已经通过 PermAgentWrite 的管理身份可以通过 query 的 agent_id 选择某个 Agent 的私有视图及维护范围；这个参数不能赋予管理权限，也不能改变数据中的 owner。人可在该范围内显式发布或废止知识，审计仍记录人为管理操作；Run-bound Agent 接口没有发布/废止入口。

### 2.2 Run-bound Agent

Run-bound 请求使用 Bearer token。token 由控制面在创建活动 Run 时生成，绑定 Run、workspace、Agent 和 work item；调用方不能用请求体或 query 改写这些身份。

- 缺少 token、token 无效或来源 Run 已终态：401；
- 条目、版本、作业或提交不属于该 Run 的可见范围：404；
- 终态 Run 不再拥有知识访问能力；
- Run-bound 提交和调查仍使用当前 Run 的 agent/workspace/work item 作为服务端真相。

## 3. Workspace 管理面端点

前缀为 /api/v1。所有 JSON 字段使用 snake_case。配置、候选提交、整理、发布和人发起的调查需要 Idempotency-Key；取消接口自然幂等，header 可选；Run-bound 提交/调查至少要有 body 或 header 中的 client_key。响应错误使用 application/problem+json。

| 方法和路径 | 请求字段 / query | 成功响应 |
|---|---|---|
| GET /workspaces/{workspace_id}/knowledge/config | 无 | KnowledgeLibrarianConfig |
| PATCH /workspaces/{workspace_id}/knowledge/config | body: librarian_agent_id、enabled、auto_collect、expected_version | KnowledgeLibrarianConfig |
| GET /workspaces/{workspace_id}/knowledge/items | query: q、status、scope、kind、visibility、agent_id、owner_agent_id、limit、cursor | KnowledgeItemList |
| GET /workspaces/{workspace_id}/knowledge/items/{item_id} | query: agent_id | KnowledgeItemBundle |
| GET /workspaces/{workspace_id}/knowledge/items/{item_id}/versions | query: agent_id | KnowledgeVersionList |
| GET /workspaces/{workspace_id}/knowledge/items/{item_id}/versions/{version} | query: agent_id；version 是数字或 kbv_ ID | KnowledgeItemBundle |
| GET /workspaces/{workspace_id}/knowledge/items/{item_id}/relations | query: direction=both/out/in、agent_id | KnowledgeRelationList |
| POST /workspaces/{workspace_id}/knowledge/items/{item_id}/repeal | body: expected_version、reason；query 可带 agent_id | KnowledgeItem |
| POST /workspaces/{workspace_id}/knowledge/submissions | body: KnowledgeSubmitCandidateRequest | 201 + KnowledgeSubmission |
| GET /workspaces/{workspace_id}/knowledge/submissions | query: status、agent_id | KnowledgeSubmissionList |
| GET /workspaces/{workspace_id}/knowledge/submissions/{submission_id} | query: agent_id | KnowledgeSubmission |
| POST /workspaces/{workspace_id}/knowledge/submissions/{submission_id}/curate | body 为空对象或省略 | 201 + KnowledgeJob |
| POST /workspaces/{workspace_id}/knowledge/submissions/{submission_id}/publish | body 为空对象或省略 | KnowledgeSubmission |
| POST /workspaces/{workspace_id}/knowledge/inquiries | body: KnowledgeInquiryRequest；query 可带 agent_id | 201 + KnowledgeJob |
| GET /workspaces/{workspace_id}/knowledge/jobs | query: status、agent_id | KnowledgeJobList |
| GET /workspaces/{workspace_id}/knowledge/jobs/{job_id} | query: agent_id | KnowledgeJob |
| POST /workspaces/{workspace_id}/knowledge/jobs/{job_id}/cancel | body 为空对象或省略；query 可带 agent_id | KnowledgeJob |

未配置管理员时，读取 config 返回 disabled、空 librarian_agent_id 和 version=0 的安全投影；这不阻止共享知识读取。调查和整理在管理员未启用或 Agent 不可用时返回 422 capability_missing。

items 的 `q` 为空时保持浏览；有值时在同一 workspace、权限、status、scope、kind、visibility 和可选 `owner_agent_id` 过滤下查询已发布 SQLite 投影，正文和标题都可命中，中文使用现有 FTS5 加子串 fallback。`owner_agent_id` 只筛选条目的归属 Agent，不改变 `agent_id` requester 的可见权限；viewer 即使指定 owner 也只能看到 workspace 公开条目，不能借此读取私有条目。合法但不存在或属于其他 workspace 的 owner 返回安全空集，不暴露 Agent 是否存在。查询结果按条目 id 降序使用同一个 keyset 游标分页，不把全库加载到内存；命中页的条目可带 `search_excerpt` 摘要。q 只产生读取投影，不改变知识事实。items 的 scope query 只能是 key=value 或 JSON object；limit 为 1..200。status 默认 effective，非 effective 条目需要管理权限。items 使用按 id 的 keyset 分页并返回 next_cursor；next_cursor 为 null 才表示当前可见条目已经返回完。submissions 当前最多返回 200 条，jobs 当前最多返回 100 条；两者返回 truncated=true 时只表示服务端达到展示上限，不能当作完整列表。

## 4. Run-bound 端点

这些路径不使用 session cookie，使用当前 Run 的 Bearer token：

| 方法和路径 | 请求字段 | 成功响应 |
|---|---|---|
| GET /knowledge-agent/runs/{run_id}/items/{item_id} | 无 | KnowledgeItemBundle |
| GET /knowledge-agent/runs/{run_id}/items/{item_id}/versions/{version} | 无；version 是数字或 kbv_ ID | KnowledgeItemBundle |
| POST /knowledge-agent/runs/{run_id}/submissions | body: KnowledgeAgentSubmitCandidateRequest | 201 + KnowledgeSubmission |
| POST /knowledge-agent/runs/{run_id}/inquiries | body: KnowledgeInquiryRequest | 201 + KnowledgeJob |
| GET /knowledge-agent/runs/{run_id}/jobs/{job_id} | 无 | KnowledgeJob |
| POST /knowledge-agent/runs/{run_id}/jobs/{job_id}/cancel | body 为空对象或省略 | KnowledgeJob |

## 5. 知识读取对象

### 5.1 条目、版本、来源和关系

条目使用稳定 id，版本使用独立的 kbv_ ID 和数字 version：

~~~text
KnowledgeItem:
id, workspace_id, owner_agent_id?, visibility, kind, title, summary?,
tags?, aliases?, scope?, current_version_id?, current_version, status,
version, created_at, updated_at

KnowledgeVersion:
id, item_id, version, base_version, status, kind, title, summary?,
body_markdown, tags?, aliases?, scope?, metadata?, content_digest,
created_by_agent_id, created_by_run_id?, created_by_work_item_id?,
supersedes_version_id?, published_at?, created_at

KnowledgeSource:
id, workspace_id, submitted_by_agent_id, kind, ref, locator?, excerpt?,
digest?, metadata?, created_at

KnowledgeRelation:
id, workspace_id, source_version_id, from_item_id, to_item_id, kind,
condition?, rationale?, source_ids?, created_at
~~~

知识状态是 candidate、draft、effective、superseded、repealed。正文和证据版本不可变；修订产生新版本。private 条目必须有 owner_agent_id，workspace 条目按 project/scope 和调用权限筛选。

GET item、GET item/version 返回：

~~~json
{
  "item": {},
  "version": {},
  "sources": []
}
~~~

列表响应统一包在 items 中；空集返回空数组，不返回 null。items 列表另返回 `next_cursor`：只有 null（或兼容实现返回空游标）才表示当前可见结果没有后续页；submissions/jobs 的 `truncated=true` 表示达到服务端上限，不代表完整列表。

### 5.2 来源核验边界

来源对象的 `metadata.verification` 由控制面按来源类型写入，不能由原始 Agent 提交的 `verified=true` 字段授予。SourceInput 尚无持久 ID 时只能作为待核验候选。

| 来源类型 | 受控核验标记 | UI/调用方可表达的含义 |
|---|---|---|
| run | `run_output_verified` 且 `digest_verified=true` | 来源 Run 已终态，已核验其固定输出和 digest |
| artifact | `artifact_manifest_verified` 且 `content_read=false` | 仅核验 artifact manifest/digest，不能说正文已核验 |
| work_item | `work_item_reference_verified` | 已核验任务引用，不代表任务验收通过 |
| document / user | `submitted_excerpt` | 已登记提交摘录，不代表系统重新读取来源 |
| code / test | `submitted_excerpt_not_repository_verified` | 摘录未经过当前仓库核验 |
| agent | `agent_identity_only` | 仅核验 Agent 身份 |

### 5.3 配置

~~~text
KnowledgeLibrarianConfig:
workspace_id, librarian_agent_id, enabled, auto_collect, version,
created_at, updated_at
~~~

PATCH body：

~~~json
{
  "librarian_agent_id": "agent_pm",
  "enabled": true,
  "auto_collect": true,
  "expected_version": 2
}
~~~

三个可变字段都可以省略，但 expected_version 必须存在。服务端先比较当前 version，再校验 Agent 属于当前 workspace、不是系统 Agent 且可运行；失败不产生部分配置。

## 6. 候选提交、整理和发布

### 6.1 提交候选

管理面 body 必须明确 source Agent：

~~~text
KnowledgeSubmitCandidateRequest:
agent_id, run_id?, work_item_id?, client_key,
changes?: KnowledgeChange[], no_change?

KnowledgeChange:
item_id?, base_version, owner_agent_id?, visibility?, title, body, kind,
scope?, summary?, tags?, aliases?, sources?, relations?

KnowledgeSourceInput:
kind, ref, locator?, excerpt?, digest?, metadata?, role?

KnowledgeRelationInput:
to_item_id, kind, from_item_id?, condition?, rationale?, source_ids?
~~~

workspace_id 由路径决定，不能由 body 伪造。changes 非空和 no_change=true 互斥；没有 durable delta 时可以提交 no_change，不应制造重复笔记。

Run-bound body 只要求 client_key；workspace_id、agent_id、run_id 和 work_item_id 即使出现也由活动 Run 覆盖。没有来源证据的主张只能留在 candidate/draft。

### 6.2 整理和发布

POST curate 不直接发布知识，返回 curation KnowledgeJob。管理员读取原文、核对证据、归并现有条目、检查版本和关系后，作业会生成新的候选提交。

POST publish 只接受通过整理和发布门的提交，返回更新后的 KnowledgeSubmission。发布时有效版本、证据关系和检索可见性必须一起更新；旧版本引用仍可读取。

提交状态语义：`received` 表示已收件，`processing` 表示正在整理，`needs_review` 表示整理结果待复核（只有 `result_version_ids` 非空且与 `result_item_ids` 数量一致时才具备发布条件），`accepted` 表示已经发布，`merged` 表示已合并或确认无变更，`rejected` 表示已拒绝。`accepted` 和 `merged` 都是只读终态，不能再次触发发布。

发布失败的典型语义：

- 409 version_conflict：base/current version 已变化；
- 409 idempotency_conflict：同一 Idempotency-Key 的请求体不同；
- 409 idempotency_in_progress：相同写请求仍在处理，可原 key 重试；
- 409 state_conflict：提交或作业状态不允许当前动作；
- 422 validation_failed：字段、来源、Agent 或关系不合法；
- 422 capability_missing：管理员未启用、未配置或运行底座不可用。

## 7. 调查作业

### 7.1 创建

KnowledgeInquiryRequest：

~~~text
question: string, required
context?: string
scope?: object
budget?: KnowledgeJobBudget
client_key?: string
~~~

KnowledgeJobBudget 包含：

~~~text
max_results, max_depth, max_nodes, max_bytes, max_searches, max_relations,
max_turns, max_input_tokens, max_output_tokens, max_duration_seconds
~~~

服务端用 Idempotency-Key 或 body client_key 做作业创建幂等。普通查询返回 queued/running 作业；调用方必须用 GET job 读取真实进度，不能在创建响应中自行宣布完成。

### 7.2 读取和终止

KnowledgeJob 的核心字段：

~~~text
id, workspace_id, requesting_agent_id, source_run_id?, submission_id?,
agent_profile_id, work_item_id, current_run_id?, mode, status, question,
context?, scope?, budget, used, coverage, evidence_ids?,
visited_version_ids?, required_item_ids?, required_relations?, index_revision,
snapshot_ids?, frontier?,
observations?, result?, turn_seq, retry_count, repair_attempt,
next_action_at?, last_decision_digest?, last_error?, client_key, version,
created_at, updated_at, finished_at?
~~~

status：

- queued → running；
- running 可以进入 queued、waiting_retry、completed、incomplete、conflict、cancelled 或 failed；
- waiting_retry 可以回 queued/running，也可以 cancelled/failed；
- completed、incomplete、conflict、cancelled、failed 是终态。

作业查询的 coverage 包含 status、entries、visited_nodes、visited_relations、truncated 和 budget。entries 每项包含 subject、status、evidence_ids、related_item_ids、missing、note。预算、权限、冲突或来源边界导致的结果必须保持 incomplete/conflict。

POST cancel 会转发到当前 Run 的取消面并返回作业最新快照。取消不是删除；终态作业再次取消由服务端按终态语义处理。

管理界面轮询中的作业从非终态进入终态后，会重新读取候选收件箱以反映整理结果；该刷新不清空手动提交表单中的未提交输入。

### 7.3 调查结果

result 是当前 Harness 动作或终态知识包的结构化对象，可能包含：

~~~text
action?: search | read | relations | finish
status?: complete | partial | missing | conflict
answer?, summary?, count?
hits?, items?, versions?
coverage?, evidence_ids?, citations?, relations?
gaps?, conflicts?, revised_changes?, no_change?
submission_id?, snapshot_id?, snapshot_ids?, _accounted_run_ids?
~~~

引用字段为 item_id、version_id、source_id、locator、excerpt。调用方应保存知识 ID+版本和作业 snapshot；后续发布不能改写已交付回答。

## 8. CLI bridge

### 8.1 控制面部署

先构建本机 Worker 可调用的 CLI：

~~~sh
cd agent-team-workbench
go build -o atw-knowledge ./cmd/atw-knowledge
~~~

控制面必须显式配置两个部署变量：

~~~text
ATW_KNOWLEDGE_ENDPOINT=<控制面 HTTP(S) 基址，例如 http://127.0.0.1:8080>
ATW_KNOWLEDGE_CLIENT=<已构建的 atw-knowledge 绝对执行路径>
~~~

基址只能包含 scheme 和 host，可带固定 path，但不能带 credentials、query 或 fragment。ATW_KNOWLEDGE_CLIENT 必须指向本机已安装且可执行的 atw-knowledge；未构建或未安装时，Run 没有知识 CLI bridge，不能称为已接入。

控制面在项目空间下使用 `knowledge-access/` 作为本机 capability 目录，并强制目录权限为 0700；该目录不能是符号链接。每个 Run 的 capability 文件权限为 0600，文件名使用 Run ID。

当前 Worker Shell bridge 只在本机执行 Host 上可用。远程 Worker 不会收到本地 capability 文件或 Shell 命令；远程知识管理员 Run 仍沿既有 Run/Runner/Adapter 协议执行，由控制面 Harness 负责调查动作。不能把本机 Worker bridge 的可用性推断为远端 Worker 已接入。

### 8.2 本机 Run capability

控制面为本机普通 Agent Run 创建一次性的 0600 capability 文件，文件内容包含 Run-bound URL 和 token。文件路径可以进入 Run 的受控输入以便 Shell 调用，但 token 不进入 instruction、Run 事件、产物或最终回答。Run 终态后 capability 文件会被清理，凭据同时失效。

注入到本机 Worker instruction 的命令形态为：

~~~sh
<atw-knowledge绝对路径> --access-file <Run capability 文件> ask '问题与任务用途'
~~~

`--access-file` 只接受私有 regular file（权限不得向 group/other 开放），CLI 会从该文件读取 URL/token 后发起 Run-bound 请求。不要把 token 放入命令行、环境变量、提示词或提交内容。远程 Worker 当前没有这条本地文件桥。

### 8.3 CLI 命令

~~~sh
atw-knowledge --access-file /path/to/run.json ask '问题与任务用途'
atw-knowledge --access-file /path/to/run.json read kb_xxx
atw-knowledge --access-file /path/to/run.json read kb_xxx 2
printf '%s' '<candidate JSON>' | atw-knowledge --access-file /path/to/run.json submit
~~~

- ask 创建调查并轮询 jobs，直到 completed、incomplete、conflict、failed 或 cancelled；
- read 读取当前版本，或用数字版本 / kbv_ ID 读取固定版本；
- submit 发送带 changes 或 no_change 的候选；
- CLI 的 timeout 默认 3 分钟，最大 30 分钟；调用上下文取消时先向控制面 POST cancel，再返回取消错误；
- 同一 key 同一请求重试返回既有结果；同 key 不同请求体返回冲突。

该 bridge 只使用 Agent 已有的 Shell 能力和 Run-bound HTTP 凭据，不代表 Codex/Kimi/provider 或远端 Worker 已经完成验收。知识管理员自己的 Run 不递归领取 Worker bridge，而是沿既有受控 Run 协议进行 search/read/relations/finish。普通 Agent 的新任务结果、已登记 artifact 和提交证据是默认知识输入；未授权源码不会被自动读取。

## 9. 错误体与调用方处理

错误统一为：

~~~json
{
  "type": "https://workbench.example/problems/version-conflict",
  "title": "Resource version conflict",
  "status": 409,
  "code": "version_conflict",
  "detail": "资源版本已变化，请刷新快照后重试",
  "request_id": "req_xxx",
  "retryable": true,
  "current_version": 3
}
~~~

调用方按 status/code 处理：

| 状态 | 语义 | 处理 |
|---|---|---|
| 400 | 缺少 Idempotency-Key 或请求格式无法解析 | 补齐请求或修正 JSON；不要重放同一错误请求 |
| 401 | session 或 Run-bound Bearer 缺失/无效/过期 | 重新取得合法身份；不能盲目重放已终态 Run |
| 403 | 当前角色没有读取或管理权限 | 停止当前动作；不能通过修改 agent_id 绕过 |
| 404 | 资源不在当前 Workspace/Agent/Run scope | 报告不可见或不存在；不能泄露其存在 |
| 409 | 版本、幂等或状态冲突 | 刷新当前快照或用同 key 重试；保留冲突证据 |
| 422 | 请求或能力校验失败 | 修正字段、来源、scope 或配置；不自动降级为 fresh/空结果 |
| 5xx | 控制面内部或 provider 执行失败 | 按 retryable 决定重试；保留原错误 |
