# U07 web-idea 完整验收

日期：2026-09-09。状态：已通过。

统一工作空间、原件路径读取、单题问答、产品回填、变更复核及任务发布已经接通。最后一次真实 AI 运行使用当前最终代码，经现有 Chat → Run → ModuleRunner 完成读取并形成分析 revision 3；没有独立需求模型调用链。

## 最终真实运行

Run `run_01M22RPGQ4N8BM7NQPF5ZB5BK5`，Kimi/DSH，10 次实际工具调用。只在新 Markdown 原件中放入随机校验值，Run 输入没有该值；工具结果和 AI 报告均正确读回，证明运行时实际按路径读取了原件。

| 材料 | 已核验证据 | 覆盖边界 |
| --- | --- | --- |
| Word | `word/document.xml`、页眉和页脚原文；SRC-DOCX-01 标记 | 文本读取；未验版式和图形 |
| PowerPoint | 两页 slide 与两页 speaker notes；SRC-PPTX-01 标记 | 文本和备注；未验版式和图形 |
| Markdown | 原需求、变更说明、新路径校验件三份实际 read 调用 | 校验值不在输入，只存在原文件 |
| web-idea 代码 | session.ts 的读取片段、client.ts 的 writeFile 片段；两个文件整文件 SHA256 | 实际 read 为 limit=60 及 offset=175/limit=30，未把它写成这次全文阅读 |
| Agent 知识 | 当前 Run 授权工具按 ID 读取知识版本 1，摘要匹配 | ask 模式在本测试 Mock 知识助手配置下校验失败；随后直接 read 成功，不宣称 ask 成功 |

12 个业务事项、4 个问题与 revision 2 精确保留；3 个已回填产品结论仍有效；原已答/暂缓问题继续通过 lineage 保留。新校验资料没有变成业务要求、问题或产品批准。真实浏览器中只有 1 个当前需求问题。

## 跨单元核验

- U01：唯一全局工作空间、A/B 回切、未发送输入与历史页面恢复；新空间独立 Agent 和业务内容。
- U02：知识查询、版本、关系、直接 ID 与真实 CLI 访问遵守 Workspace/Agent 边界。
- U03：上传原件、Run 固化引用、运行时自行按路径读取；上传不等于已读。真实 Office、损坏文件、续聊新原件与排队恢复证据保留。
- U04：现有 Chat 中形成严格分析版本，一次一问，提交/暂缓/响应丢失恢复；普通答案不构成产品确认。
- U05：明确回填结论/依据/版本，保留历史；实际需求变化只重开相关确认，无关确认保留；错误 LSP 推断已经按真实代码纠正。
- U06：从有效确认形成独立草案并明确发布；Task/context/publication 原子写入，真实 HEAD 固化到 Task/首 Run；来源/代码变更拒绝旧草案；503 丢响应以原 key/原载荷取回同 Task；首次执行前漂移落持久 blocked。

每单元独立验证的详细证据均在对应验收文档。本轮最终回归没有把 Mock 产物当成 Java 实现：Task 执行测试明确使用 Mock，因其没有 PlanDecisionV2，既有修复流程最终进入 blocked。Task 发布与首 Run 上下文已验证，实际 Java 开发不在本轮范围。

## 新空间配置的最后补验

普通 Agent 之外，内置 Coordinator 的 primary/fallback runtime、model 和 reasoning，以及知识助手的 runtime/model、允许的 enabled/auto_collect 设置，都一次性继承源空间。目标系统身份保持独立，使用当前保护模板，业务表为空。真实 SQLite 回归验证了非默认设置、重复 Ensure 与新 Service reload 后保持，源空间后续不覆盖目标。

## 旧流程收口

删除旧 stateless task-intake provider、HTTP/OpenAPI、客户端、store、旧页面和死组件。实际 POST `/workspaces/{ws}/task-intake/analyze` 返回 404，路由/OpenAPI 对账保持一致。

`/task-chat/*` 仍转到现有 Chat。旧 v1/v2 内容按原 Workspace 只读发现，展示旧标题与 projectKey；“恢复文字”只把历史内容放入当前 Chat 输入，保留原桶，不自动发布、不切换代码目录。恢复后的文字包含历史项目来源说明。实际 UI 证明另一 Workspace 的内容不出现，恢复没有创建 Run 或业务任务。QA 生成的临时浏览器桶在验证后恢复原值，产品恢复动作本身没有删除历史。

旧 mixed tree 与原预览保留；其 requirement review 原型没有已应用的数据表，不假设存在可自动迁移的业务确认。

## 证据与限制

[最终证据](../../review/assets/u07-web-idea-acceptance/)包含真实工具事件、AI 原始输出、接受后的分析、nonce 核验、旧路由 404、旧草案恢复截图、单题截图以及 web-idea 前后摘要。

使用隔离数据库与测试产品结论，当前应用身份仍为 `user_demo`；没有声称多用户认证、生产数据库升级或真实 Java 交付。Office 图形/版式、完整 application race 均未作为已通过项目。用户 web-idea 的 HEAD、工作树状态和 diff 摘要与 U03 开始前一致。

最终门禁：[final-gates.md](../../review/assets/u07-web-idea-acceptance/final-gates.md)。前端 129 文件/1004 测试、后端 build/vet/触面 race、真实 AI 与 UI/数据库验收通过。验收阶段的任务 worktree 和预览继续保留。用户随后授权提交并合入本地 main，具体合并状态见 Git 记录；未授权推送。主树的 AGENTS.md 外部修改和既有 index.js 均未纳入交付。
