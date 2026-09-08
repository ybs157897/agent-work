# 部署架构

Status: accepted

Date: 2026-08-30

## 1. 拓扑

### 1.1 本地开发（默认）

```text
localhost:5173  web (Vite)
localhost:8080  gateway
  └─ child process: java … jdtls  (stdio)
工程目录: 开发者本机路径
```

### 1.2 Docker Compose（单机）

```text
[browser] → [gateway:8080] → [jdtls container|process]
                ↓
         volume: workspace root (bind mount)
```

- Gateway 与 jdtls 可同 compose 网络；jdtls **不** publish 端口。
- 内存：为 jdtls 单独 `mem_limit`（建议 ≥ 1–2 GiB）。

### 1.3 嵌入宿主

```text
Host UI ──iframe / path reverse-proxy──► web static
Host API 签发 token ──────────────────► gateway
Host 已有磁盘上的 execution root ─────► gateway workspace.root
```

要求：Gateway 所见路径与宿主 agent 的 `WorkspaceRoot` 一致（同一机器或共享卷）。

嵌入回路由宿主会话提供 `view/` 静态路径与同级 `bootstrap`；iframe 读取 bootstrap 后，
只使用宿主返回的同源 Gateway 代理前缀。Gateway 不向浏览器暴露 host token，静态资源
采用相对路径。配置 `WEBIDEA_STATIC_DIR` 时只服务该构建目录，拒绝路径遍历和逃逸符号
链接；未知的无扩展路径才回退到该目录的 `index.html`。

## 2. 配置面（环境变量草案）

| 变量 | 含义 |
|------|------|
| `WEBIDEA_LISTEN` | 默认 `127.0.0.1:8080` |
| `WEBIDEA_AUTH_MODE` | `dev` / `bearer` / … |
| `WEBIDEA_DEV_TOKEN` | dev 模式令牌 |
| `WEBIDEA_JDTLS_JAVA` | `java` 可执行文件 |
| `WEBIDEA_JDTLS_HOME` | jdtls 安装根 |
| `WEBIDEA_JDTLS_XMX` | 如 `1g` |
| `WEBIDEA_MAX_FILE_BYTES` | 默认 `2097152` |
| `WEBIDEA_IDLE_TTL` | sidecar 空闲回收 |
| `WEBIDEA_STATIC_DIR` | 可选的 `apps/web/dist` 静态目录 |

## 3. 资源与扩缩

| 组件 | 扩缩 |
|------|------|
| web | 静态资源 CDN / Nginx |
| gateway | 无状态水平扩展时，**workspace 亲和** 必须 sticky（LSP 连到持有 sidecar 的实例）或改用共享编排器 |
| jdtls | 每 workspace 一进程；按内存垂直扩展 |

MVP 建议：**单 Gateway 实例**，避免 sticky 复杂度。

## 4. 可观测性

- `/healthz`：进程活着；
- `/readyz`：可选，JDK 可执行；
- 日志字段：`workspace_id`、`lsp_method`、`duration_ms`、错误码；无路径越狱细节给客户端。

## 5. 备份与状态

- 无业务 DB（MVP）；workspace 为内存态 + jdtls `-data` 目录。
- `-data` 可落 `var/jdtls-data/{id}`；删除 workspace 时清理。
