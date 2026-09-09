收到。本轮为只读验收：实际重读全部原件（Word 正文/页眉页脚、PPT 幻灯片/备注、三个 Markdown）、两个代码文件与知识条目，并新增读取 u07-readback-note.md。先并行发起全部读取。
本轮只读验收读取证据如下：

**附件（均经原件路径实际读取）**
- Word（reading-scope.docx）：正文全部段落提取，含范围建议（首期仅文件阅读、定义和引用；写入/重命名/一键应用需单独确认）、四类异常、待确认事项与"状态：pending"边界说明；页眉页脚均为"模拟测试材料｜未获用户确认 · SRC-DOCX-01"。与此前观察一致。
- PPT（review-proposal.pptx）：slide1（跨文件 Rename 与 Quick Fix、一键应用并保存，pending）、slide2（预览/人审/冲突/回滚/JDK 版本五项 pending）；两页备注均为"模拟测试材料｜未获用户确认 · SRC-PPTX-01，所有内容保持 pending"。与此前观察一致。
- original-request.md：原始需求 Decision-State: unresolved_scope，已确认工作方式段落不变。
- change-and-correction.md：外部依赖新增只读标识/来源路径提示提议与 JDK 降级规则不变、开发纠正段落，均与此前一致。
- u07-readback-note.md（新校验件）：正文实际读取，Readback marker 为 `U07-PATH-e0a767fd-da7a-47dc-a15b-f35c140b1f2b`，文件自声明"no product requirement or approval"。仅作路径读取校验来源，不转为业务事项/问题。

**代码**：`apps/web/src/lsp/session.ts`（517 行，`clientDocUri`/`pathFromClientUri`/`lspWsUrl`，SHA-256 `49918225…6cce75`）与 `apps/web/src/api/client.ts`（215 行，`writeFile` 在第 186 行，PUT 写盘，`c2d57f49…f5e22`）——两文件哈希与冻结 revision=2 中已声明的摘要逐字一致，无实质变化，不触发任何事项更新。

**知识**：经本 Run 授权工具直接读取 `kb_01M222X97MVQ0MB9Z9GJAQYR8F` 版本 1，content_digest 去前缀为 `cc2907ecb68b214b18687fcde934ef8b500dde549986d4a6825e905d682fdb6a`，与此前引用一致，JDK 缺失降级规则内容未变。

**未覆盖/限制**：docx/pptx 仅提取文字与页眉页脚/备注，版式与图形未读；知识工具的问询（ask）模式本轮一次返回未通过其内部校验，已改用固定 read 引用成功读取，结果完整；App.tsx、usageHits.ts、gtdu.ts、manager.go、package.json、AGENTS.md 不在本轮重读清单内（此前哈希已核验未漂移）。全部业务事项与问题未变，仅以 preserve 引用；不回答未决业务问题。

```atw-analysis
{
  "version": "chat-analysis/v1",
  "summary": "U07 最终只读验收：实际重读 Word 正文与页眉页脚、PPT 两页幻灯片与备注、original-request.md、change-and-correction.md，重读 apps/web/src/lsp/session.ts 与 apps/web/src/api/client.ts（SHA-256 与冻结 revision=2 已声明摘要逐字一致），并经授权知识工具读取 kb_01M222X97MVQ0MB9Z9GJAQYR8F 版本 1（content_digest 去前缀 cc2907…fdb6a，未变）；新增读取 u07-readback-note.md 校验件，正文 Readback marker 为 U07-PATH-e0a767fd-da7a-47dc-a15b-f35c140b1f2b，该文件自声明不含产品需求或批准，仅作为本轮路径读取校验来源登记，不转为业务事项或问题。所有业务材料与代码均无实质变化：12 个事项与 4 个问题全部仅以 preserve IDs 引用，不重写；items/questions 为空。限制：docx/pptx 仅提取文字（版式图形未读），知识 ask 问询一次未通过内部校验、改以 read 成功读取。已保存产品回填结论独立保留，不写入 item 正文；不批准、不发布任务、不改代码。",
  "sources": [
    {
      "id": "s-u07note",
      "kind": "attachment",
      "ref": "src_01M22RPGPGA3BX7MKWAY5390YS",
      "sha256": "c6c660e3c5a8cf1867800d86fbdee95346f922fe9e4bb45537b40ff5686675ed",
      "locator": "全文 5 行",
      "read_status": "read",
      "coverage": "全文读取：路径读取校验说明与 Readback marker",
      "limitations": "文件自声明为隔离传输校验标记，不含产品需求或批准；仅作本轮读取校验证据，不构成业务事项、问题或确认依据",
      "quote": "Readback marker: U07-PATH-e0a767fd-da7a-47dc-a15b-f35c140b1f2b"
    },
    {
      "id": "s-conv-u07",
      "kind": "conversation",
      "ref": "conversation:sha256:a62da7d784b45be3934165509ce7d139a9a9377873656c1a6a7806f0daaacdab",
      "sha256": "a62da7d784b45be3934165509ce7d139a9a9377873656c1a6a7806f0daaacdab",
      "locator": "用户 U07 只读验收指令",
      "read_status": "read",
      "coverage": "重读全部原件与两个代码文件、读取知识条目、登记 u07-readback-note.md 校验来源、preserve 全部当前事项与问题的要求",
      "limitations": "仅含本 Run 上下文内提供的对话内容"
    }
  ],
  "items": [],
  "questions": [],
  "preserve_item_ids": [
    "normal-jdtls",
    "exc-deps",
    "workflow-confirmed",
    "scope-readonly",
    "conflict-scope",
    "normal-read",
    "boundary",
    "exc-index",
    "exc-jdk",
    "exc-jump",
    "unknown-write-details",
    "unknown-jdk-version"
  ],
  "preserve_question_ids": [
    "deps-badge",
    "scope-choice",
    "exception-interaction",
    "write-confirm"
  ]
}
```