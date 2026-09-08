# ADR-004: Gateway 使用 Go

## Status

Accepted

## Date

2026-08-30

## Context

Gateway 需做 HTTP、WebSocket 代理、子进程管理、路径沙箱。团队周边（agent-team-workbench）以 Go 为主。TypeFox 示例为 Node。

## Options Considered

### A: Node/TypeScript Gateway

- Pros: 与 monaco-languageclient 示例同构，抄桥快
- Cons: 与宿主/运维栈分裂；双语言控制面

### B: Go Gateway（本决策）

- Pros: 与现有控制面技能一致；静态二进制；并发与进程管理成熟
- Cons: 需自行实现 LSP stdio ↔ WS 换档（工作量可控）

## Decision

选择 **B**。Web 仍为 TypeScript。

## Consequences

- `apps/gateway` 为 Go module；
- 可借鉴 Node 示例的帧语义，但不引入 Node 作为生产 Gateway。
