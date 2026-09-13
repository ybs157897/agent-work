# Run 文件只读预览（对话里点开 md 直接读）

Status: implemented

## 决策与理由

对话里的 `file` 块新增 `path` 字段（仓库相对路径，POSIX），文件行渲染「打开」动作，点开在右侧抽屉里按
Markdown 渲染内容。内容由控制平面端点 `GET /api/v1/runs/{run_id}/file?path=...` 提供：客户端只能给相对
路径，服务端用 `hostregistry.ReadableRoots(snapshot)` 在「Run 自己的 checkout + 同一仓库的主工作树与
git worktree」集合里解析，路径经 `secureJoin`（拒绝对路径、`..` 逃逸、symlink 穿透）与 `.git` 前缀拒绝，
只读文本、1 MiB 上限，响应只有相对路径与内容，**不含任何宿主绝对路径**。

可读集合刻意包含同仓库的 worktree：需求文档这类交付物常常写在 agent 自建的任务 worktree 里（本例就是
`agent-work-taskboard-title-search`，它落在工作区受信根 `agent-work` 之外），只在授权根内解析会让最常见的
一个场景继续打不开。集合仍由受信 registry 与 git 事实派生，调用方给不了路径。

起因见 [被否决的正文承载方案](../../rejected/feature/2026-09-13-chat-delivered-document-body.md) 的「当初的起因」。
规范口径落在 [ContentBlock v1](../../../docs/frontend/chat-content-blocks-v1.md)。

## 放弃了什么

- **正文承载整篇文档**：用户已明确否决（正文摊大饼、交付物没有独立形态），见 rejected note。
- **让 `file` 块自己带 `content`**：长文档塞进 JSON 字符串会引入转义风险，fence 一旦解析失败整段回落源码；
  文件本来就在盘上，没必要复制进消息。
- **接收绝对路径或 `file://` 路径**：执行上下文族端点「不携带、不返回宿主绝对路径」是既有硬约束；放宽它
  等于把整个宿主文件系统暴露给浏览器。
- **目录列举/搜索、图片与二进制预览**：v1 只做文本只读预览；二进制、非 UTF-8、超限文件都返回可读 problem，
  不做截断——半份文档比明确的拒绝更危险。
- **远程执行宿主预览**：非 `host_local` 直接 409 `run_file_unavailable`，不伪造本地通道。

## 复活条件

- 需要远程宿主预览时：先给 Runner 定义等价的文件读取契约（同样只收相对路径、同样返回内容不返回路径），
  再把 `run_file_unavailable` 换成远程通道；预埋点是 application 层只依赖 `runFileRoots` 钩子，未绑定宿主细节。
- 需要图片预览时：在既有端点上按 MIME 增加二进制分支，或新增并行的 assets 端点，不复用文本分支。

## 验证

- `go test -count=1 ./internal/hostregistry/`：可读根覆盖 Run 自己的 checkout 与同仓库 worktree、跳过已删除
  worktree、快照 identity 不匹配时 fail closed。
- `go test -count=1 ./internal/application/ -run 'TestNormalizeRunFilePath|TestReadRunFilePreview|TestPreviewMIME|TestReadRunFile'`：
  路径收敛与拒绝表、二进制/非 UTF-8/超限/目录拒绝、授权根优先与 worktree 兜底、未挂载钩子时 fail closed。
- `go test -count=1 ./internal/httpapi/`：路由 ↔ `contracts/web/openapi.yaml` 双向对账通过。
- `web`：`pnpm tsc -b && pnpm test && pnpm lint` 全绿；新增 file 块打开动作、历史消息路径兜底、越界路径
  不产生动作、problem code 文案映射的定向断言。
- 干跑（不触碰运行中的服务，只读真实 `workbench.db` + 真实 `host-registry.yaml`）：用户那条对话的 Run
  `run_01M2C9FS5VS5E6N48MMR536XZQ` 解析同一路径得到 10,456 B `text/markdown`，内容确为那份需求文档；
  `../secret.md`、`/etc/passwd`、`.git/config`、缺失路径分别得到 invalid/invalid/invalid/not found。
  该文件在任务 worktree 里，证明「同仓库 worktree 计入可读集合」按预期生效。
- 端到端（2026-09-13 交付窗口，合并后）：重建 `bin/control-plane` 与 `web/dist` 并重启 8080，在用户那条真实
  对话（`wi_01M2C9FS5HTWMDD61KH6VTF6G2`）里点开同一文件行：抽屉标题栏显示仓库相对路径、10.2 KB、
  `text/markdown`，正文按 Markdown 渲染出 5,176 字符（任务看板按标题搜索 / 背景 / 现状核对（改动前事实）/
  需求规格 / 验收条件 / 决策与理由，含表格）。历史消息靠展示名兜底的打开动作也一并验证。
