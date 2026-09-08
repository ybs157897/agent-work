# 阅读工作台：交互与资源生命周期

Status: implemented
Date: 2026-09-07

## 用户路径

AI 产出代码后，人在项目树或 Go to File 找文件，阅读代码与 README，跟随 Java 声明/引用，再通过 Back/Forward 返回原光标位置。熟悉的 IDEA 交互服务这条路径；页面只展示已实现的动作。

## 组件

| 组件 | 责任 |
|---|---|
| `App.tsx` | 当前 buffer、串行保存、文件打开代际、标签/最近路径、光标历史、工作区关闭 |
| `QuickOpen` | 180ms 防抖，取消旧请求，文件/路径搜索和最近文件；`path:line:column` 解析；上下键与 Enter |
| `FileTree` | 按需展开、键盘操作、定位当前文件、预览与双击固定 |
| `CodePane` | 单 Monaco model、按路径保存 view state、Java 跳转动作、字号/换行/Markdown 视图 |
| `JavaLspSession` | 首次 Java 打开时启动；活动文档 didOpen/didChange/didSave/didClose |
| `NavHistory` | 有界的返回/前进位置栈 |

文件 API 与语义 URI 均由 Gateway 沙箱约束。文件路径搜索不读取正文，返回与扫描规模受限，截断结果在 UI 中明示。协议详见 [workspace-fs.md](workspace-fs.md)。

## 保存与切换

活动 path 和 buffer 在目标文件读取成功后一起切换。切换先取消自动保存计时器、等待已开始的保存并保存最新改动；只有保存成功才离开。失败显示错误并保留当前文件和改动。读取期间编辑器只读；程序化装载不会触发自动保存。

打开操作使用递增序号，旧读取不能覆盖新目标。关闭项目先保存，再 DELETE Gateway workspace，成功后清除本地会话和 timers；服务端释放对应 Java 进程。外部修改只在当前 buffer 干净时重载。

## 资源约束

- 仅一个活动 Monaco model；最多 30 个标签/最近路径、30 个 view state、100 个历史位置。标签不保留文件正文。
- Markdown/纯文本浏览不启动 Java 索引；切出 Java 文档发送 didClose。
- 当前文件以 2 秒周期检测外部变化，隐藏页面不发起轮询，同一时刻只允许一条检测链。
- 不载入远程字体；Monaco 与语法资源通过本地构建提供，不添加第二套语言服务。
- 单模型会在切换文件时重置 undo 栈；返回恢复光标/滚动。需要跨文件 undo 时重新评估有界多模型方案。
- 宿主 iframe 从 `../bootstrap` 读取同源 Gateway 代理；`read_only=true` 时 Monaco、保存
  和 Gateway PUT 均关闭，LSP 仍允许文档同步及阅读查询。

本轮不承诺固定内存或延迟收益。后续使用同一工程比较冷启动、首次 Java ready、跳转 P50/P95、连续阅读后的浏览器 heap 与 sidecar RSS。

## 验证

2026-09-08 收口，在本机 Gateway、真实 Eclipse JDT LS 和浏览器中验证。Java 交互使用磁盘上的最小 Maven 工程（未 mock LSP），另在本仓库验证真实路径查找与文本搜索。

| 验证 | 结果 |
|---|---|
| `bash scripts/verify-scaffold.sh` | Gateway 测试、Web 类型检查、6 条导航/补全回归通过 |
| `go test -race ./...`（Gateway） | 通过，包含工作区删除/代理与 sidecar 生命周期回归 |
| `pnpm build`（Web） | 通过；Monaco 作为首次打开文件时加载的独立 chunk |
| Go to File + `:6:34` | 首次打开精确定位到行列 |
| F12 → 声明 → Back | 从调用处跳到实现，返回原 6:34 光标位置 |
| Alt+F7 → 上下键/Enter | 真实 Java 用法列表与键盘跳转通过；弹层取得焦点，避免编辑原文件 |
| `service.` 成员补全 | 真实 jdtls 返回 `greet(String name)` 等成员；请求前同步 buffer |
| Go to Line / Markdown | 定位行入口、README 排版与源文件路径打开通过 |
| 编辑后立即切换 | 原文件保存成功；目标文件正文不被旧 buffer 覆盖 |
| 磁盘拒绝写入 | 原标签/改动保留，其他文件不变；恢复权限后重试成功 |
| 编辑后立即关闭最后标签 | editor 消失；观察到 didSave → didClose，等待超过防抖周期没有 didOpen |
| 连续打开 35 个文件 | 观测到最大 1 个 Monaco model、1 个 editor |
| Close Project | 浏览器发出 DELETE；对应 jdtls 进程退出，无验收 sidecar 残留 |
| 1024px 阅读视图 | 自动布局稳定后无页面横向溢出，字号调节生效 |
| 资源请求 | 生产页面无远程字体 / Monaco CDN 请求 |

生产构建入口 JS 约 324.7 kB（gzip 97.4 kB），延迟加载的 CodePane/Monaco chunk 约 2.69 MB（gzip 701 kB）。Vite 对后者保留大 chunk 提示；未把告警阈值调高来隐藏它。

本轮未做桌面 IDEA 同工程内存/延迟对照，也未覆盖大型多模块工程的冷索引基准。`var/acceptance/reading/` 下保留本机截图与可编译验收工程，属于忽略的验证工件。
