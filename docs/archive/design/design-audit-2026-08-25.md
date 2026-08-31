# 设计审计问题单（redesign-skill 审计流）

> 状态：**时点快照**（2026-08-25 审计定格）。下方问题项已并入 `frontend-design-md-redesign.md`
> 的 M1/M2 路线消化；截至 2026-08-26 至少 #1/#2 骨架屏与 #8 弹窗 Drawer 化已落地，
> 其余以路线账本与 git log 为准，本文不再更新。

日期：2026-08-25
方法：`~/.zcode/skills/redesign-skill`（Scan→Diagnose，只修不重建）+ 代码扫描 +
生产构建（:8080）四页实测截图（总览/对话/看板/智能体配置）。
**上下文过滤**：redesign-skill 清单偏营销页（hero/testimonials/pricing），
本产品是数据工作台，只审计适用类目；被过滤项在文末「不适用项」说明。

---

## 0. 通过项（保持）

- 单强调色纪律；无纯黑（侧栏 off-black `222 28% 11%`）；冷灰族一致，无冷暖灰混用；
- 无 `window.alert`；toast/文案无感叹号、无 "Oops"、无 AI 腔（Elevate/Seamless 类零命中）；
- 交互件 hover/active/focus-visible/disabled 四态完备；全局 focus ring；
- 导航 active 高亮 + 面包屑；`layout-safe` 1440 限宽容器；
- 动画走 transform/opacity，主要动效有 reduced-motion 覆盖；
- 真实数据（无 lorem/Acme/John Doe）；数字普遍 `tabular-nums`；
- 语义标签在 layout-shell（aside/nav）与 settings/dashboard 已用。

## 1. 问题单

### P1（体验缺口，进 M1/M2 迁移刀）

| # | 问题 | 证据 | 建议修法 | 归属 |
|---|---|---|---|---|
| 1 | 加载态全是通用 spinner | `async-state.tsx:7` Loader2 animate-spin | 换匹配布局形状的骨架屏，>300ms 才显示（DESIGN.md M2 已列，提升优先级） | M1 |
| 2 | 任务看板零加载态 | grep `Loading` tasks.page=0 | 看板列骨架（四列卡片形状） | M1 tasks 刀 |
| 3 | 原生 `<select>` 破坏手感 | agents 页 6 处、models/settings 同 | ui/ 出 Select 组件：`appearance-none` + 自定义 chevron + input-field 同系边框/焦点环 | M1 |
| 4 | div soup：5 页无语义分节 | tasks/agents/models/logs/chat 的 `<section>/<main>` 计数=0 | 逐页补 `main/section/nav` 语义标签 | M1 随页 |
| 5 | 看板空列无设计 | 「阻塞 0」列仅裸头 | 空列加 composed 空态（虚线占位 + 一句说明） | M1 tasks 刀 |
| 6 | 表单校验态缺失 | Known Gap；models/agents/settings 无 inline error | Field error 槽位已有，补校验逻辑与错误文案规范（主动语态、无感叹号） | M2 |
| 7 | 404 静默重定向 | `App.tsx:73` `path="*"` → Navigate "/" | 品牌化 not-found 页（给返回路径），不再静默吞 | M2 |

### P2（偏好级，留痕不强制改）

| # | 问题 | 说明 |
|---|---|---|
| 8 | 弹窗偏多 | create-task/block/return/models 多处 modal；skill 建议简单动作改 slide-over/inline。drawer 组件已存在，M2 评估逐件替换 |
| 9 | ui-card = border+shadow+白 的通用卡相 | skill 偏好"卡只在层级需要时存在"。我们卡片承载层级，保留；个别平面（如看板列内）可试无边框分组 |
| 10 | 左栏侧边栏 | skill 视其为 dashboard 默认答案；IA 已冻结（confirmed-ia.json），不改 |
| 11 | Lucide 单一图标集 | skill 视为 AI 默认选择；一致性优先，不换 |
| 12 | 无 skip-to-content | 键盘用户直达主内容隐藏链接，a11y 补强，M2 |
| 13 | 光学对齐类细节 | 图标-文本基线、按钮光学居中等逐件走查项，随 M1 页面刀顺手修 |

## 2. 与 skill 修复优先级的映射

skill 的 Fix Priority 七步中，1（换字体）2（调色板）3（hover/active）4（布局限宽）
我们已在前序刀完成或冻结；**剩余命中集中在 5–7 步**：替换通用组件（select）、
补状态（骨架/看板加载/空列/校验）、排版细节。与 M1/M2 路线天然对齐，无需新路线。

## 3. 不适用项（已过滤，留痕）

hero/渐变mesh/视差/噪点/testimonial/定价表/博客日期/cookie 同意/法律页脚/
favicon 品牌化（工作台非公开站）/图标隐喻替换/100dvh（无移动端立项）/
"dashboard 别用左栏"（IA 冻结）。这些是营销页或移动语境条目，不进入产品工作台问题单。

## 4. 结论

工作台在"纪律面"（token/状态/文案/动效）已通过审计；**差距集中在"完成度面"**：
骨架屏、看板加载、select 组件化、语义标签、空列、校验、404——全部是
可逐页消化的小刀，没有结构性返工。并入 M1/M2 执行。
