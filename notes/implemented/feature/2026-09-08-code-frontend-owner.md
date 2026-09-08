# 前端代码工作台会话边界

Status: implemented; local acceptance complete; uncommitted task worktree

## 决策与理由

前端把代码工作台会话绑定到 workspace、developer Agent 和 conversation。创建时带上当时存在的最新 Run；Run 状态变化不会重开 iframe 会话，避免阅读界面因为聊天流更新反复丢失位置。关闭、切换会话/Agent/Workspace 或卸载时发送 keepalive DELETE，并用请求版本号清理迟到的创建结果。

代码阅读复用现有 Chat 的中间画布与右侧对话布局，iframe 只在代码开关打开后创建，主题通过同源 `postMessage` 同步。代码页面保持只读，不在宿主前端引入第二份 Monaco 或文件路径输入。

## 放弃了什么

没有把代码工作台做成独立路由或在开发 Agent 初始化时常驻启动。独立路由会丢失当前会话上下文，常驻启动会为未使用的 Agent 占用 Gateway/JDTLS 资源；按需 iframe 和作用域会话保留了现有对话布局并让关闭行为可验证。

## 复活条件

【需要 Agent 主动执行 Java 语义查询或写入代码时 → 新增显式工具/写入授权与独立交互 → 继续保持当前只读 iframe 不变，不能把写入口偷偷放进阅读画布】
