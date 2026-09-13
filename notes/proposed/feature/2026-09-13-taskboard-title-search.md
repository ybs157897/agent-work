# 任务看板按标题搜索

Status: proposed

## 背景

看板页面 `agent-team-workbench/web/src/pages/tasks.page.tsx` 已有搜索能力，需求方提出的「增加按标题搜索」实际是**收窄匹配面 + 对齐文案**，不是新建功能。收窄与改文案会改变已有行为与已有断言，故留痕。

## 现状核对（改动前事实）

| 诉求 | 现状 | 判定 |
| --- | --- | --- |
| 按标题搜索 | 搜索框已存在 `tasks.page.tsx:215-235`；`matchesTaskQuery`（`:110-115`）同时匹配 `id`、`workItemKey`、`title`、`description` 四项 | 部分满足，需收窄 |
| 只针对当前工作空间 | 请求路径含 workspaceId（`web/src/api/endpoints.ts:257-267`）、`X-Workspace-ID` 头（`web/src/api/client.ts:71-72`）、store 二次防线（`web/src/stores/tasks.store.ts:48,90,109`）、generation fencing（`web/src/stores/scope.ts:44-46`）、切库 reset（`tasks.store.ts:120`） | 已满足 |
| 模糊匹配 + 英文不区分大小写 | `:111` trim + `toLocaleLowerCase`，`:114` `includes` | 已满足 |
| 清空关键词恢复完整列表 | 输入框内 X 只清关键词（`:224-234`）；空关键词返回 `true`（`:112`） | 已满足（带前提） |
| 无结果文案 | `EmptyTaskSearchState`（`:424-440`）标题为「没有匹配的任务」（`:428`）；`hasSearchNoResults`（`:170`）与 `hasNoTasks`（`:171`）已正确区分 | 需改文案 |
| 不加标签/负责人筛选 | 页面仅有优先级筛选（`:236-246`）；`filter.assignee` 有过滤逻辑无 UI 入口（`:155-158`） | 保持不动 |

## 需求规格

- 匹配面：**只匹配 `work_items.title`**。移除 `description` 与裸 `id`／WI 短编号匹配。
- 匹配语义：子串包含（`String.prototype.includes`），非子序列、非编辑距离、不按空白切词、不引入新依赖。
- 归一：两侧统一 `toLowerCase()`（替换现有 `toLocaleLowerCase()`，规避土耳其语 `I→ı` 的 locale 行为）；`trim()` 后为空即视为未搜索，返回全量。
- 作用域：当前 workspace。无需任何数据面改动。
- 数据面：**纯前端内存过滤，不动后端**。理由是 store 已用游标循环拉完全部分页（`tasks.store.ts:71-86`），后端默认 limit 50（`internal/persistence/sqlstore/workitems.go:118-121`），规模未到需要下沉的量级。
- 关键词生命周期：跟随 workspace 切换清空，与 store 的 `reset`（`tasks.store.ts:120`）对齐。当前 `query` 是页面本地 state（`tasks.page.tsx:133`），切库会残留。
- 清空口径：输入框内 X 保持「只清关键词」；回到完整列表由工具条「清空筛选」（`:247-256`）负责。「清空关键词即恢复完整列表」在存在优先级筛选时**不成立**，验收条件据此写明前提。
- 子任务边界：过滤基座为 `rootItems`（`:150`，只含根任务），子任务不参与搜索。该边界需在空态副标题显式告知，不静默不命中。
- 文案：空态标题替换为「没有找到相关任务」；placeholder（`:220`）、`aria-label`（`:221`）、空态副标题（`:429`）三处因不再匹配描述与 WI 编号而必须同步修改，否则文案失实。

## 验收条件

验证手段受测试设施约束：vitest `environment: 'node'`，无 jsdom、无 @testing-library，因此交互行为不能靠真实 DOM 事件验证，必须由**可直测的纯导出函数**承担语义断言，渲染结论用 `renderToStaticMarkup` 字符串断言（需先导出空态组件或整页 `MemoryRouter` 渲染）。

| 编号 | 验收条件（Given/When/Then） | 验证方式 |
| --- | --- | --- |
| A01 | 标题「发布登录模块」，输入「登录」→ 命中 | 纯函数直测 |
| A02 | 标题「修复支付回调超时」，输入「支付回调」→ 命中 | 纯函数直测 |
| A03 | 标题「login timeout」，输入「LOGIN」→ 命中 | 纯函数直测 |
| A04 | 标题「LOGIN TIMEOUT」，输入「login timeout」→ 命中 | 纯函数直测 |
| A05 | 输入「 login 」结果等同「login」（首尾 trim） | 纯函数直测 |
| A06 | 输入全空白等同空关键词：列表不变，`hasActiveFilter` 仍为 false | 纯函数直测 |
| A07 | 无任何标题命中 → 出现精确文案「没有找到相关任务」 | 渲染断言 |
| A08 | 同 A07 时不得出现「还没有总任务」，旧文案「没有匹配的任务」已从源码消失 | 渲染断言 + 文案 grep |
| A09 | 清空关键词后列表恢复为全部根任务，顺序同 `sortTasksTree` | 纯函数 + 渲染断言 |
| A10 | 关键词与优先级筛选并存时两者同时生效 | 纯函数组合用例 |
| A11 | 切到另一 workspace 后不出现原 workspace 的任务 | store 直测 + fetch stub |
| A12 | 搜索不产生新网络请求，请求 URL 无 `q`/`title` 参数 | fetch stub 断言 |
| A13 | 仅 `description` 含关键词 → 不命中 | 纯函数直测 |
| A14 | 仅裸 `id` 含关键词 → 不命中 | 纯函数直测 |
| A15 | 根任务标题不命中时，其子任务不出现在看板 | 纯函数 `rootTasks` + 过滤 |
| A16 | 计数徽标数值等于可见任务数 | 渲染断言 |
| A17 | 关键词非空时切换到另一 workspace → 关键词被清空，列表为该 workspace 的全量根任务 | 需在 workspace 重置链路（`web/src/stores/scope.ts` 触发 `tasks.store.ts:120` 的 reset）提供可直测入口；否则无 jsdom 可测，只能人工核对 |
| A18 | 搜索框 placeholder 与 `aria-label` 不再承诺匹配描述或 WI 编号，空态副标题不再承诺 | 渲染断言 + 源码文案 grep |

## 决策与理由

**收窄到纯标题**。需求方明确「本次只做标题搜索」。保留 `description` 匹配会让用户搜索到看不到该词的卡片，保留裸 `id` 匹配更是命中不可见的 UUID——两者都产生不可解释的结果。WI 短编号是展示态派生值，同属标题之外的字段，一并移除。

**不下沉服务端**。加 `q` 参数要同时动 `WorkItemFilter`（`internal/application/repositories.go:442-452`）、handler（`internal/httpapi/handlers.go:211-219`）、SQL（`workitems.go:122-168`）与 openapi 契约（`contracts/web/openapi.yaml:574-588`），而 `work_items.title` 无任何索引、keyset 游标又要求「改变筛选必须重新分页」（`repositories.go:442-443`）。在内存已持有全量的前提下，收益为负。

**关键词跟随 workspace 清空**。不这么做时，切库后旧关键词会作用到新库，新库大概率直接显示「没有找到相关任务」，用户无法判断是空库还是搜不到。

**子任务不参与搜索**。让子任务可搜需要改过滤基座与列投影，会让看板列里出现无匹配父行的孤儿行，破坏既有看板语义；代价是一个需要在文案里说明的边界。

**关键词不进 URL**。进 `?q=` 能带来刷新/分享/回退保留，但那是新增能力，不在本次六条诉求内。

## 放弃了什么

- **子序列 / 编辑距离 / 分词模糊匹配**。需求方说的「模糊匹配」在仓库语境下是子串包含，子序列会把「登页录」判为命中「登录」，对中文标题只增噪声；仓库也没有 fuse 类依赖。
- **多词按空白切词做 AND**。`search.go:82-86,243-251` 有此先例，但需求方未要求，属自造语义。
- **在 `matchesTaskQuery` 里保留 WI 短编号**。虽然 `tasks.page.test.ts:194` 已断言该行为，但「只做标题搜索」与之直接冲突，选择改语义并重写断言，而不是为保测试保留字段。
- **把无结果空态换成共享 `EmptyState`**（`web/src/components/ui/empty-state.tsx:14`）。任务页两个空态使用专用布局类 `plane-board-search-empty`（`web/src/index.css:581-593`），替换属样式重构，超出本次范围。
- **挂载 `pages/tasks/search-panel.tsx` 与 `review-queue.tsx`**。两者均未被任何页面挂载（仅被自身 render 测试引用），前者连后端全文检索与命中高亮、后者自带状态筛选下拉，一旦纳入会把「看板标题过滤」升级成跨库检索面板。
- **结果计数之外的额外 a11y 改造**：`role="status"`（`:426`）已具备播报能力，不额外加 `aria-live`。
- **不做**：标签/负责人/状态筛选、排序控件、搜索历史、命中词高亮、i18n、防抖与虚拟滚动、SQL 迁移与 `title` 索引。

## 复活条件

- 【单 workspace 任务总量超过约 3000–5000 条】→ 下沉服务端标题过滤：`WorkItemFilter` 加 `Query`、handler 读 `q`、SQL 复用 `LOWER(...) LIKE ? ESCAPE '!'` 与 `likePattern`（`internal/persistence/sqlstore/search.go:83-85,146-149`）、同步 openapi；预埋要求：加 `q` 后前端必须丢弃旧 cursor 重拉首页，并考虑 `title` 表达式索引。该阈值是工程判断，仓库内未发现单 workspace 任务数硬上限常量。
- 【需要跨 workspace 或跨描述/编号检索】→ 改为挂载既有 `search-panel.tsx` 通道，而不是继续放宽看板标题匹配。
- 【产品要求子任务可被搜到】→ 需重新设计过滤基座与看板列投影（`tasks.page.tsx:150,162-165`），并使列表视图的树展开语义与之自洽。
- 【需要分享/刷新保留搜索状态】→ 关键词进 URL `?q=`，与 `?view=`（`:141-148`）同层处理。

## 影响面

- 改动集中在 `agent-team-workbench/web/src/pages/tasks.page.tsx`：`matchesTaskQuery`（`:110-115`）、`EmptyTaskSearchState`（`:424-440`）、placeholder/aria-label（`:220-221`）、关键词生命周期。
- `matchesTaskQuery` 全仓仅 `:159` 一处调用，且看板与列表两视图共用 `visibleItems`（`:153-160`），单点改动两视图同时生效。
- 必须同刀更新 `web/src/pages/tasks.page.test.ts:190-197`——其中 `'abc123'` 用例当前靠 id 尾部数字恰好出现在 title 中而巧合通过，`description` fixture 为空（`:18`）故收窄描述无测试保护。
- 关键词从页面本地 state 迁到随 workspace 重置的位置，是本次唯一触及状态生命周期的改动；A17 也是唯一在现有测试设施下无法自动验证的验收条件（无 jsdom），实现时须先给它留可直测入口，否则只能人工核对。
- 本次改文案不触碰 `src/design-tokens.test.ts` 门禁（它只拦内联色值与 Tailwind 默认调色板类名）。
- 门禁：`cd agent-team-workbench/web && pnpm tsc -b && pnpm test && pnpm lint`。
