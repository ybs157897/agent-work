# 产品 Agent 文档画布：需求与选型调研

日期：2026-09-08

状态：本报告记录选型阶段；用户已确认交互，最终行为见[产品说明](product-agent-canvas.md)。

代码基线：`e34dcfcad353caac4296a95957c0c11f5dcc2ee5`（已包含知识阅读与统一工作台主题）。

## 结论

建议先把现有知识阅读器嵌入产品 Agent 工作区，形成“中间文档、右侧对话”；如果要把多个产品文档放在可缩放的平面上，并展示需求之间的关系，优先评估 **React Flow**。`diagram-design` 可补充高质量产品流程图的生成能力。

这三个部分职责不同：阅读器负责完整 PRD，React Flow 负责文档节点与关系的空间浏览，`diagram-design` 负责把图表内容画出来。知识正文、版本、来源和生效状态继续属于已有知识库。

若用户明确需要手绘、自由图形、便签与连线组成的白板，再优先评估 **Excalidraw**；若需要高度可定制的混合对象无限画布，可评估 **tldraw**，但其当前默认许可证不授予生产使用权限，须有适用的额外授权。不能把可在开发环境运行理解成可以直接发布。

## 给定项目的定位

[`cathrynlavery/diagram-design`](https://github.com/cathrynlavery/diagram-design) 是面向 Agent 的图表设计技能和模板，生成自包含 HTML/SVG；[README](https://github.com/cathrynlavery/diagram-design/blob/main/README.md)说明静态变体无需构建、JavaScript 或外部图片，亦支持可选的动态说明。

它适合生成用户流程、需求依赖、状态图等产品文档插图。它没有承担可嵌入画布 SDK 的文档管理、拖拽编辑、知识权限和版本职责，不能通过安装这个 skill 就获得本次需要的完整页面。图表生成结果需要关联到真实知识文档及其版本；展示 HTML/SVG 时仍需沿应用的安全边界处理。

## GitHub 方案对比

以下结论来自 2026-09-08 的 GitHub API、README、LICENSE 与官方文档核验。适配判断是针对本项目的工程建议；没有用 star 数或主观综合分替代判断，也没有把 npm 解包大小当成实际加载性能。

| 候选 | 最适合的内容与交互 | 对我们意味着什么 | 许可与维护证据 | 建议 |
|---|---|---|---|---|
| [React Flow / xyflow](https://github.com/xyflow/xyflow) | 可缩放的节点、边、自定义文档卡片；受控状态与保存/恢复 | 节点引用知识条目，点击进入已有阅读器；正文、版本和权限仍由知识服务负责 | [核心 MIT](https://github.com/xyflow/xyflow/blob/main/LICENSE)；调研观察到 React 包 `12.11.6`，2026-09-01 发布 | **文档全景首选**；它不负责富文档编辑或知识权限 |
| [Excalidraw](https://github.com/excalidraw/excalidraw) | 手绘图形、便签、自由连线、示意草图 | 可作为文档中的可编辑草图；场景 JSON 与知识正文各司其职 | [MIT](https://github.com/excalidraw/excalidraw/blob/master/LICENSE)；调研观察到 `v0.18.1`，2026-04-21 发布，9 月仍有提交 | **自由手绘优先评估**；不以几何元素承载长篇 PRD |
| [tldraw](https://github.com/tldraw/tldraw) | 无限画布、自定义对象、工具及画布交互 | 可以实现混合对象产品工作台，但文档形状、知识存取和状态同步仍需自建 | [当前默认许可证](https://github.com/tldraw/tldraw/blob/main/LICENSE.md)仅许可开发环境，生产须适用的额外授权；调研观察到 `v5.4.0`，2026-09-02 发布 | 产品明确需要高度自由的混合画布时再做接入验证 |
| [BlockSuite](https://github.com/toeverything/blocksuite) / [AFFiNE](https://github.com/toeverything/AFFiNE) | 文档块与 Edgeless 画布一体，协作编辑模型 | 体验最接近文档与白板融合，但要接入另一套块/协作状态模型，工作量远超只读画布 | 独立仓库 [main 提交](https://github.com/toeverything/blocksuite/commit/5cb5cb68471ca692f3c162258f0087cb22fcb82d)仍为 2025-07-07；AFFiNE 仍在演进，不能据此认定整个生态停更。根许可 MPL-2.0 与部分包 MIT 声明有差异，需核对选定分发内容 | **一体化编辑备研**，初版不整体引入 |
| [Tiptap](https://github.com/ueberdosis/tiptap) | React 中可定制的富文本和块编辑 | 若以后允许直接编辑正文，再验证与知识 Markdown 的转换和保真；不提供无限画布 | [核心 MIT](https://github.com/ueberdosis/tiptap/blob/main/LICENSE.md)，商业扩展另计；调研观察到 `v3.31.3`，2026-09-04 发布 | 后续富文本编辑候选；本轮不迁移正文模型 |
| [Milkdown](https://github.com/Milkdown/milkdown) | Markdown 优先的可视化编辑 | 若必须保留 Markdown 工作流，值得与 Tiptap 用真实文档对比；不提供无限画布 | [MIT](https://github.com/Milkdown/milkdown/blob/main/LICENSE)；调研观察到 `v7.22.1`，2026-08-12 发布 | 后续 Markdown 编辑候选；仍需验证表格、图表和引用往返 |
| [diagram-design](https://github.com/cathrynlavery/diagram-design) | Agent 生成 HTML/SVG 图表 | 生成产品流程图、需求关系说明，关联为文档内容或附件 | [MIT](https://github.com/cathrynlavery/diagram-design/blob/main/LICENSE)；2026-09-07 仍有提交 | 图表生成补充能力，不承担画布底座 |

### 为什么优先 React Flow，而不直接嵌入整套 AFFiNE

本项目已经具备聊天、知识正文、来源、版本与发布流程。当前增量主要是将这些能力放入产品 Agent 工作区，并提供空间浏览。React Flow 的自定义节点适合绑定已有知识 ID；其[保存与恢复示例](https://reactflow.dev/examples/interaction/save-and-restore)说明了节点和视口状态的序列化能力，但具体的知识权限、事务和持久化仍由本项目实现。

BlockSuite 的文档/画布融合值得参考，特别是用户确实需要同一份文档块在纸面与无限平面间互转时。当前独立仓库与 AFFiNE 内部演进的发布边界、内容模型和许可边界都需要额外验证。不能仅因为演示完整，就认为搬进现有系统比复用现有阅读器更省事。

### 自由白板与文档全景的区别

React Flow 更适合“需求 A 依赖需求 B，点击卡片阅读全文”。Excalidraw 更适合“围绕一个想法随手画图和写便签”。tldraw 更适合需要自定义对象和自由布局的完整画布应用。三者可以组合，但初版同时引入会重复承担对象模型、工具栏、保存和快捷键体系；应由用户要完成的交互决定。

## 用户要完成的工作

点击产品 Agent 后，在中间持续阅读产品文档，在右侧与该 Agent 讨论；文档来自其负责的知识库，讨论可以围绕正在阅读的内容继续进行。

已经明确的约束：

- 入口属于产品 Agent，右侧保留对话，中间留给文档画布。
- 画布承载可长期使用的产品知识，不能只显示本轮聊天临时生成的一张图。
- 需要考察 `cathrynlavery/diagram-design` 及适合现有项目的 GitHub 方案。

调研时尚未确认的体验选择：中间默认是长文阅读、自由拖动的无限画布，还是两种视图切换。其后用户已确认文档/全景双视图，当前契约见[产品说明](product-agent-canvas.md)。随附早期草图使用示例数据，不能作为真实后端验收证据。

## 现有产品约束

当前[知识体验契约](knowledge-experience.md)规定：知识页面负责阅读，团队成员通过对话调用内置知识管理员整理内容；知识库日常页面不提供资料录入、发布或废止表单。[设计事实源](../../agent-team-workbench/web/DESIGN.md)要求正文渲染复用通用组件，入库状态以服务端事实为准。

因此，文档画布应成为同一知识库在产品 Agent 工作区的阅读表面。聊天里说“已经更新”不能代替真实的版本和入库结果；草稿不能被展示为当前生效版本。直接编辑文档、拖线修改知识关系属于额外的写操作需求，需要明确后才纳入实现契约。

## 当前代码能支撑到哪里

以下为 `e34dcfc` 的只读源码结论，行号用于本基线定位。

| 触面 | 当前事实 | 接入建议与证据 |
|---|---|---|
| Agent 入口 | 配置页“对话”已经跳转 `/chat?agent=<真实 ID>` | 保留既有会话入口；见 [agents.page.tsx](../../agent-team-workbench/web/src/pages/agents.page.tsx)，479–482 行 |
| Chat 布局 | 当前是内部 Agent/会话侧栏与对话主区，成果面板按需在右侧出现 | 在 `ConversationPane` 外层布局插入知识主区并收窄对话；见 [chat.page.tsx](../../agent-team-workbench/web/src/pages/chat.page.tsx)，216–283、879–1023 行。需要同步处理原有成果面板的打开状态，避免三层右侧栏挤占正文 |
| Agent 识别 | `kind` 仅区分普通用户 Agent、任务协调器和知识管理员；`role=pm` 只是显示标签，且 Atlas 配置的角色与产品技能并不一致 | 工作区绑定稳定 `agent.id`；显示入口用明确配置的工作区能力，不能推断名字或提示词。见 [agent.go](../../agent-team-workbench/internal/domain/agent.go)，23–65 行；[atlas/agent.yaml](../../agent-team-workbench/agents/atlas/agent.yaml)，1–14 行 |
| 知识模型 | 已有 `owner_agent_id`、`visibility`、`scope`、`status`、`current_version` | 直接引用现有条目；见 [knowledge.ts](../../agent-team-workbench/web/src/api/knowledge.ts)，30–72 行；[0044 迁移](../../agent-team-workbench/migrations/0044_knowledge_library.sql)，8–57 行 |
| Agent 读取视角 | 前后端已有 `agent_id` 参数，可用于列表、正文、版本和关系；服务端验证是否允许读取该 Agent 的私有视图 | 见 [handlers_knowledge.go](../../agent-team-workbench/internal/httpapi/handlers_knowledge.go)，27–96 行；[前端 API helper](../../agent-team-workbench/web/src/api/knowledge.ts)，107–183 行。管理员/普通 viewer 的行为不可混同 |
| 可见性与归属 | SQL 读取允许 workspace 共享知识，或 requester 自己的私有知识 | `agent_id` 选择读取视角，不等于只返回该 Agent 创作的知识；见 [知识 SQL](../../agent-team-workbench/internal/persistence/sqlstore/knowledge.go)，293–342 行。具体归属筛选须单独确认 |
| 管理员与版本 | 知识管理员整理时继承原始 Agent 的 owner、visibility 和 scope；已有条目修订不允许改 owner/visibility | 见 [knowledge_librarian_curation.go](../../agent-team-workbench/internal/application/knowledge_librarian_curation.go)，18–79 行；[knowledge_publication.go](../../agent-team-workbench/internal/application/knowledge_publication.go)，41–88 行。画布不能把整理者当成产品知识的所有者 |
| 正文和图表 | `AgentOutput` 统一渲染，已有 Markdown/GFM/公式/Callout；Mermaid 懒加载、清洗与缓存 | 复用 [agent-output.tsx](../../agent-team-workbench/web/src/components/chat/agent-output.tsx)，19–97 行；[markdown-body.tsx](../../agent-team-workbench/web/src/components/chat/markdown-body.tsx)，240–394 行；[mermaid-diagram.tsx](../../agent-team-workbench/web/src/components/chat/mermaid-diagram.tsx)，23–91 行 |
| 完整知识阅读 | 已有文档列表、正文、目录、版本与来源布局；当前 `/knowledge` 使用共享 human view | 抽取可复用阅读区域，不复制整页；见 [knowledge.page.tsx](../../agent-team-workbench/web/src/pages/knowledge.page.tsx)，456–471、617–640 行，以及 [knowledge-reading.css](../../agent-team-workbench/web/src/pages/knowledge-reading.css) |
| 成果面板限制 | `ArtifactWorkspace` 只有成果元数据，没有 artifact 内容读取端点 | 不能直接把它改名当作文档画布；见 [artifact-workspace.tsx](../../agent-team-workbench/web/src/components/chat/artifact-workspace.tsx)，22–26 行 |
| 图与编辑器缺口 | 未引入 React Flow、Excalidraw、tldraw、Tiptap 或 Milkdown；没有知识 bulk graph API | 全景需要新增视图并控制逐条关系读取的 N+1 请求；若新增聚合端点，复用相同 workspace/agent/visibility 门禁。实际依赖与加载成本仍须 POC 验证 |

知识阅读和统一主题已合入当前基线，不需要重新合并旧的 `knowledge-reading` 分支。存在历史 Atlas canvas 远端分支，但它与当前主线分歧较大，不能作为本次直接合并的基线。

### “产品自己的知识库”应区分的两个含义

**归属集合**是该 Agent 负责或产出的知识。**读取视角**是该 Agent 被允许看到的知识，其中可能包含整个工作区的共享内容。现有 `agent_id` 实现了后者的权限视角；它不能自动代替前者的产品筛选。

当前 `KnowledgeListOptions` 没有 owner 字段，见 [domain/knowledge.go](../../agent-team-workbench/internal/domain/knowledge.go)，177–188 行。`visibility=private + agent_id=<产品 Agent>` 只能筛出它的私有知识，不能得到“它负责的全部共享与私有知识”。若选择严格归属集合，需要补充受控的 owner 查询语义，而非前端读取一页后按 owner 过滤。

目前只有 Owner/Admin 具备读取指定 Agent 私有视角所需的 `PermAgentWrite`；Operator/Approver/Viewer 不具备。此路径当前返回 422 `validation_failed`，不是通常的 403；见 [rbac.go](../../agent-team-workbench/internal/security/rbac.go)，23–40 行，以及 [problems.go](../../agent-team-workbench/internal/httpapi/problems.go)，82–87 行。UI 应依据已知权限和具体错误原因提供共享视图入口，不得显示为空库，也不能把所有 422 都解释为权限不足。

调研建议文档目录优先呈现“本 Agent 负责的文档”，需要引用其他团队知识时再明确扩展。后续已按 `owner_agent_id` 实现严格归属集合，并保留原有共享/私有读取门禁。`scope` 表达适用条件，不能当作私有权限。

## 建议的用户流程

1. 点击产品 Agent，打开其工作区；中间恢复上次阅读的知识文档，右侧恢复该 Agent 的对话。
2. 从文档目录或全景中的文档节点打开 PRD、业务规则、验收标准；长文正文始终能完整阅读。
3. 用户选中文档片段，点击“围绕这段讨论”。右侧明确显示本轮引用的文档、版本和选区；切换阅读文档不悄悄改动已经选定的引用。
4. 产品 Agent 结合引用回答，提出需求修订。需要保留的结论沿现有知识管理员流程整理，并返回可以核验的结果。
5. 服务端产生新版本后，画布提示“有新版本”，允许用户查看。阅读旧版本时保持原位置，不能被后台刷新强制跳走。

全景中的文档节点引用同一条知识及其版本；画布坐标、缩放和折叠属于展示状态。拖动文档位置不修改正文。知识关联是业务事实，不能直接把用户临时拉的一条线当作已发布的知识关系。

## 建议的交付顺序

### 第一阶段：文档与对话真正联动

在现有 Agent 聊天工作区加入知识目录和完整正文阅读，复用当前阅读与聊天组件。首先确定产品 Agent 识别方式和知识归属规则，再接入只读列表、详情、版本与来源。实现文档上下文引用，以及真实发布结果驱动的刷新提示。验收一条完整链路：打开文档 → 围绕段落讨论 → 知识管理员整理 → 查到实际新版本。

本阶段不依赖引入新的画布或富文本编辑器。它验证这项功能的核心价值：用户能持续看着产品知识与 Agent 协作。

### 第二阶段：在同一知识上提供全景

如果确认需要空间视图，使用 React Flow 试验文档摘要节点、需求关系、缩放、定位与点开阅读。先验证少量实际知识，随后验证有界分页与节点规模；不在每个节点里重复渲染长篇 PRD。全景与文档模式共享当前文档和知识引用。

### 第三阶段：按明确需求增加编辑与图表能力

图表生成可以接入 `diagram-design`，作为版本化文档的插图来源。自由手绘再引入白板编辑器；用户直接修改长文再评估 Markdown 或块编辑器。每种新增写操作都应具备服务端校验、版本冲突处理和失败恢复，不能以编辑器演示可用代替知识链路验收。

## 实施前的验收契约

以下是拟议验收项，未在本轮执行：

- 从产品 Agent 入口打开中间文档与右侧对话；非产品 Agent 的既有入口仍可正常使用。
- 没有相关知识时显示真实空态并保留对话；读取失败提供局部重试，不能显示为空库。
- 产品文档归属由服务端可校验的规则决定，不能只凭标题、`requirement` 类型或前端过滤判断“属于这个 Agent”。
- 选区引用绑定知识 ID、版本及锚点；快速切换 Agent、文档或工作区时，迟到响应不能写进新上下文。
- Agent 声称更新但服务端未发布时，画布不能显示成功；发布失败保留可恢复的草案和错误证据。
- 刷新、重新打开以及查看旧版本后，正文、来源和状态仍一致；废止内容不冒充当前生效知识。
- 全景仅按需加载有界文档集合与摘要；离开画布释放监听器和重型视图，不在每个节点挂载完整长文或完整编辑器。
- Markdown、图表和导入内容沿既有安全渲染边界处理；外部生成的 HTML 不能直接作为可信页面执行。
- 后续实现须运行改动触面的检查与真实 UI 路径；本轮仅有文档和交互草图，不将构建或模拟交互称作生产验收。

## 本轮证据与交付边界

- 已查证：当前路由、聊天布局、知识模型与权限、正文/图表组件、知识阅读合并状态；GitHub 候选的定位、许可与维护证据。
- 关键抽验：直接读取了 `diagram-design` README、React Flow MIT 许可、Excalidraw MIT 许可、tldraw 默认许可，以及 BlockSuite 默认分支提交。React Flow 官方 [Pro options](https://reactflow.dev/api-reference/react-flow#pro-options)允许通过 `hideAttribution` 隐藏归属标记，不能把订 Pro 当成必须条件。
- 维护数据复核入口：[React Flow npm](https://registry.npmjs.org/@xyflow/react)、[Excalidraw releases](https://github.com/excalidraw/excalidraw/releases)、[tldraw v5.4.0](https://github.com/tldraw/tldraw/releases/tag/v5.4.0)、[BlockSuite v0.22.4](https://github.com/toeverything/blocksuite/releases/tag/v0.22.4)、[Tiptap releases](https://github.com/ueberdosis/tiptap/releases)、[Milkdown releases](https://github.com/Milkdown/milkdown/releases)。这些是查证当日的快照，正式选定依赖时仍需固定版本。
- 文档检查：已校验本地链接目标与 Markdown 基本结构；本轮没有生产源码变更，不运行与文档无关的 Go/前端测试。
- 草图检查：HTML fragment、JavaScript 语法、渲染包装和无网络调用检查通过；主交互按本地事件实现。浏览器加载遇到本地连接拒绝，未完成截图级视觉验收，验证浏览器空间与临时服务已清理。草图右侧为静态对话示例，发送按钮禁用。
- 调研阶段没有执行 SDK 集成与真实交互。后续归属查询、文档引用、刷新和真实 Agent 验证见[验收记录](product-agent-canvas-acceptance.md)，不能把早期草图当作这些能力的证据。
- 决策记录见[架构记录](../../notes/implemented/architecture/2026-09-08-product-agent-canvas.md)。用户已明确授权将完成的实现合入 main；代码状态以 Git 历史和产品说明为准。
