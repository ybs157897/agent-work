# ADR-001: Java 只读导航范围

## Status

Superseded by [ADR-007](./ADR-007-java-edit-completion.md) for the 「不可写」负向保证；导航范围结论仍有效。

## Date

2026-08-30

## Context

需要在浏览器中提供接近 IDE 的读代码体验。完整 IDE（编辑、调试、重构、多语言）成本高，且与「AI 写码、人审查」主场景不完全重合。

## Options Considered

### A: 完整 Web IDE（多语言可写 + 调试）

- Pros: 产品叙事完整
- Cons: 人年规模；运维与安全面巨大

### B: Java 只读 + 目录浏览 + Definition/References（本决策）

- Pros: 贴合审查场景；可分刀交付；安全面可控
- Cons: 不能替代日常编码 IDE

### C: 仅目录 + 高亮，无语义跳转

- Pros: 几天可交付
- Cons: 不满足「点方法跳转」核心诉求

## Decision

选择 **B**。写入产品非目标清单；扩展可写/多语言必须新开 ADR。

## Consequences

- 路线图 P1/P2 清晰；
- 与 Lithe/IDEA 桌面方案并存，不声称替换；
- 宿主嵌入时定位为「审查面板」而非「主 IDE」。
