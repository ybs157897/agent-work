# 远程终态必达：pending 重传 + 连接级背压

Status: implemented

## 决策与理由

远程 runner 的终态投递语义选型：**runnerd 侧 pending 重传缓冲（帧先入 pending、ACK 才清除、断线重连按 runner_seq 有序重发）+ 网关侧连接级背压（发送缓冲满时不丢帧、直接重置连接）**，而不是网关侧持久化 outbox 或阻塞等待。

依据：runner_seq 去重（RunnerEventDedup）已经给出幂等消费端，至少一次投递是现成的；缺的只是「重发真的会发生」（原 runnerd 断线即进程退出，pending 机制是死代码——本次补上指数退避重连循环）与「缓冲满不吞帧」。重发必须有序：乱序重发会被 Run 状态机拒收（如 succeeded 先于 running 到达），排序按 seq 而不是入队时间。

配套取舍（同属本决策）：

- 网关缓冲满的重置适用于**全部**经 sendEnvelope 下行的帧（ack / run.command / run.offer），不限"与终态相关"的帧：丢 ack 会让 pending 不收敛，丢 run.command 丢用户意图，没有一类帧是可丢的；心跳在 writeLoop 内直写、不经过该缓冲，不受影响。
- fencing 拒收（lease 已释放/易主）的帧**仍回 ACK**：这些帧永远不可能被应用，不 ACK 会让 runner 的 pending 无限增长、每次重连空转重发。ACK 语义是"消费完毕、停止重发"，不是"成功写入"。
- runnerd RecordRunStatus 等桥接方法在发送失败时返回 error：帧仍在 pending（重连必达），但 module 层（ModuleRunner.status）必须能记日志。副作用是 recordTerminal 的兜底 RunFailed 帧可能与原终态帧同时进 pending——重放时原终态先落、兜底帧被终态状态机拒绝，属有界噪声。

## 放弃了什么

- **网关侧持久化 outbox（终态落库后重推）**：把「必达」做在网关需要事件重放与存储状态机，而 runner→网关方向已有 ACK/去重闭环，重复造一层不可靠传输。
- **缓冲满时阻塞等待（背压上抛）**：sendEnvelope 的调用方在 readLoop/dispatch 等多处，阻塞会把慢连接扩散成全局停顿；重置连接让重传机制自己消化，代价是一次重连。
- **runnerd 首连失败 log.Fatalf**：守护进程语义下改为退避重试，接受"控制平面未起时 runner 空转重连"。

## 复活条件

若未来出现跨控制平面重启的终态必达需求（runner 存活、控制面重启），pending 在 runner 内存中仍会丢——届时才需要网关/runner 任一侧的持久化 outbox，当前单控制面部署下不引入。
