# Java功能参考与差异

Source-ID: SRC-REF-01
Kind: reference_capability
Authority: reference_only_not_scope_approval
Reference-Commit: 5ada6f537896bdf5d549ac12cd7f3e38ff637093

这是有界模块/文件取证，不是穷尽全部Java action。IntelliJ JVM/PSI模块不能直接作为React浏览器组件加载；协议接入、重实现或服务化是待确认方案。所有功能范围仍待用户确认。

## JAVA-001 编辑与高亮
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-impl/resources/intellij.java.impl.xml
独立web-idea：implemented；工作台嵌入：partial_readonly
建议实现方向（未确认）：Monaco及保存协议
需要检查的异常：只读约束、失败保留、并发外部修改

## JAVA-002 Java补全
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-impl/src/com/intellij/codeInsight/completion/JavaCompletionContributor.java
独立web-idea：implemented；工作台嵌入：partial_readonly
建议实现方向（未确认）：JDT LS completion
需要检查的异常：索引未就绪、过期响应、只读表面是否适用

## JAVA-003 悬停文档
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-psi-api/src/com/intellij/psi/PsiJavaFile.java
独立web-idea：not_exposed；工作台嵌入：not_exposed
建议实现方向（未确认）：JDT LS hover或服务
需要检查的异常：模块级证据；精确documentation功能清单仍需细化

## JAVA-004 定义与引用导航
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/platform/indexing-api/src/com/intellij/psi/search/PsiSearchHelper.java
独立web-idea：implemented；工作台嵌入：implemented
建议实现方向（未确认）：JDT LS definition/references
需要检查的异常：零/多结果、外部库无源码、返回位置、索引状态

## JAVA-005 诊断与检查
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-impl-inspections/resources/intellij.java.impl.inspections.xml
独立web-idea：not_exposed；工作台嵌入：not_exposed
建议实现方向（未确认）：诊断模型与UI
需要检查的异常：JDT LS诊断不等于完整IntelliJ inspections

## JAVA-006 快速修复
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-analysis-api/resources/intellij.java.analysis.xml
独立web-idea：not_exposed；工作台嵌入：not_exposed
建议实现方向（未确认）：codeAction与写入合同
需要检查的异常：预览过期、越界修改、部分失败、待确认写权限

## JAVA-007 重命名与重构
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-impl-refactorings/resources/intellij.java.impl.refactorings.xml
独立web-idea：not_exposed；工作台嵌入：not_exposed
建议实现方向（未确认）：rename/refactor与受控写入
需要检查的异常：跨文件/模块、冲突、原子应用、撤销、只读

## JAVA-008 代码格式化
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/platform/lang-impl/src/com/intellij/codeInsight/actions/ReformatCodeProcessor.java
独立web-idea：not_exposed；工作台嵌入：not_exposed
建议实现方向（未确认）：formatting接口
需要检查的异常：不同风格配置、未保存缓冲区、只读

## JAVA-009 优化导入
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/platform/lang-impl/src/com/intellij/codeInsight/actions/OptimizeImportsProcessor.java
独立web-idea：not_exposed；工作台嵌入：not_exposed
建议实现方向（未确认）：organizeImports接口
需要检查的异常：同名类、未解析依赖、误删、只读

## JAVA-010 Maven工程导入
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/plugins/maven/plugin/resources/META-INF/plugin.xml
独立web-idea：partial；工作台嵌入：partial
建议实现方向（未确认）：工程服务和JDT LS
需要检查的异常：依赖下载失败、离线、多模块、生成目录副作用

## JAVA-011 Gradle工程导入
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/plugins/gradle/plugin/resources/META-INF/plugin.xml
独立web-idea：partial；工作台嵌入：partial
建议实现方向（未确认）：工程服务和JDT LS
需要检查的异常：marker识别不等于完整模型；toolchain与多模块

## JAVA-012 JDK与模块类路径
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/platform/projectModel-api/src/com/intellij/openapi/roots/ProjectRootManager.java
独立web-idea：partial；工作台嵌入：partial
建议实现方向（未确认）：JDK与工程上下文
需要检查的异常：宿主JDK与项目语言级别分离；8/11/17/21待定

## JAVA-013 构建编译
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/compiler/openapi/resources/intellij.java.compiler.xml
独立web-idea：not_exposed；工作台嵌入：not_exposed
建议实现方向（未确认）：需要执行服务
需要检查的异常：失败日志、取消、环境、预算；不得并列新Run权威

## JAVA-014 运行Java程序
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/execution/openapi/resources/intellij.java.execution.xml
独立web-idea：not_exposed；工作台嵌入：not_exposed
建议实现方向（未确认）：需要执行服务
需要检查的异常：运行配置、权限、进程取消、输入输出

## JAVA-015 JUnit
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/plugins/junit/resources/META-INF/plugin.xml
独立web-idea：not_exposed；工作台嵌入：not_exposed
建议实现方向（未确认）：需要测试执行服务
需要检查的异常：发现与执行分开；版本、失败、报告绑定

## JAVA-016 TestNG
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/plugins/testng/resources/META-INF/plugin.xml
独立web-idea：not_exposed；工作台嵌入：not_exposed
建议实现方向（未确认）：需要测试执行服务
需要检查的异常：框架版本、参数、取消、结果追溯

## JAVA-017 Java调试
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/debugger/openapi/resources/intellij.java.debugger.xml
独立web-idea：not_exposed；工作台嵌入：not_exposed
建议实现方向（未确认）：需要调试协议服务
需要检查的异常：断点、断连、进程归属、权限

## JAVA-018 反编译阅读
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/plugins/java-decompiler/plugin/resources/META-INF/plugin.xml
独立web-idea：unverified；工作台嵌入：unverified
建议实现方向（未确认）：可能需要反编译服务
需要检查的异常：源码不可得、许可证、生成内容非原始源码

## JAVA-019 字节码查看
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/plugins/ByteCodeViewer/resources/META-INF/plugin.xml
独立web-idea：unverified；工作台嵌入：unverified
建议实现方向（未确认）：需要专门只读服务
需要检查的异常：二进制解析失败、大小限制、版本

## JAVA-020 代码生成模板
参考：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/java/java-impl/resources/fileTemplates/internal/Class.java.ft
独立web-idea：not_exposed；工作台嵌入：not_exposed
建议实现方向（未确认）：模板与写入合同
需要检查的异常：同名文件、包路径、覆盖确认、模板来源

## 来源与许可证边界
根许可：https://github.com/JetBrains/intellij-community/blob/5ada6f537896bdf5d549ac12cd7f3e38ff637093/LICENSE.txt
IntelliJ开源仓库不等于商业发行版全部功能。直接复用代码前须逐模块核对许可和第三方依赖；本范本只保留链接与简短结论，不移植IntelliJ代码。
