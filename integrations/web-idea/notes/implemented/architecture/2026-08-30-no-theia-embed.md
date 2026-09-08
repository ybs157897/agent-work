# 不嵌入 Theia / code-server

Status: implemented

## 决策与理由

采用 Monaco + jdtls + Go Gateway 自研壳，拒绝整机 Web IDE 嵌入。满足侧栏/深链嵌入与信任边界可控。

## 放弃了什么

- code-server + vscode-java：Java 成熟，但是整站产品。
- Eclipse Theia：云 IDE 框架，定制成本高。
- Lithe/IDEA Web：不存在可嵌组件。

## 复活条件

宿主明确要求「完整 VS Code 扩展兼容 + 多语言调试」且愿意承担双壳 UX 与独立运维团队 → 重新评估 code-server 旁路产品，而非替换本仓库 MVP。
