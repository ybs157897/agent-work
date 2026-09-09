# U07 前端交接记录

日期：2026-09-09。Owner：frontend。范围：`agent-team-workbench/web/`。

## 已删除

- 删除旧 stateless 任务需求 API：`src/api/task-intake.ts` 及其 API 测试。
- 删除旧 `task-intake.store.ts` 及其完整测试；当前主链不再调用旧 provider、旧分析循环或旧发布逻辑。
- 删除退出主路由的旧 `TaskChatPage`、旧问题卡组件及其 render/helper tests。
- `App.tsx` 未恢复旧页面；`/task-chat/*` 继续显式重定向到当前 `/chat`，保留 query/hash。

## 保留边界

- `chat-workspace-state.ts` 的 legacy reader 继续只读发现：`task-intake:v1:<workspace>`、`task-intake:v2:<workspace>:<projectKey>` 和旧 binding 标记均不删除、不自动换绑、不恢复旧目录。
- Chat 侧只显示当前 Workspace 下的历史条目；用户点击“恢复”后把旧正文显式放入当前 Chat composer。`projectKey` 作为历史来源信息保留，不作为当前项目授权。
- 当前需求链保持 Chat → `chat-analysis/v1` → 单题回答 → 产品确认 → publication；未回退到旧页面或旧 provider。

## 验证

- `./node_modules/.bin/tsc -b`：通过。
- `./node_modules/.bin/eslint src`：通过。
- `./node_modules/.bin/vitest run`：129 files / 1003 tests passed。
- `./node_modules/.bin/vite build`：通过；仅保留既有大 chunk warning。

未提交、未合并、未推送；未操作浏览器。
