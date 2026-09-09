# 推理链不得成为助手正文（message.delta 通道判定）

Status: implemented

## 决策与理由

`message.delta` 是双通道事件：答案通道（`raw.chunk.type=text-delta`）与推理通道
（`reasoning-delta`）。通道此前只被前端（`web/src/stores/delta-chunk.ts`）和 adapter
注释承认，canonical 层提取助手正文用的却是「递归找任意 text 字段」的无通道提取器。
于是只要 run 没有 `message.completed`（中断/崩溃），`completedOrDeltaText` 的 delta
兜底就把推理链拼成助手正文：经 `conversationHistory` 灌进下一轮 prompt（实测一次
中断 run 把 37.5k 字符的英文推理当成了上一轮回答，下一轮首步为此多付约 1.4 万
token 输入并拉长思考），并经 `runFinalText` 污染 plan 提取、评估、需求分析、知识
沉淀、交付简报等消费方。`internal/application/conversation.go` 文件头早已立下
「推理链不进回放」的负向保证，但没有任何代码守它。

修复把通道判定收敛成一处 canonical 契约：`internal/domain/message_delta.go` 的
`MessageDeltaChannel` 只认 `raw.chunk.type`——`text-delta` → 答案、`reasoning-delta`
→ 推理，其余（缺 `raw.chunk`、扁平 text 载荷、未来新增类型）一律按答案兜底；
`completedOrDeltaText` 只累加答案通道。adapter 侧改用 domain 常量发帧，线形契约
同步进 `contracts/events/asyncapi.yaml`。

## 放弃了什么

- **只修回放、不动 `runFinalText`**：同一份文本有 8 个提取类消费方，只堵回放等于
  把同一颗雷留在评估/需求分析/知识沉淀路径上。
- **让每个消费方各自过滤**：通道判定散成多份实现并持续漂移，正是这次 bug 的成因。
- **把 `message.delta` 拆成 `message.reasoning_delta` 与 `message.delta` 两个 canonical
  事件**：通道进事件名更干净，但要同时动 domain 常量、asyncapi、SSE 白名单、三个
  adapter 与全部前端 store，且与既有 `raw.chunk` 契约形成两套真相；当前收益不成比例。

## 复活条件

【出现不解析 `raw.chunk` 也必须区分推理的 canonical 消费方（如独立的推理归档或成本
归因面）→ 在 `MessageDeltaChannel` 处改读 canonical 字段或拆分事件 → 预埋要求：通道
判定必须保持单点，adapter 与前端不得各自解析】
