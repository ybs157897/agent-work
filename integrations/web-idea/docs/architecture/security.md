# 安全模型

Status: accepted

Date: 2026-08-30

## 1. 资产

| 资产 | 级别 |
|------|------|
| 工作区源码 | 高 |
| 会话令牌 | 高 |
| jdtls 进程（可执行构建脚本副作用） | 高 |
| Gateway 配置中的 root 列表 | 中 |

## 2. 威胁与缓解

| 威胁 | 缓解 |
|------|------|
| 路径穿越读任意文件 | root jail；见 workspace-fs.md；单测覆盖 `../`、编码绕过 |
| 未授权打开他人工作区 | workspace id 绑定会话；枚举 id 无权限则 404 |
| 浏览器直连 jdtls | 不暴露端口；仅 stdio / loopback |
| LSP URI 走私 | 出入站 URI 映射与 jail 校验 |
| jdtls 通过构建执行恶意代码 | MVP 假定 workspace **可信**；不可信租户需容器 + seccomp + 只读挂载（部署文档） |
| Token 泄露进日志 / Referer | 禁止长期 secret 放 query；WSS 优先 header；日志脱敏 |
| 大文件 / DoS | 文件大小上限、tree 截断、限流、jdtls 内存上限 `-Xmx` |
| 嵌入页面越界写入 | workspace 记账 `read_only`；Gateway 拒绝 PUT 与只读 LSP 变更方法 |
| 静态资源越界 | `WEBIDEA_STATIC_DIR` 只服务解析后的构建目录，拒绝 `..`、反斜杠、NUL 和出界符号链接 |
| SSRF（若未来加远程 root） | MVP **仅本地路径**；远程文件系统需新 ADR |

## 3. 信任假设（MVP）

1. 能创建 workspace 并传入 `root` 的调用方，已对应该目录有权访问（本地单用户或宿主已鉴权）。
2. Gateway 与 sidecar 运行在同一信任主机（或 Gateway 管控的容器网络）。
3. 不面向「多租户共享一台 Gateway 且租户互不信任」的公有云硬隔离——若要做，必须容器级隔离 + 独立 uid + 网络策略（见 deployment.md）。

## 4. 鉴权模式

| 模式 | 用途 |
|------|------|
| `dev` | 静态 `WEBIDEA_DEV_TOKEN`；仅本地 |
| `bearer` | 调用方提供 JWT / opaque token；Gateway 校验签名或 introspect |
| `host-headers` | 反代后的可信身份头（仅内网，需共享密钥防伪造） |

嵌入宿主时：宿主签发短时 audience=`web-idea` 的 token，含允许的 `workspaceId` 或 `root` 哈希。

当前开发 Agent 集成使用宿主同源受限代理；iframe bootstrap 只返回代理前缀、workspace
元数据和 `read_only` 标志，不返回上游凭据或宿主绝对路径。

## 5. 负向保证

- MVP **不** 做跨用户共享同一 jdtls 进程。
- MVP **不** 在无认证情况下监听 `0.0.0.0` 作为默认（默认 `127.0.0.1`）。
- 生产配置若 `Listen=0.0.0.0` 必须强制非 dev 鉴权，否则拒启。
