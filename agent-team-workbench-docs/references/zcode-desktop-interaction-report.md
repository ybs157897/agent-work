# ZCode 桌面 App 对话交互逆向报告

> 给后续改 workbench chat / LanguageGUI 的 agent 用：说明 ZCode 桌面端「思考 → 工具 → 最终正文」如何渲染与联动，便于对齐交互语义，**不要抄组件库、不要贴其源码**。
>
> 研究对象：本机 `/Applications/ZCode.app`（`dev.zcode.app`，版本 **3.10.1** / build `3.10.1.6272`，Electron 41 + React 19）。
> 对照对象：本仓 `agent-team-workbench/web` 对话子系统（见 `chat-rendering-spec.md`、既有 `zcode/chat-interaction-polish` 等支线）。
> 方法：`app.asar` 抽出静态分析（主 bundle `out/renderer/assets/styles-*.js` + CSS）+ 本机 `~/.zcode/cli/agents/**/transcript.jsonl` 事件序列统计。未改专有软件；下文只描述行为与可复查符号名。
> 日期：2026-08-28。
> 姊妹文档：[codex-desktop-rendering-comparison.md](./codex-desktop-rendering-comparison.md)（Codex/ChatGPT 桌面对照）；
> 蜂群正文草案（本仓自拟，非 ZCode 行为）：[swarm-chat-body.md](./swarm-chat-body.md)。

---

## 0. 给执行 agent 的结论摘要（先读这段）

| 问题 | ZCode 怎么做 | 对本仓的含义 |
| --- | --- | --- |
| 时间线结构 | **按 row 纵向追加**，不是「整 turn 重排槽位」 | 多段思考/工具/正文各自成段，顺序=发生顺序 |
| 思考 UI | 独立 `Reasoning` 折叠面板；流式可收；收起时头行扫光「Thinking」+ **末行横滚 ticker** | 对齐 ThoughtChain 头行，勿强制 `open \|\| streaming` |
| 思考结束 | `autoCollapseKey` 从 streaming→settled 变一次 → **自动收起**（用户点过则尊重） | complete 时收，手动展开不抢 |
| 多段思考 | 默认设置显示全部；关掉「显示推理」后 **只留第一段** | 多阶段 run 要能并排多块，不能只留最后一段 |
| 工具 UI | 单卡 + Explore/终端/写文件/CUA **成组**；`autoCollapseOnComplete` | 完成即摘要行，进行中扫光 |
| Final answer | 新 `assistantText` row；高 >≈120px 默认裁切 Expand | 长文折叠与思考折叠分开 |
| Final 时上方 | 已完成的思考/工具 **保持折叠摘要**，不随 final 再展开 | 上方内容「退场成摘要」，不是删除 |
| 异常分层 | **工具失败留在工具卡**；**模型/网络/配额失败走输入区 Error Banner**；取消 → turn `interrupted`/`cancelled`，未必弹 banner | 勿把 tool error 画成 assistant 正文；勿把 model error 塞进时间线当普通 bubble |
| 自动重试 | 模型层 `maxAttempts≈11` / `maxRetries≈10`；可重试失败先内部重试，耗尽才上屏 | UI 重试按钮是用户动作，与 SDK 自动重试分离 |

**负向保证（抄交互时别踩）：**

- 不要把工具卡伪装成 assistant 正文或 LanguageGUI ContentBlock。
- 不要把多段 reasoning 合成一块「本轮思考」——ZCode 是 **一段模型循环 = 一段 reasoning row**。
- 不要在流式期禁止折叠。
- 不要用「FinalAnswer」特殊节点名——ZCode 侧就是 `assistantText` / `text_*` 流。
- 不要把 tool execution failure 升级成整轮 chat error banner（ZCode 默认工具失败可回流模型继续转）。
- 不要对「用户取消 / 队列抢占」一律弹致命错误条（多为 cancelled，telemetry 记 fail，UI 常示 interrupted）。

---

## 1. 分析对象与证据地图

### 1.1 Bundle

| 项 | 路径 / 事实 |
| --- | --- |
| App | `/Applications/ZCode.app` |
| 主包 | `Contents/Resources/app.asar`（`@zcode/desktop`） |
| Renderer | `out/renderer/index.html` → `assets/index-*.js` + 大包 `assets/styles-*.js` + `assets/styles-*.css` |
| 本地数据 | `~/.zcode/cli/db/db.sqlite`、`~/.zcode/cli/agents/sess_*/agent_*/transcript.jsonl` |
| 产品站 | `https://zcode.z.ai`（桌面 UI 不依赖其 HTTP 反代） |

ZCode **不暴露**对话 UI 的本地 HTTP 端口；「反代看壳」只能静态 serve renderer（缺 preload/IPC，跑不了真对话）。动态结论来自 asar + transcript，不是浏览器 CDP。

### 1.2 关键符号（minified displayName / 数据契约）

| 符号 | 角色 |
| --- | --- |
| `Reasoning` / `ReasoningTrigger` / `ReasoningContent` | 思考折叠三件套（displayName） |
| `row.kind ∈ {reasoning, toolCall, assistantText, userInput, …}` | 时间线原子 |
| `row.appended` / `row.upserted` / `row.delta` / `row.removed` | 时间线补丁 op |
| `agent_thought_chunk` / `agent_message_chunk` / `tool_call` | 运行时事件（telemetry / host） |
| `reasoning_*` / `text_*` / `tool_input_*` | transcript `model_streaming.payload.kind` |
| `autoCollapseKey` | 思考：settled 时触发自动收 |
| `autoCollapseOnComplete` | 工具：running→done 自动收 |
| `messageStreamShowReasoning` | 设置项，默认 **true** |
| `messageStreamFirstReasoningRowId` | 关闭「显示推理」时只显示第一段 |

### 1.3 与 Codex 桌面的结构差异（避免混抄）

| | ZCode | Codex 桌面（见姊妹文档） |
| --- | --- | --- |
| 回合编排 | **时间序 row 流** | turn 内 **固定槽位重排** |
| 思考归属 | 独立 `reasoning` row，可多段 | 常挂在 agent-activity 组内 |
| Final | 普通 `assistantText` | `assistant-item` 槽 |
| 工具 | 可 Explore/Execute/Changes/CUA 成组 | activity-collapsible |

抄 ZCode 时按 **时间线追加** 建模；抄 Codex 时按 **turn 槽位** 建模——不要混。

---

## 2. 流事件 → 时间线 row（数据面）

### 2.1 单次 model 请求内的流形态

本机最近长 session（`transcript.jsonl`）统计的 `model_streaming.payload.kind` 形状：

| 压缩形态 | 含义 | 出现频次（该样本） |
| --- | --- | --- |
| `S-R-Tool-F` | start → reasoning → tool_input/tool_call → finish | 多 |
| `S-R-T-Tool-F` | 推理后先吐一点 text 再 tool | 有 |
| `S-Tool-F` | 无推理直接 tool | 有 |
| `S-R-T-F` | 推理后直接 final text（无 tool） | 少 |

典型闭环（多跳 agent）：

```
MODEL_REQ
  └─ stream: start → reasoning_start/delta/end → tool_input_* → tool_call → finish
TOOL_RUN → TOOL_DONE
MODEL_REQ
  └─ …（再一段 reasoning + tool）…
MODEL_REQ
  └─ stream: … → text_start/delta/end → finish   ← 用户看到的 final answer
TURN_DONE
```

要点：**每一轮 model 循环都可以再产一段 reasoning**；final 不是特殊事件名，就是最后一轮（或同轮）的 `text_*`。

### 2.2 时间线补丁

UI 状态由 delta op 驱动（schema 可见）：

- `row.appended` / `row.upserted` — 新建/覆盖整行
- `row.delta` — 按 path 追加文本（`text` → reasoning/assistantText；`inputText` / `output.text` → toolCall）
- `row.removed` / `state.updated`

同 path 追加规则（概念）：

| delta path | 作用对象 |
| --- | --- |
| `text` | `assistantText` 或 `reasoning` |
| `inputText` | `toolCall` |
| `output.text` | `toolCall.output` |
| `summaryText` | `subagent` |

### 2.3 渲染开关：哪些 reasoning 可见

```
visible(row) =
  settings.messageStreamShowReasoning === true
  OR row.rowId === messageStreamFirstReasoningRowId
```

`messageStreamFirstReasoningRowId` 取当前 assistant work 区里 **第一个** `kind===reasoning` 的 rowId。

- 默认设置开：多段全部画出来。
- 设置关：只保留第一段，后续循环的思考整行不渲染（`case reasoning: return nQ(...)? <D8e/> : null`）。

---

## 3. 思考模式 UI（Reasoning）

### 3.1 组件树

```
[row reasoning]
  Reasoning (isStreaming, autoCollapseKey, duration?)
    ReasoningTrigger     ← 头行可点
    ReasoningContent     ← 体部，可延迟卸载
```

行级封装行为（概念）：

- `isStreaming = (row.state === 'streaming')`
- 流式且 `text.length===0` → **不渲染**（避免空壳闪一下）
- `autoCollapseKey = streaming ? null : row.state`  
  → 从 streaming 落到 completed 时 key 变化一次，触发自动收起
- `duration` = `ceil(durationMs/1000)` 秒（有则显示）

### 3.2 头行状态机

| 状态 | 头行文案 / 视觉 |
| --- | --- |
| 流式 + 展开 | 扫光「Thinking」类文案（i18n `chat.reasoning.thinking`）+ chevron 旋转 |
| 流式 + 折叠 | 扫光 Thinking · **当前思考最后一行**横滚（overflow hidden，自动 scrollLeft=scrollWidth） |
| 已完成 | 「Thought · Ns」或「Thought · 几秒」（`chat.reasoning.thought` + duration） |
| Chevron | 展开 `rotate-90`；折叠默认透明，hover 才显 |

末行 ticker：从全文按行倒找第一条非空 trim 行；内容变了用 contentKey 做轻量换行动画；过宽时左右 mask。

### 3.3 体部

- `max-h-60`（240px）+ `overflow-auto`
- 默认变体左边线：`ml-2 border-l pl-3.5`
- `whitespace-pre-wrap break-words`
- **粘底滚动**：距底部 ≤ 2px 视为贴底；贴底时内容增高跟滚；用户上翻则取消粘底
- 上下滚动 fade mask（`data-reasoning-scroll-mask`: none/top/bottom/both）
- 折叠：内容区先关，**300ms** 后再 `shouldRenderContent=false`（退场动画窗口）

常量（bundle 内）：

| 名 | 值 | 用途 |
| --- | --- | --- |
| 时长 tick | 1000ms | 流式展开时每秒刷新「已思考秒数」 |
| 卸载延迟 | 300ms | 折叠后退场 |
| 贴底阈值 | 2px | stick-to-bottom |

### 3.4 自动折叠 vs 用户意图

```
shouldAutoCollapse =
  autoCollapseKey != null
  AND autoCollapseKey !== previousKey
  AND !userInteracted
```

任一用户点击开合 → `userInteracted=true` → 之后 settled **不再**强制收。  
受控 `open` prop 存在时不走这套（完全听父级）。

### 3.5 多段思考动态（执行 agent 必懂）

```
时间 →
  [Thinking #1 流式…] → [#1 自动收成 Thought · 3s]
  [Tool A 卡…] → [A 完成自动收]
  [Thinking #2 流式…] → [#2 自动收]
  [Tool B…]
  [assistantText final…]
```

- 各段 **独立** Reasoning 实例，不共享 open 状态。
- Final 出现时：上方已 settled 的思考保持折叠摘要；**不会**因为 final 再全部展开。
- 若用户在 #1 流式时手动展开并保持：该实例因 `userInteracted` 可能不在 complete 时强制收（与「默认自动收」并存）。

---

## 4. 工具调用展示

### 4.1 单卡

工具行 → `toolCallNode` 渲染器。通用折叠壳支持：

| 能力 | 行为 |
| --- | --- |
| `isRunning` | 头行 kindLabel 用 `animated-gradient-text` 扫光 |
| `autoCollapseOnComplete` | `wasRunning && !isRunning` → 强制收起并写入持久 open map |
| `autoOpen` / `forceOpen` / `canToggle` | 权限/特殊工具可禁折叠或强制开 |
| 摘要行 | kindLabel · primaryText · secondaryText；展开可换文案 |
| 状态 | statusLabel / failure；可悬停说明 |

完成态默认变成 **一行摘要**（省纵向空间），细节在展开后。

### 4.2 成组（展示层投影，不改底层 row）

在 `assistantWork` 行序列上再投影：

| 组 | 条件（概念） | 合成 toolName |
| --- | --- | --- |
| `exploreGroup` | 连续 explore 类工具 | `Explore` |
| `executeGroup` | 连续终端/执行类 | `ExecuteGroup` |
| `changesGroup` | 连续 file-write 族 | `ChangesGroup` |
| `cuaGroup` | Computer Use 官方轨迹 | 把同 response 的 tool / reasoning / message 吸进组事件流 |
| `agentToolCall` | Agent/Task/subagent + 子会话 | 父工具 + subagent row |

`stageTailIsRunning`：若时间线尾仍在跑，组状态保持 `in_progress`，避免过早 `completed`。

设置项（默认）：

- `toolGroupingExploreEnabled` ≈ true  
- `toolGroupingTerminalEnabled` ≈ true  
- `toolGroupingChangesEnabled` ≈ false  

Todo 族工具可用 `messageStreamShowTodos` 隐藏（默认 false）。

### 4.3 工具与思考的交错

工具 **不会** 嵌进 Reasoning 面板内部（非 CUA 组时）。时间线上就是：

```
reasoning → toolCall → toolCall → reasoning → toolCall → assistantText
```

CUA 例外：官方 CUA 轨迹可把 reasoning/assistantMessage **吸收为组内 event**，外层只见 `cuaGroup`。

---

## 5. Final answer（assistantText）与上方联动

### 5.1 正文行

- Markdown / 富文本渲染在 `assistantText` row。
- 长文外壳：内容测量 `scrollHeight`；超过 **120px + 1** 出现 Expand；默认裁到约 120px 高。
- 用户点 Expand 后保持展开；`contentText` / `rowId` 变化时重置折叠意图。
- 流式过程中 ResizeObserver / rAF 重测，避免半截高度算错。

### 5.2 「Final 出现时上面怎么样」——准确语义

没有名为 FinalAnswer 的特殊节点。当 `text_*` 开始写入 `assistantText` 时：

1. **同循环内若刚结束 reasoning**：该 Reasoning 的 `autoCollapseKey` 已/将变为 settled → 默认自动收成「Thought · Ns」。
2. **已完成的 tool 卡**：`autoCollapseOnComplete` 已收成摘要行。
3. **历史多段**：仍占位，但是折叠摘要，不抢 final 的纵向注意力。
4. **不会删除** 上方思考/工具 DOM 结构（除非设置隐藏推理）。
5. **新一轮** 再来 reasoning：在 final **之后** 追加新 row（多跳），不是改写旧 final。

视觉节奏：上面逐步「退成细头行」，下面 final 成为主阅读面；长 final 自己再套一层 120px 帽。

### 5.3 动画语言（CSS 可复查）

| 动画 | 用途 |
| --- | --- |
| `gradient-flow` + `.animated-gradient-text` | Thinking / 运行中工具标题扫光 |
| `zcode-collapsible-up` / fade-out | 折叠高度与透明度 |
| `zcode-stream-text-in` | 流式文本进入 |
| `zcode-stream-marker-in` | 流式标记色过渡 |

主题 token：`--animated-gradient-text-strong/soft`（亮/暗/zai 主题各有一套）。

---

## 6. 整轮交互时序图（多段）

```
User prompt
    │
    ▼
┌─ Model loop 1 ─────────────────────────────────────┐
│  Reasoning#1  [Thinking… / 可折 / 末行 ticker]      │
│       │ complete → auto-collapse → Thought · Ns     │
│  Tool cards…  [扫光] → complete → auto-collapse     │
└────────────────────────────────────────────────────┘
    │ tool results
    ▼
┌─ Model loop 2 ─────────────────────────────────────┐
│  Reasoning#2  …                                     │
│  Tool cards…                                        │
└────────────────────────────────────────────────────┘
    │
    ▼
┌─ Model loop N（收束）──────────────────────────────┐
│  （可选）Reasoning#N → auto-collapse                │
│  assistantText 流式 → 落定；>120px 则 Expand 帽     │
└────────────────────────────────────────────────────┘
```

---

## 7. 对本仓 workbench 的可执行对齐清单

对照已有实现（`chat-interaction-polish` / `chat-reasoning-fold` / `long-answer-fold`）与缺口：

| ZCode 行为 | 本仓应对 | 状态提示 |
| --- | --- | --- |
| 流式可折 + settled 自动收 | `ReasoningProcessPanel` 可折；勿 `streaming` 锁死 | 已部分对齐 |
| 思考体 `max-h` 内滚 | `max-h-52` 一类帽 | 已对齐思路（高度数值可再校准） |
| 流式扫光 | `.chat-reasoning-sweep` | 已修过死样式 |
| 多段思考并存 | chronological transcript 多 `thinking` 段 | 需保持；历史 cap 用 folded 投影 |
| 工具完成收成摘要 | ActivityGroup 折叠语义 | 对齐「完成即收」，别做成永远展开日志 |
| 长 final 帽 | long-answer-fold | 对齐「预览 + Expand」，阈值可参考 120px 量级 |
| 工具≠正文 | DESIGN.md 负向保证 | 保持 |

**建议后续若再打磨，优先顺序：**

1. settled 自动收 + 尊重 `userInteracted`（若尚未钉测试）。
2. 折叠态流式 **末行 ticker**（本仓目前多是整板/扫光，缺横滚末行）。
3. 多 loop 下「旧思考保持摘要、新思考新开一块」的视觉节奏验收用例。
4. 工具 `autoCollapseOnComplete` 与成组 in_progress 尾态。

---

## 8. 证据与局限

**证据：**

- asar：`/Applications/ZCode.app/Contents/Resources/app.asar` → `out/renderer/assets/styles-*.js|css`
- 真实流：`~/.zcode/cli/agents/**/transcript.jsonl`（`reasoning_delta` / `text_delta` / `tool_*` 序列）
- 错误面：asar 内 Error Banner / `jSe` 分类器 / `task_error`；本机 `~/.zcode/cli/log/zcode-*.jsonl`（`tool.call.failed`、`model.*.failed`、`turn.failed`、`model.retry.*`）
- 设置默认：`messageStreamShowReasoning ?? true` 等

**局限：**

- 未挂 Electron `--remote-debugging-port`，无运行时 DOM/动画帧截图（本机 Screencapture 权限拒绝）。
- 符号名为 minify 后 displayName / 局部函数行为还原，升级 3.x 小版本可能改 hash 文件名，**语义契约比文件名更稳**。
- 静态 HTTP serve renderer **不能**代替真 app（无 host RPC）。

**复现静态壳（仅资源）：**

```bash
npx asar extract "/Applications/ZCode.app/Contents/Resources/app.asar" /tmp/zcode-asar-inspect
cd /tmp/zcode-asar-inspect/out/renderer && python3 -m http.server 8766 --bind 127.0.0.1
# http://127.0.0.1:8766/  — 壳页面，对话不可用
```

若要肉眼跟一局动态：用  
`/Applications/ZCode.app/Contents/MacOS/ZCode --remote-debugging-port=9222`  
再 CDP 附着（会与单实例会话冲突，需用户同意重启）。

---

## 9. 异常与错误处理（模型 / 输出 / 工具 / 取消）

ZCode 把「坏结果」拆成 **三条面**，不要混成一种 UI：

| 面 | 典型原因 | UI 落点 | 是否终止 turn |
| --- | --- | --- | --- |
| A. 工具执行失败 | Read 超 token、Edit 未匹配、shell 非 0… | **工具卡** `status=failed` + `showFailureStatus` + errorText/tooltip | **通常否**：失败结果回灌模型，可继续下一 loop |
| B. 模型/供应商/网络失败 | 401/429/5xx、配额、空响应、超时、配置缺失… | **对话输入区上方 Error Banner**（非时间线 bubble） | **是**：`task_error` / `turn.failed`，generation/reasoning step 记 `fail` |
| C. 取消 / 中断 | 用户 Stop、队列抢占 `sendQueuedNow`、session stopped | 工作态 `interrupted` / history「已停止」；流日志常 `cancelled` | 是（主动中止）；**常不弹**「模型报错」式 banner |

本机当日日志抽样：`tool.call.failed`、`model.request.failed`、`model.sdk.stream.failed`、`turn.failed`、`model.network.failed`、`model.retry.*`、`zcode_protocol.v4.gateway_error` 均可见；大量 `stream.failed` 的 `reason=cancelled` 来自用户/队列中断，不是供应商挂了。

### 9.1 分层模型：errorSource × failureReason

可见错误上报前先走分类器（telemetry 事件名 `chat_error_banner` / group `ui_error`），归一成：

**errorSource（粗源）**

- `provider` — 供应商 HTTP/业务码
- `network` — 超时、断连、TLS、代理、idle timeout…
- `runtime` — 本地配置缺失、存储、非法输入、取消、流恢复丢弃…

**failureReason（细因，节选）**

| 集合 | 示例 |
| --- | --- |
| 网络类 | `network_error`、`timeout`、`proxy_error`、`stale_connection`、`stream_idle_timeout`、`tls_error` |
| 运行时类 | `model_config_missing`、`storage_error`、`invalid_input`、`cancelled`、`stream_recovery_discarded`、`provider_not_configured` |
| 供应商类 | `auth_failed`、`rate_limited`、`quota_exhausted`、`balance_insufficient`、`context_exceeded`、`empty_model_response`、`model_not_found`、`server_error`、`provider_overloaded`、`plan_expired`、`plan_access_denied` |

分类输入优先用 `error.attribution`（`source` / `statusCode` / `providerErrorCode` / `retryable` / `errorPhase` / `exceptionKind`），否则用 **HTTP 状态码启发式** + **文案正则**（如 413→context/invalid_request，402→余额，429→限流，5xx→server_error）。展示文案截断 **500** 字符再上报。

固定 code→reason 表（节选）：`MODEL_CONFIG_MISSING`→runtime/`model_config_missing`；`MessageAbortedError`→`cancelled`；`StartPlanBusyAutoRetryExhaustedError`→provider/`rate_limited`；`CAPTCHA_VERIFY_FAILED`→`auth_failed`；`StreamRecoveryDiscarded`→`stream_recovery_discarded`；`ERR_SQLITE_ERROR`→`storage_error`。

### 9.2 模型错误 → Error Banner（面 B）

**组件形态**（居中条，输入区一带，非 transcript row）：

- 图标 + **截断主文案**（`jb(error, intl)` 解析）
- 可选动作：
  - **Retry**（`onRetry`）
  - **展开详情**（`error.detail` → modal `<pre>`）
  - **复制全文**（message + detail + traceId 模板）
  - **反馈**（开 bug 单，可截窗，带 `traceId`）
  - **关闭**（dismiss）
- 配额/无可用模型类：换成 **升级** + **设置模型**，不走普通 Retry 套件
- 特殊抑制：`ZCODE_RUNTIME_MODEL_UNAVAILABLE` 且命中若干内置文案 → **直接不渲染 banner**（`Mb` 返回 null）

**文案解析优先级（概念）**

1. 无可用模型 code → `chat.error.noAvailableModel`
2. `CLAUDE_UNKNOWN_COMMAND` → 专用 i18n（可带参数）
3. 供应商业务码（`1005/1006/2007/3001…/429`）→ `zcode.error.providerBusiness.*`
4. 「可疑空模型输出」启发式 → `zcode.error.modelSuspiciousEmpty`
5. 白名单 runtime code → `zcode.error.${code}`
6. 否则裸 `error.message`

**供应商业务恢复动作**（`providerBusinessRecoveryAction`，与文案码绑定）：

| 业务码 | 建议动作 |
| --- | --- |
| 1006 | `login` |
| 1005 | `refresh-quota` |
| 3006 | `switch-model` |
| 3007 | `retry-captcha` |
| 3008/3009/3010 | `upgrade` |
| 3002 / 2007 / 429 | `retry-later` |
| 3001 | 无自动动作 |

Banner 首次可见时 `reportVisibleChatError` → ARMS 自定义事件（含 `error_key` 去重指纹、source/reason/phase、provider/model、retryable、status_code）。同一 `errorKey` 不重复刷上报。

**任务终态**：host 事件 `task_error` 会：

- 结束未闭合的 `reasoningStep` / `generationStep`（status `fail`，带 `errorType`/`errorMsg`）
- 清 task 本地 step 表
- 与 `task_complete` 对称（success vs fail）

空/异常模型输出：除 `empty_model_response` 分类外，还有 `modelSuspiciousEmpty` 文案路径——**当输出像「空成功」时仍当错误展示**，避免静默空白气泡。

### 9.3 工具失败（面 A）——留在卡上，不升格为 Banner

日志：`tool.call.failed`，常见 `code=TOOL_EXECUTION_FAILED` / `type=tool_execution_failed`（例：Read `read_output_too_many_tokens`；Edit 字符串未匹配）。

UI：

- 工具 row `status === 'failed'`
- 折叠壳 `showFailureStatus` + `statusLabel`；tooltip 可挂 `errorText`
- 失败输出可出现在卡展开区（与成功 output 同位），**不是** assistant Markdown
- 组状态映射：`error`→展示态 `failed`；`cancelled`→`stopped`

与模型错误的关键差：工具失败默认是 **agent 可继续的观测**；telemetry 里 task 级若最终因工具挂掉，会用 `TOOL_CALL_FAILED`（或首个工具错误文案）作为 task 失败摘要——那是 **整轮收束** 时的归因，不是每张失败卡都弹 Banner。

### 9.4 取消 / 中断（面 C）

| 信号 | 表现 |
| --- | --- |
| `model.sdk.stream.failed` + `reason=cancelled` | 用户 Stop / `v4 session stopped` / `sendQueuedNow preempts active turn` |
| `turn.failed` + `TURN_CANCELLED` | turn 中止；cause 常挂 `model_request_cancelled` |
| UI workStatus `interrupted` | 历史条显示「已停止」类文案（`chat.history.stopped`） |
| `assistantText.state === 'interrupted'` | 半截正文可保留；预览/投影仍可基于该行 |

取消路径上 SDK 仍可能走 retry 决策（`model.retry.delay.resolved`），但 `canRetry=false` + `reason=cancelled` 时 **不会**当真重试。UI 侧不要把取消画成「模型坏了」。

### 9.5 自动重试 vs 用户 Retry

- **自动**：适配器层 `maxAttempts≈11`、`maxRetries≈10`；网络类可 `model.network.retry_scheduled` / `model.retry.scheduled`。
- **用户**：Banner 上的 Retry 按钮——显式再发；与自动重试计数独立。
- **流恢复**：transcript 大量 `stream_recovery_anchor_created`（工具结果锚点等）；若恢复被丢弃 → runtime/`stream_recovery_discarded`，可进 Banner 分类。

### 9.6 对上方思考/工具在出错时的联动

| 失败类型 | 已在流中的 reasoning | 已在流中的 tool 卡 | 正文 |
| --- | --- | --- | --- |
| 工具失败（面 A） | 保持（通常已/将 auto-collapse） | 该卡标 failed；其它卡不变 | 无 final 或稍后模型再解释 |
| 模型失败（面 B） | step 记 fail；面板按 settled 规则收 | 已完成卡保持；进行中应变终态 | 可能无 `assistantText`，错误在 Banner |
| 取消（面 C） | 停在 interrupted/cancelled | 进行中 → stopped/cancelled | 半截 `interrupted` 可留存 |

**不要**在 model error 时把 Banner 文案再复制一份进 transcript 当 assistant 气泡（ZCode 主路径是 Banner + 可选 detail）。

### 9.7 对本仓对齐建议

1. **双通道**：`meta`/`error` 段或顶栏问题条 = 面 B；Activity/tool card 内状态色 = 面 A。  
2. 工具失败保持可回流（不要一失败就锁死 run），除非产品明确要 fail-fast。  
3. 取消与失败分文案（interrupted ≠ provider error）。  
4. 空输出/可疑空成功要有显式错误态（对齐 `empty_model_response` / `modelSuspiciousEmpty`）。  
5. Retry 按钮与自动重试策略分开文档化；问题条带 `traceId`/detail 展开利于排障。

---

## 10. 一页速查（给 agent 贴进 prompt）

```
ZCode chat interaction (v3.10.1):
- Timeline = ordered rows: reasoning* / toolCall* / assistantText
- Each model loop may emit its own reasoning row; final = last text_* as assistantText
- Reasoning: collapsible anytime; streaming collapsed shows Thinking + last-line ticker;
  on settle auto-collapse unless user toggled; body max-h-60 stick-to-bottom
- Tools: summary row when done (autoCollapseOnComplete); optional Explore/Execute/Changes/CUA groups
- Final: does not re-expand prior reasoning/tools; long text folds ~120px with Expand
- Setting messageStreamShowReasoning default ON; OFF → only first reasoning row visible
- Errors:
  - Tool fail → stay on tool card (failed); usually loop continues
  - Model/provider/network/quota/empty → chat Error Banner near composer (not a timeline bubble);
    classify errorSource×failureReason; Retry/detail/copy/feedback; business codes map to recovery actions
  - Cancel/preempt → interrupted/cancelled; often no fatal banner
  - Auto-retry in adapter (~11 attempts); user Retry is separate
- Do NOT merge all thoughts into one block; do NOT render tools as assistant prose;
  do NOT promote every tool error to a chat-wide banner
```
