# 交付路线图

Status: accepted

Date: 2026-08-30

## 原则

分刀交付；每一刀可独立演示。优先完成当前阅读主路径；每个出口以真实验证为准。

## P0 — 仓库与契约

- [x] 仓库骨架、`docs/**` 架构与 ADR
- [x] OpenAPI 初稿进 `contracts/openapi`
- [x] Gateway / Web 可编译模块

**出口**：文档评审通过；目录与负向保证无争议。

## P1 — 只读浏览（约 3–5 人日）

- [x] Gateway：`workspaces` + `fs/tree|file|stat` + root jail 单测
- [x] Web：目录树 + Monaco 只读打开文件
- [x] 鉴权：`dev` token
- **无** jdtls

**出口**：指定本机 Maven/Gradle 工程可浏览并打开 `.java`。

## P2 — Java 语义跳转（已集成）

- [x] jdtls-sidecar 启动 / 下载脚本（`apps/jdtls-sidecar`）
- [x] Gateway `lspproxy` + jdtls 生命周期 + `GET …/lsp` WebSocket
- [x] Web Language Client：`textDocument/references` + Cmd/Ctrl+click 弹层跳转
- [x] URI 映射 `webidea://ws/{id}/…` ↔ `file://`；状态条 jdtls 状态
- [x] definition（声明与用法分流）
- [ ] hover（后续）
- [x] `jdt://`：弹层标 external，不打开（P3 再反编译）

**出口**：标准 Spring/Maven 工程内项目源码可跳定义与查找引用。

## P2.5 — 可编辑保存与成员补全（已集成）

- [x] Gateway：`PUT …/fs/file` 写盘（jail + UTF-8 + 大小上限）
- [x] Web：Monaco 可编辑、dirty、Cmd/Ctrl+S 保存
- [x] LSP：`didChange` / `didSave` + `textDocument/completion`（`.` 触发）
- [x] 文档：ADR-007；vision / lsp 契约同步

**出口**：改 `.java` 可保存；输入 `obj.` 在 jdtls ready 后弹出方法列表。

## 阅读交互迭代（2026-09-07）

- Gateway 文件名/路径搜索，浏览器快速打开与最近文件；支持路径后附行列。
- IDEA 式预览/固定标签、可见 Back/Forward、真实光标历史、项目树键盘操作和跳转揭示。
- 可调字号/软换行、目录面包屑、Markdown 文档阅读；删除未实现菜单。
- 一个 Monaco model、有界标签/历史、Java 按需启动与文档关闭、Close Project 释放 sidecar。
- 文件切换前完成保存，失败保留 buffer；路径沙箱和帧上限回归覆盖。

验证记录见 [reading-workspace.md](reading-workspace.md)。独立于未来嵌入、反编译与对照性能基准。

## P3 — 硬化与嵌入（约 +2–4 人周）

- `jdt://` 反编译只读打开
- 空闲回收、崩溃退避、内存上限调优
- bearer / 宿主 token；反向代理嵌入样例
- Call Hierarchy（可选）
- 多模块 / 非标准布局探测改进

**出口**：可嵌入 agent-team-workbench 深链演示；有基本运维手册。

## 明确延期

- 重构写盘、批量格式化、调试 UI、多人协作
- 多语言 LS
- 多租户硬隔离公有云

## 与工作量估算的对齐

见会话结论：P1 天级；P2 周级；整体可用约 1–1.5 人月。本路线图用于排期，不替代迭代内估算。
