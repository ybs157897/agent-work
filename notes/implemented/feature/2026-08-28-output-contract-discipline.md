# languagegui/v1 契约硬规则化：长回答组织纪律

Status: implemented

任务分支 `zcode/output-contract-discipline`（42fb973）。前序调研归档：
`agent-team-workbench-docs/references/cli-prompt-engineering.md`（支线 zcode/cli-prompt-references）。

## 背景与诊断

长任务结果（如项目 review）输出为整面 markdown 长文、结构化块闲置。调研确认根因链：

1. 契约原文 "Only when structured data is materially clearer" 是软建议，块的采用判定
   整个推给模型自由裁量；
2. codex 底座的篇幅纪律是为 GPT-5.x 家族对齐的（kimi/deepseek 走通用底座只吃到
   软性版本），且 CLI 语境的 ≤10 行默认值在长任务下被模型合理放宽；
3. kimiapp(kap) 路径契约随 system_prompt 走首条用户消息前缀（最弱位置）；
4. 无违规校验回路。

## 本刀范围

只动契约文本（`internal/orchestrator/output_contract.go` 的 languageGUIV1Prompt）——
对所有 adapter 路径立即生效，零接线成本。新增 "Answer organization" 强制节
（限定 substantive answers：报告/评审/计划/分析或超 ~15 行，闲聊不受影响）：

- 结论先行：首段直接给结论，禁过程叙述开头；
- 场景触发强制块：评审必出 review-summary（findings 按严重度排序带 file:line，
  正文不复列）；表格数据必走 table 块、禁 markdown 表格；关键数字必走 metric 块；
- 结构克制：短段落、扁平 bullet（禁嵌套、每组 ≤6）、短标题（1–3 词）、
  禁 before/after 代码对、禁贴大文件、`path/to/file.ts:42` 引用；
- 块文不重复：块承载细节、散文承载推理。

测试钉进 `TestApplyOutputContractKeepsInstructionAndSystemPromptStable` 的 fragment
清单（硬规则关键句 + 原有白名单/安全约束不丢）。

## 有意不做（后续层）

- **渲染层长文折叠**（治本兜底，不依赖模型遵从率）：长 assistant 回合渐进披露，
  另案。
- **违规校验回路**（对标 kimi-code minChars 打回）：检测"该出块没出块"并重试，
  另案。
- **按角色覆盖槽位**（学 kimi-code replyStyleGuide/hostIdentity）：契约纪律目前是
  全局默认；agent.yaml 级覆盖口子另案。kap 路径升级到模板层需 kimi-code fork
  透传 hostIdentity（该 fork 在本机 ~/Documents/ybs/code/kimi-code-2），另案。

## 验证

`go build ./... && go vet`、`gofmt` 干净、`go test -race ./internal/orchestrator/
./internal/application/` 全绿。提示词改动属统计性改善，真实遵从率提升幅度需在
生产对话中观察校准（评审类提示是否开始稳定产出 review-summary 块）。
