# Web 设计素材库

> 用途：agent-team-workbench 前端与营销面做设计时的**检索入口**——先查本库选对资源站，
> 再进站搜，不要凭记忆裸搜。2026-08-27 建档。
> 素材级明细（每个组件/分类的地址、描述、适用场景）在姊妹篇
> `design-asset-index.md`：本份管选站，那份管选素材。
> 维护约定见文末；条目失效直接删（删除优先，不留尸体链接）。

## 用法索引（什么场景查哪个）

| 场景 | 首选 | 备注 |
|---|---|---|
| agent 对话组件（审批卡/思考块/todo/流式/diff） | AICSS | 免费组件可直接取码作参考实现 |
| 动效/交互氛围 moodboard | ohwow | 只有截图录屏，看中后点回原推找作者 |
| 应用型整站/后台界面布局参考 | Curated | 用 `?category=web-apps` 过滤 |
| 营销面/落地页布局 | Curated + SaaS Landing Page | |
| 真实产品界面截图（按控件/流程找） | Mobbin（候选） | 未内容核验 |
| 暗色界面专项 | Dark.design（候选） | tx 正文暗色皮肤的同类参考池 |
| LLM 输出富化形态（表格/图表/评分 widget、workflow 屏） | LanguageGUI | 纯 Figma 设计 Kit，MIT 免费商用，无代码实现 |

## 核心条目（已核验）

### AICSS — AI agent 界面组件库
- 入口：https://www.aicss.dev ｜ 组件目录 JSON：`/r` ｜ 单组件取码：`/r/{slug}?format=md`
- 形态：14 个 agent 对话组件（思考态/工具调用/流式/todo/diff/审批卡…），React/Vue/Svelte 三版本，复制即用，自包含 CSS + 自定义属性 token
- 授权：10/14 免费可商用；付费组件 $190（个人）/$490（团队）一次性
- 核验：2026-08-26（内容+授权已核）；approval-card 原作者即 X 上 @kvnkld
- 适用：正文区组件的先手参考（本项目审批卡重设计已对照其工艺基准）

### ohwow.design — X 策展设计画廊
- 入口：https://ohwow.design ｜ 数据 API：`/api/saves`（分页 cursor）
- 形态：从 X（Twitter）策展的 UI 设计截图/录屏瀑布流，每条回跳原推文；**无代码、无下载**
- 授权：仅作灵感参考；看中的交互找原作者（部分原推附 Source code）
- 核验：2026-08-26（API 实测 106 条样本）
- 适用：动效/视觉氛围 moodboard；不是组件市场，别来这找可复制代码

### Curated — 真实网站灵感画廊
- 入口：https://curated.design ｜ 应用界面分类：`/?category=web-apps`
- 形态：人工精选的**真实可访问网站**画廊（"Websites you can actually visit"，卡片带原站链接）+ sections 区块库（hero/pricing/footer/FAQ，$9/mo Pro）+ Framer 模板（$19–79）
- 授权：画廊免费浏览；sections 库与模板付费
- 核验：2026-08-27（分类与付费形态已核）
- 适用：整站布局、页面级设计参考；web-apps 分类看应用界面，营销面看落地页

## 候选补充（2026-08-27 探活，内容未核验）

> 403 为站点反爬，探活视为存活。用前先人工过一眼内容质量与授权。

- https://mobbin.com — 真实 app/Web 界面截图库，按控件与流程检索（200）
- https://godly.website — 高端网站画廊（301 探活）
- https://www.dark.design — 暗色界面专项画廊（200）
- https://www.saaslandingpage.com — SaaS 落地页画廊（200）
- https://land-book.com — 落地页画廊（403 反爬探活）
- https://pageflows.com — 真实产品用户流程录像（403 反爬探活）

## 维护约定

1. 新增核心条目必须先核验（内容形态 + 授权/付费），并标注核验日期；候选条目至少探活。
2. 失效/变质的条目直接删除，不留「已失效」尸体段落。
3. 用途与授权说不清的资源不进核心条目。
4. 与设计决策相关时，条目引用写进 `notes/` 决策档（如审批卡重设计引用 AICSS）。
