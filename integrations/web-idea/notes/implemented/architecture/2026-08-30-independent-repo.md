# 独立仓库承载 web-idea

Status: implemented

## 决策与理由

在 `code/web-idea` 新建独立 Git 仓库承载「浏览器 Java 只读 IDE 子集」，而不是塞进 `agent-work` 单体。理由：jdtls 运维与宿主 Agent 编排生命周期不同；要保持可嵌入但不绑死。

## 放弃了什么

- 在 `agent-team-workbench` 内直接加 `/explorer` 模块（联调近，但 CI/概念/发布耦合）。
- 直接 fork Theia/code-server 当产品（体量与嵌入成本不匹配）。

## 复活条件

若连续两个季度唯一生产部署形态都是「仅随 agent-work 发布、无独立用户」，可评估迁回 monorepo 子目录——前提是抽出独立 Go module 边界不变。
