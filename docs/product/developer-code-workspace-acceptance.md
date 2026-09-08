# 开发 Agent Java 代码工作台验收

状态：Java 阅读集成本地验收通过，2026-09-08；用户已授权合并到 main。本记录不包含真实模型聊天验收。

## 真实聊天未验收

独立验收预览采用空 Agent/模型配置目录，并由新数据库 seed 创建演示 Forge 和 Mock 绑定。右侧属于 `record_kind=chat`，但使用 `adapter_id=mock`，没有调用真实模型。用户发送“你好”的运行 `run_01M20A9HE8FFB01V3TV5DM8XB8` 返回了 Mock 写死的“任务执行完成，产物已生成”。这不代表正常聊天能力已通过验收，也不会因 Git 合并自动切换为真实运行时。

## 版本与环境

- Agent Work 基线 `a9ef3b2`。
- web-idea 完整本地源码（原始 HEAD `2bbfb37` 及当时的未提交修改）；142 个原始文件摘要见 `integrations/web-idea/SOURCE_SNAPSHOT.json`。全部文件均已纳入，复核原项目 142 个文件的摘要没有改变。
- 独立 SQLite 数据库，按现有唯一迁移链 0001–0048 建立；未使用主树业务库。
- 独立 Git Maven Java 工程、Java 21.0.12、真实 Eclipse JDT LS。源码先经 javac 编译并运行得到 `Hello, Workbench`。
- 控制平面静态页面与 Vite preview 均走真实文件/Java Gateway；聊天运行时是 Mock。端口由操作系统分配。只启动/停止本次记录的 PID/PGID，未停止主树已有服务。

## 真实交互与权限

| 检查 | 结果 |
| --- | --- |
| 角色入口 | Nova 无代码入口且知识画布可用；Forge 可展开代码画布 |
| 快速打开 | `Application.java:6:36` 精确落到方法调用 |
| F12 | 跳到 `GreetingService.java:4:24` |
| Alt+F7 | 返回 3 条结果：声明、Application 调用、GreetingExample 调用 |
| 返回 | 恢复 `Application.java:6:36` |
| Vite WebSocket | 保留浏览器 Host，通过同源检查，真实 Java ready/F12/引用均成功 |
| 明暗主题 | 宿主 token 与 iframe 一致；项目树、Monaco、portal 引用弹层及高亮可读 |
| 窄屏 | 1024px/640px 无页面横向溢出，主侧栏可见；代码/对话切换保留 iframe 与草稿 |
| 非开发、其他 Workspace/Run、任意 root | HTTP 403 |
| 其他上游 workspace、文件 PUT | HTTP 403 |
| 路径穿越 | HTTP 400；合法 Java 文件读取 200 |
| LSP 变更命令 | `workspace/executeCommand` 返回 JSON-RPC `-32001` |
| 撤销开发 Agent | disable 200 后，已有独立 LSP socket 在实测 1914 ms 关闭；新建读取 403；enable 200 恢复 |
| 浏览器整页刷新 | 旧 Java 进程退出，旧约 41 MB 索引目录删除，新阅读会话可打开 |
| 关闭面板 | iframe 移除、Java 进程退出、对话草稿保留 |
| 数据与执行状态 | bootstrap 不含宿主绝对路径或上游 token；阅读验收阶段未创建 ExecutionRun/TaskSession，随后用户“你好”的 Mock Chat Run 单独保留 |

## 自动检查

- 工作台 `go build ./...`、`go vet ./...` 通过。
- application/httpapi 普通测试以及代码工作区、契约路由、并发关闭、释放重试、空会话惰性启动、generation 恢复等聚焦 race 检查通过。
- Supervisor 独立 race 测试通过：并发共享、私有凭据、启动取消、崩溃恢复、关闭进程组与索引目录回收。
- 工作台前端 `tsc -b`、lint、build 通过；最终全量测试 **119 个测试文件、950 条断言通过**。测试渲染器仍输出既有 React Router useLayoutEffect SSR 提示，不是测试失败。
- web-idea Gateway 全包 race、vet、build 通过；包含 client_key 并发幂等、冲突、TTL、数量限制、索引回收和 server 生命周期。
- web-idea Web 9 条回归、`tsc -b`、build、scaffold 检查通过；引用高亮最后一项样式修复另经 build 和真实明暗截图验证。
- 两份 OpenAPI 与 CI YAML 解析、`git diff --check` 通过。CI 已新增独立 Java 工作台门禁；本轮没有 push 或触发远端 CI。

## 实际边界

源码文件和 Git 状态保持不变；Eclipse `.project/.classpath/.settings` 元数据已转移到服务专属索引目录。进一步检查目录列表发现 Maven 导入仍创建空的 `target/classes` 和 `target/test-classes`，因此此版本不宣称操作系统层面的零写入沙箱。

只支持已有本机 HostRegistry 能解析的工作区；远程 Host 没有文件/语义读取通道时明确拒绝。普通用户认证仍沿用工作台现有 demo-owner 模型。

## 工件归档

原独立预览使用本机 59823 端口；为完成任务 worktree 清理，该进程已停止。它不是生产部署，旧预览地址不再作为可用入口。

原始证据及用户的“你好”记录已归档至主树工作台 `.agent-work/developer-code-workspace-acceptance-20260908/`（不进入 Git）：

- `developer-code-light.png`、`developer-code-dark.png`、`developer-code-narrow.png`。
- `java-navigation-final.json`：真实定义/引用/返回结果。
- `permissions-final.json`：只读命令拒绝及权限撤销后的连接关闭。
- `pagehide-final.json`：浏览器刷新后的进程与索引目录释放。
- `responsive-final.json`、`frontend-final-tests.log`、`backend-final-tests.log`。
- `preview.json`、`control-plane.log`：进程与启动信息。
