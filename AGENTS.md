# agent-work 仓库工作指令

本文件规定工程事实和仓库操作；Codex 的团队组织与角色职责由用户级指令定义，不在本仓库重复配置。**本仓库任何改动——产品代码、文档、AGENTS.md 等指令文件、notes——一律在任务 worktree 的任务分支上完成，不在主树就地编辑**；主树只承载合并与只读核对。用户级配置（如 `~/.agents` 下的 skill，非 git 仓库）按最小编辑 + 备份处理。

## 开发风格：dsh-dev-workflow

本仓库开发遵循 `dsh-dev-workflow` skill（`~/.agents/skills/dsh-dev-workflow`），只在相关场景读取 references。引用 skill 的一般建议不另行增加人工确认；以下仓库约定明确提交、隔离和检查的适用范围。

1. **一任务一分支**：`<agent|类型>/<短横线任务 slug>`（如 `codex/fix-run-lease-renewal`）；不加 Co-Authored-By 尾注。
2. **分刀提交**：产品开发任务允许在任务分支上对本次改动做本地提交，用户明确要求不提交时除外；该约定不授权 merge、push 或发布。存在设计/契约文档改动时先落文档；独立的注释、功能和纯删除按可审阅单元分刀，测试随实现。`git add` 指定文件，不用 `git add -A`；conventional commits。不为分刀重复询问授权或制造空文档、空提交。
3. **删除优先于垫片**：本项目未对外承诺兼容性——不留兼容层、不留注释掉的尸体、不做渐进退役；死代码成建制删。
4. **证据匹配表面**：开发期间跑改动触面的检查；提交、汇报和 Owner 审查不自动触发全量重跑。全量收口归 CI（`.github/workflows/ci.yml`）或交付窗口；每修一个行为 bug 钉一条防回归断言，绝不绕过真实失败。
5. **决策留痕**：重要取舍、否决和负向保证写 `notes/{lifecycle}/{class}/yyyy-mm-dd-slug.md`；格式见 skill 的 `references/agent-notes.md`。普通代码和文档已经足够表达的内容不重复记。

耗时领域工作复用已确认的执行者实例；执行者不会因此自动成为正式 Owner。团队协调档案按用户级指令保存。项目临时计划文件在合并后按需清理，保留仍有效的产品决策文档与验收证据。

## 任务实施隔离：git worktree

主树可能有其他会话的未提交改动；**所有进入仓库的改动**（产品代码、文档、指令文件、notes）都在任务 worktree 的任务分支内完成，不混入主树在途工作。就地编辑主树文件会让他人改动被覆盖、被误提交或直接丢失。

1. **先盘点再隔离**：只读核对分支、基线、未提交改动、已有 worktree 和任务归属。同任务续作复用已确认的 worktree；新任务才执行 `git worktree add ../agent-work-<任务slug> -b <agent|类型>/<任务slug> <已确认基线>`。基线和路径按现场确定，不把当前分支或旧任务分支自动当作新任务基线。子 agent 使用派工指定的绝对 cwd，不重复创建 worktree。
2. **按已有授权收尾**：实现与验证完成后停留在任务分支交付；本任务已有明确合并授权且条件满足时，回目标主树执行 `git merge --no-ff <分支>`，无需重新询问。没有合并授权则交付分支和证据；用户明确要求验收后再确认的，保留该最终确认点。合并授权不包含 push。
3. **清理顺序**：合并后确认任务 worktree 内无须保留的改动和其他会话占用，先 `git worktree remove <任务worktree绝对路径>`，再 `git branch -d <已合并任务分支>`，最后检查 `git worktree list`。不强制删除、不清理无关 worktree 或分支。
4. 分刀提交、决策留痕和文件唯一修改者规则在任务 worktree 内照常执行。

## 验证入口与时机

以下为对应触面的验收入口，不要求每次编辑或每个提交全部重跑。开发期选择受影响包/测试；交付时补齐该触面必要门禁，CI 跑全量。独立 QA 运行足以支撑结论的验证；有新修改、真实失败或证据缺口时重跑受影响检查。

**门禁按风险选，不按包边界选**：优先用能证伪本次改动的最小证据，包级/全量门禁是兜底而不是交付仪式。某条门禁在本机跑不动（超时、几十分钟、环境相关）时，跑定向测试，把「哪条、为什么没跑完、由谁覆盖」记进台账后照常交付，不为它阻塞；反过来，改动触及并发、锁、共享状态、取消、进程组、生命周期时，`-race` 与相关集成测试是必须的，不得以「跑得慢」跳过。

- 后端触面交付：`cd agent-team-workbench && go build ./... && go vet ./...`，gofmt 干净（CI 有门禁），行为测试跑改动直接覆盖的用例（`go test -count=1 ./internal/<pkg>/ -run <相关用例>`）。`-race` 只在改动触及并发/共享状态/生命周期时加，且只跑相关包或用例；纯逻辑改动不必加（race 不提供判定力，只贡献耗时）。包级与跨包全量、全量 `-race` 归 CI。
  已知事实：`internal/application` 全包 `-race` 在本机需 30 分钟以上并会超时（496 个串行集成测试，每个重建 SQLite 并跑全量迁移；无 race 全量约 160 秒），不要拿它当交付阻塞门禁。
- 前端触面交付：`cd agent-team-workbench/web && pnpm tsc -b && pnpm test && pnpm lint`。
  根 `tsconfig.json` 是 `files:[]` 项目引用，`pnpm tsc --noEmit` 在根配置上等于空跑；必须用 `tsc -b` 才会检查 `src` 的 strict 类型。
- 迁移触面：用任务独占临时数据库执行 `go run ./cmd/migrate -dsn "sqlite://<任务临时数据库绝对路径>"`；不得把共享或正在使用的 `workbench.db` 当验收库。`migrations/` 是唯一 SQLite 迁移真相源，不得新增数据库方言分支或第二套 DDL。
- 提交前自行检查 repo-local Git 身份；缺失时先查现有项目约定，不编造身份。“检查”不等于要求用户再次批准提交。

## 前端设计纪律

改动 `agent-team-workbench/web/` 的视觉与交互前先读 `agent-team-workbench/web/DESIGN.md`（设计事实源）；颜色/间距/圆角只引用语义 token，禁止内联色值（`src/design-tokens.test.ts` 门禁）。

## 会话/运行时硬约束（防回归共识）

- Run 状态机 13 态是唯一权威；ModuleRunner 是进程内唯一推进点；任何 Outcome 都必须能落终态（不许卡死）。
- resume 探测失败的 adapter 必须报 `session_unknown` 走自愈，永不静默降级 fresh（instruction 已是「只发当轮」）。
- task_sessions 只写墓碑不 DELETE；runs_count/usage 按 run 维度幂等。
- 取消一律经控制面前转 + adapter 取消面，进程组终止用启动时缓存的 pgid。
