# 产品愿景与范围

Status: accepted

Date: 2026-08-30

## 一句话

让人在浏览器里 **像用 IDE 一样读/改 Java 工程**：按目录找文件，点符号跳转，编辑保存，并由 jdtls 提供成员补全——服务「AI 写完代码后，人要看懂并轻改」的场景。

## 问题

Agent / LLM 批量改代码后，人仍需：

1. 在工程树里定位文件；
2. 读方法实现与调用关系；
3. 用熟悉的 IDEA 导航与代码排版检查 AI 产物；
4. 控制浏览器 buffer 与 Java 索引生命周期，减少为阅读而常驻完整 IDE 的需求。实际内存收益通过后续同工程基准验证。

现有 agent workbench 只有 chat 内高亮与 run-diff，**没有** 工程树与语义跳转。桌面 IDEA / Lithe 无法嵌进 Web 控制面。

## 目标用户

- 使用 AI 编码工具的 Java / Spring 开发者；
- 需要在 Web 控制面内审查仓库，而不切换到本机 IDEA 的团队。

## 成功标准（MVP）

1. 指定 Workspace Root 后，可浏览目录并打开任意文本文件（默认可编辑）；
2. 在已成功索引的 Maven/Gradle 工程内，对项目源码符号执行 **Go to Definition**、**Find References**，以及触发字符 `.` 的 **Completion**；
3. 经 Gateway jail **保存** UTF-8 文本（Cmd/Ctrl+S）；
4. 路径逃逸、未授权工作区访问被拒绝；
5. jdtls 可按需启停，失败时 UI 有明确降级（可读写文件、不可语义/补全）。

开发 Agent 嵌入模式将同一阅读能力固定为只读，由宿主会话绑定工作区并负责生命周期；
独立部署仍保留轻量文本编辑能力。

## 非目标（明确不做）

| 不做 | 理由 |
|------|------|
| 重构写盘、批量格式化、多文件原子提交 | 信任面与冲突面仍受控；见 ADR-007 |
| 断点调试、Run Configuration UI | 桌面 IDE 主场；成本高 |
| 多语言语义（Go/TS/Python LS） | 聚焦 Java；避免 N 套 sidecar |
| 嵌入 Theia / code-server 整站 | 双产品壳、鉴权与 UX 割裂 |
| 复制完整 IntelliJ 产品与逐像素皮肤 | 保留 IDEA 的导航习惯、布局和快捷键；优先真实阅读路径 |
| 云端多人实时协作编辑 | 超出 MVP |

## 与相关产品的关系

| 产品 | 关系 |
|------|------|
| IntelliJ IDEA | 能力参照；不依赖、不嵌入 |
| Lithe-IDEA | 桌面轻量替代品；本项目是 **Web** 路线，可并存 |
| agent-team-workbench | **可选宿主**：未来经 iframe / 模块联邦 / 反向代理嵌入；本仓库独立演进 |
| VS Code Java / jdtls | **复用语言服务**，不复用 VS Code 壳 |

## 版本切片

见 [roadmap.md](../architecture/roadmap.md)。
