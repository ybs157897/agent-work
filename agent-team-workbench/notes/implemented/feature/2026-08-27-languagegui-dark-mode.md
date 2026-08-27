# LanguageGUI Chat 暗色模式

Status: implemented

## 决策与理由

只在生产 Chat 的 `.chat-languagegui-skin[data-theme]` 边界内提供 light/dark 两套语义
token 映射。主题选择由页头按钮显式切换并本地记忆；不修改全局水墨主题，不引入散落的
Tailwind `dark:` 变体。正文、ContentBlock、Workflow、PromptBox、Sidebar 和状态色继续
消费同一组语义 token，因此新组件自动覆盖两种主题。

## 放弃了什么

不跟随系统主题自动切换，避免用户正在阅读时界面无提示变化；首次仍使用已验收的浅色
LanguageGUI 皮肤。不把暗色扩散到总览、任务、配置页面，也不复制一套组件级颜色。

## 复活条件

【全产品统一主题系统立项并定义跨页面持久化/系统跟随策略】
→ 把 Chat 局部主题迁入全局 ThemeProvider → 删除本地 Chat 存储键和 scoped token 覆盖。
