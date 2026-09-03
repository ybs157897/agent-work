# Governance control receipts and delegated Handoff fencing

Status: implemented

## 决策与理由

Control receipt 的 phase 7 由调用方在 Coordinator state CAS 后追加；admission helper 只负责 phase 1–6。这样 projection 的 immutable snapshot 必然包含本次 repair/replan/user-action 对控制线的可见状态，而不会在状态写入前冻结旧 projection。任何 governed source Run 在新 control Turn admission 前先按原 Turn 的冻结 quota/evidence 结算；旧 reservation 仍处于 reserved 时拒绝推进新 Turn，避免遗留预算遮蔽新 Turn 的准入判断。

Delegated Handoff 的过期不等于 ownership 消失：只要 owner 与 claim generation 仍逐字匹配，恢复面返回该 Handoff 并用同代 CAS 续租；若 claim 已被释放或被其他 Agent 接管，则回报冲突。Todo same-generation renewal 保留初次 `claimed_at`，只延长 `expires_at`，并由应用 service 与 SQLite trigger 共同保证。

Target accept 是 source decision 的 fencing 边界：记录在 checkpoint 中的 source Run 后续即使成功返回 PlanDecision 或 evaluation verdict，也只保留为 Run evidence，不再提交 Plan 或推进验收。source snapshot 只能来自 state 当前的受保护 Coordinator Run，不从同 root/同 Agent 的历史 Run 猜测；没有当前 Run 就明确 fresh。已转移 target 可再次 Handoff，但必须用上一跳精确 Handoff/claim generation 原子替换 checkpoint。

已持久但仍指向 system Coordinator 的 settlement wake 在消费时重解析当前 transferred Handoff，改派精确 target 并携带 Handoff/claim-generation proof，不会与 target continuation 双跑。

阻塞会释放 Todo claim，但同时清除 Coordinator 的 Handoff continuation checkpoint，保留 transferred Handoff 作为历史事实并在 blocker/state event 标注清理。Unblock 因而明确回到系统 Coordinator，而不是拿着已失效 target checkpoint 永久卡死或静默误派。

## 放弃了什么

- 在控制收据 helper 内直接追加 phase 7：调用方的 Coordinator state 仍未落库，projection 会冻结错误的控制线视图。
- 以“claim 已过期”直接丢弃 transferred Handoff：会把同 owner/generation 的可恢复 continuation 错误降级成 system Coordinator。
- 在 Handoff accept 后仍消费 source 的迟到 PlanDecision：会与 target continuation 形成双执行；accept 后 source 只能作 evidence。
- 从“同 root 下 source Agent 的最新 Run”推测 Handoff snapshot：多轮/二次转交时可能克隆无关上下文；只允许 state 当前受保护 Run。
- 按 wake 创建时的 system target 永久路由：持久 wake 与后来的 ownership transfer 会分歧；消费边界必须重读当前权威。
- blocker 后保留 Handoff checkpoint、只释放 claim：Unblock 无法满足 delegated claim proof，只会循环报 stale；清除 checkpoint 比伪造新的 target claim 更诚实。
- renewal 重写 `claimed_at` 或允许缩短 `expires_at`：会把续租伪装成新的 ownership，并允许直接 SQL 缩短有效期破坏恢复窗口。

## 复活条件

若未来允许 blocker 后自动继续原 delegated target，必须新增显式、持久的“blocker 后重新确认 Handoff”命令，并在同一事务重建新 claim generation 与 continuation checkpoint；在此之前只能按当前决策清除 checkpoint、由 Unblock 走 system Coordinator。
