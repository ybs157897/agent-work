# web (apps/web)

React + Monaco 只读浏览前端（P1）。

## 本地

```bash
pnpm install
pnpm dev
# http://127.0.0.1:5173
```

可选环境变量：

- `VITE_GATEWAY_URL`（默认 `http://127.0.0.1:8080`）
- `VITE_DEV_TOKEN`（默认 `dev-token-change-me`）

先启动 Gateway，再在页面填入本机工程的绝对路径作为 Workspace Root。

当页面位于同源 iframe 的 `/view/` 路径时，会自动读取 `../bootstrap`，使用宿主提供的
Gateway 代理和 workspace；`read_only: true` 会隐藏连接表单并关闭 Monaco 写入，保留
Java 导航、搜索和 Markdown 阅读。

## 阅读交互迭代

Go to File、最近文件、预览/固定标签、光标历史、字号/软换行与按需 Java 生命周期，见[阅读工作台](../../docs/architecture/reading-workspace.md)。默认 Gateway 为 `127.0.0.1:8080`，Workspace Root 由用户输入或 `VITE_DEFAULT_ROOT` 提供。
