# tx：正文独立暗色皮肤（纸墨倒置），全局适配延后

Status: implemented

决策依据：用户 2026-08-26 约束调整——「正文里面的这些适配都不需要和整体去做适配，
我们要先做的是一个完美的正文输出，最终最终再去做适配」。DESIGN.md 的「与水墨外壳
一致」约束对正文区（chat transcript 输出面）暂停执行；设计基准取 kvnkld 审批卡
（aicss.dev approval-card 原型）：层深、单强调色、微交互、内容即界面。
任务分支 `zcode/chat-transcript-redesign`。

## 决策与理由

- **架构 = 作用域覆写语义层，不自建类族**。`.tx-scope` 内覆写 `--color-*` 全套
  （surface/text/border/brand/status/identity 22+ 项）；Tailwind 色通道是
  `hsl(var(--color-*) / <alpha-value>)`，语义工具类与 chat-* 组件类在作用域内自动
  换肤，TSX 几乎零改动。新增结构（倒计时、图标芯片、回执行）只加少量 `--tx-*`
  变量（--tx-brand 朱砂微剂量、暗影、环进度）。门禁 `design-tokens.test.ts` 不放宽：
  色字面量只出现在 CSS 层（门禁只扫 TS/TSX）。
- **材质方向：松烟墨暗面**。224-226 冷灰蓝色相的 near-black 三阶层深：
  stream 面（--color-surface-base）→ 卡面（raised）→ 凹陷（sunken：终端/代码/diff）。
  **行动绿（152）是正文唯一强调色**，落在批准 CTA 与运行态；红=危险、琥珀=警示
  （仅中高风险出现）；朱砂经 `--tx-brand` 只做身份点（agent 角色点），延续品牌
  DNA。「纸墨倒置」：外壳宣纸、正文是墨，最终适配时可保留此对比（阅读面深色是
  生产力工具惯例）或单点回退。
- **挂载点**：chat.page 消息流滚动容器 + 底部固定区两处 `.tx-scope`；chat 头部
  （身份/运行控制条）属壳层不动。run-panel 等页面审批面不在作用域内，不受影响。
- **hljs**：全局 github.min.css（light）不动；`.tx-scope` 内覆写 hljs token 类为
  暗色（不加第二个全局主题 import，避免冲突）。
- **审批卡倒计时产品语义**：仅 `risk=low && kind∈GRANTABLE && pending` 展示，
  30s 归零自动 `resolveApproval('approved', scope='once')`；不做 hover 暂停
  （行为可预期优先）；`prefers-reduced-motion` 下滚动数字退化为静态数字；
  组件卸载/决议后清 timer，busy/重复触发有 ref 守卫。
- **审批卡三变体（样式分型）**：command → mono 命令块（$ 前缀）；question →
  对话式 prose；file_change/permissions/tool → prose 摘要。决议后从居中小字升级为
  左对齐回执行（状态图标 + 人话标题 + 时间）。

## 放弃了什么

- **plan/question 变体的契约升级**：`ApprovalRequest` 无 plan steps / question
  options 字段，内嵌 todo 预览（kvnkld 版招牌）需要后端先扩契约，本次不做——
  不发明后端不存在的数据。
- **中/高风险倒计时**：风险不对称，自动批准越权；高风险卡反而要做重（红图标芯片
  + 风险显式标注）。
- **独立 tx 类族整体替换 chat-\* 类**：语义层覆写已覆盖 90% 换肤，类名不动减小
  diff；仅审批卡等新结构用新类。

## 负向保证

- `.tx-scope` 外零影响：全部覆写收敛在作用域内，移除两处挂载类名即回水墨基线。
- 最终适配是单点操作：调亮/删改 `.tx-scope` 的 `--color-*` 覆写段，或重挂载点，
  不需要触碰组件结构。
- 门禁零豁免扩容：`design-tokens.test.ts` 的 EXEMPT_FILES 保持只有 ansi.ts。
