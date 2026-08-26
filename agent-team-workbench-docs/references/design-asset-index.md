# 设计素材索引（素材级明细）

> 与 `design-resource-library.md`（选站库单）的分工：那份答「去哪个站搜」，本份答
> 「具体素材的地址 / 描述 / 适用场景」。两个付费站（aicss/curated）的素材清单
> 2026-08-27 全量核验。维护约定同库单：失效直接删，新素材进表前先核验。

## 一、AICSS 组件索引（14/14 全量）

来源库：https://www.aicss.dev ｜ 组件页 `https://www.aicss.dev/components/{slug}` ｜
程序化取码：`https://www.aicss.dev/r/{slug}?format=md` ｜ 全目录 JSON：`/r`
授权总则：free=可个人+商用无需 license；locked=$190（个人）/$490（团队）一次性解锁后同权。

### 免费（10）

| 组件 | slug | 描述 | workbench 适用场景 |
|---|---|---|---|
| Thinking State | `thinking-state` | 回答前的微光处理中标签 | 正文 thinking-placeholder 的对照件 |
| Thinking + Reasoning | `thinking-reasoning` | 可折叠思考块：微光展开推理，落定折叠为「Thought for Ns」 | `ReasoningProcessPanel` 的折叠交互与落定摘要对照 |
| Orbs | `orbs` | 紧凑活动指示器（DOM+CSS 离散圆点） | 运行中状态的迷你指示（会话头/头像旁），不阻塞消息流 |
| Text Response | `text-response` | 助手正文排版 + 行内代码样式 | `chat-prose` 排版对照 |
| Streaming Text | `streaming-text` | 打字机流式文本 + 闪烁光标 | `chat-stream-caret` 的流式节奏对照 |
| Code Block | `code-block` | 带语言标签 + 一键复制的代码块 | `chat-code-panel` 工具栏交互对照 |
| To-do List | `task-list` | Cursor 式 todo 清单：可折叠头 + 完成/进行中/待办三态 | `chat-bottom-dock` 的 todoPlan 展示与 `PlanCard` 精修 |
| Data Table | `data-table` | 结构化对比/结果数据表 | 用量表、评估结果表、模型对比 |
| AI Agent Input | `ai-agent-input` | 输入框：附件 + 模型切换菜单 + enhance prompt 四态 | chat 输入面升级参考（模型切换菜单对多模型配置有用） |
| Approval Card | `approval-card` | 三变体审批卡（澄清问题/命令/计划）+ 自动批准倒计时 | tx 审批卡重设计的工艺基准（原作者 @kvnkld）；plan/questions 变体交互待后端契约扩展后对照 |

### 付费（4，按需购买，勿提前解锁）

| 组件 | slug | 描述 | workbench 适用场景 | 判断 |
|---|---|---|---|---|
| File Diff | `file-diff` | 行内 diff 卡（增删行统计） | diff 卡对照 | **不建议买**：已有 `DiffCard`，模式简单可自实现 |
| Image Generation | `image-generation` | 图像生成中的 shimmer 画布占位 | 图像生成场景 | 暂无此场景 |
| Inline Citations | `inline-citations` | 上标引用标记 + 紧凑来源脚注 | 知识层检索引用面（未来） | 到知识层再做决策 |
| Comparison Table | `comparison-table` | 功能×方案对比矩阵（含勾叉） | 模型/方案对比页 | 可自实现，不买 |

## 二、Curated 检索面索引

来源库：https://curated.design ｜ 形态：真实可访问网站画廊（卡片带原站链接），非代码资源。

### 免费画廊分类（/inspiration/{slug}/，2026-08-27 实抓 33 个）

应用与产品界面（workbench 设计主用）：

| 分类 | 地址 | 适用场景 |
|---|---|---|
| web-apps | `/inspiration/web-apps/` | **主力入口**：浏览器应用界面整页布局 |
| desktop-apps | `/inspiration/desktop-apps/` | 桌面应用壳/多面板布局 |
| productivity | `/inspiration/productivity/` | 效率工具的信息密度与工作台布局 |
| saas | `/inspiration/saas/` | SaaS 产品面 + 营销面 |
| ai / ai-tool / artificial-intelligence | `/inspiration/ai/` 等 | AI 产品交互语言（agent 界面同类） |
| development-tools | `/inspiration/development-tools/` | 开发者工具审美（终端/日志/密度） |
| app / mobile-apps | `/inspiration/app/` | 移动端参考（次要） |

风格专项（按视觉方向查）：

| 分类 | 地址 | 适用场景 |
|---|---|---|
| dark | `/inspiration/dark/` | 暗色界面专项（tx 正文皮肤的同类参考池） |
| minimal | `/inspiration/minimal/` | 克制留白方向 |
| animated | `/inspiration/animated/` | 动效突出的站（配合 ohwow 看录屏） |
| colorful / gradients / pastel | 同构 | 色彩方向探索 |
| neobrutalism | `/inspiration/neobrutalism/` | 粗野主义专项（一般用不上，备查） |

行业/其余（agency、branding、catalog、design、design-tools、designer、development、ecommerce、finance、marketing、portfolio、shops、tech、web3、assets）：营销面与品牌站参考，workbench 产品设计少用，需要时按 slug 直达。

### Sections 区块库（付费）

- 入口：https://curated.design/sections/ ｜ 共 **2,229 个区块**（hero/pricing/footer/FAQ 等）
- 授权：Pro $9/月解锁完整库；未购买时可按 Section type 过滤浏览缩略图
- 适用场景：营销站/落地页的区块级布局参考；产品内 UI 用处有限

### 模板商店（付费单件）

- 入口：https://curated.design/store/ ｜ Framer 模板 $19–79
- 适用场景：营销站整体骨架参考；注意是 **Framer** 产物，不直接产出 React 代码

### 订阅

- RSS：https://curated.design/rss.xml（每周精选新收录）

## 三、ohwow（例外说明）

流式画廊，素材=推文截图/录屏，**无稳定素材地址**（内容随 feed 翻页变化），故不设素材级索引。
检索方式：`GET https://ohwow.design/api/saves?cursor=` 分页拉取，每条带原推回跳。
2026-08-26 已全量过筛 106 条：除 @kvnkld 审批卡（=AICSS approval-card 原型）外与 workbench 无关。
用途定位：动效氛围随手翻的 moodboard，不当素材库用。

## 维护约定

1. 新素材进表 = 地址 + 描述 + 适用场景三列齐全，缺一不收。
2. AICSS 新组件发布（其 /r 会变）→ 重抓一次全量对账；Curated 分类增删 → 按需补。
3. 付费素材一律标「判断」列，避免重复评估。
