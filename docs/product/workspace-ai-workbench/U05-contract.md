# U05 产品回填与变更合同

状态：实施中。Owner：backend 负责全部 Go/SQLite/API，frontend 负责 web，root 负责验收与整合；不提交、不合并。U01–U04 为 staged 依赖。

## 用户动作与事实

保留现有 Chat 和单题流程。普通回答是开发理解；只有明确的“回填产品结论”动作才能写产品确认。开发填写确认/否决/仍待澄清、结论、沟通依据和产品版本。每次处理一个分析事项；可切换当前事项，历史与已答内容折叠查看，避免一次展示全部问题。

结论绑定当前 Workspace/Chat/Agent、分析 revision、item_id、服务端计算的 ItemFingerprint 和来源依赖，记录服务器识别的开发者 actor 与时间。AI 文档、附件、普通聊天、deferred 及模拟确认文件不能自动生成产品确认。

## API 与持久化

- 扩展 GET `/work-items/{chat}/analysis`：增加 `decisions[]`（有效性及 review_reason），保留 U04 字段；当前事项由客户端从 document.items 选择，不增加第二个服务端投影。
- POST `/work-items/{chat}/analysis/decisions`：`{expected_version,revision,item_id,outcome,conclusion,basis,product_version,client_key}`。outcome 仅 `confirmed|rejected|needs_clarification`，正文/依据/版本必填。Scope、actor、fingerprint、时间由服务端派生。CAS 与实体 client_key 幂等，不同 revision/item/内容复用 key 冲突。重试不重复写，响应丢失恢复。
- GET `/work-items/{chat}/analysis/history`：有界返回不可变 revisions、answers、decision/reopen 记录与替代关系。需提供 UI 可核对的历史，不只在数据库保留。
- POST `/work-items/{chat}/analysis/recheck`：核对所引用附件、代码、知识是否仍是有效版本；返回当前 projection。也在确认/草案前调用同一核验，不仅依赖用户刷新。
- SQLite migrations 是唯一 DDL。旧 decision 不被覆盖；自动重开也留不可变原因/来源/版本，重复复核不制造重复历史。普通 Run 状态仍是唯一执行权威。

## 变更处理

新附件、明确的变更说明或检测到已引用代码/知识版本变化后，标记当前分析需要复核，阻止旧草案发布。用户在现有 Chat 补充材料/文字，以同一个 `chat-analysis/v1` Run 重新分析；不新建模型循环。

新有效分析到达后，按稳定 item ID、内容/来源 fingerprint 比较：相关或无法可靠匹配的结论重开并说明原因；无关且完全一致的结论保持有效。历史分析、答案、产品结论及重开原因可追溯。旧结论不能被迟到模型结果覆盖。不要只按“整个会话摘要变化”重开全部事项。

U04 普通答案也需按 question fingerprint 选择性继承：未改变的问题不再询问，改变的问题注明重问原因；旧答案仍保存原 revision，不能把其原始记录伪装为新提交。选择性继承需有明确 lineage，不能重新激活已被后续变更否定的远古答案。新分析上下文包含之前产品结论与答案，要求模型逐字保留未改变的条目；人类确认仍独立于模型输出。

确认与新附件/变更/新分析/重核必须共享 CAS 边界。资料变化后尚未分析完时，旧确认可查看但不能被用于旧草案发布。无关结论在分析核对后继续有效，相关结论必须开发重新回填；没有自动批准路径。

## UI

在 Chat 内增加产品结论回填、已答记录与历史查看，以及“补充变更并复核”动作（带当前 composer 文字/原件，经既有发送管线）。正在回答时也允许明确的变更动作，不能要求先回答已经失效的问题。只读历史不渲染新的可答问题列表。

未提交结论草稿按 Workspace/Agent/Chat/item/revision 保存。变更后不能静默把旧选择绑定新版本；失败保留输入，成功按 client_key 回执清理。展示 confirmed/rejected/needs_clarification/needs_reconfirmation 的不同含义，普通 answer/deferred 从不显示为产品已确认。

## 验收

用隔离 web-idea Chat 的真实有效分析，回填两个独立事项和一个否决；注入变更再经真实 Agent 分析，证明相关事项重开、无关事项保留、旧历史可看。API/DB 覆盖空间越界、CAS、重放、迟到结果、源版本漂移、草稿/刷新恢复。真实模型与协议模拟分别记录。

U04 真实样本中发现仅从 package.json 推断不存在前端 LSP/写盘的错误；不要确认该推断。用实际客户端实现证据纠正并保留旧历史，作为 U05/U07 的真实复核案例。

U06 消费合同：最新有效分析 + 当前有效产品结论 + 其来源/版本指纹。发布仍需重新验证材料与项目基线；本单元不创建 Task。


## 未变事项的明确引用

真实模型会在“未变”的事项里补写产品已确认等叙述，导致全文指纹发生变化。为避免措辞改变把无关结论重开，更新分析可输出 `preserve_item_ids` 与 `preserve_question_ids`，并省略对应完整对象。服务端只从本 Run 冻结的 `base_revision` 展开原记录，拒绝不存在/重复/冲突引用；补齐所需旧来源后仍重新核验当前权限、原件/代码/知识版本，并执行原有严格校验。不得通过放宽指纹忽略实质来源变化。完整首次文档继续使用原合同。

保留是对旧条目的明确引用，不能把新措辞伪装为未改变；产品结论独立存放，不回填到模型 item 正文中。旧失败复核记录保留；新回归环境只用于重复验收，不改写原历史。
