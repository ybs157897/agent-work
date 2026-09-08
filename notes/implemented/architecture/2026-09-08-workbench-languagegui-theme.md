# Adopt one LanguageGUI theme for the workbench

Status: implemented

## 决策与理由

工作台现在使用生产 Agent 正文已经验证的 LanguageGUI light/dark palette。
`useWorkbenchThemeStore` 是唯一的外观偏好入口，复用 `chat:theme`，由 shell
把当前模式写到 workbench root 和 document element。这样普通页面、任务/配置页、
Chat 和 portal 对话框解析同一组语义 token，存储不可用时仍能在内存中切换。

主侧栏改成固定展开的原生 shell aside，菜单始终同时渲染图标和文字。旧
Aceternity 折叠/悬浮展开/移动全屏导航状态被删除；窄屏只压缩宽度，并把 Chat
内部资源栏改成顶部独立滚动区，保证正文仍有可用空间。

## 放弃了什么

保留旧全局 palette、在 Chat 外继续套一层局部皮肤、或给新 palette 加覆盖式
兼容层都会留下两套视觉事实，也会使 portal 和任务/配置页面继续漂移。水墨
装饰组件、纹理和专用 Bento 包没有对外兼容承诺，因此连同唯一消费者直接删除；
页面改用 Aceternity Bento 与 `workbench-panel` 的语义入口。

## 复活条件

只有在产品明确批准新的可审计视觉语言，并同时提供完整 light/dark token、portal
继承策略和迁移验收清单时，才重新引入新的局部皮肤；不得恢复第二套全局 token。
