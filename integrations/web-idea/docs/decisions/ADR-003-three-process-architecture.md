# ADR-003: 三进程架构（web / gateway / jdtls）

## Status

Accepted

## Date

2026-08-30

## Context

jdtls 内存与稳定性波动大；浏览器不能安全直连语言服务；FS 与 LSP 都需要统一鉴权。

## Options Considered

### A: 浏览器直连 jdtls（TCP/WS 暴露）

- Cons: 致命安全问题；否决

### B: 单一 Node 进程既托管静态页又拉起 jdtls（示例形态）

- Pros: 原型快
- Cons: 与现有 Go 生态不一致；职责混杂；难嵌多宿主

### C: web + Go gateway + jdtls sidecar（本决策）

- Pros: 信任边界清晰；可杀可重启；FS/LSP 同鉴权
- Cons: 多一个进程与部署单元

## Decision

选择 **C**。Gateway 是唯一 TCB 入口。

## Consequences

- 部署文档以三进程为准；
- LSP 必须经代理（见 lsp-bridge.md）；
- 水平扩展需 sticky（MVP 单实例）。
