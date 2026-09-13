# 合并前用 ego 在隔离环境自验

Status: implemented

## 决策与理由

用户 2026-09-13 定调：**改完之后先自己用 ego（浏览器）验证，没问题了再谈合并**。合并后验证会把"未验的
候选代码"先落到 main 与世界共享的 8080 上，验证失败时回滚成本高且已经打扰了正在使用工作台的人；隔离
验证把候选代码跑成第二个实例，验证通过再合并，回滚面从"线上行为"缩到"一个临时目录"。

隔离实例的配方（`.agent-work` 与数据库都是本机私有状态，worktree 里没有）：

1. **候选二进制**：在任务 worktree 里 `go build -o bin/control-plane ./cmd/control-plane`。
2. **凭据**：复制主树 `agent-team-workbench/.agent-work/credentials.local.yaml` 到 worktree 同路径。
   真凭据在项目空间，不在 `models/credentials.local.yaml`（那只是 legacy 兼容路径）；漏了会在第一轮
   run 直接 `failed(kimi_config: 缺少 API Key)`。
3. **数据库**：用主库的一致快照 `sqlite3 <主库> ".backup '<tmp>/workbench.db'"`，不要两个实例写同一个库。
4. **隔离仓库根**：`git clone --local <主树> <tmp>/repo`，再写一份临时 `host-registry.yaml` 指向它；
   这样 agent 落盘/建 worktree 的副作用都留在 `/tmp`，不污染主树（主树常有其他会话的在途改动）。
   自建 worktree 会出现为 `<tmp>/repo` 的兄弟目录，仍在隔离范围内。
5. **generation 对齐**：新 registry 的 generation 与复制库里的 `workspace_locations.mount_generation`
   不一致时创建 Run 会被 409 拒绝；从
   `GET /api/v1/execution-hosts/host_local/mounts` 取 `registry_generation`，UPDATE 复制库的
   `workspace_locations.mount_generation` 与 `execution_host_mounts.registry_generation`。
6. **启动**：`DATABASE_URL=sqlite://<tmp>/workbench.db LISTEN_ADDR=:8090 ATW_HOST_REGISTRY=<tmp>/host-registry.yaml WEB_DIST=<主树>/web/dist ./bin/control-plane`
   （纯后端改动可复用主树已构建的 `web/dist`；前端改动要在 worktree 里 `pnpm build` 并把 WEB_DIST 指过去）。
7. 用 ego 打开 `http://127.0.0.1:8090/...` 走真实交互；验完杀掉实例、删临时目录，主树的 8080 全程不动。

## 放弃了什么

- **合并后在生产实例上验证**：省事，但候选代码在未被验证前就已经对用户可见；2026-09-13 之前两次交付
  都是这个顺序，用户明确改掉了。
- **让第二个实例连主库**：省掉快照与 generation 对齐，但两个控制面写同一个 SQLite、共享 SSE 发布面，
  且自检 run 会写进用户真实历史。
- **只跑单测就算验过**：契约文本类改动（agent 行为）只有真实 run 能证伪；本轮就是靠真跑才发现"agent 会
  自己选落盘 + 文件卡"这条行为事实。

## 复活条件

当 `dsh`/控制面提供官方的"候选实例"启动方式（例如 `make verify-server` 或 runner 自带隔离工作区）时，
改用官方入口，删掉本 note 的手工配方。远程执行宿主接管后，隔离点从"本地目录"变成"宿主分配"，配方需重写。

## 验证

2026-09-13 本轮：同一配方在 `:8090` 上跑通候选分支，ego 完成两次真实交互——
① 旧对话文件卡点开 → 抽屉渲染 5,176 字符 Markdown；
② 新对话让 agent 交付验收条件文档 → agent 自行选择"落盘 + 带 `path` 的文件卡"，正文只留结论与待裁决，
   点开卡片抽屉渲染出新文档（9.9 KB，A1–A11 条款）。主树 `git status` 全程只有既有未跟踪文件，
   用户 8080 实例健康未受影响。
