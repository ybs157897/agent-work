# RESEARCH：intellij-community 作为 web-idea Java 需求分析测试范本

> 汇总者复核：本报告负责参考仓库，不负责目标产品现状。第6节的诊断展示是调研时的假设示例；当前web-idea诊断UI未接入，以 current-capabilities.md 为准。根LICENSE.txt和JavaCompletionContributor.java已用GitHub连接器在同一固定SHA抽验。原始来源哈希与最终集成Git版本分别标识，不将二者混为同一快照。

## 结论先行

1. **取证锚点**：`JetBrains/intellij-community` 默认分支为 `master`；本次固定 commit SHA 为 `5ada6f537896bdf5d549ac12cd7f3e38ff637093`（采集起点 UTC `2026-09-08T13:37:52Z`，收束 UTC `2026-09-08T13:42:37Z`）。未全量克隆；用匿名 REST `contents` 按目录抽样 + `raw.githubusercontent.com` 校验文件存在性。
   来源：https://api.github.com/repos/JetBrains/intellij-community · https://api.github.com/repos/JetBrains/intellij-community/commits/master

2. **Community 源码可见的 Java 能力族**（编辑/补全、导航引用、检查诊断、重构、格式导入、项目/JDK/Maven/Gradle、构建运行、JUnit/TestNG、调试、反编译/外部库、代码生成等）在仓库 `java/`、`platform/`、`plugins/`、`jps/` 等目录有**模块级源码证据**；下列每族给出至少一条固定 SHA 的 blob/raw URL。**本次为目录抽样，不穷尽所有 action/inspection/refactoring。**

3. **开源 Community ≠ 营销页完整 IntelliJ**：本仓库 README 自述为 IDE 的 **open-source part**；官方 Unified FAQ 明确 OSS build **不含**若干付费/闭源能力（AI Assistant、完整 Spring 工具链、Profiler、HTTP Client、部分数据工具等需 Ultimate 订阅或 Marketplace 插件）。`plugins/` 抽样未见 `spring`/`database`/`sql`/`jpa` 等目录名——**不得凭营销页假定仓库含 Ultimate 功能。**
   来源：https://raw.githubusercontent.com/JetBrains/intellij-community/5ada6f537896bdf5d549ac12cd7f3e38ff637093/README.md · https://lp.jetbrains.com/intellij-idea-unified-faq

4. **对 web-idea（React + Monaco + Go Gateway + Eclipse JDT LS）**：IntelliJ **PSI / Index / Platform JVM 模块不能直接嵌入浏览器**；可借鉴交互与能力清单，经 **服务器侧语言协议/自研服务**再实现。JDT LS「潜在支持」≠ web-idea「已实现」——对照表一律标 **待确认**。

5. **许可证**：根目录 `LICENSE.txt` 为 *JetBrains Open-Source Build Terms*，并声明开源软件受 **Apache 2.0** 约束；另有 `NOTICE.txt`、`license/`、Fernflower `LICENSE.txt`、`libraries/` 第三方嵌入库说明。本文只给 URL，**不复制大段条款、不宣称法律结论**。

6. **需求冲突例（真实、适合本次流程）**：若现有 web-idea 集成按**只读**交付，而参考 IDE（Community）默认具备**重构/保存写回**——这是产品范围冲突，**不宣称用户已选择开放写入**；须在需求确认中显式裁决。

---

## 0. 采集方法与未核实边界

| 项 | 事实 |
|---|---|
| 方法 | `curl` 匿名 `api.github.com` + `raw.githubusercontent.com`；不使用 `gh`；不全量克隆 |
| 默认分支 | `master` |
| 固定 SHA | `5ada6f537896bdf5d549ac12cd7f3e38ff637093` |
| 仓库规模（API `size`） | 约 5,917,669 KB（约 5.6+ GiB 量级，仅作规模提示） |
| Stars（瞬时） | 20530 |
| 递归 tree 陷阱 | 对 `/git/trees/{sha}?recursive=0` 仍得到 `truncated: true`、约 48299 条——GitHub 文档语义下 **只要带 `recursive` 参数即走递归树**；截断结果**不可当作完整树**。本次改用 **单层 `contents` API** |
| 未核实 | 各模块运行时行为、完整 action 清单、与某一 IntelliJ 发行版功能矩阵的逐项对齐、web-idea 现网真实能力、第三方许可证兼容性法律意见 |

来源：
- https://api.github.com/repos/JetBrains/intellij-community
- https://docs.github.com/en/rest/git/trees（递归/截断行为说明，官方 REST 文档）
- 本次本地 API 响应（未落盘仓库外文件）

---

## 1. Java 功能族 × Community 源码路径证据（固定 SHA）

> 约定：blob 形如 `https://github.com/JetBrains/intellij-community/blob/<SHA>/<path>`。标明 **抽样不穷尽**。

### 1.1 编辑 / 补全

| 证据路径 | 说明 |
|---|---|
| `platform/completion/` | Platform 补全前后端拆分模块目录 |
| `java/java-impl/src/com/intellij/codeInsight/completion/JavaCompletionContributor.java` | Java 补全贡献者源码 |
| `java/java-impl/resources/intellij.java.impl.xml` | 注册 `codeInsight.completion.command.factory` 等 Java 补全扩展点 |

固定 SHA URL：
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-impl/src/com/intellij/codeInsight/completion/JavaCompletionContributor.java
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/platform/completion/common/resources/intellij.platform.completion.common.xml

### 1.2 导航 / 引用 / 查找

| 证据路径 | 说明 |
|---|---|
| `platform/find/` | Find in Files / Usage 相关平台模块 |
| `platform/indexing-api/src/com/intellij/psi/search/PsiSearchHelper.java` | 搜索 API |
| `java/java-indexing-api/src/com/intellij/psi/search/searches/ClassInheritorsSearch.java` | 继承者搜索（导航/引用类能力） |

固定 SHA URL：
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/platform/find/resources/intellij.platform.find.xml
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/platform/indexing-api/src/com/intellij/psi/search/PsiSearchHelper.java
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-indexing-api/src/com/intellij/psi/search/searches/ClassInheritorsSearch.java

### 1.3 检查 / 诊断（Inspections）

| 证据路径 | 说明 |
|---|---|
| `java/java-impl-inspections/` | Java inspections 实现模块 |
| `java/java-analysis-api/` | Java 分析 API 模块 |
| `jvm/jvm-analysis-*` | JVM 级分析/quickFix 模块族 |

固定 SHA URL：
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-impl-inspections/resources/intellij.java.impl.inspections.xml
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-analysis-api/resources/intellij.java.analysis.xml

### 1.4 重构

| 证据路径 | 说明 |
|---|---|
| `java/java-impl-refactorings/` | Java 重构实现（含 Move/Introduce 等扩展点） |
| `platform/refactoring/` | 平台重构框架 |
| `plugins/rareJavaRefactorings/` | 额外 Java 重构插件（Remove Middle Man 等） |

固定 SHA URL：
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-impl-refactorings/resources/intellij.java.impl.refactorings.xml
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/plugins/rareJavaRefactorings/resources/META-INF/plugin.xml

### 1.5 格式化 / 优化导入

| 证据路径 | 说明 |
|---|---|
| `platform/lang-impl/.../ReformatCodeProcessor.java` | 重格式化处理器 |
| `platform/lang-impl/.../OptimizeImportsProcessor.java` | 优化导入处理器 |
| `platform/core-api/.../CodeStyleManager.java` | 代码风格管理 API |
| `platform/code-style-api/` | 代码风格 API 模块 |

固定 SHA URL：
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/platform/lang-impl/src/com/intellij/codeInsight/actions/ReformatCodeProcessor.java
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/platform/lang-impl/src/com/intellij/codeInsight/actions/OptimizeImportsProcessor.java
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/platform/core-api/src/com/intellij/psi/codeStyle/CodeStyleManager.java

### 1.6 项目模型 / JDK / Maven / Gradle

| 证据路径 | 说明 |
|---|---|
| `platform/projectModel-api/.../ProjectRootManager.java` | 项目根/SDK 相关 API 入口之一 |
| `plugins/maven/` + `plugin.xml` | Maven 工程导入、pom 编辑、goal 运行等（插件自述） |
| `plugins/gradle/` + `plugin.xml` | Gradle 导入、同步、任务运行等（插件自述） |
| `plugins/eclipse/` | Eclipse 工程互操作（与 JDT 生态相关，但≠嵌入 JDT LS） |
| `java/mockJDK-*` | 测试用 mock JDK 树（说明 JDK 模型在工程中的地位） |

固定 SHA URL：
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/platform/projectModel-api/src/com/intellij/openapi/roots/ProjectRootManager.java
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/plugins/maven/plugin/resources/META-INF/plugin.xml
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/plugins/gradle/plugin/resources/META-INF/plugin.xml

### 1.7 构建 / 运行

| 证据路径 | 说明 |
|---|---|
| `java/compiler/` | Java 编译器集成（含 `Compiler` 扩展点） |
| `java/execution/` | Java 运行配置/执行 |
| `jps/` | JetBrains Project System（构建模型与 builders） |
| `platform/execution*` | 通用执行框架 |

固定 SHA URL：
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/compiler/openapi/resources/intellij.java.compiler.xml
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/execution/openapi/resources/intellij.java.execution.xml
- https://github.com/JetBrains/intellij-community/tree/5ada6f537896bdf5d549ac12cd7f3e38ff637093/jps

### 1.8 JUnit / TestNG

| 证据路径 | 说明 |
|---|---|
| `plugins/junit/` | JUnit 3/4/5/6 创建、导航、运行、结果视图（plugin 自述） |
| `plugins/testng/` | TestNG 运行配置与引用贡献 |
| `platform/smRunner`、`platform/testRunner` | 测试运行器平台支撑（目录级） |

固定 SHA URL：
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/plugins/junit/resources/META-INF/plugin.xml
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/plugins/testng/resources/META-INF/plugin.xml

### 1.9 调试

| 证据路径 | 说明 |
|---|---|
| `java/debugger/` | Java 调试器（openapi/impl/agent/JDI 等） |
| `platform/xdebugger-api/` | 通用调试器 API |

固定 SHA URL：
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/debugger/openapi/resources/intellij.java.debugger.xml
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/platform/xdebugger-api/resources/intellij.platform.debugger.xml

### 1.10 反编译 / 字节码 / 外部库

| 证据路径 | 说明 |
|---|---|
| `plugins/java-decompiler/` | Fernflower 反编译引擎 + IDE 插件（`.class` 查看扩展） |
| `plugins/ByteCodeViewer/` | ASM 格式字节码查看 |
| `libraries/` | 产品内嵌第三方库模块（README 说明用途） |

固定 SHA URL：
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/plugins/java-decompiler/plugin/resources/META-INF/plugin.xml
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/plugins/java-decompiler/engine/README.md
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/plugins/ByteCodeViewer/resources/META-INF/plugin.xml
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/libraries/README.md

### 1.11 代码生成（模板 / Generate）

| 证据路径 | 说明 |
|---|---|
| `java/java-impl/resources/fileTemplates/internal/*.java.ft` | Class/Interface/Record 等文件模板 |
| `java/java-impl/resources/fileTemplates/code/*.ft` | 方法体、Override、Javadoc 等代码模板 |
| `intellij.java.impl.xml` | `generateAccessorProvider`、`GenerateToString*` 等扩展点 |

固定 SHA URL：
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-impl/resources/fileTemplates/internal/Class.java.ft
- https://github.com/JetBrains/intellij-community/tree/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-impl/resources/fileTemplates/code
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-impl/resources/intellij.java.impl.xml

### 1.12 PSI / Index（平台内核，跨能力底座）

| 证据路径 | 说明 |
|---|---|
| `platform/core-api/.../PsiElement.java` | PSI 根类型 |
| `java/java-psi-api/.../PsiJavaFile.java` | Java PSI |
| `java/java-indexing-api/`、`platform/indexing-api/` | 索引 API |

固定 SHA URL：
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/platform/core-api/src/com/intellij/psi/PsiElement.java
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-psi-api/src/com/intellij/psi/PsiJavaFile.java
- https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-indexing-api/resources/intellij.java.indexing.xml

**再次声明**：上表为功能族抽样证据，**不是** Community Java 全部 action/inspection 穷尽列表。

---

## 2. 开源 Community 能力 vs 需额外产品/插件的能力

### 2.1 本仓库定位（开源部分）

- README：本仓库是 JetBrains IDEs 代码库的 **open-source part**，并作为 IntelliJ Platform 开发基础。
  来源：https://raw.githubusercontent.com/JetBrains/intellij-community/5ada6f537896bdf5d549ac12cd7f3e38ff637093/README.md
  HTML：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/README.md

### 2.2 官方：Unified 发行中「免费核心」与 Ultimate 订阅

- Unified FAQ：单一安装包；**核心 Java/Kotlin 免费**；**Ultimate 订阅**解锁高级能力（文中列举 Spring 深度工具、JVM 生态扩展、数据工具、Profiler、HTTP Client、AI 等）。
  来源：https://lp.jetbrains.com/intellij-idea-unified-faq
- 博客/帮助页同主题（产品形态变迁背景，非源码证据）：
  https://blog.jetbrains.com/idea/2025/12/intellij-idea-unified-release/
  https://www.jetbrains.com/help/idea/intellij-idea-single-distribution.html

### 2.3 OSS build 明确「不含」的能力（FAQ Open source 节）

官方列出 open-source build **不包含**例如：Backup and Sync、LSP support（指 IDE 内 LSP API 相关闭源部分）、Package Checker、AI ranking / AI Assistant、Qodana plugin、部分本地化插件、Kotlin Notebook、Code With Me 等；并称多数可通过 Marketplace 插件获得。
来源：https://lp.jetbrains.com/intellij-idea-unified-faq

### 2.4 与本次 `plugins/` 目录抽样的交叉核对（弱证据，待确认）

在固定 SHA 的 `plugins/` 一层目录名中，**未出现**含 `spring` / `database` / `sql` / `jpa` / `hibernate` / `kubernetes` / `profiler` / `http` 等子串的插件目录（抽样自 contents API）。
→ 支持「勿把 Ultimate 营销能力当成仓库内开源模块」；**不等于**证明商业发行版永不通过其他仓库/闭源模块提供这些能力。
来源：https://api.github.com/repos/JetBrains/intellij-community/contents/plugins?ref=5ada6f537896bdf5d549ac12cd7f3e38ff637093

### 2.5 对需求分析的操作含义

| 归类 | 建议用法 |
|---|---|
| 本仓库有模块/plugin.xml 证据的 Java 核心族 | 可作为 **Community 参考基线** 进入对照表 |
| FAQ/营销标为 Ultimate 或 Marketplace 的能力 | 标 **需额外产品/插件 · 待确认**，不写入「Community 源码已含」 |
| 统一发行后「新增免费」项（如基础 Spring 高亮、基础 DB schema） | 可能 **不在** 本 OSS 树或不全在；**待确认**是否进入 web-idea 范围 |

---

## 3. 对 web-idea 架构：可嵌入 vs 仅可借鉴

**给定架构（用户陈述，非本仓库事实）**：React + Monaco（浏览器）+ Go Gateway + Eclipse JDT LS（服务器语言服务）。

| IntelliJ Community 概念 | 能否直接嵌入浏览器 | 对 web-idea 的合理借鉴方式 |
|---|---|---|
| PSI（`PsiElement` 等） | **否**（JVM 内存模型/IDE 进程内 API） | 借鉴「语义树/符号」交互；实现应对齐 **JDT LS / 自研服务** 的协议结果 |
| Index / Stub 索引 | **否**（平台索引子系统，依赖 IDE 进程与磁盘索引） | 借鉴「索引未就绪」UX；由服务器索引状态机表达 |
| IntelliJ Platform 模块（Swing/Jewel UI、ActionSystem、ProjectModel） | **否**（桌面 IDE 运行时） | 只借鉴信息架构与快捷操作清单；UI 用 Monaco/React 重做 |
| 补全/诊断/导航/重构的**产品交互** | 不可搬二进制；可搬需求 | 经 LSP（或扩展 RPC）映射到 Monaco；**能力以 web-idea+Gateway+JDT LS 实测为准** |
| JPS/编译器进程、调试器 JDI | **否**（本地/远程 JVM 工具链） | 若需要，属服务器侧作业系统；**待确认**是否在范围 |
| Fernflower 反编译引擎（Apache-2.0 源码在仓） | 不可在浏览器直接跑完整引擎（除非另行 WASM/服务化，**未核实**） | 可考虑服务端反编译只读展示；**待确认**许可与集成策略 |

**硬约束（需求表述）**：

- 不要把「JDT LS 文档/潜在支持的 LSP 方法」写成「web-idea 已实现」。
- 不要把「Community 源码存在某重构」写成「浏览器端可调用 IntelliJ PSI」。
- 对照表中「实现路径」建议三选一标注：**Monaco 本地** / **经 Gateway+JDT LS** / **需新服务**——均为 **待确认**。

PSI/Index 证据 URL（见 §1.12）。

---

## 4. 许可证与第三方约束（URL only）

| 文件 | 固定 SHA URL | 摘记（非法律结论） |
|---|---|---|
| 根 `LICENSE.txt` | https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/LICENSE.txt | *JetBrains Open-Source Build Terms*；文内指向 Apache 2.0；含 Third-Party Software 条款引用 |
| 根 `NOTICE.txt` | https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/NOTICE.txt | IntelliJ IDEA / JetBrains 版权声明 |
| `license/` 目录 | https://github.com/JetBrains/intellij-community/tree/5ada6f537896bdf5d549ac12cd7f3e38ff637093/license | 若干第三方许可文本（如 javahelp、javolution、saxon、yourkit redistributable 等） |
| Fernflower `LICENSE.txt` | https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/plugins/java-decompiler/engine/LICENSE.txt | Apache License 2.0 全文 |
| Fernflower `NOTICE.txt` | https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/plugins/java-decompiler/engine/NOTICE.txt | 引擎 NOTICE |
| `libraries/README.md` | https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/libraries/README.md | 说明为 product-embedded third-party library modules |
| Apache 2.0 原文（LICENSE 内链） | https://www.apache.org/licenses/LICENSE-2.0 | 根 LICENSE 引用 |

**边界**：未枚举 `libraries/` 下全部依赖的许可证；未评估与 web-idea 许可证的兼容性；**不做法律结论**。

---

## 5. 功能对照骨架（12–20 项）+ 异常场景建议

> 全部范围建议标 **待确认**。Community 列 =「源码/插件证据存在」；web-idea 列勿填「已实现」，除非另有实测证据（本次无）。

| # | 能力项 | Community 源码证据（摘要） | web-idea 建议映射 | 建议范围 |
|---|---|---|---|---|
| 1 | 语法高亮/基本编辑 | Platform editor + Java syntax 模块族 | Monaco 本地 | 待确认 |
| 2 | 代码补全 | `JavaCompletionContributor` 等 | Gateway → JDT LS completion | 待确认 |
| 3 | 悬停文档 | Java PSI / documentation 相关（未深挖全部） | LS hover | 待确认 |
| 4 | 转到定义 / 查找引用 | indexing + find/usage | LS definition/references | 待确认 |
| 5 | 诊断/检查 | `java-impl-inspections` | LS diagnostics（≠全量 IDEA inspection） | 待确认 |
| 6 | 快速修复 | analysis/quickFix 模块族 | LS codeAction | 待确认 |
| 7 | 重命名/移动等重构 | `java-impl-refactorings` | LS rename/…；写回策略见冲突例 | 待确认 |
| 8 | 格式化 | `ReformatCodeProcessor` | LS formatting / Monaco | 待确认 |
| 9 | 优化导入 | `OptimizeImportsProcessor` | LS organizeImports 或等价 | 待确认 |
| 10 | 工程导入 Maven | `plugins/maven` | Gateway 工程服务 | 待确认 |
| 11 | 工程导入 Gradle | `plugins/gradle` | Gateway 工程服务 | 待确认 |
| 12 | JDK/模块类路径 | `ProjectRootManager` 等 | 服务器 JDK 配置 | 待确认 |
| 13 | 编译/构建 | `java/compiler` + `jps` | 远端构建作业 | 待确认 |
| 14 | 运行 Application | `java/execution` | 远端 run（权限敏感） | 待确认 |
| 15 | JUnit | `plugins/junit` | 远端 test runner | 待确认 |
| 16 | TestNG | `plugins/testng` | 远端 test runner | 待确认 |
| 17 | 调试 | `java/debugger` + xdebugger | 远端 debug 适配 | 待确认 |
| 18 | 反编译只读 | Fernflower 插件 | 服务端反编译预览 | 待确认 |
| 19 | 字节码查看 | ByteCodeViewer | 服务端/只读视图 | 待确认 |
| 20 | 从模板新建类 | `fileTemplates/internal` | 写路径依赖「是否可写」 | 待确认 |

### 建议纳入测试的异常场景（均待确认优先级）

1. **索引/语言服务未就绪**：补全/引用空结果或排队提示（对标 IDEA indexing / LS initialize）。
2. **无 JDK / JDK 不匹配**：类路径断裂、诊断洪水。
3. **编译失败**：运行/调试入口禁用或明确错误。
4. **重构冲突**：预览与应用不一致、部分文件失败回滚。
5. **多模块/多根工程**：跨模块引用、错误模块上下文。
6. **只读文件 / 只读工作区**：重构与保存被拒（见 §6）。
7. **远程/权限**：Gateway 鉴权失败、无写权限、沙箱拒绝执行。
8. **取消与断线**：长请求取消、WebSocket/LSP 断线重连后的脏状态。
9. **外部库无源码**：仅 class/反编译视图与导航降级。
10. **大仓库超时**：目录抽样/索引超时的产品文案（对标本仓库体量风险）。

---

## 6. 需求分析冲突例：只读集成 vs 参考 IDE 重构/保存

**冲突陈述（适合带入本次需求评审，不作裁决）**：

- **现状假设（用户侧既有集成方向）**：web-idea 对仓库/工作区按 **只读浏览 + 语言智能（经 JDT LS）** 交付——允许打开、导航、诊断展示，但 **不默认开放写回**。
- **参考系（Community 源码事实）**：IntelliJ Community 的重构模块、格式化/优化导入、文件模板生成等，在桌面 IDE 中以 **可写工程** 为前提（见 §1.4、§1.5、§1.11 源码路径）。
- **冲突点**：若需求文案写「对标 IDEA 重构体验」却未同步「写权限/保存/预览应用」决策，测试会同时要求「只读安全」与「多文件变更写回」——互相排斥。
- **可选方向（均待确认，不代替用户决定）**：
  A. 明确 **只读模式**：重构/生成仅预览或禁用；
  B. **受控写入**（分支/PR/沙箱）；
  C. 分角色权限。
- **本文不宣称用户已选择开放写入。**

---

## 7. 关键来源索引（汇总）

- 仓库：https://github.com/JetBrains/intellij-community
- 固定 commit：https://github.com/JetBrains/intellij-community/commit/5ada6f537896bdf5d549ac12cd7f3e38ff637093
- API 元数据：https://api.github.com/repos/JetBrains/intellij-community
- README：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/README.md
- LICENSE / NOTICE：见 §4
- Unified FAQ：https://lp.jetbrains.com/intellij-idea-unified-faq
- Unified 发布说明：https://blog.jetbrains.com/idea/2025/12/intellij-idea-unified-release/

---

## 8. 未核实边界（收束）

- 未克隆仓库；未跑构建；未枚举全部 Java actions/inspections。
- 未核对 web-idea / Go Gateway / JDT LS 的现网实现与配置。
- 未对 Ultimate 闭源模块做源码取证（亦不在本仓库保证范围内）。
- 许可证仅列入口 URL，无合规签字意见。
- 统一发行后「新增免费功能」与 OSS 树的对应关系 **未逐项核实**。

<!-- RESEARCH-DONE -->
