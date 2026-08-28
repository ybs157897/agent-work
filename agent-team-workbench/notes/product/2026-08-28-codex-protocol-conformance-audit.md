# Codex app-server Adapter 协议合规审计（对照官方文档）

- 日期：2026-08-28
- 范围：`internal/runtime/adapters/codexapp/`（生产路径）、`contracts/runtime/codex-app-server-v2.md`
- 基准：官方 `codex-rs/app-server/README.md`（main，2026-08-28 快照）+ `developers.openai.com/docs/app-server`；钉版行为以 0.149.0 tag 源码仲裁；vendored CLI 实测 `codex-cli 0.149.0`
- 结论：**实现与钉定基线 0.149.0 高度吻合，未发现对基线行为不符的硬伤**；风险集中在官方 main 已发生的漂移会在升级时集中爆发。

## 背景

此前协议认知靠「读源码 + 抓流量」手工对账；OpenAI 现已把 App Server 立为正式产品面
（公告 + developers.openai.com/docs/app-server + 仓库 README）。本次审计是第一次把
实现逐条对照官方文档核验。官方锚点与同步规程见
`agent-team-workbench-docs/references/codex-appserver-official-protocol-reference.md`。

## 合规面（摘要）

所有发出的方法（initialize/initialized、thread/start|resume、turn/start|steer|interrupt、
model/list、account/read、三类审批响应）、全部消费的通知（item/* 全家、turn/*、
thread/tokenUsage/updated、turn/plan/updated、error）与 item 类型词表，均与官方文档或
0.149.0 源码逐字吻合。tokenUsage 的 `{total,last{inputTokens,cachedInputTokens,outputTokens}}`
形状、plan step 枚举、`declined` 状态语义、`expectedTurnId` steering 契约等逐一核验通过。
两个此前存疑点经 0.149.0 源码仲裁为实现正确：`sandbox` kebab-case、`approvalPolicy: untrusted`
（官方 main README 的 `workspaceWrite`/`unlessTrusted` 是上游文档自身漂移，不采信）。

## 发现（按风险排序）

### 升级即爆的漂移（main 相对 0.149.0）

1. main 文档示例改用 `sandbox: "workspaceWrite"`（camelCase）与 `approvalPolicy: "unlessTrusted"`；
   0.149.0 wire 是 kebab-case/`untrusted`。**升级 vendored CLI 前必须 `generate-json-schema
   --experimental` 重算 SHA 对比**，不能照文档示例改。
2. 顶层 `thread/start|resume.developerInstructions` 在 main README 已无记载（只剩
   `collaborationMode.settings.developer_instructions`）；Agent `instructions` 注入通道存在
   被上游迁移的风险。

### 解析/语义缺口（当前基线下即存在）

3. `error` 通知整包 `rawString` 截 200 字符：丢失 `error.message` 结构化提取、`codexErrorInfo`
   枚举与 `will_retry`，重试性只能靠关键词猜（codexapp.go:725）。
4. 审批参数未解析 `kind`（command|writeStdin）与 `approvalId`：writeStdin 审批会被当普通命令展示。
5. 拒绝一律回 `cancel`（中断轮）；官方 `decline`（模型收到拒绝可继续）未使用——工作台行为选择，
   官方无此规定；permissions 拒绝后主动 `turn/interrupt` 同为自定行为（契约已固化，双边记录）。

### 官方已提供、实现未接（增益点，按价值排序）

6. `turn/diff/updated`（轮级聚合 unified diff）与 `fileChange.changes[{path,kind,diff}]`——
   文件变更卡片此前「diff 无数据源」的直接解法。
7. `turn/completed` 内置最终 agentMessage 兜底——delta 丢失时防空答案。
8. `serverRequest/resolved`——审批请求清理回执，现落入 unhandled-warn。
9. `thread/list|read|turns/list|items/list`（不 resume 读历史）、`thread/archive|delete`
   （codex:// 死锚点的 rollout 回收）、`account/rateLimits/*`（配额前瞻）、
   `thread/compact/start`（长线程超限自愈）——按需再接。

### 契约措辞与代码的细微出入

10. Contract「`turn/completed.status=interrupted` → interrupted or cancelled according to the
    control command」与代码不符：无控制面取消意图时一律投影 `cancelled`，interrupted 投影只在
    控制取消在飞时出现（本次已订正契约措辞）。
11. 契约配置映射缺两行：`turn/start.effort` 恒发（默认 medium）；plan 模式空模型先经
    `model/list` 发现 CLI 默认模型（本次已补）。
12. dynamicTools：item 解析器留有 `dynamicToolCall` 分支，但 `thread/start.dynamicTools` 从不
    发送、`item/tool/call` 请求回 `-32601`，属无触发路径；契约限制清单已补记。

### 经验遗留（实现有、契约与官方皆无，维持现状 + 留痕）

- `thread/compacted` 通知兜底分支：官方词表已无此通知，0.149.0 不发射，防御性死路径。
- tokenUsage 解析的 snake_case 回退、`codexDeltaText` 嵌套探测：容错性经验代码。
- `session_unknown` 依赖错误文案匹配（含 0.149.0 专属 `"no rollout found"`）：版本脆弱，
  升级时需复检。
- 审计时点更正：protocol.go:247/282 引用的两份 notes（usage-telemetry / plan-and-compaction）
  在仓库根 `notes/implemented/architecture/` 下**存在**，非死链（初报有误，复查更正）。

## 后续行动建议

1. （低成本高收益）接入 `error.message`/`will_retry` 结构化解析 + `turn/diff/updated` 投影。
2. （升级前必做）按 reference 文档的「升级同步规程」重算 schema SHA、对比词表、跑
   `go test -race ./internal/runtime/adapters/codexapp/...`。
3. `decline`/`acceptForSession` 决策是否开放给 UI，随审批交互改造一并定。
