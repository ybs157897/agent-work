# Gateway workspace lease and idempotency

Status: implemented; local acceptance complete; uncommitted task worktree

## 决策与理由

Gateway workspace 继续只存在内存中，不新增 SQLite、Run 或第二套持久化状态。只读 workspace 在创建时固定 `expires_at`；显式 TTL 受 Gateway 可配置安全上限约束，省略 TTL 时使用有限的只读默认值。`client_key` 绑定规范化后的 root 和 `read_only`，同一 payload 重放原 workspace，payload 冲突返回 409。

过期清理由 Gateway Server 显式拥有的 sweeper 触发，所有到期路径都通过同一回调停止 jdtls、删除单 workspace 索引并解除 client key。Server.Close 先停止 sweeper，再释放内存 workspace 和 jdtls 资源，避免后台 goroutine 脱离进程生命周期。

## 放弃了什么

没有用 Get 请求续租，因为 BFF 的 `expires_at` 是创建时的固定边界，续租会让响应丢失后的 orphan workspace 无限存活。没有把租约写入数据库或 Run，因为这是 Gateway 进程内的阅读会话，不应成为控制面的第二个事实源。
