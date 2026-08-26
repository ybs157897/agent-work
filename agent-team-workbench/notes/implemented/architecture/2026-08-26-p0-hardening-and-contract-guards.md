# P0 加固与文档一致性门禁：session_unknown 补齐、凭据写后不可读、契约对账自动化

Status: implemented

设计依据：`notes/product/2026-08-25-project-review-detailed.md` NOW 清单 + 2026-08-26
蜂群六路架构分析 P0 结论。任务分支 `agent/p0-hardening-and-doc-consistency`
（37da917..23b979b 六刀）。

## 决策与理由

### kimi CLI：失败后分类，弃恢复前探测

- **第一手证据**：实跑仓内 vendored `runtimes/kimi/darwin-arm64/kimi` v0.38.0，
  `-S <不存在id>` 时 exit 1，stderr 首行逐字为
  `error: failed to run prompt: Session "sess_…" not found.`——经 drainStderr 落
  turnFailure("provider_error")，原本得 transient_upstream/retryable=true，永不触发
  maybeSelfHeal，违反「resume 永不静默降级」硬约束。
- **弃探测**：kimi CLI 无 `sessions` 子命令（实测 unknown command），最接近的
  `export <id>` 落 ZIP 归档需解析判存，成本不成比例；dsh 式恢复前探测不适用。
- **引号陷阱**：真实文案把 id 夹在引号对里，照抄 claudecode 的裸子串匹配会漏判；
  匹配 `session "` + `"` 尾配对，同时**不做裸 `not found` 匹配**（防误吞
  method/model not found，沿用 codexapp 纪律）。防回归：fixture 逐字复刻实测输出，
  另有负例钉死「普通 auth 失败不误报 session_unknown」——活锚点不得被无谓清掉。

### 凭据：写后不可读 + 权限收紧 + 错误不伪装

- GET `/models/provider-credentials` 改 `{configured, masked_hint?, length?}`：
  长度 >8 才回末 4 位（短密钥零泄露），**任何分支不含 api_key 字段**；PUT 不变（204）。
- UI 行为变更：输入框永远留空起始，**留空保存 = 不改 Key**；不再提供「清空已存 Key」
  入口（要清需删供应商）。独立清除按钮如需要另派任务。
- `CredentialsStore.Get` 改三返回值：IO/解析错误向上抛，不再吞错伪装「无凭据」
  （原形态会让 hydrate 静默跳过真实故障）。
- 遗留路径 `models/credentials.local.yaml` 读到即 chmod 0600（best-effort 不阻断读取）；
  本机存量文件已运维侧手动收紧。收紧只发生在该文件被实际读到的轮次——主路径存在
  时存量 0644 文件不会被动到，属已知边界。

### 契约一致性门禁（文档驱动开发的执行面）

- **自省机制 = go/parser AST 只读解析**（server.go 的 HandleFunc 注册、events.go 的
  字面量常量），不改任何生产代码、零新依赖（yaml.v3 已在）。提取器**空集即 Fatal**，
  契约文件结构变化红灯而非静默放行。
- 门禁 A：路由集合 ↔ openapi paths×methods 双向对账（56↔56，`:id`→`{id}` 归一）。
  门禁 B：事件常量 ↔ asyncapi `type.enum`（50↔50）+ aggregate 枚举（9↔9）+ channel
  地址必须对应真实 GET 路由。反证实验（注入假路径/改名事件）验证过灵敏度。
- **已知缺口**：门禁覆盖 (method, path) 与事件名级；响应 schema 形状未自动化
  （凭据端点的形状由 handler 单测钉死，其余端点未覆盖）。
- asyncapi 事件白名单从 YAML 注释升级为机器可读 `type.enum`（原注释尸体删除）。

### 迁移清单派生化

- 双目录等价守卫：编号+slug 逐条对账 + SQLite 全量 apply 幂等断言（cmd/migrate
  重构出包级函数复用生产路径，CLI 输出逐字不变）。当前 14↔14 无漂移。
- `internal/migtest.ApplyAll`：非 `_test` 普通包、不 import testing、纯 stdlib、
  runtime.Caller 定位仓库根——五处硬编码清单（mcpserver + 四个测试夹具）全部收编，
  新增迁移免同步。**披露**：wakeups_test 夹具从只 apply 0001–0005 变宽为全量，
  race 实证无影响（迁移只增不改）。

### 明确不做（本轮）

- **认证空窗**（demoRole=owner 硬编码）：特性级工作（登录源/会话/RBAC 接线），
  归评审路线图 NEXT（1–4 周），不属于本轮 P0 修复；RBAC 矩阵实现仍在、仅生产
  路径未接真实身份。
- claudecode 英文文案匹配的本地化脆弱性：已有分类，加固非本轮。

## 负向保证

- 凭据 GET 任何分支不含 api_key 字段（`TestGetProviderCredentialNeverEchoesPlaintext`）。
- kimi CLI 会话丢失永不落 transient 重试；普通失败不误报 session_unknown（正反两测钉死）。
- 契约/迁移守卫提取器空集即 Fatal；门禁差异输出双向明细，不静默放行。
- task_sessions 墓碑、Accept 唯一收口等既有红线未受本轮任何改动影响（触面包全绿）。

## 事故记录：共享工作树被外部会话切支

第二波执行期间，另一会话把共享工作树从本任务分支 checkout 到
`zcode/chat-transcript-redesign`（基于 fc09535）并提交暗色皮肤 note（9cb1319）。
症状：executor 读到旧形状凭据 handler 且无 diff、树里出现非本任务的 web 改动。
处置协议：reflog 取证 → 确认本任务提交未丢（都在任务分支）→ carry-safe checkout
（未提交改动无损携带、git add 只点名本任务文件）→ 分刀提交 → 把树切回对方分支
归还。教训已固化为蜂群纪律：**派发前与提交前都要核对当前分支**；未提交工作挂
在共享树上期间是最大风险窗口，完工即收。
