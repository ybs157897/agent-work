# U02 验收记录

日期：2026-09-09。结果：已完成。分支：`codex/u02-agent-knowledge`；U01 为已验收依赖，尚未提交或合入主线。

## 已交付

沿用既有 Workspace、Agent、private/workspace 可见性和知识版本模型。HTTP 与 Run 工具原有空间校验得到保留；仓储入口增加实际 Agent 存在且属于请求空间的校验，防止内部调用将另一空间的 Agent 身份与当前空间组合。`human:shared` 仍用于空间内共享视图。

知识画布和知识页显示当前空间及 Agent 归属，搜索使用通用知识文案；没有增加独立工作空间选择器，也没有跨空间复制知识。

## 实际验证

- [45 项知识 HTTP/SQLite 检查](../../review/assets/u02-agent-knowledge/api-checks.json)：私有/共享视图、同名 Agent、检索、原文及版本、来源、关系、候选写入/重试、跨空间修改拒绝和空空间均通过。
- [16 项运行凭据与实际 CLI 检查](../../review/assets/u02-agent-knowledge/runtime-checks.json)：真实创建两个空间的 Chat/Run，分别使用私有 access file 调用知识 CLI；各自读取自身私有知识，不能读取另一空间或互换 Run token。测试结束取消 Run，凭据失效且文件清理。
- [A 视图](../../review/assets/u02-agent-knowledge/ui-alpha.json)、[B 视图](../../review/assets/u02-agent-knowledge/ui-beta.json)、[切回 A](../../review/assets/u02-agent-knowledge/ui-return-alpha.json)通过实际浏览器验证；B 的普通知识页只显示 B 的共享条目。
- 仓储、application、HTTP 的 Knowledge 聚焦检查、race、build/vet 通过；前端 tsc、ESLint、21 项聚焦检查及 production build 通过。

发布知识数据由现有 KnowledgeRepo 接口构造为[合成夹具](../../review/assets/u02-agent-knowledge/knowledge.json)，没有绕过产品整理流程宣称“AI 已发布”。运行生命周期使用 Mock，知识 CLI 和 HTTP 为真实调用，未调用模型整理。仓储加固针对调用身份归属；复核未发现原有外部 HTTP/Run 入口可直接触发的跨空间泄漏。

汇总：[verification.json](../../review/assets/u02-agent-knowledge/verification.json)。后续 U03 接原附件及运行时路径，不重复建设知识库。
