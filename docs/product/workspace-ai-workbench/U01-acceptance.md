# U01 验收记录

日期：2026-09-09。结果：已完成。分支：`codex/u01-global-workspace`。尚未提交或合入主线。

## 已交付

全局入口负责项目空间的打开、创建与切换；一个空间固定一个经核验的本机项目根。同一实际目录的别名/符号链接会复用已有空间。新空间复制普通 Agent 配置为独立实例，原有知识、任务、会话及运行记录不随之复制。

Agent 文件配置按空间隔离；旧单空间目录迁移有冻结清单、内容摘要和可恢复步骤。数据库提交与文件落盘分别记录，配置未落盘不会显示就绪，重新打开或重启可恢复。已固定空间的旧 Location API 不能添加第二目录或替换实际根。

现有聊天按空间、Agent 和会话恢复草稿、队列及本地原附件。切换预加载失败保留原空间；切回恢复各自最近页面；旧页面退出时不再覆盖新空间路由。旧需求页链接进入现有聊天，旧草案按原空间提供显式恢复入口。

## 验证证据

- [34 项打开/创建/克隆/重载检查](../../review/assets/u01-global-workspace/api-checks.json)、[9 项失败恢复与对象隔离检查](../../review/assets/u01-global-workspace/recovery-checks.json)、[5 项最终目录及 Bootstrap 边界检查](../../review/assets/u01-global-workspace/final-boundary-checks.json)：共 48 项真实 HTTP/SQLite 检查通过。
- [现有会话草稿回切和刷新恢复](../../review/assets/u01-global-workspace/ui-existing-chat-restored.json)、[A 最近设置页](../../review/assets/u01-global-workspace/ui-last-route-alpha.json)、[B 最近对话页](../../review/assets/u01-global-workspace/ui-last-route-beta.json)、[切换失败保留内容](../../review/assets/u01-global-workspace/ui-switch-failure.json)均通过实际浏览器验证。
- [附件真实字节](../../review/assets/u01-global-workspace/ui-attachment-bytes.json)与原文件 SHA-256 一致；[后台运行归属与队列恢复](../../review/assets/u01-global-workspace/ui-background-run.json)使用内置 Mock 验证控制面生命周期，测试运行已取消并清理队列。
- [单一入口与明确选择](../../review/assets/u01-global-workspace/ui-open-project-explicit.json)、[重复打开恢复原空间](../../review/assets/u01-global-workspace/ui-open-existing-final.json)、[旧链接及历史内容保留](../../review/assets/u01-global-workspace/ui-legacy-redirect.json)通过。
- 后端构建、vet、gofmt 通过。Agent 配置迁移、创建失败、目录约束、scope 和原生 iframe 请求均有触面回归；agentconfig/httpapi/sqlstore race 通过。
- 前端类型检查、ESLint、production build 通过；完整套件曾通过 121 文件/961 测试，之后的草稿、路由修复分别通过聚焦回归并重新构建。

## 边界与后续单元

`application` 全包 race 在 600 秒超时；当时所在的 `TestStaticValidationRollsBackRunCreation` 独立 race 复跑通过（4.10 秒），不将全包超时记作全量通过。当前 worktree 的 node_modules 为符号链接，pnpm 的目录安全检查会拒绝，已用同一依赖目录下的实际 tsc/eslint/vitest/vite 可执行文件完成检查。

桌面明暗主题与 1024px 主流程已检查；640px 下全局选择器可见、无整页横向溢出，旧聊天导航存在拥挤。

U01 只验证空间基础。AI 接收文件路径并读取原件属于 U03，需求确认与发布版本关联属于 U04–U06，真实模型完整流程属于 U07；以上单元仍未完成。汇总见[机器可读验收记录](../../review/assets/u01-global-workspace/verification.json)。

## U07 配置继承补验

内置 Coordinator 与 Knowledge Librarian 的可配置 runtime/model 也继承源空间，同时创建独立系统身份并保留当前保护模板；目标业务表空，Ensure/新Service重载不覆盖设置。实际 SQLite/race 验证见[最终验收](U07-acceptance.md)。
