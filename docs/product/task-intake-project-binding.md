# 需求分析前选择项目与代码工作区

> 2026-09-09 状态修正：下文记录的是已验证过的局部项目绑定实现。用户已明确“工作空间入口唯一且全局使用”，页面级项目选择方案须按[执行总清单 U01](workspace-ai-workbench/units/U01-global-workspace.md)重整；历史验收不代表新要求已完成。功能开发保持暂停。

状态：已实现并完成本地验收，尚未合入main。基线 main@651056c；任务支线 codex/task-intake-project-binding。

## 用户行为与范围

在“任务对话”需求分析入口先选择已登记的项目工作目录和 checkout，再核对当前分支、HEAD 与未提交修改。用户明确纳入未提交修改后才可分析；也可以改选干净 checkout。选择不会执行 checkout、stash、建分支、修改代码或扩展 Host 路径授权。

未选择或绑定失效时，分析与发布不可用，并提供选择/刷新/设置的恢复入口。项目切换后对话、答案、草案与待重试请求按项目绑定隔离；过期请求不得覆盖当前项目。恢复本地草稿前重新核对绑定。发布结果不确定时保留原请求、幂等键和绑定，禁止换项目后重发同一发布。

本刀接入已有 task-intake 文本分析。项目元数据作为服务端核验的分析上下文提供；不宣称本刀已经读取所有代码、接入Word/PPT或实现多源需求分析。知识仍按当前Workspace权限约束，目标repository作为明确范围标识，参考GitHub项目与目标目录分开。

## API约定

沿用现有WorkspaceLocation、HostMount、DevelopmentContext与执行快照权威；宿主绝对路径不进入API。

- `GET /api/v1/workspaces/{workspace_id}/task-intake/projects` → `{items: IntakeProjectOption[]}`。Option包含`workspace_location_id, display_label, repository_identity, execution_host_id, status, unavailable_reason?, refs: IntakeProjectRef[]`；Ref为`ref_kind, branch_name?, checkout_ref?, worktree_ref?, label`。仅返回该Workspace已登记的目录；远程/非Git/离线/失效目录明确不可用。
- `POST /api/v1/workspaces/{workspace_id}/task-intake/project/resolve` 请求 `IntakeProjectSelection`（`workspace_location_id,ref_kind,branch_name?,checkout_ref?,worktree_ref?`）。返回 `IntakeProjectSnapshot`：`workspace_id, selection, project_key, display_label, repository_identity, current_branch, head_revision, dirty, changed_file_count, binding_digest, location_version, mount_generation`。所有字段来自服务端真实探测。
- `ProjectBindingInput` 为 `{selection: IntakeProjectSelection, expected_digest: string, include_uncommitted: boolean}`。`task-intake/analyze` 必须携带`project_binding`；WorkItem创建可以携带同字段，需求发布必须带上。
- 后端按逻辑选择重新解析Host/Location/ref并重算Git HEAD+工作树摘要，拒绝身份/版本/内容漂移，不信任客户端提供的项目元信息。dirty且未明确include_uncommitted时拒绝。
- 分析开始前以及返回结果前都核对绑定。发布采用同一核对，并在创建Task事务内持久化已有DevelopmentContext，再触发Coordinator，避免先启动在默认目录再补绑定。
- 建议稳定错误：`analysis_project_required`、`analysis_project_unavailable`、`analysis_project_changed`、`analysis_project_dirty_confirmation_required`、`analysis_project_too_large`。错误不泄露宿主路径、Git凭据或原始命令输出。

Git检查需要有超时、文件数/字节数上限，避免大目录或未跟踪文件拖住分析入口。绑定是请求边界的可核对快照，不是操作系统文件锁，不保证外部进程永远不修改目录。不支持任意旧commit或自动创建工作树，只选已登记的现有checkout。

## 验收

- 显式选择两个本地Git项目与现有checkout，显示正确的分支/HEAD/dirty状态。
- 未选择、未确认dirty、跨Workspace伪造、未登记路径、目录失效、远程Host、内容漂移均fail closed。
- staged/unstaged/untracked内容变化可被发现；同路径内容变化不能仅靠status文件名判断。
- A/B项目切换隔离对话草案；迟到响应无效；刷新恢复重新核对；发布失败保留原绑定与幂等键。
- 发布Task的显式DevelopmentContext在Coordinator自动启动前已存在，目标目录与分析绑定一致。
- 浏览器验证真实服务选择/切换/失效、明暗主题、窄屏与Portal；模型调用可用本地协议模拟，但必须标明不是真实模型能力验收。

不自动提交、合并或推送。测试范本位于另一独立支线，原始web-idea和主树资料保留。

验收结果见[项目绑定验收](task-intake-project-binding-acceptance.md)。
