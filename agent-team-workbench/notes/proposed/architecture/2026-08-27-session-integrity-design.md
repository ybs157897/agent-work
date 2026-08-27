# 会话信息完整性：回放保真、渐进压缩与观测面

Status: proposed

## 决策与理由

**验收不变量**：模型层无状态，只关心请求的信息完整性（用户公理）。因此任何一层异常、任何换底、任何轮换后，工作台都必须能为模型重建信息完整的请求；**信息降级必须分档、必须记录在 `session.decision`、除预算终档外不允许悬崖**。

现状语义已经是「会话真相在工作台（`execution_runs.input` 快照 + `run_events` 台账），provider 会话只是缓存句柄」：原生 resume=缓存命中，回放=请求重建（`EffectiveInstruction` 三档，`agent-team-workbench/internal/runtime/context.go:69-105`）。本 note 补齐违反不变量的三个缺口：

1. **回放保真（turn 级轨迹 digest）**：`conversationHistory` 现在只保留用户指令与最终回复，工具轨迹/中途输入丢失（`conversation.go:52-53` 注释、`:88` 显式跳过）。改为每个 run 追加三类信息：
   - 工具轨迹 digest：从 `run_events` 的 `tool.started`（带 args）/`tool.completed` 生成「工具名 + 参数摘要 + 结果状态」，挂在该轮助手侧；
   - steering 输入：run 中途的用户输入按时间序并入该轮用户内容（撤销 `conversation.go:88` 的跳过）；
   - 审批只进事实与裁决（同意/拒绝），敏感内容维持不进（延续 `conversation.go:53` 负向保证）。
2. **渐进压缩（三档替换悬崖）**：现状超 35% 窗口预算（`conversation.go:17-23`）直接跳到 8×400 handoff（`sessions.go:22-23`）。改为三档降级，每档落 `session.decision`：
   - **full**：预算内，全量历史（现状）；
   - **digest**：超预算 → 老化压缩：最近 N 轮保留全文，更早轮次规则压缩（助手文本截关键段 + 保留工具轨迹 digest），一期不引入 LLM；
   - **handoff**：超压缩上限或触发轮换（40 轮 / 100 万 token / 72h）→ 现有 `buildHandoffSummary`。
   一期全程无状态（CreateRun 时从 `run_events` 确定性计算），不加新存储。
3. **观测面升级（本轮）**：`session.decision` data 增加 `history_tier`（full/digest/handoff）与 `history_stats`（轮数、估算 tokens），reason 族补轮换/自愈；web `chat.store.ts:247` 的 sessionMeta 行渲染轮换、自愈与压缩档位。锚点状态徽标为延伸项，不在本轮。

需求存档（范围/节奏/观测面三项拍板）：[2026-08-27-session-integrity-requirement.md](../feature/2026-08-27-session-integrity-requirement.md)

**实施顺序**：保真 + 渐进压缩同一刀（digest 参与预算计量，二者耦合）；观测面随后一刀。每刀带防回归测试（回放拼装、档位判定、decision payload 各钉断言）。

## 放弃了什么

1. **骨架优先（先做锚点状态机）**：信息完整性是公理级需求，状态机不阻塞它；本轮扩展的 `history_tier` payload 已为状态机预留接入口。
2. **一期引入 LLM 滚动摘要**：控制面今天没有直连模型的能力（一切模型调用走 harness），为单一摘要场景新增是错配；规则分档已消除悬崖。
3. **推理内容进回放**：token 体量大、各 provider 形态不一，噪声大于价值。
4. **与原生 resume 字节级对等**：provider 上下文（压缩态/前缀缓存）天然不可重建；目标定为「信息档位对等」，不承诺内容逐字节一致。

## 负向保证

- 延续：永不静默降级 fresh。
- 新增：信息降级只走 full → digest → handoff 三档，每档必须落 `session.decision`；不得引入其他悬崖。
- 延续：敏感审批内容永不进入回放。
- 新增：回放路径永不承诺与 provider 上下文等价，只承诺 ≥ 工作台台账可呈现的信息。

## 复活条件

- **锚点状态机**【首次出现「跨 run 上下文查询会话状态」的需求（运维面板 / 主动过期检测 / 会话状态卡）→ 以本轮 `history_tier` payload 为接入口 → 在 `task_sessions` 上建 ACTIVE/COMPACTED/STALE/LOST/ROTATED/HEALED 状态机】。
- **LLM 滚动摘要**【实测 digest 档信息损失造成真实任务断档（轮换后重复提问/方向漂移）→ 先落控制面直连模型能力（复用 `models/registry.yaml` 凭据链路）→ 以滚动摘要替换 digest 档规则压缩，摘要状态存 `task_sessions.session_params`】。
- **推理进回放**【任一 provider 给出结构化可裁剪的 reasoning 且实测出现「缺推理导致记忆断档」的案例 → 评估以 digest 形态纳入】。
