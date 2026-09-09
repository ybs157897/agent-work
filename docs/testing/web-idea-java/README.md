# web-idea Java 能力需求分析测试范本

状态：范本准备完成；应用端到端与真实模型分析待验收。用于验证 agent-work 的多源需求分析、异常场景检查、开发确认回填与需求变更核对；不代表 Java 功能移植已经完成。

## 原始需求

> 就拿 web-idea 这个项目；https://github.com/JetBrains/intellij-community 是开源参考项目；要做的是集成它所涉及的 Java 功能，用这个作为当前测试范本。

目标项目是本机独立 web-idea 当前工作树；agent-work 中 integrations/web-idea 是需要单独标识的嵌入表面。IntelliJ Community 是参考来源，不能把目录存在或协议支持自动解释成可直接复用或已经集成。

## 范本要证明什么

1. 从用户原始需求、目标源码、知识材料与 Word/PPT/Markdown 附件形成有来源的功能和场景分析。
2. 将真实事实、模拟材料、建议、待确认规则和模拟确认事件区分开。
3. 发现独立应用可编辑与嵌入只读、参考重构涉及写入之间的冲突，列出影响并一次只问一个关键问题。
4. 开发回填确认后保存决定；收到新变更时指出受影响的旧结论，不能静默覆盖。
5. 来源不可读、附件解析失败和版本漂移必须显式暴露。

## 交付范围

本次交付一个有真实来源、模拟冲突材料和变更事件的测试包，以及可重复的输入组装和完整性检查。App 端附件上传/解析、代码引用和需求确认持久化是否完成，应另按产品实现验收；不能用本包的自检代替应用闭环通过。

功能范围尚未定案；“Java 功能”原始目标完整保留。首期分组是分析建议，不能自动缩成用户已批准的子集。

## 已准备的材料

| 材料 | 内容 |
| --- | --- |
| [Java能力对照](capability-matrix.json) | 20类功能族；真实目标现状、固定参考来源、建议实现方向和异常重点；范围均未代替用户定案 |
| [目标现状](current-capabilities.md) / [源码指纹](source-manifest.json) | 独立 web-idea 与 ATW 嵌入表面分别核对，12个关键源码文件SHA-256 |
| [IntelliJ参考取证](intellij-reference.md) | 固定提交 `5ada6f537896bdf5d549ac12cd7f3e38ff637093`，模块抽样；附[抽验记录](reference-verification.json) |
| [场景契约](scenario-contract.md) | 8种需求分析场景及可判断预期 |
| [输入与阶段清单](../../../testdata/requirements/web-idea-java/pack.json) | 12个来源、7个阶段；未来确认与变更不会进入初始输入 |
| [Word讨论稿](../../../testdata/requirements/web-idea-java/inputs/reading-scope.docx) | 模拟“首期只读”，每页标明未获用户确认 |
| [PPT评审提议](../../../testdata/requirements/web-idea-java/inputs/review-proposal.pptx) | 模拟“Rename/Quick Fix一键应用”，与Word形成可追溯冲突 |
| [Office QA](office-fixture-qa.md) | 两份附件均为2页；已渲染检查文字和布局 |
| [复核示例](../../../testdata/requirements/web-idea-java/examples/README.md) | 范本作者编写的预期结构，不是应用或模型运行结果，不注入模型输入 |

模拟知识导出没有生产知识库ID，也未写入真实知识库。模拟确认和变更分别作为后续阶段事件，不改变用户的实际决策。IntelliJ只引用来源，不在此范本复制或移植它的实现代码。

## 如何使用

在本任务工作树根目录运行：

```sh
python3 testdata/requirements/web-idea-java/assemble.py --check
python3 testdata/requirements/web-idea-java/test_assemble.py
python3 testdata/requirements/web-idea-java/assemble.py --stage initial --output /tmp/web-idea-java-initial.json
python3 testdata/requirements/web-idea-java/assemble.py --stage confirmed --output /tmp/web-idea-java-confirmed.json
python3 testdata/requirements/web-idea-java/assemble.py --stage changed --output /tmp/web-idea-java-changed.json
```

将组装后的JSON作为分析器输入，再按场景契约人工核对输出。不要把 `examples/` 或 `pack.json` 中的预期断言一起给分析器，避免预期答案泄漏。这个准备工具本身不调用模型，不创建Task/Run，也不发布知识。

其他阶段：`unreadable`、`stale_code`、`broken_attachment`、`untrusted_attachment`。故意缺失、过期或损坏的来源会保留在 `gaps`；生成JSON成功不等于所有来源已读取。`--check` 只接受阶段事先声明的缺口。

DOCX实际解析正文XML段落，PPTX按演示文稿关系顺序解析幻灯片文字，Markdown按行定位；不会拿手写expected.txt替代附件读取。旧式 `.doc/.ppt`、OCR、图表语义、SmartArt、备注/页眉页脚等不在该工具已验证范围内，缺少这些内容不能声称全文语义覆盖。

## 第一轮应问出的关键问题

> 这次Java集成范围是否包含会修改文件的编辑/重构，还是保持工作台嵌入只读？

这个问题保持未回答，不影响范本准备完成，也不授权开放写入。原始“集成Java功能”目标仍完整保留；20类功能只是当前取证骨架。

## 验收边界

本次已完成范本、真实Office文字提取和准备工具回归检查。[verification.json](verification.json) 保存实际验证结果。**工作台端到端、真实模型分析和Java功能移植均未运行或未实施**；应在需求分析产品流程接通后，用同一材料再验收。不要将范本自检绿灯换算为产品完成率。

工作树基线为 agent-work `651056c`；独立 web-idea 基线为 `2bbfb37` 加当时未提交内容。原始代码、主树 `index.js` 及业务数据未修改；本任务未提交、合并或推送。
