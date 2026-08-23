# Runner lease 续租复用心跳通道

Status: implemented

## 决策与理由

runner 执行的 run 租约（leaseTTL=60s）续租复用 runnerd 已有的 15s 周期 heartbeat 消息：网关 `handleMessage` 的 heartbeat 分支调用 `RenewLeasesByRunner` 推进该 runner 名下全部活跃 lease 的 `renewed_until`，并顺带回收终态 run 的残留 lease。

依据：心跳即存活证据，15s 间隔远小于 TTL（60s），续租由存活驱动而非 lease 新鲜度驱动，语义比「按 lease 快到期才续」更稳；且零协议改动——welcome 里广告的 `renew_interval_seconds: 20` 终于有了兑现方。

## 放弃了什么

- **新增专用 renew 消息类型**：协议更精确（按 lease 逐个续），但要动 runner 契约+双侧实现，收益不抵成本。
- **zombie 判定改用 runner 连接活性**：能根治「lease 过期即误判」类问题，但进程内模块 run 没有连接概念，需要两套判定并存，判定面更复杂。当前 lease 方案对进程内 run 天然兼容（无 lease 即 alive）。

## 复活条件

runnerd 心跳间隔硬编码 15s、未消费 welcome 下发的协商参数。若服务端将 heartbeat 间隔调至 ≥ TTL/2（30s）→ 续租静默失效 → 返工点：`cmd/runnerd/main.go` heartbeatLoop 采纳协商间隔；预埋要求：`heartbeat_interval_seconds` 与 `lease_policy` 已在 welcome 帧结构中。
