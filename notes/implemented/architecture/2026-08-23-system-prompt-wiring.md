# system_prompt 接线：BuildInput 注入 + 配置指纹漂移

Status: implemented

## 决策与理由

agent.Instructions（agents/&lt;slug&gt;/prompt.md 为真相源）经 `orchestrator.BuildInput` 固化进 `run.Input["system_prompt"]`，五家 adapter 经 `runtime.SystemPromptOf` 消费；注入发生在 `CreateRun` 内 `ConfigDigest` 计算之前（BuildInput 先于 digest，见 internal/application/runs.go CreateRun 组装顺序）。**提示词即配置**：提示词变更 → config digest 漂移 → task_sessions 指纹失配 → 旧 provider 会话被丢弃（fresh，经 tier-3 全量历史内联保持任务连续性），旧提示词不会残留进续接会话。

接线本体随 1bc19b6（paperclip 编排迁移）落地；本任务核实其在位后补集成级回归钉（internal/application/runs_integration_test.go），不再另设注入点。

## 放弃了什么

- **把提示词拼进 instruction 文本**：污染 tier-3 历史回放（每轮重复全文）与 provider 缓存前缀（instruction 逐轮变化导致前缀缓存全失效）。
- **system_prompt 置于 digest 稳定键之外**：配置变更不触发指纹漂移 → 旧会话被续接。kimi `--agent-file` 首轮绑定语义下，旧提示词将残留在整个 provider 会话生命周期里。
- **在 CreateRun 内于 BuildInput 之后再写一次 system_prompt**（本 note 对应任务的原始提案）：与 BuildInput 双写同值，注释会成为伪真相（声称此处是注入点/轮换触发点，实际语义在 resolveResume）；空值与纯空白值的边角行为也不会因此变对。拒绝重复注入。
- **指纹漂移标记 session_rotation=true + handoff 摘要**（任务原始预期）：现语义中 session_rotation 专指阈值轮换档（resolveResume 对漂移返回 plain fresh）。漂移 fresh 已携带全量历史，连续性有保障；把漂移升级为 handoff 档属独立取舍，不借本任务夹带。

## 负向保证与路径语义

- **retry**：`cloneInput(parent.Input)` 快照——重试 run 沿用父 run 创建时的提示词，不随 agent 后续修改而变（重试 = 重新执行同一输入）。
- **resume(lost)**：重走 CreateRun，读取 agent 当前 Instructions 并重算 digest——重执行采用当前配置；若提示词已变，指纹失配自动落 fresh，不会用旧会话承载新提示词。
- 两条路径对 system_prompt 均零感知、无需改动。

## 观察项（未处理）

BuildInput 以 `Instructions != ""` 原文判断，不 TrimSpace：纯空白 Instructions 会注入空白提示词并参与 digest。现有写入路径（web/CLI 编辑）不会产生该值，留观。
