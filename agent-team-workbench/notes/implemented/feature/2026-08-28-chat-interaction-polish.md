# chat 正文交互修复轮：思考面板折叠/滚动帽/扫光 + dock 终态投影

Status: implemented

任务分支 `zcode/chat-interaction-polish`（worktree 隔离）。决策依据：用户
2026-08-28「按 LanguageGUI→Ant Design X 的思路再打磨 chat 正文，顺带修复交互问题
（无法折叠收起、展示动态效果不对等）」。Ant Design X 侧只借 RICH 范式与
ThoughtChain/Bubble 交互语义，不引入 antd 组件库。

## 修掉的真 bug（均有测试钉）

- **思考面板流式期间无法折叠。** `ReasoningProcessPanel` 旧实现
  `open = expanded || streaming` 强制展开且 `onClick` 直接 return——长思考流式时
  面板是正文流里唯一不可控的占位，用户点不动。改为全程可折叠（流式仍默认展开），
  对齐 Ant Design X ThoughtChain 头行折叠语义。
- **滚动帽从未生效。** 体部 CSS 固定 `h-52`，但 framer-motion 展开动画落定后留
  inline `height:auto`，类高度被永久覆盖——长思考把正文流撑爆、`overflow-y-auto`
  成摆设。改 `max-h-52`：短内容自然高、长内容 208px 内滚（浏览器实测
  scrollHeight 356 > clientHeight 207）。
- **流式扫光是死的。** 扫光 span 的样式只挂在重构前已退役的
  `.chat-reasoning-toggle` 作用域（无背景、不可见），且 `inset-0` 与 keyframes 的
  `left` 位移动画冲突。挂回 `.chat-reasoning-panel` 作用域（300px 柔光 2.6s），
  死样式成建制删除。
- **dock 永远旋转的进行中。** run 到终态但模型未闭合 todo 时，in_progress 步骤的
  LoaderCircle 无限旋转（线上实见 succeeded run 停在 3/4）。新增展示层投影
  `projectWorkflowSteps`（不改数据层）：succeeded 未闭合步骤视为完成，其余终态
  in_progress → 已中断（CirclePause + warning 色），pending 保持。

## 小修

- 头行双 chevron（左右各一、方向语义矛盾）只留尾部一枚；删除编造的
  「持续了几秒」假时长副标题（无数据源的伪造事实）。
- ActivityGroup 折叠态 `aria-controls` 悬空指向未挂载元素，改为仅展开态输出。
- assistant-turn 的 `.chat-streaming` 类无任何 CSS 定义，按删除优先移除
  （保留 key 切换的 streaming→settled 重挂载语义）。
- `chat-rendering-spec.md` 同步 max-h 帽与扫光作用域订正。

## 发现的既有缺口（本轮不动，立项备查）

- **长 run 的思考过程整层消失。** `TIMELINE_CAP=500` 的 capTimeline 保留策略里
  reasoning-delta 与 text-delta 同为非结构帧按尾部填充；推理永远先于正文，
  长 run（如 9425 帧）的 8102 条 reasoning-delta 全部被淘汰——历史思考面板
  一个都不渲染。文本锚点（message.completed）在、推理不在。建议方向：推理摘要
  并入结构事件（adapter 侧 reasoning.completed 或挂进 message.completed payload），
  让内容随锚点存活；涉及 SSE 回放内存边界设计，另案评估。
- codex 真实通道当前不可验（registry 未提交态报「Codex 模型名不能为空」），
  流式思考面板的真模型实况未复核；结构、样式与折叠语义已由测试 + 夹具页 +
  生产页 DOM 断言覆盖。

## 验证

- `npx tsc -b` / `pnpm test`（447 全过，含新增 8 条钉）/ `pnpm lint` 全绿。
- 生产页实测（worktree dev）：dock 3/4 旋转 → 4/4 已完成；10 段历史思考面板
  渲染、单 chevron、max-h 内滚生效。
- 已知 harness 限制：本环境 IAB 合成事件（click/scroll）全面失效、桌面 CUA 无
  AX 授权，真实点击折叠未能在线演练；交互路径为裸 React onClick（旧阻塞点
  `if (streaming) return` 已删），结构语义由 render 测试钉死。
