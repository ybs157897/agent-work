# CLI 提示词工程参考：Codex 与 Kimi Code 的输出纪律与堆叠范式

> 2026-08-28 三路调研收口（Codex CLI 0.149.0 二进制提取 + GitHub main 对照；Kimi Code CLI v0.38.0
> 二进制提取 + 本机源码克隆核对）。用途：为 workbench 的 agent 提示词与 languagegui/v1
> 输出契约设计提供业界对照。原文资产在 `prompt-library/` 子目录。

## 1. 资产清单与出处

### Codex（openai/codex，Apache-2.0）

| 文件 | 出处 | 说明 |
|---|---|---|
| `prompt-library/codex/base-instructions-generic.actual.md` | **本机 rollout 实证**（`.agent-work/codex/sessions/.../session_meta.base_instructions`） | ★ 最高保真件：我们生产 run（含 kimi/deepseek 中转）实际收到的通用底座提示词 |
| `prompt-library/codex/gpt_5_2_prompt.upstream.md` | GitHub main `codex-rs/core/gpt_5_2_prompt.md`（与本地二进制逐字 1 行差） | GPT-5.2 家族：篇幅三档硬限额所在 |
| `prompt-library/codex/gpt_5_codex_prompt.upstream.md` | GitHub main `codex-rs/core/gpt_5_codex_prompt.md` | gpt-5-codex 短版底座 |
| `prompt-library/codex/collab-family-extracted.md` | 本地二进制 instructions_template | 协作家族（桌面/云）：50–70 行总硬顶所在 |
| `prompt-library/codex/cloud-desktop-family-extracted.md` | 本地二进制（上游无对应文件，疑随模型元数据下发） | 含 Personality/Writing style/Visualizations 全章 |
| `prompt-library/codex/collab-agent.experimental.md` | 上游 `templates/collab/experimental_prompt.md` | 多智能体协作提示词 |

### Kimi Code（MoonshotAI/kimi-code，MIT）

| 文件 | 出处 | 说明 |
|---|---|---|
| `prompt-library/kimi-code/system.agent.md` | 本机 v0.38.0 会话落盘 wire.jsonl 完整渲染版 | 主 profile 渲染产物 |
| `prompt-library/kimi-code/system.coder.md` | 同上 | coder 子代理（含交接完整性要求） |
| `prompt-library/kimi-code/system.explore.md` | 同上 | explore 只读子代理 |
| `prompt-library/kimi-code/system.expert-*.md` | 二进制内嵌模板提取 | expert teams 系列（架构师/产品经理/评审组长） |

源码克隆：`~/Documents/ybs/code/kimi-code-2`（fork，`agent/v2-upgrade`，可对照
`packages/agent-core-v2`）。注意本机克隆比 v0.38.0 二进制旧，机制一致、文案有漂移。

## 2. 提示词堆叠的两种范式

| | Codex（消息层叠加） | Kimi Code（模板槽位） |
|---|---|---|
| 形态 | base_instructions + developer 消息 + user 消息**分层并存** | **一条完整渲染的 system 字符串**，槽位一次填充 |
| 宿主定制口 | app-server `developerInstructions`（消息层追加） | 无"传 prompt 拼接"通道：槽位替换（productName/replyStyleGuide）/整条替换（SYSTEM.md、--agent-file，可 `${base_prompt}` 嵌回包裹）/插件追加段（64KB 预算、标注非特权） |
| wire 层 | Responses `instructions` + 消息序列 | 单条 system（Anthropic `system` / Responses `instructions`） |
| 对我们的含义 | 我们的 system_prompt 以 developer 角色追加，codex 底座恒在 | 若直接以 kimi-code 为 harness，只能整条替换 + `${base_prompt}` 包裹，或仅占风格槽位 |

## 3. 输出内容形态纪律对照（长结果可读性相关条款）

| 纪律 | Codex | Kimi Code |
|---|---|---|
| 篇幅总量 | 通用底座软约束「≤10 行默认，可因复杂度放宽」；GPT-5.2 三档硬限额（小改 2–5 句/中改 ≤6 bullets/大改每文件 1–2 bullets）；协作家族 50–70 行硬顶 + ≤2–3 小节 | "Never give the user more than what they want"（总则软约束） |
| 裁剪算法 | "回答开始变成 changelog 就压缩：先砍逐文件细节/重复框架/低信号复述，最后才砍结论/验证/风险" | compaction 笔记"篇幅与任务成比例，结构随任务走，不强加固定章节" |
| 结论先行 | "Lead with the outcome"；final 必须自包含（过程播报会被折叠） | 子代理 final message=全部交接物，minChars=200 硬打回 |
| Markdown 结构 | 标题 1–3 词 `**Title Case**` 非必需；bullet 扁平每组 4–6 条按重要性排序、一条一行、禁嵌套；结构量=任务复杂度 | reply_style_guide：轻量 Markdown、默认散文、禁深嵌套/**大表格**/重标题、禁 emoji |
| 代码瘦身 | 禁 before/after 对、完整方法体、大代码块；已写文件只引用路径 | 禁占位符交付（`// ... rest unchanged`） |
| 文件引用 | 行内代码 + 起始行号、禁行范围、禁 URI（协作家族用 markdown 链接） | 统一 `path/to/file.ts:42`（可被 UI 识别跳转） |
| 可视化门槛 | "只有重要关系难以线性表达才上图表，选最小形态"（协作家族） | —（由 reply_style_guide 的禁大表格兜底） |
| 过程旁白 | 30s 一次 1–2 句；句式不许重复开头 | 工具调用前 8–10 词一句话；阶段切换才加一行，保持稀疏 |
| 语气 | 禁「Done—/Got it」式开场白；禁自我表扬对比；禁内部黑话 | "seasoned engineer, not a cheerleader"；禁吹捧 filler |
| 风格可配置 | 否（按模型家族换模板） | **是**：`reply_style_guide` 是宿主可覆盖槽位（hostIdentity） |

## 4. 我们三条 adapter 路径的提示词位置（「会不会把底座干成空壳」实测结论）

| 路径 | system_prompt 通道 | 底座是否保留 | 约束力位置 |
|---|---|---|---|
| codexapp（kimi/deepseek 皆走此） | thread/start `developerInstructions`（codexapp.go） | ✅ codex 21KB base_instructions 恒注入（rollout 实证），我们叠加在后 | developer 角色消息——强 |
| kimi（kimi-code CLI headless） | `--agent-file` 首轮绑定，system.md = `${base_prompt}` + 我们的提示词（kimi.go createAgentFile） | ✅ `${base_prompt}` 包裹嵌回，kimi 底座完整保留 | 模板层 system——最强 |
| kimiapp（kap app-server） | **服务端无 system_prompt 通道**（创建路由忽略 agent_config） | ✅ kap 默认底座原样保留 | ⚠️ 人设只能文本注入 fresh 会话首个用户消息（submitPrompt 前缀），是最弱位置 |

**结论：三条路径都没有把 harness 干成空壳子。** kimi/codex 的底座纪律始终在生效——这也解释了
为什么底座有格式纪律而长报告仍失控：通用底座对长任务是软约束（"可因复杂度放宽"），
真正强硬的三档限额只在 GPT-5.2 家族模板里，kimi/deepseek 吃不到。

kap 路径的人设注入位置（用户消息前缀）是三路径中最弱的，若该路径走向生产，
应考虑推动 kap 侧支持 system_prompt 通道，或接受其作为"会话级约定"的弱约束力。

## 5. 对 languagegui/v1 契约补丁的设计建议

1. **补"长回答组织纪律"一节**（契约现在只声明块的存在）：结论先行（首段 TL;DR）；
   超量内容强制落块（评审→review-summary、数据表→table 块、禁 markdown 大表格）；
   markdown 正文只做串联解释。
2. **结构克制规则直接可抄**：标题 1–3 词、bullet 扁平每组 ≤6 条、禁嵌套、
   禁 before/after 代码对（Codex 原文，见资产库）。
3. **纪律做成可覆盖槽位**（学 kimi-code reply_style_guide）：契约为默认值，
   agent.yaml 留按角色覆盖的口子（如 PM 允许长报告、reviewer 强制 findings 先行）。
4. **软规则 + 硬机制**（学 kimi-code minChars 打回）：长报告场景可在渲染层兜底
   （长文折叠/渐进披露），不依赖模型遵从率。
