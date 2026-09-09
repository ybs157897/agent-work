# U07 旧调用链与历史恢复最终只读复核

日期：2026-09-09。当前 U07 worktree 清理后复核；未修改产品、数据库或用户端口。

## 清理已完成

当前文件系统确认以下旧 provider/API/page/store 均已删除：

- `internal/taskintake/`
- `internal/httpapi/handlers_task_intake.go` 及其旧测试
- `web/src/api/task-intake.*`
- `web/src/stores/task-intake.store.*`
- `web/src/pages/task-chat.page.*`
- `web/src/components/chat/task-intake-question-card.tsx`
- OpenAPI `/workspaces/{workspace_id}/task-intake/analyze` 及 schemas

当前 backend 搜索没有旧 provider/client/route/import；只剩专门验证 404 的 `handlers_legacy_routes_test.go`。Frontend 搜索也没有旧 page/store/client import。U07 contract 的“旧直接 API 不存在”边界已落地。

## Legacy 恢复仍保留

- `web/src/App.tsx:92,115-118` 保留 `/task-chat/* → /chat` 重定向。
- `chat-workspace-state.ts:368-420` 保留 v1 未绑定桶、v2 projectKey 分桶和旧 binding 标记的只读扫描；不删除、不合并、不自动换绑。
- `chat.page.tsx:240-281,448,465,505-525` 按当前 Workspace 展示历史条目，逐条由用户点击恢复到当前 Chat；不自动发布、不恢复旧页面级目录绑定。
- `chat-workspace-state.test.ts:93-127` 保留跨 Workspace 隔离、v1/v2 多桶发现、原桶不删除和旧发布状态提示测试。

因此 `rg task-intake` 的剩余命中仅属于 legacy storage key、恢复类型/测试与 404 route test，不是 provider 或独立模型调用。

## 结论

U07 旧 stateless provider/API/OpenAPI/dead frontend chain 已清理完成；唯一保留的是按 Workspace 隔离的历史 v1/v2 文本恢复兼容层和旧链接重定向。当前正式需求入口仍是 Chat → Run → `chat-analysis/v1` → U05 decision → U06 publication。
