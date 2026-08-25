# M2 交互契约四刀：遗留类清零 / 校验态 / 404 / spinner 清零

Status: implemented

M1 把六个页面迁上 ui/ 组件后，M2 收四类契约欠账：遗留 CSS 类双轨、
表单无内联校验、未知路由静默重定向、加载态仍用通用 spinner。
提交：26073f1 / adbe4ad / 56a519a / bfabb16 / 95f02c5。

## 决策与理由

1. **遗留类成建制删，不留垫片**：`.btn*` / `.ui-card*` / `.status-pill` /
   `.input-field` / `.ui-section-title` 引用归零后整块出 index.css；注释与
   测试标题里的历史引用同步改写（注释只描述现契约，不记迁移史）。
2. **校验态只做错误态，不做成功态**：输入框 `invalid` 属性承担 error 描边
   （status-error/60 + error 焦点环）+ `aria-invalid`；FieldError 顶替 hint。
   否决绿色对勾——没有错误即正常，成功态是装饰性状态色，违反 DESIGN.md
   色彩纪律。文案规则：主动语态、说清怎么改、无感叹号。
3. **chrome 抽共享件，配置系与 ui 系同源**：`fieldChromeNeutral/Invalid`
   从 ui/input 导出，config-workbench 的 `configInputCls/InvalidCls` 复用
   （配置输入更高 py-2.5，其余同规格）。否决两套独立校验样式。
4. **404 在壳内渲染，不静默重定向**：未知路由从 `Navigate to="/"` 改为
   not-found.page（h1 + 单一 primary 返回）。否决独立全屏 404——壳内渲染
   保留导航，用户不必"回到起点再找路"。
5. **skip-to-content 用 sr-only + 聚焦显现**：shell 首位可聚焦元素；main 挂
   id + tabindex=-1。否决始终可见的跳转条（视觉噪音）。
6. **启动壳骨架 delayMs=0 例外**：300ms 门控对页面加载态成立，但启动屏是
   全屏空白，先白 300ms 再出骨架更糟。DESIGN.md 骨架契约已注明例外。
7. **chat-approval-btn 族不在本批清零**：它有 danger/warning-outline 等
   审批语义档，超出 ui/Button 三档（primary/secondary/ghost）；强塞会污染
   通用按钮契约。单独立项，见 Known Gaps 思路。

## 负向保证

- 不再新增 `.btn-*` / `.ui-card*` 等 index.css 组件层遗留类；新控件一律
  ui/ 组件或 token 类（门禁 + 本 note 双保险）。
- 校验错误不再只走 toast：可客户端判定的规则（Base URL 协议前缀）内联
  报错并拦截保存；服务端错误仍走 toast（不重复表达）。
- `async-state.Loading` 已删，通用 spinner 无组件可用——新加载态只能选骨架。

## 后续（同日，蜂群两刀）

- **审批按钮族已立项并落地**（11c52c1）：ui/Button 增 success/danger/
  danger-outline/warning-outline 四档状态变体（色值逐字取自遗留类），审批卡
  六处换装，`.chat-approval-btn*` 成建制删。第 7 条「单独立项」已兑现；
  status-usage 约束写入 DESIGN.md（状态档只许审批/破坏性语境）。
- **Toast 契约补齐**（a8e3754）：role=region/alert/status + 150ms 进出场
  （reduced-motion 归零）+ store 防回归测试；仍不设 undo/action 槽。
- M3 候选：modal→slide-over 逐件评估（13 处已盘点：删模型/删供应商两处
  破坏确认按纪律留模态，其余创建/编辑表单是 drawer 候选）。
