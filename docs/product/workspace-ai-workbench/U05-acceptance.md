# U05 产品确认与变更验收

日期：2026-09-09。状态：已通过。

开发回填的产品结论保存为独立、不可变记录，包含结论、依据、产品版本、来源指纹、分析版本及当前本机身份。普通回答、AI 建议和附件里的“确认”字样不会生成批准。

## 已完成的实际验收

- 在隔离数据库以实际 HTTP 保存外部依赖、JDK 降级确认以及对错误代码推断的否决，13 项 HTTP/SQLite 检查通过，含版本冲突、实体重试、空依据拒绝及跨空间访问拒绝。
- 实际 Kimi/DSH 读取变更 Markdown 与 web-idea 的 JavaLspSession、client.ts、App.tsx。旧版仅凭 package.json 推断缺少 LSP/写盘能力被纠正；旧分析和否决保留。
- 新分析 revision 2 仅重开 `exc-deps`、`normal-jdtls`；`exc-jdk` 原文、来源和有效产品结论精确保留。2 条未变问题的回答以明确 lineage 继承，变化问题不沿用旧答案。
- 实际 UI 回填修正后的 LSP 结论，刷新后恢复草稿并提交。外部依赖新结论草稿经 A→B→A 保留；B 无 A 的确认面板。
- 实际 UI 提交产品结论后模拟丢失 HTTP 响应，服务端只保存一行；最终库有 7 条不可变产品记录（含故障回归记录）、2 条重开记录、3 个有效事项；最终 U05-P2S 仅一条。

## 真实失败与修正

首次真实模型重写了未变 JDK 事项，将“产品已确认”混入模型事项文本，导致全部确认重开。保留该失败输出和原始数据库；没有改写历史。新增 `preserve_item_ids`/`preserve_question_ids`，从 Run 冻结的 base revision 展开原对象，再严格校验来源和完整文档。后续发现 preserved question 引用变化事项被过度拒绝、history 裁剪漏旧 conversation 来源，两项均修复并通过实际模型新 Run 验证。

UI 迟到 refresh 覆盖 pending clientKey 的问题已修正。实际故障注入后 key 保留；刷新通过 history receipt 查回已保存记录，待提交草稿清空，表单回到空白，选择恢复到原事项。前端完整 130 个文件 / 1011 个测试通过，类型检查、lint、build 通过。

## 最终检查

- `go build ./...`、触面 `go vet`、application/HTTP focused race 通过。纯 `chatanalysis` race 通过。
- 新增 foreign Workspace/Chat/Agent、同 conversation ref 不同 digest 拒绝测试及“current revision 已变但 retry context/base 保持父 Run”回归，两组明确执行且 race 通过。self-heal/lost 同样继承冻结上下文。
- 前端类型、lint、构建及 130 个文件 / 1011 项测试通过；存在原有 SSR useLayoutEffect 警告，不影响本轮测试结果。gofmt 与 diff whitespace 门禁通过。
- 此处只声明触面 race；此前完整 application race 超时未作为通过。

## 证据及边界

证据目录：[u05-decisions-changes](../../review/assets/u05-decisions-changes/)。`selective-verification.json` 对应真实模型结果；`api-checks.json` 是实际接口/数据库；`ui-decision-response-loss.json` 仅故障注入为模拟，保存和数据库为真实。

首次真正读取资料的 Run 与最终协议重发 Run 已分别标注于 `evidence-provenance.json`；最终 accepted Run 未重新调用工具，不重复声称重新读取。

产品沟通均为隔离验收模拟，不代表用户已批准 web-idea 的具体产品范围。当前应用身份是 `user_demo`，不宣称产品本人登录或多用户认证。原件仍由运行时按核验路径自行读取；上传状态不代表已读。代码/知识来源在校验时检查，不把模型推断当作独立事实证明。

U05 输出 effective decision state 和 immutable snapshots；旧草案发布拒绝在 U06 集成验收，不在本单元提前宣称已实现。
