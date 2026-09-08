# ADR-002: Monaco + jdtls，不嵌入 Theia / code-server

## Status

Accepted

## Date

2026-08-30

## Context

开源侧存在 Theia、Eclipse Che、code-server 等 Web IDE。也存在 Monaco Language Client + jdtls 示例。需要决定「整机嵌入」还是「自研壳 + 语言服务」。

## Options Considered

### A: 嵌入 code-server / openvscode-server + vscode-java

- Pros: Java 体验成熟
- Cons: 整站 IDE；鉴权/主题/双壳；难嵌进现有控制面为侧栏

### B: 嵌入 Eclipse Theia

- Pros: 为云 IDE 设计；有 Java 路径
- Cons: 平台体量大；定制与升级成本高

### C: Monaco + eclipse.jdt.ls + 自研 Gateway（本决策）

- Pros: 可嵌；边界清晰；复用 LSP 标准；与工作量预期匹配
- Cons: 需自建生命周期与 URI 映射；无现成完整产品

### D: 复用 Lithe / IntelliJ 远程

- Pros: 语义强
- Cons: 无官方 Web 组件；Projector 停更；无法嵌入

## Decision

选择 **C**。可参考 TypeFox 示例的帧格式，但生产鉴权与工作区模型自研。

## Consequences

- 依赖 jdtls 运维质量；
- 不与 VS Code 扩展市场兼容（可接受）；
- 若未来证明「整机 IDE」需求上升，以 rejected 复活条件重新评估 A/B。
