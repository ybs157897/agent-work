# U06 任务草案与显式发布验收

日期：2026-09-09。状态：已通过。

在原 Chat 里选择有效的产品确认事项、填写标题、保存草案，之后单独点击“发布任务”。新建另一个草案可以选择另一组事项，已发布任务及其审计记录保留。正文以人类结论、依据、产品版本为主，AI 背景明确注明不构成批准。

## 实际界面、接口和数据库

- 15 项实际 HTTP/SQLite 草案检查通过：有效确认选择、固定 Workspace/Chat、真实 Git HEAD、实体重放、不同内容冲突、未确认事项拒绝、禁止客户端注入目录/基线/正文/验收条件、直接 ID 跨空间拒绝。
- 保存草案前后没有新增 Task/Run。实际 UI 选择与标题经 A→B→A、刷新保留；B 不显示 A 的草案。
- 实际 UI 发布后人为丢弃 HTTP 成功响应；刷新从服务端查回同一 Task，未重复创建，发布按钮停用，可继续新建独立草案。
- 最终 JDK 范围 Task：`wi_01M22P2JB25FX6VFDHMS94X042`；其 Task context、首 Run 和恢复 Run 的 BaseRevision 均为真实 `2bbfb37ae0ba5f70c8a6092533119f239dce1458`。Run 使用新 Task 的 snapshot，没有复用 Chat snapshot ID。
- publication 记录持久保存确认 IDs、来源版本、项目基线、原分析 context 引用及 publish key/version。最终 Task 只有一条 publication。
- 在独立负向数据库修改保存原件的字节后，草案重核变为 stale；恢复原件后校验 SHA。主验收数据库和用户源码没有被该故障注入修改。
- 已发布后，在另一个隔离副本修改原件，原 publish 请求仍返回原 Task；同 key 改 expected_version 返回 409，Task 数量不增加。
- 真实临时 Git + WorkspaceProject 集成回归证明：发布后、首 Run 前发生变化时，零 dispatch/零 Run，并持久落 Coordinator/Task blocked 和原因。

## 实际执行范围

资料分析与 U05 变更复核来自真实 Kimi/DSH。发布测试将本隔离空间的协调器及全部 7 个可能执行任务的 Agent 显式配置为 Mock；其它空间和全局模型配置不变。

Mock 发出了运行事件，证明 Task/Run 与上下文接通。它没有生成 PlanDecisionV2，既有有界修复结束后 Task 进入 blocked。这不是 Java 实现或真实产物验收；Mock 产物元数据不代表文件存在。本轮没有执行 web-idea 功能开发。

## 保留的失败与修复

- 初版草案正文使用 AI item detail，已改为人类确认内容，背景单独标注。
- 初版未比较 expected_version，且未重读原件；实际负向请求曾返回 200/ready。两项已有失败文件及修复后结果。
- 初版 Task snapshot 缺失 HEAD。后续构建的新 Task/Run 已经以数据库实证通过，旧 Task 未回写，历史保留。
- 0054 已被验收数据库应用后再增加字段会被迁移器跳过；改为追加 0055 升级，不删原有记录。缺列失败没有留下半个 Task、publication 或孤立 context。
- 基线变化最初只返回错误，留下 queued；真实 Git 回归发现后补上持久 blocked 状态和事件。
- 未知基础设施错误已改为 500、可重试，HTTP 故障注入后同 key 成功且仅一个 Task；旧0054升级0055有独立回归。实际 UI 另模拟提交后代理503，字段锁定，点击“重试发布”用完全相同 key/version 取回原Task。

## 证据与检查

[证据目录](../../review/assets/u06-task-publication/)。`ui-publish-response-loss.json` 是首次丢响应用例；`ui-upgraded-publish.json`、`final-task-context.json` 是升级后最终 HEAD 用例。不同 Task 的证据不混算。`go-project-baseline.json` 直接来自正式 Go Registry.ResolveProjectBaseline，辅助 Python probe 不作实现证明。

已通过前端类型、lint、构建、133 文件/1026 项测试；后续小幅界面变更另有 focused 检查。后端 build/vet、publication focused race、真实 Git normal/drift race 和 OpenAPI route guard 通过；最终命令见证据目录 `final-gates.md`。此前全量 go test 的磁盘不足不作为通过，未重复全量 application race。

web-idea 的 HEAD、`git status --porcelain -z` 及 `git diff --binary HEAD` 摘要与 U03 前相同。所有测试产品结论均为模拟，不是用户对 Java 产品范围的批准。
