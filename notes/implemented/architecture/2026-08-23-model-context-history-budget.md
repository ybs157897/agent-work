# 历史回放预算按模型上下文窗口推导，超限轮换而非截断

Status: implemented

## 决策与理由

废掉 tier-3 内联历史回放的固定 48KB 字节上限（`maxConversationHistoryBytes`），
改为 token 预算：`models/` 注册表 `context_window` 的 35%（缺窗口时回退 32768），
由 CJK 一字一 token、其余四字符一 token 的粗估驱动。超预算的处置从
「头部截断」（`trimRecentHistory`）升级为「会话轮换」：复用既有 rotation 通道，
携带 handoff 摘要开新会话。

理由：48KB 是迁移期冻结的魔法数，与任何模型能力无关（现代窗口 128K–1M token，
48KB 文本仅约 1.5–3 万 token）；且无状态 provider（DeepSeek 等）的前缀缓存只认
字节级一致前缀，头部截断使每轮请求前缀整体移动、缓存持续清零——恰好发生在
对话变长（缓存最值钱）之后。轮换只付一次性新前缀成本，之后恢复纯追加。

预算判定只作用于 tier-3（resume 不可用/无锚点）内联档；tier-1（resume 命中）
上下文由 harness 持有，其增长归锚点阈值（40 轮/1M token/72h）管辖。

## 放弃了什么

- **保留固定字节上限**：与模型窗口脱钩，单位（字节 vs token）也是错的。
- **头部截断（原 trimRecentHistory）**：每轮移动请求前缀，是最差的缓存形态；
  作为预算超限的处置方式被整体删除。
- **统一「[用户当前消息]」标签为「[用户]」以追求逐轮严格前缀扩展**：分析后
  放弃——相邻两轮请求的共同前缀止于上一轮已定局历史末尾（本轮用户消息的
  回复必然插在下一轮历史之前），标签统一并不扩大共同前缀，反而丢失「当前
  消息」的语义标记。字节稳定契约以测试钉在历史区（见
  `TestEffectiveInstructionHistoryRegionByteStable`）。

## 复活条件

若未来需要精确 token 计量（粗估误判轮换时机），复活点在
`estimateTokens`——替换为 provider usage 回报驱动的计量，接口不变。
