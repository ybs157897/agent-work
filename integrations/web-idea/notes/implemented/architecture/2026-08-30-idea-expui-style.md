# P1 UI: IntelliJ ExpUI Light 样式与 Find in Files

Date: 2026-08-30

Status: implemented

## Why

用户反馈编辑器样式未对齐 IntelliJ（字段色、配置文件 key、全局搜索）。对照
[intellij-community](https://github.com/JetBrains/intellij-community)
`expUI_lightScheme.xml` / `expUI_light.theme.json` 抄配色，而不是自造主题。

## What shipped

- Monaco `idea-light`：关键词/字符串/数字/注释/注解/YAML·JSON·Properties key 色值取自 ExpUI Light。
- Java 启发式装饰（无 jdtls）：字段紫、注解橄榄、方法声明青绿；真语义着色留给 P2。
- `GET /api/v1/workspaces/{id}/fs/search` + Find in Files（⌘⇧F）。
- Chrome CSS 变量对齐 ExpUI Gray/Blue 色板。

## Rejected / deferred

- 不嵌整站 IDE / 不拷贝 JetBrains 专有资源文件进仓库（只移植公开色值）。
- 不做 TextMate 全套语法引擎；Properties 用轻量 Monarch。
- 全局搜索为子串扫描，非 IDE 索引；大仓有 truncated。
