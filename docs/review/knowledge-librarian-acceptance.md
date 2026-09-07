# 知识管理员闭环验收

实施日期：2026-09-06；收口复核：2026-09-07。实施分支：`codex/knowledge-librarian`，基线 `main@12906d6`。

状态：本机读写闭环已通过；以下区分真实模型验收、应用集成验证及未实现的运行边界。

## 交付范围

[架构方案](../architecture/knowledge-librarian-design.md)、[产品合同](../product/knowledge-librarian-requirements.md)、[部署与调用协议](../protocol/knowledge-librarian.md) 对应同一实现：SQLite 保存共享知识、Agent 私有视图、不可变版本与双向关系；管理员通过既有 Run 执行有界调查/整理；任务产出先收件，再整理为待复核变化集，经显式管理命令原子发布。

资料完整性是对声明范围、版本和预算的判断。文档摘录未经系统独立读取、资料不足、冲突或关联未交付时必须保留缺口。

## 自动化证据

| 验收目标 | 证据 |
|---|---|
| A 查询展开 B，保留条件、来源与固定版本 | `TestScopedRetrieverExpandsRelatedItemAndCarriesEvidence`，SQLite 关系检索测试 |
| 不能遗漏已发现端点并自称完整 | `TestKnowledgeFinishGateRejectsDroppedDiscoveredEndpoint`，对应成功对照测试 |
| Workspace/Agent 隔离，关系端点不泄漏 | `TestKnowledgePrivateScopeAndVersionCAS`、`TestKnowledgeRelationsDoNotRevealPrivateEndpoint`、HTTP 共享视图/版本 URL 测试 |
| 任务终态收件→自动整理→候选→发布→再次检索 | `TestKnowledgeTaskCaptureCurationPublicationRoundTrip`；真实应用服务、SQLite、Run 钩子，模型回调用测试数据驱动 |
| 幂等、不可变版本与废止保留历史 | `TestKnowledgeSubmissionIdempotencyAndImmutableVersion`、`TestKnowledgeRepealKeepsHistoryAndWithdrawsDefaultSearch` |
| 有界错误、预算、取消与恢复 | Librarian runtime/finish gate 测试；恢复列表先过滤后分页；CLI 取消测试 |
| Run 凭据不被跨 Run 使用，终态失效 | `TestKnowledgeRunCapabilityCannotBeForgedOrUsedAfterCompletion`、远程/空 Host 禁止注入、符号链接与路径穿越回归 |
| 来源真实性层次与首次建库 | 原始提交摘录/来源身份校验；拒绝 ID 子串伪造；不以管理员自身输出作为主证据 |
| 中文检索、无半发布、单连接不死锁 | SQLite 发布/FTS/来源/范围/CAS 测试；嵌套查询 2 秒 deadline 回归 |
| 新库/升级/重跑 | `TestMigrationsApplyEndToEnd`；`TestKnowledgeMigrationsUpgradePreservesTasksAndReruns` 从 0043 升级并保留原有任务 |

验证命令在 `agent-team-workbench/` 运行：

```sh
go build ./...
go vet ./...
go test -race -count=1 -timeout 180s \
  ./internal/knowledge ./internal/persistence/sqlstore ./internal/application \
  ./internal/httpapi ./internal/knowledgeclient ./cmd/atw-knowledge ./contracts \
  -run 'Knowledge|Engine|ScopedRetriever|PathFieldCheck|Ask|ScopedToken|AccessFile|ContractGuard'
go test -count=1 ./cmd/migrate -run 'TestKnowledgeMigrations|TestMigrationsApplyEndToEnd'
cd web
pnpm tsc -b
pnpm test
pnpm lint
```

最终后端触面 race 全部通过（SQLite 约 53 秒、application 约 44 秒、HTTP 约 18 秒）；命令另包含 `ContractGuard` 契约门禁。构建、vet、gofmt、完整 HTTP 包、新库/升级/重跑迁移均通过。前端完整套件 107 个文件 / 882 个测试、`tsc -b`、lint 通过；后续关系历史说明文案只重跑类型检查及知识页测试。

两次扩大到整个 application race 的尝试在迁移密集测试中超过约 9–10 分钟，分别被停止或超时，**未计为通过**。本次按改动触面验收，不用局部通过替代全量 CI。

## 真实运行与浏览器记录

使用独立临时 SQLite、合成 Git 仓库和本机已登录的原生 Codex Runtime；未修改主工作树的数据库、Agent 配置或原始 Codex 凭据。测试模型读取现有配置，为 `gpt-6-astra`，推理强度 low。

合成资料：取消订单 A 触发库存释放 B；只有已支付订单取消才触发退款 C。资料明确状态/事件顺序、幂等、库存预占条件、共享订单号、重试与六类验收场景，不代表生产业务规则。

- 在真实浏览器选择并保存管理员、启用配置、提交候选及独立原文摘录。
- 早期多轮真实整理暴露的“资料不足无 proposal 被误判协议错误”和计费用量丢失已加回归；`kbj_01M1TJF0NAGJT83KV371S1NMQ0` 正常以 incomplete 结束，4 个真实 Run 对应 used.turns=4，完整保留缺口。
- 浏览器发起并取消 `kbj_01M1TJKWKCBRF25GQH24DN04AW`，其知识作业和当前 Run 均为 cancelled。
- 最终机器协议接入后，`kbj_01M1TKTWG4B98B14FSM38HZSM3` 经 3 个真实 Run 完成 search → read.source_ids → finish，生成 3 个待复核版本；浏览器发布 `kss_01M1TKYA2WQ9X4BAQJ8JZB8HSX` 后三个主体均为 effective v1。随后真实关系查询发现顶层 finish.relations 未持久化的问题；仅主体发布不计为关系闭环通过。
- `kbj_01M1TM1HJ3EXZ3SW4VG8K09CCG` 的提问只命名 A，模型从规格中找出 B/C 并给出条件与证据；因缺少实际实现/测试证据、关系表未登记及达到轮次边界返回 incomplete。这是缺口反馈的真实证据，未计为完整查询成功。
- 普通 Agent Run `run_01M1TM1J2E1SC9086H5PVZW5WV` 实际通过 CLI ask 创建了绑定查询 `kbj_01M1TM8XHVW1085M6STGSBTEZP`。原生沙箱阻止本机 HTTP 时，按现有审批面单次批准验收命令；没有改变全局或 Workspace 的网络权限。
- 该普通 Agent Run 最终 succeeded：继续调用 read 读取 A 的内容版本 1（`kbv_01M1TKYA2XBNVPSDG2XQ2CJ8Y9`），再通过 stdin submit 返回 `kss_01M1TMMZZ8X3S2FMY199CYWXKH`（received、no_change=true）。最终回答列出 B/C、触发条件、幂等、六类验收场景以及查询缺口，没有宣称实现已经完成。终态后对应 capability 文件已不存在。
- 修复关系持久化后，真实整理 `kbj_01M1TN70SF5DK1FW8N3194X0XN` 用 4 轮生成 2 个必要修订版本。浏览器发布 `kss_01M1TNBNJ6GQHKM5Y8J180YBNG`：A/B 为 v2，C 保留 v1；HTTP 从数据库读回 **7 条唯一有效关系**，每条都有来源 ID，A 直接可见的正反向关系为 6 条。
- 最终调查 `kbj_01M1TNG6JGAD0DM926FJQCZENS` 的问题只命名 A，限定为已发布合成规格。4 个真实 Run、1 次搜索，返回 A/B/C、7 条结构化关系、逐项来源和六个 complete 覆盖维度。结果明确只复述规格，未声称仓库实现或测试已通过。运行流程使用权威机器 schema，没有 fixture 替代模型输出。
- 无增量快速收口修复后，普通 Agent Run `run_01M1TN8DCCY466BMBM9PPF67WQ` 真实 submit 返回 `kss_01M1TNAXQFDGB4ZVX358MPCBKE`，状态 **merged**、no_change=true；没有启动整理模型。
- 浏览器切换 A 的 v2/v1，v1 原文、来源和版本 ID 仍可读取。条目面板明确将“当前有效关系”与所选历史正文版本区分，避免把当前关系当成历史快照。
- 2026-09-07 用户在界面点击废止 C 后，HTTP 核验：C 不再出现在默认列表、条目状态为 repealed；固定 v1（`kbv_01M1TKYA2Y4RW7ZAW6ZKXFVAPV`）和 1 份来源仍可读取；此前完成调查的 Result SHA-256 保持不变，覆盖投影仍为 3 个主体、7 条关系。

最后一次界面/废止复核使用生产 HTTP/application 模块和验收库副本，不执行模型 Run，也不修改原库后续产生的 Kimi 配置同步待办。该待办因 `kimi-2-7` 缺少 API Key 阻止完整控制面重启；本记录没有把最后的模块验收算作新一次原生模型启动通过。原生模型证据来自前述 2026-09-06 的完整控制面实例。

## 可审阅工件

- [完整结构化调查结果](assets/knowledge-final-inquiry.json)：固定范围、版本、关系、来源、覆盖和预算。
- [1440 像素条目/来源界面](assets/knowledge-details-1440.png)。
- [1024 像素关系/谱系界面](assets/knowledge-relations-1024.png)。

两种宽度均已检查无横向溢出。2026-09-07 的页面与覆盖统计通过真实 DOM 复核；当日截图工具超时，没有生成新的截图，也没有将既有截图标成当日新图。

临时验收浏览器空间已关闭，HTTP 验收进程已停止，复制的 Codex 登录文件与配置已清理；Run capability 目录为空。主工作树、原始登录配置及原验收库的后续模型配置待办均保留。

## 实现边界

- Worker CLI bridge 仅对显式本机 Execution Host 注入；远程 Worker 工具注册与凭据传输尚未实现。
- 管理员调查使用已入库知识和显式提交的来源摘录；没有自动扫描仓库或网络，没有向量数据库。
- 自动收集默认关闭；普通 Task 可开启终态收集，普通 Chat 通过主动 submit 写入。
- 实现先在独立支线验证，合并按用户明确指令执行；不自动推送。主工作树既有未提交修改保留。

2026-09-07 合并前复核：modelconfig 全包 race、HTTP 并发删除与幂等回归、后端 build/vet、知识迁移测试均通过；前端 tsc、107 个文件/882 项测试与 lint 通过。已完成的临时状态工件归档到本地运行资料目录，从版本树移除。
