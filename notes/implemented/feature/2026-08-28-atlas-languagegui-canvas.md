# Atlas LanguageGUI 画布块

## 决策

- 为 **Atlas**（架构/产品 agent）增加画布能力，但不引入外部白板依赖（tldraw / Excalidraw / MCP 桥）。
- 复用现有 Chat `languagegui/v1` 输出契约，新增结构化块 `type:"canvas"`，由前端 SVG + 语义 token 渲染。
- 画布能力通过 Atlas `prompt.md` 与 `agent.yaml` skills 启用；其他 agent 不强制使用。

## 取舍

- **选用 declarative canvas 块**而非嵌入式 tldraw：范围可控、无新 npm 重依赖、与现有 ContentBlock 管线一致。
- **只读渲染 MVP**：Chat 内展示节点/边与清单；交互编辑留待 artifact 内容 API 成熟后再做。
- **不挂 MCP/OpenBoard**：与「无 MCP 终局」方向一致；agent 通过 fenced JSON 输出，不经外部画布服务。

## 负向保证

- 不承诺与 JSON Canvas / tldraw / Excalidraw 文件格式兼容。
- 不在本刀实现 artifact 持久化或双向编辑。
