# ClawTeam-OpenClaw 调研与对比（agent-team-workbench）

> 对象：https://github.com/win4r/ClawTeam-OpenClaw （HKUDS/ClawTeam 的 OpenClaw 适配 fork，~1.4k stars，Python 3.10+，MIT）
> 方法：README + 浅克隆源码静态分析（/tmp/clawteam，v0.3.0+openclaw2）。
> 日期：2026-08-24。

## 一句话定位

面向 CLI 编码 agent 的多智能体群体协调框架："你定目标，agent 群自组织分工"。**无中心守护进程**——文件系统即控制面（`~/.clawteam/` JSON + 原子写 + flock），协调靠 prompt 协议教 agent 自轮询 `clawteam` CLI。

## 核心机制速查

| 机制 | 实现 |
|---|---|
| Spawn | tmux window（Win 退 subprocess）；trap EXIT 回调 `lifecycle on-exit` → 自动 respawn（≤2 次）+ 指数退避 + 幂等键 |
| 熔断 | `agent_health.json`：healthy→degraded→open（连续失败 3 次），quality_score ±0.1/0.2 滑窗，60s 冷却半开 |
| 任务看板 | 4 态（pending/in_progress/completed/blocked）+ blocked_by DAG（DFS 环检测，前置完成自动解锁）+ locked_by 任务锁（死 agent 可抢占） |
| 通信 | 文件 inbox（默认，msg-*.json + .consumed + 死信）；可选 ZMQ PUSH/PULL（peers 文件发现 + 1s 租约心跳，失败回退文件）；Redis 仅唤醒 |
| 编排 | harness 相位机 discuss→plan→execute→verify→ship，gate 可插拔（artifact/任务全完成/人工审批=落盘 approval JSON） |
| 观测 | `board serve` stdlib HTTP；SSE 实为 2s 全量快照推式轮询；成本=agent 自报（prompt 教它 `cost report`）滚动缓存 |
| 工作区 | 每 agent 一个 git worktree（`clawteam/{team}/{agent}`），checkpoint→merge→cleanup 生命周期 |
| 多 harness | basename 匹配 + flag 方言表（权限/prompt 注入/session 捕获/resume），10 家 CLI |
| MCP | 整套控制面暴露 26 个 MCP 工具（team/task/mailbox/plan/board/cost/workspace） |

## 与我们的对比结论

功能面相似 ~70%（团队/看板/依赖/通信/审批/成本/worktree 隔离双方都有）；架构面相似 ~20%（去中心化文件态 vs 我们的中心化控制平面+adapter+SSE 事件流）。

### 值得借鉴

1. 任务锁 locked_by/locked_at + 死 owner 抢占（比我们 run lease 更贴任务粒度）
2. 熔断器 + quality_score 滑窗评分（runner 健康度轻量参考）
3. 幂等键进任务与消息维度（我们目前只在 run 维度）
4. spawn 方言表的整理法（每 CLI 一张：权限 flag/prompt 注入/session 隔离）
5. 控制面 MCP 工具化（26 工具让 agent 直接操作看板）

### 明确不借

- 文件系统控制面（吞吐/一致性天花板低，已超我们需求）
- prompt 驱动协调（agent 不自觉轮询即死锁，可靠性与模型行为绑定）
- SSE 快照轮询（我们有真事件流）

## 证据路径

spawn 后端 `clawteam/spawn/{base,tmux_backend,subprocess_backend}.py`；prompt 协议 `spawn/prompt.py:build_agent_prompt`；看板 `store/file.py:FileTaskStore`；通信 `transport/{file,p2p}.py` + `team/mailbox.py`；相位机 `harness/{phases,orchestrator,conductor}.py`；观测 `board/{server,collector}.py`；工作区 `workspace/manager.py`；适配层 `spawn/{adapters,command_validation,session_locators/}.py`。
