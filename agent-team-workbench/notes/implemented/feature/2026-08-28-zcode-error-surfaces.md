# ZCode 对话错误三通道

Status: implemented

## 决策与理由

错误按可恢复范围分三条展示面：`tool.failed` 留在对应工具行，普通工具失败不自动升级为整轮错误；
`RunFailed + failure` 在 composer 上方显示模型/供应商错误 Banner；`cancelled/interrupted/user_stopped`
只进入停止状态与信息提示，不冒充模型故障。运行级失败不再复制进 transcript，避免同一错误既像工具行、
又像 assistant 正文、再像 Banner。

Banner 主文案最多 500 字符，完整 code/message 留给详情与复制；`retryable=true` 才显示真实 Retry，
调用现有 `/runs/{id}/commands/retry` 创建可追溯的新 Run 并继续订阅。当前协议没有 traceId/detail/恢复动作
字段，因此不伪造反馈、升级或切模型按钮。

## 放弃了什么

- 不把每个 tool failure 弹成全局 Banner；模型仍可能消费失败结果并继续下一 loop。
- 不给取消使用 error 红色或 Retry；主动中止与 provider failure 语义不同。
- 不做没有 API 支撑的假反馈/假 trace 链接。
