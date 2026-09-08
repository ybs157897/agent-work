# ADR-007: Java 可编辑保存与 LSP 补全

## Status

Accepted

## Date

2026-08-30

## Context

ADR-001 将 MVP 定为只读导航。用户明确要求：可编辑保存，并在输入 `xxx.` 时由 jdtls 索引带出成员方法（completion）。审查场景仍在，但「轻改 + 智能补全」成为产品必需。

## Options Considered

### A: 继续只读，补全仅只读提示

- Pros: 信任面不变
- Cons: 不满足用户刚需

### B: 工作区内文本写盘 + jdtls didChange/completion（本决策）

- Pros: 贴合 IDEA 写代码手感；仍经 Gateway jail；不引入重构/调试
- Cons: 扩大写盘信任面；需处理脏缓冲与索引延迟

### C: 完整可写 IDE（重构、格式化写盘、多光标协作）

- Pros: 叙事完整
- Cons: 超出现阶段；与「轻量审查面板」定位冲突

## Decision

选择 **B**。部分取代 ADR-001「不可写」负向保证：

- **允许**：UTF-8 文本经 `PUT …/fs/file` 写回 jail；Monaco 可编辑；`textDocument/didChange` + `textDocument/completion`（含 triggerCharacter `.`）。
- **仍不做**：重构写盘、格式化整树、调试、多人协作、二进制写盘。

## Consequences

- 更新 vision / roadmap / lsp 契约与 OpenAPI；
- Gateway 写路径必须复用 Resolve jail，限制体积与 UTF-8；
- UI 显示 dirty / 保存状态；补全依赖 jdtls ServiceReady。
