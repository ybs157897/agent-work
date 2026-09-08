# ADR-005: Workspace Root 路径沙箱

## Status

Accepted

## Date

2026-08-30

## Context

FS API 与 LSP URI 映射若处理不当会导致任意文件读取。

## Options Considered

### A: 信任客户端传来的绝对路径

- Cons: 不可接受

### B: chroot / 容器每工作区一根

- Pros: 强隔离
- Cons: MVP 过重

### C: 应用层 root jail + symlink 解析校验（本决策）

- Pros: 实现快；单测可覆盖；配合本机可信租户假设足够
- Cons: 不如内核隔离硬

## Decision

选择 **C**。多租户不可信场景升级到容器隔离（部署文档），不改变 API 形状。

## Consequences

- 强制相对路径；详尽单测；
- 安全模型写明信任假设。
