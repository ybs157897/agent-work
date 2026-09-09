# U03 原件读取验收

更新：2026-09-09。状态：已完成。接口、UI、真实 Agent 原件读取及原生续聊均已验证。

## 已验证的实现

原件通过现有 Chat 上传并保留字节，Run 请求使用 `input.source_refs`。服务端核对空间、会话、Agent 和 SHA-256，再将路径固化到本轮运行上下文。没有固定 Word/PPT 提取流程，也没有独立模型调用链。fresh 与原生 resume 均附加本轮资料路径，用户 instruction 保持原文。

## 真实接口与界面

- 49 项 HTTP/SQLite 检查：真实 DOCX/PPTX/MD 原件字节、同 key 重放、4 并发上传、空间/会话/Agent 拒绝、错误摘要、文件单独发送、Run 重放、后续轮复用。
- 11 项原件完整性检查：丢失或篡改时 metadata 明确不可用，下载和新 Run 拒绝，原记录保留；恢复原字节后可重新访问；客户端自带 path 字段被拒绝。
- 浏览器实际完成 Word 单独发送、PPT 入队、A/B/A 与刷新恢复、停止后显式继续队列。
- 浏览器实际阻断上传后，文字及 Markdown 原件保留，刷新后能再次发送。
- 浏览器模拟“服务端已创建 Run、响应丢失”，刷新后恢复同一 Run；SQLite 仅一条 Run 和一份 source。

发现并修复了并发上传错误冲突、首次 system prompt 漏掉续聊附件、Lost 恢复漏传 source_refs、执行启动前漏校验原件，以及跨空间深链接打开旧对话时的异步 hydration 问题。网络故障模拟、Mock 生命周期与真实模型结果分开记录。

## 检查

后端 build/vet、Chat source 接口及并发回归、协议合同检查、相关 race 通过。FileStore 与 runtime 包 race 通过。前端 `tsc -b`、124 文件/973 测试、完整 lint 和生产构建通过。大 chunk 提示为既有构建提示。

## 真实模型

最终使用已有 Kimi 模型配置，经隔离 DSH 网关运行于 web-idea 的真实项目目录。模型自行调用 10 组文件/命令工具，读取 DOCX 正文、页眉页脚，PPTX 幻灯片与备注，Markdown、两份真实源码和当前 Agent 的私有知识。32 项实际内容/摘要/工具/未修改检查通过。

损坏 DOCX 被如实报告为失败；Word 缩略图未读，报告明确披露。原件中的不可信指令未被当作产品确认或写入授权。源码 HEAD、dirty status 和 diff 的摘要前后一致。

服务端重启后，同一 DSH 会话又通过真实 read 工具读取第二轮新上传的 Markdown；独特标识只存在原件中，最终回复正确回显。该轮 session_ref 与上一轮相同，证明新附件在原生续聊中仍能交付。

最初 DeepSeek 通道返回经销商禁用 403；旧 OpenRouter 模型不存在，目录中的可用付费模型因账号额度不足失败。这些是外部通道失败，均保留在隔离 Run 历史中，没有折算为读取通过，没有购买额度或更改全局配置。

## 读取状态的边界

source shelf 显示“原件已保存 / 已交给 Agent / 原件不可用”；实际已读范围、部分读取与格式失败通过既有 Run 工具事件和最终报告持久呈现。没有从上传成功或任意 shell 命令自动推测“整个文件已读”。U04 将消费来源覆盖和分析版本；运行状态仍只由现有 Run/ModuleRunner 推进。

## 证据

- [接口与数据库检查](../../review/assets/u03-chat-sources/api-checks.json)、[完整性检查](../../review/assets/u03-chat-sources/integrity-checks.json)。
- [真实读取核验](../../review/assets/u03-chat-sources/real-reading-verification.json)、[工具与最终回复](../../review/assets/u03-chat-sources/real-selected-events.json)、[续聊新原件](../../review/assets/u03-chat-sources/real-resume-verification.json)。
- [真实界面](../../review/assets/u03-chat-sources/ui-real-reading.png)、[失败原文恢复](../../review/assets/u03-chat-sources/ui-upload-failure.json)、[响应丢失数据库核对](../../review/assets/u03-chat-sources/ui-response-loss-db.json)。
- [检查汇总](../../review/assets/u03-chat-sources/verification.json)。协议模拟、真实原件接口、UI 操作及真实模型分别记录。
