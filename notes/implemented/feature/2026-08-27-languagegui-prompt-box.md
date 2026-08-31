# LanguageGUI PromptBox

Status: implemented

## 决策与理由

生产 Chat 把当前 textarea 升级为 LanguageGUI expanded PromptBox：保留真实队列、用量、
停止和发送行为，并增加附件/图片选择、文件拖放、语音转文字入口及 Library/Apps 面板。
附件在浏览器本地完成校验、预览和移除；在后端没有 upload/user-content-part 协议前，
存在附件时发送明确禁用并解释原因，绝不静默丢弃文件。

Library 只填充用户可见的 prompt 模板；Apps 面板只显示真实启用的 LanguageGUI 输出
协议和「尚未配置」状态，不改变 Runtime tool allowlist。语音只在浏览器原生转写能力
可用时启用，结果进入普通 draft；不声称向 Agent 发送音频。

## 放弃了什么

不把 File/Blob 序列化进 CreateRun JSON，不把本地路径或 base64 拼入用户 instruction，
也不展示没有 API 的上传进度、成功状态或外部 App 连接。这样会让用户误以为 Agent 已
读取文件，并造成数据库膨胀、凭据/隐私泄漏和队列重试不可幂等。

## 复活条件

【后端提供幂等 upload endpoint、受控存储、附件引用与 Runtime user-content-part 能力协商】
→ 把本地 pending attachment 升级为 uploaded reference → CreateRun 只携附件 ID，并补
上传失败回滚、队列重试、会话切换隔离和各 adapter 的 conformance 测试。

【服务端提供音频转写或 Runtime 声明 audio input supported】
→ 开放录音/音频附件 → 仍保留浏览器语音转文字作为低成本路径。
