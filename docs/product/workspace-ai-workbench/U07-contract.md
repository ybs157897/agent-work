# U07 完整验收与旧调用链清理

2026-09-09，已验收。当前 worktree `/Users/yin/Documents/ybs/code/agent-work-u07-web-idea-acceptance`，分支 `codex/u07-web-idea-acceptance`。U01–U06 verified patch 为 staged 依赖，SHA `ff47014155e06290462ccbd0d651c11a0106e139d9a3b313cbb2550cd91822b5`。不提交、不合并、不推送。

## 收口范围

1. 完整流程复核已验证的 web-idea → 原件/代码/知识 → Chat Run AI → 单题 → 产品回填 → 变更 → 独立任务草案 → 明确发布。真实 AI 与 Mock 执行分别标注；不实施 Java 移植，不改用户 web-idea。
2. 删除已退出 UI 主路由的旧 stateless `/task-intake/analyze` provider 调用、HTTP 路由/OpenAPI 和无引用的客户端/store/旧组件。唯一需求模型入口继续是现有 Chat → Run → ModuleRunner。旧 API 直接访问应不存在，不保留兼容调用。
3. 保留 `/task-chat/*` 到现有 Chat 的重定向、旧 v1/v2 localStorage 按 Workspace 只读发现和显式文本恢复。保留原projectKey来源信息，不自动恢复旧页面级目录绑定；当前全局 WorkspaceProject 是唯一代码根。这符合历史多目录不猜拆、不静默搬迁。
4. 旧 mixed tree 原样保留；其未接线 requirement-review 原型没有已应用的 review 数据表，不把旧prototype数据库直接套入本轮新迁移。

5. 补齐新空间对内置协调器、知识助手的可配置 runtime/model 设置的一次性继承；系统身份独立、受保护提示模板按当前应用初始化，业务记录保持空白。此项是 U01 原配置继承要求的最终漏项修复，由 backend owner 处理并验证重载与不再覆盖。

## Ownership

- Backend：旧 provider/client、HTTP/server/OpenAPI 删除；后端回归。仅处理确实无消费者的 legacy 调用。
- Frontend：web 中旧 API/store/无引用页面/组件删除，保留最小历史恢复读取器；前端回归。
- Architecture：只读核验调用图、legacy恢复边界、最终跨单元验收。
- Root：验收环境、真实UI/API/DB/AI证据、文档和最终补丁；共享文件只有一个owner。

## 完成标准

旧直接模型入口不存在；历史内容不丢不自动发布；当前单题/确认/发布不回退。原件仍按核验路径交 runtime 自主读取，无固定附件分析器。验证实际模型/界面/数据库/恢复，记录运行时覆盖边界。全部单元与总清单一致，最后才勾完成。
