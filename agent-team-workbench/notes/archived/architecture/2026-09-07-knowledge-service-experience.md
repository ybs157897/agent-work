# 知识服务新手体验与 Agent 使用链路

Status: archived
Archived: 2026-09-08

## 决策与理由

当前后端已经有一条完整的知识真相链：`knowledge_items`/`knowledge_versions` 保存条目和不可变版本，`knowledge_sources` 保存证据，`knowledge_relations` 保存关系，提交与调查作业分别由 `knowledge_submissions` 和 `knowledge_jobs` 持久化；发布后检索只读 SQLite 的 published projection。知识管理员不是第二种 Run，而是由 chat_backend 注入的内置 Agent 身份执行；整理时在该 Run 上替换为固定的 `knowledge-librarian/v1` 协议提示词。这样保留现有 Run、lease、计费和终态唯一权威。

现有配置 API 已经支持读取和 CAS 更新：`GET/PATCH /api/v1/workspaces/{workspace_id}/knowledge/config`。每个 workspace 由系统提供唯一的内置 `knowledge_librarian` Agent；PATCH 只更新启用和自动收集开关，管理员的运行方式与模型在该内置 Agent Profile 上配置。缺失配置会惰性创建为 enabled 默认值，旧普通 Agent 只在内置身份首次创建时贡献一次 runtime/model 偏好，不再作为运行目标。现有知识页已消费这条 API，但此前缺少把知识管理员带到“智能体模型/运行配置”的入口，选中管理员 Agent 时还会因 `skills` 空值而白屏。普通 Agent 的知识使用也已有 Run-bound `ask/read/submit`：本机 Run 只有在显式设置 `ATW_KNOWLEDGE_ENDPOINT`、`ATW_KNOWLEDGE_CLIENT` 后才获得 0600 capability 文件和注入 instruction；远程 Run 不获得本机文件桥。终态后 `auto_collect` 会把任务产出放入收件箱，由管理员整理、再显式发布。

用户只在聊天正文里表达需求或事实；由产品、开发等 Agent 在得到用户确认后调用 Agent-bound record API，提交自然语言沉淀内容。服务端从当前 Run 得出 Agent、Workspace、Run 和 WorkItem 身份，把原文封存为 `agent` 来源候选，再交给内置知识管理员 Job 结构化。前一轮面向人类的 `/knowledge/materials` 入口没有新的产品消费者，将删除其 API、实现和契约，不留下兼容垫片。后续仍复用现有 curation Job、证据核验、版本 CAS、发布门和检索投影，不引入另一张知识表或另一套执行状态机。

当前 M1 HTTP 身份仍是服务端固定的 `user_demo`（`GET /api/v1/me` 同样返回该值），不把它描述成已经接入生产登录。Agent-bound record 入口不接受客户端 actor/Agent/user 字段；身份只能由活动 Run 的 Bearer capability 得出。

## 放弃了什么

- 早期曾把知识管理员实现为普通 Agent；该方案已撤回。当前身份由唯一 workspace-scoped 内置 Knowledge Librarian system Agent 承载，避免用户选择或修改管理员身份。
- 不让浏览器直接创建 effective 条目或由用户填写结构化 claims。Agent 的原始记录只进入 candidate/submission 收件箱，结构化、去重和关系整理交给内置管理员 Harness；普通记录只能以带 Agent 身份来源的 `observation/experience` 受控自动进入 effective，确认需求则必须带受限发布意图、整理为 `requirement/agreement` 并通过来源/版本/权限校验。
- 不把原始材料直接塞入 `knowledge_items`，也不新增平行的“用户知识库”表。原始材料以既有 submission request 和受控 source evidence 保留，整理后的唯一有效状态仍由知识表和版本链承载。
- 不把普通 Agent 的 workspace API 查询权限扩大成管理员全库权限。Agent 通过当前 Run 的 bearer capability 读取自身可见 effective 知识；跨 Agent 私有视图仍由管理面身份和显式 `agent_id` 控制。

## 现状证据与最小接口契约

- 配置：`internal/httpapi/handlers_knowledge_write.go` 注册 GET/PATCH config；`internal/application/knowledge_librarian.go` 对 Agent/workspace/availability 做校验和版本 CAS。
- Agent 查询：`internal/httpapi/handlers_knowledge_jobs.go` 的 Run-bound inquiry；`internal/httpapi/handlers_knowledge.go` 的 Run-bound item/version read；`handlers_knowledge_write.go` 的 Run-bound submission/record。
- Run 注入：`internal/application/runs.go` 在创建普通本机 Run 前调用 `AttachKnowledgeRunAccess`；管理员 Run 带 `knowledge_librarian` 标记并跳过递归 capability。
- 自动收集：`internal/application/knowledge_capture.go` 在外部任务终态且 `auto_collect=true` 时入 capture inbox，随后 `StartKnowledgeCuration` 创建管理员 Job。
- 结构真相：`internal/domain/knowledge.go` 的 `KnowledgeItem`、`KnowledgeVersion`、`KnowledgeSource`、`KnowledgeRelation`、`KnowledgeSubmission`；`internal/persistence/sqlstore/knowledge*.go` 为唯一 SQLite 实现。

Agent-bound 原始记录入口的请求/响应语义：

```text
POST /api/v1/knowledge-agent/runs/{run_id}/records
Idempotency-Key: <required>
{
  "content": "Agent 从聊天中提炼的自然语言沉淀内容，或使用 text 字段，二选一",
  "title": "可选标题",
  "publish_intent": "confirmed_requirement（可选；仅表示用户已确认需求/约定）",
  "client_key": "可选；缺省使用 Idempotency-Key"
}
→ 201 {submission, job?, curation_status, curation_error?, publication_status?}
```

服务端将 Agent 记录转换为一个待整理 change：普通记录使用 `kind=observation`，确认需求使用 `kind=requirement`，两者均为 `visibility=workspace`；正文保留原文。source 使用 `kind=agent`、`ref=当前 Agent ID`，并由服务端写入 `origin_agent_id`、`origin_run_id` 和 `record_intent`（普通记录为 `agent_record`）。服务端立即启动 curation Job。普通记录只有在管理员保留 `observation/experience`、固定来源、版本、关系和 workspace 权限校验通过后才自动发布，并保持 `verification=agent_identity_only`；`publish_intent=confirmed_requirement` 则必须整理为 `requirement/agreement` 才可自动发布。证据不足、冲突、scope 不清、非法类型或源 Run 失败/取消都保持 `needs_review`，通过 Job 结果/聊天反馈缺口。自动发布审计绑定原调用 Agent/Run 和管理员 Agent，不冒充用户，也不声称实现已验证。任务结束自动收集仍独立受 `auto_collect` 控制。

## 已验收证据

2026-09-08 的 root 真实 UI/运行验收曾验证上一轮原始材料入口：创建 `kbj_01M1Y9PW19BM9AKF0BF0Q6S9NM`，知识管理员实际完成 3 个 Codex Run，自动生成 3 个 draft 和 2 个 `triggers` 关系；整理后的 `kss_01M1Y9SADQ72FWDR3CY1S7P39C` 保持 `needs_review`，`source.kind=user` 来源仍在，作业最终为 `incomplete` 并明确列出未提供的证据。该入口随后因产品方向更正而删除；当前闭环由 Agent-bound record 使用 `source.kind=agent`，确认需求走受限自动发布门。

普通 Agent 的知识 bridge 仍以部署事实为准：`cmd/control-plane/main.go` 只有在显式配置 `ATW_KNOWLEDGE_ENDPOINT` 与 `ATW_KNOWLEDGE_CLIENT` 后，才由本机 Run 注入 `atw-knowledge --access-file`；远程 Host 不会收到本机 capability 文件。知识管理员 Run 使用专用协议提示词，并跳过递归 bridge。当前 `GET /api/v1/me` 的 M1 身份仍是服务端固定 `user_demo`，不能当作生产登录 actor 已接入。

原 `/knowledge/materials` 人类收件入口已因产品方向更正而删除；其消费者改为 Agent 的 `/knowledge-agent/runs/{run_id}/records`，知识库页面只保留只读内容和作业状态。

成功源 Run 的生命周期取舍：Run-bound inquiry 一旦已经创建 Job，源 Run 进入 `succeeded` 表示调用方已完成交付，不能再被解释为用户取消，知识 Job 继续由自己的 turns、时长和查询预算推进；源 Run 进入 `failed`、`cancelled`、`interrupted` 或 `lost` 时仍沿用取消策略。源 Run 终态后的 capability 清理保持不变，旧 Run 不获得延长权限。跨 Run 的 Agent 读取和 CLI 显式 detach 属于后续协议扩展，本次只修正成功终态的误取消。

## 复活条件

【Agent 记录需要多个文件、附件二进制或显式领域 owner 审批 → 扩展 Agent-bound record envelope 和 source/审核门 → 保留当前自然语言 record 字段与 Run/Agent 绑定兼容】

## 查询等待预算

知识 CLI 的缺省等待时间从同一 `KnowledgeJobBudget.Normalize()` 默认墙钟预算派生，并增加一分钟收取终态结果的余量。原 CLI 三分钟超时小于服务端默认五分钟预算，可能在管理员仍合法调查时提前放弃；现在默认等待六分钟，服务端原预算不扩大，显式 `--timeout` 仍可覆盖。
