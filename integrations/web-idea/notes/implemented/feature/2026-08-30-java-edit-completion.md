# Java 可编辑保存与成员补全

Status: implemented

## 决策与理由

ADR-007：在 jail 内允许 UTF-8 文本写盘，并用 jdtls `didChange` + `completion`（`.` 触发）提供 `obj.` 方法列表。Monaco 默认可写；Cmd/Ctrl+S → `PUT …/fs/file`；dirty 用 tab `•` 提示。

## 放弃了什么

- **重构/格式化写盘、多文件原子提交**：信任面仍收口。
- **未索引时的本地假补全**：只走 jdtls，避免误导。
- **新建深层路径自动 mkdir**：WriteFile 要求父目录已存在。
