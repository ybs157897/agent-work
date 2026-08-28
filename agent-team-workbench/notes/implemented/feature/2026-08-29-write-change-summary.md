# Write/Edit 紧凑文件变更摘要

日期：2026-08-29
状态：implemented

## 现场契约

会话 `wi_01M14GZNJW5ZR8P686BEH6M93N` 的 Write 调用只提供：

- `tool.started`: `tool/call_id/args_summary/args`
- `tool.completed`: `output = "Wrote 24832 bytes to knowledge/prd/evolution-roadmap-next-phase.md"`

该事件没有旧文件快照、unified diff、`additions` 或 `deletions`。截断到 2000 字符的
`args.content` 也不能作为完整新文件，因此不得从字节数或残缺内容推算增删行。

## 展示契约

- 有 canonical `change_stats` 或可验证 unified diff：显示
  `N 个文件已更改 +A −D`，新增使用 success token，删除使用 error token。
- 只有 Write 的可靠字节结果：显示 `1 个文件已更改 · 24.8 KB`，不显示假的 `+0/−0`。
- 同一摘要同时出现在 44px 工具折叠行和展开后的单条工具列表；原始 output 仍可在详情中追溯。
- 紧凑摘要是静态信息，不伪装成可点击标签；整行 button 继续承担展开语义与可访问名称。

## Canonical 字段

Kimi adapter 将可验证的 Write 字节结果提升为：

```json
{
  "change_stats": {
    "operation": "write",
    "files": 1,
    "bytes": 24832,
    "path": "knowledge/prd/evolution-roadmap-next-phase.md"
  }
}
```

未来执行器只有在拥有旧/新快照或真实 diff 时才可追加 `additions/deletions`；缺失必须省略，
不能由前端估算。前端保留对 unified diff 的真实解析，供已经输出 diff 的 Edit/Write 工具使用。

## 回归钉

- adapter 测试确认 Write `tool.completed` 发出 bytes/path，且没有伪造增删行。
- store 测试确认 `change_stats` 完整进入 ChatMessage。
- model/render 测试覆盖 canonical 优先、unified diff 统计、Wrote bytes fallback、折叠态与展开态文案。
