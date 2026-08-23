# resume 探测失败永不静默降级 fresh

Status: implemented

## 决策与理由

立负向保证：**adapter 在 resume 探测失败（会话不存在）时必须返回 `Failure{Family: session_unknown, Retryable: false}`，绝不就地降级开 fresh 会话。** dsh（gateway.go resolveSession）与 kimiapp 均按此实现；应用层 maybeSelfHeal 统一清锚点 + fresh 重建一轮（此时 EffectiveInstruction 走 tier-3 全量内联）。

依据：instruction 在 ModuleRunner.Dispatch 时已按「有 resume ref → 只发当轮」选定，adapter 内部降级 fresh 意味着模型只收到当轮指令=静默失忆——这正是 dsh 跨轮失忆根因（见评审记录）的同构形态。降级发生在错误的层：只有应用层能重建指令。

## 放弃了什么

- **adapter 内直接降级 + 事件告警**：用户少一次失败往返，但丢上下文是隐性数据损失，事件告警没人看；宁可要一次显性的 failed→自愈重试。
- **adapter 自己重建全量指令**：需要把会话历史可见性下沉到 adapter，破坏「控制平面是唯一持久层」的分层。

## 复活条件

无（负向保证）。若未来出现「会话可有可无」类 adapter（如无状态翻译器），应在 Manifest 能力里显式声明 resume=unavailable，让 CreateRun 不注入 resume ref（已有能力门控），而不是绕过本保证。
