# Agent 配置 durable sync-intent 与安全边界

Status: implemented

## 决策与理由

0036 把 Agent CAS、Agent 事件和完整的非敏感目标快照放进同一个 SQLite 事务。外部效果由该 intent 驱动：先原子发布 Codex/Kimi 配置和 Agent bundle，全部成功后再把 intent 标记为 `applied`；启动和 reload 先对账未完成 intent，再允许文件导入覆盖 DB 投影。0038 进一步禁止直接 SQL 修改或删除已应用 intent 的整行。

Agent 创建也走同一 durable intent。外部同步成功返回 201；外部条件暂不可用时返回 202，并把结果留在同一个 HTTP 幂等键下，因此同 key 重试只重放原 Agent，不会重复创建。PATCH 失败保留 intent，外部错误通过重试或 reload 对账；目标漂移和不可修复的静态 target 进入 conflict，避免静默覆盖。

## 当前边界

- 单 Workspace 继续兼容根目录 `<slug>/`；多 Workspace 使用 `workspaces/<workspace_id>/<slug>/`，根目录 legacy bundle 在多 Workspace 下拒绝导入。
- Import 只忽略确实不存在的 `prompt.md`；权限、目录替代文件和其他 I/O 错误均 fail closed。
- legacy slug 在导入时要求是安全的单路径组件；危险值不进入 DB，写 bundle 时空值/危险值会规范化为名称 slug。
- Codex/Kimi target 在 CAS 提交前完成模型、协议、环境变量名和 endpoint 静态校验；Base URL 拒绝 userinfo、query 和 fragment。API key 只从进程环境读取，Kimi 配置文件使用 owner-only 权限。
- HTTP 对账/导入错误只返回稳定公共错误，不反射本地 home、Agent 配置目录或临时文件路径。
- 文件发布统一使用 owner-only 临时 inode、fsync、rename、目录同步和过期临时文件清理；bundle manifest 只记录文件摘要，不取代 SQLite intent 权威。

## 放弃了什么

- 仅记录日志并返回 200：会把 DB/文件漂移伪装成成功。
- 在 HTTP 请求中伪造 SQLite 与文件系统的跨介质 2PC：当前两者没有共同提交协议，恢复保证来自 durable intent、目标摘要和有序重放。
- 让无 workspace 身份的根目录配置在多 Workspace 下隐式复制：会产生跨 Workspace 覆盖。

## 复活条件

若未来引入带 WAL/事务提交钩子的外部配置存储，仍须以崩溃注入测试证明提交顺序，才能重新评估移除 intent。当前不宣称 SQLite 与外部文件是单一原子事务；真实 Provider、Remote Host 和运行时进程的 E2E 仍属于外部验收门。
