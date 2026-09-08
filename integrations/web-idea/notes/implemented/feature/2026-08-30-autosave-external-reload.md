# 自动保存与外部改动刷新

Status: implemented

## 决策与理由

编辑后 **800ms 防抖自动写盘**（仍保留 Cmd/Ctrl+S）。对当前打开文件 **800ms 轮询** `stat` 的 mtime/size；磁盘变化且本地未 dirty 时立刻 `readFile` 刷新缓冲并 `didChange` 给 jdtls。自己刚保存后忽略约 1.2s 轮询，避免回读抖动。

## 放弃了什么

- **fsnotify / SSE 实时推送**：轮询够用且不改 Gateway 协议面。
- **本地 dirty 时强行覆盖**：避免打字中被外部写冲掉；等自动保存落地后再跟盘。
