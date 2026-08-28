# 长 run 历史思考面板整层消失：逐出推理折叠回结构锚点

Status: implemented

任务分支 `zcode/chat-reasoning-fold`。前序
`2026-08-28-chat-interaction-polish.md`（d0cb398 已合 main）在实测中发现本缺口并立项。

## 问题

`TIMELINE_CAP=500`（runs.store capTimeline）的保留策略：结构锚点
（run.created/message.completed）> Plan/Goal 快照 > 工具 bundle > 其余帧按尾填充。
reasoning-delta 与 text-delta 同池竞争尾部预算，而**推理永远先于正文产生**——
长 run（实测 9425 帧、8102 条推理 delta）超帽后推理帧被整段淘汰，
`buildMessages` 的 reasoningBuf 聚不到任何内容，历史「思考过程」面板一个都不渲染。

## 方案：折叠，而非扩容

- **不改 500 帽**（内存边界是 SSE 回放审计定下的既有设计），改在截断点做信息折叠：
  capTimeline 落定后把每个 message.completed 锚点之前、未全程存活的推理文本聚合成
  合成投影字段 `reasoning_folded` 挂回锚点 data（纯前端投影，不上线、不进契约）。
  结构锚点必被保留，思考内容随之存活。尾部 4000 字符预算截断防内存回流，
  截断时展示层加「早期推理已省略」前缀（诚实 UI）。
- **折叠粘滞**：增量合并每来一个事件都重跑截断，扫描窗口随尾部淘汰收窄；
  锚点已携带折叠时绝不重算覆盖，否则折叠会越合越短（实现首版即犯此错，
  被「部分逐出」测试当场抓住——先有的折叠在锚点定稿时生成，此后该块不可能再有
  新增推理，保持原值即完整）。
- **双源不重复出卡**：块内帧全部存活（未超帽/尾部全留）时不写折叠，
  buildMessages 走原 reasoningBuf 路径；折叠存在时优先于残存缓冲
  （后者可能只是尾部片段）。
- **extractDeltaChunk 迁至叶子模块** `stores/delta-chunk.ts`：chat.store 与
  runs.store 共用同一线形（raw.chunk.{type,text}）解析，防双写漂移。
  注意依赖方向：chat.store→runs.store 已有运行时引用，runs.store 不得回引
  chat.store（成环），共享解析器必须落在叶子模块。

## 路径差异（有意为之）

- **历史回放（loadHistory 一次性合并）**：折叠扫描覆盖完整事件集，
  折叠=全量（预算内）。这是用户场景（打开历史会话），本 bug 的正式修复面。
- **增量直播（applyEvent 逐帧）**：锚点到达时早期帧可能已被淘汰，
  折叠覆盖「当时仍存活的窗口」并粘滞——严格优于现状（此前为零），
  但不回补已淘汰内容。直播中的思考展示本来就走 aggregateRunStream 实时缓冲。
- 更彻底的终态是 adapter/网关侧在 message.completed 载荷里直接落推理摘要
  （结构事件天然存活），涉契约演进，另案评估。

## 验证

- 新增 6 条测试钉（截断预算/部分逐出/粘滞/未超帽不折叠/纯文本不折叠/
  消费端双源优先级）；全量 453 过、tsc -b、lint 绿。
- 生产页实测（真实 9425 帧 run、正常 500 帽）：10 段历史思考面板全部恢复渲染，
  3 段超长块恰好按 4000 预算截断并带省略前缀；面板 max-h 内滚与前轮修复叠加生效。
