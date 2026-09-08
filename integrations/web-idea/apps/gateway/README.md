# Gateway

Go 控制面：鉴权、Workspace FS、jdtls 生命周期（P2）、LSP WebSocket 代理（P2）。

## 本地（P1）

```bash
export WEBIDEA_DEV_TOKEN=dev-token-change-me
go test ./...
go run .
# listens on 127.0.0.1:8080
```

设置 `WEBIDEA_LISTEN=127.0.0.1:0` 时，进程成功监听后会在 stdout 输出
`WEBIDEA_READY=http://127.0.0.1:<port>`，适合由宿主 supervisor 发现端口。设置
`WEBIDEA_STATIC_DIR=/absolute/path/to/apps/web/dist` 可由 Gateway 提供构建后的
嵌入页面；静态路径会拒绝遍历与出界符号链接。

环境变量见仓库根 `.env.example`。
