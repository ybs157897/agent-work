# 待答问题期间不得把 run 投影成已停

Status: implemented

## 决策与理由

线上现象：PM agent 用 `AskUserQuestion` 提问后，UI 既没渲染可点的提问卡、又把在途工具行
渲染成「已中断」，用户无法作答；随后 appserver 一直静默（它在等人），10 分钟后 adapter 的
idle 看门狗判 `idle_timeout` 把 run 打死（`internal/runtime/adapters/kimiapp/kimiapp.go`
的 `IdleTimeout` 默认 10m）。整条因果链是"UI 假报中断 → 答不了 → 看门狗判死"。

**看门狗保留**（用户定调）：正常流程里用户在窗口内答完就不会触发，它仍是"用户始终不答"的
兜底。修复收在 UI 层两处：`web/src/pages/chat.page.tsx` 的提问卡不再被 run 状态快照二次
门控（`questions.store` 是权威，run 终态事件会自行清空），并把"有待答问题的 run"一律按
运行中投影给工作过程时间线——否则在途工具行会被 `stoppedRuns` 投影成「已中断」，用户据此
以为流程断了。

## 放弃了什么

- **改看门狗**（提问挂起期间重新计时）：用户明确要求保留；且会让"用户永不回答"的 run 无限
  悬挂，得再补总上界，复杂度不划算。
- **在时间线里特判 AskUserQuestion 工具名**：把工具名硬编码进渲染层，其他会阻塞等人的工具
  （审批）仍会踩同一个坑；按"待答问题"这一领域信号投影更普适。

## 复活条件

【若将来允许"提问等待不计入 idle"（例如无人值守场景）→ 在 `kimiapp.go`/`dsh/gateway.go`
的 idle 分支按 pending question/approval 重新计时 → 预埋要求：必须有总上界，且 UI 侧
"questions store 为权威"的口径保持不变】
