# Runner 接单登记与 ACK 完整身份围栏

Status: implemented

## 决策与理由

Runner 在发送 `run.accept` 前先登记本地 Run、lease、控制 channel 与审批 channel。控制面观察到接单后即可立即下发命令，因此接单帧是“命令围栏已经可用”的承诺，不能早于本地登记。

事件 ACK 以 `(run_id, lease_id, producer_seq)` 定位 pending 帧后，仍逐字核对 `runner_id`、`fencing_token` 与 `event_id`；任一不匹配均保留 pending 等待合法 ACK。这样旧 lease、错误事件或伪造确认不能破坏至少一次投递。

DSH supervisor 同时改为启动后立即缓存 `pgid`，关闭时只使用缓存值发送组级信号。组长被收尸后不再现场调用 `Getpgid`，避免孤儿进程逃逸。

## 放弃了什么

- 仅依赖 WebSocket 单读循环“通常不会并发”：这不是协议保证，也无法覆盖未来读取/命令处理并发化。
- 只按三元 key 删除 pending：key 能定位记录，但不能证明 ACK 对应同一 runner、fence 和 event。
- 关闭时重新查询进程组：组长退出后查询会失败，无法回收仍存活的同组子进程。

## 复活条件

只有 Runner 协议升级为带加密认证的单调累计 ACK，且形式化证明累计水位同时绑定 runner、lease、fencing 与事件序列时，才可重新评估逐事件身份核对；迁移必须保留旧帧的 fail-closed 行为。
