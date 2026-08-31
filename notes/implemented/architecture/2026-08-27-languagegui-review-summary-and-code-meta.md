# LanguageGUI review-summary 与代码块 meta 决策

Status: implemented

## 决策与理由

`review-summary` 纳入 `languagegui/v1` 的结构化正文 union，专门承载评审或验证结果。它使用固定的 verdict、finding severity 和 check status 枚举，限制 findings/checks/next_steps 数量与文本规模，并要求至少一类列表有有效项；文件名只作为展示文本，URL 必须经过安全白名单。这样评审结果可以稳定回放，且不会把模型输出直接提升为可执行 UI。

普通 Markdown 与标准代码 fence 保持原有语义。只有明确的 `languagegui` fence 或 canonical `content_blocks` 才进入 ContentBlock parser；代码块支持 `filename=`/`title=`、行号高亮 `{3,5-7}` 和 `highlight=...` meta。代码块的 filename/title 可用于展示，但下载只取安全 basename；行号、高亮标记属于 chrome，不进入复制正文，不接受 class 或事件处理器。

## 放弃了什么

没有把 `review-summary` 设计成任意 Markdown/HTML 模板，也没有允许模型传入 CSS、React 组件名、事件处理器或可执行链接。没有让普通代码 fence 根据内容猜测为评审卡，避免误升级、历史回放漂移和恶意 payload 绕过结构化 parser。

## 复活条件

如果后端提供经过权限控制的评审结果资源并要求跳转到具体 finding，应新增独立的受信任引用字段和服务端鉴权链路，再扩展 `review-summary` 的 URL/动作语义；在此之前保持文件名展示、来源链接白名单和无动作 fallback。
