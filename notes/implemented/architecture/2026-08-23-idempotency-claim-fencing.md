# 幂等键 claim-first 与 owner fencing

Status: implemented

## 决策与理由

HTTP 写命令采用 claim-first：请求先在 `idempotency_keys` 建立
`status_code IS NULL` 的执行中占位，副作用结束后再写回响应。0037 为占位增加
`claim_token` 和显式 `claim_expires_at`：活动行必须同时拥有 token/expiry，完成行清空两者，
`created_at` 只表示创建时间且不可修改。升级遗留的活动占位被明确置为已过期，并使用
RFC3339-compatible 文本，避免 SQLite 时间字符串的词典序误判。

`Claim` 返回 owner token；`Complete`、`Release` 和 `Renew` 必须同时核验 request hash、
token 与活动状态。HTTP 合同把 Renew 作为强制能力：没有续租的 Store 不执行写命令；运行中的
请求每 5 分钟续租，失去 owner 时取消执行上下文。过期的同 hash 占位只在后续 Claim 时按
`claim_expires_at` 回收，旧 owner 的晚到完成/释放会得到 `ErrIdempotencyClaimLost`。

结果最终化使用 `context.WithoutCancel` 加 10 秒上限，客户端在副作用完成后断开连接也不会
丢失幂等结果。Complete/Release/Record 错误不再被吞掉；最终化失败返回 retryable problem，
不伪造已经持久化的重放事实。没有 owner-fenced claim-first 实现的存储直接返回
`idempotency_not_durable`，不再退回 Check→exec→Record。

## 放弃了什么

- 旧的 Check→exec→Record：并发同 key 存在双执行窗口。
- 启动时无条件把所有 NULL 占位改成过期：没有旧进程已退出的证明，滚动启动会与仍存活的
  handler 并行执行；过期回收统一留在 Claim，活动 owner 用 Renew 保活。
- 用 `created_at` 兼作 lease 心跳：会破坏审计字段，也让时间格式的词典序成为隐患；lease
  生命周期只写 `claim_expires_at`。
- 把 Renew 作为可选扩展：无续租能力的实现无法证明长命令仍由原 owner 执行。
- 把幂等记录和业务副作用强行放入一个跨层长事务：当前实现仍以 owner fence、重试和
  明确的 at-least-once 崩溃语义为边界。

## 尚未覆盖

幂等占位与各类业务副作用仍不共享同一个 SQLite 事务。进程可能在副作用完成、结果未完成
落盘的窗口退出；过期后同 hash 重试是可证明的 at-least-once 语义，而不是 exactly-once。
若以后要求 exactly-once，必须把具体命令的业务写入与幂等占位放入同一权威事务，或引入
可恢复的外部 job/outbox 协议，不能只扩大 claim TTL。

## 复活条件

若需要无等待的进程重启恢复，应增加可验证的控制平面实例 lease/存活证明，再决定是否缩短
claim TTL；不能恢复启动时无条件改旧 claim 的做法。若幂等响应需要保留额外 headers，应为
响应元数据增加明确的持久化字段，不把它们塞进自由格式 result body。
