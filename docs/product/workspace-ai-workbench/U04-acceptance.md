# U04 分析版本与单题问答验收

更新：2026-09-09。状态：已完成；产品确认与变更保留进入 U05。

## 已交付

现有 Chat 的“整理需求，逐项确认”会携带当前文字与原附件，复用 Chat Run/ModuleRunner。模型按 `chat-analysis/v1` 提供完整问题队列；服务端校验结构、来源归属与字节/知识版本后保存不可变分析版本，界面只显示一个当前问题。

分析目录、旧文档、旧答案和错误通过每轮 `analysis_context` 传递。逐条用户消息使用稳定来源标识，避免不相关消息让整个会话的来源摘要一起变化。回答具有 revision、问题指纹、client_key 和 CAS；暂不确定是独立记录，不是产品批准。

## 真实验收

- Kimi/DSH 经真实聊天读取 DOCX、PPTX、Markdown、代码、知识，产生包含四类来源、五类分析事项和三个问题的有效版本；9 组读取工具开始/完成事件留存。
- 新 Chat 的文字与三份暂存附件全部进入同一个分析 Run；未建设独立模型链。
- 当前只显示一题且没有默认勾选；保存第一题后进入第二题，暂不确定第二题后进入第三题。
- 已选项与补充文字经过 A/B/A 和刷新恢复，B 不显示 A 的内容。
- 第三题服务端已保存但响应丢失：浏览器保留输入；补齐答案回执 client_key 后，刷新识别已保存答案并清理草稿。SQLite 只有三条答案（2 answered、1 deferred）。
- 18 项真实 HTTP/SQLite 检查通过；跨 revision 复用 client key 被拒绝。非法后续 Mock 输出保留之前真实模型的有效文档和答案。普通 Chat 不自动回答或创建 Task。

## 检查

后端触面 build/vet、application/HTTP/runtime/迁移测试与相关 race 通过。strict decoder 的重复 JSON key、无效来源、知识版本、路径、选项和指纹回归通过。前端 `tsc -b`、完整 lint、127 文件/995 测试与生产构建通过。PNPM 最初拒绝共享 node_modules 的自动维护；改用本单元自己的依赖副本后构建通过，未改动上一单元依赖。

## 明确边界

服务端验证来源身份、摘要与结构；读取范围是 AI 报告，真实工具记录单独保存。业务推断仍要开发核对。真实样本曾把 package.json 未见依赖推断成没有前端 LSP/写入能力；这不能由仅有的文件证据推出，已记录并强化提示词，未作为产品确认。U05 应通过明确回填和变更流程处理，不能修改掉原历史。

本单元保证同一分析版本的回答恢复与逐题推进。跨分析版本的选择性答案/产品结论继承由 U05 处理；旧回答已经持久保留并提供给下一轮模型。

## 证据

- [真实模型与数据库核验](../../review/assets/u04-analysis-questions/real-analysis-verification.json)、[读取工具记录](../../review/assets/u04-analysis-questions/real-tool-events.json)。
- [逐题推进](../../review/assets/u04-analysis-questions/ui-answer-sequence.json)、[草稿恢复界面](../../review/assets/u04-analysis-questions/ui-answer-draft.png)、[丢失响应恢复](../../review/assets/u04-analysis-questions/ui-answer-lost-recovered.json)。
- [接口检查](../../review/assets/u04-analysis-questions/api-checks.json)、[旧版本保留](../../review/assets/u04-analysis-questions/invalid-output-preserves-valid.json)、[AI 推断需核对记录](../../review/assets/u04-analysis-questions/model-quality-observation.json)。
