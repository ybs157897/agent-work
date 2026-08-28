# Run 级文件变更审核与安全撤销

Status: implemented

## 决策与理由

Chat 的 Final Answer 之后增加 Run 级文件变更卡：显示真实文件清单和增删统计；「审核」打开右侧
Diff 工作区；「撤销」只恢复本 Run 写工具产生的变更。审核是只读查看，不等同于接受 Artifact，
也不隐含 commit。

数据源遵循 ZCode 的写盘快照契约，而不是整仓 Git 状态：每次 write/edit 前读取目标文件全文为
`beforeContent`，写盘成功后读取 `afterContent`。同一 Run 内同一路径多次写入时保留首次 before、
持续更新最终 after，并累计 writeCount；卡片只包含本 Agent 经写工具实际修改的文件。

增删统计统一使用公共前后缀裁剪 + 中间 LCS；中间矩阵超过 400,000 时整段按替换计算。卡片摘要、
逐文件统计和撤销全部来自同一快照桶。审核侧栏由 before/after 构造带上下文的 unified diff；Git
工作区面板是另一条全局链路，不能反向成为聊天卡的数据源。

撤销采用乐观并发：当前受影响路径必须仍等于 snapshot after 的 hash，整批文件全部安全才允许写回
before；任一文件被用户或其他 Run 再次修改都返回 409，且整批不写。新建文件恢复为不存在，修改文件
恢复首次 before。请求携幂等键；成功后写审计和持久 `file_changes.reverted` 事件，进程重启仍能识别
已撤销状态。

## 实现事实

- Kimi app-server adapter 为 Write/Edit 采集 workspace 相对路径、before/after 内容、存在性和 hash；
  路径穿越、symlink、二进制与超过 400KB 的文件拒绝采集。
- `internal/filechanges` 是统计与 Diff 唯一实现：同路径首 before/末 after、LCS 行统计、3 行上下文 hunk。
- Run changes API 提供摘要、逐文件 Diff 与幂等撤销；旧 Run 没有 snapshot 时返回 unavailable。
- 前端在最新 Final Answer 后显示卡片，默认列 3 个文件；审核 Drawer 支持文件切换与 DiffCard；撤销使用
  破坏性确认 Modal，409 冲突对用户可见。

## 放弃了什么

- 不把当前 `git diff HEAD` 或 `git diff --numstat` 归因给最新 Run：它会吞并用户手改和 Run 前 dirty。
- 不根据 `tool.completed.output` 或截断 args 推断可撤销内容：缺少 old/new 快照时无法恢复。
- 不让前端直接执行 `git restore`：路径、并发、权限、幂等和审计都必须由服务端控制。
- 不复用 Artifact 的 draft/accepted 状态表达代码审核；产物生命周期与工作区变更是不同领域。

## 复活条件

当前版本只承诺 Kimi app-server 能可靠拦截且带单文件 path 的 Write/Edit。若 shell/bash 修改文件、
apply_patch 一次修改多文件、工具协议没有写前事件、文件过大需要外置 blob，或执行器位于远端，必须先
增加执行器侧 snapshot/contentRefs 上报；不得回退到扫描 Git dirty 猜测本轮文件。
