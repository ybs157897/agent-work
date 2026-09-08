# ADR-006: 独立仓库，而非放入 agent-work 单体

## Status

Accepted

## Date

2026-08-30

## Context

能力由 agent workbench 场景激发，但 Web IDEA 有独立生命周期、依赖（jdtls）与部署形态。放进 `agent-work` 单体将拖累其 CI 与概念负担。

## Options Considered

### A: `agent-team-workbench` 内新目录

- Pros: 一键联调
- Cons: 依赖与发布耦合；违反「可嵌入但不绑死宿主」

### B: `code/web-idea` 独立仓库（本决策）

- Pros: 边界清晰；可单独版本；宿主经 HTTP 集成
- Cons: 跨仓联调需约定

## Decision

选择 **B**。路径：`/Users/yin/Documents/ybs/code/web-idea`。

## Consequences

- 与 agent-work 的集成以契约 + 深链/iframe 为后期工作；
- 文档不假设宿主仓库内相对路径为运行时依赖。
