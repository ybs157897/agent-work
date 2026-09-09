# 需求分析项目绑定验收

日期：2026-09-09。实现基线 `main@651056c`，任务支线 `codex/task-intake-project-binding`。实现与本地验收完成，未提交、合并或推送。

## 交付行为

“任务对话”顶部先选择已登记项目与当前存在的 checkout，再核对分支、当前提交和未提交修改。未选定或绑定不可用时禁止分析/新发布；dirty 工作树需要明确勾选纳入。选择不会切换Git分支、创建工作树或修改项目文件。

对话、草案、输入和重试请求按 Workspace + 项目选择隔离。浏览器刷新后会重新核对绑定。旧的未绑定本地内容保留为恢复来源，不能直接变成绑定新项目的已分析草案。

后端使用受信 HostRegistry 和 WorkspaceLocation，核对真实Git根、HEAD以及staged/unstaged/untracked内容摘要。目录不能通过Git向上发现而冒用父仓库。分析请求在模型调用前后核对；Task创建同事务保存显式DevelopmentContext与原始绑定元数据，再触发Coordinator。重放检查原绑定，原创建未启动时的漂移会阻塞首次Run；已有运行后的工作树不永久冻结。

## 自动检查

- Go全模块 `go build ./...`、`go vet ./...` 通过。
- 后端owner执行 application/httpapi/domain/hostregistry/taskintake/sqlstore 相关普通与聚焦race检查；首次启动漂移、伪造validated handle、staged blob变化、同key换项目、嵌套非Git目录及linked worktree均有回归。race使用120/180秒有界检查，未把局部结果当全仓race。
- 最终前端完整套件：**120个测试文件、970条测试通过**。类型检查、lint、生产构建通过；工作树复用已安装的node_modules，pnpm拒绝symlink时执行同等的bundled tsc/vitest/eslint/vite命令，没有升级依赖。
- 独立SQLite按唯一迁移目录应用到0049；原绑定元数据不可原地更新。
- 真实HTTP/SQLite检查：22项核心检查通过；补充的非Git/父仓库拒绝、跨Workspace拒绝、模型返回前代码变化拒绝3项通过。

详细日志与原始JSON位于本任务工作树被忽略的 `.agent-work/project-binding-acceptance/` 和owner状态工件；不用构建成功代替浏览器验收。

## 浏览器与状态核对

| 路径 | 实测结果 |
| --- | --- |
| 未选择项目 | 不默认选择第一个；即使输入文字也不能分析 |
| Alpha/main | UI分支/提交/clean状态与真实Git一致 |
| Alpha→Beta→Alpha | Beta不出现Alpha输入；回到Alpha恢复原草稿和模型选择 |
| 现有linked worktree | 正确选择feature/sample，并显示与main独立的提交 |
| 浏览器刷新 | 恢复所选项目与草案，重新向服务端核对后才允许使用 |
| 现有web-idea | 只读探测显示2bbfb37与40个未提交修改文件；未勾选时禁止分析，明确勾选后允许。未在原项目发布或执行任务 |
| 发布前内容变化 | 后端拒绝；UI清除不应保留的发布pending，保留原内容，绑定失效且发布禁用 |
| 新clean HEAD重新核对 | 原消息保留，旧可发布草案被移出发布状态，不会直接挂到新提交上 |
| 创建成功但响应丢失 | 注入一次本机fetch响应丢失，服务端已创建Task；再修改测试工作树并重试，UI找回相同Task ID，SQLite同client_key只有一行 |
| 目录失效 | 临时移走隔离checkout后核对失败，发送禁用，原输入保留；目录原样恢复 |
| 明暗/窄屏 | 1440、1024、640宽度无页面横向溢出，主侧栏保留 |
| Portal与键盘 | 深色“重新开始”弹窗位于React根外、焦点在dialog内，Escape关闭；清除提示按当前项目范围修正 |

浏览器使用真实control-plane、Git目录与SQLite。模型服务为本机OpenAI协议模拟器，返回带标记的固定草案；这证明绑定、传输、错误恢复和发布路径，不证明真实模型的需求分析质量。Beta发布恢复Task为 `wi_01M20TTGKZE5DCD3WGB6XCVEJ9`，仅属于隔离验收库。

## 本轮发现并修复的边界

- 项目列表和解析响应加入Scope/序列隔离，避免迟到结果污染另一个Workspace。
- 将绑定前置拒绝与真正不确定的发布结果分开，409/422/413都有明确处理，幂等冲突和未知5xx仍保守保留原请求。
- 绑定错误丢失snapshot后，重新核对新HEAD也会使旧草案失效，避免“刷新变绿”直接发布旧分析。
- Git探测验证真实仓库根，拒绝嵌套普通目录借用父Git仓库身份。

## 边界与复验

本刀只支持已登记的本机Git根和已有checkout。任意路径登记、远程目录、自动建/切工作树、历史提交内容快照不在本刀范围。项目元数据进入分析，`code_read=false`明确表示未读取代码全文；Word/PPT与多源材料分析仍是后续工作。

绑定是乐观的请求边界校验和WorkItem创建来源，不是文件系统锁，也不是冻结后的代码副本。后续运行期间外部修改仍由既有执行治理与后续证据设计处理。

预览为本机隔离服务，地址保存在 `.agent-work/project-binding-acceptance/server.json`；使用已观察到的PID/端口管理，不猜测或广泛停止进程。当前预览使用协议模拟模型，主服务配置、业务数据库和原web-idea源码未改。

## 界面证据

![选定Alpha与提交基线](../review/assets/task-intake-project-binding/bound-alpha-1440.png)

![现有web-idea的dirty确认](../review/assets/task-intake-project-binding/web-idea-dark-1024.png)

![窄屏选择已有worktree](../review/assets/task-intake-project-binding/worktree-640.png)

![新HEAD后旧草案不可直接发布](../review/assets/task-intake-project-binding/new-head-invalidates-draft.png)
