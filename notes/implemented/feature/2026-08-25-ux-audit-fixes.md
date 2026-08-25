# UX 审计修复三刀：token 清账 / 可读性交互 / 契约修正

Status: implemented

输入：自审（代码 grep + 浏览器实测）产出的问题清单——响应式纸面承诺、
非语义色残留、小字对比度、假悬停、reduced-motion 漏覆盖。

## 决策与理由

1. **text-white 全量换 text-text-inverse**（15 处，含 index.css @apply）：
   反白文字走语义 token，门禁扩类名扫描后这类欠账不会再溜进来。
2. **Avatar 默认调色板 token 化为 identity-1..8**：值冻结自原 8 色（视觉
   零回归），语义限定"只用于头像/归属标记"——既不违反"状态色不做装饰"，
   又把颜色收进真相源。ring-white→ring-surface-raised。
3. **门禁从字面量扩到类名**：design-tokens.test.ts 新增 NON_SEMANTIC_CLASS
   （text-white + 默认调色板 bg/text/border/ring/from/to-*-\d），全文件扫描
   （含测试文件）。此前门禁只拦 hex/rgb/hsl 字面量，类名违规是已知盲区，
   这次实测到并钉死。
4. **tertiary/muted 亮度 52%→45%**：浅底对比度从约 3.4–4:1 提到 ≥4.5:1
   （AA）；45% 写进 DESIGN.md 作为"不得再调浅"的下限。
5. **响应式不修代码修承诺**：对话/配置分栏不收折是产品现实（桌面工作台），
   修法是 DESIGN.md 改 desktop-first 声明 + Known Gaps，而不是写一套没人
   维护的移动端样式。文档不再过度承诺断点行为。
6. **假悬停改真跳转**：dashboard Agent 速览行有 hover 无 onClick 是误导
   仿用；改 button + 直达 `/chat?agent=`（比删 hover 更有产品价值）。
7. **动画"闪"问题撤出范围**：截图落在半透明帧疑为 IAB 捕获窗口问题，
   不可复现就不修——证据匹配表面。

## 放弃了什么

- 移动端收折实现（立项前只声明不支持）。
- artifacts 手动刷新入口（缓存行为正常，优先级低）。
- 浏览器点击验证：IAB 合成事件管线对 dashboard 按钮点击不生效（playwright
  超时/cua 无响应，截图 50% 失败），属已知 harness 限制；按钮语义已由
  a11y 树确认（role=button + 完整 accessible name），onClick 为两行直连。

## 防回归断言

- design-tokens.test.ts：非语义类名全量扫描（text-white/默认调色板）。

## 验证

pnpm tsc --noEmit / 330 Vitest 全绿 / lint 0 errors；a11y 快照确认
dashboard 行已为 button；text-white grep 清零；非 diff 11px 清零。
