# 会话元模型：任务 / 派发 / 会话组（Session Meta-Model）

> 日期：2026-08-29
> 版本：v1.0
> 状态：implemented —— 2026-08-30 于分支 `kimi/session-meta-model` 全四期落地（S1–S4）
> 范围：agent-team-workbench 单 agent 执行打通之后的上一层架构；会话层的统一管理与多 agent 协作形态

---

## 执行摘要

```
会话元模型（Session Meta-Model）
=============================================
用户痛点：会话碎片化 —— 每事开新会话、讲过就忘、找不到历史、多了难管
核心主张：任务管"我的事"，派发管"我刚发的"，会话组管"谁在跑"，会话内部归 harness
执行形态：派发 = 1 条 lead 主线（旗舰模型）+ N 条 worker 参与线（够用模型）
关键纪律：横向协作只走任务台账（禁止会话串门）；lead 等待 = 会话沉睡 + 事件唤醒
技术可行性：高 —— work_items / task_sessions / parent_anchor_id / wakeup / fork 均已就位
主要新增：dispatch 关联键、任务台账（滚动摘要 + 决策原话）、派发卡片 UI、FTS 检索
```

**一句话判断**：会话不是用户要管理的对象，而是实现细节；把 `work_items` 从后台任务台账抬升为用户唯一面对的"事项"对象，会话降级为片段与黑盒，是这一阶段唯一的新概念。

---

## 背景与动机

单 agent 执行（Run 状态机、ModuleRunner、续接、fork、三档压缩）已打通。需求方的痛点不在执行层，而在**会话这一层的用户对象错了**：

1. 每讲一件不同的事都要开新会话，心智负担重；
2. 讲得多了之后，回顾某个功能点完全找不到当时聊在哪；
3. 会话数量膨胀后无法管理。

需求方心智里的单位是"某件事"，工具给的单位却是"会话"。会话本应只是上下文窗口装不下时的物理切片，却变成了用户要面对和管理的单位。本提案把因果关系倒过来。

---

## 核心模型

### 三层结构 + 一个黑盒

```
任务（work_item 升格）                    ← 管理对象：用户唯一面对的事
└── 派发（dispatch）                      ← 注意力对象：用户一次发送形成的批次
    └── 会话组（session group）           ← 执行集合：响应本次派发的若干会话
        ├── lead 主线会话（旗舰模型）      ← 编排者：拆解、派工、汇总
        └── worker 参与线 × N（够用模型）  ← 执行者：各一条会话
            └── 会话内部（黑盒）           ← harness 子代理 / 工具调用，纯展示素材
```

### 术语表

| 术语 | 定义 | 落点 |
|------|------|------|
| 任务（Topic） | 用户面向的"事项"，持有滚动摘要、决策台账、谱系 | `work_items` 升格 |
| 派发（Dispatch） | 一次用户发送触发的执行批次，会话组的关联键 | 新增 `dispatch_id` |
| 会话组（Session Group） | `WHERE dispatch_id = ?` 的全部会话；组的存在靠外键，不靠树遍历 | 逻辑概念 |
| 参与线（Participant Line） | (agent × 任务) 的一条长期会话 lineage | `task_sessions` 唯一键已是 |
| 片段（Segment） | 参与线上的一次会话续接段落；片段间靠 handoff 档传递连续性 | `task_sessions` 锚点序列化 |
| 任务台账（Task Ledger） | 任务级共享记忆：滚动摘要 + 决策原话 + 产物索引；不属于任何会话 | `work_items` 扩展 |
| lead 主线 | 派发内的编排会话，跑旗舰模型；是派发主线，**不是任务主干** | 参与线之一 |

### 两个"主干"的语义钉死

讨论中"主干"出现过两个含义，必须区分，否则后续沟通会打架：

- **任务主干** = 数据结构（work_item + 台账）。不是智能体，不会被污染，不会成为上下文瓶颈。
- **派发主线** = lead 会话。是智能体，跑旗舰模型，生命周期限于一次派发（跨派发可续接，但身份是编排者而非主干）。

---

## 关键决策（含纠偏留痕）

### D1：任务主干是账本，不是智能体

最早设想是"主干自己识别新开会话还是续老会话"。被否决：主干若为 LLM 会话，它的上下文会变成全系统最长最杂的一条（分支本应避免的污染全部集中到主干），且路由要准就必须维护全分支实时摘要索引——索引腐化即误路由。**路由确定性优先**：同 task_key 且指纹未漂移 → 续；新事项 → 新参与线；残差模糊交给用户确认或 lead 判断，全程留痕可撤销（split / chain 作为逃生舱）。

### D2：harness 子代理收回会话内部，工作台不建实体

codex / kimi 原生支持会话内开子代理，这是 **harness 的内部能力**。工作台对它不建会话实体、不记血缘、不做恢复——只把子代理活动当事件流里的展示素材收进 transcript。工作台层面不存在"委派边"。

### D3：用户注意力的单位是派发卡片，不是会话也不是树

上层不关注底层如何实现、如何收敛，只关注"与当前发送相关的会话组"。任务页 = 派发卡片的时间线；卡片 = 用户那句话 + 会话组状态（几人在跑/已完成）+ 汇总结果；展开卡片看各会话正文，再展开才看到会话内部的子代理活动。**树、血缘、轮换、压缩、续接判断全部下沉为管道**，只为恢复与审计服务，永不出现在用户导航里。

### D4：横向协作只走任务台账，禁止会话串门

agent B 不许读 agent A 的会话内容。横向同步只走任务台账（决策日志、黑板、产物）。晚加入的 agent 从台账冷启动——读"任务已经定了什么、做到哪了"，而不是"别人聊了什么"。此纪律一破，上下文耦合爆炸，调试时再也无法说清谁的知识从哪来。

### D5：lead 的等待 = 会话沉睡 + 事件唤醒，不阻塞 run

run 是易失执行单元（有 lease、有心跳门控），阻塞等待的 lead run 既占 lease 又会被超时杀掉。正确形态：lead 派发完子任务后**本轮 run 结束、会话沉睡、token 零消耗**；worker 完成事件触发 wakeup 机制唤醒 lead 开新一轮 run 做汇总。逻辑上的"等待他们返回"不变，物理形态是事件驱动。

### D6：并行靠子任务拆分，父任务互斥锁不放开

现状 `work_items.locked_by_run_id` 是全任务互斥。决策：**保持互斥**；lead 拆出的子任务是各自独立的 work_item，各持各的锁、各走各的参与线。"多 agent 同时协作"落地为"兄弟子任务并行"，写冲突在结构上不存在。锁粒度下沉到产物级是 LATER 项，届时需先建产物级冲突纪律与实时可见性。

### D7：部分失败与超时是常态路径，不是异常路径

lead 等待带截止日期：worker 挂了或超时，lead 拿 n-1 份结果降级汇总，并在台账中显式标注缺口。禁止"无限等"和"一人失败全组失败"。

### D8：派发期内用户可追加、可喊停

会话组运行的分钟级窗口里用户可能补充或中止：追加走客户端内存队列（已有），取消走控制面前转 + adapter 取消面（已有纪律）。派发卡片常驻这两个入口。

### D9：什么时候才开组会

不是每条消息都惊动旗舰 lead。**用户 @具体 agent → 直达该参与线，lead 不出场**；用户只对任务说话 → lead 接诊，但 lead 可以判断"不用拆"直接答。小事无组会开销，大事才有编队。

### D10：模型分层是成本纪律

旗舰只跑 lead 主线（规划、仲裁、汇总）；worker 用够用的便宜模型。lead 的上下文是全组最值钱的资产，靠读任务台账而非全靠自身记忆，抑制膨胀。

---

## 被否决方案（负向保证）

| 方案 | 否决原因 |
|------|----------|
| 主干 = LLM 智能体，自由心证路由 | 主干成污染黑洞 + 索引腐化误路由；误判中"该新开却续老"不可逆 |
| 会话级 merge（两条会话上下文合并） | 语义合并有损且不可验证；git 类比在 merge 处断裂，本模型不提供 merge |
| 工作台为 harness 子代理建血缘 | 子代理是 harness 黑盒内部细节；建实体 = 为不属于自己的状态担责 |
| 无限延长的会话链 | 保真度随深度单调衰减（摘要的摘要 = 传话游戏）；续接层级深了必须 chain 重开 |
| 嵌套编排（lead 再派生 lead） | 结果层层摘要回流同样是传话游戏；编排深度上限 1 层 |
| 放开任务级互斥锁实现同任务并行 | 写冲突与可见性时序问题未解前不开；并行用子任务拆分表达 |

---

## 信息流总表（三种流，各走各的道）

| 流 | 方向 | 载体 | 现状 |
|----|------|------|------|
| 纵向续接 | 片段 → 片段 | handoff 压缩档 | ✅ 已有（三档压缩） |
| 纵向派发 | lead → worker | fork 上下文包（parent_id + 描述） | ✅ 已有 |
| 纵向回流 | worker → lead | 完成事件 + 结果摘要 → wakeup 唤醒 | ⚠️ 契约待补 |
| 横向协作 | agent ↔ agent | **仅任务台账**（决策日志 / 产物） | ⚠️ 台账待建 |
| 用户回看 | 任意 → 任意 | FTS 检索 + 决策原话引文 | ❌ 待建 |

---

## Schema 增量草案

```sql
-- 1) 派发：会话组的关联键
CREATE TABLE dispatches (
    id            TEXT PRIMARY KEY,
    work_item_id  TEXT NOT NULL REFERENCES work_items(id),
    trigger       TEXT NOT NULL,              -- user_message | lead_plan | wakeup
    lead_run_id   TEXT REFERENCES execution_runs(id),
    status        TEXT NOT NULL DEFAULT 'running'
                  CHECK (status IN ('running','collecting','completed','degraded','cancelled')),
    created_at    DATETIME NOT NULL,
    closed_at     DATETIME
);
ALTER TABLE execution_runs ADD COLUMN dispatch_id TEXT REFERENCES dispatches(id);

-- 2) 任务台账：滚动摘要 + 决策原话
ALTER TABLE work_items ADD COLUMN rolling_digest TEXT NOT NULL DEFAULT '';
CREATE TABLE decision_entries (
    id            TEXT PRIMARY KEY,
    work_item_id  TEXT NOT NULL REFERENCES work_items(id),
    quote         TEXT NOT NULL,              -- 用户原话，禁止 LLM 转述
    source_run_id TEXT REFERENCES execution_runs(id),
    source_ref    TEXT,                       -- 链回片段内位置
    created_at    DATETIME NOT NULL
);

-- 3) 参与线片段序号（同一 task_key 下第 N 段，轮换/续接显式化）
ALTER TABLE task_sessions ADD COLUMN segment_seq INTEGER NOT NULL DEFAULT 1;

-- 4) 检索：FTS5 虚表（片段摘要 + 决策台账 + 产物标题）
CREATE VIRTUAL TABLE search_index USING fts5(
    work_item_id, kind, title, body,          -- kind: segment_summary|decision|artifact
    tokenize = 'unicode61'
);
```

纪律：决策台账 `quote` 字段必须存用户原话（引文 + 链回），LLM 转述只允许进 `rolling_digest`——转述会漂移，"当时怎么定的"必须保真。

---

## 分期实施

| 期 | 内容 | 用户可感知价值 |
|----|------|----------------|
| S1 | `dispatches` 表 + `dispatch_id` 外键；任务页派发卡片时间线；@直达 / 任务接诊路由 | "我发的话"有了实体，会话组可见 |
| S2 | 任务台账：`rolling_digest` + 决策原话台账；片段关闭自动摘要 | 不再"讲过就忘" |
| S3 | worker → lead 回流契约 + wakeup 唤醒汇总；部分失败降级路径 | 多 agent 编队真正闭环 |
| S4 | FTS5 检索 + 收件箱主题与片段归置（grooming 可交产品 agent） | "找不到当时聊的"根治 |

S1 不依赖 lead 智能（可用规则路由先跑）；S3 依赖编排 M1–M4 的 plan 执行器复用。

---

## 悬而未决（Open Questions）

1. **任务锁放开时机**：产物级锁 + 实时可见性是前置条件；当前无计划。
2. **主题自身的拆分/合并**：主题也会膨胀，需要显式 split/merge 操作；自动化归并禁止（归错 + 不可见 = 等于丢）。
3. **滚动摘要的刷新策略**：每片段关闭时增量更新 vs 定期全量重建；需实验定阈值。
4. **lead 跨派发的身份**：同一任务的 lead 参与线长期续接，还是每次派发新起？倾向长期续接（指挥上下文有积累价值），但需台账卸载记忆防膨胀。

---

## 讨论留痕（2026-08-29，五轮对齐）

1. **起点**：单 agent 执行已通，求"以 session 为元配件"的上层架构方向。
2. **需求方初案**：主干 + git 式分支树，主干自己识别新开/续老。→ 分析出 git 类比两处断裂（无 merge、延长不免费），路由自由心证不可靠；修正为"主干是账本、路由确定性优先、宁宽勿深"。
3. **痛点揭示**：真实动机是会话碎片化（每次新开、找不到、难管理）。→ 定性为对象层级错位，work_items 升格为事项层，会话降级为片段。
4. **多 agent 维度补全**：任务下多 agent、每 agent 多会话、会话内开子代理。→ 定型一根两轴；此时委派边仍在工作台层面。
5. **两次纠偏定稿**：(a) harness 子代理收回会话内部，工作台不建实体；(b) 用户注意力单位是"与本次发送相关的会话组"，底层收敛机制全部下沉。→ 三层模型定稿，lead 主线（旗舰模型）+ worker 参与线的执行形态确认，"等待"修正为事件驱动唤醒。

---

## 落地记录（2026-08-30，分支 `kimi/session-meta-model`）

实施刀法（每刀独立可回滚）：`6ee7c85` 迁移 0016+domain+sqlstore → `2954bc5` dispatch 关联与 @路由 → `0e94c14` 派发卡片端点 → `0fed865` 前端派发时间线+@提及 → `674e257` 迁移 0017 台账+自动摘要 → `195ff77` 前端台账区+钉为决策 → `b4b2cf0` S3 回流唤醒与降级收口 → `96dcc8b` 迁移 0018 FTS5+检索端点 → `6ee1d58` 前端搜索入口。

接入主线前发现 `main` 已占用 0015（run event agent identity）；会话元模型三条尚未进入主线的迁移整体顺延到 0016–0018，避免合并后触发迁移编号唯一性门禁。

真实 SQLite/API/浏览器验收暴露的默认收口、结果回流、产物索引、中文检索与
`@mention` 身份问题已修复；决策与放弃项见 [会话元模型真实验收修复](../bug-fix/2026-08-30-session-meta-model-validation-fixes.md)。

随后将模型的宿主边界钉死为 Task：`work_items.record_kind` 持久化区分 `chat | task`；
Chat 只复用 Run/Session/Adapter、续接、产物和通用正文投影，不进入派发、计划、任务锁、
状态机、台账、决策、收口或任务检索。Chat 与 Task 的列表、深链、事件和参与线均分域；
Task 的 Agent 正文留在任务详情内展示，不借道 Chat。完整决策与验证证据见
[Chat 与 Task 记录及执行边界隔离](./2026-08-30-chat-task-record-isolation.md)。

### 实施期的取舍与偏差（相对提案文本）

- **接诊批落库顺序**：`lead_run_id`↔`dispatch_id` 互指成环，实现为同事务三步（建批 lead_run_id=NULL → 落 run → SetLeadRun 回填），语义不变。
- **lead_plan 继承语义**：plan 子 run 优先继承 source run 的既有批次；仅当 source run 无批次（手动提交 plan/存量 run）才落 trigger=lead_plan 兜底批，同 source run 幂等复用。
- **@直达边界**：@ 命中停用 agent 不回退（响亮失败走既有校验）；前端候选过滤停用 agent 与名字含空白的 agent。直达 Run 的助手身份按 `agent_profile_id` 渲染，目标 Agent 的 task session 参与线也进入其会话列表，不改任务 assignee。
- **rolling_digest = 全量重算覆盖写**（非增量追加）：run 终态即片段关闭，整段重算后以 work_items.version 乐观锁覆盖、有界重试收敛；同一 run 重放终态输出逐字节不变，天然幂等。摘要不 bump updated_at（避免扰动按 updated_at 的统计口径），不发 SSE（每终态一条会打爆事件流），前端详情打开时自取。
- **决策台账 v1 只经显式端点写入**（POST /work-items/{id}/decisions，幂等键必带），不做启发式抽取；补了契约外的 GET 读端点（SSE 只有增量，冷启动需要全量）。校验失败按平台惯例落 422。
- **汇总 run 识别**：run 表无 trigger 列，以 `input.wakeup.settle_dispatch_id` 标记；存量 wakeup 路径（timer/assignment/on_demand）无此键、不挂批，零影响。
- **「只唤醒一次」的硬保证 = MarkCollecting CAS**：存储层原子迁移，成功方才 enqueue——并发终态、唤醒重放、collecting 下迟到成员全部 no-op。
- **enqueue 与 collecting 迁移同事务**（有意偏离 maybeAdvancePlans 的事务外先例）：消灭「迁移成功但入队失败 → 批卡 collecting 且无 wakeup」的死缝；enqueue 失败整体回滚到 running。最后一个终态事件后的持久化重试仍待统一 durable job reconciler 收口。
- **全取消→cancelled 的成员集合只含 worker**：若含汇总 run，汇总 succeeded 永远破坏全员 cancelled，规则成死代码；「整批喊停非部分失败」只在 worker 侧成立。汇总 run 自身失败仍落 degraded。
- **接诊批全取消仍会唤醒 lead**（collecting→汇总→cancelled），不跳过唤醒。
- **settlement automation 是必达控制面工作**：不依赖 heartbeat、不因同任务活跃 Run 被 coalesced；冲突时保持 queued 重试。lead-only 批次直接收口，不创建自我汇总 Run。
- **汇总材料 = worker 最后一条 assistant `message.completed`（120 rune）**；失败优先使用 Failure，Runtime 无完成正文时显式标注 instruction 兜底，绝不把任务描述冒充结果。
- **FTS + CJK 子串 fallback**：ASCII/结构化 token 继续走 FTS；含汉字查询补充同 workspace/work-item/kind 过滤的子串检索。mock 与真实 Runner 产物入口共用 artifact 索引投影。搜索结果仍无 created_at 列（v1 省略，需要时加 indexed_at）。
- **前端请求失败可恢复**：dispatch、decision、digest、task session 子请求分别显示就地 `role=alert` 与重试，不把失败伪装成空态或无限加载。tasks Header 搜索是新增入口；chat 页 ⌘K 本地会话过滤保留不动。

### 负向保证（本模型不做）

- **不设独立 dispatch 超时器**：成员必落终态由 run 层 lease/超时保证，dispatch 跟随收口；settlement wakeup 创建后的冲突/失败保持 queued，不超龄 coalesce。
- **dispatch 终态后迟到成员不复活批次**：已 completed/degraded/cancelled 的批，后落终态的 lead_plan 成员一律 no-op，其结果不进任何汇总（v1 已知限制）。
- **collecting 不回 running**：收口是单向的，防汇总循环。
- **decision_entries.quote 永不存 LLM 转述**：转述只允许进 rolling_digest；原话保真是台账的存在理由。
- **收件箱主题归置 grooming 不做**：留给产品 agent（schedules/grooming 回路就位后再议）。
- **搜索索引是派生存储**：不发 SSE、不做实时推送；前端搜索纯请求-响应。

### 悬而未决项的落地态

- Open Question 4（lead 跨派发身份）按倾向解落地：汇总 run 落 lead 在主任务的参与线，task_sessions 谱系长期续接，无新机制。
- 其余三问（任务锁放开、主题 split/merge、滚动摘要刷新策略）维持开放，不随本次落地收编。
