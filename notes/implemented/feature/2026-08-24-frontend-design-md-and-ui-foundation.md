# 前端 DESIGN.md 法典化 + ui/ 组件库基座（M0）

Status: implemented

输入调研：VoltAgent/awesome-design-md（73 份 DESIGN.md，含 claude.ai 官网
设计系统逆向）+ Anthropic 官方提示词教程补充源 + 本仓 `.cursor/skills/`
（ui-design-brain / using-ui-stack）。完整设计方案见
`agent-team-workbench-docs/frontend-design-md-redesign.md`。

## 决策与理由

1. **吸收方法论，不换皮（否决记录）**：否决把 claude.ai 的奶油底/珊瑚/
   衬线审美搬到工作台——本项目 IA 已冻结（`references/confirmed-ia.json`）、
   品牌蓝方向经产品评审基线确认，换皮 = 全量视觉回归。采纳的是 awesome-design-md
   的格式与纪律：YAML token frontmatter + Do's/Don'ts 双向约束 +
   Iteration Guide（一次一组件）+ Known Gaps 负向边界。
2. **`web/DESIGN.md` = 设计面单一真相源**，与 AGENTS.md 平行分工
   （AGENTS 管怎么构建，DESIGN 管长什么样）。颜色生效值的终极真相源仍是
   `index.css :root` + `tailwind.config.js`；DESIGN.md frontmatter 只引用
   不重造，漂移时以代码为准修文档——避免双真相源。
3. **token 纪律从提示词软约束升级为测试硬门禁**：`design-tokens.test.ts`
   全量扫描 TS/TSX 禁止内联色值；唯一豁免 `chat/blocks/ansi.ts`（协议键侧），
   豁免清单被断言钉死且要求文件存在防腐烂。多智能体并行写 UI 时，即使
   executor 没读 DESIGN.md 也会被打回——这是对风格漂移最便宜的防线。
   已知边界：门禁只拦字面量，**类名引用默认调色板**（如 `sky-500`）拦不住，
   靠迁移逐页收编（dashboard 迁移已收编两处）。
4. **组件库迁移三段式、删除优先**：M0 建 `ui/` 基座 + dashboard 示范；
   M1 逐页迁移（chat transcript 渲染区不动，其规格归 codex 逆向文档管）；
   M2 遗留类引用归零即从 index.css 成建制删，不留兼容别名。
5. **button 默认档是 secondary 而非 primary**：DESIGN.md 要求每区域至多
   一个 primary，默认档不带强调色。size=sm 无遗留对应物，按 4px 网格
   最小补齐并已回写 frontmatter 契约。

## 放弃了什么

- **暗色模式**：Known Gap 显式声明，不写 `dark:` 变体——单主题现状下
  不为不存在的主题付维护税（语义化 CSS 变量已留好扩展口）。
- **表单校验态视觉规格**：`ui/field` 只留 error 槽位占位约定，样式规格
  待立项，不臆造。
- **渲染层 DOM 测试**：项目无 jsdom/testing-library，ui/ 组件测纯映射
  逻辑；视觉等价性靠"类名逐字复刻"构造保证 + 后续浏览器巡检补强。

## 防回归断言

- `src/design-tokens.test.ts` 三条：豁免清单内容钉死、豁免文件存在、
  全量源码零内联色值（负向验证：探针文件 `#ff0000` 精确报红）。

## 后续路线（见设计方案 §4）

M1 逐页迁移（logs→tasks→settings→models→agents，一页一 executor 蜂群
并行，共享文件留合并阶段）；M2 删遗留类 + 首次成功路径接线 + Toast Undo +
骨架屏 300ms 阈值。
