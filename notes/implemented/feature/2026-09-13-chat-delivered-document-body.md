# 对话交付型文档以正文承载

Status: implemented

## 决策与理由

用户要的产出本身就是一份文档时（需求规格、PRD、方案、评审报告、notes 决策记录），正文必须完整写在
消息的 Markdown 正文里；按仓库约定落盘只是留痕，不得替代正文。`output_contract.go` 的
languagegui/v1 追加段新增 Delivered documents 规则，并收紧 file 块口径：只报告文件存在，不得用来
把文档交给用户，禁用 `file:`/`data:`/`blob:` 等本地与协议 URL；[ContentBlock v1](../../../docs/frontend/chat-content-blocks-v1.md)
新增「交付型文档」一节，写明落盘与展示是两条通道、正文精简纪律不约束交付文档。

起因是一次真实会话：Nova 把需求文档写成 `notes/proposed/feature/2026-09-13-taskboard-title-search.md`
并在任务 worktree 提交，对话正文只给了同名的 file 块指针，块的 `url` 是 `file:///Users/...`。前端的
安全 URL 白名单（`isSafeContentUrl`）按规范丢弃非 http(s)/站内 URL，规范又规定「没有安全 URL 时只展示
元数据，不渲染假下载动作」，于是那行只剩文件名与大小，用户点不开也读不到。

## 放弃了什么

- **站内本地文件预览**（新增只读文件端点 + 抽屉渲染 Markdown）：本例文件在 agent 自建的 worktree
  `agent-work-taskboard-title-search`，落在工作区受信根 `agent-work` 之外；要覆盖它就得放宽执行上下文
  「端点不携带、不返回宿主绝对路径」的既有约束，代价与风险都高于把正文放进消息。
- **只给「复制路径」**：改不动「读不到内容」这件事，只是让不可用的行看起来可用。
- **文档入会话资料／知识库**：链路最长（产出 → 登记 → 阅读），而需求整理场景的产物本来就该在对话里读完。
- **放开 `file://` URL**：浏览器禁止 http 页面跳转 `file://`，放开只会渲染一个点了没反应的假动作。

## 复活条件

- 出现「必须在对话里读本地长文档」的真实需求（例如文档超出单条消息可承载的体量，或用户明确要站内阅读器）
  → 先定义可预览根边界（受信根与工作区 worktree 兄弟目录）和只读端点契约，再返工前端。
- 远程执行宿主接管文件读取后重新评估：那时「本地文件」对浏览器本就不存在，路径语义需要重定义。

## 验证

- `go test -count=1 ./internal/orchestrator/`：契约文本片段断言（本次新增 6 条）通过。
- `go test -count=1 ./internal/application/ -run TestSystemPromptInjection`：Chat system prompt 注入路径通过。
- `go build ./...`、`go vet`、gofmt 干净。
- 未执行：端到端验证「agent 真的按新契约把正文写进消息」需要重建后端并跑一轮新对话；历史消息不回溯改写，
  既有那条对话的文件指针仍是死行。
