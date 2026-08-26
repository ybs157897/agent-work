# 水墨化 Aceternity 工作台视觉系统

Status: implemented

## 决策与理由

将 Web 工作台从冷蓝控制台整体改为古代水墨语言：宣纸表面、墨色层级、朱砂单强调色、楷体标题与克制的山水留白。路由、导航标签、业务字段、数据密度与交互行为保持不变，视觉层允许重构。

组件分三层：Aceternity UI 提供 Sidebar、Bento Grid 与文字/状态动效原语；Ink Design System 负责语义 token、材质、形状与 motion token；页面只消费水墨化后的工作台组件。AE/Lottie 只预留在低频氛围与关键状态反馈，不承担按钮、表单、Drawer 等实时交互。

正文动效采用双强度：已完成的 Assistant/Plan 正文按 Markdown 标签做段级显影、错峰列表、标题 Text Generate 与水墨 Tracing Beam；流式正文维持克制，只保留 caret、thinking 与工具 sweep，避免每次 token 追加重播动画或改变自动滚底语义。

## 放弃了什么

- 放弃原有冷灰白画布、深蓝侧栏与亮蓝强调色，它们与用户指定的水墨方向冲突。
- 不直接搬用 Aceternity 默认的霓虹、科技光束、强 3D 与深色主题，避免“水墨背景 + SaaS 霓虹组件”的割裂。
- 不让每张卡片播放 AE 动画；绝大多数界面保持静止，动效只表达层级、反馈与状态切换。
- 不把 Aceternity Tracing Beam 直接挂进 live activity/tool 组；工具组 key 与高度会在追加帧时变化，整组重播会破坏阅读和滚动跟随。Beam 只用于完成态稳定内容。

## 复活条件

若真实浏览器验收显示水墨材质降低文本对比度、数据扫描效率或运行性能，则保留 Aceternity/Ink 分层，回退对应纹理或氛围动画；不得回退业务行为、键盘可达性与语义 token 门禁。
