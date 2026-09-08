# 开发 Agent Java 代码工作台

状态：Java 阅读集成已实现并完成本地验收，用户于 2026-09-08 授权合并到 main。使用 web-idea 当时本地的完整版本；范围与结果见[验收记录](developer-code-workspace-acceptance.md)。

## 体验与范围

在现有 `/chat?agent=<id>` 中，仅已启用的用户管理 Agent 且 `role=developer` 展示“代码”开关。展开后，中间是 Java 代码工作台，右侧保留当前对话；主侧栏保持可见。知识画布与代码画布互斥，窄屏通过代码/对话按钮切换。关闭、切换 Agent、工作区或会话必须释放旧实例，不接收迟到的打开结果。

右侧沿用已有 Chat 和所选 Agent 的运行时配置。本次独立预览使用 Mock 验证界面与生命周期，没有接入真实模型，因此不构成真实聊天验收；在该预览发送“你好”会得到 Mock 的固定完成回复。合并代码不自动修改现有模型配置或重启主服务。

完整保留 web-idea 当前工作树的导航、文件树、搜索、Monaco、Java 定义/引用、Markdown 阅读与资源释放实现。集成画布承担代码阅读，文件写入入口关闭；原始独立应用的编辑实现仍保留在源码快照中。Java 索引按需启动；未安装 JDK/jdtls 时文件阅读仍可用，并明确显示语义服务不可用。

“只读”约束文件保存 API、Monaco 编辑和 LSP 变更方法，不等同于操作系统文件系统沙箱。集成关闭自动构建并把 Eclipse 元数据移到专属索引目录；本次 Maven 验收源码文件和 Git 状态未变，但导入仍会创建空的 `target/classes`、`target/test-classes` 目录。不要把 Git clean 当作文件系统零写入证明。

## 源码与运行时

`integrations/web-idea/` 是独立 Go Gateway 与 React 应用的源码快照，包含原项目未提交的修改与新增源码；`SOURCE_SNAPSHOT.json` 记录原始 HEAD、分支及逐文件 SHA-256。不得只采用旧 HEAD，也不得复制 node_modules、缓存、密钥或本机运行数据。原始 web-idea 工作树保持原样。

工作台通过同源受限代理承载 iframe。Gateway 使用仅服务端持有的随机凭据和本机回环监听；浏览器无需填写端口、绝对目录或 dev token。构建与启动入口解析仓库内快照，独立 Gateway 随控制平面关闭，jdtls 随阅读会话关闭。不要通过启动第二套 Run/Task/checkout 锁来承载阅读状态。

### 构建与启动

在 `agent-team-workbench/` 下执行：

```sh
make code-workspace-build
make code-workspace-jdtls
make web-build
make run-control-plane
```

Java 语义查询需要可执行的 Java 21+ 与 JDT LS；可用 `JAVA_HOME` 指定已安装的 JDK。`code-workspace-jdtls` 从 Eclipse 官方下载依赖，存在时复用；依赖安装和构建产物均不进入 Git。只浏览文件时可不安装 jdtls。首次展开时才启动 Gateway，监听由操作系统分配的空闲回环端口，启动失败在画布显示可重试的错误。

宿主目录仍在现有 `host-registry.yaml` 中登记；单工作区且只有一个可用挂载时沿用默认绑定，多个工作区须显式配置 location。不会为了打开画布自行修改工作区绑定。配置缺失时在页面显示原因。

部署可覆盖 `ATW_WEBIDEA_ROOT`（源码/运行时根）、`ATW_WEBIDEA_BIN`（已构建 Gateway）、`ATW_WEBIDEA_WEB_DIST`（Web 构建目录）、`ATW_WEBIDEA_JDTLS_LAUNCH`（语言服务启动脚本）。`WEBIDEA_JDTLS_HOME` 可指向已安装的 JDT LS，`WEBIDEA_JDTLS_XMX` 控制 Java 堆上限。默认均从仓库内快照解析；服务端凭据自动生成，不设置固定 dev token。生产静态托管和 Vite 开发代理都必须转发 WebSocket。

## API 与目录绑定

- `POST /api/v1/workspaces/{workspace_id}/agent-profiles/{agent_id}/code-workspaces`，请求 `{conversation_id?, run_id?}`；拒绝客户端提交 root/path/cwd。响应 `{id, frame_url, repository_identity, branch?, worktree_ref?, read_only, expires_at}`。
- `DELETE /api/v1/code-workspaces/{session_id}` 关闭上游 workspace 和语义连接；重复关闭可安全处理。
- `GET /api/v1/code-workspaces/{session_id}/bootstrap` 返回 `{gateway_base_url, workspace, read_only: true, embedded: true}`，不返回上游服务凭据或宿主绝对路径。
- `/api/v1/code-workspaces/{session_id}/view/` 承载构建静态资源，采用相对 asset base。
- `/api/v1/code-workspaces/{session_id}/gateway/{rest...}` 仅代理此会话固定的上游 workspace 的阅读端点与 LSP WebSocket；拒绝创建其他 workspace、任意 ID、文件写入和路径越界。

后端依据存储中的 Agent、Workspace 与 Conversation/Run 关系检查资格。指定 Run 必须属于此 Agent、工作区和会话；优先用其不可变执行上下文。无 Run 时使用现有工作区默认 location/branch，并由本机 HostRegistry 解析，不从浏览器目录或进程 cwd 猜测。历史 Run 可用于只读检查，不要求运行中、不占用 checkout 写锁。远程 Host 在没有阅读通道时明确拒绝，不能误读本机同名目录。

每个阅读会话持有随机 ID、有过期和清理机制；每次访问重新检查 Agent 已启用、开发角色及资源归属，使用时复核目录身份。已建立的 LSP 连接每 2 秒复核授权，撤销后关闭两侧连接。删除失败的会话进入不可读取的 closing 状态并重试；确认 Gateway generation 改变后清除失效句柄。空会话不会因清理定时器启动 Gateway。

上游 workspace 创建携带随机 `client_key` 与固定 TTL，同 key 重试返回同一工作区，内容冲突明确拒绝；未拿到创建响应的孤儿工作区仍由 Gateway TTL 回收。浏览器 pagehide/React 卸载尽力发送 keepalive DELETE，网络失败由 TTL 兜底。后端重启后的旧会话失效，前端提供重试。现有工作台普通 API 的 demo-owner 用户模型未在本次改为多用户登录；角色门禁约束此功能的使用上下文，不宣称可隔离拥有本机 shell/管理员权限的主体。

## 嵌入约定

iframe 从当前 URL 的 `../bootstrap` 读取固定工作区配置，直接进入阅读界面，不显示独立应用连接表单。父页面以同源 `postMessage` 同步主题，消息必须核对 origin 和 source。iframe 不自行新建任意工作区。主题与宿主 DESIGN.md 的语义变量一致，工作台本身不加载第二份 Monaco。

## 验收

- 开发 Agent 展开、关闭、重新打开；非开发 Agent 无入口，直接请求被后端拒绝。
- 任意目录、其他工作区/Agent/会话/Run、其他 Gateway workspace 与写入请求被拒绝。
- Java 工程真实文件读取、快速打开、F12 定义、查找引用、返回位置；浏览器与真实 jdtls 进程共同证明。
- 切换会话/Agent/工作区、展开请求竞态、WebSocket 断开、关闭面板、服务退出后释放资源。
- 明暗主题、窄屏、固定侧栏、键盘焦点与错误重试可用；既有对话与知识画布测试保持通过。
- 后端 build/vet、触面 race；前端 tsc -b/test/lint/build；web-idea Gateway race 与 Web test/build。

决策记录见 `notes/implemented/feature/2026-09-08-developer-code-workspace.md`。
