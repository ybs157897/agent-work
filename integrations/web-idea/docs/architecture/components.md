# 组件设计

Status: accepted

Date: 2026-08-30

## 1. 组件一览

| 组件 | 路径 | 语言 | 对外接口 |
|------|------|------|----------|
| Web Shell | `apps/web` | TS/React | 用户 UI |
| Gateway | `apps/gateway` | Go | HTTP + WebSocket |
| JDTLS Sidecar | `apps/jdtls-sidecar` | 脚本/镜像 + Java | 仅被 Gateway spawn |
| Contracts | `contracts/` | OpenAPI / Markdown | 版本化契约 |

## 2. Gateway

### 2.1 子模块

| 模块 | 职责 |
|------|------|
| `auth` | 校验 Bearer / 宿主注入会话；开发模式可用静态 token |
| `workspace` | 注册 Workspace：`id` → 绝对 `root`、状态、`read_only`、jdtls 句柄 |
| `fsjail` | `list` / `stat` / `read` / `write` / 内容与路径搜索；执行 [workspace-fs.md](workspace-fs.md) 沙箱 |
| `lspproxy` | 每工作区一条 WSS ↔ jdtls stdio 双向代理 |
| `jdtls.Manager` | 启停 jdtls；自动空闲回收/崩溃重启仍属后续硬化 |
| `events` | （可选）SSE：索引进度、sidecar 状态 |

### 2.2 HTTP API（草案，权威稿进 `contracts/openapi`）

```text
POST   /api/v1/workspaces              # { root } → { id, status }
GET    /api/v1/workspaces/{id}         # 元数据与 jdtls 状态
DELETE /api/v1/workspaces/{id}         # 释放 sidecar

GET    /api/v1/workspaces/{id}/fs/tree?path=&depth=
GET    /api/v1/workspaces/{id}/fs/file?path=
PUT    /api/v1/workspaces/{id}/fs/file?path=
GET    /api/v1/workspaces/{id}/fs/search?q=
GET    /api/v1/workspaces/{id}/fs/files?q=
GET    /api/v1/workspaces/{id}/fs/stat?path=

GET    /api/v1/workspaces/{id}/lsp     # Upgrade: websocket（LSP 帧）

GET    /api/v1/workspaces/{id}/events  # SSE（可选）
GET    /healthz
```

规则：

- 所有 `path` 为 **相对 Workspace Root** 的 POSIX 风格相对路径（`/` 分隔）；
- 拒绝 `..`、绝对路径、NUL、Windows 盘符（Unix 部署）；
- `read` 设最大字节上限（默认 2 MiB，可配）。
- `read_only=true` 的 workspace 拒绝 `PUT /fs/file`，LSP 仅转发阅读、同步、生命周期和取消方法；未知或变更方法返回 JSON-RPC `-32001`。

### 2.3 工作区状态机

```text
created → browsing → indexing → ready
                ↘        ↓
                 failed ←┘
任意态 → stopped（DELETE 或空闲回收）
```

- `browsing`：FS 可用，jdtls 未起或未就绪；
- `indexing`：jdtls 已起，UI 显示进度；
- `ready`：语义请求可服务；
- `failed`：可重试 start；FS 仍可用。

## 3. Web

| 模块 | 职责 |
|------|------|
| `App` | 左树 / 标签 / 编辑器 / 状态；串行保存、导航历史与资源释放 |
| `FileTree` | 懒加载展开、键盘导航、定位当前文件、预览/固定 |
| `CodePane` | 单 model 编辑/阅读、语法高亮、Markdown；标签状态由 App 管理 |
| `JavaLspSession` | 按需 WSS；definition / references / completion；文档生命周期 |
| `QuickOpen` | Gateway 文件路径搜索、最近文件、行列定位 |
| `StatusBar` | jdtls 状态、索引提示、错误 |

文件操作使用 `workspaceId` + 工作区相对 path；用户输入的 root 仅用于创建 Gateway 工作区。当前阅读交互与资源边界详见 [reading-workspace.md](reading-workspace.md)。

## 4. jdtls-sidecar

| 内容 | 说明 |
|------|------|
| 启动脚本 | 组装 `-jar` launcher、`-configuration`、`-data`、`-Xmx` |
| 工作数据目录 | 每 workspace 独立 `-data`，避免串索引 |
| 镜像（可选） | 预装 JDK 21 + jdtls；Gateway 用 `docker run` 或本机进程 |

Sidecar **不** 暴露端口到宿主机网络（推荐 stdio）；若必须 TCP，仅绑 loopback 并由 Gateway 连接。

## 5. 依赖方向

```text
web → (HTTP/WSS) → gateway → (spawn/stdio) → jdtls
                     gateway → disk (root jail)
contracts ← web, gateway（共享类型/OpenAPI）
```

禁止：`web` 直接依赖 jdtls 包；禁止 `jdtls-sidecar` 依赖 Gateway 业务包。
