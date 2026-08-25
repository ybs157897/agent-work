# 前端架构与交互重设计方案 —— DESIGN.md 法典化与组件库重构

日期：2026-08-24
状态：M0 已实施（见 §5 提交清单），M1/M2 为后续路线
输入：
- VoltAgent/awesome-design-md 仓库调研（73 份 DESIGN.md，含 `design-md/claude/DESIGN.md`）
- Anthropic 官方提示词工程教程（`anthropics/prompt-eng-interactive-tutorial`，补充来源）
- 本仓库 `.cursor/skills/`（`ui-design-brain`、`using-ui-stack` 等 66 个技能）
- 前端现状摸底（`agent-team-workbench/web/`）
- 产品评审留痕 `agent-team-workbench/notes/product/2026-08-24-project-review.md`

---

## 0. 调研结论与设计决策（先说取舍）

### 0.1 awesome-design-md 的真相

该仓库**不是** "Claude 模型提示词最佳实践"资料集，而是 **DESIGN.md 设计规范文件集合**：从 73 个知名网站提取的、供 AI 编码代理阅读的设计系统文档。其中与 Claude 相关的唯一条目 `design-md/claude/DESIGN.md` 是 **claude.ai 官网视觉系统的逆向分析**（暖奶油底 `#faf9f5` + 珊瑚 CTA `#cc785c` + 衬线展示字体），而非模型使用方法论。

仓库真正的可迁移价值是**格式本身**——每份 DESIGN.md 都是一套"用结构化文档约束 AI 生成前端"的完整提示词策略：

1. **单文件设计上下文注入**：把 DESIGN.md 放进项目根，告诉 agent 去读它——"Markdown is the format LLMs read best"。
2. **双轨表达**：YAML frontmatter 给机器可读的精确 token，正文给人类/模型可读的设计意图。
3. **token 引用纪律**：组件样式一律引用 `{colors.xxx}`，"never inline hex"——可被程序校验。
4. **Do's and Don'ts 双向约束**：正向指令 + 负向禁令同等重要；负向禁令是防止多 agent 各自发挥的最便宜手段。
5. **Known Gaps 负向边界**：显式声明未覆盖范围（动画时长、校验态等），防 agent 臆造。
6. **Iteration Guide 迭代协议**："一次只改一个组件，引用其 token key"——与蜂群调度的子任务边界纪律同构。

Anthropic 官方教程的补充要点（用于 §2 提示词策略）：角色设定要带全量上下文；数据与指令用 XML 标签分离；结构化输出 + prefill 强制输出起点；few-shot 示例是最有效的格式约束手段；复杂提示词按 10 元素顺序组织（角色 → 上下文 → 规则 → 示例 → 数据 → 末尾重述任务 → 输出格式）。

### 0.2 关键决策：吸收方法论，不换皮（否决记录）

**否决**：把 claude.ai 的奶油底 + 珊瑚 + 衬线审美搬到工作台。
**理由**：
- 本项目信息架构已冻结（`references/confirmed-ia.json`：60px 图标导航 + fluid 主区 + 280px 详情面板），品牌方向已定（蓝 `#3B82F6→#0EA5E9`、"轻盈亲和指挥舱"），且经过产品评审基线确认。
- 换皮 = 全量视觉回归，与当前"补交付质量"的阶段目标冲突。
- DESIGN.md 的价值在**钉死既成事实**形成单一真相源，而不是发明新视觉。

**采纳**：DESIGN.md 格式、token 纪律、双向约束、负向边界、迭代协议，全部落地；其中 token 纪律从"提示词软约束"升级为"测试硬门禁"（§2.3）。
**留口**：`--color-surface-warm`（40 20% 98%）token 已存在但基本未用——未来若要引入 Claude 式暖调编辑感（如知识层/文档阅读场景），以它作为主题扩展锚点，不改主色板。

---

## 1. 前端组件库重构建议

### 1.1 现状诊断（事实）

| 维度 | 现状 | 问题 |
|---|---|---|
| 设计系统载体 | `index.css`（877 行）`@layer components` 的 `@apply` 类约 110 处 + `tailwind.config.js` token | 类名字符串无类型检查；变体靠手工拼类（`btn` + `btn-primary`） |
| 组件库 | **无** `ui/` 目录；`modal`/`drawer`/`toast`/`toggle`/`avatar`/`status` 等散件在 `components/` 根 | 无统一出口，交互态（四态）完整性靠自觉 |
| 页面消费方式 | 7 个页面直接拼魔法类字符串（`className="ui-card-padded"`） | 多 agent 并行开发时风格漂移无门禁 |
| chat 子系统 | 已组件化 + CSS Modules（`components/chat/blocks/`），对齐 Codex 桌面端逆向规格 | 是仓库内最佳实践，作为迁移参照物 |
| token 纯净度 | TS/TSX 零硬编码色值（已核验）；唯一特例 `chat/blocks/ansi.ts`（ANSI 协议键 → token 引用映射） | 基线干净，可直接上硬门禁 |

### 1.2 目标结构

```
web/src/components/ui/          # 设计系统组件库（M0 建立）
  button.tsx        # variant: primary | secondary | ghost；size: sm | md；四态完备
  card.tsx          # Card（= ui-card）+ padded/interactive 变体收敛为 props
  input.tsx         # Input（= input-field）、Textarea、Select
  field.tsx         # Field：label + 控件 + hint/error 三段式（表单唯一入口）
  status-pill.tsx   # = status-pill，语义色只允许 status-* token
  empty-state.tsx   # 空态：图标 + 标题 + 引导动作（对应 §3.1 首次成功路径）
  index.ts          # barrel 导出
```

设计原则（来自 `ui-design-brain` / `using-ui-stack` 技能，已写进 `web/DESIGN.md`）：

- **变体是 props 的穷尽联合类型**，不允许页面层拼魔法类；新增变体必须先改组件 + DESIGN.md 的 `components:` 条目（"变体作为独立条目"迭代规则）。
- **四态强制**：每个交互组件必须实现 hover / active / focus-visible / disabled；focus-visible 走 `index.css` base 层全局 ring。
- **按钮标签动词开头**，每区域最多一个 `primary`。
- **语义色纪律**：品牌色只做强调与交互；状态表达只能用 `status-*`；禁止引入第二强调色。

### 1.3 迁移策略：三段式，删除优先（不留垫片）

- **M0（本次）**：建 `ui/` 基座 + `dashboard` 页示范迁移。新旧并存期仅限迁移窗口。
- **M1**：逐页迁移，顺序按收益/风险：`logs`（最小）→ `dashboard`（已完成）→ `tasks` → `settings` → `models` → `agents`（920 行最重，最后）。`chat` 页只迁外围（左栏/会话列表），**transcript 渲染区不动**（已对齐 Codex 规格，动它 = 视觉回归）。
- **M2**：某类旧组件类（如 `.btn-primary`）引用归零即从 `index.css` 成建制删除；不做兼容别名。

### 1.4 代码示例（M0 已实现，实际代码见 `web/src/components/ui/`）

```tsx
// button.tsx —— 变体收敛进组件，页面层只见 <Button variant="primary">
const variants = {
  primary: 'bg-brand-primary text-text-inverse shadow-sm hover:bg-brand-accent active:scale-[0.98]',
  secondary: 'border border-border-strong bg-surface-raised text-text-secondary hover:bg-surface-base hover:text-text-primary active:scale-[0.98]',
  ghost: 'border border-brand-primary/30 bg-transparent text-brand-primary hover:bg-brand-primary/5 active:scale-[0.98]',
} as const;

export function Button({ variant = 'secondary', size = 'md', className, ...rest }: ButtonProps) {
  return <button className={cx('inline-flex items-center justify-center gap-2 rounded-button font-medium transition-all duration-150',
    'focus-visible:ring-2 focus-visible:ring-brand-primary/40 focus-visible:ring-offset-2',
    'disabled:opacity-50 disabled:cursor-not-allowed disabled:pointer-events-none',
    size === 'sm' ? 'px-snug py-1 text-caption' : 'px-base py-tight text-body',
    variants[variant], className)} {...rest} />;
}
```

---

## 2. 关键页面的提示词集成策略

### 2.1 DESIGN.md = 设计面单一真相源

`agent-team-workbench/web/DESIGN.md`（本次落地）与 `AGENTS.md` 平行分工（awesome-design-md 的原始设定）：

| 文件 | 读者 | 定义什么 |
|---|---|---|
| `AGENTS.md` | coding agents | 怎么构建（纪律、命令、约束） |
| `web/DESIGN.md` | design agents（含 pixel persona 与蜂群 executor） | 应该长什么样（token、组件、Do/Don't、Known Gaps） |

结构完全对齐 awesome-design-md 规范：YAML frontmatter（colors/typography/spacing/rounded/shadows/components，全部引用 `--color-*` 真相源）→ Overview → Colors → Typography → Layout → Elevation → Shapes → Components → **Do's and Don'ts** → Responsive → **Iteration Guide** → **Known Gaps**。

### 2.2 三处接线

1. **根 `AGENTS.md`** 增补一条：凡改动 `agent-team-workbench/web/` 的视觉与交互，先读 `web/DESIGN.md`；token 只引用不硬编码。
2. **`agents/pixel/prompt.md`**（UI 设计师 persona）：从"遵循项目既有设计系统"升级为"以 `web/DESIGN.md` 为唯一事实源，输出必须引用其 token key，未知项按 Known Gaps 声明不臆造"。
3. **蜂群前端子任务提示词模板**（§2.4）：把 Iteration Guide 的"一次一组件"协议直接作为子任务拆解模板。

### 2.3 token 纪律硬门禁（提示词约束 → 编译期约束）

`web/src/design-tokens.test.ts`（本次落地）：扫描 `src/**/*.{ts,tsx}`，禁止十六进制色、`rgb(`/`hsl(` 字面量；豁免面仅 `chat/blocks/ansi.ts`（终端色协议键 → token 映射），且豁免清单本身被测死（文件消失则测试失败，防豁免面腐烂）。

设计意图：即使某个 executor 没读 DESIGN.md，测试也会把它打回。这是对"多智能体并行写 UI 风格漂移"最便宜的防线，对应仓库纪律"每修一个 bug 钉一条防回归断言"。

### 2.4 蜂群前端子任务提示词模板（可直接粘贴）

```text
角色：你是 agent-team-workbench 前端开发者。改动前先读两份文件：
仓库根 AGENTS.md（开发纪律）与 agent-team-workbench/web/DESIGN.md（设计事实源）。

任务：<一次只改一个组件/一个页面，例如：把 tasks.page.tsx 的统计卡迁到 ui/ 组件>

约束：
- 颜色/间距/圆角只允许引用 DESIGN.md frontmatter 中的语义名；禁止内联色值。
- 交互元素四态完备（hover/active/focus-visible/disabled）。
- 文件边界：只允许触碰 <文件清单>；共享文件（package.json、tailwind.config.js、index.css）不在你的范围内。
- 未知设计决策：查 DESIGN.md 的 Known Gaps；未覆盖的不要臆造，在交付说明中上报。

验收：
- pnpm tsc --noEmit 通过；pnpm test <触面测试> 通过；
- 交付说明列出：改动文件、影响的视觉面、是否需要浏览器复核。

输出格式：先给结论（改了什么/验证结果），再列文件清单。
```

该模板即 Anthropic 10 元素结构的精简工程化版本（角色带全量上下文 → 规则含兜底 → 数据边界 → 末尾验收标准 → 输出格式）。

---

## 3. 用户体验优化点

来源：产品评审（用户体验 6.5 分）+ `ui-design-brain` 反模式清单 + `using-ui-stack` 硬性指标。

| # | 优化点 | 依据 | 落点 | 期次 |
|---|---|---|---|---|
| 1 | **首次成功路径**：dashboard 空态不再只报数字，给三步引导（注册模型 → 配置智能体 → 创建任务），每步直达对应页面 | 评审原话"缺首次成功路径向导" | `ui/empty-state` + dashboard 迁移（M0 组件就绪，接线在 M2） | M2 |
| 2 | **信息密度降级**：统计卡只保留需要人做决定的数字，其余进二级；卡片高度节奏不均等（对齐 `confirmed-ia.json`） | 评审"信息密度接近上限" | dashboard 迁移示范（M0 局部体现） | M1 |
| 3 | **骨架屏 > 300ms 才显示**，替代首屏白等 | `ui-design-brain` components.md | `async-state.tsx` 增加延迟阈值 | M1 |
| 4 | **破坏性操作带 Undo**：打回（return）、取消任务的 toast 附撤销入口 | `ui-design-brain`：Toast 4–6s + Undo | `toast.store` + `return-modal` | M2 |
| 5 | **五态巡检**：375/428/768/1280/1536 五档截图巡检（`responsive-testing` 技能）；`config-split` 双栏在 <768 折为抽屉 | `using-ui-stack` 触控目标 ≥44px | 每页迁移的验收动作 | M1 随页 |
| 6 | **空态文案**：任务/日志空态按 `writing-copy` 技能重写（说清楚"为什么空 + 下一步做什么"） | 评审 + 技能库 | 随页迁移 | M1 |
| 7 | **暗色模式**：不做（Known Gap）。token 已是语义化 CSS 变量，未来扩展成本低，但现在不为不存在的主题付维护税 | 单主题现状 + 删除优先 | `web/DESIGN.md` Known Gaps | 暂缓 |
| 8 | **键盘可达性**：`focus-visible` ring 已在 base 层；迁移时保证 `ui/` 组件全部保留 | `accessibility-auditing` 技能 | ui/ 组件验收标准 | M0 起 |

---

## 4. 实施步骤与路线

### M0（本次，已完成）——法典与基座

| 提交刀 | 内容 |
|---|---|
| docs | 本设计方案 + `web/DESIGN.md` |
| feat(web) | `components/ui/` 基座（button/card/input/field/status-pill/empty-state）+ 变体逻辑单测 |
| test(web) | `design-tokens.test.ts` token 纪律硬门禁 |
| chore | 根 `AGENTS.md` 增补 + `agents/pixel/prompt.md` 接线 |
| feat(web) | dashboard 页迁移到 `ui/` 组件（示范） |

验证门禁（证据匹配表面）：`pnpm tsc --noEmit && pnpm test && pnpm lint`；dashboard 改动需浏览器目检（`visual-qa-testing` 技能）。

### M1——逐页迁移（蜂群并行方案）

拆分原则：一页一 executor，文件边界互斥；共享文件（`index.css` 旧类删除）留到 M2 由主导者集中执行。

1. `logs`（63 行，热身）
2. `tasks`（476 行 + 详情/弹窗子模块，最大收益：看板卡 → `Card`，状态徽标 → `StatusPill`）
3. `settings`（429 行）→ `models`（972 行）→ `agents`（920 行，配置工作台 `config-*` 类族保留为布局语言，只迁控件）

每页验收：类型/测试/lint + 五档视口截图巡检 + 交付说明。

### M2——清场与体验补强

1. 旧组件类引用归零即删（`.btn*`、`.ui-card*`、`.status-pill`、`.input-field`）；`config-*` 与 `chat-*` 布局类族保留（它们是布局语言不是组件类）。
2. 首次成功路径引导接线（§3-1）；Toast Undo（§3-4）；骨架屏延迟阈值（§3-3）。
3. 全量视觉巡检 + `notes/` 收尾留痕。

---

## 5. 风险与对策

| 风险 | 对策 |
|---|---|
| 迁移中视觉回归（页面拼类与组件默认态不一致） | 每页迁移后浏览器 before/after 截图对比（`comparing-branches-visually` 技能）；chat transcript 区明确不动 |
| executor 绕过 DESIGN.md 自创样式 | `design-tokens.test.ts` 硬门禁 + 子任务模板的文件边界 |
| DESIGN.md 与 tailwind.config 漂移 | token 真相源仍是 `index.css`/`tailwind.config.js`，DESIGN.md frontmatter 只引用不重造；发现漂移以代码为准修 DESIGN.md |
| 豁免面扩张（越来越多人往豁免清单塞文件） | 豁免清单被测死且要求注明理由；新增豁免必须在 PR/notes 留痕 |

---

## 6. 对话页体验精修（2026-08-25 并入实施）

输入：知乎开放平台 CLI 站内调研（官方 `zhihu-cli` search）。三条公认结论并入本刀：

1. **运行可见性**（AG-UI 实践："没有运行中可见性的会话，用户放弃率是有实时进度面板的 3 倍"）——执行过程要有视觉存在感，不能渲染成事后文档。
2. **Chat UI ≠ Agent UI**（dd-y）：Agent UI 围绕 Run/Step/ToolCall/Approval/Artifact 组织，用户关心"正在做什么/为什么没完/要不要我确认"。我们的协议层对象一一对应，缺的是呈现。
3. **结构化信息优于纯文本**（Google Research 生成式 UI 实验：83% 偏好率）——远期方向：A2UI/生成式卡片，本刀不实施。

### 本刀落地清单（全部在 DESIGN.md token 体系内）

| # | 项 | 对应洞察 | 改动面 |
|---|---|---|---|
| 1 | 助手回合头：头像 + 角色名 + 时间 | 运行可见性（回合归属一目了然） | `assistant-turn.tsx` / `transcript-view.tsx` |
| 2 | 用户气泡品牌浅底表面 | 表面节奏（白底气泡不可见） | `transcript-view.tsx` UserBubble |
| 3 | 空会话脚手架：按角色建议首条提示词 chips | 首次成功路径 | `chat.page.tsx` + `utils/chat-session-visuals.ts` |
| 4 | Composer：运行中停止按钮入坞、发送按钮换 ui/Button（顺手消灭 `text-white` 非语义类） | 控制权可见 | `chat.page.tsx` |
| 5 | 会话列表状态点（思考中脉冲/已回复绿/失败红/待审批黄） | 运行可见性 | `chat.page.tsx` + 纯函数 helper + 单测 |

活动/消息分离（工具活动组折叠）已有实现（ActivityGroup，终态自动收拢），本刀不动；生成式 UI（A2UI）列为 LATER，待审批/表单类交互密度上升后再评估。
