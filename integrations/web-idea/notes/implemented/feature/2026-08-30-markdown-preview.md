# Markdown Preview（只读）

Status: implemented

## 决策与理由

`.md` / `.markdown` / `.mdx` 打开时默认 **Preview**，可用 **Source** 看原文。渲染用 `marked`（GFM）+ `DOMPurify` 消毒；预览态仍挂着 Monaco（`hidden`），避免来回切换重挂载。

链接：外链新标签；`#heading` 文内滚动；相对路径在工作区内打开（`#L12` 进 Source 定位；跨文件 heading 打开后滚动）。**目录链接**（如 `./ServletAjax/`）：展开 Project 树，并优先打开目录内 README，不再对目录调 `fs/file`（避免 `not a file`）。

## 放弃了什么

- **分屏 Preview|Source**：MVP 只读浏览够用；双栏占宽。
- **自研迷你 Markdown 解析**：GFM 表格/代码块成本高，不如依赖小库。
- **相对图片经 Gateway 代理**：暂不处理 workspace 相对资源 URL，外链仍可显示。
- **完整浏览器导航历史给 md 锚点**：沿用现有文件级 Back/Forward，不做 hash router。
