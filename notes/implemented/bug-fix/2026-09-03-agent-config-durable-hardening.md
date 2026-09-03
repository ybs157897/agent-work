# Agent 配置 durable recovery 加固

Status: implemented

## 决策与理由

0036 的跨介质 intent 保留 SQLite 为唯一恢复权威；本轮补齐了它周围的安全边界。HTTP 幂等占位增加 owner token 与显式 `claim_expires_at` 续租，完成/释放必须携带 token 和 request hash；没有续租能力的存储直接拒绝写命令，避免长请求被误回收。非 claimer 存储直接拒绝写命令，落盘错误不再静默吞掉。

Agent bundle 对多 Workspace 使用 workspace 命名空间；安全 legacy slug 保留，危险 slug 规范化，prompt 读取只忽略真正的 NotExist。运行时 target 在 Agent CAS 提交前完成 Codex/Kimi 静态预校验，BaseURL 拒绝 userinfo、query 与 fragment。创建 Agent 也写入 intent，外部同步失败返回可被同一 HTTP 幂等键重放的 202，避免 retry 重复创建。

## 放弃了什么

- 用 `created_at` 复用 lease 心跳或启动时无条件改旧 claim：会破坏审计字段且没有 owner liveness 证明；改为显式 `claim_expires_at` + 活跃 owner 续租。
- 让根目录 legacy Agent 配置在多 Workspace 下隐式复制：它没有 workspace 身份，继续导入会造成跨 Workspace 覆盖；多 Workspace 要求显式命名空间。
- 外部同步失败后继续返回 200 或让 CreateAgent 释放 claim 后重建：前者掩盖漂移，后者会重复创建 Agent。

## 复活条件

若部署需要无等待的进程重启恢复，可再引入带进程存活证明的全局控制平面 lease；不能用一次性修改 created_at 替代 owner fencing。若未来允许带查询参数的 provider endpoint，必须先定义其 secret redaction 与 snapshot 保密存储，不能直接放宽当前 URL 校验。
