# web-idea

浏览器内的 **Java 代码阅读工作台**：保留 IDEA 的项目树、标签和导航习惯，帮助人在 AI 写完代码后快速读懂实现与用法，并完成轻量编辑。

> 定位：**不是** 完整 IntelliJ / Theia / VS Code Web 替代品；**是** 可嵌入产品（如 agent workbench）或独立部署的「看懂 Java 代码」子系统。

## 目标能力（MVP）

| 能力 | 说明 |
|------|------|
| 目录树 | 在锁定的 Workspace Root 下浏览源码树 |
| 打开文件 | Monaco 阅读和编辑文本，Markdown 预览/源码切换 |
| 快速导航 | Go to File、最近文件、行列定位、预览/固定标签、返回/前进 |
| 语义跳转 | 点击符号 → Definition / References（经 Eclipse JDT LS） |
| 工程感知 | 探测 Maven / Gradle 工程根；按需启动 jdtls |

非目标见 [docs/product/vision.md](docs/product/vision.md) 与 [ADR-001](docs/decisions/ADR-001-java-readonly-navigation-scope.md)。

## 架构一句话

```
Browser (React + Monaco + Language Client)
    │  HTTPS + WebSocket
Gateway (Go：鉴权、FS API、LSP 代理、工作区生命周期)
    │  stdio JSON-RPC
jdtls-sidecar (Eclipse JDT Language Server + JDK)
```

详述：[docs/architecture/overview.md](docs/architecture/overview.md)

## 仓库布局

```text
web-idea/
├── apps/
│   ├── web/              # 前端（React + Monaco）
│   ├── gateway/          # 控制面 / FS / LSP 桥
│   └── jdtls-sidecar/    # jdtls 启动与镜像约定
├── contracts/            # OpenAPI / LSP 信封约定
├── docs/
│   ├── product/          # 产品愿景与范围
│   ├── architecture/     # 架构事实源
│   └── decisions/        # ADR
├── notes/                # DSH 决策留痕
└── scripts/              # 本地开发与校验脚本
```

## 文档入口

| 文档 | 用途 |
|------|------|
| [产品愿景](docs/product/vision.md) | 做什么 / 不做什么 |
| [架构总览](docs/architecture/overview.md) | C4 风格总图与原则 |
| [系统上下文](docs/architecture/system-context.md) | 与外部系统边界 |
| [组件设计](docs/architecture/components.md) | 三进程职责与接口 |
| [LSP 桥](docs/architecture/lsp-bridge.md) | Monaco ↔ jdtls |
| [工作区文件系统](docs/architecture/workspace-fs.md) | list/read 与路径沙箱 |
| [安全模型](docs/architecture/security.md) | 信任边界与威胁 |
| [部署](docs/architecture/deployment.md) | 本地 / Docker / 嵌入 |
| [路线图](docs/architecture/roadmap.md) | P0–P3 交付切片 |
| [ADR 索引](docs/decisions/README.md) | 架构决策记录 |

## 状态

三个阶段成果（只读浏览、Java 导航、编辑补全）已集成。当前迭代聚焦 IDEA 式快速导航、阅读排版、文件切换安全与资源释放；真实语义能力需要配置 jdtls。详见[阅读交互架构](docs/architecture/reading-workspace.md)。

## 本地开发

两个进程，默认 token 与 [.env.example](.env.example) 一致：

```bash
# Gateway（127.0.0.1:8080）
cd apps/gateway
export WEBIDEA_DEV_TOKEN=dev-token-change-me
go test ./...
go run .

# Web（另开终端，http://127.0.0.1:5173）
cd apps/web
pnpm install && pnpm dev
```

在页面填入 Gateway 本机上的工程绝对路径作为 Workspace Root，即可浏览并打开 `.java`。

## 开发 Agent 嵌入模式

宿主通过同源 iframe 提供 `/api/v1/code-workspaces/{session_id}/view/`，页面会从
`../bootstrap` 读取固定的 `gateway_base_url` 与 workspace 元数据；嵌入页面不显示
Gateway、token 或绝对目录表单，也不会自行创建 workspace。`read_only: true` 时，
编辑和写盘入口关闭，但 Java 导航、搜索和 Markdown 阅读仍可用。

构建后的 `apps/web/dist` 可通过 Gateway 的 `WEBIDEA_STATIC_DIR` 提供；Vite 使用相对
asset base，适合挂在宿主的 `/view/` 路径下。Gateway 用 `WEBIDEA_LISTEN=127.0.0.1:0`
启动时，会在成功监听后向 stdout 输出唯一的 `WEBIDEA_READY=http://<addr>` 行，供宿主
supervisor 发现端口。

## 常用操作

- **Go to File**：工具栏入口，macOS `⌘⇧O`，或 `⌘/Ctrl+P`；输入 `Service.java:42:5` 定位行列。
- **Recent Files**：`⌘/Ctrl+E`；**Find in Files**：`⌘/Ctrl+Shift+F`。
- 项目树单击临时预览，双击或 **Keep open** 固定；修改会自动固定，标签可切换与关闭。
- **Back / Forward**：工具栏箭头，macOS `⌘[` / `⌘]`，Windows/Linux `Ctrl+Alt+←/→`。
- Java **声明 / 用法**：`⌘/Ctrl+click`、`⌘/Ctrl+B` / `F12`；查用法 `Alt+F7`。
- 编辑区 `⌘/Ctrl+L` 定位行；工具栏调整字号与软换行。`Alt+1` 隐藏/显示项目树。
- 修改后自动保存，或 `⌘/Ctrl+S`。切换/关闭会先完成保存，失败保留原 buffer 并显示重试入口。

## 资源边界

编辑区只保留一个 Monaco model，最多 30 个标签/最近文件，100 个返回位置。首次打开 Java 文件才启动索引，关闭项目会 DELETE 工作区并停止对应 jdtls。隐藏页面暂停文件轮询；文件名搜索不读取文件正文。

这些是已实现的资源约束；目前没有与桌面 IDEA 的同工程内存/延迟对照基准，不能据此宣称固定比例的性能提升。

## 许可证

Apache License 2.0（见 [LICENSE](LICENSE)）。
