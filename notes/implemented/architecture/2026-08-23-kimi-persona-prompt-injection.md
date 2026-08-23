# kimi persona/plan 语义经 prompt 文本注入（kap 无原生通道）

Status: implemented

## 决策与理由

kimiapp 适配器把 persona（agent system_prompt）注入 **fresh 会话首个 prompt 的文本前缀**，plan 模式指令追加到**每个 plan prompt 的文本末尾**；不再经 `agent_config`/`prompt.plan_mode` 前向透传。

依据（2026-08-23 对照 kap-server 源码核实）：`POST /sessions` 的创建路由完全忽略 `agent_config`；`POST /sessions/{id}/profile` 只应用 model/permission_mode 等少数字段，`system_prompt` 在 schema 里但无任何应用逻辑；`POST /prompts` 的 `plan_mode` 接受但不应用（routes/prompts.ts 无引用）。即 kap 协议当前**不存在** session 级 system_prompt 与 prompt 级 plan_mode 的生效通道，透传是死代码。文本注入后 resume 轮靠会话上下文记忆首轮 persona（只发当轮输入的语义不变），plan 指令逐 prompt 携带以覆盖模式逐 run 变化。

## 放弃了什么

- **等 kap-server 上游补 system_prompt/plan_mode 应用逻辑**：正确形态，但跨仓库依赖不可控；且当前 adapter 已声明 `CapAdapterTranslated`，文本注入与该声明语义一致。
- **profile 路由（POST /sessions/{id}/profile）补齐 model/plan_mode**：只覆盖 creation 后的 profile 应用面，system_prompt 仍无通道；且 model 已由 prompt 级字段逐轮生效，收益为零。
- **每轮重注 persona**：防止用户中途改 agent 人设后失效，但会让 resume 轮 prompt 膨胀、语义重复；人设变更以会话轮换（config_digest 变化）重建会话为准。

## 复活条件

kap-server 支持 session 级 `system_prompt` 应用（profile 或 create 路由落地）→ 返工点：kimiapp.go `submitPrompt` 的 persona 前缀注入删除，改回原生通道，Manifest 的 `system_prompt`/`modes` 能力恢复 CapSupported。
