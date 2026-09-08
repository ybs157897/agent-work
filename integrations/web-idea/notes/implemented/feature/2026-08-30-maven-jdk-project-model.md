# Maven / JDK Project 模型（只读）

Status: implemented

## 决策与理由

对齐 IDEA Project 视图的「工程感」而不引入可写编辑：Gateway `GET …/project` 解析根 `pom.xml`（modules / 声明依赖 / `java.version`）并探测宿主机 JDK；Web 状态栏显示 JDK/Maven/artifact，树顶显示模块名，底部挂 External Libraries + JDK 虚拟节点；`pom.xml` 用独立图标，子模块目录用 `moduleJava`。

## 放弃了什么

- **可写编辑 / 保存**：路线图明确延期；「写代码」在本刀仅指围绕 Maven/JDK 的阅读交互，不是写盘。
- **完整 Maven 解析（effective POM / 传递依赖）**：成本高；声明依赖足够先像 IDEA。
- **jdtls classpath 实时同步**：等 sidecar 稳定后再接；当前不阻塞 UI。
