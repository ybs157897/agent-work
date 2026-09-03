# Run Journal D2：远程 runner 相位帧语义

Status: implemented（分支 codex/run-journal-m1，W6）

## 决策与理由

D2（runner 协议 v2 相位帧）落地时测绘发现：runnerd 侧 `moduleEngine.RecordRunEvent`
对 internal 事件本无过滤，`run.phase_entered/phase_closed` 帧其实已在上线；真正的断点
有三个，本期分别收口：

1. **log_chunk 上线（带宽违约）**：runnerd 把 `run.log_chunk` 原样发上线并落库。
   D3 的 64KB 预算是进程内落库纪律，不是带宽预算；远程路径本期明确
   **log_chunk 不上线**——`moduleEngine.RecordRunEvent` 只放行
   `run.phase_entered/run.phase_closed` 两类 internal 帧，其余静默丢弃
   （返回 nil 非错误：观测面缺帧不打断业务路径）。
2. **settle 闭包被 stale 收走**：ModuleRunner 的 `phase_closed{settle}` 在终态
   status 帧之后发出（recordTerminal：先 status 后 closeSettle），而控制面应用
   终态时同事务释放 lease、网关清 activeRuns 镜像——迟到相位帧被 ackStale，
   **远程 run 的 journal 全部以未闭合 settle 收尾**（假"崩溃"信号，违反
   「未闭合即故障点」的定位语义）。处理：把既有「终态观测例外」（原
   usage.updated 专用）扩面到两类相位帧——网关 `lateUsageTransportValid` 泛化为
   `lateObservationTransportValid`，应用层 `releasedLeaseUsageAllowed` 泛化为
   `releasedLeaseObservationAllowed`。**围栏不松**：仍要求 lease_id/run/runner/
   fencing 四要素逐字匹配 + 租约确已释放 + Run 已终态，错 fencing/错 runner/
   未知 lease 的帧一律 stale；message.* 等 surface 帧不享受例外。
3. **Notify 语义对齐**：ApplyRunnerEvent 对每条 applied 帧无条件唤醒 workspace
   notifier；进程内路径 `RecordRunEvent` 对 internal 类跳过 Notify。远程相位帧
   补齐同一闸门——internal 帧不触发 SSE 唤醒。

dedup 无需改动：`RunnerEventDedupV2`（key=(run_id, lease_id, runner_id,
producer_seq)）对相位帧与 surface 帧同一语义，同帧重放得 duplicate ACK、不重复落库。
schema 零结构改动：`RunEventPayload.properties.event` 本是开放对象（kind 无枚举），
仅在描述里补充相位帧语义说明。

## 放弃了什么

- log_chunk 远程上线：带宽不可控（stderr 原始输出无上限），M2 不做；待 M3 日志
  查询面定需求后再议分片/采样方案。
- `run.decision` 经 runner 转发：decision 是控制面产生的因果事件，runner 侧无生产点。
- 迟到观测例外扩到 surface 帧：surface 事件进对话投影，迟到帧会污染终态后的回放，
  维持 stale。
- runnerd 终态后 lease framing 回收的窄窗口竞态（status 帧 ACK 恰在
  closeSettle 入 pending 前被处理 → lease/seq 被回收 → 相位帧发不出）：
  需要重构终态清理时序才可消除，真实网络 RTT 下不可达；失败模式是优雅降级
  （单条相位闭包缺失，模块层记日志），留待 runnerd 终态管理下一次改动时一并处理。

## 复活条件

- 远程 run 需要 stderr 证据（log_chunk 上线）时：需先定带宽预算与截断协议
  （帧级上限 + run 级环形窗口），不能照搬进程内 64KB 预算。
- settle 闭包竞态在真实环境出现时：把 runnerd 的 terminalPending 清理改为
  "终态后静止窗口"或由控制面显式关闭 run 上下文。
