# U06 确认结果形成任务与发布

状态：实施中，2026-09-09。U01–U05 已验收；本单元在 `codex/u06-task-publication` 独立工作树，不提交、不合并、不推送。

## 用户流程

在现有 Chat 中选择本次已经明确确认的事项，填写任务标题，保存并查看草案；明确点击“发布任务”后，才进入现有 Task/Run 流程。草案正文、验收条件、来源和确认版本由服务端从所选有效产品结论生成。未选事项不进入任务，也不新增模型调用链或项目目录选择。

所选事项必须有当前 `valid + confirmed` 产品结论。U05 问题模型没有 required/blocking 字段，普通未答或 deferred 问题不会覆盖这项明确确认；未选事项的疑问不阻断独立任务。当前分析为 stale、analyzing、failed 或来源核验失败时不能形成/发布有效草案。AI 建议和普通回答不能替代产品确认。

## 接口

- `POST /work-items/{chat}/analysis/drafts`：仅接收 `{expected_version,revision,item_ids,title,client_key}`。`item_ids` 必须明确选择。浏览器不提供正文、验收、确认 ID、可信目录、代码基线、Workspace、Agent 或 actor。
- `GET /work-items/{chat}/analysis/drafts` 与 `GET .../drafts/{id}`：返回有界列表/持久草案。草案含 id、version、当前状态、标题/正文/验收条件、所属 Chat/Workspace、分析 revision、确认 IDs、来源依赖、路径无关的代码基线及已发布 Task ID。
- `POST .../drafts/{id}/recheck`：核验有效性，不启动模型或 Task。漂移时保留旧草案和原因，要求重新形成草案。
- `POST .../drafts/{id}/publish`：仅接收 `{expected_version,client_key}`，唯一创建 Task 的入口。成功返回确切的 Task 与草案发布记录；前后端使用同一 DTO，不兼容猜测多个形状。

准确 DTO 由 backend owner 锁定在 `.agent-work/u06-api-contract.md` 并同步 OpenAPI，frontend 严格消费。所有直接 ID 入口都核对当前 Workspace/Chat 归属。

## 可信基线

Architecture 提供 `Registry.ResolveProjectBaseline(ctx, *domain.ExecutionContextSnapshot) (domain.ProjectBaseline,error)`。只通过持久执行上下文和 HostRegistry 解析代码根，不接受客户端路径，也不向 API 返回本机绝对路径。

基线包含 repository identity、ref kind/branch/checkout、实际 HEAD，以及 staged、unstaged、untracked 和 combined content digest。已有 dirty 工作树允许使用，后续身份、HEAD 或内容发生漂移才拒绝；未知 HEAD、Host/mount 变化、越界或扫描超限均拒绝。复用有界 Git 扫描逻辑，包含重命名、删除、索引变化和未跟踪文件。

草案形成和发布时核对当前有效确认、来源、分析/草案版本及真实项目基线。Task 第一次 Coordinator Run 前再次核对基线，防止发布到开始之间发生漂移；发生漂移进入现有阻塞/恢复状态，不重新选择目录或偷换版本。

## 原子创建与恢复

Task、DevelopmentContext、确认/来源/代码基线记录、publication 关联、Coordinator state 和 comment cursor 在同一事务落库，提交之后才启动现有 Coordinator。不能先调用自动启动的公开 CreateWorkItem，再另写上下文。后续仍由现有 Run 状态机和 ModuleRunner 推进。

草案和发布均保存实体级 client_key 及完整请求指纹。相同请求重试取回同一实体，不同内容复用 key 返回冲突；同一草案换 key 也不能重复创建 Task。已发布后，即使代码后来变化，原发布请求仍可取回既有 Task。启动副作用失败保留已创建 Task，交既有恢复路径处理。

前端单独保存 publicationDraft 及冻结提交载荷/key。刷新、A/B 切换、失败或响应丢失都保留归属；收到服务端保存回执后清理待提交内容。迟到响应不能覆盖新空间或新草稿。

## Owner 与验收

- Architecture：`domain/project_baseline.go`、`hostregistry/project_scan.go` 及专包测试。
- Backend：application、persistence、migrations、HTTP/OpenAPI、剩余 domain 和 main 接线。
- Frontend：`web/`，遵守 DESIGN 与语义 token。
- Root：验收、文档、补丁整合；共享文件只有一个 owner。

使用隔离 U05 数据库延续真实 web-idea/DOCX/PPTX/MD/知识分析；Task 执行明确配置 Mock，不修改用户 web-idea。验收覆盖实际 UI 草案/明确发布、HTTP/SQLite 归属与幂等、确认/来源/真实 Git 基线漂移、第一次 Run 快照、刷新/A-B/响应丢失恢复。真实 AI、实际系统行为与 Mock 执行分别记录，验收通过再勾单元和总清单。
