# 统一知识库（资料管理员）契约

Status: implemented

## 形态

每个 workspace 一个知识库（`knowledge_libraries`），库根由产品决定，当前指向
`<repo>/.agent-work/knowledge`。**Markdown 正文（`content/**`）是写作真相，SQLite 是可重建投影**：
reindex 从库根重建记录，而不是反过来把库当成真相源。

七个逻辑对象：Document / Entity / Assertion / Relation / Evidence / SourceSnapshot / KnowledgeRelease。
来源（`knowledge_library_sources`）登记多仓库、各自 commit 冻结成 SourceSnapshot 与 representation，
代码证据由程序采集（Evidence），解析结果落 Assertion/Relation，发布时钉进 Release。

## 不变量

- **单条 FIFO 写队列**：每库同时只有一个活动任务（`idx_knowledge_write_tasks_single_active`），
  `blocked` 也占队头（`status.OccupiesHead()`）。事件接口是异步契约：先给 202 回执，再进队列，
  页面因此不把「已受理」当成「已生效」。重试已有成功轮次的任务只重跑 harness，不新派模型轮次。
- **发布一次性封口**：`SettlePublication` 在同一个事务里写 publication、current 指针、supersede、
  任务终态和事件终态；冻结输入树只在封口之后释放。任一步失败整体回滚并保留可恢复输入，恢复器
  只接管不再占队头的任务产物，同一轮重放复用同一 release。
- **读取按 release 钉住**：读者只能看到 `committed` 的发布（`visibleReleasePredicate`）。默认读解析
  当前已提交 release；显式版本号必须等于该 release 固定的版本（否则 422），未发布版本 404；证据展开
  在给定 release 时必须确被该 release 引用（沿用证据仍可展开）。
- **取消与恢复的边界**：已进入发布阶段（publication 为 `prepared`/`committed`）的任务拒绝取消并提示
  先恢复或重试；running/awaiting_agent/completed 同样不可取消。
- **冻结输入**：`git archive <commit>` 加冻结的 dirty overlay；需求在受理时冻结进
  `_system/requirements/<reqID>/<version>-<shortdigest>/requirement.md`，登记为 `kind=requirement` 的
  source。`declaresRequirement` 只认真实需求身份或需求事件，code/workspace 事件的 `content_ref`/`summary`
  只作为变更上下文，不会被当正文。

## 接口与界面

HTTP 命名空间是 `/api/v1/workspaces/{workspaceId}/library/...`（来源登记、事件受理、状态与 release、
文档/断言/关系/证据读取、reindex、任务取消）。计划动词 `consult_knowledge` 通过
`application.KnowledgeLibraryRetriever` 读同一个库，不再有第二条知识通道。

管理页挂在 `/library`（旧 `/knowledge` 地址已随旧实现一起退役，见
[退役旧资料管理员](2026-09-12-retire-legacy-knowledge-tables.md)），五个密集 tab 同页：
资料库与来源、初始化与更新、知识浏览、查询与展开、版本与队列；交互与视觉规则见 `web/DESIGN.md`。
