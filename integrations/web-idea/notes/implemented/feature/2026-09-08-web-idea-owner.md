# web-idea 嵌入 owner 状态

Status: implemented; local acceptance complete; uncommitted task worktree

## 已落地

- 嵌入前端从 iframe 当前 URL 的 `../bootstrap` 读取同源工作区配置；失败显示可重试状态，bootstrap Promise 共享以避免 React StrictMode 重复创建会话。
- 嵌入会话使用同源 Gateway 代理路径与空 token；GatewayClient 不发送空 `Authorization`，LSP WebSocket 不拼接静态 token。
- 嵌入模式强制 Monaco 与保存入口只读，保留项目树、快速打开、文本搜索、Java Definition/References、Markdown 与导航历史。
- Vite 使用相对 asset base；接收并校验父窗口同源 `atw-code-theme` 消息，应用宿主语义 token 与明暗主题。
- Gateway 支持受限静态文件服务、动态回环监听并输出单行 `WEBIDEA_READY=http://...`；收到 SIGINT/SIGTERM 时停止 HTTP 服务并 `StopAll` jdtls。
- Workspace 的 `read_only` 状态由 Gateway 记账；只读 workspace 拒绝 PUT 与未列入阅读 allowlist 的 LSP 请求。
- 只读 jdtls 通过 `WEBIDEA_READ_ONLY` + JVM system property 将 Eclipse metadata 重定向到
  workspace 专属 data 区；DELETE/StopAll 定向清理对应 data 子目录，短暂 WebSocket 断线保留索引。
- 只读 LSP 帧现在 fail-closed：批量、非对象、非法 JSON、非法 response 和未知/变更方法不会
  穿过代理；合法 response 只接受合法 id 与单一 result/error。
- 树、工具窗、搜索和选择状态使用语义变量，宿主暗色 tokens 下保持文本与边框对比度。

## 证据与待收口

- 已通过 Gateway 普通测试和 Web 前端 `tsc -b`、导航/补全回归、生产构建；完整 race、静态服务和真实 READY/jdtls 进程验收待根 Agent 联合运行。
- 本 owner 不提交 Git；所有修改限于 `integrations/web-idea/` 快照。
- 独立 clean fixture 证据：默认配置会产生 `.classpath/.project/.settings/target`；只读配置
  后 checkout `git status` 保持 clean，workspace data 约 41MB，DELETE 后专属目录被删除。

## 文件系统边界复核

最终联合验收源码文件和 Git 状态未变；但 Maven 导入仍建立空 target/classes、target/test-classes 目录。Eclipse metadata 已移入 data 区。Git clean 不代表目录树零变化，本实现不是操作系统文件系统沙箱。
