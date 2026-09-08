# Monaco 编辑光标被受控 value 打飞

Status: implemented

## 决策与理由

对齐 IDEA：本地击键只改模型，不把同一 buffer 经 React `value` 回灌。`@monaco-editor/react` 在受控 `value` 变化时会 `setValue`，重置选区；同时 `didOpen` / `reveal` 若依赖 `fileContent`，每键都会重开文档或跳回定位行。改为 `contentRevision` 仅在打开文件与磁盘外部变更时递增，Monaco 用 `defaultValue` + 按 revision `setValue`；击键只走 `onChange` → `didChange`。

## 放弃了什么

- **继续用受控 `value={content}`**：实现简单但对编辑器致命。
- **每键 `didOpen`**：LSP 已打开时应 `didChange`，不应每击重开。
