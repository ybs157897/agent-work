# agent-work 仓库工作指令

## 开发风格：dsh-dev-workflow

本仓库的一切开发活动遵循 `dsh-dev-workflow` skill（`~/.agents/skills/dsh-dev-workflow`，操作细节读其 references/）。核心纪律：

1. **一任务一分支**：`<agent|类型>/<短横线任务 slug>`（如 `codex/fix-run-lease-renewal`）；不加 Co-Authored-By 尾注。
2. **分刀提交**：文档先落、注释随手提、功能自成一刀、删除独立成刀；`git add` 指定文件，不用 `git add -A`；conventional commits（`fix(runner): renew lease before expiry...`）。
3. **删除优先于垫片**：本项目未对外承诺兼容性——不留兼容层、不留注释掉的尸体、不做渐进退役；死代码成建制删。
4. **证据匹配表面**：本地只跑改动触面的检查；全量收口归 CI（`.github/workflows/ci.yml` 在仓库根）/PR 窗口；每修一个 bug 钉一条防回归断言；绝不绕过真实失败。
5. **决策留痕**：取舍/否决/负向保证写 `notes/{lifecycle}/{class}/yyyy-mm-dd-slug.md`（格式见 skill 的 references/agent-notes.md）。

蜂群模式补充：耗时领域任务派后台 executor 并让其成为该领域长期 owner（完成不弃，后续变更派同一实例）；owner 工作状态落盘工件；工件合并后清出主干。

## 任务实施隔离：git worktree

主树常带其他会话/用途的未提交改动，主线任务不在当前工作树直接做：

1. **开 worktree 开工**：主线任务开始时先 `git worktree add ../agent-work-<任务slug> -b <agent|类型>/<任务slug>`，该任务的一切改动发生在 worktree 内的支线上，与主树互不影响。
2. **完成后等用户声明再合并**：实现与验证完成后停留在支线、如实汇报，**不自行合入主线分支**；待用户明确声明/指示后，才由我回主树执行 `git merge --no-ff <分支>`，随后 `git branch -d <分支>` + `git worktree remove`，恢复现场。
3. 分支命名、分刀提交、决策留痕等 dsh-dev-workflow 纪律在 worktree 内照常；蜂群 executor 派进 worktree 干活，文件边界规则照旧。

## 验证入口

- 后端：`cd agent-team-workbench && go build ./... && go vet ./... && go test -race -count=1 <触面包>`；gofmt 必须干净（CI 有门禁）。
- 前端：`cd agent-team-workbench/web && pnpm tsc --noEmit && pnpm test && pnpm lint`。
- 迁移：`go run ./cmd/migrate -dsn "sqlite://workbench.db"`；`migrations/` 与 `migrations/sqlite/` 双目录保持语义等价。
- 提交前确认 repo-local git 身份已配置（本机无全局身份）。

## 前端设计纪律

改动 `agent-team-workbench/web/` 的视觉与交互前先读 `agent-team-workbench/web/DESIGN.md`（设计事实源）；颜色/间距/圆角只引用语义 token，禁止内联色值（`src/design-tokens.test.ts` 门禁）。

## 会话/运行时硬约束（防回归共识）

- Run 状态机 13 态是唯一权威；ModuleRunner 是进程内唯一推进点；任何 Outcome 都必须能落终态（不许卡死）。
- resume 探测失败的 adapter 必须报 `session_unknown` 走自愈，**永不静默降级 fresh**（instruction 已是「只发当轮」）。
- task_sessions 只写墓碑不 DELETE；runs_count/usage 按 run 维度幂等。
- 取消一律经控制面前转 + adapter 取消面，进程组终止用启动时缓存的 pgid。
